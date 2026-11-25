package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAntigravityCountTokensUsesResolvedSelectionIDWhenMaxOutputTokensZero(t *testing.T) {
	const (
		baseModel     = "gemini-3.8-flash-high"
		resolvedModel = "gemini-3.8-flash-medium"
	)

	tests := []struct {
		name            string
		payload         string
		baseModel       string
		selectedModel   string
		maxOutputTokens int
		wantModel       string
	}{
		{
			name:            "remapped selection.ID with zero MaxOutputTokens sets request.model when absent",
			payload:         `{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			baseModel:       baseModel,
			selectedModel:   resolvedModel,
			maxOutputTokens: 0,
			wantModel:       "models/" + resolvedModel,
		},
		{
			name:            "remapped selection.ID with zero MaxOutputTokens overwrites preexisting unmapped request.model",
			payload:         `{"request":{"model":"models/` + baseModel + `","contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			baseModel:       baseModel,
			selectedModel:   resolvedModel,
			maxOutputTokens: 0,
			wantModel:       "models/" + resolvedModel,
		},
		{
			name:            "unmapped selection.ID with zero MaxOutputTokens preserves caller-supplied request.model",
			payload:         `{"request":{"model":"models/custom-tokenizer","contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			baseModel:       baseModel,
			selectedModel:   baseModel,
			maxOutputTokens: 0,
			wantModel:       "models/custom-tokenizer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(tc.payload)
			applyAntigravityCountTokensModelDoc(doc, tc.baseModel, tc.selectedModel, tc.maxOutputTokens)
			if got := doc.Get("request.model").String(); got != tc.wantModel {
				t.Fatalf("request.model = %q, want %q; doc=%s", got, tc.wantModel, doc.String())
			}
		})
	}

	t.Run("payload override rule remains last in CountTokens", func(t *testing.T) {
		auth := newIssue7865TestAuth("count-tokens-resolved-model-override")
		auth.Metadata["disable_cooling"] = true
		cfg := &config.Config{
			Payload: config.PayloadConfig{
				Override: []config.PayloadRule{
					{
						Models: []config.PayloadModelRule{
							{Name: "gemini-3-flash", Protocol: "antigravity"},
						},
						Params: map[string]any{
							"model": "models/custom-override-model",
						},
					},
				},
			},
		}
		got := captureAntigravityThinkingGroupRequest(
			t,
			cfg,
			auth,
			"gemini-3-flash",
			sdktranslator.FormatAntigravity,
			`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`,
			antigravityCountTokensPath,
		)
		if gotModel := got.Get("request.model").String(); gotModel != "models/custom-override-model" {
			t.Fatalf("request.model = %q, want %q; body=%s", gotModel, "models/custom-override-model", got.Raw)
		}
	})
}
