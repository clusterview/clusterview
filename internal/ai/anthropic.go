package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"
)

type anthropicModel struct {
	client anthropic.Client
	model  string
}

func newAnthropic(cfg Config) *anthropicModel {
	model := cfg.Model
	if model == "" {
		model = DefaultAnthropicModel
	}
	opts := []option.RequestOption{
		option.WithAPIKey(cfg.APIKey),
		option.WithRequestTimeout(2 * time.Minute),
		option.WithMaxRetries(2),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &anthropicModel{client: anthropic.NewClient(opts...), model: model}
}

func (m *anthropicModel) Name() string { return "Anthropic · " + m.model }

// fallbackModels re-run a request the model declines on a model that
// accepts it, inside the same call. A Kubernetes log full of auth failures
// can look like an attack to a safety classifier.
var fallbackModels = map[string]bool{
	"claude-fable-5-1":  true,
	"claude-opus-5-5":   true,
	"claude-opus-5":     true,
	"claude-sonnet-5-5": true,
}

func (m *anthropicModel) JSON(ctx context.Context, req Request) ([]byte, error) {
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = 16000
	}
	effort := anthropic.BetaOutputConfigEffortMedium
	if req.Effort == "low" {
		effort = anthropic.BetaOutputConfigEffortLow
	}
	params := anthropic.BetaMessageNewParams{
		Model:     m.model,
		MaxTokens: maxTokens,
		System:    []anthropic.BetaTextBlockParam{{Text: req.System}},
		Messages:  []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(req.User))},
		OutputConfig: anthropic.BetaOutputConfigParam{
			Effort: effort,
			Format: anthropic.BetaJSONOutputFormatParam{Schema: req.Schema},
		},
	}
	if fallbackModels[m.model] {
		params.Betas = []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01}
		params.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
	}
	resp, err := m.client.Beta.Messages.New(ctx, params)
	if err != nil {
		var apiErr *anthropic.Error
		if errors.As(err, &apiErr) {
			return nil, fmt.Errorf("the Anthropic API answered %d: %s", apiErr.StatusCode, apiMessage(apiErr))
		}
		return nil, fmt.Errorf("couldn't reach Anthropic: %w", err)
	}
	switch resp.StopReason {
	case anthropic.BetaStopReasonRefusal:
		return nil, errors.New("the model declined to answer this one")
	case anthropic.BetaStopReasonMaxTokens:
		return nil, errors.New("the model's answer was cut off; try again")
	}
	var text strings.Builder
	for _, block := range resp.Content {
		if b, ok := block.AsAny().(anthropic.BetaTextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	return extractJSON(text.String())
}

// apiMessage is the error's message from Anthropic, without the request
// dump the SDK's Error() includes.
func apiMessage(e *anthropic.Error) string {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(e.RawJSON()), &body)
	if body.Error.Message != "" {
		return body.Error.Message
	}
	return truncate(e.Error(), 300)
}
