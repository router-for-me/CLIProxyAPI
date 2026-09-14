package executor

import (
	"net/http"
	"net/url"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestApplyClaudeHeaders_OpenCodeGatewaySessionHeader(t *testing.T) {
	t.Parallel()

	headers := http.Header{"X-Session-Affinity": []string{"ses_client"}}
	body := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	targets := map[string]string{
		"https://opencode.ai/zen/go/v1/messages": "ses_client",
		"https://api.anthropic.com/v1/messages":  "",
	}
	for target, want := range targets {
		parsed, errParse := url.Parse(target)
		if errParse != nil {
			t.Fatalf("parse %s: %v", target, errParse)
		}
		req := newClaudeHeaderTestRequest(t, headers)
		req.URL = parsed

		if errApply := applyClaudeHeaders(req, auth, "test-key", true, nil, body, nil, headers, false, "session-id"); errApply != nil {
			t.Fatalf("applyClaudeHeaders() error = %v", errApply)
		}
		if got := req.Header.Get("x-opencode-session"); got != want {
			t.Fatalf("%s: x-opencode-session = %q, want %q", target, got, want)
		}
	}
}
