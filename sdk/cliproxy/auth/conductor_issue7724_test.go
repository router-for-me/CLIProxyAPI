package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// issue7724CooldownDispatcher always answers the Home dispatch with a model_cooldown error so
// executeHomeOnce surfaces a homeDispatchRetryAfterError on every round.
type issue7724CooldownDispatcher struct {
	calls atomic.Int32
}

func (*issue7724CooldownDispatcher) HeartbeatOK() bool { return true }
func (d *issue7724CooldownDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(map[string]any{
		"error": map[string]any{
			"type":           "model_cooldown",
			"message":        "all credentials are cooling down",
			"retryable":      true,
			"retry_after_ms": int64(1),
		},
	})
}
func (*issue7724CooldownDispatcher) AbortAmbiguousDispatch() {}

type issue7724NoopExecutor struct {
	calls atomic.Int32
}

func (*issue7724NoopExecutor) Identifier() string { return "home-execution" }
func (e *issue7724NoopExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, nil
}
func (*issue7724NoopExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*issue7724NoopExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*issue7724NoopExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*issue7724NoopExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestIssue7724RetryRoundWaitedReturnsUnwrappedError covers the retryRoundWaited branch of
// executeHome: once a second Home cooldown round arrives after a wait, the surfaced error must be
// a caller-facing Home cooldown error without leaking internal boundary wrappers, matching the
// streaming parity path in ExecuteStream.
func TestIssue7724RetryRoundWaitedReturnsUnwrappedError(t *testing.T) {
	dispatcher := &issue7724CooldownDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(4, time.Second, 10)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	executor := &issue7724NoopExecutor{}
	manager.RegisterExecutor(executor)

	_, errExecute := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want cooldown error")
	}
	var marked *upstreamExecutionAttemptError
	if errors.As(errExecute, &marked) && marked != nil {
		t.Fatalf("Execute() error = %#v, internal upstreamExecutionAttemptError wrapper leaked to caller", errExecute)
	}
	var exhausted *homeRetryRoundExhaustedError
	if errors.As(errExecute, &exhausted) && exhausted != nil {
		t.Fatalf("Execute() error = %#v, internal homeRetryRoundExhaustedError wrapper leaked to caller", errExecute)
	}
	var cooldown *homeDispatchRetryAfterError
	if !errors.As(errExecute, &cooldown) || cooldown == nil {
		t.Fatalf("Execute() error = %#v, want homeDispatchRetryAfterError", errExecute)
	}
	if got := dispatcher.calls.Load(); got < 3 {
		t.Fatalf("dispatch calls = %d, want >= 3 (retry round wait must happen)", got)
	}
}

// issue7724MultiAuthDispatcher returns a fresh auth per dispatch call so executeHomeOnce can keep
// redispatching across credentials.
type issue7724MultiAuthDispatcher struct {
	calls atomic.Int32
}

func (*issue7724MultiAuthDispatcher) HeartbeatOK() bool { return true }
func (d *issue7724MultiAuthDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	index := d.calls.Add(1)
	if index > 8 {
		return nil, home.ErrAuthNotFound
	}
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:       "home-auth-" + string(rune('0'+index)),
		Provider: "home-execution",
		Status:   StatusActive,
		Metadata: map[string]any{"access_token": "token-value"},
	}})
}
func (*issue7724MultiAuthDispatcher) AbortAmbiguousDispatch() {}

// issue7724TurnErrorExecutor fails Execute with a non-retryable upstream websocket turn error.
type issue7724TurnErrorExecutor struct {
	calls atomic.Int32
}

func (*issue7724TurnErrorExecutor) Identifier() string { return "home-execution" }
func (e *issue7724TurnErrorExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	return cliproxyexecutor.Response{}, &cliproxyexecutor.UpstreamWebsocketTurnError{
		NotSent: true,
		Cause:   errors.New("upstream websocket disconnected"),
	}
}
func (*issue7724TurnErrorExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*issue7724TurnErrorExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*issue7724TurnErrorExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*issue7724TurnErrorExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestIssue7724WebsocketTurnErrorStopsRedispatch covers executeHomeOnce terminating on an
// upstream websocket turn error instead of redispatching to another credential, matching the
// executeStreamMixedOnce / executeStreamWithModelPool parity paths. The explicit
// IsUpstreamWebsocketTurnError check keeps the non-streaming Home path aligned even if the
// request-scoped classification of turn errors ever narrows.
func TestIssue7724WebsocketTurnErrorStopsRedispatch(t *testing.T) {
	dispatcher := &issue7724MultiAuthDispatcher{}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.SetRetryConfig(4, time.Second, 10)
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	executor := &issue7724TurnErrorExecutor{}
	manager.RegisterExecutor(executor)

	_, errExecute := manager.Execute(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a"}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("Execute() error = nil, want upstream websocket turn error")
	}
	if !cliproxyexecutor.IsUpstreamWebsocketTurnError(errExecute) {
		t.Fatalf("Execute() error = %#v, want upstream websocket turn error", errExecute)
	}
	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("Execute() calls = %d, want 1 (turn error must not redispatch to another credential)", got)
	}
	if got := dispatcher.calls.Load(); got != 1 {
		t.Fatalf("dispatch calls = %d, want 1 (turn error must not trigger another dispatch)", got)
	}
}

// issue7724ResultHook captures the Result payloads reported to the Home hook.
type issue7724ResultHook struct {
	results chan Result
}

func newIssue7724ResultHook() *issue7724ResultHook {
	return &issue7724ResultHook{results: make(chan Result, 8)}
}
func (*issue7724ResultHook) OnAuthRegistered(context.Context, *Auth) {}
func (*issue7724ResultHook) OnAuthUpdated(context.Context, *Auth)    {}
func (h *issue7724ResultHook) OnResult(_ context.Context, result Result) {
	select {
	case h.results <- result:
	default:
	}
}
func (h *issue7724ResultHook) await(t *testing.T) Result {
	t.Helper()
	select {
	case result := <-h.results:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("Home result hook did not run")
		return Result{}
	}
}

// issue7724SessionDispatcher returns one auth without any session id so selection.CanonicalSessionID
// stays empty and executeHomeOnce must fall back to ensureCanonicalSessionMetadata.
type issue7724SessionDispatcher struct{}

func (*issue7724SessionDispatcher) HeartbeatOK() bool { return true }
func (*issue7724SessionDispatcher) RPopAuth(_ context.Context, _ string, _ string, _ http.Header, _ int) ([]byte, error) {
	return json.Marshal(homeAuthDispatchResponse{Auth: Auth{
		ID:                "home-auth-session",
		Provider:          "home-execution",
		Status:            StatusActive,
		CredentialVersion: 7,
		RegistrationEpoch: 3,
		Metadata:          map[string]any{"access_token": "token-value"},
	}})
}
func (*issue7724SessionDispatcher) AbortAmbiguousDispatch() {}

// issue7724SuccessExecutor returns a completed payload and records the session id bound into the
// execution context.
type issue7724SuccessExecutor struct {
	sessionID string
}

func (*issue7724SuccessExecutor) Identifier() string { return "home-execution" }
func (e *issue7724SuccessExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.sessionID = util.SessionIDFromContext(ctx)
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"completed"}`)}, nil
}
func (*issue7724SuccessExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (*issue7724SuccessExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*issue7724SuccessExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte(`{"status":"completed","input_tokens":1}`)}, nil
}
func (*issue7724SuccessExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// TestIssue7724ResultParityAndCanonicalSession covers the executeHomeOnce Result contract:
// the reported Result must carry CompletedResponse plus the auth snapshot fields. The request
// carries no session signal at all, so selection.CanonicalSessionID stays "" and the
// ensureCanonicalSessionMetadata fallback branch runs (structural parity with executeMixedOnce);
// both the reported metadata and the execution context must then agree on the same canonical
// session id — here, the absence of one. The test invokes executeHome directly so no upstream
// Enrich pre-populates a derived session id.
func TestIssue7724ResultParityAndCanonicalSession(t *testing.T) {
	hook := newIssue7724ResultHook()
	manager := NewManager(nil, nil, hook)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(&issue7724SessionDispatcher{}, executionregistry.New(), 1)
	executor := &issue7724SuccessExecutor{}
	manager.RegisterExecutor(executor)

	// No session signal in payload, headers, or metadata: homeDispatchSessionIDs returns "", so
	// selection.CanonicalSessionID is "" and executeHomeOnce runs the else branch calling
	// ensureCanonicalSessionMetadata. The helper derives its canonical id from the same
	// extractSessionIDs source the dispatch already exhausted, so no id can appear here; the
	// assertions below lock in that the metadata and the execution context stay consistent
	// (both empty) rather than diverging or picking up a stale value.
	payload := []byte(`{"input":"hello"}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}
	_, errExecute := manager.executeHome(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a", Payload: payload}, opts, false)
	if errExecute != nil {
		t.Fatalf("executeHome() error = %v", errExecute)
	}

	result := hook.await(t)
	if !result.Success {
		t.Fatalf("Result.Success = false, result = %#v", result)
	}
	if !result.CompletedResponse {
		t.Fatalf("Result.CompletedResponse = false, want true for completed payload; result = %#v", result)
	}
	if result.AccessToken != "token-value" {
		t.Fatalf("Result.AccessToken = %q, want %q", result.AccessToken, "token-value")
	}
	if result.CredentialVersion != 7 {
		t.Fatalf("Result.CredentialVersion = %d, want 7", result.CredentialVersion)
	}
	if result.RegistrationEpoch != 3 {
		t.Fatalf("Result.RegistrationEpoch = %d, want 3", result.RegistrationEpoch)
	}
	canonicalID, _ := result.Options.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey].(string)
	if canonicalID != executor.sessionID && !(canonicalID == "" && executor.sessionID == "") {
		t.Fatalf("metadata canonical session id %q and execution context session id %q diverge", canonicalID, executor.sessionID)
	}
}

// TestIssue7724CountTokensResultSkipsCompletedResponse ensures the Home count_tokens path keeps
// CompletedResponse unset even when the counting payload carries a completed-looking status,
// matching executeCountMixedOnce which never reports CompletedResponse.
func TestIssue7724CountTokensResultSkipsCompletedResponse(t *testing.T) {
	hook := newIssue7724ResultHook()
	manager := NewManager(nil, nil, hook)
	manager.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(&issue7724SessionDispatcher{}, executionregistry.New(), 1)
	manager.RegisterExecutor(&issue7724SuccessExecutor{})

	payload := []byte(`{"input":"hello"}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}
	_, errCount := manager.executeHome(context.Background(), []string{"home-execution"}, cliproxyexecutor.Request{Model: "model-a", Payload: payload}, opts, true)
	if errCount != nil {
		t.Fatalf("executeHome(countTokens) error = %v", errCount)
	}

	result := hook.await(t)
	if !result.Success {
		t.Fatalf("Result.Success = false, result = %#v", result)
	}
	if result.CompletedResponse {
		t.Fatalf("Result.CompletedResponse = true for count_tokens, want false; result = %#v", result)
	}
	if result.AccessToken != "token-value" || result.CredentialVersion != 7 || result.RegistrationEpoch != 3 {
		t.Fatalf("count_tokens Result auth snapshot = %#v, want token/7/3", result)
	}
}
