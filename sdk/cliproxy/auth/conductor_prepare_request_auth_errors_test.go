package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// credentialScopedStatusError mirrors antigravity's statusErr for a
// credential-scoped upstream failure (e.g. a 429 from project discovery) so
// isCredentialScopedError can classify it correctly.
type credentialScopedStatusError struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e credentialScopedStatusError) Error() string              { return e.msg }
func (e credentialScopedStatusError) StatusCode() int            { return e.code }
func (e credentialScopedStatusError) RetryAfter() *time.Duration { return e.retryAfter }
func (e credentialScopedStatusError) IsCredentialScoped() bool   { return true }

// countingPrepareExecutor returns a configurable error from PrepareRequestAuth
// and counts how many credentials were prepared so tests can assert that a
// request-scoped stop prevents rotation across the remaining pool.
type countingPrepareExecutor struct {
	prepareCalls atomic.Int32
	prepareErr   error
}

func (e *countingPrepareExecutor) Identifier() string { return "antigravity" }

func (e *countingPrepareExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	return auth == nil || auth.Metadata == nil || testStringValue(auth.Metadata["project_id"]) == ""
}

func (e *countingPrepareExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	e.prepareCalls.Add(1)
	if e.prepareErr != nil {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		return nil, e.prepareErr
	}
	updated := auth.Clone()
	if updated.Metadata == nil {
		updated.Metadata = make(map[string]any)
	}
	updated.Metadata["project_id"] = "prepared-project"
	return updated, nil
}

func (e *countingPrepareExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *countingPrepareExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *countingPrepareExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *countingPrepareExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *countingPrepareExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "http not implemented"}
}

// registerPrepareAuths registers three antigravity credentials sharing the same
// request-scoped error rules so every prepared credential evaluates the same
// rule set.
func registerPrepareAuths(t *testing.T, manager *Manager, model string, rules []internalconfig.RequestScopedErrorRule) []*Auth {
	t.Helper()
	auths := make([]*Auth, 0, 3)
	reg := registry.GetGlobalRegistry()
	for _, id := range []string{"prepare-auth-1", "prepare-auth-2", "prepare-auth-3"} {
		auth := &Auth{
			ID:       id,
			Provider: "antigravity",
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":          "token-" + id,
				"request_scoped_errors": rules,
			},
		}
		if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", id, errRegister)
		}
		reg.RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
		auths = append(auths, auth)
	}
	t.Cleanup(func() {
		for _, auth := range auths {
			reg.UnregisterClient(auth.ID)
		}
	})
	return auths
}

func TestPrepareRequestAuthScopedStopDoesNotRotate(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "gemini-3.1-pro"
	rules := []internalconfig.RequestScopedErrorRule{
		{Status: http.StatusForbidden, Match: []string{"TOS_VIOLATION"}, Action: "stop"},
	}
	executor := &countingPrepareExecutor{
		prepareErr: customStatusError{code: http.StatusForbidden, msg: `{"error":{"reason":"TOS_VIOLATION"}}`},
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerPrepareAuths(t, manager, model, rules)

	_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected Execute error on 403 prepare failure")
	}
	if got := executor.prepareCalls.Load(); got != 1 {
		t.Fatalf("prepare calls = %d, want 1 (request-scoped stop must prevent rotation)", got)
	}
}

func TestPrepareRequestAuthRetryAfterAndCredentialScopeRecorded(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "gemini-3.1-pro"
	retryAfter := 45 * time.Second
	executor := &countingPrepareExecutor{
		prepareErr: credentialScopedStatusError{
			code:       http.StatusTooManyRequests,
			msg:        "rate limited",
			retryAfter: &retryAfter,
		},
	}
	hook := &captureResultHook{done: make(chan Result, 1)}
	manager := NewManager(nil, nil, hook)
	manager.RegisterExecutor(executor)
	registerPrepareAuths(t, manager, model, nil)

	_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected Execute error on 429 prepare failure")
	}

	recorded := <-hook.done
	if recorded.RetryAfter == nil || *recorded.RetryAfter != retryAfter {
		t.Fatalf("recorded.RetryAfter = %v, want %v", recorded.RetryAfter, retryAfter)
	}
	// A 429 from PrepareRequestAuth is credential-scoped: the failing credential
	// must cool down so sibling models on the same credential are not retried.
	if !recorded.CredentialScope {
		t.Fatal("recorded.CredentialScope = false, want true for credential-scoped prepare failure")
	}
}

func TestPrepareRequestAuthInvalidRequestStopsRotation(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "gemini-3.1-pro"
	executor := &countingPrepareExecutor{
		prepareErr: customStatusError{code: http.StatusBadRequest, msg: `{"error":{"message":"invalid request"}}`},
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerPrepareAuths(t, manager, model, nil)

	_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected Execute error on 400 prepare failure")
	}
	if got := executor.prepareCalls.Load(); got != 1 {
		t.Fatalf("prepare calls = %d, want 1 (400 invalid request must stop rotation)", got)
	}
}

// TestPrepareRequestAuthUpstreamErrorPreferred verifies the upstream error from
// PrepareRequestAuth is preferred over the synthetic auth_unavailable pick error
// once the pool is exhausted.
func TestPrepareRequestAuthUpstreamErrorPreferred(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "gemini-3.1-pro"
	prepareErr := customStatusError{code: http.StatusForbidden, msg: "upstream tos violation"}
	executor := &countingPrepareExecutor{prepareErr: prepareErr}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerPrepareAuths(t, manager, model, nil)

	_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected Execute error")
	}
	// The surfaced error must be the upstream 403, not an auth_unavailable pick
	// failure that masks the real upstream rejection.
	var statusCoder interface{ StatusCode() int }
	if !errors.As(errExecute, &statusCoder) || statusCoder.StatusCode() != http.StatusForbidden {
		t.Fatalf("Execute() error = %T/%v, want upstream 403 status", errExecute, errExecute)
	}
}

// TestPrepareRequestAuthTransportErrorKeepsRotating verifies that a prepare
// failure with no upstream HTTP status (e.g. a transport/network error or a
// locally synthesized missing-project-id error) is not mistaken for a client
// request fault, so credential rotation continues to the next credential.
func TestPrepareRequestAuthTransportErrorKeepsRotating(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "gemini-3.1-pro"
	executor := &countingPrepareExecutor{
		// No HTTP status code: mirrors a transport/proxy failure or a locally
		// synthesized missing-project_id error, which must not be classified as
		// a client request fault.
		prepareErr: errors.New("proxy connect: connection refused"),
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerPrepareAuths(t, manager, model, nil)

	_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected Execute error")
	}
	// A transport-style failure is not a client request fault, so the manager
	// must keep rotating through the remaining credentials rather than stopping
	// after the first.
	if got := executor.prepareCalls.Load(); got != 3 {
		t.Fatalf("prepare calls = %d, want 3 (transport failure must keep rotating)", got)
	}
}

// TestPrepareRequestAuthStopPreservesRetryAfterHeader verifies that when a
// request-scoped stop rule matches an upstream 429 from PrepareRequestAuth, the
// returned stop error still exposes the trusted Retry-After header so the
// client receives the provider-supplied recovery deadline.
func TestPrepareRequestAuthStopPreservesRetryAfterHeader(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "gemini-3.1-pro"
	retryAfter := 45 * time.Second
	rules := []internalconfig.RequestScopedErrorRule{
		{Status: http.StatusTooManyRequests, Match: []string{"rate limited"}, Action: "stop"},
	}
	executor := &countingPrepareExecutor{
		prepareErr: credentialScopedStatusError{
			code:       http.StatusTooManyRequests,
			msg:        "rate limited",
			retryAfter: &retryAfter,
		},
	}
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	registerPrepareAuths(t, manager, model, rules)

	_, errExecute := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected Execute error on 429 stop prepare failure")
	}
	if got := executor.prepareCalls.Load(); got != 1 {
		t.Fatalf("prepare calls = %d, want 1 (request-scoped stop must prevent rotation)", got)
	}
	if hdr := SafeResponseHeaders(errExecute).Get("Retry-After"); hdr == "" {
		t.Fatalf("expected Retry-After header on stop error, headers=%v", SafeResponseHeaders(errExecute))
	}
}

type wrappedPrepareTransportTimeoutError struct {
	msg   string
	cause error
}

func (e wrappedPrepareTransportTimeoutError) Error() string     { return e.msg }
func (e wrappedPrepareTransportTimeoutError) StatusCode() int   { return 0 }
func (e wrappedPrepareTransportTimeoutError) ErrorCode() string { return ErrorCodeTransientTransport }
func (e wrappedPrepareTransportTimeoutError) Unwrap() error     { return e.cause }

type prepareNetTimeoutError struct{}

func (prepareNetTimeoutError) Error() string   { return "dial tcp: i/o timeout" }
func (prepareNetTimeoutError) Timeout() bool   { return true }
func (prepareNetTimeoutError) Temporary() bool { return true }

func TestPrepareRequestAuthMissingProjectIDAndNetTimeoutClassification_Issue7751(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	missingErr := credentialScopedStatusError{
		code: http.StatusServiceUnavailable,
		msg:  "antigravity auth missing project_id",
	}
	if isRequestInvalidError(missingErr) {
		t.Fatal("isRequestInvalidError(missing project_id 503) = true, want false")
	}
	missingResultErr := resultErrorFromError(missingErr)
	if shouldSkipCredentialCooldown(missingResultErr) {
		t.Fatalf("shouldSkipCredentialCooldown(%+v) = true, want false so broken credential is cooled", missingResultErr)
	}
	if missingResultErr.Code == ErrorCodeCredentialQuota || missingResultErr.Code == ErrorCodeTransientTransport {
		t.Fatalf("missingResultErr.Code = %q, want empty", missingResultErr.Code)
	}

	timeoutErr := wrappedPrepareTransportTimeoutError{
		msg:   "antigravity auth missing project_id: execute request: dial tcp: i/o timeout",
		cause: prepareNetTimeoutError{},
	}
	if isRequestInvalidError(timeoutErr) {
		t.Fatal("isRequestInvalidError(transport timeout) = true, want false")
	}
	if !isTransientTransportError(timeoutErr) {
		t.Fatal("isTransientTransportError(transport timeout) = false, want true")
	}
	var netErr net.Error
	if !errors.As(timeoutErr, &netErr) || !netErr.Timeout() {
		t.Fatal("expected errors.As to unwrap net.Error timeout")
	}
	timeoutResultErr := resultErrorFromError(timeoutErr)
	if timeoutResultErr.Code != ErrorCodeTransientTransport {
		t.Fatalf("timeoutResultErr.Code = %q, want %q", timeoutResultErr.Code, ErrorCodeTransientTransport)
	}
	if !shouldSkipCredentialCooldown(timeoutResultErr) {
		t.Fatalf("shouldSkipCredentialCooldown(%+v) = false, want true for transient transport timeout", timeoutResultErr)
	}

	internalDeadlineErr := wrappedPrepareTransportTimeoutError{
		msg:   "antigravity auth missing project_id: execute request: context deadline exceeded",
		cause: context.DeadlineExceeded,
	}
	if !isRequestRetryRoundError(internalDeadlineErr) {
		t.Fatal("isRequestRetryRoundError(transient_transport wrapping internal deadline) = false, want true")
	}
	if isRequestRetryRoundError(context.DeadlineExceeded) {
		t.Fatal("isRequestRetryRoundError(raw context.DeadlineExceeded) = true, want false")
	}
	if isRequestRetryRoundError(context.Canceled) {
		t.Fatal("isRequestRetryRoundError(raw context.Canceled) = true, want false")
	}
}
