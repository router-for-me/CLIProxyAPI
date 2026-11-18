package openai

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type sseContinuityScheduler struct {
	pickFn       func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error)
	continuityFn func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error)
}

func (s *sseContinuityScheduler) PickAuth(ctx context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	if s.pickFn != nil {
		return s.pickFn(ctx, req)
	}
	return pluginapi.SchedulerPickResponse{}, false, nil
}
func (s *sseContinuityScheduler) HasSchedulerContinuity() bool { return true }
func (s *sseContinuityScheduler) EvaluateSchedulerContinuity(ctx context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
	return s.continuityFn(ctx, req)
}

type sseContinuityCodexExecutor struct {
	calls atomic.Int32
}

func (e *sseContinuityCodexExecutor) Identifier() string { return "codex" }
func (e *sseContinuityCodexExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *sseContinuityCodexExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"id":"resp-ok","output":[]}}

`)}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}
func (e *sseContinuityCodexExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (e *sseContinuityCodexExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *sseContinuityCodexExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestResponsesSSEWaitOnlyKeepAliveDuringContinuityWait(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("emits SSE keep-alive comment only while waiting", func(t *testing.T) {
		tickCh := make(chan time.Time)
		restore := handlers.SetContinuityKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
			return tickCh, func() {}
		})
		defer restore()

		manager := coreauth.NewManager(nil, nil, nil)
		exec := &sseContinuityCodexExecutor{}
		manager.RegisterExecutor(exec)
		recoverAt := time.Now().Add(time.Minute)
		auth := &coreauth.Auth{
			ID:             "codex-sse-1",
			Provider:       "codex",
			Status:         coreauth.StatusActive,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

		var mu sync.Mutex
		polls := 0
		waitStarted := make(chan struct{})
		releaseReady := make(chan struct{})
		manager.SetPluginScheduler(&sseContinuityScheduler{
			continuityFn: func(_ context.Context, _ pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				mu.Lock()
				polls++
				cur := polls
				mu.Unlock()
				if cur == 1 {
					select {
					case <-waitStarted:
					default:
						close(waitStarted)
					}
					return pluginapi.SchedulerContinuityResponse{
						Decision:       pluginapi.SchedulerContinuityWait,
						PollIntervalMs: 50,
						WaitBudgetMs:   2000,
					}, true, nil
				}
				<-releaseReady
				// Clear cooldown so admission succeeds.
				_, _ = manager.Update(context.Background(), &coreauth.Auth{
					ID:       auth.ID,
					Provider: "codex",
					Status:   coreauth.StatusActive,
				})
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})

		cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 0, SendKeepAliveBeforeFT: false}}
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))

		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))

		done := make(chan struct{})
		go func() {
			defer close(done)
			h.Responses(c)
		}()

		<-waitStarted
		tickCh <- time.Now()
		close(releaseReady)

		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("Responses handler did not finish")
		}

		body := recorder.Body.String()
		if !strings.Contains(body, ": keep-alive\n\n") {
			t.Fatalf("expected wait-only SSE keep-alive comment, got %q", body)
		}
		if !strings.Contains(body, "response.completed") {
			t.Fatalf("expected response.completed after ready, got %q", body)
		}
	})

	t.Run("disabled continuity emits zero SSE keep-alive comments and returns 429 JSON", func(t *testing.T) {
		manager := coreauth.NewManager(nil, nil, nil)
		manager.RegisterExecutor(&sseContinuityCodexExecutor{})
		recoverAt := time.Now().Add(time.Minute)
		auth := &coreauth.Auth{
			ID:             "codex-sse-disabled",
			Provider:       "codex",
			Status:         coreauth.StatusActive,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

		manager.SetPluginScheduler(&sseContinuityScheduler{
			continuityFn: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
			},
		})

		cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 0, SendKeepAliveBeforeFT: false}}
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))

		h.Responses(c)
		if recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429; body=%q", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), ": keep-alive") {
			t.Fatalf("unexpected SSE keep-alive comment when continuity disabled: %q", recorder.Body.String())
		}
	})
}

func TestResponsesWebsocketWaitOnlyPingDuringContinuityWait(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreInterval := handlers.SetContinuityKeepAliveIntervalForTest(20 * time.Millisecond)
	defer restoreInterval()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&sseContinuityCodexExecutor{})
	recoverAt := time.Now().Add(time.Minute)
	auth := &coreauth.Auth{
		ID:             "codex-ws-1",
		Provider:       "codex",
		Status:         coreauth.StatusActive,
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
		NextRetryAfter: recoverAt,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	var polls atomic.Int32
	pingReceived := make(chan struct{})
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			if polls.Add(1) == 1 {
				return pluginapi.SchedulerContinuityResponse{
					Decision:       pluginapi.SchedulerContinuityWait,
					PollIntervalMs: 30,
					WaitBudgetMs:   2000,
				}, true, nil
			}
			select {
			case <-pingReceived:
			case <-time.After(time.Second):
			}
			_, _ = manager.Update(context.Background(), &coreauth.Auth{
				ID:       auth.ID,
				Provider: "codex",
				Status:   coreauth.StatusActive,
			})
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 0}}
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil && !strings.Contains(errClose.Error(), "use of closed network connection") {
			t.Logf("websocket close: %v", errClose)
		}
	}()

	pong := conn.PingHandler()
	conn.SetPingHandler(func(payload string) error {
		select {
		case <-pingReceived:
		default:
			close(pingReceived)
		}
		return pong(payload)
	})

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[]}`)); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	_, payload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read: %v", errRead)
	}
	if !strings.Contains(string(payload), "response.completed") {
		t.Fatalf("payload = %s, want response.completed", payload)
	}
	select {
	case <-pingReceived:
	default:
		t.Fatal("expected WebSocket Ping while continuity wait was in progress")
	}
}

type continuityLifecycleHost struct {
	mu        sync.Mutex
	completed pluginapi.RequestCompletion
}

func (h *continuityLifecycleHost) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}
func (h *continuityLifecycleHost) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}
func (h *continuityLifecycleHost) InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return pluginapi.ResponseInterceptResponse{}
}
func (h *continuityLifecycleHost) InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return pluginapi.StreamChunkInterceptResponse{}
}
func (h *continuityLifecycleHost) CompleteRequest(_ context.Context, completion pluginapi.RequestCompletion) {
	h.mu.Lock()
	h.completed = completion
	h.mu.Unlock()
}

func (h *continuityLifecycleHost) lastCompletion() pluginapi.RequestCompletion {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.completed
}

func TestResponsesContinuityRequestAndTraceIDCorrelation(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&sseContinuityCodexExecutor{})
	recoverAt := time.Now().Add(time.Minute)
	auth := &coreauth.Auth{
		ID:             "codex-trace-1",
		Provider:       "codex",
		Status:         coreauth.StatusActive,
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
		NextRetryAfter: recoverAt,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	var capturedReq pluginapi.SchedulerContinuityRequest
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			capturedReq = req
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
		},
	})

	host := &continuityLifecycleHost{}
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	base.SetPluginHost(host)

	ctx := logging.WithRequestID(context.Background(), "trace-corr-7589")
	_, _, errMsg := base.ExecuteWithAuthManager(ctx, "openai-response", "gpt-5-codex", []byte(`{"model":"gpt-5-codex","input":"hi"}`), "")
	if errMsg == nil {
		t.Fatal("expected cooldown error when continuity disabled")
	}
	completed := host.lastCompletion()
	if capturedReq.RequestID == "" || capturedReq.RequestID != completed.RequestID {
		t.Fatalf("RequestID mismatch: continuity=%q complete=%q", capturedReq.RequestID, completed.RequestID)
	}
	if capturedReq.TraceID != "trace-corr-7589" || completed.TraceID != "trace-corr-7589" {
		t.Fatalf("TraceID mismatch: continuity=%q complete=%q", capturedReq.TraceID, completed.TraceID)
	}
}

type bootstrapChunkErrCodexExecutor struct {
	streamCalls atomic.Int32
}

func (e *bootstrapChunkErrCodexExecutor) Identifier() string { return "codex" }
func (e *bootstrapChunkErrCodexExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *bootstrapChunkErrCodexExecutor) ExecuteStream(ctx context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	e.streamCalls.Add(1)
	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{Err: bootstrapChunkQuotaErr{}}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}
func (e *bootstrapChunkErrCodexExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (e *bootstrapChunkErrCodexExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *bootstrapChunkErrCodexExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

type bootstrapChunkQuotaErr struct{}

func (bootstrapChunkQuotaErr) Error() string {
	return `{"error":{"code":"usage_limit_reached","message":"usage limit reached"}}`
}
func (bootstrapChunkQuotaErr) StatusCode() int          { return http.StatusTooManyRequests }
func (bootstrapChunkQuotaErr) IsCredentialScoped() bool { return true }

func TestResponsesSSEBootstrapRetriesCannotBypassContinuityTermination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		decision pluginapi.SchedulerContinuityDecision
	}{
		{name: "disabled", decision: pluginapi.SchedulerContinuityDisabled},
		{name: "budget_exhausted", decision: pluginapi.SchedulerContinuityWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(1, 0, 0)
			exec := &bootstrapChunkErrCodexExecutor{}
			manager.RegisterExecutor(exec)
			auth := &coreauth.Auth{ID: "codex-bootstrap-" + tc.name, Provider: "codex", Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register: %v", errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

			var continuityCalls atomic.Int32
			manager.SetPluginScheduler(&sseContinuityScheduler{
				continuityFn: func(_ context.Context, _ pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
					continuityCalls.Add(1)
					// Recover credential in the background; bootstrap-retries must still NOT bypass continuity termination.
					_, _ = manager.Update(context.Background(), &coreauth.Auth{
						ID:       auth.ID,
						Provider: "codex",
						Status:   coreauth.StatusActive,
					})
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

			cfg := &sdkconfig.SDKConfig{
				Streaming: sdkconfig.StreamingConfig{
					BootstrapRetries: 3,
				},
			}
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))

			h.Responses(c)
			if recorder.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429; body=%q", recorder.Code, recorder.Body.String())
			}
			if exec.streamCalls.Load() != 1 || continuityCalls.Load() != 1 {
				t.Fatalf("streamCalls=%d continuityCalls=%d, want 1 and 1 (no bootstrap-retries bypass)", exec.streamCalls.Load(), continuityCalls.Load())
			}
		})
	}
}

type wsPinnedFailoverContinuityExecutor struct {
	turn       atomic.Int32
	pinnedID   string
	altID      string
	altCalls   atomic.Int32
	totalCalls atomic.Int32
}

func (e *wsPinnedFailoverContinuityExecutor) Identifier() string { return "codex" }
func (e *wsPinnedFailoverContinuityExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *wsPinnedFailoverContinuityExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	coreexecutor.MarkUpstreamAttempt(ctx)
	e.totalCalls.Add(1)
	if auth != nil && auth.ID == e.altID {
		e.altCalls.Add(1)
		ch := make(chan coreexecutor.StreamChunk, 1)
		ch <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-alt","output":[]}}`)}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}
	if e.turn.Add(1) == 1 {
		ch := make(chan coreexecutor.StreamChunk, 1)
		ch <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-turn-1","output":[]}}`)}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}
	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{Err: bootstrapChunkQuotaErr{}}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}
func (e *wsPinnedFailoverContinuityExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (e *wsPinnedFailoverContinuityExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}
func (e *wsPinnedFailoverContinuityExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestResponsesWebsocketPinnedFailoverCannotBypassContinuityTermination(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name     string
		decision pluginapi.SchedulerContinuityDecision
	}{
		{name: "disabled", decision: pluginapi.SchedulerContinuityDisabled},
		{name: "budget_exhausted", decision: pluginapi.SchedulerContinuityWait},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			manager.SetRetryConfig(1, 0, 0)
			pinnedID := "codex-ws-pinned-" + tc.name
			altID := "codex-ws-alt-" + tc.name
			exec := &wsPinnedFailoverContinuityExecutor{pinnedID: pinnedID, altID: altID}
			manager.RegisterExecutor(exec)

			pinnedAuth := &coreauth.Auth{
				ID:         pinnedID,
				Provider:   "codex",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"websockets": "true", "priority": "10"},
			}
			altAuth := &coreauth.Auth{
				ID:         altID,
				Provider:   "codex",
				Status:     coreauth.StatusActive,
				Attributes: map[string]string{"websockets": "true", "priority": "1"},
			}
			for _, a := range []*coreauth.Auth{pinnedAuth, altAuth} {
				if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
					t.Fatalf("register %s: %v", a.ID, errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
				id := a.ID
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}

			var continuityCalls atomic.Int32
			manager.SetPluginScheduler(&sseContinuityScheduler{
				continuityFn: func(_ context.Context, _ pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
					continuityCalls.Add(1)
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

			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.GET("/v1/responses", h.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
			conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
			if errDial != nil {
				t.Fatalf("dial: %v", errDial)
			}
			defer func() {
				if errClose := conn.Close(); errClose != nil && !strings.Contains(errClose.Error(), "use of closed network connection") {
					t.Logf("websocket close: %v", errClose)
				}
			}()

			// Turn 1 pins codex-ws-pinned on the connection.
			if errWrite1 := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[]}`)); errWrite1 != nil {
				t.Fatalf("write turn 1: %v", errWrite1)
			}
			_, payload1, errRead1 := conn.ReadMessage()
			if errRead1 != nil {
				t.Fatalf("read turn 1: %v", errRead1)
			}
			if !strings.Contains(string(payload1), "response.completed") {
				t.Fatalf("turn 1 payload = %s, want response.completed", payload1)
			}

			// Turn 2 is a self-contained response.create that hits 429 quota on the pinned credential.
			// When continuity terminates, in-place failover must NOT execute altAuth.
			if errWrite2 := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[]}`)); errWrite2 != nil {
				t.Fatalf("write turn 2: %v", errWrite2)
			}
			_, payload2, errRead2 := conn.ReadMessage()
			if errRead2 != nil {
				t.Fatalf("read turn 2: %v", errRead2)
			}
			if !strings.Contains(string(payload2), "usage_limit_reached") {
				t.Fatalf("turn 2 payload = %s, want usage_limit_reached error frame", payload2)
			}
			if exec.altCalls.Load() != 0 {
				t.Fatalf("altCalls = %d, want 0 (in-place failover must not bypass continuity termination)", exec.altCalls.Load())
			}
			if continuityCalls.Load() != 1 {
				t.Fatalf("continuityCalls = %d, want 1", continuityCalls.Load())
			}
		})
	}
}

func TestResponsesSSEBootstrapRetriesCannotBypassContinuityCandidateExhaustion(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(1, 0, 0)
	exec := &bootstrapChunkErrCodexExecutor{}
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{ID: "codex-bootstrap-exhausted", Provider: "codex", Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	var polls atomic.Int32
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(_ context.Context, _ pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			if polls.Add(1) == 1 {
				// Disable the only credential during wait so poll 2 finds zero candidates.
				_, _ = manager.Update(context.Background(), &coreauth.Auth{
					ID:       auth.ID,
					Provider: "codex",
					Status:   coreauth.StatusDisabled,
					Disabled: true,
				})
				return pluginapi.SchedulerContinuityResponse{
					Decision:       pluginapi.SchedulerContinuityWait,
					PollIntervalMs: 10,
					WaitBudgetMs:   1000,
				}, true, nil
			}
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 3,
		},
	}
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))

	h.Responses(c)
	// Even if credential is re-enabled later, candidate exhaustion terminated continuity and must not trigger bootstrap retries.
	_, _ = manager.Update(context.Background(), &coreauth.Auth{
		ID:       auth.ID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Disabled: false,
	})
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%q", recorder.Code, recorder.Body.String())
	}
	if exec.streamCalls.Load() != 1 || polls.Load() != 1 {
		t.Fatalf("streamCalls=%d polls=%d, want 1 and 1", exec.streamCalls.Load(), polls.Load())
	}
}

func TestResponsesSSEBootstrapRetriesCannotBypassWhenRetryBudgetExhaustedWithDisableCooling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	exec := &bootstrapChunkErrCodexExecutor{}
	manager.RegisterExecutor(exec)
	auth := &coreauth.Auth{
		ID:       "codex-bootstrap-disable-cooling",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"disable_cooling": true},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	var continuityCalls atomic.Int32
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			continuityCalls.Add(1)
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 3,
		},
	}
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))

	h.Responses(c)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%q", recorder.Code, recorder.Body.String())
	}
	if exec.streamCalls.Load() != 1 || continuityCalls.Load() != 0 {
		t.Fatalf("streamCalls=%d continuityCalls=%d, want 1 and 0 (bootstrap-retries must not bypass exhausted retry budget)", exec.streamCalls.Load(), continuityCalls.Load())
	}
}

type interleavedBeforeAuthHost struct {
	continuityLifecycleHost
	onBeforeAuth func(context.Context)
}

func (h *interleavedBeforeAuthHost) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	if h.onBeforeAuth != nil {
		h.onBeforeAuth(ctx)
	}
	return pluginapi.RequestInterceptResponse{}
}

func TestResponsesWebsocketInterleavedContinuityWaitsPreservePing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreInterval := handlers.SetContinuityKeepAliveIntervalForTest(15 * time.Millisecond)
	defer restoreInterval()

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&sseContinuityCodexExecutor{})
	recoverAt := time.Now().Add(time.Minute)
	auth := &coreauth.Auth{
		ID:             "codex-ws-interleaved",
		Provider:       "codex",
		Status:         coreauth.StatusActive,
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
		NextRetryAfter: recoverAt,
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	waitAStarted := make(chan struct{})
	waitACallEnded := make(chan struct{})
	releaseWaitA := make(chan struct{})
	releaseWaitB := make(chan struct{})
	pingDuringWaitB := make(chan struct{})
	var pingCount atomic.Int32
	var mu sync.Mutex
	pollsByReq := make(map[string]int)
	var waitAReqID, waitBReqID string

	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			mu.Lock()
			pollsByReq[req.RequestID]++
			poll := pollsByReq[req.RequestID]
			if poll == 1 {
				if waitAReqID == "" {
					waitAReqID = req.RequestID
					close(waitAStarted)
				} else if waitBReqID == "" && req.RequestID != waitAReqID {
					waitBReqID = req.RequestID
				}
			}
			isA := req.RequestID == waitAReqID
			isB := req.RequestID == waitBReqID
			mu.Unlock()

			if poll == 1 {
				return pluginapi.SchedulerContinuityResponse{
					Decision:       pluginapi.SchedulerContinuityWait,
					PollIntervalMs: 10,
					WaitBudgetMs:   2000,
				}, true, nil
			}
			if isA {
				// Wait A finishes while Wait B is already waiting (continuityWaitCount == 2 -> 1).
				<-releaseWaitA
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
			}
			if isB {
				close(releaseWaitA)
				<-waitACallEnded
				<-pingDuringWaitB
				close(releaseWaitB)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
			}
			// Main WS request after nested waits complete: clear cooldown and succeed.
			_, _ = manager.Update(context.Background(), &coreauth.Auth{
				ID:       auth.ID,
				Provider: "codex",
				Status:   coreauth.StatusActive,
			})
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 0}}
	base := handlers.NewBaseAPIHandlers(cfg, manager)
	var beforeCalls atomic.Int32
	host := &interleavedBeforeAuthHost{
		onBeforeAuth: func(ctx context.Context) {
			if beforeCalls.Add(1) != 1 {
				return
			}
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, _ = base.ExecuteModel(ctx, handlers.ModelExecutionRequest{
					EntryProtocol:           "openai-response",
					ExitProtocol:            "openai-response",
					Model:                   "gpt-5-codex",
					Body:                    []byte(`{"model":"gpt-5-codex","input":"a"}`),
					SkipInterceptorPluginID: "self",
				})
				close(waitACallEnded)
			}()
			<-waitAStarted
			go func() {
				defer wg.Done()
				_, _ = base.ExecuteModel(ctx, handlers.ModelExecutionRequest{
					EntryProtocol:           "openai-response",
					ExitProtocol:            "openai-response",
					Model:                   "gpt-5-codex",
					Body:                    []byte(`{"model":"gpt-5-codex","input":"b"}`),
					SkipInterceptorPluginID: "self",
				})
			}()
			wg.Wait()
		},
	}
	base.SetPluginHost(host)

	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil && !strings.Contains(errClose.Error(), "use of closed network connection") {
			t.Logf("websocket close: %v", errClose)
		}
	}()

	pong := conn.PingHandler()
	conn.SetPingHandler(func(payload string) error {
		pingCount.Add(1)
		select {
		case <-waitACallEnded:
			select {
			case <-pingDuringWaitB:
			default:
				close(pingDuringWaitB)
			}
		default:
		}
		return pong(payload)
	})

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[]}`)); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	_, payload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read: %v", errRead)
	}
	if !strings.Contains(string(payload), "response.completed") {
		t.Fatalf("payload = %s, want response.completed", payload)
	}
	if pingCount.Load() == 0 {
		t.Fatal("expected WebSocket Ping during interleaved continuity waits")
	}
	select {
	case <-releaseWaitB:
	default:
		t.Fatal("Wait B did not observe Ping after Wait A ended")
	}
}

type gatedCodexExecutor struct {
	sseContinuityCodexExecutor
	pingGate <-chan struct{}
}

func (e *gatedCodexExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk, 2)
	ch <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.created","response":{"id":"resp_outer"}}`)}
	go func() {
		defer close(ch)
		<-e.pingGate
		ch <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp_outer"}}`)}
	}()
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func TestResponsesWebsocketOuterReturnDoesNotCancelConcurrentNestedWaitPing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreInterval := handlers.SetContinuityKeepAliveIntervalForTest(15 * time.Millisecond)
	defer restoreInterval()

	nestedWaitEntered := make(chan struct{})
	releaseNestedWait := make(chan struct{})
	pingAfterOuterReturn := make(chan struct{})
	var outerReturned atomic.Bool
	var pingObserved atomic.Bool

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&gatedCodexExecutor{
		pingGate: pingAfterOuterReturn,
	})

	recoverAt := time.Now().Add(time.Minute)
	for _, a := range []*coreauth.Auth{
		{ID: "codex-outer-active", Provider: "codex", Status: coreauth.StatusActive},
		{
			ID:             "codex-nested-cooling",
			Provider:       "codex",
			Status:         coreauth.StatusActive,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		},
	} {
		if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register %s: %v", a.ID, errRegister)
		}
	}
	registry.GetGlobalRegistry().RegisterClient("codex-outer-active", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	registry.GetGlobalRegistry().RegisterClient("codex-nested-cooling", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex-nested"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("codex-outer-active")
		registry.GetGlobalRegistry().UnregisterClient("codex-nested-cooling")
	})

	var nestedPolls atomic.Int32
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			if nestedPolls.Add(1) == 1 {
				return pluginapi.SchedulerContinuityResponse{
					Decision:       pluginapi.SchedulerContinuityWait,
					PollIntervalMs: 10,
					WaitBudgetMs:   2000,
				}, true, nil
			}
			// Poll 2 guarantees NotifyContinuityWaitStart has already incremented continuityWaitCount.
			select {
			case <-nestedWaitEntered:
			default:
				close(nestedWaitEntered)
			}
			<-releaseNestedWait
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 0}}
	base := handlers.NewBaseAPIHandlers(cfg, manager)
	var beforeCalls atomic.Int32
	var nestedDone sync.WaitGroup
	defer nestedDone.Wait()
	host := &interleavedBeforeAuthHost{
		onBeforeAuth: func(ctx context.Context) {
			if beforeCalls.Add(1) != 1 {
				return
			}
			nestedDone.Add(1)
			go func() {
				defer nestedDone.Done()
				_, _ = base.ExecuteModel(ctx, handlers.ModelExecutionRequest{
					EntryProtocol:           "openai-response",
					ExitProtocol:            "openai-response",
					Model:                   "gpt-5-codex-nested",
					Body:                    []byte(`{"model":"gpt-5-codex-nested","input":"nested"}`),
					SkipInterceptorPluginID: "self",
				})
			}()
			<-nestedWaitEntered
		},
	}
	base.SetPluginHost(host)

	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
	if errDial != nil {
		t.Fatalf("dial: %v", errDial)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil && !strings.Contains(errClose.Error(), "use of closed network connection") {
			t.Logf("websocket close: %v", errClose)
		}
	}()

	pong := conn.PingHandler()
	conn.SetPingHandler(func(payload string) error {
		if outerReturned.Load() && pingObserved.CompareAndSwap(false, true) {
			close(pingAfterOuterReturn)
			close(releaseNestedWait)
		}
		return pong(payload)
	})

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[]}`)); errWrite != nil {
		t.Fatalf("write: %v", errWrite)
	}
	// Reading response.created guarantees ExecuteStreamWithAuthManager has already returned.
	_, firstPayload, errFirstRead := conn.ReadMessage()
	if errFirstRead != nil {
		t.Fatalf("read first: %v", errFirstRead)
	}
	if !strings.Contains(string(firstPayload), "response.created") {
		t.Fatalf("firstPayload = %s, want response.created", firstPayload)
	}
	outerReturned.Store(true)

	_, payload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read completed: %v", errRead)
	}
	if !strings.Contains(string(payload), "response.completed") {
		t.Fatalf("payload = %s, want response.completed", payload)
	}
	if !pingObserved.Load() {
		t.Fatal("expected WebSocket Ping while nested continuity wait remained active after outer ExecuteStream returned")
	}
}

type gatedSSEResponsesExecutor struct {
	sseContinuityCodexExecutor
	keepAliveGate <-chan struct{}
}

func (e *gatedSSEResponsesExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk, 2)
	ch <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created","response":{"id":"resp_sse_outer"}}

`)}
	go func() {
		defer close(ch)
		<-e.keepAliveGate
		ch <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"id":"resp_sse_outer","output":[]}}

`)}
	}()
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func TestResponsesSSEOuterReturnPreservesKeepAliveWhileNestedWaitActive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreInterval := handlers.SetContinuityKeepAliveIntervalForTest(15 * time.Millisecond)
	defer restoreInterval()

	nestedWaitEntered := make(chan struct{})
	releaseNestedWait := make(chan struct{})
	keepAliveAfterOuterCreated := make(chan struct{})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&gatedSSEResponsesExecutor{
		keepAliveGate: keepAliveAfterOuterCreated,
	})

	recoverAt := time.Now().Add(time.Minute)
	for _, a := range []*coreauth.Auth{
		{ID: "codex-sse-outer-active", Provider: "codex", Status: coreauth.StatusActive},
		{
			ID:             "codex-sse-nested-cooling",
			Provider:       "codex",
			Status:         coreauth.StatusActive,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		},
	} {
		if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register %s: %v", a.ID, errRegister)
		}
	}
	registry.GetGlobalRegistry().RegisterClient("codex-sse-outer-active", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	registry.GetGlobalRegistry().RegisterClient("codex-sse-nested-cooling", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex-nested"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("codex-sse-outer-active")
		registry.GetGlobalRegistry().UnregisterClient("codex-sse-nested-cooling")
	})

	var nestedPolls atomic.Int32
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(_ context.Context, _ pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			if nestedPolls.Add(1) == 1 {
				return pluginapi.SchedulerContinuityResponse{
					Decision:       pluginapi.SchedulerContinuityWait,
					PollIntervalMs: 10,
					WaitBudgetMs:   2000,
				}, true, nil
			}
			select {
			case <-nestedWaitEntered:
			default:
				close(nestedWaitEntered)
			}
			<-releaseNestedWait
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 0}}
	base := handlers.NewBaseAPIHandlers(cfg, manager)
	var beforeCalls atomic.Int32
	var nestedDone sync.WaitGroup
	defer nestedDone.Wait()
	host := &interleavedBeforeAuthHost{
		onBeforeAuth: func(ctx context.Context) {
			if beforeCalls.Add(1) != 1 {
				return
			}
			nestedDone.Add(1)
			go func() {
				defer nestedDone.Done()
				_, _ = base.ExecuteModel(ctx, handlers.ModelExecutionRequest{
					EntryProtocol:           "openai-response",
					ExitProtocol:            "openai-response",
					Model:                   "gpt-5-codex-nested",
					Body:                    []byte(`{"model":"gpt-5-codex-nested","input":"nested"}`),
					SkipInterceptorPluginID: "self",
				})
			}()
			<-nestedWaitEntered
		},
	}
	base.SetPluginHost(host)

	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, errPost := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))
	if errPost != nil {
		t.Fatalf("POST /v1/responses: %v", errPost)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Logf("response body close: %v", errClose)
		}
	}()

	scanner := bufio.NewScanner(resp.Body)
	var sawCreated, sawPostCreatedKeepAlive, sawCompleted bool
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "response.created") {
			sawCreated = true
		}
		if sawCreated && strings.HasPrefix(line, ": keep-alive") && !sawPostCreatedKeepAlive {
			sawPostCreatedKeepAlive = true
			close(keepAliveAfterOuterCreated)
			close(releaseNestedWait)
		}
		if strings.Contains(line, "response.completed") {
			sawCompleted = true
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		t.Fatalf("scan SSE body: %v", errScan)
	}
	if !sawCreated || !sawPostCreatedKeepAlive || !sawCompleted {
		t.Fatalf("flags created=%v postCreatedKeepAlive=%v completed=%v, want all true", sawCreated, sawPostCreatedKeepAlive, sawCompleted)
	}
}

type deferredFirstFrameSSEExecutor struct {
	sseContinuityCodexExecutor
	firstFrameGate <-chan struct{}
}

func (e *deferredFirstFrameSSEExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	ch := make(chan coreexecutor.StreamChunk, 2)
	go func() {
		defer close(ch)
		<-e.firstFrameGate
		ch <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created","response":{"id":"resp_sse_deferred"}}

`)}
		ch <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.completed","response":{"id":"resp_sse_deferred","output":[]}}

`)}
	}()
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func TestResponsesSSEOuterReturnPreservesKeepAliveBeforeFirstDataFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)
	restoreInterval := handlers.SetContinuityKeepAliveIntervalForTest(15 * time.Millisecond)
	defer restoreInterval()

	nestedWaitEntered := make(chan struct{})
	releaseNestedWait := make(chan struct{})
	releaseFirstFrame := make(chan struct{})

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&deferredFirstFrameSSEExecutor{
		firstFrameGate: releaseFirstFrame,
	})

	recoverAt := time.Now().Add(time.Minute)
	for _, a := range []*coreauth.Auth{
		{ID: "codex-sse-peek-active", Provider: "codex", Status: coreauth.StatusActive},
		{
			ID:             "codex-sse-peek-nested-cooling",
			Provider:       "codex",
			Status:         coreauth.StatusActive,
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		},
	} {
		if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
			t.Fatalf("register %s: %v", a.ID, errRegister)
		}
	}
	registry.GetGlobalRegistry().RegisterClient("codex-sse-peek-active", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	registry.GetGlobalRegistry().RegisterClient("codex-sse-peek-nested-cooling", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex-nested"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("codex-sse-peek-active")
		registry.GetGlobalRegistry().UnregisterClient("codex-sse-peek-nested-cooling")
	})

	var nestedPolls atomic.Int32
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(_ context.Context, _ pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			if nestedPolls.Add(1) == 1 {
				return pluginapi.SchedulerContinuityResponse{
					Decision:       pluginapi.SchedulerContinuityWait,
					PollIntervalMs: 10,
					WaitBudgetMs:   2000,
				}, true, nil
			}
			select {
			case <-nestedWaitEntered:
			default:
				close(nestedWaitEntered)
			}
			<-releaseNestedWait
			return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{KeepAliveSeconds: 0}}
	base := handlers.NewBaseAPIHandlers(cfg, manager)
	var beforeCalls atomic.Int32
	var nestedDone sync.WaitGroup
	defer nestedDone.Wait()
	host := &interleavedBeforeAuthHost{
		onBeforeAuth: func(ctx context.Context) {
			if beforeCalls.Add(1) != 1 {
				return
			}
			nestedDone.Add(1)
			go func() {
				defer nestedDone.Done()
				_, _ = base.ExecuteModel(ctx, handlers.ModelExecutionRequest{
					EntryProtocol:           "openai-response",
					ExitProtocol:            "openai-response",
					Model:                   "gpt-5-codex-nested",
					Body:                    []byte(`{"model":"gpt-5-codex-nested","input":"nested"}`),
					SkipInterceptorPluginID: "self",
				})
			}()
			<-nestedWaitEntered
		},
	}
	base.SetPluginHost(host)

	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	server := httptest.NewServer(router)
	defer server.Close()

	resp, errPost := http.Post(server.URL+"/v1/responses", "application/json", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))
	if errPost != nil {
		t.Fatalf("POST /v1/responses: %v", errPost)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			t.Logf("response body close: %v", errClose)
		}
	}()

	scanner := bufio.NewScanner(resp.Body)
	var sawKeepAliveBeforeCreated, sawCreated, sawCompleted bool
	for scanner.Scan() {
		line := scanner.Text()
		if !sawCreated && strings.HasPrefix(line, ": keep-alive") && !sawKeepAliveBeforeCreated {
			sawKeepAliveBeforeCreated = true
			close(releaseFirstFrame)
			close(releaseNestedWait)
		}
		if strings.Contains(line, "response.created") {
			sawCreated = true
		}
		if strings.Contains(line, "response.completed") {
			sawCompleted = true
		}
	}
	if errScan := scanner.Err(); errScan != nil {
		t.Fatalf("scan SSE body: %v", errScan)
	}
	if !sawKeepAliveBeforeCreated || !sawCreated || !sawCompleted {
		t.Fatalf("flags preFirstFrameKeepAlive=%v created=%v completed=%v, want all true", sawKeepAliveBeforeCreated, sawCreated, sawCompleted)
	}
}

type ordinary429CodexExecutor struct {
	sseContinuityCodexExecutor
	calls atomic.Int32
}

func (e *ordinary429CodexExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	ch := make(chan coreexecutor.StreamChunk, 1)
	ch <- coreexecutor.StreamChunk{Err: &coreauth.Error{
		Code:       "rate_limit_exceeded",
		HTTPStatus: http.StatusTooManyRequests,
		Message:    `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`,
	}}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func TestResponsesSSEBootstrapRetriesDoNotReenterPreAdmissionAfterOrdinary429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	exec := &ordinary429CodexExecutor{}
	manager.RegisterExecutor(exec)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-bootstrap-ordinary-429",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient("codex-bootstrap-ordinary-429", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-bootstrap-ordinary-429") })

	var continuityCalls atomic.Int32
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			continuityCalls.Add(1)
			return pluginapi.SchedulerContinuityResponse{
				Decision:       pluginapi.SchedulerContinuityWait,
				PollIntervalMs: 10,
				WaitBudgetMs:   200,
			}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 2,
		},
	}
	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIResponsesAPIHandler(base)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.Responses(c)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if continuityCalls.Load() != 0 {
		t.Fatalf("continuityCalls = %d, want 0 (bootstrap retry must not re-enter pre_admission after ordinary 429)", continuityCalls.Load())
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("streamCalls = %d, want 1", exec.calls.Load())
	}
}

type partialFrameThenOrdinary429CodexExecutor struct {
	sseContinuityCodexExecutor
	calls atomic.Int32
}

func (e *partialFrameThenOrdinary429CodexExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	ch := make(chan coreexecutor.StreamChunk, 2)
	// First chunk is an incomplete SSE frame (does not trigger first-frame commit),
	// followed by an ordinary non-quota 429 stream error.
	ch <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.`)}
	ch <- coreexecutor.StreamChunk{Err: &coreauth.Error{
		Code:       "rate_limit_exceeded",
		HTTPStatus: http.StatusTooManyRequests,
		Message:    `{"error":{"code":"rate_limit_exceeded","message":"slow down"}}`,
	}}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func TestResponsesSSEBootstrapRetriesDoNotReenterPreAdmissionAfterPartialFrameAndOrdinary429(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	exec := &partialFrameThenOrdinary429CodexExecutor{}
	manager.RegisterExecutor(exec)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex-bootstrap-partial-429",
		Provider: "codex",
		Status:   coreauth.StatusActive,
	}); errRegister != nil {
		t.Fatalf("register: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient("codex-bootstrap-partial-429", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-bootstrap-partial-429") })

	var continuityCalls atomic.Int32
	manager.SetPluginScheduler(&sseContinuityScheduler{
		continuityFn: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
			continuityCalls.Add(1)
			return pluginapi.SchedulerContinuityResponse{
				Decision:       pluginapi.SchedulerContinuityWait,
				PollIntervalMs: 10,
				WaitBudgetMs:   200,
			}, true, nil
		},
	})

	cfg := &sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			BootstrapRetries: 2,
		},
	}
	base := handlers.NewBaseAPIHandlers(cfg, manager)
	h := NewOpenAIResponsesAPIHandler(base)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5-codex","stream":true,"input":"hi"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.Responses(c)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
	}
	if continuityCalls.Load() != 0 {
		t.Fatalf("continuityCalls = %d, want 0 (bootstrap retry must not re-enter pre_admission after partial frame + ordinary 429)", continuityCalls.Load())
	}
	if exec.calls.Load() != 1 {
		t.Fatalf("streamCalls = %d, want 1", exec.calls.Load())
	}
}

type partialFrameThenQuota429CodexExecutor struct {
	sseContinuityCodexExecutor
	calls atomic.Int32
}

func (e *partialFrameThenQuota429CodexExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	ch := make(chan coreexecutor.StreamChunk, 2)
	ch <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.")}
	ch <- coreexecutor.StreamChunk{Err: &coreauth.Error{
		Code:       "usage_limit_reached",
		HTTPStatus: http.StatusTooManyRequests,
		Message:    "{\"error\":{\"type\":\"usage_limit_reached\",\"code\":\"usage_limit_reached\",\"message\":\"quota exhausted\"}}",
	}}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func TestResponsesSSEBootstrapRetriesRespectPreOutput429GateAndDisableCoolingBudgetAfterPartialFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("disable_cooling zero retry budget blocks replay without ready", func(t *testing.T) {
		manager := coreauth.NewManager(nil, nil, nil)
		manager.SetRetryConfig(0, 0, 0)
		exec := &partialFrameThenQuota429CodexExecutor{}
		manager.RegisterExecutor(exec)
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID:       "codex-bootstrap-partial-quota-dc",
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Metadata: map[string]any{"disable_cooling": true},
		}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-bootstrap-partial-quota-dc", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-bootstrap-partial-quota-dc") })

		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&sseContinuityScheduler{
			continuityFn: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})

		cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 2}}
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{\"model\":\"gpt-5-codex\",\"stream\":true,\"input\":\"hi\"}"))
		c.Request.Header.Set("Content-Type", "application/json")
		h.Responses(c)

		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
		}
		if continuityCalls.Load() != 0 || exec.calls.Load() != 1 {
			t.Fatalf("continuityCalls=%d streamCalls=%d, want 0 and 1", continuityCalls.Load(), exec.calls.Load())
		}
	})

	t.Run("non-ready pre_output_429 gate blocks bootstrap replay", func(t *testing.T) {
		manager := coreauth.NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 0, 0)
		exec := &partialFrameThenQuota429CodexExecutor{}
		manager.RegisterExecutor(exec)
		if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
			ID:       "codex-bootstrap-partial-quota-gate",
			Provider: "codex",
			Status:   coreauth.StatusActive,
		}); errRegister != nil {
			t.Fatalf("register: %v", errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient("codex-bootstrap-partial-quota-gate", "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("codex-bootstrap-partial-quota-gate") })

		var continuityCalls atomic.Int32
		var observedPhase string
		manager.SetPluginScheduler(&sseContinuityScheduler{
			continuityFn: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				observedPhase = req.Phase
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
			},
		})

		cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 2}}
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("{\"model\":\"gpt-5-codex\",\"stream\":true,\"input\":\"hi\"}"))
		c.Request.Header.Set("Content-Type", "application/json")
		h.Responses(c)

		if rec.Code != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want 429; body=%s", rec.Code, rec.Body.String())
		}
		if continuityCalls.Load() != 1 || observedPhase != "pre_output_429" {
			t.Fatalf("continuityCalls=%d phase=%q, want 1 and pre_output_429", continuityCalls.Load(), observedPhase)
		}
		if exec.calls.Load() != 1 {
			t.Fatalf("streamCalls = %d, want 1", exec.calls.Load())
		}
	})
}

func TestResponsesWebsocketKeepAliveSuppressesQueuedTickAfterWaitEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	intervals := make(chan time.Duration, 2)
	tickCh := make(chan time.Time, 1)
	tickerCreated := make(chan struct{})
	tickerStopped := make(chan struct{})
	var tickerOnce sync.Once
	var stopOnce sync.Once
	var pingCount atomic.Int32
	var active atomic.Bool
	active.Store(true)

	done := make(chan error, 1)
	go func() {
		done <- runResponsesWebsocketKeepAliveWithTicker(
			ctx,
			intervals,
			active.Load,
			func() error {
				pingCount.Add(1)
				return nil
			},
			func(time.Duration) (<-chan time.Time, func()) {
				tickerOnce.Do(func() { close(tickerCreated) })
				return tickCh, func() {
					stopOnce.Do(func() { close(tickerStopped) })
				}
			},
		)
	}()

	intervals <- 15 * time.Millisecond
	<-tickerCreated

	// Transition wait to inactive, then queue a tick and stop interval together.
	// Whichever case select chooses first (<-ticks or <-intervals), stopCurrent()
	// stops the ticker and shouldPing suppresses any queued tick.
	active.Store(false)
	tickCh <- time.Now()
	intervals <- 0
	<-tickerStopped
	cancel()
	if errRun := <-done; errRun != nil {
		t.Fatalf("runResponsesWebsocketKeepAliveWithTicker error = %v", errRun)
	}
	if pingCount.Load() != 0 {
		t.Fatalf("pingCount = %d, want 0 after wait ended", pingCount.Load())
	}
}

type wsPinnedFailoverQuotaExecutor struct {
	sseContinuityCodexExecutor
	calls   atomic.Int32
	authIDs []string
	mu      sync.Mutex
}

func (e *wsPinnedFailoverQuotaExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	callNum := e.calls.Add(1)
	e.mu.Lock()
	if auth != nil {
		e.authIDs = append(e.authIDs, auth.ID)
	}
	e.mu.Unlock()
	ch := make(chan coreexecutor.StreamChunk, 2)
	if callNum == 1 {
		// Turn 1 succeeds and establishes pinned auth on the websocket session.
		ch <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ws_turn1\"}}\n\n")}
		ch <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_turn1\",\"output\":[]}}\n\n")}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}
	// Turn 2 emits a partial SSE frame (not committed downstream) followed by quota 429.
	if callNum == 2 {
		ch <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.")}
		ch <- coreexecutor.StreamChunk{Err: &coreauth.Error{
			Code:       "usage_limit_reached",
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "{\"error\":{\"type\":\"usage_limit_reached\",\"code\":\"usage_limit_reached\",\"message\":\"quota exhausted\"}}",
		}}
		close(ch)
		return &coreexecutor.StreamResult{Chunks: ch}, nil
	}
	// Call 3 (failoverInPlace replay after ready) succeeds on the alternative auth.
	ch <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_ws_turn2_failover\"}}\n\n")}
	ch <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_turn2_failover\",\"output\":[]}}\n\n")}
	close(ch)
	return &coreexecutor.StreamResult{Chunks: ch}, nil
}

func TestResponsesWebsocketFailoverInPlaceRespectsPreOutput429GateWithZeroBootstrapRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)

	runCase := func(t *testing.T, disableCooling bool, retryLimit int, continuityDecision pluginapi.SchedulerContinuityDecision, wantContinuityCalls int32, wantExecCalls int32, wantTurn2Success bool) {
		t.Helper()
		manager := coreauth.NewManager(nil, nil, nil)
		manager.SetRetryConfig(retryLimit, 0, 0)
		exec := &wsPinnedFailoverQuotaExecutor{}
		manager.RegisterExecutor(exec)

		var authMeta map[string]any
		if disableCooling {
			authMeta = map[string]any{"disable_cooling": true}
		}
		for _, a := range []*coreauth.Auth{
			{ID: "codex-ws-failover-a", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}, Metadata: authMeta},
			{ID: "codex-ws-failover-b", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}, Metadata: authMeta},
		} {
			if _, errReg := manager.Register(context.Background(), a); errReg != nil {
				t.Fatalf("register %s: %v", a.ID, errReg)
			}
			registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
			authID := a.ID
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
		}

		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&sseContinuityScheduler{
			continuityFn: func(context.Context, pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				return pluginapi.SchedulerContinuityResponse{Decision: continuityDecision}, true, nil
			},
		})

		cfg := &sdkconfig.SDKConfig{
			Streaming: sdkconfig.StreamingConfig{
				BootstrapRetries: 0,
			},
		}
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
		router := gin.New()
		router.GET("/v1/responses", h.ResponsesWebsocket)
		server := httptest.NewServer(router)
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
		conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
		if errDial != nil {
			t.Fatalf("dial: %v", errDial)
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil && !strings.Contains(errClose.Error(), "use of closed network connection") {
				t.Logf("websocket close: %v", errClose)
			}
		}()

		// Turn 1 pins auth A.
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"turn1"}]}`)); errWrite != nil {
			t.Fatalf("write turn1: %v", errWrite)
		}
		for {
			_, msg, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read turn1: %v", errRead)
			}
			if strings.Contains(string(msg), "response.completed") {
				break
			}
		}

		// Turn 2 gets partial frame + quota 429 on pinned auth A while auth B is available.
		// With bootstrap-retries=0, failoverInPlace must NOT bypass pre_output_429 gate or zero retry budget.
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"turn2"}]}`)); errWrite != nil {
			t.Fatalf("write turn2: %v", errWrite)
		}
		for {
			_, turn2Msg, errRead2 := conn.ReadMessage()
			if errRead2 != nil {
				t.Fatalf("read turn2: %v", errRead2)
			}
			if wantTurn2Success {
				if strings.Contains(string(turn2Msg), "response.completed") {
					break
				}
				continue
			}
			if !strings.Contains(string(turn2Msg), "usage_limit_reached") && !strings.Contains(string(turn2Msg), "rate_limit") {
				t.Fatalf("turn2 msg = %s, want quota error", string(turn2Msg))
			}
			break
		}
		if exec.calls.Load() != wantExecCalls {
			t.Fatalf("exec calls = %d, want %d", exec.calls.Load(), wantExecCalls)
		}
		if continuityCalls.Load() != wantContinuityCalls {
			t.Fatalf("continuityCalls = %d, want %d", continuityCalls.Load(), wantContinuityCalls)
		}
	}

	t.Run("disable_cooling zero retry budget blocks failoverInPlace", func(t *testing.T) {
		runCase(t, true, 0, pluginapi.SchedulerContinuityReady, 0, 2, false)
	})

	t.Run("non-ready pre_output_429 gate blocks failoverInPlace", func(t *testing.T) {
		runCase(t, false, 1, pluginapi.SchedulerContinuityDisabled, 1, 2, false)
	})

	t.Run("ready pre_output_429 gate allows failoverInPlace", func(t *testing.T) {
		runCase(t, false, 1, pluginapi.SchedulerContinuityReady, 1, 3, true)
	})

	t.Run("failoverInPlace gate unpins and excludes failed auth from candidate set", func(t *testing.T) {
		manager := coreauth.NewManager(nil, nil, nil)
		manager.SetRetryConfig(1, 0, 0)
		exec := &wsPinnedFailoverQuotaExecutor{}
		manager.RegisterExecutor(exec)

		// Register only Auth A initially so Turn 1 deterministically pins Auth A,
		// then register active Auth B before Turn 2 so failoverInPlace has an alternative target.
		authA := &coreauth.Auth{ID: "codex-ws-failover-a", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}}
		if _, errReg := manager.Register(context.Background(), authA); errReg != nil {
			t.Fatalf("register %s: %v", authA.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(authA.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authA.ID) })

		var sawFailedAuthInCandidates atomic.Bool
		var sawAlternativeCandidate atomic.Bool
		var sawPinnedPolicy atomic.Bool
		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&sseContinuityScheduler{
			continuityFn: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				continuityCalls.Add(1)
				if len(req.Options.Metadata) > 0 {
					if pinned, ok := req.Options.Metadata[coreexecutor.PinnedAuthMetadataKey].(string); ok && pinned != "" {
						sawPinnedPolicy.Store(true)
					}
				}
				for _, cand := range req.Candidates {
					if cand.ID == "codex-ws-failover-a" {
						sawFailedAuthInCandidates.Store(true)
					}
					if cand.ID == "codex-ws-failover-b" && !cand.InCooldown {
						sawAlternativeCandidate.Store(true)
					}
				}
				if sawFailedAuthInCandidates.Load() || !sawAlternativeCandidate.Load() || sawPinnedPolicy.Load() {
					return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityDisabled}, true, nil
				}
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})

		cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 0}}
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
		router := gin.New()
		router.GET("/v1/responses", h.ResponsesWebsocket)
		server := httptest.NewServer(router)
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
		conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
		if errDial != nil {
			t.Fatalf("dial: %v", errDial)
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil && !strings.Contains(errClose.Error(), "use of closed network connection") {
				t.Logf("websocket close: %v", errClose)
			}
		}()

		// Turn 1 pins auth A.
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"turn1"}]}`)); errWrite != nil {
			t.Fatalf("write turn1: %v", errWrite)
		}
		for {
			_, msg, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read turn1: %v", errRead)
			}
			if strings.Contains(string(msg), "response.completed") {
				break
			}
		}

		// Register active Auth B before Turn 2.
		authB := &coreauth.Auth{ID: "codex-ws-failover-b", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}}
		if _, errReg := manager.Register(context.Background(), authB); errReg != nil {
			t.Fatalf("register %s: %v", authB.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(authB.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authB.ID) })

		// Turn 2 fails on A -> gate must unpin and exclude A, seeing only active B.
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"turn2"}]}`)); errWrite != nil {
			t.Fatalf("write turn2: %v", errWrite)
		}
		for {
			_, msg, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read turn2: %v", errRead)
			}
			if strings.Contains(string(msg), "response.completed") {
				break
			}
		}
		if continuityCalls.Load() != 1 || sawFailedAuthInCandidates.Load() || !sawAlternativeCandidate.Load() || sawPinnedPolicy.Load() {
			t.Fatalf("continuityCalls=%d sawFailed=%v sawAlt=%v sawPinned=%v, want 1, false, true, false",
				continuityCalls.Load(), sawFailedAuthInCandidates.Load(), sawAlternativeCandidate.Load(), sawPinnedPolicy.Load())
		}
		if exec.calls.Load() != 3 {
			t.Fatalf("exec calls = %d, want 3 (turn1 A, turn2 A fail, turn2 B replay)", exec.calls.Load())
		}
	})

	t.Run("failoverInPlace_gate_waits_and_recovers_when_alternative_auth_starts_in_quota_cooldown", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		exec := &wsPinnedFailoverQuotaExecutor{}
		manager := coreauth.NewManager(nil, nil, nil)
		manager.RegisterExecutor(exec)
		manager.SetRetryConfig(1, 500*time.Millisecond, 0)

		authA := &coreauth.Auth{ID: "codex-ws-cooldown-a", Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}}
		if _, errReg := manager.Register(context.Background(), authA); errReg != nil {
			t.Fatalf("register %s: %v", authA.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(authA.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authA.ID) })

		var mu sync.Mutex
		var pickRequestIDs []string
		var continuityRequestIDs []string
		var continuityCalls atomic.Int32
		manager.SetPluginScheduler(&sseContinuityScheduler{
			pickFn: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
				reqID, _ := req.Options.Metadata[coreexecutor.RequestIDMetadataKey].(string)
				mu.Lock()
				pickRequestIDs = append(pickRequestIDs, reqID)
				mu.Unlock()
				return pluginapi.SchedulerPickResponse{}, false, nil
			},
			continuityFn: func(_ context.Context, req pluginapi.SchedulerContinuityRequest) (pluginapi.SchedulerContinuityResponse, bool, error) {
				mu.Lock()
				continuityRequestIDs = append(continuityRequestIDs, req.RequestID)
				mu.Unlock()
				n := continuityCalls.Add(1)
				if n == 1 {
					return pluginapi.SchedulerContinuityResponse{
						Decision:       pluginapi.SchedulerContinuityWait,
						PollIntervalMs: 10,
					}, true, nil
				}
				// Recover alternative auth B from quota cooldown during wait.
				_, _ = manager.Update(context.Background(), &coreauth.Auth{
					ID:         "codex-ws-cooldown-b",
					Provider:   "codex",
					Status:     coreauth.StatusActive,
					Attributes: map[string]string{"websockets": "true"},
				})
				return pluginapi.SchedulerContinuityResponse{Decision: pluginapi.SchedulerContinuityReady}, true, nil
			},
		})

		cfg := &sdkconfig.SDKConfig{Streaming: sdkconfig.StreamingConfig{BootstrapRetries: 0}}
		h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
		router := gin.New()
		router.GET("/v1/responses", h.ResponsesWebsocket)
		server := httptest.NewServer(router)
		defer server.Close()

		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
		conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
		if errDial != nil {
			t.Fatalf("dial: %v", errDial)
		}
		defer func() {
			if errClose := conn.Close(); errClose != nil && !strings.Contains(errClose.Error(), "use of closed network connection") {
				t.Logf("websocket close: %v", errClose)
			}
		}()

		// Turn 1 pins auth A.
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"turn1"}]}`)); errWrite != nil {
			t.Fatalf("write turn1: %v", errWrite)
		}
		for {
			_, msg, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read turn1: %v", errRead)
			}
			if strings.Contains(string(msg), "response.completed") {
				break
			}
		}

		// Register Auth B in quota cooldown before Turn 2.
		recoverAt := time.Now().Add(time.Minute)
		authB := &coreauth.Auth{
			ID:             "codex-ws-cooldown-b",
			Provider:       "codex",
			Status:         coreauth.StatusActive,
			Attributes:     map[string]string{"websockets": "true"},
			Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
			NextRetryAfter: recoverAt,
		}
		if _, errReg := manager.Register(context.Background(), authB); errReg != nil {
			t.Fatalf("register %s: %v", authB.ID, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(authB.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5-codex"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authB.ID) })

		// Turn 2 fails on A -> gate must run BEFORE alternative-target check, wait, recover B, and failover in place.
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"turn2"}]}`)); errWrite != nil {
			t.Fatalf("write turn2: %v", errWrite)
		}
		for {
			_, msg, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read turn2: %v", errRead)
			}
			if strings.Contains(string(msg), "response.completed") {
				break
			}
		}

		if continuityCalls.Load() != 2 {
			t.Fatalf("continuityCalls = %d, want 2 (wait + ready)", continuityCalls.Load())
		}
		if exec.calls.Load() != 3 {
			t.Fatalf("exec calls = %d, want 3 (turn1 A, turn2 A fail, turn2 B replay)", exec.calls.Load())
		}
		mu.Lock()
		picks := append([]string(nil), pickRequestIDs...)
		conts := append([]string(nil), continuityRequestIDs...)
		mu.Unlock()
		if len(picks) != 3 || len(conts) != 2 {
			t.Fatalf("pickRequestIDs=%v continuityRequestIDs=%v, want 3 picks and 2 continuity calls", picks, conts)
		}
		if picks[0] == "" || picks[1] == "" || picks[0] == picks[1] {
			t.Fatalf("turn1 vs turn2 pick RequestIDs = %v, want distinct non-empty IDs", picks)
		}
		if picks[1] != conts[0] || picks[1] != conts[1] || picks[1] != picks[2] {
			t.Fatalf("turn2 RequestIDs mismatched across initial pick, continuity gate, and failover replay: picks=%v conts=%v", picks, conts)
		}
	})
}
