package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestHasCodexProvider(t *testing.T) {
	tests := []struct {
		name      string
		providers []string
		want      bool
	}{
		{"empty", []string{}, false},
		{"codex only", []string{"codex"}, true},
		{"codex uppercase", []string{"CODEX"}, true},
		{"codex with spaces", []string{"  codex  "}, true},
		{"other providers", []string{"openai", "anthropic"}, false},
		{"mixed with codex", []string{"openai", "codex"}, true},
		{"codex-api-key not matched", []string{"codex-api-key"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := hasCodexProvider(tt.providers)
			if got != tt.want {
				t.Errorf("hasCodexProvider(%v) = %v, want %v", tt.providers, got, tt.want)
			}
		})
	}
}

func TestBuildCodexFallbackRequest(t *testing.T) {
	tests := []struct {
		name             string
		model            string
		wantOK           bool
		wantModel        string
		wantDisplayModel string
	}{
		{
			name:             "gpt-5.5 falls back to gpt-5.4",
			model:            "gpt-5.5",
			wantOK:           true,
			wantModel:        "gpt-5.4",
			wantDisplayModel: "gpt-5.5",
		},
		{
			name:             "gpt-5.5 with suffix falls back preserving suffix",
			model:            "gpt-5.5(high)",
			wantOK:           true,
			wantModel:        "gpt-5.4(high)",
			wantDisplayModel: "gpt-5.5",
		},
		{
			name:   "gpt-5.4 has no fallback",
			model:  "gpt-5.4",
			wantOK: false,
		},
		{
			name:   "unknown model has no fallback",
			model:  "gpt-4o",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := cliproxyexecutor.Request{Model: tt.model}
			opts := cliproxyexecutor.Options{Metadata: map[string]any{}}
			fbReq, fbOpts, ok := buildCodexFallbackRequest(req, opts)
			if ok != tt.wantOK {
				t.Fatalf("buildCodexFallbackRequest() ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if fbReq.Model != tt.wantModel {
				t.Errorf("buildCodexFallbackRequest() model = %q, want %q", fbReq.Model, tt.wantModel)
			}
			displayModel := fbOpts.Metadata[cliproxyexecutor.CodexFallbackDisplayModelMetadataKey]
			if displayModel != tt.wantDisplayModel {
				t.Errorf("buildCodexFallbackRequest() display model = %q, want %q", displayModel, tt.wantDisplayModel)
			}
			requestedModel := fbOpts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
			wantRequestedModel := "gpt-5.4"
			if requestedModel != wantRequestedModel {
				t.Errorf("buildCodexFallbackRequest() requested model = %q, want %q", requestedModel, wantRequestedModel)
			}
		})
	}
}
