package auth

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// TestProviderDefaultFormatMatchesDocumentedTable locks the provider to
// upstream-format mapping published in docs/responses-tools.md and
// docs/responses-tools_CN.md. Those documents tell operators what to put in a
// responses-tools match block, and a mismatch there silently falls back to the
// convention instead of failing, so the table needs a test of its own.
func TestProviderDefaultFormatMatchesDocumentedTable(t *testing.T) {
	cases := []struct {
		provider string
		want     sdktranslator.Format
	}{
		{"codex", sdktranslator.FormatCodex},
		{"xai", sdktranslator.FormatCodex},
		{"meta", sdktranslator.FormatCodex},
		{"claude", sdktranslator.FormatClaude},
		{"gemini", sdktranslator.FormatGemini},
		{"vertex", sdktranslator.FormatGemini},
		{"aistudio", sdktranslator.FormatGemini},
		{"kimi", sdktranslator.FormatOpenAI},
		{"antigravity", sdktranslator.FormatAntigravity},
		{"openai-compatible-openrouter", sdktranslator.FormatOpenAI},
		{" CODEX ", sdktranslator.FormatCodex},
	}
	for _, testCase := range cases {
		if got := providerDefaultFormat(testCase.provider); got != testCase.want {
			t.Errorf("provider %q: format = %q, want %q", testCase.provider, got, testCase.want)
		}
	}
}
