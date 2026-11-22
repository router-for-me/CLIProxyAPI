package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// expiredPickTestExecutor serves requests only with a fresh access token and
// counts refreshes. refreshErr makes every refresh fail.
type expiredPickTestExecutor struct {
	refreshes  atomic.Int32
	refreshErr error
}

func (e *expiredPickTestExecutor) Identifier() string { return "claude" }

func (e *expiredPickTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if auth.Metadata["access_token"] != "fresh-access-token" {
		return cliproxyexecutor.Response{}, &Error{Code: "unauthorized", Message: "stale token", HTTPStatus: http.StatusUnauthorized}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"id":"msg_ok"}`)}, nil
}

func (e *expiredPickTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if auth.Metadata["access_token"] != "fresh-access-token" {
		return nil, &Error{Code: "unauthorized", Message: "stale token", HTTPStatus: http.StatusUnauthorized}
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"message_stop"}` + "\n\n")}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *expiredPickTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.refreshes.Add(1)
	if e.refreshErr != nil {
		return nil, e.refreshErr
	}
	updated := auth.Clone()
	updated.Metadata["access_token"] = "fresh-access-token"
	updated.Metadata["expired"] = time.Now().Add(8 * time.Hour).Format(time.RFC3339)
	return updated, nil
}

func (e *expiredPickTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *expiredPickTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

// registerExpiredClaudeAuths registers n Claude OAuth credentials whose access
// tokens all expired a minute ago, as after a laptop sleeps past their expiry.
func registerExpiredClaudeAuths(t *testing.T, manager *Manager, model string, n int) {
	t.Helper()
	expired := time.Now().Add(-time.Minute).Format(time.RFC3339)
	for i := 0; i < n; i++ {
		a := &Auth{
			ID:         t.Name() + "-" + string(rune('a'+i)),
			Provider:   "claude",
			Status:     StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
			Metadata: map[string]any{
				"type":          "claude",
				"access_token":  "stale-access-token",
				"refresh_token": "refresh-token",
				"expired":       expired,
			},
		}
		registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: model}})
		id := a.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if _, errReg := manager.Register(WithSkipPersist(t.Context()), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
}

func expiredPickRequest(model string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FormatClaude,
			Metadata:     map[string]any{},
		}
}

func TestManagerExecute_AllAccessTokensExpired_RefreshesInsteadOfAuthUnavailable(t *testing.T) {
	const model = "claude-expired-pick-test"
	for _, stream := range []bool{false, true} {
		name := "execute"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.SetRetryConfig(3, 30*time.Second, 0)
			exec := &expiredPickTestExecutor{}
			manager.RegisterExecutor(exec)
			registerExpiredClaudeAuths(t, manager, model, 3)

			req, opts := expiredPickRequest(model)
			ctx := WithSkipPersist(t.Context())
			var errExec error
			if stream {
				errExec = executeStreamToResult(ctx, manager, req, opts)
			} else {
				_, errExec = manager.Execute(ctx, []string{"claude"}, req, opts)
			}
			if errExec != nil {
				t.Fatalf("expected the request to succeed after an on-demand refresh, got %v", errExec)
			}
			if got := exec.refreshes.Load(); got < 1 {
				t.Fatalf("expected at least one refresh, got %d", got)
			}
		})
	}
}

func TestManagerExecute_AllAccessTokensExpired_RefreshFailureStillUnavailable(t *testing.T) {
	const model = "claude-expired-pick-fail-test"
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	exec := &expiredPickTestExecutor{refreshErr: errors.New("dial tcp: lookup api.anthropic.com: no such host")}
	manager.RegisterExecutor(exec)
	registerExpiredClaudeAuths(t, manager, model, 2)

	req, opts := expiredPickRequest(model)
	_, errExec := manager.Execute(WithSkipPersist(t.Context()), []string{"claude"}, req, opts)
	if errExec == nil {
		t.Fatal("expected an error when every refresh fails")
	}
	// One refresh per credential for this request, not a refresh storm.
	if got := exec.refreshes.Load(); got > 2 {
		t.Fatalf("expected at most one refresh per credential, got %d", got)
	}
}

// expiredPickOutcome is the credential state a failed refresh leaves behind.
type expiredPickOutcome struct {
	Status           Status
	StatusMessage    string
	Unavailable      bool
	RequiresSignIn   bool
	Rejected         bool
	RefreshScheduled bool
	LastErrorCode    string
}

func expiredPickOutcomeOf(t *testing.T, manager *Manager, id string) expiredPickOutcome {
	t.Helper()
	auth, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %q not found", id)
	}
	out := expiredPickOutcome{
		Status:           auth.Status,
		StatusMessage:    auth.StatusMessage,
		Unavailable:      auth.Unavailable,
		RequiresSignIn:   auth.RequiresSignIn,
		Rejected:         auth.RejectedAccessToken != "",
		RefreshScheduled: !auth.NextRefreshAfter.IsZero(),
	}
	if auth.LastError != nil {
		out.LastErrorCode = auth.LastError.Code
	}
	return out
}

// A refresh that fails during a request must leave the credential exactly as
// the background refresher would: the on-demand path must not turn a retryable
// failure into a sign-in requirement.
func TestManagerExecute_AllAccessTokensExpired_FailedRefreshMatchesBackgroundRefresh(t *testing.T) {
	const model = "claude-expired-pick-differential-test"
	failures := map[string]error{
		"network":            errors.New("dial tcp: lookup api.anthropic.com: no such host"),
		"invalid_grant":      errors.New("token refresh failed with status 400: invalid_grant"),
		"unauthorized_grant": errors.New("token refresh failed with status 401: invalid_grant"),
		"upstream_5xx":       errors.New("token refresh failed with status 503: service unavailable"),
		"rate_limited":       errors.New("token refresh failed with status 429: rate limited"),
	}
	for name, refreshErr := range failures {
		t.Run(name, func(t *testing.T) {
			outcome := func(onDemand bool) expiredPickOutcome {
				manager := NewManager(nil, &RoundRobinSelector{}, nil)
				manager.SetRetryConfig(3, 30*time.Second, 0)
				manager.RegisterExecutor(&expiredPickTestExecutor{refreshErr: refreshErr})
				registerExpiredClaudeAuths(t, manager, model, 1)
				id := t.Name() + "-a"
				ctx := WithSkipPersist(t.Context())
				if onDemand {
					req, opts := expiredPickRequest(model)
					if _, errExec := manager.Execute(ctx, []string{"claude"}, req, opts); errExec == nil {
						t.Fatal("expected an error when the refresh fails")
					}
				} else {
					manager.refreshAuth(ctx, id)
				}
				return expiredPickOutcomeOf(t, manager, id)
			}
			background, onDemand := outcome(false), outcome(true)
			if background != onDemand {
				t.Fatalf("on-demand refresh left a different state:\n background: %+v\n on-demand:  %+v", background, onDemand)
			}
		})
	}
}

// Requests arriving together share refreshes: at most one per credential, and
// retry rounds never refresh a credential whose refresh just failed.
func TestManagerExecute_AllAccessTokensExpired_ConcurrentRequestsRefreshOncePerCredential(t *testing.T) {
	const model = "claude-expired-pick-concurrent-test"
	for _, failing := range []bool{false, true} {
		name := "success"
		if failing {
			name = "failure"
		}
		t.Run(name, func(t *testing.T) {
			manager := NewManager(nil, &RoundRobinSelector{}, nil)
			manager.SetRetryConfig(3, 30*time.Second, 0)
			exec := &expiredPickTestExecutor{}
			if failing {
				exec.refreshErr = errors.New("dial tcp: lookup api.anthropic.com: no such host")
			}
			manager.RegisterExecutor(exec)
			registerExpiredClaudeAuths(t, manager, model, 3)

			const requests = 24
			errs := make(chan error, requests)
			for i := 0; i < requests; i++ {
				go func() {
					req, opts := expiredPickRequest(model)
					_, errExec := manager.Execute(WithSkipPersist(t.Context()), []string{"claude"}, req, opts)
					errs <- errExec
				}()
			}
			for i := 0; i < requests; i++ {
				if errExec := <-errs; (errExec != nil) != failing {
					t.Fatalf("request error = %v, want failure=%v", errExec, failing)
				}
			}
			if got := exec.refreshes.Load(); got < 1 || got > 3 {
				t.Fatalf("expected one to three refreshes for three credentials, got %d", got)
			}
		})
	}
}

// gatedRefreshExecutor blocks its first refresh until released.
type gatedRefreshExecutor struct {
	expiredPickTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *gatedRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	if e.refreshes.Load() == 0 {
		close(e.started)
		<-e.release
	}
	return e.expiredPickTestExecutor.Refresh(ctx, auth)
}

// On wake the background refresher and the first request race. A request that
// finds a background refresh queued or running must wait for it and use its
// result, not fail and not refresh a second time.
func TestManagerExecute_AllAccessTokensExpired_UsesBackgroundRefreshInFlight(t *testing.T) {
	const model = "claude-expired-pick-inflight-test"
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(3, 30*time.Second, 0)
	exec := &gatedRefreshExecutor{started: make(chan struct{}), release: make(chan struct{})}
	manager.RegisterExecutor(exec)
	registerExpiredClaudeAuths(t, manager, model, 1)
	id := t.Name() + "-a"
	ctx := WithSkipPersist(t.Context())

	registered, _ := manager.GetByID(id)
	job := manager.markRefreshPending(nil, id, registered.RegistrationEpoch, time.Now())
	if job == nil {
		t.Fatal("expected a pending background refresh job")
	}
	backgroundDone := make(chan struct{})
	go func() {
		defer close(backgroundDone)
		if manager.beginRefreshJob(ctx, job) {
			_, _ = manager.refreshAuthForRequestAtEpoch(ctx, id, "", job.registrationEpoch)
		}
		manager.finishRefreshJob(job, time.Time{}, false)
	}()
	<-exec.started

	requestDone := make(chan error, 1)
	requestFinished := make(chan struct{})
	go func() {
		defer close(requestFinished)
		req, opts := expiredPickRequest(model)
		_, errExec := manager.Execute(ctx, []string{"claude"}, req, opts)
		requestDone <- errExec
	}()
	// The request must queue behind the running background refresh.
	waitForRefreshLockWaiters(t, manager, id, 1, requestFinished)
	close(exec.release)
	if errExec := <-requestDone; errExec != nil {
		t.Fatalf("expected the request to use the background refresh, got %v", errExec)
	}
	<-backgroundDone
	if got := exec.refreshes.Load(); got != 1 {
		t.Fatalf("expected the single background refresh to serve the request, got %d refreshes", got)
	}
}
