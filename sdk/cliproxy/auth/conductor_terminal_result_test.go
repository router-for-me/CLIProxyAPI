package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestTerminalBootstrapRecordsCredentialCooldown(t *testing.T) {
	terminal := &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusTooManyRequests, Body: []byte(`{"error":"credential quota"}`)}
	failure := credentialTerminalResponseError{error: terminal}
	executor := &claudeCancellationTestExecutor{streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
		chunks := make(chan cliproxyexecutor.StreamChunk, 1)
		chunks <- cliproxyexecutor.StreamChunk{Err: failure}
		close(chunks)
		return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
	}}
	hook := &resultCaptureHook{}
	manager, auth, model := newClaudeCancellationTestManager(t, executor, hook)
	requireTerminalResponse(t, invokeTerminalResponse(manager, executor.Identifier(), model, "bootstrap"), terminal)
	results := hook.Results()
	current, _ := manager.GetByID(auth.ID)
	t.Logf("results=%d unavailable=%t retryInFuture=%t", len(results), current.Unavailable, current.NextRetryAfter.After(time.Now()))
	if len(results) != 1 {
		t.Errorf("result hooks=%d, want1", len(results))
	} else if !results[0].CredentialScope {
		t.Error("lost credential scope")
	}
	if !current.Unavailable || !current.NextRetryAfter.After(time.Now()) {
		t.Error("credential not cooled")
	}
}
