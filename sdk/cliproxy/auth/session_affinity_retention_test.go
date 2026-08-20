package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type configurableTestExecutor struct {
	provider string
	calls    atomic.Int32
	handler  func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
}

func (e *configurableTestExecutor) Identifier() string { return e.provider }
func (e *configurableTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	if e.handler != nil {
		return e.handler(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}
func (e *configurableTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.calls.Add(1)
	return nil, nil
}
func (e *configurableTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}
func (e *configurableTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *configurableTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestSessionAffinity_Transient503RetainsBindingAcrossRecovery(t *testing.T) {
	ctx := context.Background()
	p1 := "affinity-503-p1"
	p2 := "affinity-503-p2"
	model := "test-model-503"
	auth1ID := "auth-1-503"
	auth2ID := "auth-2-503"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)

	var auth1ShouldFail atomic.Bool
	auth1ShouldFail.Store(true)

	exec1 := &configurableTestExecutor{
		provider: p1,
		handler: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			if auth1ShouldFail.Load() {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "service unavailable 503"}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"served_by":"auth-1"}`)}, nil
		},
	}
	exec2 := &configurableTestExecutor{
		provider: p2,
		handler: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{Payload: []byte(`{"served_by":"auth-2"}`)}, nil
		},
	}
	manager.RegisterExecutor(exec1)
	manager.RegisterExecutor(exec2)

	for _, auth := range []*Auth{
		{ID: auth1ID, Provider: p1, Status: StatusActive},
		{ID: auth2ID, Provider: p2, Status: StatusActive},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{"sess-retention-503"}},
	}

	// 1. First Execute: auth-1 fails with transient 503, retry falls over to auth-2 which succeeds.
	resp, errExec := manager.Execute(ctx, []string{p1, p2}, req, opts)
	if errExec != nil {
		t.Fatalf("first Execute failed: %v", errExec)
	}
	if string(resp.Payload) != `{"served_by":"auth-2"}` {
		t.Fatalf("first Execute payload = %s, want auth-2", string(resp.Payload))
	}

	// Session affinity binding must STILL point to auth-1 (retained despite transient failure)
	sessionKey := "mixed::header:sess-retention-503::" + model
	boundAuthID, ok := affinity.cache.Get(sessionKey)
	if !ok {
		t.Fatalf("expected sessionKey %q to remain in cache after transient 503", sessionKey)
	}
	if boundAuthID != auth1ID {
		t.Fatalf("sessionKey bound to %q, want %q (transient 503 must not purge affinity)", boundAuthID, auth1ID)
	}

	// 2. Cooldown for auth-1 clears
	auth1ShouldFail.Store(false)
	expireSessionAffinityPriorityModelCooldown(t, manager, auth1ID, model)

	// 3. Second Execute for the SAME session must return to auth-1 where prompt cache lives
	resp2, errExec2 := manager.Execute(ctx, []string{p1, p2}, req, opts)
	if errExec2 != nil {
		t.Fatalf("second Execute failed: %v", errExec2)
	}
	if string(resp2.Payload) != `{"served_by":"auth-1"}` {
		t.Fatalf("second Execute payload = %s, want auth-1 (session should return to original warm cache auth)", string(resp2.Payload))
	}
}

func TestSessionAffinity_Transient429RetryAfterRetainsBindingAcrossRecovery(t *testing.T) {
	ctx := context.Background()
	p1 := "affinity-429-p1"
	p2 := "affinity-429-p2"
	model := "test-model-429"
	auth1ID := "auth-1-429"
	auth2ID := "auth-2-429"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)

	var auth1ShouldFail atomic.Bool
	auth1ShouldFail.Store(true)

	retryAfterDuration := 100 * time.Millisecond
	exec1 := &configurableTestExecutor{
		provider: p1,
		handler: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			if auth1ShouldFail.Load() {
				return cliproxyexecutor.Response{}, &retryAfterStatusError{
					status:     http.StatusTooManyRequests,
					retryAfter: retryAfterDuration,
					message:    "rate limited 429",
				}
			}
			return cliproxyexecutor.Response{Payload: []byte(`{"served_by":"auth-1"}`)}, nil
		},
	}
	exec2 := &configurableTestExecutor{
		provider: p2,
		handler: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{Payload: []byte(`{"served_by":"auth-2"}`)}, nil
		},
	}
	manager.RegisterExecutor(exec1)
	manager.RegisterExecutor(exec2)

	for _, auth := range []*Auth{
		{ID: auth1ID, Provider: p1, Status: StatusActive},
		{ID: auth2ID, Provider: p2, Status: StatusActive},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{"sess-retention-429"}},
	}

	// 1. First Execute: auth-1 returns 429, request falls over to auth-2 which succeeds.
	resp, errExec := manager.Execute(ctx, []string{p1, p2}, req, opts)
	if errExec != nil {
		t.Fatalf("first Execute failed: %v", errExec)
	}
	if string(resp.Payload) != `{"served_by":"auth-2"}` {
		t.Fatalf("first Execute payload = %s, want auth-2", string(resp.Payload))
	}

	// Binding must STILL be auth-1
	sessionKey := "mixed::header:sess-retention-429::" + model
	boundAuthID, ok := affinity.cache.Get(sessionKey)
	if !ok {
		t.Fatalf("expected sessionKey %q to remain in cache after 429 rate limit", sessionKey)
	}
	if boundAuthID != auth1ID {
		t.Fatalf("sessionKey bound to %q, want %q", boundAuthID, auth1ID)
	}

	// 2. Cooldown for auth-1 clears
	auth1ShouldFail.Store(false)
	expireSessionAffinityPriorityModelCooldown(t, manager, auth1ID, model)

	// 3. Next Execute returns to auth-1
	resp2, errExec2 := manager.Execute(ctx, []string{p1, p2}, req, opts)
	if errExec2 != nil {
		t.Fatalf("second Execute failed: %v", errExec2)
	}
	if string(resp2.Payload) != `{"served_by":"auth-1"}` {
		t.Fatalf("second Execute payload = %s, want auth-1", string(resp2.Payload))
	}
}

func TestSessionAffinity_Terminal401InvalidAPIKeyUnbindsSession(t *testing.T) {
	ctx := context.Background()
	p1 := "affinity-401-p1"
	p2 := "affinity-401-p2"
	model := "test-model-401"
	auth1ID := "auth-1-401"
	auth2ID := "auth-2-401"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)

	exec1 := &configurableTestExecutor{
		provider: p1,
		handler: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &Error{
				Code:       "unauthorized",
				HTTPStatus: http.StatusUnauthorized,
				Message:    "invalid_api_key",
			}
		},
	}
	exec2 := &configurableTestExecutor{
		provider: p2,
		handler: func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{Payload: []byte(`{"served_by":"auth-2"}`)}, nil
		},
	}
	manager.RegisterExecutor(exec1)
	manager.RegisterExecutor(exec2)

	for _, auth := range []*Auth{
		{ID: auth1ID, Provider: p1, Status: StatusActive},
		{ID: auth2ID, Provider: p2, Status: StatusActive},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"X-Session-Id": []string{"sess-retention-401"}},
	}

	// 1. First Execute: auth-1 fails with 401 terminal error, unbinds, falls over to auth-2 which succeeds and binds.
	resp, errExec := manager.Execute(ctx, []string{p1, p2}, req, opts)
	if errExec != nil {
		t.Fatalf("first Execute failed: %v", errExec)
	}
	if string(resp.Payload) != `{"served_by":"auth-2"}` {
		t.Fatalf("first Execute payload = %s, want auth-2", string(resp.Payload))
	}

	// Session is now permanently rebound to auth-2
	sessionKey := "mixed::header:sess-retention-401::" + model
	boundAuthID, ok := affinity.cache.Get(sessionKey)
	if !ok {
		t.Fatalf("expected sessionKey %q to be bound to auth-2", sessionKey)
	}
	if boundAuthID != auth2ID {
		t.Fatalf("sessionKey bound to %q, want %q (terminal 401 should rebind to next auth)", boundAuthID, auth2ID)
	}

	// 2. Subsequent requests for the same session stay on auth-2
	resp2, errExec2 := manager.Execute(ctx, []string{p1, p2}, req, opts)
	if errExec2 != nil {
		t.Fatalf("second Execute failed: %v", errExec2)
	}
	if string(resp2.Payload) != `{"served_by":"auth-2"}` {
		t.Fatalf("second Execute payload = %s, want auth-2", string(resp2.Payload))
	}
}

func TestSessionAffinitySelector_RequestScopedExclusionBreaksCarousel(t *testing.T) {
	t.Parallel()

	fallback := &RoundRobinSelector{}
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: fallback,
		TTL:      time.Minute,
	})
	defer selector.Stop()

	auths := []*Auth{
		{ID: "auth-a"},
		{ID: "auth-b"},
		{ID: "auth-c"},
	}

	payload := []byte(`{"metadata":{"user_id":"user_xxx_account__session_carousel-break"}}`)
	opts := cliproxyexecutor.Options{OriginalRequest: payload}

	// 1. First pick establishes affinity binding to auth-a
	first, err := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if first.ID != "auth-a" {
		t.Fatalf("initial pick = %q, want auth-a", first.ID)
	}
	selector.OnResult(Result{AuthID: first.ID, Provider: "claude", Model: "claude-3", Options: opts, Success: true})

	// 2. Simulated retries within one request: auth-a failed and is excluded from available candidates
	availableWithoutFirst := make([]*Auth, 0, len(auths)-1)
	for _, a := range auths {
		if a.ID != first.ID {
			availableWithoutFirst = append(availableWithoutFirst, a)
		}
	}

	// 20 successive retry attempts within the request must NEVER return auth-a
	for attempt := 0; attempt < 20; attempt++ {
		got, errPick := selector.Pick(context.Background(), "claude", "claude-3", opts, availableWithoutFirst)
		if errPick != nil {
			t.Fatalf("attempt %d Pick() error = %v", attempt, errPick)
		}
		if got.ID == first.ID {
			t.Fatalf("attempt %d returned excluded auth %q, expected different", attempt, first.ID)
		}
	}

	// 3. New request after recovery with full candidates returns to auth-a (warm cache retained)
	recovered, errRecovered := selector.Pick(context.Background(), "claude", "claude-3", opts, auths)
	if errRecovered != nil {
		t.Fatalf("Pick() after recovery error = %v", errRecovered)
	}
	if recovered.ID != first.ID {
		t.Fatalf("Pick() after recovery = %q, want original bound auth %q", recovered.ID, first.ID)
	}
}

func TestSessionAffinitySelector_OnResult_TransientVsTerminalClassification(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		err        *Error
		wantRetain bool
	}

	cases := []testCase{
		{
			name:       "500 Internal Server Error (transient)",
			err:        &Error{HTTPStatus: http.StatusInternalServerError, Message: "internal error"},
			wantRetain: true,
		},
		{
			name:       "502 Bad Gateway (transient)",
			err:        &Error{HTTPStatus: http.StatusBadGateway, Message: "bad gateway"},
			wantRetain: true,
		},
		{
			name:       "503 Service Unavailable (transient)",
			err:        &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "service unavailable"},
			wantRetain: true,
		},
		{
			name:       "504 Gateway Timeout (transient)",
			err:        &Error{HTTPStatus: http.StatusGatewayTimeout, Message: "gateway timeout"},
			wantRetain: true,
		},
		{
			name:       "429 Too Many Requests / Quota (transient)",
			err:        &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limit exceeded"},
			wantRetain: true,
		},
		{
			name:       "408 Request Timeout (transient)",
			err:        &Error{HTTPStatus: http.StatusRequestTimeout, Message: "request timeout"},
			wantRetain: true,
		},
		{
			name:       "Cloudflare challenge (transient)",
			err:        &Error{HTTPStatus: http.StatusForbidden, Message: "just a moment... cloudflare challenge"},
			wantRetain: true,
		},
		{
			name:       "400 Bad Request / client fault (skip cooldown, retain)",
			err:        &Error{HTTPStatus: http.StatusBadRequest, Message: `{"error":{"type":"invalid_request_error"}}`},
			wantRetain: true,
		},
		{
			name:       "401 Unauthorized / invalid API key (terminal)",
			err:        &Error{HTTPStatus: http.StatusUnauthorized, Message: "invalid_api_key"},
			wantRetain: false,
		},
		{
			name:       "402 Payment Required / out of credits (terminal)",
			err:        &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
			wantRetain: false,
		},
		{
			name:       "403 Forbidden / account disabled (terminal)",
			err:        &Error{HTTPStatus: http.StatusForbidden, Message: "account suspended"},
			wantRetain: false,
		},
		{
			name:       "404 Not Found / unsupported model (terminal)",
			err:        &Error{HTTPStatus: http.StatusNotFound, Message: "model not found for plan"},
			wantRetain: false,
		},
		{
			name:       "invalid_grant OAuth token revoked (terminal)",
			err:        &Error{HTTPStatus: http.StatusBadRequest, Message: `{"error":"invalid_grant"}`},
			wantRetain: false,
		},
		{
			name:       "model_not_supported error (terminal)",
			err:        &Error{HTTPStatus: http.StatusBadRequest, Message: "requested model is not supported for your plan"},
			wantRetain: false,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: &RoundRobinSelector{},
				TTL:      time.Hour,
			})
			defer selector.Stop()

			sessionID := "sess-classify-" + tc.name
			model := "test-model"
			authID := "auth-target"
			cacheKey := "mixed::header:" + sessionID + "::" + model

			selector.cache.Set(cacheKey, authID)

			opts := cliproxyexecutor.Options{
				Headers: http.Header{"X-Session-Id": []string{sessionID}},
				Metadata: map[string]any{
					cliproxyexecutor.SessionAffinityProviderMetadataKey: "mixed",
					cliproxyexecutor.SessionAffinityModelMetadataKey:    model,
				},
			}

			selector.OnResult(Result{
				AuthID:   authID,
				Provider: "claude",
				Model:    model,
				Success:  false,
				Error:    tc.err,
				Options:  opts,
			})

			got, ok := selector.cache.Get(cacheKey)
			if tc.wantRetain {
				if !ok || got != authID {
					t.Fatalf("cache binding purged, want retained auth %q (ok=%v, got=%q)", authID, ok, got)
				}
			} else {
				if ok {
					t.Fatalf("cache binding unexpectedly retained %q, want purged", got)
				}
			}
		})
	}
}
