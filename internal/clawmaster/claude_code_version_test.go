package clawmaster

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// forgetClaudeCodeVersions clears the newest version served, for a test and after it.
func forgetClaudeCodeVersions(t *testing.T) {
	t.Helper()
	reset := func() {
		newestClaudeCodeMu.Lock()
		newestClaudeCode = claudeCodeVersion{}
		newestClaudeCodeMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

// The pool's own calls present Claude Code's interactive form at the newest version its clients run, and
// never an older one than measured; anything not Claude Code's own form on its major line is ignored.
func TestThePoolsOwnCallsPresentTheNewestClaudeCodeServed(t *testing.T) {
	forgetClaudeCodeVersions(t)
	if got := claudeCodeUserAgent(); got != "claude-cli/2.1.330 (external, cli)" {
		t.Fatalf("before any client: %q", got)
	}
	for _, ignored := range []string{
		"claude-cli/2.1.329 (external, cli)", // older than measured
		"claude-cli/3.0.0 (external, cli)",   // another major line
		"claude-cli/2.1.999",                 // not the full form
		"Mozilla/5.0 claude-cli/2.1.900 (external, cli)",
		"claude-cli/2.1.99999 (external, cli)",
	} {
		noteClaudeCodeUserAgent(ignored)
	}
	if got := claudeCodeUserAgent(); got != "claude-cli/2.1.330 (external, cli)" {
		t.Fatalf("after only ignored agents: %q", got)
	}
	noteClaudeCodeUserAgent("claude-cli/2.1.340 (external, sdk-cli)")
	noteClaudeCodeUserAgent("claude-cli/2.1.335 (external, claude-vscode, agent-sdk/0.3.220)")
	if got := claudeCodeUserAgent(); got != "claude-cli/2.1.340 (external, cli)" {
		t.Fatalf("after 2.1.340 then 2.1.335: %q", got)
	}
}

// A request the backend serves is what teaches it the version, and the usage poll then presents it.
func TestTheBackendLearnsTheClientsVersionAndThePollPresentsIt(t *testing.T) {
	forgetClaudeCodeVersions(t)
	authID := t.Name() + "-subscription"
	capture := &backendModelMapCapture{}
	manager := coreauth.NewManager(nil, &backendSelector{authID: authID, provider: "claude"}, nil)
	manager.SetRetryConfig(0, 0, 1)
	manager.RegisterExecutor(capture)
	registry.GetGlobalRegistry().RegisterClient(authID, "claude", []*registry.ModelInfo{{ID: backendAuthSelectionModel}})
	t.Cleanup(func() {
		manager.CloseExecutionSession(coreauth.CloseAllExecutionSessionsID)
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
	if _, err := manager.Register(t.Context(), &coreauth.Auth{ID: authID, Provider: "claude", Status: coreauth.StatusActive}); err != nil {
		t.Fatal(err)
	}
	handler := newBackendHandler(t.Context(), BackendOptions{Provider: "claude", UseRequestModel: true}, handlers.NewBaseAPIHandlers(&config.SDKConfig{}, manager))
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-test-model","messages":[{"role":"user","content":"hello"}]}`))
	request.Header.Set("User-Agent", "claude-cli/2.1.377 (external, sdk-cli)")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	var asked string
	_, _ = fetchClaudeUsage(t.Context(), &coreauth.Auth{ID: authID}, func(_ context.Context, _ *coreauth.Auth, req *http.Request) (*http.Response, error) {
		asked = req.Header.Get("User-Agent")
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})
	if asked != "claude-cli/2.1.377 (external, cli)" {
		t.Fatalf("the poll asked as %q, want the served client's version in the interactive form", asked)
	}
}
