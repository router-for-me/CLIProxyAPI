package auth

import (
	"context"
	"errors"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestHomeWebsocketReusesCanonicalModelSelection(t *testing.T) {
	dispatcher := &retainingHomeExecutionDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.RegisterExecutor(&retainingHomeExecutionExecutor{})
	t.Cleanup(func() { manager.CloseExecutionSession("canonical-model-session") })

	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.ExecutionSessionMetadataKey: "canonical-model-session",
		cliproxyexecutor.PinnedAuthMetadataKey:       "home-auth",
	}}
	for _, model := range []string{"model-a(high)", "model-a"} {
		if _, errExecute := manager.Execute(ctx, []string{"home-execution"}, cliproxyexecutor.Request{Model: model}, opts); errExecute != nil {
			t.Fatalf("Execute(%q) error = %v", model, errExecute)
		}
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("Home RPOP calls = %d, want 1 for one credential and canonical model", got)
	}
}

func TestAuditHomeCreditsFailClosed(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.auths["local-credits"] = &Auth{ID: "local-credits", Provider: "antigravity", Status: StatusActive}

	_, _, errExecute := manager.tryAntigravityCreditsExecute(context.Background(), cliproxyexecutor.Request{Model: "claude-test"}, cliproxyexecutor.Options{})
	assertHomeCreditsFallbackUnsupported(t, errExecute)

	_, _, errStream := manager.tryAntigravityCreditsExecuteStream(context.Background(), cliproxyexecutor.Request{Model: "claude-test"}, cliproxyexecutor.Options{Stream: true})
	assertHomeCreditsFallbackUnsupported(t, errStream)
}

func assertHomeCreditsFallbackUnsupported(t *testing.T, err error) {
	t.Helper()
	var authErr *Error
	if !errors.As(err, &authErr) || authErr.Code != "home_fallback_unsupported" {
		t.Fatalf("error = %v, want home_fallback_unsupported", err)
	}
}
