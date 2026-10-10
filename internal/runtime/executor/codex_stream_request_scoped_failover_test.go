package executor

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// The upstream rejection observed in production: the Codex backend refuses the
// credential with a 400 invalid_prompt. It may or may not carry a top-level status,
// so both shapes must be covered by the request-scoped continue handling.
const (
	codexInvalidPromptEvent       = `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_prompt","message":"flagged as potentially violating our usage policy"},"sequence_number":2}`
	codexInvalidPromptStatusEvent = `{"type":"error","status":400,"error":{"type":"invalid_request_error","code":"invalid_prompt","message":"flagged as potentially violating our usage policy"},"sequence_number":2}`
)

func codexRequestScopedAuth(baseURL, action string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": baseURL, "api_key": "test"},
		Metadata: map[string]any{
			"request_scoped_errors": []config.RequestScopedErrorRule{
				{
					Status: http.StatusBadRequest,
					Match:  []string{"invalid_prompt"},
					Action: action,
				},
			},
		},
	}
}

// runCodexRequestScopedSession runs ExecuteStream bound to a named execution session and
// reports whether the upstream teardown was signalled to the downstream handler, exactly
// like executeWebsocketStreamInSession but with an auth carrying request-scoped rules.
// A non-empty action installs the invalid_prompt rule with that action; an empty action
// runs the same frames without any rule.
//
// The failure can be returned either as an ExecuteStream error (buffered bootstrap path)
// or as an in-stream chunk error (unbuffered path), so both are folded into err.
func runCodexRequestScopedSession(t *testing.T, buffering bool, action string, frames ...string) (bool, error) {
	t.Helper()

	server := codexWebsocketServerHoldingConnection(t, frames...)
	defer server.Close()

	auth := codexTestAuth(server.URL)
	if action != "" {
		auth = codexRequestScopedAuth(server.URL, action)
	}

	exec := NewCodexWebsocketsExecutor(codexBufferingConfig(buffering))
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}

	const sessionID = "request-scoped-session"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected a disconnect channel")
	}

	req, opts := codexWebsocketRequest()
	opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID}
	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err == nil && result != nil {
		_, err = drainChunks(result)
	}

	notified := false
	select {
	case <-disconnectCh:
		notified = true
	default:
	}
	return notified, err
}

// A rejection matched by a request-scoped continue rule is answered by retrying on another
// credential, so the downstream disconnect must not be published: publishing it closes the
// client websocket before the retry can deliver anything. Only the continue action is exercised
// here: this layer's predicate treats both continue actions identically, and the action
// distinction is pinned by TestCodexWSRequestScopedFlaggedCredentialFailsOverWithinDownstreamSession
// in test/codex_ws_request_scoped_failover_test.go.
func TestCodexWebsocketsExecutor_RequestScopedContinue_DoesNotNotifyDownstreamDisconnect(t *testing.T) {
	frames := map[string]string{
		"terminal_failure": codexInvalidPromptEvent,
		"upstream_error":   codexInvalidPromptStatusEvent,
	}
	for _, buffering := range []bool{true, false} {
		for name, frame := range frames {
			t.Run(fmt.Sprintf("buffering=%t/%s", buffering, name), func(t *testing.T) {
				notified, err := runCodexRequestScopedSession(t, buffering, cliproxyauth.RequestScopedActionContinue, frame)
				if err == nil {
					t.Fatal("expected the request-scoped rejection to fail the attempt so the conductor can retry")
				}
				if got := statusCodeFromTestError(t, err); got != http.StatusBadRequest {
					t.Fatalf("status code = %d, want %d", got, http.StatusBadRequest)
				}
				if notified {
					t.Fatal("request-scoped continue rejection must not signal a downstream disconnect")
				}
			})
		}
	}
}

// Without the rule the same rejection stays a request fault: nothing will rotate the
// credential, so the disconnect must keep being published and the client is told to retry.
// Only the unbuffered mode is exercised here, as the pin for the unbuffered notifying teardown;
// the buffered counterpart is asserted by
// TestCodexWebsocketsExecutor_BootstrapNonOverload_StillNotifiesDownstreamDisconnect in
// codex_stream_bootstrap_buffering_test.go.
func TestCodexWebsocketsExecutor_RequestScopedRuleAbsent_StillNotifiesDownstreamDisconnect(t *testing.T) {
	notified, err := runCodexRequestScopedSession(t, false, "", codexInvalidPromptEvent)
	if err == nil {
		t.Fatal("expected the unmatched rejection to fail the attempt")
	}
	if !notified {
		t.Fatal("a rejection with no request-scoped continue rule must still signal the downstream disconnect")
	}
}
