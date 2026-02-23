package executor

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/thinking"
	"github.com/tidwall/gjson"
)

func normalizeGeminiCLIModel(model string) string {
	model = strings.TrimSpace(strings.ToLower(model))
	switch {
	case strings.HasPrefix(model, "gemini-3") && strings.Contains(model, "-pro"):
		return "gemini-2.5-pro"
	case strings.HasPrefix(model, "gemini-3-flash"):
		return "gemini-2.5-flash"
	default:
		return model
	}
}

func TestNormalizeGeminiCLIModel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		model string
		want  string
	}{
		{name: "gemini3 pro alias maps to 2_5_pro", model: "gemini-3-pro", want: "gemini-2.5-pro"},
		{name: "gemini3 flash alias maps to 2_5_flash", model: "gemini-3-flash", want: "gemini-2.5-flash"},
		{name: "gemini31 pro alias maps to 2_5_pro", model: "gemini-3.1-pro", want: "gemini-2.5-pro"},
		{name: "non gemini3 model unchanged", model: "gemini-2.5-pro", want: "gemini-2.5-pro"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeGeminiCLIModel(tt.model)
			if got != tt.want {
				t.Fatalf("normalizeGeminiCLIModel(%q)=%q, want %q", tt.model, got, tt.want)
			}
		})
	}
}

func TestApplyGeminiThinkingForAttemptModelUsesRequestSuffix(t *testing.T) {
	t.Parallel()

	rawPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"ping"}]}]}}`)
	requestSuffix := thinking.ParseSuffix("gemini-2.5-pro(2048)")

	translated, err := applyGeminiThinkingForAttempt(rawPayload, requestSuffix, "gemini-2.5-pro", "gemini", "gemini-cli", "gemini-cli")
	if err != nil {
		t.Fatalf("applyGeminiThinkingForAttempt() error = %v", err)
	}

	budget := gjson.GetBytes(translated, "request.generationConfig.thinkingConfig.thinkingBudget")
	if !budget.Exists() || budget.Int() != 2048 {
		t.Fatalf("expected thinking budget 2048, got %q", budget.String())
	}
}
