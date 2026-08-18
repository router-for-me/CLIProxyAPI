package executor

import (
	"context"
	"strings"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexCountTokensRejectsMalformedClaudeRequest(t *testing.T) {
	executor := NewCodexExecutor(nil)
	_, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"operator","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err == nil {
		t.Fatal("CountTokens() error = nil, want malformed request rejection")
	}
	if !strings.Contains(err.Error(), "message role") {
		t.Fatalf("error = %q, want message-role validation", err)
	}
}
