package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestIssue7740_ExecuteStream_BootstrapStopPreservesUpstreamHeaders(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	t.Run("rule_matched_stop_preserves_headers_without_bootstrap_downgrade", func(t *testing.T) {
		model := "claude-sonnet-4-6-7740-a-" + uuid.NewString()
		m := NewManager(nil, &FillFirstSelector{}, nil)
		m.SetRetryConfig(2, 5*time.Second, 2)
		m.SetConfig(&internalconfig.Config{
			OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
				"antigravity": {
					{
						Status: http.StatusTooManyRequests,
						Match:  []string{"quota_stop_7740"},
						Action: "stop",
					},
				},
			},
		})

		auth1 := &Auth{
			ID:         "ag-7740-a1-" + uuid.NewString(),
			Provider:   "antigravity",
			Status:     StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		}
		auth2 := &Auth{
			ID:         "ag-7740-a2-" + uuid.NewString(),
			Provider:   "antigravity",
			Status:     StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth1.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		reg.RegisterClient(auth2.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth1.ID)
			reg.UnregisterClient(auth2.ID)
		})
		if _, errReg1 := m.Register(context.Background(), auth1); errReg1 != nil {
			t.Fatalf("Register(auth1): %v", errReg1)
		}
		if _, errReg2 := m.Register(context.Background(), auth2); errReg2 != nil {
			t.Fatalf("Register(auth2): %v", errReg2)
		}

		var calls atomic.Int32
		retryHint := 17 * time.Second
		m.RegisterExecutor(&customStreamMockExecutor{
			identifier: "antigravity",
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				calls.Add(1)
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{
					Err: customStatusError{
						code:       http.StatusTooManyRequests,
						msg:        "quota_stop_7740",
						retryAfter: &retryHint,
					},
				}
				close(ch)
				return &cliproxyexecutor.StreamResult{
					Headers: http.Header{
						"Retry-After": []string{"17"},
						"X-Trace-Id":  []string{"trace-stop-7740"},
					},
					Chunks: ch,
				}, nil
			},
		})

		res, errStream := m.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
		if res != nil {
			t.Fatalf("ExecuteStream() result = %+v, want nil direct error so outer bootstrap retries cannot replay", res)
		}
		if errStream == nil {
			t.Fatal("ExecuteStream() error = nil, want non-nil")
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("executor calls = %d, want 1 (must stop after first credential)", got)
		}
		if isRequestStopError(errStream) {
			t.Fatalf("ExecuteStream() leaked internal requestStopError marker: %T", errStream)
		}
		var marked *upstreamExecutionAttemptError
		if errors.As(errStream, &marked) && marked != nil {
			t.Fatalf("ExecuteStream() leaked internal upstreamExecutionAttemptError marker: %#v", errStream)
		}
		var bootstrap *streamBootstrapError
		if errors.As(errStream, &bootstrap) && bootstrap != nil {
			t.Fatalf("ExecuteStream() error must not be *streamBootstrapError (would downgrade to streamErrorResult): %T", errStream)
		}
		var headerCarrier interface{ Headers() http.Header }
		if !errors.As(errStream, &headerCarrier) || headerCarrier == nil {
			t.Fatalf("ExecuteStream() error %T (%v) does not expose Headers()", errStream, errStream)
		}
		if got := headerCarrier.Headers().Get("Retry-After"); got != "17" {
			t.Fatalf("Headers().Get(Retry-After) = %q, want %q", got, "17")
		}
		if got := headerCarrier.Headers().Get("X-Trace-Id"); got != "trace-stop-7740" {
			t.Fatalf("Headers().Get(X-Trace-Id) = %q, want %q", got, "trace-stop-7740")
		}
		if got := SafeResponseHeaders(errStream).Get("Retry-After"); got == "" {
			t.Fatal("SafeResponseHeaders(errStream).Get(Retry-After) = empty, want trusted Retry-After preserved")
		}
	})

	t.Run("pre_wrapped_stop_bootstrap_preserves_headers", func(t *testing.T) {
		model := "claude-sonnet-4-6-7740-b-" + uuid.NewString()
		m := NewManager(nil, &FillFirstSelector{}, nil)
		m.SetRetryConfig(2, 5*time.Second, 2)

		auth1 := &Auth{ID: "ag-7740-b1-" + uuid.NewString(), Provider: "antigravity", Status: StatusActive}
		auth2 := &Auth{ID: "ag-7740-b2-" + uuid.NewString(), Provider: "antigravity", Status: StatusActive}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth1.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		reg.RegisterClient(auth2.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth1.ID)
			reg.UnregisterClient(auth2.ID)
		})
		if _, errReg1 := m.Register(context.Background(), auth1); errReg1 != nil {
			t.Fatalf("Register(auth1): %v", errReg1)
		}
		if _, errReg2 := m.Register(context.Background(), auth2); errReg2 != nil {
			t.Fatalf("Register(auth2): %v", errReg2)
		}

		var calls atomic.Int32
		m.RegisterExecutor(&customStreamMockExecutor{
			identifier: "antigravity",
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				calls.Add(1)
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{
					Err: wrapRequestStopError(&Error{HTTPStatus: http.StatusForbidden, Message: "permission denied"}),
				}
				close(ch)
				return &cliproxyexecutor.StreamResult{
					Headers: http.Header{
						"Retry-After": []string{"23"},
						"X-Trace-Id":  []string{"trace-prestop-7740"},
					},
					Chunks: ch,
				}, nil
			},
		})

		res, errStream := m.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
		if res != nil {
			t.Fatalf("ExecuteStream() result = %+v, want nil", res)
		}
		if errStream == nil {
			t.Fatal("ExecuteStream() error = nil, want non-nil")
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("executor calls = %d, want 1", got)
		}
		if isRequestStopError(errStream) {
			t.Fatalf("ExecuteStream() leaked internal requestStopError marker: %T", errStream)
		}
		var marked *upstreamExecutionAttemptError
		if errors.As(errStream, &marked) && marked != nil {
			t.Fatalf("ExecuteStream() leaked internal upstreamExecutionAttemptError marker: %#v", errStream)
		}
		var headerCarrier interface{ Headers() http.Header }
		if !errors.As(errStream, &headerCarrier) || headerCarrier == nil {
			t.Fatalf("ExecuteStream() error %T (%v) dropped upstream Headers()", errStream, errStream)
		}
		if got := headerCarrier.Headers().Get("Retry-After"); got != "23" {
			t.Fatalf("Headers().Get(Retry-After) = %q, want %q", got, "23")
		}
		if got := headerCarrier.Headers().Get("X-Trace-Id"); got != "trace-prestop-7740" {
			t.Fatalf("Headers().Get(X-Trace-Id) = %q, want %q", got, "trace-prestop-7740")
		}
	})

	t.Run("terminated_bootstrap_unwraps_internal_attempt_marker_and_preserves_headers", func(t *testing.T) {
		model := "claude-sonnet-4-6-7740-c-" + uuid.NewString()
		m := NewManager(nil, &FillFirstSelector{}, nil)
		m.SetRetryConfig(2, 5*time.Second, 2)

		auth1 := &Auth{ID: "ag-7740-c1-" + uuid.NewString(), Provider: "antigravity", Status: StatusActive}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth1.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { reg.UnregisterClient(auth1.ID) })
		if _, errReg := m.Register(context.Background(), auth1); errReg != nil {
			t.Fatalf("Register(auth1): %v", errReg)
		}

		m.RegisterExecutor(&customStreamMockExecutor{
			identifier: "antigravity",
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{
					Err: &cliproxyexecutor.RequestTerminatedError{
						HTTPStatus: http.StatusForbidden,
						Header:     http.Header{"X-Term": []string{"yes"}},
						Body:       []byte("blocked"),
					},
				}
				close(ch)
				return &cliproxyexecutor.StreamResult{
					Headers: http.Header{"X-Upstream-Trace": []string{"trace-term-7740"}},
					Chunks:  ch,
				}, nil
			},
		})

		res, errStream := m.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
		if res != nil {
			t.Fatalf("ExecuteStream() result = %+v, want nil", res)
		}
		var marked *upstreamExecutionAttemptError
		if errors.As(errStream, &marked) && marked != nil {
			t.Fatalf("ExecuteStream() leaked internal upstreamExecutionAttemptError inside header wrapper: %#v", errStream)
		}
		var terminated *cliproxyexecutor.RequestTerminatedError
		if !errors.As(errStream, &terminated) || terminated == nil || terminated.ResponseHeaders().Get("X-Term") != "yes" {
			t.Fatalf("ExecuteStream() lost RequestTerminatedError: %#v", errStream)
		}
		var headerCarrier interface{ Headers() http.Header }
		if !errors.As(errStream, &headerCarrier) || headerCarrier.Headers().Get("X-Upstream-Trace") != "trace-term-7740" {
			t.Fatalf("ExecuteStream() lost upstream Headers() on terminated bootstrap error: %#v", errStream)
		}
	})
}

type issue7740HomeDispatcher struct {
	calls atomic.Int32
}

func (*issue7740HomeDispatcher) HeartbeatOK() bool       { return true }
func (*issue7740HomeDispatcher) AbortAmbiguousDispatch() {}
func (d *issue7740HomeDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	n := d.calls.Add(1)
	if n == 2 {
		return json.Marshal(map[string]any{
			"error": map[string]any{
				"type":      "auth_unavailable",
				"message":   "round 0 exhausted",
				"retryable": true,
			},
		})
	}
	return json.Marshal(homeAuthDispatchResponse{
		SelectionID: fmt.Sprintf("sel-7740-%d", n),
		Model:       model,
		Provider:    "home-7740",
		AuthIndex:   fmt.Sprintf("auth-7740-%d", n),
		Auth: Auth{
			ID:       fmt.Sprintf("auth-7740-%d", n),
			Provider: "home-7740",
			Status:   StatusActive,
		},
	})
}

func TestIssue7740_HomeRetryRoundPending_ExitsImmediatelyOnStopAndTerminated(t *testing.T) {
	for _, stream := range []bool{false, true} {
		mode := "execute"
		if stream {
			mode = "stream"
		}
		for _, kind := range []string{"stop", "terminated"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				start := time.Unix(1_700_000_000, 0)
				clock := newFakeContinuityClock(start)
				dispatcher := &issue7740HomeDispatcher{}

				m := NewManager(nil, nil, nil)
				m.SetConfig(&internalconfig.Config{Home: internalconfig.HomeConfig{Enabled: true}})
				m.SetRetryConfig(2, 10*time.Second, 2)
				m.SetContinuityClockForTest(clock)
				m.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)

				var execCalls atomic.Int32
				var wantTerminal *cliproxyexecutor.RequestTerminatedError
				var roundOneErr error
				if kind == "terminated" {
					wantTerminal = &cliproxyexecutor.RequestTerminatedError{
						HTTPStatus: http.StatusTooManyRequests,
						Header:     http.Header{"X-Home-Term": []string{"preserved-7740"}},
						Body:       []byte(`{"detail":"terminated on retry round"}`),
					}
					roundOneErr = errors.Join(wantTerminal, &homeDispatchRetryAfterError{
						cause:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "terminated on retry round"},
						retryAfter: 5 * time.Second,
					})
				} else {
					roundOneErr = wrapRequestStopError(&homeDispatchRetryAfterError{
						cause:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "stop immediately on retry round"},
						retryAfter: 5 * time.Second,
					})
				}
				m.RegisterExecutor(&customStreamMockExecutor{
					identifier: "home-7740",
					mockCustomErrorExecutor: mockCustomErrorExecutor{
						identifier: "home-7740",
						executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
							call := execCalls.Add(1)
							cliproxyexecutor.MarkUpstreamAttempt(ctx)
							if call == 1 {
								return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "transient 503", Retryable: true}
							}
							return cliproxyexecutor.Response{}, roundOneErr
						},
					},
					streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						call := execCalls.Add(1)
						cliproxyexecutor.MarkUpstreamAttempt(ctx)
						if call == 1 {
							return nil, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "transient 503", Retryable: true}
						}
						return nil, roundOneErr
					},
				})

				ctx := context.WithValue(context.Background(), continuitySessionContextKey{}, &continuitySession{
					deadline: start.Add(30 * time.Second),
				})
				req := cliproxyexecutor.Request{Model: "model-7740"}
				opts := cliproxyexecutor.Options{Stream: stream}

				var errOut error
				if stream {
					_, errOut = m.ExecuteStream(ctx, []string{"home-7740"}, req, opts)
				} else {
					_, errOut = m.Execute(ctx, []string{"home-7740"}, req, opts)
				}
				if errOut == nil {
					t.Fatal("expected error, got nil")
				}
				if isRequestStopError(errOut) {
					t.Fatalf("leaked internal requestStopError wrapper: %T", errOut)
				}
				if wantTerminal != nil {
					requireTerminalResponse(t, errOut, wantTerminal)
				}
				if gotClock := clock.Now(); !gotClock.Equal(start) {
					t.Fatalf("continuity clock advanced by %v, want 0 (%s error on attempt>=1 must not sleep)", gotClock.Sub(start), kind)
				}
				if gotDispatches := dispatcher.calls.Load(); gotDispatches != 3 {
					t.Fatalf("Home dispatch calls = %d, want 3 (round 0 auth + round 0 exhausted + round 1 %s auth)", gotDispatches, kind)
				}
				if gotExecs := execCalls.Load(); gotExecs != 2 {
					t.Fatalf("executor calls = %d, want 2", gotExecs)
				}
			})
		}
	}
}

func TestIssue7740_ExecuteStreamMixedOnce_HonorsStopAndTerminatedWithoutReEval(t *testing.T) {
	t.Run("plugin_request_terminated_stops_despite_matching_continue_rule", func(t *testing.T) {
		model := "claude-sonnet-4-6-7740-term-" + uuid.NewString()
		m := NewManager(nil, &FillFirstSelector{}, nil)
		m.SetRetryConfig(2, 5*time.Second, 2)
		m.SetConfig(&internalconfig.Config{
			OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
				"antigravity": {
					{
						Status: http.StatusTooManyRequests,
						Match:  []string{"terminated"},
						Action: "continue",
					},
				},
			},
		})

		auth1 := &Auth{
			ID:         "ag-7740-term-1-" + uuid.NewString(),
			Provider:   "antigravity",
			Status:     StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		}
		auth2 := &Auth{
			ID:         "ag-7740-term-2-" + uuid.NewString(),
			Provider:   "antigravity",
			Status:     StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth1.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		reg.RegisterClient(auth2.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth1.ID)
			reg.UnregisterClient(auth2.ID)
		})
		if _, errReg1 := m.Register(context.Background(), auth1); errReg1 != nil {
			t.Fatalf("Register(auth1): %v", errReg1)
		}
		if _, errReg2 := m.Register(context.Background(), auth2); errReg2 != nil {
			t.Fatalf("Register(auth2): %v", errReg2)
		}

		var interceptCalls atomic.Int32
		var streamCalls atomic.Int32
		m.RegisterExecutor(&customStreamMockExecutor{
			identifier: "antigravity",
			streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				streamCalls.Add(1)
				return nil, errors.New("unexpected upstream call")
			},
		})

		opts := cliproxyexecutor.Options{
			Stream: true,
			RequestAfterAuthInterceptor: func(_ context.Context, _ cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				interceptCalls.Add(1)
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{
					Terminate:       true,
					StatusCode:      http.StatusTooManyRequests,
					ResponseHeaders: http.Header{"X-Terminated": []string{"true"}},
					ResponseBody:    []byte("terminated by plugin"),
				}
			},
		}

		res, errStream := m.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, opts)
		if res != nil {
			t.Fatalf("ExecuteStream() result = %+v, want nil", res)
		}
		var terminated *cliproxyexecutor.RequestTerminatedError
		if !errors.As(errStream, &terminated) || terminated == nil {
			t.Fatalf("ExecuteStream() error = %T (%v), want *RequestTerminatedError", errStream, errStream)
		}
		if got := terminated.ResponseHeaders().Get("X-Terminated"); got != "true" {
			t.Fatalf("ResponseHeaders().Get(X-Terminated) = %q, want %q", got, "true")
		}
		if got := interceptCalls.Load(); got != 1 {
			t.Fatalf("interceptor calls = %d, want 1 (must not rotate credentials on RequestTerminatedError)", got)
		}
		if got := streamCalls.Load(); got != 0 {
			t.Fatalf("stream calls = %d, want 0", got)
		}
	})

	t.Run("explicit_stop_error_honored_without_rewrapping", func(t *testing.T) {
		model := "claude-sonnet-4-6-7740-stop-" + uuid.NewString()
		m := NewManager(nil, &FillFirstSelector{}, nil)
		m.SetConfig(&internalconfig.Config{
			OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
				"antigravity": {
					{
						Status: http.StatusTooManyRequests,
						Match:  []string{"explicit stop"},
						Action: "continue",
					},
				},
			},
		})

		auth1 := &Auth{
			ID:         "ag-7740-stop-1-" + uuid.NewString(),
			Provider:   "antigravity",
			Status:     StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		}
		auth2 := &Auth{
			ID:         "ag-7740-stop-2-" + uuid.NewString(),
			Provider:   "antigravity",
			Status:     StatusActive,
			Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
		}
		reg := registry.GetGlobalRegistry()
		reg.RegisterClient(auth1.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		reg.RegisterClient(auth2.ID, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() {
			reg.UnregisterClient(auth1.ID)
			reg.UnregisterClient(auth2.ID)
		})
		if _, errReg1 := m.Register(context.Background(), auth1); errReg1 != nil {
			t.Fatalf("Register(auth1): %v", errReg1)
		}
		if _, errReg2 := m.Register(context.Background(), auth2); errReg2 != nil {
			t.Fatalf("Register(auth2): %v", errReg2)
		}

		var streamCalls atomic.Int32
		m.RegisterExecutor(&customStreamMockExecutor{
			identifier: "antigravity",
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				streamCalls.Add(1)
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return nil, issue7658ExplicitStopError{status: http.StatusTooManyRequests, msg: "explicit stop"}
			},
		})

		homeRetryLimit := -1
		res, errOnce := m.executeStreamMixedOnce(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true}, 2, &homeRetryLimit, 0, 2)
		if res != nil {
			t.Fatalf("executeStreamMixedOnce() result = %+v, want nil", res)
		}
		if errOnce == nil {
			t.Fatal("executeStreamMixedOnce() error = nil, want non-nil")
		}
		if got := streamCalls.Load(); got != 1 {
			t.Fatalf("stream calls = %d, want 1", got)
		}
		if _, rewrapped := errOnce.(requestStopError); rewrapped {
			t.Fatalf("executeStreamMixedOnce() double-wrapped explicit stop error in requestStopError: %T", errOnce)
		}
	})

	t.Run("non_comparable_error_unwrap_does_not_panic", func(t *testing.T) {
		base := issue7740NonComparableError{status: http.StatusForbidden, body: []byte("non-comparable")}
		headers := http.Header{"X-Trace-Id": []string{"trace-nc-7740"}}
		wrappedHeader := wrapRequestStopError(newStreamHeaderError(base, headers))
		unwrappedHeader := unwrapExecutionBoundaryError(wrappedHeader)
		var headerCarrier interface{ Headers() http.Header }
		if !errors.As(unwrappedHeader, &headerCarrier) || headerCarrier.Headers().Get("X-Trace-Id") != "trace-nc-7740" {
			t.Fatalf("unwrapped header error lost Headers(): %#v", unwrappedHeader)
		}
		unwrappedPlain := unwrapExecutionBoundaryError(newStreamBootstrapError(base, headers))
		if !errors.As(unwrappedPlain, &headerCarrier) || headerCarrier.Headers().Get("X-Trace-Id") != "trace-nc-7740" {
			t.Fatalf("unwrapped bootstrap error lost Headers(): %#v", unwrappedPlain)
		}
	})

	for _, viaBootstrap := range []bool{false, true} {
		mode := "direct_400_continue_rotates_with_upstream_attempt_marked"
		if viaBootstrap {
			mode = "bootstrap_400_continue_rotates_with_upstream_attempt_marked"
		}
		t.Run(mode, func(t *testing.T) {
			model := "claude-sonnet-4-6-7740-cont400-" + uuid.NewString()
			m := NewManager(nil, &FillFirstSelector{}, nil)
			m.SetRetryConfig(1, 0, 2)
			m.SetConfig(&internalconfig.Config{
				OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
					"antigravity": {
						{
							Status: http.StatusBadRequest,
							Match:  []string{"retryable_bad_request"},
							Action: "continue",
						},
					},
				},
			})
			auth1 := "ag-7740-c400-1-" + uuid.NewString()
			auth2 := "ag-7740-c400-2-" + uuid.NewString()
			for _, id := range []string{auth1, auth2} {
				if _, errReg := m.Register(context.Background(), &Auth{
					ID:         id,
					Provider:   "antigravity",
					Status:     StatusActive,
					Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth},
				}); errReg != nil {
					t.Fatalf("Register(%s): %v", id, errReg)
				}
				registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
			}

			var called []string
			m.RegisterExecutor(&customStreamMockExecutor{
				identifier: "antigravity",
				streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					called = append(called, auth.ID)
					cliproxyexecutor.MarkUpstreamAttempt(ctx)
					if auth.ID == auth1 {
						err400 := &Error{HTTPStatus: http.StatusBadRequest, Message: "retryable_bad_request"}
						if viaBootstrap {
							ch := make(chan cliproxyexecutor.StreamChunk, 1)
							ch <- cliproxyexecutor.StreamChunk{Err: err400}
							close(ch)
							return &cliproxyexecutor.StreamResult{
								Headers: http.Header{"X-Trace-Id": []string{"trace-400"}},
								Chunks:  ch,
							}, nil
						}
						return nil, err400
					}
					ch := make(chan cliproxyexecutor.StreamChunk, 1)
					ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok\n\n")}
					close(ch)
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				},
			})

			res, errStream := m.ExecuteStream(context.Background(), []string{"antigravity"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
			if errStream != nil {
				t.Fatalf("ExecuteStream() error = %v, want rotation to second credential on continue rule", errStream)
			}
			var out []byte
			for chunk := range res.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected chunk error: %v", chunk.Err)
				}
				out = append(out, chunk.Payload...)
			}
			if string(out) != "data: ok\n\n" {
				t.Fatalf("stream output = %q, want %q", string(out), "data: ok\n\n")
			}
			if len(called) != 2 || called[0] != auth1 || called[1] != auth2 {
				t.Fatalf("credential calls = %v, want [%s %s]", called, auth1, auth2)
			}
		})
	}
}

type issue7740NonComparableError struct {
	status int
	body   []byte
}

func (e issue7740NonComparableError) Error() string   { return string(e.body) }
func (e issue7740NonComparableError) StatusCode() int { return e.status }
