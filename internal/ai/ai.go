// Package ai talks to the model people bring their own key for: Anthropic,
// OpenAI, or anything that speaks OpenAI's chat API (Ollama, vLLM, LM Studio).
// Everything else in clusterview works without it.
package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Providers.
const (
	ProviderAnthropic = "anthropic"
	// ProviderOpenAI is OpenAI, or with a base URL any server that speaks its
	// chat completions API, such as a local model.
	ProviderOpenAI = "openai"
)

// DefaultAnthropicModel is used when no model is named.
const DefaultAnthropicModel = "claude-opus-5-5"

// Config picks the model. An empty Provider turns AI off.
type Config struct {
	Provider string
	Model    string
	// BaseURL points the openai provider at another server, such as
	// http://ollama:11434/v1.
	BaseURL string
	APIKey  string
	// Describe asks the model what unknown services are, from their image
	// names, so the map can say something about them.
	Describe bool
}

// ErrOff means no model is set up.
var ErrOff = errors.New("AI isn't set up: set ai.provider and a key (see the README)")

// Model answers one question with JSON matching a schema.
type Model interface {
	// JSON sends system and user and returns the model's JSON reply.
	JSON(ctx context.Context, req Request) ([]byte, error)
	// Name is "<provider> · <model>", shown under answers.
	Name() string
}

// Request is one question.
type Request struct {
	System string
	User   string
	// SchemaName names the schema, for providers that want one.
	SchemaName string
	Schema     map[string]any
	MaxTokens  int64
	// Effort is "low" for quick lookups; anything else means medium.
	Effort string
}

// New returns the configured model, or nil and no error when AI is off.
func New(cfg Config) (Model, error) {
	switch strings.ToLower(cfg.Provider) {
	case "", "none", "off":
		return nil, nil
	case ProviderAnthropic:
		if cfg.APIKey == "" {
			return nil, errors.New("ai.provider is anthropic but no API key is set (CLUSTERVIEW_AI_API_KEY or ANTHROPIC_API_KEY)")
		}
		return newAnthropic(cfg), nil
	case ProviderOpenAI:
		if cfg.Model == "" {
			return nil, errors.New("ai.provider is openai: name a model with ai.model (--ai-model)")
		}
		if cfg.APIKey == "" && cfg.BaseURL == "" {
			return nil, errors.New("ai.provider is openai but no API key is set (CLUSTERVIEW_AI_API_KEY or OPENAI_API_KEY)")
		}
		return newOpenAI(cfg), nil
	}
	return nil, fmt.Errorf("unknown ai.provider %q: use anthropic or openai", cfg.Provider)
}

// extractJSON returns the outermost JSON object in text. Local models
// sometimes wrap their answer in prose or a code fence despite being asked
// not to.
func extractJSON(text string) ([]byte, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil, fmt.Errorf("the model didn't answer in JSON: %q", truncate(text, 200))
	}
	return []byte(text[start : end+1]), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
