package executor

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
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

// A CPA-injected fake metadata.user_id (cloak / fingerprint identity) must not
// outrank the caller's routing session when synthesizing the gateway header:
// the routing identity from the context wins over body extraction.
func TestApplyClaudeHeaders_OpenCodeSessionPrefersRoutingIdentityOverCloakedBody(t *testing.T) {
	t.Parallel()

	headers := http.Header{"X-Session-Affinity": []string{"ses_client"}}
	// Body as it looks after cloaking injected a per-credential fake user_id;
	// body extraction alone would return "claude:fake-credential-session".
	body := []byte(`{"model":"claude-opus-5","metadata":{"user_id":"user_2f3c85_session_fake-credential-session"},"messages":[{"role":"user","content":"hi"}]}`)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "test-key"}}

	req := newClaudeHeaderTestRequest(t, headers)
	parsed, errParse := url.Parse("https://opencode.ai/zen/go/v1/messages")
	if errParse != nil {
		t.Fatalf("parse: %v", errParse)
	}
	req.URL = parsed
	req = req.WithContext(util.WithSessionID(req.Context(), "affinity:ses_client"))

	if errApply := applyClaudeHeaders(req, auth, "test-key", true, nil, body, nil, headers, false, "session-id"); errApply != nil {
		t.Fatalf("applyClaudeHeaders() error = %v", errApply)
	}
	if got := req.Header.Get("x-opencode-session"); got != "ses_client" {
		t.Fatalf("x-opencode-session = %q, want routing session %q", got, "ses_client")
	}
}
