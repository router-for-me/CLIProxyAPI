package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type testContinuityScheduler struct {
	pickFunc       func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error)
	continuityFunc func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error)
	hasContinuity  bool
}

func (s *testContinuityScheduler) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	if s.pickFunc != nil {
		return s.pickFunc(ctx, req)
	}
	return pluginapi.SchedulerPickResponse{}, false, nil
}

func (s *testContinuityScheduler) HasSchedulerContinuity() bool {
	return s.hasContinuity
}

func (s *testContinuityScheduler) EvaluateSchedulerContinuity(ctx context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
	if !s.hasContinuity || s.continuityFunc == nil {
		return pluginapi.SchedulerContinuityResponse{}, false, nil
	}
	return s.continuityFunc(ctx, req)
}

type fakeContinuityClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeContinuityClock(start time.Time) *fakeContinuityClock {
	return &fakeContinuityClock{now: start}
}

func (c *fakeContinuityClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeContinuityClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeContinuityClock) Wait(ctx context.Context, d time.Duration) error {
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	c.Advance(d)
	return nil
}

type continuityTestExecutor struct {
	provider string
	execFn   func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	streamFn func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func (e *continuityTestExecutor) Identifier() string { return e.provider }
func (e *continuityTestExecutor) Execute(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if e.execFn != nil {
		return e.execFn(ctx, a, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (e *continuityTestExecutor) ExecuteStream(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if e.streamFn != nil {
		return e.streamFn(ctx, a, req, opts)
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}
func (e *continuityTestExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }
func (e *continuityTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *continuityTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type codexQuotaRejectionErr struct {
	status           int
	msg              string
	credentialScoped bool
	transient        bool
}

func (e codexQuotaRejectionErr) Error() string              { return e.msg }
func (e codexQuotaRejectionErr) StatusCode() int            { return e.status }
func (e codexQuotaRejectionErr) IsCredentialScoped() bool   { return e.credentialScoped }
func (e codexQuotaRejectionErr) IsTransientRateLimit() bool { return e.transient }

func TestCodexContinuityPreAdmissionCandidateFilteringAndRecovery(t *testing.T) {
	clock := newFakeContinuityClock(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
	manager := NewManager(nil, nil, nil)
	manager.SetContinuityClockForTest(clock)
	manager.SetRetryConfig(3, 0, 0)

	var execCalls atomic.Int32
	manager.RegisterExecutor(&continuityTestExecutor{
		provider: "codex",
		execFn: func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			execCalls.Add(1)
			return cliproxyexecutor.Response{Payload: []byte(`{"selected":"` + a.ID + `"}`)}, nil
		},
	})
	manager.RegisterExecutor(&continuityTestExecutor{provider: "gemini"})

	recoverAt := time.Now().Add(400 * time.Millisecond)
	coolingAuth := &Auth{
		ID:       "codex-cooling",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"plan_type":    "plus",
			"access_token": "secret-should-not-leak",
		},
		Metadata: map[string]any{"refresh_token": "secret-refresh"},
		Storage:  &testTokenStorage{token: "secret-doc"},
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: recoverAt,
		},
		NextRetryAfter: recoverAt,
	}
	disabledAuth := &Auth{ID: "codex-disabled", Provider: "codex", Disabled: true, Status: StatusDisabled}
	unauthAuth := &Auth{
		ID:          "codex-unauth",
		Provider:    "codex",
		Status:      StatusError,
		Unavailable: true,
		LastError:   &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	}
	unsupportedAuth := &Auth{ID: "codex-unsupported", Provider: "codex", Status: StatusActive}
	geminiAuth := &Auth{
		ID:       "gemini-cooling",
		Provider: "gemini",
		Status:   StatusActive,
		Quota:    QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
	}

	reg := registry.GetGlobalRegistry()
	for _, a := range []*Auth{coolingAuth, disabledAuth, unauthAuth, unsupportedAuth, geminiAuth} {
		if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register %s: %v", a.ID, errRegister)
		}
	}
	reg.RegisterClient("codex-cooling", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	reg.RegisterClient("codex-disabled", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	reg.RegisterClient("codex-unauth", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	reg.RegisterClient("codex-unsupported", "codex", []*registry.ModelInfo{{ID: "other-model"}})
	reg.RegisterClient("gemini-cooling", "gemini", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() {
		for _, id := range []string{"codex-cooling", "codex-disabled", "codex-unauth", "codex-unsupported", "gemini-cooling"} {
			reg.UnregisterClient(id)
		}
	})

	var polls int
	manager.SetPluginScheduler(&testContinuityScheduler{
		hasContinuity: true,
		continuityFunc: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			polls++
			if req.RequestID != "req-life-1" || req.TraceID != "trace-life-1" {
				t.Fatalf("lifecycle identity = (%q, %q), want (req-life-1, trace-life-1)", req.RequestID, req.TraceID)
			}
			if len(req.Candidates) != 1 || req.Candidates[0].ID != "codex-cooling" {
				t.Fatalf("candidates = %#v, want only codex-cooling", req.Candidates)
			}
			cand := req.Candidates[0]
			if cand.Attributes["access_token"] != "" || cand.Attributes["plan_type"] != "plus" {
				t.Fatalf("candidate attributes leaked or missing plan_type: %#v", cand.Attributes)
			}
			if polls == 1 {
				if !cand.InCooldown {
					t.Fatal("poll 1 InCooldown = false, want true")
				}
				return pluginapi.SchedulerContinuityResponse{
					Decision:       pluginapi.SchedulerContinuityWait,
					PollIntervalMs: 500,
					WaitBudgetMs:   2000,
				}, true, nil
			}
			// Clear host cooldown on recovery before returning ready.
			manager.mu.Lock()
			if cur := manager.auths["codex-cooling"]; cur != nil {
				cur.Quota = QuotaState{}
				cur.NextRetryAfter = time.Time{}
			}
			manager.mu.Unlock()
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
		},
	})

	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{
			cliproxyexecutor.RequestIDMetadataKey: "req-life-1",
			cliproxyexecutor.TraceIDMetadataKey:   "trace-life-1",
		},
	}
	resp, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, opts)
	if errExec != nil {
		t.Fatalf("Execute() error = %v, want nil", errExec)
	}
	if string(resp.Payload) != `{"selected":"codex-cooling"}` {
		t.Fatalf("Payload = %s", resp.Payload)
	}
	if polls != 2 || execCalls.Load() != 1 {
		t.Fatalf("polls=%d execCalls=%d, want 2 and 1", polls, execCalls.Load())
	}
}

type testTokenStorage struct{ token string }

func (s *testTokenStorage) SaveTokenToFile(string) error { return nil }

func TestCodexContinuityWaitBudgetTighteningAndNonExtension(t *testing.T) {
	start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	clock := newFakeContinuityClock(start)
	manager := NewManager(nil, nil, nil)
	manager.SetContinuityClockForTest(clock)
	manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})

	recoverAt := time.Now().Add(10 * time.Minute)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:             "codex-1",
		Provider:       "codex",
		Status:         StatusActive,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
		NextRetryAfter: recoverAt,
	}); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })

	var polls int
	manager.SetPluginScheduler(&testContinuityScheduler{
		hasContinuity: true,
		continuityFunc: func(_ context.Context, _ pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			polls++
			switch polls {
			case 1:
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 400, WaitBudgetMs: 1000}, true, nil
			case 2:
				// Attempts to extend budget to 5000ms must be ignored.
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 400, WaitBudgetMs: 5000}, true, nil
			case 3:
				// Shortening budget to 900ms from start (when elapsed is 800ms) caps remaining wait to 100ms.
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 400, WaitBudgetMs: 900}, true, nil
			default:
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 400, WaitBudgetMs: 900}, true, nil
			}
		},
	})

	_, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("Execute() error = nil, want cooldown error after budget exhaustion")
	}
	elapsed := clock.Now().Sub(start)
	if elapsed != 900*time.Millisecond {
		t.Fatalf("elapsed = %v, want 900ms (tightened deadline, not extended)", elapsed)
	}
	if polls != 3 {
		t.Fatalf("polls = %d, want 3", polls)
	}
}

func TestCodexContinuityPreOutput429QuotaGuardAndBoundary(t *testing.T) {
	t.Run("positive quota 429 waits and redispatches on ready", func(t *testing.T) {
		clock := newFakeContinuityClock(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
		manager := NewManager(nil, nil, nil)
		manager.SetContinuityClockForTest(clock)
		manager.SetRetryConfig(1, 0, 0)

		var attempts atomic.Int32
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "codex",
			streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if attempts.Add(1) == 1 {
					return nil, codexQuotaRejectionErr{
						status:           http.StatusTooManyRequests,
						msg:              `{"error":{"code":"usage_limit_reached","message":"usage limit reached"}}`,
						credentialScoped: true,
					}
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed"}`)}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		})
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-1", Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })

		var continuityPolls int
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityPolls++
				if continuityPolls == 1 {
					return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 100, WaitBudgetMs: 1000}, true, nil
				}
				var snap *Auth
				manager.mu.Lock()
				if cur := manager.auths["codex-1"]; cur != nil {
					clearCooldownStateForAuth(cur, time.Now())
					cur.LastError = nil
					cur.Status = StatusActive
					snap = cur.Clone()
				}
				manager.mu.Unlock()
				if snap != nil && manager.scheduler != nil {
					manager.scheduler.upsertAuth(snap)
				}
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})

		res, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{Stream: true})
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v, want nil", errStream)
		}
		if res == nil || attempts.Load() != 2 || continuityPolls != 2 {
			t.Fatalf("attempts=%d continuityPolls=%d, want 2 and 2", attempts.Load(), continuityPolls)
		}
	})

	t.Run("transient capacity 429 does not enter continuity", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 0, 0)
		var attempts atomic.Int32
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "codex",
			execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				attempts.Add(1)
				return cliproxyexecutor.Response{}, codexQuotaRejectionErr{
					status:    http.StatusTooManyRequests,
					msg:       `{"error":{"code":"turn_rate_limit_exceeded","message":"at capacity"}}`,
					transient: true,
				}
			},
		})
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-1", Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })
		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 100, WaitBudgetMs: 1000}, true, nil
			},
		})

		_, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute() error = nil, want 429 error")
		}
		if continuityCalls.Load() != 0 {
			t.Fatalf("continuityCalls = %d, want 0 for transient capacity 429", continuityCalls.Load())
		}
	})

	t.Run("connection-bound websocket continuation skips continuity", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 0, 0)
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "codex",
			streamFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				return nil, codexQuotaRejectionErr{
					status:           http.StatusTooManyRequests,
					msg:              `{"error":{"code":"usage_limit_reached"}} `,
					credentialScoped: true,
				}
			},
		})
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-1", Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })
		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 100, WaitBudgetMs: 1000}, true, nil
			},
		})
		ctx := cliproxyexecutor.WithRequiredUpstreamWebsocket(context.Background())
		_, errStream := manager.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{Stream: true})
		if errStream == nil {
			t.Fatal("ExecuteStream() error = nil, want 429 error")
		}
		if continuityCalls.Load() != 0 {
			t.Fatalf("continuityCalls = %d, want 0 for connection-bound request", continuityCalls.Load())
		}
	})
}

func TestCodexContinuityRequestCancellationTerminatesWait(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
	recoverAt := time.Now().Add(10 * time.Minute)
	if _, errRegister := manager.Register(context.Background(), &Auth{
		ID:             "codex-1",
		Provider:       "codex",
		Status:         StatusActive,
		Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
		NextRetryAfter: recoverAt,
	}); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := make(chan struct{})
	manager.SetPluginScheduler(&testContinuityScheduler{
		hasContinuity: true,
		continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			select {
			case <-waiting:
			default:
				close(waiting)
			}
			return pluginapi.SchedulerContinuityResponse{
				Decision:       pluginapi.SchedulerContinuityWait,
				PollIntervalMs: 5000,
				WaitBudgetMs:   10000,
			}, true, nil
		},
	})

	errCh := make(chan error, 1)
	go func() {
		_, errExec := manager.Execute(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
		errCh <- errExec
	}()

	select {
	case <-waiting:
	case errEarly := <-errCh:
		t.Fatalf("Execute() returned early with error %v before entering wait", errEarly)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for continuity hook")
	}
	cancel()
	select {
	case errDone := <-errCh:
		if !errors.Is(errDone, context.Canceled) {
			t.Fatalf("Execute() error = %v, want context.Canceled", errDone)
		}
	case <-time.After(time.Second):
		t.Fatal("continuity wait did not terminate promptly on cancellation")
	}
}

func TestCodexContinuityScopeAndAuthorityBoundaries(t *testing.T) {
	t.Run("no waiting when active scheduler lacks capability", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
		recoverAt := time.Now().Add(time.Minute)
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:             "codex-1",
			Provider:       "codex",
			Status:         StatusActive,
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		var calls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: false,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				calls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 50, WaitBudgetMs: 1000}, true, nil
			},
		})
		_, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute() error = nil, want cooldown error")
		}
		if calls.Load() != 0 {
			t.Fatalf("calls = %d, want 0", calls.Load())
		}
	})

	t.Run("non-codex provider does not invoke continuity", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.RegisterExecutor(&continuityTestExecutor{provider: "gemini"})
		recoverAt := time.Now().Add(time.Minute)
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:             "gemini-1",
			Provider:       "gemini",
			Status:         StatusActive,
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		var calls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				calls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 50, WaitBudgetMs: 1000}, true, nil
			},
		})
		_, errExec := manager.Execute(context.Background(), []string{"gemini"}, cliproxyexecutor.Request{Model: "gemini-2.5-pro"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute() error = nil, want cooldown error")
		}
		if calls.Load() != 0 {
			t.Fatalf("calls = %d, want 0", calls.Load())
		}
	})

	t.Run("request-retry zero prevents pre-output 429 redispatch", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(0, 0, 0)
		var attempts atomic.Int32
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "codex",
			execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				attempts.Add(1)
				return cliproxyexecutor.Response{}, codexQuotaRejectionErr{
					status:           http.StatusTooManyRequests,
					msg:              `{"error":{"code":"usage_limit_reached"}}`,
					credentialScoped: true,
				}
			},
		})
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-1", Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })
		var calls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				calls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})
		_, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute() error = nil, want 429 error")
		}
		if attempts.Load() != 1 || calls.Load() != 0 {
			t.Fatalf("attempts=%d calls=%d, want 1 and 0 when request-retry=0", attempts.Load(), calls.Load())
		}
	})

	t.Run("arbitrary 429 without positive quota classification does not enter continuity", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 0, 0)
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "codex",
			execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{}, codexQuotaRejectionErr{
					status: http.StatusTooManyRequests,
					msg:    `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`,
				}
			},
		})
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-1", Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })
		var calls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				calls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})
		_, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute() error = nil, want 429 error")
		}
		if calls.Load() != 0 {
			t.Fatalf("calls = %d, want 0 for non-quota 429", calls.Load())
		}
	})

	t.Run("excludes request ExcludedAuthIDsMetadataKey candidates", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
		recoverAt := time.Now().Add(time.Minute)
		for _, id := range []string{"codex-excluded", "codex-eligible"} {
			if _, errRegister := manager.Register(context.Background(), &Auth{
				ID:             id,
				Provider:       "codex",
				Status:         StatusActive,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
				NextRetryAfter: recoverAt,
			}); errRegister != nil {
				t.Fatalf("register %s: %v", id, errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		}
		var seenIDs []string
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				for _, c := range req.Candidates {
					seenIDs = append(seenIDs, c.ID)
				}
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
			},
		})
		opts := cliproxyexecutor.Options{
			Metadata: map[string]any{
				ExcludedAuthIDsMetadataKey: []string{"codex-excluded"},
			},
		}
		_, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, opts)
		if errExec == nil {
			t.Fatal("Execute() error = nil, want cooldown error")
		}
		if len(seenIDs) != 1 || seenIDs[0] != "codex-eligible" {
			t.Fatalf("seenIDs = %v, want [codex-eligible] only", seenIDs)
		}
	})

	t.Run("mixed-provider failover where non-codex fails with quota does not enter codex continuity", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 0, 0)
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "codex",
			execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{}, codexQuotaRejectionErr{status: http.StatusInternalServerError, msg: "codex transient 500"}
			},
		})
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "antigravity",
			execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{}, codexQuotaRejectionErr{
					status:           http.StatusTooManyRequests,
					msg:              `{"error":{"code":"usage_limit_reached"}}`,
					credentialScoped: true,
				}
			},
		})
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-1", Provider: "codex", Status: StatusActive, Attributes: map[string]string{"priority": "10"}}); errRegister != nil {
			t.Fatalf("register codex: %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "ag-1", Provider: "antigravity", Status: StatusActive, Attributes: map[string]string{"priority": "1"}}); errRegister != nil {
			t.Fatalf("register ag: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		registry.GetGlobalRegistry().RegisterClient("ag-1", "antigravity", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient("codex-1")
			registry.GetGlobalRegistry().UnregisterClient("ag-1")
		})
		var calls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				calls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})
		_, errExec := manager.Execute(context.Background(), []string{"codex", "antigravity"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute() error = nil, want 429 error")
		}
		if calls.Load() != 0 {
			t.Fatalf("continuity calls = %d, want 0 when failing quota auth was non-codex", calls.Load())
		}
	})

	t.Run("rejects ready returned after existing deadline expires during callback", func(t *testing.T) {
		start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
		clock := newFakeContinuityClock(start)
		manager := NewManager(nil, nil, nil)
		manager.SetContinuityClockForTest(clock)
		var execCalls atomic.Int32
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "codex",
			execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				execCalls.Add(1)
				return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
			},
		})
		recoverAt := time.Now().Add(time.Minute)
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:             "codex-1",
			Provider:       "codex",
			Status:         StatusActive,
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })

		var polls int
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				polls++
				if polls == 1 {
					return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 400, WaitBudgetMs: 1000}, true, nil
				}
				// Advance past the 1000ms deadline while inside callback, then clear cooldown and return Ready.
				clock.Advance(600 * time.Millisecond)
				manager.mu.Lock()
				if cur := manager.auths["codex-1"]; cur != nil {
					clearCooldownStateForAuth(cur, time.Now())
					cur.Status = StatusActive
				}
				manager.mu.Unlock()
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})

		_, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute() error = nil, want failure when ready returned past deadline")
		}
		if execCalls.Load() != 0 {
			t.Fatalf("execCalls = %d, want 0 when ready returned past deadline", execCalls.Load())
		}
	})

	t.Run("continuity termination skips antigravity credits fallback", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			decision pluginapi.SchedulerContinuityDecision
		}{
			{name: "disabled", decision: pluginapi.SchedulerContinuityDisabled},
			{name: "budget_exhausted", decision: pluginapi.SchedulerContinuityWait},
		} {
			t.Run(tc.name, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				manager.SetRetryConfig(1, 0, 0)
				manager.SetConfig(&internalconfig.Config{
					QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
				})
				var agCalls atomic.Int32
				manager.RegisterExecutor(&continuityTestExecutor{
					provider: "codex",
					execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						return cliproxyexecutor.Response{}, codexQuotaRejectionErr{
							status:           http.StatusTooManyRequests,
							msg:              `{"error":{"code":"usage_limit_reached"}}`,
							credentialScoped: true,
						}
					},
					streamFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						return nil, codexQuotaRejectionErr{
							status:           http.StatusTooManyRequests,
							msg:              `{"error":{"code":"usage_limit_reached"}}`,
							credentialScoped: true,
						}
					},
				})
				manager.RegisterExecutor(&continuityTestExecutor{
					provider: "antigravity",
					execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						agCalls.Add(1)
						return cliproxyexecutor.Response{Payload: []byte(`{"ok":"ag"}`)}, nil
					},
					streamFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						agCalls.Add(1)
						ch := make(chan cliproxyexecutor.StreamChunk, 1)
						ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"ok":"ag"}`)}
						close(ch)
						return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
					},
				})

				recoverAt := time.Now().Add(time.Minute)
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:         "codex-1",
					Provider:   "codex",
					Status:     StatusActive,
					Attributes: map[string]string{"priority": "10"},
				}); errRegister != nil {
					t.Fatalf("register codex: %v", errRegister)
				}
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:             "ag-1",
					Provider:       "antigravity",
					Status:         StatusActive,
					Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
					NextRetryAfter: recoverAt,
				}); errRegister != nil {
					t.Fatalf("register ag: %v", errRegister)
				}
				const testModel = "claude-sonnet-4-6"
				registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: testModel}})
				registry.GetGlobalRegistry().RegisterClient("ag-1", "antigravity", []*registry.ModelInfo{{ID: testModel}})
				SetAntigravityCreditsHint("ag-1", AntigravityCreditsHint{Known: true, Available: true, UpdatedAt: time.Now()})
				t.Cleanup(func() {
					registry.GetGlobalRegistry().UnregisterClient("codex-1")
					registry.GetGlobalRegistry().UnregisterClient("ag-1")
					SetAntigravityCreditsHint("ag-1", AntigravityCreditsHint{})
				})

				manager.SetPluginScheduler(&testContinuityScheduler{
					hasContinuity: true,
					continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						if tc.decision == pluginapi.SchedulerContinuityWait {
							return pluginapi.SchedulerContinuityResponse{
								Decision:       pluginapi.SchedulerContinuityWait,
								PollIntervalMs: 10,
								WaitBudgetMs:   -1,
							}, true, nil
						}
						return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
					},
				})

				if _, errExec := manager.Execute(context.Background(), []string{"codex", "antigravity"}, cliproxyexecutor.Request{Model: testModel}, cliproxyexecutor.Options{}); errExec == nil {
					t.Fatal("Execute() error = nil, want 429 error")
				}
				if _, errStream := manager.ExecuteStream(context.Background(), []string{"codex", "antigravity"}, cliproxyexecutor.Request{Model: testModel}, cliproxyexecutor.Options{Stream: true}); errStream == nil {
					t.Fatal("ExecuteStream() error = nil, want 429 error")
				}
				if agCalls.Load() != 0 {
					t.Fatalf("agCalls = %d, want 0 when Codex continuity terminated", agCalls.Load())
				}
			})
		}
	})

	t.Run("empty codex pre-admission candidates do not block antigravity credits fallback", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetConfig(&internalconfig.Config{
			QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
		})
		var agCalls atomic.Int32
		manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "antigravity",
			execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				agCalls.Add(1)
				return cliproxyexecutor.Response{Payload: []byte(`{"ok":"ag-credits"}`)}, nil
			},
		})
		recoverAt := time.Now().Add(time.Minute)
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:       "codex-disabled",
			Provider: "codex",
			Status:   StatusDisabled,
			Disabled: true,
		}); errRegister != nil {
			t.Fatalf("register codex: %v", errRegister)
		}
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:             "ag-1",
			Provider:       "antigravity",
			Status:         StatusActive,
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		}); errRegister != nil {
			t.Fatalf("register ag: %v", errRegister)
		}
		const testModel = "claude-sonnet-4-6"
		registry.GetGlobalRegistry().RegisterClient("codex-disabled", "codex", []*registry.ModelInfo{{ID: testModel}})
		registry.GetGlobalRegistry().RegisterClient("ag-1", "antigravity", []*registry.ModelInfo{{ID: testModel}})
		SetAntigravityCreditsHint("ag-1", AntigravityCreditsHint{Known: true, Available: true, UpdatedAt: time.Now()})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient("codex-disabled")
			registry.GetGlobalRegistry().UnregisterClient("ag-1")
			SetAntigravityCreditsHint("ag-1", AntigravityCreditsHint{})
		})
		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
			},
		})
		resp, errExec := manager.Execute(context.Background(), []string{"codex", "antigravity"}, cliproxyexecutor.Request{Model: testModel}, cliproxyexecutor.Options{})
		if errExec != nil {
			t.Fatalf("Execute() error = %v, want antigravity credits fallback success", errExec)
		}
		if string(resp.Payload) != `{"ok":"ag-credits"}` || agCalls.Load() != 1 || continuityCalls.Load() != 0 {
			t.Fatalf("resp=%s agCalls=%d continuityCalls=%d, want ag-credits/1/0", resp.Payload, agCalls.Load(), continuityCalls.Load())
		}
	})

	t.Run("non-quota force cooldown and cloudflare challenge are excluded from continuity candidates", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
		now := time.Now()
		recoverAt := now.Add(time.Minute)
		for _, a := range []*Auth{
			{
				ID:             "codex-force-503",
				Provider:       "codex",
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: recoverAt,
				LastError:      &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "upstream 503 force cooldown"},
			},
			{
				ID:       "codex-cf-challenge",
				Provider: "codex",
				Status:   StatusError,
				ModelStates: map[string]*ModelState{
					"gpt-5-codex": {
						Status:         StatusError,
						Unavailable:    true,
						NextRetryAfter: recoverAt,
						Quota:          QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: recoverAt},
					},
				},
			},
			{
				ID:             "codex-real-quota",
				Provider:       "codex",
				Status:         StatusActive,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
				NextRetryAfter: recoverAt,
			},
		} {
			if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
				t.Fatalf("register %s: %v", a.ID, errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
		}

		candidates := manager.buildCodexContinuityCandidates(context.Background(), []string{"codex"}, "gpt-5-codex", cliproxyexecutor.Options{}, 0, false, 0, now)
		if len(candidates) != 1 || candidates[0].ID != "codex-real-quota" || !candidates[0].InCooldown {
			t.Fatalf("candidates = %+v, want only codex-real-quota in cooldown", candidates)
		}

		// When only the non-quota 503 force-cooldown auth exists, Execute must fail without invoking continuity.
		managerOnly503 := NewManager(nil, nil, nil)
		managerOnly503.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
		if _, errRegister := managerOnly503.Register(context.Background(), &Auth{
			ID:       "codex-only-force-503",
			Provider: "codex",
			Status:   StatusActive,
		}); errRegister != nil {
			t.Fatalf("register only-503: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-only-force-503", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-only-force-503") })
		managerOnly503.MarkResult(context.Background(), Result{
			AuthID:   "codex-only-force-503",
			Provider: "codex",
			Error:    &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "force 503"},
		})
		var calls atomic.Int32
		managerOnly503.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				calls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 50, WaitBudgetMs: 500}, true, nil
			},
		})
		_, errExec := managerOnly503.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
		if errExec == nil {
			t.Fatal("Execute() error = nil, want cooldown error")
		}
		if calls.Load() != 0 {
			t.Fatalf("continuity calls = %d, want 0 for non-quota 503 force cooldown", calls.Load())
		}
	})

	t.Run("weighted selector excludes zero-weight candidates from continuity", func(t *testing.T) {
		for _, withAffinity := range []bool{false, true} {
			name := "bare_weighted"
			var selector Selector = &WeightedRoundRobinSelector{}
			if withAffinity {
				name = "affinity_weighted"
				affinity := NewSessionAffinitySelector(selector)
				t.Cleanup(affinity.Stop)
				selector = affinity
			}
			t.Run(name, func(t *testing.T) {
				clock := newFakeContinuityClock(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
				manager := NewManager(nil, selector, nil)
				manager.SetContinuityClockForTest(clock)
				manager.RegisterExecutor(&continuityTestExecutor{
					provider: "codex",
					execFn: func(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						return cliproxyexecutor.Response{Payload: []byte(`{"picked":"` + a.ID + `"}`)}, nil
					},
				})
				recoverAt := time.Now().Add(time.Minute)
				for _, a := range []*Auth{
					{
						ID:             "codex-weighted-cooling",
						Provider:       "codex",
						Status:         StatusActive,
						Attributes:     map[string]string{AttributeWeight: "2"},
						Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
						NextRetryAfter: recoverAt,
					},
					{
						ID:       "codex-zero-attr",
						Provider: "codex",
						Status:   StatusActive,
						Attributes: map[string]string{
							AttributeWeight: "0",
						},
					},
					{
						ID:       "codex-zero-meta",
						Provider: "codex",
						Status:   StatusActive,
						Metadata: map[string]any{AttributeWeight: 0},
					},
				} {
					if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
						t.Fatalf("register %s: %v", a.ID, errRegister)
					}
					registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
					t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
				}

				var seenCandidates [][]string
				manager.SetPluginScheduler(&testContinuityScheduler{
					hasContinuity: true,
					continuityFunc: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						ids := make([]string, 0, len(req.Candidates))
						for _, c := range req.Candidates {
							ids = append(ids, c.ID)
						}
						seenCandidates = append(seenCandidates, ids)
						if len(seenCandidates) == 1 {
							return pluginapi.SchedulerContinuityResponse{
								Decision:       pluginapi.SchedulerContinuityWait,
								PollIntervalMs: 50,
								WaitBudgetMs:   500,
							}, true, nil
						}
						var snap *Auth
						manager.mu.Lock()
						if cur := manager.auths["codex-weighted-cooling"]; cur != nil {
							clearCooldownStateForAuth(cur, time.Now())
							cur.Status = StatusActive
							snap = cur.Clone()
						}
						manager.mu.Unlock()
						if snap != nil && manager.scheduler != nil {
							manager.scheduler.upsertAuth(snap)
						}
						return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
					},
				})

				resp, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
				if errExec != nil {
					t.Fatalf("Execute() error = %v", errExec)
				}
				if string(resp.Payload) != `{"picked":"codex-weighted-cooling"}` {
					t.Fatalf("Payload = %s, want codex-weighted-cooling", resp.Payload)
				}
				if len(seenCandidates) != 2 {
					t.Fatalf("seenCandidates = %v, want 2 polls", seenCandidates)
				}
				for _, ids := range seenCandidates {
					if len(ids) != 1 || ids[0] != "codex-weighted-cooling" {
						t.Fatalf("candidate IDs = %v, want only [codex-weighted-cooling]", ids)
					}
				}
			})
		}
	})

	t.Run("json quota_exceeded in Error() and ResponseBody() enters pre_output_429 continuity", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			err  error
		}{
			{
				name: "error_string",
				err: codexQuotaRejectionErr{
					status: http.StatusTooManyRequests,
					msg:    `{"error":{"code":"quota_exceeded","message":"quota exceeded"}}`,
				},
			},
			{
				name: "response_body",
				err: codexResponseBodyQuotaErr{
					status: http.StatusTooManyRequests,
					body:   []byte(`{"error":{"code":"quota_exceeded","message":"quota exceeded"}}`),
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				clock := newFakeContinuityClock(time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC))
				manager := NewManager(nil, nil, nil)
				manager.SetContinuityClockForTest(clock)
				manager.SetRetryConfig(1, 0, 0)
				var attempts atomic.Int32
				manager.RegisterExecutor(&continuityTestExecutor{
					provider: "codex",
					execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						if attempts.Add(1) == 1 {
							return cliproxyexecutor.Response{}, tc.err
						}
						return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
					},
				})
				if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-1", Provider: "codex", Status: StatusActive}); errRegister != nil {
					t.Fatalf("register: %v", errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient("codex-1", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-1") })
				var calls atomic.Int32
				manager.SetPluginScheduler(&testContinuityScheduler{
					hasContinuity: true,
					continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						if calls.Add(1) == 1 {
							return pluginapi.SchedulerContinuityResponse{
								Decision:       pluginapi.SchedulerContinuityWait,
								PollIntervalMs: 25,
								WaitBudgetMs:   500,
							}, true, nil
						}
						var snap *Auth
						manager.mu.Lock()
						if cur := manager.auths["codex-1"]; cur != nil {
							clearCooldownStateForAuth(cur, time.Now())
							cur.LastError = nil
							cur.Status = StatusActive
							snap = cur.Clone()
						}
						manager.mu.Unlock()
						if snap != nil && manager.scheduler != nil {
							manager.scheduler.upsertAuth(snap)
						}
						return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
					},
				})
				resp, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
				if errExec != nil {
					t.Fatalf("Execute() error = %v", errExec)
				}
				if string(resp.Payload) != `{"ok":true}` || attempts.Load() != 2 || calls.Load() != 2 {
					t.Fatalf("resp=%s attempts=%d calls=%d, want ok/2/2", resp.Payload, attempts.Load(), calls.Load())
				}
			})
		}
	})

	t.Run("ready while host remains in cooldown cannot exceed continuity deadline via waitForCooldown", func(t *testing.T) {
		for _, streamMode := range []bool{false, true} {
			name := "execute"
			if streamMode {
				name = "execute_stream"
			}
			t.Run(name, func(t *testing.T) {
				start := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
				clock := newFakeContinuityClock(start)
				manager := NewManager(nil, nil, nil)
				manager.SetContinuityClockForTest(clock)
				manager.SetRetryConfig(2, 30*time.Second, 0)
				var execAttempts atomic.Int32
				manager.RegisterExecutor(&continuityTestExecutor{
					provider: "codex",
					execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						execAttempts.Add(1)
						return cliproxyexecutor.Response{Payload: []byte(`{"unexpected":true}`)}, nil
					},
					streamFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						execAttempts.Add(1)
						ch := make(chan cliproxyexecutor.StreamChunk, 1)
						ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`{"unexpected":true}`)}
						close(ch)
						return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
					},
				})
				recoverAt := time.Now().Add(10 * time.Second)
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:             "codex-long-cooldown",
					Provider:       "codex",
					Status:         StatusActive,
					Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
					NextRetryAfter: recoverAt,
				}); errRegister != nil {
					t.Fatalf("register: %v", errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient("codex-long-cooldown", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-long-cooldown") })

				var calls atomic.Int32
				manager.SetPluginScheduler(&testContinuityScheduler{
					hasContinuity: true,
					continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						if calls.Add(1) == 1 {
							return pluginapi.SchedulerContinuityResponse{
								Decision:       pluginapi.SchedulerContinuityWait,
								PollIntervalMs: 50,
								WaitBudgetMs:   200,
							}, true, nil
						}
						// Return ready even though the host credential stays in 10s cooldown.
						return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
					},
				})

				var errExec error
				if streamMode {
					_, errExec = manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{Stream: true})
				} else {
					_, errExec = manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{})
				}
				if errExec == nil {
					t.Fatal("expected cooldown error, got nil")
				}
				if !IsContinuityTerminatedError(errExec) {
					t.Fatalf("expected continuity-terminated error, got %v", errExec)
				}
				if execAttempts.Load() != 0 {
					t.Fatalf("execAttempts = %d, want 0", execAttempts.Load())
				}
				if elapsed := clock.Now().Sub(start); elapsed > 200*time.Millisecond {
					t.Fatalf("elapsed = %v, want <= 200ms continuity budget", elapsed)
				}
			})
		}
	})

	t.Run("compound cooldown with active non-quota shadow excludes candidate regardless of deadline order", func(t *testing.T) {
		now := time.Now()
		for _, tc := range []struct {
			name string
			auth *Auth
		}{
			{
				name: "shadow_503_force_shorter_than_credential_quota",
				auth: &Auth{
					ID:             "codex-compound-short-503",
					Provider:       "codex",
					Status:         StatusActive,
					Quota:          QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(10 * time.Minute)},
					NextRetryAfter: now.Add(10 * time.Minute),
					ModelStates: map[string]*ModelState{
						"gpt-5-codex": {
							Unavailable:               true,
							Status:                    StatusError,
							Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(10 * time.Minute)},
							NextRetryAfter:            now.Add(10 * time.Minute),
							credentialQuotaPropagated: true,
							credentialQuotaShadow: &ModelState{
								Unavailable:         true,
								Status:              StatusError,
								NextRetryAfter:      now.Add(time.Minute),
								forceCooldownUntil:  now.Add(time.Minute),
								forceCooldownSeeded: true,
								LastError:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "forced 503"},
							},
						},
					},
				},
			},
			{
				name: "shadow_503_force_longer_than_credential_quota",
				auth: &Auth{
					ID:             "codex-compound-long-503",
					Provider:       "codex",
					Status:         StatusActive,
					Quota:          QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(10 * time.Minute)},
					NextRetryAfter: now.Add(10 * time.Minute),
					ModelStates: map[string]*ModelState{
						"gpt-5-codex": {
							Unavailable:               true,
							Status:                    StatusError,
							Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(10 * time.Minute)},
							NextRetryAfter:            now.Add(20 * time.Minute),
							credentialQuotaPropagated: true,
							credentialQuotaShadow: &ModelState{
								Unavailable:         true,
								Status:              StatusError,
								NextRetryAfter:      now.Add(20 * time.Minute),
								forceCooldownUntil:  now.Add(20 * time.Minute),
								forceCooldownSeeded: true,
								LastError:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "forced 503"},
							},
						},
					},
				},
			},
			{
				name: "shadow_cloudflare_shorter_than_credential_quota",
				auth: &Auth{
					ID:             "codex-compound-short-cf",
					Provider:       "codex",
					Status:         StatusActive,
					Quota:          QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(10 * time.Minute)},
					NextRetryAfter: now.Add(10 * time.Minute),
					ModelStates: map[string]*ModelState{
						"gpt-5-codex": {
							Unavailable:               true,
							Status:                    StatusError,
							Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(10 * time.Minute)},
							NextRetryAfter:            now.Add(10 * time.Minute),
							credentialQuotaPropagated: true,
							credentialQuotaShadow: &ModelState{
								Unavailable:    true,
								Status:         StatusError,
								Quota:          QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: now.Add(time.Minute)},
								NextRetryAfter: now.Add(time.Minute),
							},
						},
					},
				},
			},
			{
				name: "auth_force_503_shorter_than_credential_quota",
				auth: &Auth{
					ID:                  "codex-auth-short-503",
					Provider:            "codex",
					Status:              StatusActive,
					Quota:               QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(10 * time.Minute)},
					NextRetryAfter:      now.Add(10 * time.Minute),
					forceCooldownUntil:  now.Add(time.Minute),
					forceCooldownSeeded: true,
					LastError:           &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "forced 503"},
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
				if _, errRegister := manager.Register(context.Background(), tc.auth); errRegister != nil {
					t.Fatalf("register: %v", errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(tc.auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(tc.auth.ID) })

				var calls atomic.Int32
				manager.SetPluginScheduler(&testContinuityScheduler{
					hasContinuity: true,
					continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						calls.Add(1)
						return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
					},
				})
				if _, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{}); errExec == nil {
					t.Fatal("Execute() error = nil, want cooldown error")
				}
				if calls.Load() != 0 {
					t.Fatalf("continuity calls = %d, want 0 when active non-quota shadow/force cooldown exists", calls.Load())
				}
			})
		}
	})

	t.Run("MarkResult sequential compound failures exclude non-quota cooldown regardless of write order or deadline order", func(t *testing.T) {
		oneMin := time.Minute
		tenMin := 10 * time.Minute
		forced503 := &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "forced 503"}
		ordinary503 := &Error{Code: "upstream_503", HTTPStatus: http.StatusServiceUnavailable, Message: "ordinary 503"}
		quota429 := &Error{Code: "usage_limit_reached", HTTPStatus: http.StatusTooManyRequests, Message: "usage limit reached"}

		for _, tc := range []struct {
			name  string
			steps []Result
		}{
			{
				name: "auth_503_then_cred_quota_429",
				steps: []Result{
					{AuthID: "codex-seq", Provider: "codex", RetryAfter: &oneMin, Error: forced503},
					{AuthID: "codex-seq", Provider: "codex", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
				},
			},
			{
				name: "cred_quota_429_then_auth_503",
				steps: []Result{
					{AuthID: "codex-seq", Provider: "codex", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
					{AuthID: "codex-seq", Provider: "codex", RetryAfter: &oneMin, Error: forced503},
				},
			},
			{
				name: "model_503_then_same_model_cred_quota_429",
				steps: []Result{
					{AuthID: "codex-seq", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: forced503},
					{AuthID: "codex-seq", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
				},
			},
			{
				name: "model_503_then_other_model_cred_quota_429",
				steps: []Result{
					{AuthID: "codex-seq", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: forced503},
					{AuthID: "codex-seq", Provider: "codex", Model: "gpt-5.4", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
				},
			},
			{
				name: "cred_quota_429_then_model_503",
				steps: []Result{
					{AuthID: "codex-seq", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
					{AuthID: "codex-seq", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: forced503},
				},
			},
			{
				name: "auth_cred_scope_ordinary_503_then_cred_quota_429",
				steps: []Result{
					{AuthID: "codex-seq", Provider: "codex", CredentialScope: true, RetryAfter: &oneMin, Error: ordinary503},
					{AuthID: "codex-seq", Provider: "codex", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				manager := NewManager(nil, nil, nil)
				manager.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
				if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-seq", Provider: "codex", Status: StatusActive}); errRegister != nil {
					t.Fatalf("register: %v", errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient("codex-seq", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}, {ID: "gpt-5.4"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-seq") })

				for _, step := range tc.steps {
					manager.MarkResult(context.Background(), step)
				}

				var calls atomic.Int32
				manager.SetPluginScheduler(&testContinuityScheduler{
					hasContinuity: true,
					continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						calls.Add(1)
						return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
					},
				})
				if _, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{}); errExec == nil {
					t.Fatal("Execute() error = nil, want cooldown error")
				}
				if calls.Load() != 0 {
					t.Fatalf("continuity calls = %d, want 0 when non-quota cooldown layer is active", calls.Load())
				}
			})
		}
	})

	t.Run("credential-scoped non-429 error does not enter pre_output_429 continuity", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(2, 0, 0)
		var execCalls atomic.Int32
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "codex",
			execFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				execCalls.Add(1)
				return cliproxyexecutor.Response{}, codexCredScoped503Err{}
			},
		})
		if _, errRegister := manager.Register(context.Background(), &Auth{ID: "codex-cred-503", Provider: "codex", Status: StatusActive}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-cred-503", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-cred-503") })

		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityWait, PollIntervalMs: 10, WaitBudgetMs: 200}, true, nil
			},
		})
		if _, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{}); errExec == nil {
			t.Fatal("Execute() error = nil, want 503 error")
		}
		if continuityCalls.Load() != 0 {
			t.Fatalf("continuityCalls = %d, want 0 for credential-scoped 503", continuityCalls.Load())
		}
		if execCalls.Load() == 0 {
			t.Fatal("expected upstream execution attempt")
		}
	})

	t.Run("persisted sequential MarkResult compound cooldowns preserve non-quota exclusion across restart", func(t *testing.T) {
		oneMin := time.Minute
		tenMin := 10 * time.Minute
		forced503 := &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "forced 503"}
		quota429 := &Error{Code: "usage_limit_reached", HTTPStatus: http.StatusTooManyRequests, Message: "usage limit reached"}

		for _, tc := range []struct {
			name  string
			steps []Result
		}{
			{
				name: "auth_503_then_cred_quota_429",
				steps: []Result{
					{AuthID: "codex-persist-seq", Provider: "codex", RetryAfter: &oneMin, Error: forced503},
					{AuthID: "codex-persist-seq", Provider: "codex", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
				},
			},
			{
				name: "model_503_then_same_model_cred_quota_429",
				steps: []Result{
					{AuthID: "codex-persist-seq", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: forced503},
					{AuthID: "codex-persist-seq", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
				},
			},
			{
				name: "model_503_then_other_model_cred_quota_429",
				steps: []Result{
					{AuthID: "codex-persist-seq", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: forced503},
					{AuthID: "codex-persist-seq", Provider: "codex", Model: "gpt-5.4", CredentialScope: true, RetryAfter: &tenMin, Error: quota429},
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := t.TempDir()
				store1 := NewFileCooldownStateStore(dir)
				mgr1 := NewManager(nil, nil, nil)
				mgr1.SetCooldownStateStore(store1)
				if _, errReg := mgr1.Register(context.Background(), &Auth{ID: "codex-persist-seq", Provider: "codex", Status: StatusActive}); errReg != nil {
					t.Fatalf("register mgr1: %v", errReg)
				}
				registry.GetGlobalRegistry().RegisterClient("codex-persist-seq", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}, {ID: "gpt-5.4"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-persist-seq") })

				for _, step := range tc.steps {
					mgr1.MarkResult(context.Background(), step)
				}
				mgr1.PersistCooldownStates(context.Background())

				store2 := NewFileCooldownStateStore(dir)
				mgr2 := NewManager(nil, nil, nil)
				mgr2.SetCooldownStateStore(store2)
				mgr2.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
				if _, errReg := mgr2.Register(context.Background(), &Auth{ID: "codex-persist-seq", Provider: "codex", Status: StatusActive}); errReg != nil {
					t.Fatalf("register mgr2: %v", errReg)
				}
				if errRestore := mgr2.RestoreCooldownStates(context.Background()); errRestore != nil {
					t.Fatalf("RestoreCooldownStates: %v", errRestore)
				}

				var calls atomic.Int32
				mgr2.SetPluginScheduler(&testContinuityScheduler{
					hasContinuity: true,
					continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						calls.Add(1)
						return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
					},
				})
				if _, errExec := mgr2.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5-codex"}, cliproxyexecutor.Options{}); errExec == nil {
					t.Fatal("Execute() error = nil, want cooldown error")
				}
				if calls.Load() != 0 {
					t.Fatalf("continuity calls = %d, want 0 after restoring persisted non-quota cooldown", calls.Load())
				}
			})
		}
	})

	t.Run("expired shorter non-quota layer restores candidate eligibility during longer quota force cooldown", func(t *testing.T) {
		oneMin := time.Minute
		tenMin := 10 * time.Minute
		forcedQuota429 := &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "forced quota 429"}
		model503 := &Error{Code: "upstream_error", HTTPStatus: http.StatusServiceUnavailable, Message: "model 503"}

		for _, tc := range []struct {
			name    string
			persist bool
			steps   []Result
		}{
			{
				name: "cred_quota_force_10m_then_model_503_1m",
				steps: []Result{
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &tenMin, Error: forcedQuota429},
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: model503},
				},
			},
			{
				name: "model_quota_force_10m_then_model_503_1m",
				steps: []Result{
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &tenMin, Error: forcedQuota429},
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: model503},
				},
			},
			{
				name: "quota_force_10m_then_cred_503_1m",
				steps: []Result{
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &tenMin, Error: forcedQuota429},
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &oneMin, Error: model503},
				},
			},
			{
				name:    "persisted_quota_force_10m_then_cred_503_1m",
				persist: true,
				steps: []Result{
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &tenMin, Error: forcedQuota429},
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &oneMin, Error: model503},
				},
			},
			{
				name: "model_quota_force_10m_then_cloudflare_1m",
				steps: []Result{
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &tenMin, Error: forcedQuota429},
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", Error: &Error{Code: "cloudflare_challenge", HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge"}},
				},
			},
			{
				name:    "persisted_model_quota_force_10m_then_cloudflare_1m",
				persist: true,
				steps: []Result{
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &tenMin, Error: forcedQuota429},
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", Error: &Error{Code: "cloudflare_challenge", HTTPStatus: http.StatusForbidden, Message: "cloudflare challenge"}},
				},
			},
			{
				name:    "persisted_cred_quota_force_10m_then_model_503_1m",
				persist: true,
				steps: []Result{
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", CredentialScope: true, RetryAfter: &tenMin, Error: forcedQuota429},
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: model503},
				},
			},
			{
				name:    "persisted_model_quota_force_10m_then_model_503_1m",
				persist: true,
				steps: []Result{
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &tenMin, Error: forcedQuota429},
					{AuthID: "codex-expire-nonquota", Provider: "codex", Model: "gpt-5-codex", RetryAfter: &oneMin, Error: model503},
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := t.TempDir()
				mgr := NewManager(nil, nil, nil)
				if tc.persist {
					mgr.SetCooldownStateStore(NewFileCooldownStateStore(dir))
				}
				mgr.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
				if _, errReg := mgr.Register(context.Background(), &Auth{ID: "codex-expire-nonquota", Provider: "codex", Status: StatusActive}); errReg != nil {
					t.Fatalf("register: %v", errReg)
				}
				registry.GetGlobalRegistry().RegisterClient("codex-expire-nonquota", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-expire-nonquota") })

				for _, step := range tc.steps {
					mgr.MarkResult(context.Background(), step)
				}
				if tc.persist {
					mgr.PersistCooldownStates(context.Background())
				}

				future := time.Now().Add(2 * time.Minute)
				targetMgr := mgr
				if tc.persist {
					targetMgr = NewManager(nil, nil, nil)
					targetMgr.SetCooldownStateStore(NewFileCooldownStateStore(dir))
					targetMgr.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
					if _, errReg := targetMgr.Register(context.Background(), &Auth{ID: "codex-expire-nonquota", Provider: "codex", Status: StatusActive}); errReg != nil {
						t.Fatalf("register restored: %v", errReg)
					}
					if errRestore := targetMgr.RestoreCooldownStates(context.Background()); errRestore != nil {
						t.Fatalf("RestoreCooldownStates: %v", errRestore)
					}
				}

				targetMgr.mu.RLock()
				authSnap := targetMgr.auths["codex-expire-nonquota"]
				excludedDuringNonQuota := isQuotaCooldownBlock(authSnap, "gpt-5-codex", time.Now().Add(5*time.Second))
				eligible := isQuotaCooldownBlock(authSnap, "gpt-5-codex", future)
				targetMgr.mu.RUnlock()
				if excludedDuringNonQuota {
					t.Fatal("expected candidate to be excluded while shorter non-quota cooldown is still active")
				}
				if !eligible {
					t.Fatal("expected candidate to recover quota-cooldown eligibility once shorter non-quota cooldown expired")
				}
			})
		}
	})

	t.Run("mixed-provider opened non-codex stream chunk quota 429 does not enter codex pre_output_429 gate", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 0, 0)
		manager.RegisterExecutor(&continuityTestExecutor{
			provider: "claude",
			streamFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Err: &Error{
					Code:       "usage_limit_reached",
					HTTPStatus: http.StatusTooManyRequests,
					Message:    "quota exhausted",
				}}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		})
		if _, errReg := manager.Register(context.Background(), &Auth{ID: "claude-stream-mixed", Provider: "claude", Status: StatusActive}); errReg != nil {
			t.Fatalf("register: %v", errReg)
		}
		registry.GetGlobalRegistry().RegisterClient("claude-stream-mixed", "claude", []*registry.ModelInfo{{ID: "shared-model"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("claude-stream-mixed") })

		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&testContinuityScheduler{
			hasContinuity: true,
			continuityFunc: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
			},
		})

		ctx := WithRequestContinuitySession(context.Background())
		res, errStream := manager.ExecuteStream(ctx, []string{"codex", "claude"}, cliproxyexecutor.Request{Model: "shared-model"}, cliproxyexecutor.Options{})
		if errStream != nil {
			t.Fatalf("ExecuteStream error = %v", errStream)
		}
		chunk := <-res.Chunks
		if chunk.Err == nil {
			t.Fatal("expected async chunk.Err")
		}
		proceed, gateErr := manager.EvaluatePreOutputStreamQuotaGate(ctx, []string{"codex", "claude"}, "shared-model", cliproxyexecutor.Options{}, chunk.Err)
		if !proceed || gateErr != nil {
			t.Fatalf("EvaluatePreOutputStreamQuotaGate() = (%v, %v), want (true, nil) for non-codex stream", proceed, gateErr)
		}
		if continuityCalls.Load() != 0 {
			t.Fatalf("continuityCalls = %d, want 0 for non-codex stream error", continuityCalls.Load())
		}
	})

	t.Run("disable_cooling transient 429 after forced quota cooldown does not populate nonQuotaCooldownUntil", func(t *testing.T) {
		tenMin := 10 * time.Minute
		forcedQuota429 := &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "forced quota 429"}
		transient429 := &Error{Code: "turn_rate_limit_exceeded", HTTPStatus: http.StatusTooManyRequests, Message: "at capacity"}

		for _, persist := range []bool{false, true} {
			name := "in_memory"
			if persist {
				name = "persisted"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				mgr := NewManager(nil, nil, nil)
				if persist {
					mgr.SetCooldownStateStore(NewFileCooldownStateStore(dir))
				}
				mgr.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
				if _, errReg := mgr.Register(context.Background(), &Auth{
					ID:       "codex-dc-transient-after-force",
					Provider: "codex",
					Status:   StatusActive,
					Metadata: map[string]any{"disable_cooling": true},
				}); errReg != nil {
					t.Fatalf("register: %v", errReg)
				}
				registry.GetGlobalRegistry().RegisterClient("codex-dc-transient-after-force", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-dc-transient-after-force") })

				mgr.MarkResult(context.Background(), Result{
					AuthID:     "codex-dc-transient-after-force",
					Provider:   "codex",
					RetryAfter: &tenMin,
					Error:      forcedQuota429,
				})
				mgr.MarkResult(context.Background(), Result{
					AuthID:   "codex-dc-transient-after-force",
					Provider: "codex",
					Error:    transient429,
				})
				if persist {
					mgr.PersistCooldownStates(context.Background())
				}

				targetMgr := mgr
				if persist {
					targetMgr = NewManager(nil, nil, nil)
					targetMgr.SetCooldownStateStore(NewFileCooldownStateStore(dir))
					targetMgr.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
					if _, errReg := targetMgr.Register(context.Background(), &Auth{
						ID:       "codex-dc-transient-after-force",
						Provider: "codex",
						Status:   StatusActive,
						Metadata: map[string]any{"disable_cooling": true},
					}); errReg != nil {
						t.Fatalf("register restored: %v", errReg)
					}
					if errRestore := targetMgr.RestoreCooldownStates(context.Background()); errRestore != nil {
						t.Fatalf("RestoreCooldownStates: %v", errRestore)
					}
				}

				targetMgr.mu.RLock()
				authSnap := targetMgr.auths["codex-dc-transient-after-force"]
				nonQuotaSet := !authSnap.nonQuotaCooldownUntil.IsZero()
				eligible := isQuotaCooldownBlock(authSnap, "gpt-5-codex", time.Now().Add(time.Minute))
				targetMgr.mu.RUnlock()
				if nonQuotaSet {
					t.Fatal("expected nonQuotaCooldownUntil to remain zero on disable_cooling transient 429")
				}
				if !eligible {
					t.Fatal("expected auth to remain eligible as quota-cooldown candidate")
				}
			})
		}
	})

	t.Run("auth-level forced quota cooldown remains eligible alongside healthy or non-matching ModelStates", func(t *testing.T) {
		tenMin := 10 * time.Minute
		forcedQuota429 := &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests, Message: "forced quota 429"}

		for _, tc := range []struct {
			name        string
			existingMod string
			checkMod    string
			persist     bool
		}{
			{name: "healthy_matching_model_in_memory", existingMod: "gpt-5-codex", checkMod: "gpt-5-codex", persist: false},
			{name: "healthy_matching_model_persisted", existingMod: "gpt-5-codex", checkMod: "gpt-5-codex", persist: true},
			{name: "non_matching_model_in_memory", existingMod: "gpt-5.4", checkMod: "gpt-5-codex", persist: false},
			{name: "non_matching_model_persisted", existingMod: "gpt-5.4", checkMod: "gpt-5-codex", persist: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir := t.TempDir()
				mgr := NewManager(nil, nil, nil)
				if tc.persist {
					mgr.SetCooldownStateStore(NewFileCooldownStateStore(dir))
				}
				mgr.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
				if _, errReg := mgr.Register(context.Background(), &Auth{
					ID:       "codex-auth-force-with-models",
					Provider: "codex",
					Status:   StatusActive,
					ModelStates: map[string]*ModelState{
						tc.existingMod: {Status: StatusActive},
					},
				}); errReg != nil {
					t.Fatalf("register: %v", errReg)
				}
				registry.GetGlobalRegistry().RegisterClient("codex-auth-force-with-models", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}, {ID: "gpt-5.4"}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-auth-force-with-models") })

				mgr.MarkResult(context.Background(), Result{
					AuthID:     "codex-auth-force-with-models",
					Provider:   "codex",
					RetryAfter: &tenMin,
					Error:      forcedQuota429,
				})
				if tc.persist {
					mgr.PersistCooldownStates(context.Background())
				}

				targetMgr := mgr
				if tc.persist {
					targetMgr = NewManager(nil, nil, nil)
					targetMgr.SetCooldownStateStore(NewFileCooldownStateStore(dir))
					targetMgr.RegisterExecutor(&continuityTestExecutor{provider: "codex"})
					if _, errReg := targetMgr.Register(context.Background(), &Auth{
						ID:       "codex-auth-force-with-models",
						Provider: "codex",
						Status:   StatusActive,
						ModelStates: map[string]*ModelState{
							tc.existingMod: {Status: StatusActive},
						},
					}); errReg != nil {
						t.Fatalf("register restored: %v", errReg)
					}
					if errRestore := targetMgr.RestoreCooldownStates(context.Background()); errRestore != nil {
						t.Fatalf("RestoreCooldownStates: %v", errRestore)
					}
				}

				var continuityCalls atomic.Int32
				targetMgr.SetPluginScheduler(&testContinuityScheduler{
					hasContinuity: true,
					continuityFunc: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
						continuityCalls.Add(1)
						if len(req.Candidates) != 1 || req.Candidates[0].ID != "codex-auth-force-with-models" {
							t.Fatalf("Candidates = %#v, want [codex-auth-force-with-models]", req.Candidates)
						}
						return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
					},
				})
				if _, errExec := targetMgr.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: tc.checkMod}, cliproxyexecutor.Options{}); errExec == nil {
					t.Fatal("Execute() error = nil, want cooldown error")
				}
				if continuityCalls.Load() != 1 {
					t.Fatalf("continuityCalls = %d, want 1", continuityCalls.Load())
				}
			})
		}
	})
}

type codexResponseBodyQuotaErr struct {
	status int
	body   []byte
}

func (e codexResponseBodyQuotaErr) Error() string        { return "upstream 429" }
func (e codexResponseBodyQuotaErr) StatusCode() int      { return e.status }
func (e codexResponseBodyQuotaErr) ResponseBody() []byte { return e.body }

type codexCredScoped503Err struct{}

func (codexCredScoped503Err) Error() string            { return "upstream 503" }
func (codexCredScoped503Err) StatusCode() int          { return http.StatusServiceUnavailable }
func (codexCredScoped503Err) IsCredentialScoped() bool { return true }
