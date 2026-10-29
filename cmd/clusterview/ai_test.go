package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/clusterview/clusterview/internal/ai"
)

func TestOptionalModelSetup(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		cfg                               ai.Config
		wantModel, wantError, wantWarning bool
	}{
		{name: "unconfigured"},
		{name: "anthropic missing key", cfg: ai.Config{Provider: "anthropic"}, wantWarning: true},
		{name: "gemini missing key", cfg: ai.Config{Provider: "gemini", Model: "configured-model"}, wantWarning: true},
		{name: "openai missing key", cfg: ai.Config{Provider: "openai", Model: "configured-model"}, wantWarning: true},
		{name: "provider only", cfg: ai.Config{Provider: "openai"}, wantWarning: true},
		{name: "whitespace key", cfg: ai.Config{Provider: "gemini", Model: "configured-model", APIKey: " \n\t"}, wantWarning: true},
		{name: "explicit cloud URL", cfg: ai.Config{Provider: "openai", Model: "configured-model", BaseURL: "https://api.openai.com/v1/"}, wantWarning: true},
		{name: "keyless local endpoint", cfg: ai.Config{Provider: "openai", Model: "local-model", BaseURL: "http://127.0.0.1:11434/v1"}, wantModel: true},
		{name: "configured cloud", cfg: ai.Config{Provider: "gemini", Model: "configured-model", APIKey: "test-key"}, wantModel: true},
		{name: "unknown provider", cfg: ai.Config{Provider: "unknown"}, wantError: true},
		{name: "openai missing model with key", cfg: ai.Config{Provider: "openai", APIKey: "test-key"}, wantError: true},
		{name: "gemini missing model with key", cfg: ai.Config{Provider: "gemini", APIKey: "test-key"}, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			model, err := optionalModel(tc.cfg, slog.New(slog.NewTextHandler(&logs, nil)))
			if (err != nil) != tc.wantError || (model != nil) != tc.wantModel {
				t.Fatalf("model enabled=%v error=%v, want enabled=%v error=%v", model != nil, err, tc.wantModel, tc.wantError)
			}
			if strings.Contains(logs.String(), "AI disabled") != tc.wantWarning {
				t.Errorf("unexpected warning: %s", logs.String())
			}
			if strings.Contains(logs.String(), "test-key") {
				t.Fatal("credential appeared in logs")
			}
		})
	}
}
