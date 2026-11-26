package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7658CreditsExecutor struct {
	mu           sync.Mutex
	normalCalls  []string
	creditsCalls []string
	executeFn    func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	streamFn     func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	prepareCheck func(auth *Auth) bool
	prepareFn    func(ctx context.Context, auth *Auth) (*Auth, error)
}

func (e *issue7658CreditsExecutor) Identifier() string { return "antigravity" }

func (e *issue7658CreditsExecutor) recordCall(ctx context.Context, authID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if AntigravityCreditsRequested(ctx) {
		e.creditsCalls = append(e.creditsCalls, authID)
		return
	}
	e.normalCalls = append(e.normalCalls, authID)
}

func (e *issue7658CreditsExecutor) CreditsCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.creditsCalls))
	copy(out, e.creditsCalls)
	return out
}

func (e *issue7658CreditsExecutor) NormalCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.normalCalls))
	copy(out, e.normalCalls)
	return out
}

func (e *issue7658CreditsExecutor) ShouldPrepareRequestAuth(auth *Auth) bool {
	if e.prepareCheck != nil {
		return e.prepareCheck(auth)
	}
	return false
}

func (e *issue7658CreditsExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	if e.prepareFn != nil {
		return e.prepareFn(ctx, auth)
	}
	return auth, nil
}

func (e *issue7658CreditsExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall(ctx, auth.ID)
	if e.executeFn != nil {
		return e.executeFn(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok-" + auth.ID)}, nil
}

func (e *issue7658CreditsExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordCall(ctx, auth.ID)
	if e.streamFn != nil {
		return e.streamFn(ctx, auth, req, opts)
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte("ok-" + auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *issue7658CreditsExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *issue7658CreditsExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "CountTokens not implemented"}
}

func (e *issue7658CreditsExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

type issue7658TransientRateLimitErr struct {
	status int
	msg    string
}

func (e issue7658TransientRateLimitErr) Error() string              { return e.msg }
func (e issue7658TransientRateLimitErr) StatusCode() int            { return e.status }
func (e issue7658TransientRateLimitErr) IsTransientRateLimit() bool { return true }

type issue7658StatusErr struct {
	status     int
	code       string
	msg        string
	retryAfter *time.Duration
}

func (e issue7658StatusErr) Error() string { return e.msg }
func (e issue7658StatusErr) StatusCode() int {
	return e.status
}
func (e issue7658StatusErr) ErrorCode() string {
	return e.code
}
func (e issue7658StatusErr) RetryAfter() *time.Duration {
	return e.retryAfter
}

func setupIssue7658CreditsManager(t *testing.T, prefix, model string, count int, cfg *internalconfig.Config, maxRetryCredentials int, authSetup func(idx int, auth *Auth)) (*Manager, *issue7658CreditsExecutor, []string) {
	t.Helper()
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	cfg.QuotaExceeded.AntigravityCredits = true

	manager := NewManager(nil, &FillFirstSelector{}, nil)
	manager.SetConfig(cfg)
	manager.SetRetryConfig(0, 0, maxRetryCredentials)
	exec := &issue7658CreditsExecutor{}
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	authIDs := make([]string, 0, count)
	for i := 1; i <= count; i++ {
		id := fmt.Sprintf("%s-%d", prefix, i)
		authIDs = append(authIDs, id)
		auth := &Auth{
			ID:         id,
			Provider:   "antigravity",
			Status:     StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		}
		if authSetup != nil {
			authSetup(i, auth)
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", id, errRegister)
		}
		reg.RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(id)
			SetAntigravityCreditsHint(id, AntigravityCreditsHint{})
		})
	}
	return manager, exec, authIDs
}

func TestAntigravityCreditsExecute_StopsOnRequestScopedErrorRules(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "claude-sonnet-4-6"

	for _, stream := range []bool{false, true} {
		modeName := "execute"
		if stream {
			modeName = "stream"
		}
		t.Run(modeName+"/stop_on_validation_required", func(t *testing.T) {
			cfg := &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"antigravity": {
						{
							Status: 403,
							Match:  []string{"VALIDATION_REQUIRED"},
							Action: "stop",
						},
					},
				},
			}
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-stop-"+modeName, model, 3, cfg, 0, nil)
			exec.executeFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if !AntigravityCreditsRequested(ctx) {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"}
			}
			exec.streamFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if !AntigravityCreditsRequested(ctx) {
					return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				return nil, &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"}
			}

			var errRun error
			if stream {
				_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			} else {
				_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if errRun == nil || !strings.Contains(errRun.Error(), "VALIDATION_REQUIRED") {
				t.Fatalf("err = %v, want VALIDATION_REQUIRED", errRun)
			}
			if isRequestStopError(errRun) {
				t.Fatalf("returned error must unwrap internal requestStopError wrapper: %T", errRun)
			}
			if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
				t.Fatalf("creditsCalls = %v, want [%s] (must stop without sweeping pool)", calls, authIDs[0])
			}
			a1, _ := manager.GetByID(authIDs[0])
			if a1.LastError != nil && strings.Contains(a1.LastError.Message, "VALIDATION_REQUIRED") {
				t.Fatalf("auth %s LastError overwritten on action=stop: %+v", authIDs[0], a1.LastError)
			}
			if a1.NextRetryAfter.Sub(time.Now()) > 5*time.Minute {
				t.Fatalf("auth %s received 403 cooldown on action=stop: nextRetry=%v", authIDs[0], a1.NextRetryAfter)
			}
		})

		t.Run(modeName+"/stop_on_validation_required_preserves_zero_auth_cooldown", func(t *testing.T) {
			cfg := &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"antigravity": {
						{
							Status: 403,
							Match:  []string{"VALIDATION_REQUIRED"},
							Action: "stop",
						},
					},
				},
			}
			cooldownUntil := time.Now().Add(15 * time.Minute)
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-stop0-"+modeName, model, 3, cfg, 0, func(_ int, auth *Auth) {
				auth.ModelStates = map[string]*ModelState{
					model: {
						Unavailable:    true,
						NextRetryAfter: cooldownUntil,
						Quota: QuotaState{
							Exceeded:      true,
							NextRecoverAt: cooldownUntil,
						},
					},
				}
			})
			exec.executeFn = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"}
			}
			exec.streamFn = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				return nil, &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"}
			}

			var errRun error
			if stream {
				_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			} else {
				_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if errRun == nil || !strings.Contains(errRun.Error(), "VALIDATION_REQUIRED") {
				t.Fatalf("err = %v, want VALIDATION_REQUIRED", errRun)
			}
			if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
				t.Fatalf("creditsCalls = %v, want [%s]", calls, authIDs[0])
			}
			a1, _ := manager.GetByID(authIDs[0])
			if a1.Unavailable || !a1.NextRetryAfter.IsZero() {
				t.Fatalf("auth %s unexpectedly put in auth-level cooldown on action=stop: unavailable=%v nextRetry=%v", authIDs[0], a1.Unavailable, a1.NextRetryAfter)
			}
		})

		t.Run(modeName+"/stop_and_cooldown", func(t *testing.T) {
			cfg := &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"antigravity": {
						{
							Status: 403,
							Match:  []string{"VALIDATION_REQUIRED"},
							Action: "stop-and-cooldown",
						},
					},
				},
			}
			cooldownUntil := time.Now().Add(15 * time.Minute)
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-stopcd-"+modeName, model, 3, cfg, 0, func(_ int, auth *Auth) {
				auth.ModelStates = map[string]*ModelState{
					model: {
						Unavailable:    true,
						NextRetryAfter: cooldownUntil,
						Quota: QuotaState{
							Exceeded:      true,
							NextRecoverAt: cooldownUntil,
						},
					},
				}
			})
			exec.executeFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"}
			}
			exec.streamFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Err: &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"}}
				close(ch)
				return &cliproxyexecutor.StreamResult{
					Headers: http.Header{"Content-Type": {"text/event-stream"}},
					Chunks:  ch,
				}, nil
			}

			var errRun error
			if stream {
				_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			} else {
				_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if errRun == nil || !strings.Contains(errRun.Error(), "VALIDATION_REQUIRED") {
				t.Fatalf("err = %v, want VALIDATION_REQUIRED", errRun)
			}
			if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
				t.Fatalf("creditsCalls = %v, want [%s]", calls, authIDs[0])
			}
			a1, _ := manager.GetByID(authIDs[0])
			if !a1.Unavailable || a1.NextRetryAfter.IsZero() {
				t.Fatalf("auth %s must enter auth-level cooldown on stop-and-cooldown; unavailable=%v nextRetry=%v", authIDs[0], a1.Unavailable, a1.NextRetryAfter)
			}
		})

		t.Run(modeName+"/continue_overrides_400_and_rotates", func(t *testing.T) {
			cfg := &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"antigravity": {
						{
							Status: 400,
							Match:  []string{"transient_invalid_prompt"},
							Action: "continue",
						},
					},
				},
			}
			cooldownUntil := time.Now().Add(15 * time.Minute)
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-cont-"+modeName, model, 3, cfg, 0, func(_ int, auth *Auth) {
				auth.ModelStates = map[string]*ModelState{
					model: {
						Unavailable:    true,
						NextRetryAfter: cooldownUntil,
						Quota: QuotaState{
							Exceeded:      true,
							NextRecoverAt: cooldownUntil,
						},
					},
				}
			})
			exec.executeFn = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if auth.ID == authIDs[0] {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadRequest, Message: "transient_invalid_prompt"}
				}
				return cliproxyexecutor.Response{Payload: []byte("recovered-" + auth.ID)}, nil
			}
			exec.streamFn = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if auth.ID == authIDs[0] {
					return nil, &Error{HTTPStatus: http.StatusBadRequest, Message: "transient_invalid_prompt"}
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte("recovered-" + auth.ID)}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			}

			if stream {
				res, errRun := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if errRun != nil {
					t.Fatalf("ExecuteStream() error = %v", errRun)
				}
				var payload []byte
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("chunk error: %v", chunk.Err)
					}
					payload = append(payload, chunk.Payload...)
				}
				if string(payload) != "recovered-"+authIDs[1] {
					t.Fatalf("payload = %q, want %q", string(payload), "recovered-"+authIDs[1])
				}
			} else {
				resp, errRun := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if errRun != nil {
					t.Fatalf("Execute() error = %v", errRun)
				}
				if string(resp.Payload) != "recovered-"+authIDs[1] {
					t.Fatalf("payload = %q, want %q", string(resp.Payload), "recovered-"+authIDs[1])
				}
			}
			if calls := exec.CreditsCalls(); len(calls) != 2 || calls[0] != authIDs[0] || calls[1] != authIDs[1] {
				t.Fatalf("creditsCalls = %v, want [%s %s]", calls, authIDs[0], authIDs[1])
			}
		})
	}
}

func TestAntigravityCreditsExecute_StopsOnTerminalAndRequestInvalidErrors(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "claude-sonnet-4-6"

	cases := []struct {
		name              string
		err               error
		inPrepare         bool
		viaInterceptor    bool
		expectDisabled    bool
		expectStatus      int
		expectMsgContains string
	}{
		{
			name:              "request_terminated_error",
			err:               &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusTooManyRequests, Body: []byte("terminated by interceptor")},
			expectStatus:      http.StatusTooManyRequests,
			expectMsgContains: "request terminated by plugin",
		},
		{
			name:              "after_auth_interceptor_terminated",
			err:               &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusForbidden, Body: []byte("after-auth terminated")},
			viaInterceptor:    true,
			expectStatus:      http.StatusForbidden,
			expectMsgContains: "request terminated by plugin",
		},
		{
			name:              "tos_violation_403",
			err:               &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation", Message: "TOS_VIOLATION"},
			expectDisabled:    true,
			expectStatus:      http.StatusForbidden,
			expectMsgContains: "TOS_VIOLATION",
		},
		{
			name:              "tos_pool_pause_503",
			err:               &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "antigravity pool paused after TOS_VIOLATION"},
			expectStatus:      http.StatusServiceUnavailable,
			expectMsgContains: "antigravity pool paused after TOS_VIOLATION",
		},
		{
			name:              "request_invalid_400",
			err:               &Error{HTTPStatus: http.StatusBadRequest, Code: "invalid_request_error", Message: "invalid_request_error: bad schema"},
			expectStatus:      http.StatusBadRequest,
			expectMsgContains: "invalid_request_error",
		},
		{
			name:              "prepare_request_terminated",
			err:               &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusForbidden, Body: []byte("prepare terminated")},
			inPrepare:         true,
			expectStatus:      http.StatusForbidden,
			expectMsgContains: "request terminated by plugin",
		},
	}

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				cooldownUntil := time.Now().Add(15 * time.Minute)
				manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-term-"+mode+"-"+tc.name, model, 3, nil, 0, func(_ int, auth *Auth) {
					auth.ModelStates = map[string]*ModelState{
						model: {
							Unavailable:    true,
							NextRetryAfter: cooldownUntil,
							Quota: QuotaState{
								Exceeded:      true,
								NextRecoverAt: cooldownUntil,
							},
						},
					}
				})

				prepareCount := 0
				if tc.inPrepare {
					exec.prepareCheck = func(*Auth) bool { return true }
					exec.prepareFn = func(_ context.Context, _ *Auth) (*Auth, error) {
						prepareCount++
						return nil, tc.err
					}
				}
				exec.executeFn = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, tc.err
				}
				exec.streamFn = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					return nil, tc.err
				}

				opts := cliproxyexecutor.Options{}
				if tc.viaInterceptor {
					interceptCount := 0
					opts.RequestAfterAuthInterceptor = func(_ context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
						interceptCount++
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{
							Terminate:    true,
							StatusCode:   tc.expectStatus,
							ResponseBody: []byte("after-auth terminated"),
						}
					}
					t.Cleanup(func() {
						if interceptCount != 1 {
							t.Errorf("interceptor count = %d, want 1", interceptCount)
						}
					})
				}

				var errRun error
				if stream {
					_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, opts)
				} else {
					_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, opts)
				}
				if errRun == nil {
					t.Fatalf("expected error, got nil")
				}
				if gotStatus := statusCodeFromError(errRun); gotStatus != tc.expectStatus {
					t.Fatalf("status = %d, want %d (err=%v)", gotStatus, tc.expectStatus, errRun)
				}
				if !strings.Contains(errRun.Error(), tc.expectMsgContains) {
					t.Fatalf("err = %v, want substring %q", errRun, tc.expectMsgContains)
				}
				if tc.inPrepare {
					if prepareCount != 1 {
						t.Fatalf("prepareCount = %d, want 1", prepareCount)
					}
				} else if !tc.viaInterceptor {
					if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
						t.Fatalf("creditsCalls = %v, want [%s]", calls, authIDs[0])
					}
				}
				if tc.expectDisabled {
					a1, _ := manager.GetByID(authIDs[0])
					if a1.Status != StatusDisabled || !a1.Disabled {
						t.Fatalf("auth %s should be disabled on TOS_VIOLATION; status=%q disabled=%v", authIDs[0], a1.Status, a1.Disabled)
					}
				}
			})
		}
	}
}

func TestAntigravityCreditsExecute_RespectsMaxRetryCredentials(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	const model = "claude-sonnet-4-6"

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		t.Run(mode+"/capped_at_2", func(t *testing.T) {
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-cap-"+mode, model, 5, nil, 2, nil)
			exec.executeFn = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
			}
			exec.streamFn = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
			}

			var errRun error
			if stream {
				_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			} else {
				_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if errRun == nil {
				t.Fatal("expected error, got nil")
			}
			if normal := exec.NormalCalls(); len(normal) != 2 {
				t.Fatalf("normalCalls = %v (len %d), want 2", normal, len(normal))
			}
			if credits := exec.CreditsCalls(); len(credits) != 2 || credits[0] != authIDs[0] || credits[1] != authIDs[1] {
				t.Fatalf("creditsCalls = %v, want [%s %s] (must obey max-retry-credentials=2)", credits, authIDs[0], authIDs[1])
			}
		})

		t.Run(mode+"/unlimited_when_zero", func(t *testing.T) {
			cooldownUntil := time.Now().Add(15 * time.Minute)
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-uncap-"+mode, model, 4, nil, 0, func(_ int, auth *Auth) {
				auth.ModelStates = map[string]*ModelState{
					model: {
						Unavailable:    true,
						NextRetryAfter: cooldownUntil,
						Quota: QuotaState{
							Exceeded:      true,
							NextRecoverAt: cooldownUntil,
						},
					},
				}
			})
			exec.executeFn = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if auth.ID != authIDs[3] {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				return cliproxyexecutor.Response{Payload: []byte("ok-" + auth.ID)}, nil
			}
			exec.streamFn = func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if auth.ID != authIDs[3] {
					return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte("ok-" + auth.ID)}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			}

			if stream {
				res, errRun := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if errRun != nil {
					t.Fatalf("ExecuteStream() error = %v", errRun)
				}
				var payload []byte
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("chunk error: %v", chunk.Err)
					}
					payload = append(payload, chunk.Payload...)
				}
				if string(payload) != "ok-"+authIDs[3] {
					t.Fatalf("payload = %q, want %q", string(payload), "ok-"+authIDs[3])
				}
			} else {
				resp, errRun := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if errRun != nil {
					t.Fatalf("Execute() error = %v", errRun)
				}
				if string(resp.Payload) != "ok-"+authIDs[3] {
					t.Fatalf("payload = %q, want %q", string(resp.Payload), "ok-"+authIDs[3])
				}
			}
			if credits := exec.CreditsCalls(); len(credits) != 4 {
				t.Fatalf("creditsCalls = %v (len %d), want 4 when max-retry-credentials=0", credits, len(credits))
			}
		})

		t.Run(mode+"/capped_at_1_allows_single_credits_attempt", func(t *testing.T) {
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-cap1-"+mode, model, 3, nil, 1, nil)
			exec.executeFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if !AntigravityCreditsRequested(ctx) {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "MODEL_CAPACITY_EXHAUSTED"}
				}
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
			}
			exec.streamFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if !AntigravityCreditsRequested(ctx) {
					return nil, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "MODEL_CAPACITY_EXHAUSTED"}
				}
				return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
			}

			var errRun error
			if stream {
				_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			} else {
				_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if errRun == nil {
				t.Fatal("expected error, got nil")
			}
			if normal := exec.NormalCalls(); len(normal) != 1 {
				t.Fatalf("normalCalls = %v (len %d), want 1", normal, len(normal))
			}
			if credits := exec.CreditsCalls(); len(credits) != 1 || credits[0] != authIDs[0] {
				t.Fatalf("creditsCalls = %v, want [%s]", credits, authIDs[0])
			}
		})
	}
}

func TestShouldAttemptAntigravityCreditsFallback_GatesNonQuotaErrors(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	longRetry := 2 * time.Hour
	shortRetry := 3 * time.Second

	cases := []struct {
		name string
		err  error
		want bool
	}{
		// Rejections
		{
			name: "reject_waf_html_429",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `<!DOCTYPE html><html lang=en><title>Error 429 (Too Many Requests)!!1</title></html>`},
			want: false,
		},
		{
			name: "reject_cloudflare_429",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `<html><head><title>Just a moment...</title></head><body>cf-mitigated challenge-platform</body></html>`},
			want: false,
		},
		{
			name: "reject_generic_429_rate_limit",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `Too Many Requests`},
			want: false,
		},
		{
			name: "reject_short_burst_rate_limit_exceeded_429",
			err: issue7658StatusErr{
				status:     http.StatusTooManyRequests,
				msg:        `{"error":{"code":429,"message":"You have exhausted your capacity on this model.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`,
				retryAfter: &shortRetry,
			},
			want: false,
		},
		{
			name: "reject_short_cooldown_429",
			err:  issue7658StatusErr{status: http.StatusTooManyRequests, msg: "auth in short cooldown, 3s remaining", retryAfter: &shortRetry},
			want: false,
		},
		{
			name: "reject_resource_exhausted_generic_rate_limit_429",
			err:  issue7658StatusErr{status: http.StatusTooManyRequests, msg: `{"error":{"code":429,"message":"Too many requests","status":"RESOURCE_EXHAUSTED"}}`},
			want: false,
		},
		{
			name: "reject_resource_exhausted_short_reset_429",
			err:  issue7658StatusErr{status: http.StatusTooManyRequests, msg: `{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 3s.","status":"RESOURCE_EXHAUSTED"}}`, retryAfter: &shortRetry},
			want: false,
		},
		{
			name: "reject_transient_rate_limit_interface_429",
			err:  issue7658TransientRateLimitErr{status: http.StatusTooManyRequests, msg: "quota exhausted"},
			want: false,
		},
		{
			name: "reject_tos_violation_403",
			err:  &Error{HTTPStatus: http.StatusForbidden, Code: "tos_violation", Message: "TOS_VIOLATION"},
			want: false,
		},
		{
			name: "reject_tos_pool_pause_503",
			err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "antigravity pool paused after TOS_VIOLATION"},
			want: false,
		},
		{
			name: "reject_generic_503",
			err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream temporarily unavailable"},
			want: false,
		},
		{
			name: "reject_html_503",
			err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "<!doctype html><html><body>503 Service Unavailable</body></html>"},
			want: false,
		},
		{
			name: "reject_home_kv_unavailable_503",
			err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Code: "home_kv_unavailable", Message: "home kv store unavailable"},
			want: false,
		},
		{
			name: "reject_session_auth_retry_503",
			err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Code: "session_auth_retry", Message: "bound session credential is transiently unavailable"},
			want: false,
		},
		{
			name: "reject_400_request_invalid",
			err:  &Error{HTTPStatus: http.StatusBadRequest, Code: "invalid_request_error", Message: "invalid_request_error"},
			want: false,
		},
		{
			name: "reject_request_terminated",
			err:  &cliproxyexecutor.RequestTerminatedError{HTTPStatus: http.StatusTooManyRequests, Body: []byte("terminated")},
			want: false,
		},
		{
			name: "reject_request_stop_wrapped",
			err:  wrapRequestStopError(&Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}),
			want: false,
		},

		// Allowed quota / capacity / cooldown errors
		{
			name: "allow_status0_model_cooldown",
			err:  &Error{Code: "model_cooldown", Message: "cooldown"},
			want: true,
		},
		{
			name: "allow_status0_auth_unavailable",
			err:  &Error{Code: "auth_unavailable", Message: "unavailable"},
			want: true,
		},
		{
			name: "allow_status0_modelCooldownError",
			err:  &modelCooldownError{model: "claude-sonnet-4-6", resetIn: 5 * time.Minute},
			want: true,
		},
		{
			name: "allow_plain_quota_exhausted_429",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
			want: true,
		},
		{
			name: "allow_resource_exhausted_check_quota_429",
			err:  issue7658StatusErr{status: http.StatusTooManyRequests, msg: `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED"}}`},
			want: true,
		},
		{
			name: "allow_google_quota_exhausted_429",
			err: issue7658StatusErr{
				status: http.StatusTooManyRequests,
				msg:    `{"error":{"code":429,"message":"You have exhausted your quota.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"QUOTA_EXHAUSTED"}]}}`,
			},
			want: true,
		},
		{
			name: "allow_google_insufficient_g1_credits_429",
			err: issue7658StatusErr{
				status: http.StatusTooManyRequests,
				msg:    `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_G1_CREDITS_BALANCE"}]}}`,
			},
			want: true,
		},
		{
			name: "allow_long_rate_limit_exceeded_429",
			err: issue7658StatusErr{
				status:     http.StatusTooManyRequests,
				msg:        `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"RATE_LIMIT_EXCEEDED"}]}}`,
				retryAfter: &longRetry,
			},
			want: true,
		},
		{
			name: "allow_codex_usage_limit_reached_429",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":"usage_limit_reached","type":"usage_limit_reached"}}`},
			want: true,
		},
		{
			name: "allow_503_model_capacity_exhausted",
			err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "MODEL_CAPACITY_EXHAUSTED"},
			want: true,
		},
		{
			name: "allow_503_google_capacity_exhausted_json",
			err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Message: `{"error":{"code":503,"message":"No capacity available for model claude-sonnet-4-6","status":"UNAVAILABLE","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"MODEL_CAPACITY_EXHAUSTED"}]}}`},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldAttemptAntigravityCreditsFallback(manager, tc.err, []string{"antigravity"})
			if got != tc.want {
				t.Fatalf("shouldAttemptAntigravityCreditsFallback(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestAntigravityCreditsExecute_EndToEndSkipsWAFAndTOSPoolPause(t *testing.T) {
	const model = "claude-sonnet-4-6"

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		for _, tc := range []struct {
			name string
			err  error
		}{
			{
				name: "waf_html_429",
				err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `<!DOCTYPE html><html><title>Error 429 (Too Many Requests)</title></html>`},
			},
			{
				name: "tos_pool_pause_503",
				err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "antigravity pool paused after TOS_VIOLATION"},
			},
			{
				name: "generic_503",
				err:  &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "503 Service Temporarily Unavailable"},
			},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				manager, exec, _ := setupIssue7658CreditsManager(t, "ag-7658-gate-"+mode+"-"+tc.name, model, 2, nil, 0, nil)
				exec.executeFn = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{}, tc.err
				}
				exec.streamFn = func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					return nil, tc.err
				}

				var errRun error
				if stream {
					_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				} else {
					_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				}
				if errRun == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(errRun, tc.err) && statusCodeFromError(errRun) != statusCodeFromError(tc.err) {
					t.Fatalf("unexpected error = %v, want %v", errRun, tc.err)
				}
				if credits := exec.CreditsCalls(); len(credits) != 0 {
					t.Fatalf("creditsCalls = %v, want none when normal round failed with %s", credits, tc.name)
				}
			})
		}
	}
}

type issue7658ExplicitStopError struct {
	status int
	msg    string
}

func (e issue7658ExplicitStopError) Error() string       { return e.msg }
func (e issue7658ExplicitStopError) StatusCode() int     { return e.status }
func (e issue7658ExplicitStopError) IsRequestStop() bool { return true }

func TestAntigravityCreditsExecuteStream_FirstChunkTOSReturnsDirectError(t *testing.T) {
	const model = "claude-sonnet-4-6"

	for _, tc := range []struct {
		name       string
		chunkErr   error
		wantSubstr string
	}{
		{
			name: "first_chunk_tos_violation",
			chunkErr: &Error{
				HTTPStatus: http.StatusForbidden,
				Code:       "tos_violation",
				Message:    "TOS_VIOLATION",
			},
			wantSubstr: "TOS_VIOLATION",
		},
		{
			name: "first_chunk_validation_required",
			chunkErr: &Error{
				HTTPStatus: http.StatusForbidden,
				Message:    `{"error":{"code":403,"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED"}]}}`,
			},
			wantSubstr: "VALIDATION_REQUIRED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-fc-"+tc.name, model, 2, nil, 1, nil)
			exec.streamFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if !AntigravityCreditsRequested(ctx) {
					return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Err: tc.chunkErr}
				close(ch)
				return &cliproxyexecutor.StreamResult{
					Headers: http.Header{"Content-Type": {"text/event-stream"}},
					Chunks:  ch,
				}, nil
			}

			res, errRun := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			if errRun == nil {
				t.Fatalf("ExecuteStream() returned nil error (streamResult=%v), want direct error so HTTP bootstrap retries do not replay", res)
			}
			if !strings.Contains(errRun.Error(), tc.wantSubstr) {
				t.Fatalf("err = %v, want substring %q", errRun, tc.wantSubstr)
			}
			if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
				t.Fatalf("creditsCalls = %v, want [%s]", calls, authIDs[0])
			}
		})
	}
}

func TestAntigravityCreditsExecute_TOSViolationPreservedUnderRequestScopedRule(t *testing.T) {
	const model = "claude-sonnet-4-6"

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		for _, action := range []string{"stop", "stop-and-cooldown", "continue", "continue-and-cooldown"} {
			for _, viaFirstChunk := range []bool{false, true} {
				if !stream && viaFirstChunk {
					continue
				}
				sub := mode + "/" + action
				if viaFirstChunk {
					sub += "/first_chunk"
				}
				t.Run(sub, func(t *testing.T) {
					cfg := &internalconfig.Config{
						OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
							"antigravity": {
								{
									Status: http.StatusForbidden,
									Match:  []string{"TOS_VIOLATION"},
									Action: action,
								},
							},
						},
					}
					manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-tos-rule-"+sub, model, 2, cfg, 2, nil)
					tosErr := &Error{
						HTTPStatus: http.StatusForbidden,
						Code:       "tos_violation",
						Message:    "TOS_VIOLATION",
						Metadata:   map[string]string{"tos_appeal_url": "https://forms.gle/test-appeal"},
					}
					exec.executeFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						if !AntigravityCreditsRequested(ctx) {
							return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
						}
						return cliproxyexecutor.Response{}, tosErr
					}
					exec.streamFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						if !AntigravityCreditsRequested(ctx) {
							return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
						}
						if viaFirstChunk {
							ch := make(chan cliproxyexecutor.StreamChunk, 1)
							ch <- cliproxyexecutor.StreamChunk{Err: tosErr}
							close(ch)
							return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
						}
						return nil, tosErr
					}

					var errRun error
					if stream {
						_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
					} else {
						_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
					}
					if errRun == nil || !strings.Contains(errRun.Error(), "TOS_VIOLATION") {
						t.Fatalf("err = %v, want TOS_VIOLATION", errRun)
					}
					if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
						t.Fatalf("creditsCalls = %v, want [%s]", calls, authIDs[0])
					}
					a1, ok := manager.GetByID(authIDs[0])
					if !ok || a1 == nil {
						t.Fatalf("auth %s missing", authIDs[0])
					}
					if a1.Status != StatusDisabled || !a1.Disabled || a1.StatusMessage != "tos_violation" {
						t.Fatalf("auth %s not disabled by TOS despite matching 403 rule: status=%q disabled=%v statusMessage=%q", authIDs[0], a1.Status, a1.Disabled, a1.StatusMessage)
					}
					if appeal, _ := a1.Metadata["tos_appeal_url"].(string); appeal != "https://forms.gle/test-appeal" {
						t.Fatalf("auth %s lost tos_appeal_url metadata: %#v", authIDs[0], a1.Metadata)
					}
				})
			}
		}
	}
}

func TestAntigravityCreditsExecute_ExplicitRequestStopBeatsContinueRule(t *testing.T) {
	const model = "claude-sonnet-4-6"

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		for _, action := range []string{"continue", "continue-and-cooldown"} {
			t.Run(mode+"/"+action, func(t *testing.T) {
				cfg := &internalconfig.Config{
					OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
						"antigravity": {
							{
								Status: http.StatusForbidden,
								Match:  []string{"HARD_STOP_MARKER"},
								Action: action,
							},
						},
					},
				}
				manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-hardstop-"+mode+"-"+action, model, 3, cfg, 3, nil)
				stopErr := issue7658ExplicitStopError{status: http.StatusForbidden, msg: "HARD_STOP_MARKER"}
				exec.executeFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if !AntigravityCreditsRequested(ctx) {
						return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
					}
					return cliproxyexecutor.Response{}, stopErr
				}
				exec.streamFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if !AntigravityCreditsRequested(ctx) {
						return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
					}
					return nil, stopErr
				}

				var errRun error
				if stream {
					_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				} else {
					_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				}
				if errRun == nil || !strings.Contains(errRun.Error(), "HARD_STOP_MARKER") {
					t.Fatalf("err = %v, want HARD_STOP_MARKER", errRun)
				}
				if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
					t.Fatalf("creditsCalls = %v, want [%s] (explicit IsRequestStop must override continue rule)", calls, authIDs[0])
				}
			})
		}
	}
}

func TestAntigravityCreditsExecute_BareValidationRequiredStopsWithoutRules(t *testing.T) {
	const model = "claude-sonnet-4-6"

	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		for _, tc := range []struct {
			name string
			err  error
		}{
			{
				name: "json_reason",
				err:  &Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"code":403,"message":"Verify your account to continue.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"VALIDATION_REQUIRED"}]}}`},
			},
			{
				name: "plain_validation_required",
				err:  &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"},
			},
			{
				name: "json_top_level_status",
				err:  &Error{HTTPStatus: http.StatusForbidden, Message: `{"status":"VALIDATION_REQUIRED"}`},
			},
		} {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-bareval-"+mode+"-"+tc.name, model, 3, nil, 3, nil)
				exec.executeFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if !AntigravityCreditsRequested(ctx) {
						return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
					}
					return cliproxyexecutor.Response{}, tc.err
				}
				exec.streamFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if !AntigravityCreditsRequested(ctx) {
						return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
					}
					return nil, tc.err
				}

				var errRun error
				if stream {
					_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				} else {
					_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				}
				if errRun == nil {
					t.Fatal("expected error, got nil")
				}
				if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
					t.Fatalf("creditsCalls = %v, want [%s] (bare VALIDATION_REQUIRED must stop without rules)", calls, authIDs[0])
				}
			})
		}

		t.Run(mode+"/json_insufficient_scope_with_false_validation_metadata_still_rotates", func(t *testing.T) {
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-inscope-"+mode, model, 2, nil, 2, nil)
			errInsufficientScope := &Error{
				HTTPStatus: http.StatusForbidden,
				Message:    `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"insufficient scope","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"INSUFFICIENT_SCOPE","metadata":{"validation_required":false}}]}}`,
			}
			exec.executeFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if !AntigravityCreditsRequested(ctx) {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				if auth.ID == authIDs[0] {
					return cliproxyexecutor.Response{}, errInsufficientScope
				}
				return cliproxyexecutor.Response{Payload: []byte("ok-" + auth.ID)}, nil
			}
			exec.streamFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if !AntigravityCreditsRequested(ctx) {
					return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				if auth.ID == authIDs[0] {
					return nil, errInsufficientScope
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte("ok-" + auth.ID)}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			}

			if stream {
				res, errRun := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if errRun != nil {
					t.Fatalf("ExecuteStream() unexpected error: %v", errRun)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected chunk error: %v", chunk.Err)
					}
				}
			} else {
				resp, errRun := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if errRun != nil || string(resp.Payload) != "ok-"+authIDs[1] {
					t.Fatalf("Execute() = (%q, %v), want ok-%s", string(resp.Payload), errRun, authIDs[1])
				}
			}
			if calls := exec.CreditsCalls(); len(calls) != 2 {
				t.Fatalf("creditsCalls = %v, want 2 calls (INSUFFICIENT_SCOPE with validation_required=false metadata must still rotate)", calls)
			}
		})

		t.Run(mode+"/plain_403_permission_denied_still_rotates", func(t *testing.T) {
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-plain403-"+mode, model, 2, nil, 2, nil)
			exec.executeFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if !AntigravityCreditsRequested(ctx) {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				if auth.ID == authIDs[0] {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"permission denied"}}`}
				}
				return cliproxyexecutor.Response{Payload: []byte("ok-" + auth.ID)}, nil
			}
			exec.streamFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if !AntigravityCreditsRequested(ctx) {
					return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				if auth.ID == authIDs[0] {
					return nil, &Error{HTTPStatus: http.StatusForbidden, Message: `{"error":{"code":403,"status":"PERMISSION_DENIED","message":"permission denied"}}`}
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte("ok-" + auth.ID)}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			}

			if stream {
				res, errRun := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if errRun != nil {
					t.Fatalf("ExecuteStream() unexpected error: %v", errRun)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected chunk error: %v", chunk.Err)
					}
				}
			} else {
				resp, errRun := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if errRun != nil || string(resp.Payload) != "ok-"+authIDs[1] {
					t.Fatalf("Execute() = (%q, %v), want ok-%s", string(resp.Payload), errRun, authIDs[1])
				}
			}
			if calls := exec.CreditsCalls(); len(calls) != 2 {
				t.Fatalf("creditsCalls = %v, want 2 calls (generic 403 must still rotate)", calls)
			}
		})

		t.Run(mode+"/validation_required_continue_rule_overrides", func(t *testing.T) {
			cfg := &internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"antigravity": {
						{
							Status: http.StatusForbidden,
							Match:  []string{"VALIDATION_REQUIRED"},
							Action: "continue",
						},
					},
				},
			}
			manager, exec, authIDs := setupIssue7658CreditsManager(t, "ag-7658-valoverride-"+mode, model, 2, cfg, 2, nil)
			exec.executeFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if !AntigravityCreditsRequested(ctx) {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				if auth.ID == authIDs[0] {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"}
				}
				return cliproxyexecutor.Response{Payload: []byte("ok-" + auth.ID)}, nil
			}
			exec.streamFn = func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if !AntigravityCreditsRequested(ctx) {
					return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				if auth.ID == authIDs[0] {
					return nil, &Error{HTTPStatus: http.StatusForbidden, Message: "VALIDATION_REQUIRED"}
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte("ok-" + auth.ID)}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			}

			if stream {
				res, errRun := manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
				if errRun != nil {
					t.Fatalf("ExecuteStream() unexpected error: %v", errRun)
				}
				for chunk := range res.Chunks {
					if chunk.Err != nil {
						t.Fatalf("unexpected chunk error: %v", chunk.Err)
					}
				}
			} else {
				resp, errRun := manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if errRun != nil || string(resp.Payload) != "ok-"+authIDs[1] {
					t.Fatalf("Execute() = (%q, %v), want ok-%s", string(resp.Payload), errRun, authIDs[1])
				}
			}
		})
	}
}

func TestShouldAttemptAntigravityCreditsFallback_PlainTextRateLimitCounterexamples(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	shortTwoSec := 2 * time.Second
	longTenMin := 10 * time.Minute

	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "reject_plain_per_minute_quota_short_text_retry",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: "per-minute quota exceeded; retry in 2 seconds"},
			want: false,
		},
		{
			name: "reject_plain_per_minute_quota_with_short_retry_after",
			err: issue7658StatusErr{
				status:     http.StatusTooManyRequests,
				msg:        "per-minute quota exceeded; retry in 2 seconds",
				retryAfter: &shortTwoSec,
			},
			want: false,
		},
		{
			name: "reject_plain_connection_pool_exhausted",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: "connection pool exhausted"},
			want: false,
		},
		{
			name: "reject_plain_quota_exhausted_with_short_text_retry",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted; retry in 30s"},
			want: false,
		},
		{
			name: "reject_plain_quota_check_failed",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota check failed"},
			want: false,
		},
		{
			name: "allow_plain_quota_exhausted_long_reset",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: "QUOTA_EXHAUSTED: Resets in 140h36m"},
			want: true,
		},
		{
			name: "reject_plain_unable_to_check_quota",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: "429: unable to check quota"},
			want: false,
		},
		{
			name: "allow_json_daily_quota_exhausted_message_only",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":429,"message":"daily quota exhausted"}}`},
			want: true,
		},
		{
			name: "reject_json_metadata_note_quota_exhausted",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":429,"message":"ok","metadata":{"note":"quota exhausted"}}}`},
			want: false,
		},
		{
			name: "reject_json_structured_rate_limit_exceeded_code_despite_quota_message",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":"rate_limit_exceeded","message":"Quota exceeded"}}`},
			want: false,
		},
		{
			name: "allow_json_structured_rate_limit_exceeded_code_with_long_retry_after",
			err: issue7658StatusErr{
				status:     http.StatusTooManyRequests,
				msg:        `{"error":{"code":"rate_limit_exceeded","message":"Quota exceeded"}}`,
				retryAfter: &longTenMin,
			},
			want: true,
		},
		{
			name: "reject_json_quota_code_with_rate_limit_type_despite_quota_message",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":"resource_exhausted","type":"rate_limit_exceeded","message":"Quota exceeded"}}`},
			want: false,
		},
		{
			name: "reject_json_quota_code_with_rate_limit_reason_despite_quota_message",
			err:  &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":"resource_exhausted","message":"Quota exceeded","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldAttemptAntigravityCreditsFallback(manager, tc.err, []string{"antigravity"}); got != tc.want {
				t.Fatalf("shouldAttemptAntigravityCreditsFallback(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

type issue7658RefreshTrackingExecutor struct {
	issue7658CreditsExecutor
	mu           sync.Mutex
	refreshCalls []string
}

func (e *issue7658RefreshTrackingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.refreshCalls = append(e.refreshCalls, auth.ID)
	e.mu.Unlock()
	cloned := auth.Clone()
	if cloned.Metadata == nil {
		cloned.Metadata = make(map[string]any)
	}
	cloned.Metadata["access_token"] = "refreshed-token"
	return cloned, nil
}

func (e *issue7658RefreshTrackingExecutor) RefreshCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.refreshCalls))
	copy(out, e.refreshCalls)
	return out
}

func TestAntigravityCreditsExecute_401ExplicitRequestStopDoesNotRefreshAndRetry(t *testing.T) {
	const model = "claude-sonnet-4-6"

	for _, tc := range []struct {
		name       string
		stream     bool
		firstChunk bool
	}{
		{name: "execute", stream: false},
		{name: "stream_direct", stream: true, firstChunk: false},
		{name: "stream_first_chunk", stream: true, firstChunk: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := &issue7658RefreshTrackingExecutor{
				issue7658CreditsExecutor: issue7658CreditsExecutor{},
			}
			manager := NewManager(nil, &FillFirstSelector{}, nil)
			manager.SetConfig(&internalconfig.Config{
				QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
			})
			manager.SetRetryConfig(0, 0, 2)
			manager.RegisterExecutor(exec)

			authIDs := make([]string, 0, 2)
			for i := 1; i <= 2; i++ {
				id := fmt.Sprintf("ag-7658-401stop-%s-%d", tc.name, i)
				authIDs = append(authIDs, id)
				if _, errReg := manager.Register(context.Background(), &Auth{
					ID:       id,
					Provider: "antigravity",
					Status:   StatusActive,
					Metadata: map[string]any{
						"access_token":  "initial-token",
						"refresh_token": "refresh-token",
					},
				}); errReg != nil {
					t.Fatalf("Register(%s): %v", id, errReg)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
			}
			t.Cleanup(func() {
				for _, id := range authIDs {
					registry.GetGlobalRegistry().UnregisterClient(id)
				}
			})

			stop401 := issue7658ExplicitStopError{status: http.StatusUnauthorized, msg: "HARD_STOP_401"}
			exec.executeFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if !AntigravityCreditsRequested(ctx) {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				return cliproxyexecutor.Response{}, stop401
			}
			exec.streamFn = func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if !AntigravityCreditsRequested(ctx) {
					return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
				}
				if tc.firstChunk {
					ch := make(chan cliproxyexecutor.StreamChunk, 1)
					ch <- cliproxyexecutor.StreamChunk{Err: stop401}
					close(ch)
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				}
				return nil, stop401
			}

			var errRun error
			if tc.stream {
				_, errRun = manager.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			} else {
				_, errRun = manager.Execute(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			}
			if errRun == nil || !strings.Contains(errRun.Error(), "HARD_STOP_401") {
				t.Fatalf("err = %v, want HARD_STOP_401", errRun)
			}
			if refreshes := exec.RefreshCalls(); len(refreshes) != 0 {
				t.Fatalf("refreshCalls = %v, want none on explicit 401 IsRequestStop()", refreshes)
			}
			if calls := exec.CreditsCalls(); len(calls) != 1 || calls[0] != authIDs[0] {
				t.Fatalf("creditsCalls = %v, want [%s] (must not refresh-retry or rotate)", calls, authIDs[0])
			}
		})
	}
}

func TestShouldAttemptAntigravityCreditsFallback_SwitchedVariantRemainingCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})

	now := time.Now()
	remaining := 3 * time.Minute
	deadline := now.Add(remaining)
	auth := &Auth{
		ID:       "ag-issue8181-switched",
		Provider: "antigravity",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"claude-opus-5-5-low": {
				Status:         StatusActive,
				Unavailable:    true,
				NextRetryAfter: deadline,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: deadline,
				},
				LastError: &Error{
					HTTPStatus: http.StatusTooManyRequests,
					Message:    `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","message":"Quota exhausted","details":[{"reason":"QUOTA_EXHAUSTED"}]}}`,
					Metadata: map[string]string{
						antigravityCreditsRetryHintKey: strconv.FormatInt(int64(5*time.Hour), 10),
					},
				},
			},
		},
	}

	errTarget := CheckAntigravityTargetModel(context.Background(), auth, "claude-opus-5-5-low", now, false)
	if errTarget == nil {
		t.Fatal("CheckAntigravityTargetModel() = nil, want cooldown error")
	}
	rejectedErr := RejectAntigravityTarget(errTarget)
	if !shouldAttemptAntigravityCreditsFallback(manager, rejectedErr, []string{"antigravity"}) {
		t.Fatalf("shouldAttemptAntigravityCreditsFallback(%v) = false during final 3m of 5h quota cooldown, want true", rejectedErr)
	}

	shortRetry := 30 * time.Second
	for _, tc := range []struct {
		name string
		err  error
	}{
		{
			name: "direct_model_cooldown_with_waf_html_cause",
			err: newModelCooldownErrorWithCause(
				"claude-sonnet-4-6",
				"antigravity",
				remaining,
				&Error{HTTPStatus: http.StatusTooManyRequests, Message: `<!DOCTYPE html><html><title>Error 429</title></html>`},
			),
		},
		{
			name: "direct_model_cooldown_with_short_rate_limit_cause",
			err: newModelCooldownErrorWithCause(
				"claude-sonnet-4-6",
				"antigravity",
				remaining,
				issue7658StatusErr{
					status:     http.StatusTooManyRequests,
					msg:        `{"error":{"code":429,"status":"RESOURCE_EXHAUSTED","details":[{"reason":"RATE_LIMIT_EXCEEDED"}]}}`,
					retryAfter: &shortRetry,
				},
			),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if shouldAttemptAntigravityCreditsFallback(manager, tc.err, []string{"antigravity"}) {
				t.Fatalf("shouldAttemptAntigravityCreditsFallback(%v) = true, want false for non-quota cause", tc.err)
			}
		})
	}
}
