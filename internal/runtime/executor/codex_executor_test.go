package executor

import (
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexPrepareRequestPreservesMultipartContentType(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/backend-api/transcribe", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary=test-boundary")

	executor := NewCodexExecutor(nil)
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{
			"account_id": "account-123",
			"email":      "user@example.com",
		},
	}

	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest() error = %v", err)
	}

	if got := req.Header.Get("Content-Type"); got != "multipart/form-data; boundary=test-boundary" {
		t.Fatalf("Content-Type = %q, want %q", got, "multipart/form-data; boundary=test-boundary")
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "account-123" {
		t.Fatalf("Chatgpt-Account-Id = %q, want %q", got, "account-123")
	}
	if got := req.Header.Get("Originator"); got != "codex_cli_rs" {
		t.Fatalf("Originator = %q, want %q", got, "codex_cli_rs")
	}
	if got := req.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("Accept = %q, want %q", got, "application/json")
	}
}
