package signature_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/signature"
)

func TestSanitizeClaudeMessagesSignaturesForModel_Empty(t *testing.T) {
	out, _ := signature.SanitizeClaudeMessagesSignaturesForModel([]byte(`{"messages":[]}`), "claude-sonnet-4-5")
	if len(out) == 0 {
		t.Fatal("expected payload back")
	}
}

func TestSignatureProviderFromModelName_Claude(t *testing.T) {
	if got := signature.SignatureProviderFromModelName("claude-opus-4-5"); got != signature.SignatureProviderClaude {
		t.Fatalf("got %q, want claude", got)
	}
}
