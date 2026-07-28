package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Usage sinks must report the identity the request was routed on, so a usage
// timeline and a routing decision can never disagree about the session.
func TestContextCarriesTheRoutedSessionToUsageSinks(t *testing.T) {
	req := cliproxyexecutor.Request{Payload: []byte(`{"prompt_cache_key":"cache-1"}`)}
	opts := cliproxyexecutor.Options{Headers: http.Header{
		"User-Agent":               {"claude-cli/2.1.193 (external, sdk-cli)"},
		"X-App":                    {"cli"},
		"Anthropic-Beta":           {"claude-code-20250219"},
		"X-Claude-Code-Session-Id": {"cc-1"},
		"X-Gateway-Thread-Id":      {"thread-1"},
	}}
	_, opts = cliproxysession.Enrich(req, opts)

	ctx := contextWithRequestedModelAlias(context.Background(), opts, "claude-test")

	info := coreusage.SessionInfoFromContext(ctx)
	if info.ID != "claude:cc-1" {
		t.Fatalf("session ID = %q, want claude:cc-1", info.ID)
	}
	if info.Source != cliproxysession.SourceClientNativeHeader {
		t.Fatalf("session source = %q, want %q", info.Source, cliproxysession.SourceClientNativeHeader)
	}
	if info.Confidence != cliproxysession.ConfidenceHigh {
		t.Fatalf("session confidence = %q, want %q", info.Confidence, cliproxysession.ConfidenceHigh)
	}
	if info.Scope != cliproxysession.ScopeSession {
		t.Fatalf("session scope = %q, want %q", info.Scope, cliproxysession.ScopeSession)
	}
	if info.ClientType != cliproxysession.ClientTypeClaudeCode {
		t.Fatalf("client type = %q, want %q", info.ClientType, cliproxysession.ClientTypeClaudeCode)
	}
	if info.ThreadID != "thread-1" {
		t.Fatalf("thread ID = %q, want thread-1", info.ThreadID)
	}
}

func TestContextOmitsSessionWhenNoneWasResolved(t *testing.T) {
	ctx := contextWithRequestedModelAlias(context.Background(), cliproxyexecutor.Options{}, "gpt-test")

	if info := coreusage.SessionInfoFromContext(ctx); !info.IsZero() {
		t.Fatalf("session info = %#v, want zero value", info)
	}
}
