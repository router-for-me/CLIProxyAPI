package precompact

import (
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestSupported(t *testing.T) {
	if !Supported(sdktranslator.FormatClaude) {
		t.Fatal("claude should be supported")
	}
	if !Supported(sdktranslator.FormatOpenAI) {
		t.Fatal("openai should be supported")
	}
	if Supported(sdktranslator.FormatGemini) {
		t.Fatal("gemini must be out of scope")
	}
}

func TestCountClaude(t *testing.T) {
	body := []byte(`{"model":"claude-3","messages":[{"role":"user","content":"hello world"}]}`)
	n, err := Count(sdktranslator.FormatClaude, "claude-3", body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n <= 0 {
		t.Fatalf("expected positive token count, got %d", n)
	}
}

func TestCountUnsupportedFormat(t *testing.T) {
	_, err := Count(sdktranslator.FormatGemini, "gemini-pro", []byte(`{}`))
	if err == nil {
		t.Fatal("expected error for unsupported format")
	}
}
