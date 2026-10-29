package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// openAIModel speaks the chat completions API, which OpenAI and most local
// model servers (Ollama, vLLM, LM Studio, llama.cpp) offer.
type openAIModel struct {
	http    *http.Client
	baseURL string
	apiKey  string
	model   string
	label   string
}

func newOpenAI(cfg Config) *openAIModel {
	base := strings.TrimRight(cfg.BaseURL, "/")
	// A local model on modest hardware can take minutes over a long prompt.
	timeout := 5 * time.Minute
	if base == "" {
		base = "https://api.openai.com/v1"
		timeout = 90 * time.Second
	}
	return &openAIModel{http: &http.Client{Timeout: timeout}, baseURL: base, apiKey: cfg.APIKey, model: cfg.Model}
}

func (m *openAIModel) Name() string {
	if m.label != "" {
		return m.label
	}
	if strings.HasPrefix(m.baseURL, "https://api.openai.com") {
		return "OpenAI · " + m.model
	}
	return m.model
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func (m *openAIModel) JSON(ctx context.Context, req Request) ([]byte, error) {
	body := map[string]any{
		"model": m.model,
		"messages": []chatMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   req.SchemaName,
				"schema": req.Schema,
				"strict": true,
			},
		},
	}
	if req.MaxTokens > 0 {
		if strings.HasPrefix(m.baseURL, "https://api.openai.com/") {
			body["max_completion_tokens"] = req.MaxTokens
		} else {
			body["max_tokens"] = req.MaxTokens
		}
	}
	text, status, err := m.send(ctx, body)
	// Older local servers don't know json_schema; plain JSON mode still works,
	// with the schema spelled out in the prompt.
	if err != nil && status == http.StatusBadRequest {
		schema, _ := json.Marshal(req.Schema)
		body["response_format"] = map[string]any{"type": "json_object"}
		body["messages"] = []chatMessage{
			{Role: "system", Content: req.System + "\n\nAnswer with one JSON object matching this JSON schema:\n" + string(schema)},
			{Role: "user", Content: req.User},
		}
		text, _, err = m.send(ctx, body)
	}
	if err != nil {
		return nil, err
	}
	return extractJSON(text)
}

// send posts once more when the server was busy, failed or didn't answer
// in time, as the Anthropic SDK does.
func (m *openAIModel) send(ctx context.Context, body map[string]any) (string, int, error) {
	text, status, err := m.post(ctx, body)
	if err != nil && ctx.Err() == nil && (status == 0 || status == http.StatusTooManyRequests || status >= 500) {
		if !sleep(ctx, retryWait) {
			return "", status, ctx.Err()
		}
		return m.post(ctx, body)
	}
	return text, status, err
}

// retryWait is the pause before the second try.
var retryWait = 2 * time.Second

func (m *openAIModel) post(ctx context.Context, body map[string]any) (string, int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return "", 0, err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		return "", 0, err
	}
	r.Header.Set("Content-Type", "application/json")
	if m.apiKey != "" {
		r.Header.Set("Authorization", "Bearer "+m.apiKey)
	}
	resp, err := m.http.Do(r)
	if err != nil {
		return "", 0, fmt.Errorf("couldn't reach %s: %w", m.baseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", resp.StatusCode, err
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &out)
	if resp.StatusCode != http.StatusOK {
		msg := truncate(strings.TrimSpace(string(data)), 300)
		if out.Error != nil && out.Error.Message != "" {
			msg = out.Error.Message
		}
		return "", resp.StatusCode, fmt.Errorf("the model server answered %d: %s", resp.StatusCode, msg)
	}
	if len(out.Choices) == 0 {
		return "", resp.StatusCode, errors.New("the model server sent no answer")
	}
	c := out.Choices[0]
	switch {
	case c.Message.Refusal != "":
		return "", resp.StatusCode, errors.New("the model declined to answer this one")
	case c.FinishReason == "length":
		return "", resp.StatusCode, errors.New("the model's answer was cut off; try again")
	}
	return c.Message.Content, resp.StatusCode, nil
}
