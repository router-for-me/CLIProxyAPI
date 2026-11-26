package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type lateVariantStatusError struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e *lateVariantStatusError) Error() string              { return e.msg }
func (e *lateVariantStatusError) StatusCode() int            { return e.code }
func (e *lateVariantStatusError) RetryAfter() *time.Duration { return e.retryAfter }

func setupAntigravityLateVariantAffinityFixture(t *testing.T, rebindOn string) (*Manager, *SessionAffinitySelector, *Auth, *Auth) {
	t.Helper()
	withQuotaCooldownEnabled(t)
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatalf("ShutdownLocal(): %v", errShutdown)
	}
	t.Cleanup(func() {
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Errorf("ShutdownLocal() cleanup: %v", errShutdown)
		}
	})

	ctx := context.Background()
	const (
		provider   = "antigravity"
		routeModel = "gemini-4-hydrogen-xhigh"
		targetHigh = "gemini-4-hydrogen-high"
		targetLow  = "gemini-4-hydrogen-low"
	)

	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: rebindOn,
	})
	t.Cleanup(affinity.Stop)

	manager := NewManager(nil, affinity, nil)
	manager.SetRetryConfig(0, 0, 0)

	suffix := t.Name()
	authA := &Auth{
		ID:         "ag-late-a-" + suffix,
		Provider:   provider,
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "1"},
		Metadata:   map[string]any{"refresh_token": "refresh-a-" + suffix},
	}
	authB := &Auth{
		ID:         "ag-late-b-" + suffix,
		Provider:   provider,
		Status:     StatusActive,
		Attributes: map[string]string{"priority": "0"},
		Metadata:   map[string]any{"refresh_token": "refresh-b-" + suffix},
	}

	models := map[string]string{
		routeModel: "MODEL_GEMINI_4_HYDROGEN_XHIGH",
		targetHigh: "MODEL_GEMINI_4_HYDROGEN_HIGH",
		targetLow:  "MODEL_GEMINI_4_HYDROGEN_LOW",
	}
	groups := map[string]map[string]int{
		"gemini-4-hydrogen": {
			"low":   65536,
			"high":  65536,
			"xhigh": 65536,
		},
	}

	var registeredA, registeredB *Auth
	for i, candidate := range []*Auth{authA, authB} {
		registered, errRegister := manager.Register(WithSkipPersist(ctx), candidate)
		if errRegister != nil {
			t.Fatalf("Register(%s): %v", candidate.ID, errRegister)
		}
		if !StoreAntigravityModelCatalog(ctx, registered, models, groups, nil) {
			t.Fatalf("StoreAntigravityModelCatalog(%s) = false", registered.ID)
		}
		registry.GetGlobalRegistry().RegisterClient(registered.ID, provider, []*registry.ModelInfo{
			{ID: routeModel},
			{ID: targetHigh},
			{ID: targetLow},
		})
		id := registered.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		if i == 0 {
			registeredA = registered
		} else {
			registeredB = registered
		}
	}

	return manager, affinity, registeredA, registeredB
}

func TestAntigravityLateVariantSessionAffinityQuotaHandling(t *testing.T) {
	const (
		provider   = "antigravity"
		routeModel = "gemini-4-hydrogen-xhigh"
		targetHigh = "gemini-4-hydrogen-high"
		targetLow  = "gemini-4-hydrogen-low"
	)
	highThinkingBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"HIGH"}}}`)
	xhighThinkingBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"XHIGH"}}}`)

	t.Run("rebind_never_upstream_429_and_subsequent_turn", func(t *testing.T) {
		manager, _, authA, authB := setupAntigravityLateVariantAffinityFixture(t, SessionAffinityRebindOnNever)
		ctx := context.Background()

		calls := make(map[string]int)
		exhaustAuthA := false
		retryAfter := 155 * time.Hour

		runAttempt := func(attemptCtx context.Context, auth *Auth, req cliproxyexecutor.Request) error {
			calls[auth.ID]++
			selected := SelectAntigravityModel(attemptCtx, auth, req.Model, "high", nil)
			if selected.ID != targetHigh {
				t.Fatalf("SelectAntigravityModel(%s) = %q, want %q", auth.ID, selected.ID, targetHigh)
			}
			if errCheck := CheckAntigravityTargetModel(attemptCtx, auth, selected.ID, time.Now(), false); errCheck != nil {
				return RejectAntigravityTarget(errCheck)
			}
			cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, selected.ID)
			if auth.ID == authA.ID && exhaustAuthA {
				return &lateVariantStatusError{
					code:       http.StatusTooManyRequests,
					msg:        `{"error":{"status":"RESOURCE_EXHAUSTED","details":[{"reason":"QUOTA_EXHAUSTED","metadata":{"model":"gemini-4-hydrogen-high"}}]}}`,
					retryAfter: &retryAfter,
				}
			}
			return nil
		}

		manager.RegisterExecutor(scriptedFailoverExecutor{
			provider: provider,
			execute: func(attemptCtx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if errRun := runAttempt(attemptCtx, auth, req); errRun != nil {
					return cliproxyexecutor.Response{}, errRun
				}
				return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
			},
			stream: func(attemptCtx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if errRun := runAttempt(attemptCtx, auth, req); errRun != nil {
					return nil, errRun
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		})

		req := cliproxyexecutor.Request{Model: routeModel, Payload: highThinkingBody}
		opts := cliproxyexecutor.Options{
			Headers:         http.Header{"Session-Id": []string{"ag-late-never-session"}},
			SourceFormat:    sdktranslator.FormatGemini,
			OriginalRequest: highThinkingBody,
		}

		// Turn 1: bind session to authA.
		resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
		if errExec != nil || string(resp.Payload) != authA.ID {
			t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authA.ID)
		}

		// Turn 2: authA fails with upstream 429 QUOTA_EXHAUSTED on late variant targetHigh.
		exhaustAuthA = true
		calls[authA.ID] = 0
		calls[authB.ID] = 0
		_, errExec = manager.Execute(ctx, []string{provider}, req, opts)
		if errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
			t.Fatalf("Execute on late-variant 429 = %v (status=%d), want HTTP 429 (not 503 session_auth_retry)", errExec, statusCodeFromError(errExec))
		}
		var authErr *Error
		if errors.As(errExec, &authErr) && authErr != nil && authErr.Code == "session_auth_retry" {
			t.Fatalf("Execute returned session_auth_retry: %v", errExec)
		}
		if calls[authB.ID] != 0 {
			t.Fatalf("rebind-on=never called authB %d times, want 0", calls[authB.ID])
		}

		// Turn 3 (Execute): authA is rejected by CheckAntigravityTargetModel; must still return 429, not 503 session_auth_retry.
		_, errExec = manager.Execute(ctx, []string{provider}, req, opts)
		if errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
			t.Fatalf("subsequent Execute on cooled late variant = %v (status=%d), want HTTP 429", errExec, statusCodeFromError(errExec))
		}
		if errors.As(errExec, &authErr) && authErr != nil && authErr.Code == "session_auth_retry" {
			t.Fatalf("subsequent Execute returned session_auth_retry: %v", errExec)
		}
		if calls[authB.ID] != 0 {
			t.Fatalf("rebind-on=never subsequent Execute called authB %d times, want 0", calls[authB.ID])
		}

		// Turn 4 (ExecuteStream): must also return 429, not 503 session_auth_retry.
		streamOpts := opts
		streamOpts.Stream = true
		_, errStream := manager.ExecuteStream(ctx, []string{provider}, req, streamOpts)
		if errStream == nil || statusCodeFromError(errStream) != http.StatusTooManyRequests {
			t.Fatalf("subsequent ExecuteStream on cooled late variant = %v (status=%d), want HTTP 429", errStream, statusCodeFromError(errStream))
		}
		if errors.As(errStream, &authErr) && authErr != nil && authErr.Code == "session_auth_retry" {
			t.Fatalf("subsequent ExecuteStream returned session_auth_retry: %v", errStream)
		}
		if calls[authB.ID] != 0 {
			t.Fatalf("rebind-on=never subsequent ExecuteStream called authB %d times, want 0", calls[authB.ID])
		}
	})

	t.Run("rebind_quota_rebinds_after_late_variant_quota_exhausted", func(t *testing.T) {
		manager, affinity, authA, authB := setupAntigravityLateVariantAffinityFixture(t, SessionAffinityRebindOnQuota)
		ctx := context.Background()

		calls := make(map[string]int)
		runAttempt := func(attemptCtx context.Context, auth *Auth, req cliproxyexecutor.Request) error {
			calls[auth.ID]++
			selected := SelectAntigravityModel(attemptCtx, auth, req.Model, "high", nil)
			if errCheck := CheckAntigravityTargetModel(attemptCtx, auth, selected.ID, time.Now(), false); errCheck != nil {
				return RejectAntigravityTarget(errCheck)
			}
			cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, selected.ID)
			return nil
		}

		manager.RegisterExecutor(scriptedFailoverExecutor{
			provider: provider,
			execute: func(attemptCtx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				if errRun := runAttempt(attemptCtx, auth, req); errRun != nil {
					return cliproxyexecutor.Response{}, errRun
				}
				return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
			},
		})

		req := cliproxyexecutor.Request{Model: routeModel, Payload: highThinkingBody}
		opts := cliproxyexecutor.Options{
			Headers:         http.Header{"Session-Id": []string{"ag-late-quota-session"}},
			SourceFormat:    sdktranslator.FormatGemini,
			OriginalRequest: highThinkingBody,
		}

		// Turn 1: bind session to authA.
		resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
		if errExec != nil || string(resp.Payload) != authA.ID {
			t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authA.ID)
		}

		// Put authA's targetHigh variant into 155h quota cooldown (simulating exhaustion from another session/turn).
		retryAfter := 155 * time.Hour
		attemptCtx := manager.newUpstreamAttemptContext(ctx)
		cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, targetHigh)
		manager.MarkResult(attemptCtx, Result{
			AuthID:     authA.ID,
			Provider:   provider,
			Model:      routeModel,
			RouteModel: routeModel,
			Success:    false,
			RetryAfter: &retryAfter,
			Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "QUOTA_EXHAUSTED"},
		})

		// Turn 2: authA is rejected by CheckAntigravityTargetModel and must fail over AND rebind to authB.
		calls[authA.ID] = 0
		calls[authB.ID] = 0
		resp, errExec = manager.Execute(ctx, []string{provider}, req, opts)
		if errExec != nil || string(resp.Payload) != authB.ID {
			t.Fatalf("failover Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authB.ID)
		}
		boundID, okBound := affinity.boundAuthForRequest([]string{provider}, routeModel, opts)
		if !okBound || boundID != authB.ID {
			t.Fatalf("boundAuthForRequest after quota failover = (%q, %v), want (%q, true)", boundID, okBound, authB.ID)
		}

		// Turn 3: must route directly to authB without probing authA again.
		calls[authA.ID] = 0
		calls[authB.ID] = 0
		resp, errExec = manager.Execute(ctx, []string{provider}, req, opts)
		if errExec != nil || string(resp.Payload) != authB.ID {
			t.Fatalf("post-rebind Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authB.ID)
		}
		if calls[authA.ID] != 0 || calls[authB.ID] != 1 {
			t.Fatalf("post-rebind calls = (A=%d, B=%d), want (0, 1)", calls[authA.ID], calls[authB.ID])
		}
	})

	t.Run("rebind_never_transient_502_on_late_variant_still_returns_503_session_auth_retry", func(t *testing.T) {
		manager, _, authA, authB := setupAntigravityLateVariantAffinityFixture(t, SessionAffinityRebindOnNever)
		ctx := context.Background()

		fail502 := false
		calls := make(map[string]int)
		manager.RegisterExecutor(scriptedFailoverExecutor{
			provider: provider,
			execute: func(attemptCtx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls[auth.ID]++
				selected := SelectAntigravityModel(attemptCtx, auth, req.Model, "high", nil)
				cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, selected.ID)
				if auth.ID == authA.ID && fail502 {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadGateway, Message: "502 bad gateway"}
				}
				return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
			},
		})

		req := cliproxyexecutor.Request{Model: routeModel, Payload: highThinkingBody}
		opts := cliproxyexecutor.Options{
			Headers:         http.Header{"Session-Id": []string{"ag-late-502-session"}},
			SourceFormat:    sdktranslator.FormatGemini,
			OriginalRequest: highThinkingBody,
		}

		resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
		if errExec != nil || string(resp.Payload) != authA.ID {
			t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authA.ID)
		}

		fail502 = true
		calls[authA.ID] = 0
		calls[authB.ID] = 0
		_, errExec = manager.Execute(ctx, []string{provider}, req, opts)
		var authErr *Error
		if !errors.As(errExec, &authErr) || authErr == nil || authErr.Code != "session_auth_retry" || authErr.HTTPStatus != http.StatusServiceUnavailable {
			t.Fatalf("Execute on late-variant 502 = %v, want 503 session_auth_retry", errExec)
		}
		if calls[authB.ID] != 0 {
			t.Fatalf("rebind-on=never 502 called authB %d times, want 0", calls[authB.ID])
		}
	})

	t.Run("unrelated_variant_cooldown_does_not_block_healthy_variant", func(t *testing.T) {
		manager, _, authA, authB := setupAntigravityLateVariantAffinityFixture(t, SessionAffinityRebindOnNever)
		ctx := context.Background()

		fail502 := false
		calls := make(map[string]int)
		manager.RegisterExecutor(scriptedFailoverExecutor{
			provider: provider,
			execute: func(attemptCtx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls[auth.ID]++
				selected := SelectAntigravityModel(attemptCtx, auth, req.Model, "xhigh", nil)
				if selected.ID != routeModel {
					t.Fatalf("SelectAntigravityModel(%s, xhigh) = %q, want %q", auth.ID, selected.ID, routeModel)
				}
				if auth.ID == authA.ID && fail502 {
					return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadGateway, Message: "502 bad gateway"}
				}
				return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
			},
		})

		req := cliproxyexecutor.Request{Model: routeModel, Payload: xhighThinkingBody}
		opts := cliproxyexecutor.Options{
			Headers:         http.Header{"Session-Id": []string{"ag-late-xhigh-healthy-session"}},
			SourceFormat:    sdktranslator.FormatGemini,
			OriginalRequest: xhighThinkingBody,
		}

		// Bind session to authA on xhigh.
		resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
		if errExec != nil || string(resp.Payload) != authA.ID {
			t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authA.ID)
		}

		// Cool only targetHigh; routeModel (xhigh) remains healthy.
		retryAfter := 155 * time.Hour
		attemptCtx := manager.newUpstreamAttemptContext(ctx)
		cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, targetHigh)
		manager.MarkResult(attemptCtx, Result{
			AuthID:     authA.ID,
			Provider:   provider,
			Model:      routeModel,
			RouteModel: routeModel,
			Success:    false,
			RetryAfter: &retryAfter,
			Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "QUOTA_EXHAUSTED"},
		})

		// A transient 502 on the healthy xhigh variant must still yield 503 session_auth_retry (not be misclassified as quota-blocked by targetHigh).
		fail502 = true
		calls[authA.ID] = 0
		calls[authB.ID] = 0
		_, errExec = manager.Execute(ctx, []string{provider}, req, opts)
		var authErr *Error
		if !errors.As(errExec, &authErr) || authErr == nil || authErr.Code != "session_auth_retry" || authErr.HTTPStatus != http.StatusServiceUnavailable {
			t.Fatalf("Execute on healthy xhigh variant 502 = %v, want 503 session_auth_retry", errExec)
		}
		if calls[authB.ID] != 0 {
			t.Fatalf("rebind-on=never called authB %d times, want 0", calls[authB.ID])
		}
	})

	t.Run("rebind_quota_stream_and_direct_pick_handle_late_variant_quota", func(t *testing.T) {
		manager, affinity, authA, authB := setupAntigravityLateVariantAffinityFixture(t, SessionAffinityRebindOnQuota)
		ctx := context.Background()

		calls := make(map[string]int)
		manager.RegisterExecutor(scriptedFailoverExecutor{
			provider: provider,
			stream: func(attemptCtx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				calls[auth.ID]++
				selected := SelectAntigravityModel(attemptCtx, auth, req.Model, "high", nil)
				if errCheck := CheckAntigravityTargetModel(attemptCtx, auth, selected.ID, time.Now(), false); errCheck != nil {
					return nil, RejectAntigravityTarget(errCheck)
				}
				cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, selected.ID)
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		})

		req := cliproxyexecutor.Request{Model: routeModel, Payload: highThinkingBody}
		opts := cliproxyexecutor.Options{
			Headers:         http.Header{"Session-Id": []string{"ag-late-stream-quota-session"}},
			SourceFormat:    sdktranslator.FormatGemini,
			OriginalRequest: highThinkingBody,
			Stream:          true,
		}

		readStream := func(streamRes *cliproxyexecutor.StreamResult) string {
			t.Helper()
			var out []byte
			for chunk := range streamRes.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected stream chunk err: %v", chunk.Err)
				}
				out = append(out, chunk.Payload...)
			}
			return string(out)
		}

		// Turn 1: bind stream session to authA.
		res1, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts)
		if errStream != nil || readStream(res1) != authA.ID {
			t.Fatalf("cold ExecuteStream err=%v", errStream)
		}

		// Exhaust authA on late variant targetHigh.
		retryAfter := 155 * time.Hour
		attemptCtx := manager.newUpstreamAttemptContext(ctx)
		cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, targetHigh)
		manager.MarkResult(attemptCtx, Result{
			AuthID:     authA.ID,
			Provider:   provider,
			Model:      routeModel,
			RouteModel: routeModel,
			Success:    false,
			RetryAfter: &retryAfter,
			Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "QUOTA_EXHAUSTED"},
		})

		// Verify direct non-prevalidated shouldPreserveBindingWhenUnavailable also recognizes the late variant quota block.
		currentA, okA := manager.GetByID(authA.ID)
		if !okA || currentA == nil {
			t.Fatalf("GetByID(%s) missing", authA.ID)
		}
		if affinity.shouldPreserveBindingWhenUnavailable(ctx, []*Auth{currentA}, routeModel, opts, authA.ID, time.Now()) {
			t.Fatalf("direct shouldPreserveBindingWhenUnavailable() = true for quota-exhausted late variant, want false")
		}

		// Turn 2 (stream): must fail over AND rebind to authB.
		res2, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts)
		if errStream != nil {
			t.Fatalf("failover ExecuteStream err=%v", errStream)
		}
		if got := readStream(res2); got != authB.ID {
			t.Fatalf("failover ExecuteStream payload = %q, want %q", got, authB.ID)
		}
		boundID, okBound := affinity.boundAuthForRequest([]string{provider}, routeModel, opts)
		if !okBound || boundID != authB.ID {
			t.Fatalf("stream boundAuthForRequest = (%q, %v), want (%q, true)", boundID, okBound, authB.ID)
		}
	})

	t.Run("observed_attempt_model_without_original_request_classifies_quota_block", func(t *testing.T) {
		manager, _, authA, authB := setupAntigravityLateVariantAffinityFixture(t, SessionAffinityRebindOnNever)
		ctx := context.Background()

		exhaust := false
		retryAfter := 155 * time.Hour
		calls := make(map[string]int)
		manager.RegisterExecutor(scriptedFailoverExecutor{
			provider: provider,
			execute: func(attemptCtx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				calls[auth.ID]++
				if errCheck := CheckAntigravityTargetModel(attemptCtx, auth, targetHigh, time.Now(), false); errCheck != nil {
					return cliproxyexecutor.Response{}, RejectAntigravityTarget(errCheck)
				}
				cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, targetHigh)
				if auth.ID == authA.ID && exhaust {
					return cliproxyexecutor.Response{}, &lateVariantStatusError{
						code:       http.StatusTooManyRequests,
						msg:        "QUOTA_EXHAUSTED",
						retryAfter: &retryAfter,
					}
				}
				return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
			},
		})

		req := cliproxyexecutor.Request{Model: routeModel}
		// Deliberately omit OriginalRequest and SourceFormat to exercise the per-request observed-model tracker.
		opts := cliproxyexecutor.Options{
			Headers: http.Header{"Session-Id": []string{"ag-late-observed-only-session"}},
		}

		resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
		if errExec != nil || string(resp.Payload) != authA.ID {
			t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authA.ID)
		}

		exhaust = true
		calls[authA.ID] = 0
		calls[authB.ID] = 0
		_, errExec = manager.Execute(ctx, []string{provider}, req, opts)
		if errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
			t.Fatalf("Execute without OriginalRequest on late-variant 429 = %v (status=%d), want HTTP 429", errExec, statusCodeFromError(errExec))
		}
		if calls[authB.ID] != 0 {
			t.Fatalf("rebind-on=never called authB %d times, want 0", calls[authB.ID])
		}

		// Subsequent turn hits CheckAntigravityTargetModel rejection without OriginalRequest; must also return 429.
		_, errExec = manager.Execute(ctx, []string{provider}, req, opts)
		if errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
			t.Fatalf("subsequent Execute without OriginalRequest = %v (status=%d), want HTTP 429", errExec, statusCodeFromError(errExec))
		}
		if calls[authB.ID] != 0 {
			t.Fatalf("rebind-on=never subsequent Execute called authB %d times, want 0", calls[authB.ID])
		}
	})

	t.Run("stream_bootstrap_429_records_late_variant_for_never_and_quota", func(t *testing.T) {
		for _, mode := range []string{SessionAffinityRebindOnNever, SessionAffinityRebindOnQuota} {
			t.Run(mode, func(t *testing.T) {
				manager, affinity, authA, authB := setupAntigravityLateVariantAffinityFixture(t, mode)
				ctx := context.Background()

				failBootstrap := false
				retryAfter := 155 * time.Hour
				calls := make(map[string]int)
				manager.RegisterExecutor(scriptedFailoverExecutor{
					provider: provider,
					stream: func(attemptCtx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						calls[auth.ID]++
						// Simulate a post-auth interceptor changing thinking level to high while
						// the original request body specified xhigh, and upstream returning 429 in
						// the first stream chunk.
						cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, targetHigh)
						ch := make(chan cliproxyexecutor.StreamChunk, 1)
						if auth.ID == authA.ID && failBootstrap {
							ch <- cliproxyexecutor.StreamChunk{
								Err: &lateVariantStatusError{
									code:       http.StatusTooManyRequests,
									msg:        "QUOTA_EXHAUSTED",
									retryAfter: &retryAfter,
								},
							}
						} else {
							ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
						}
						close(ch)
						return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
					},
				})

				req := cliproxyexecutor.Request{Model: routeModel, Payload: xhighThinkingBody}
				opts := cliproxyexecutor.Options{
					Headers:         http.Header{"Session-Id": []string{"ag-late-bootstrap-" + mode}},
					SourceFormat:    sdktranslator.FormatGemini,
					OriginalRequest: xhighThinkingBody,
					Stream:          true,
				}

				readStream := func(streamRes *cliproxyexecutor.StreamResult) string {
					t.Helper()
					var out []byte
					for chunk := range streamRes.Chunks {
						if chunk.Err != nil {
							t.Fatalf("unexpected chunk err: %v", chunk.Err)
						}
						out = append(out, chunk.Payload...)
					}
					return string(out)
				}

				// Turn 1: bind session to authA.
				res1, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts)
				if errStream != nil || readStream(res1) != authA.ID {
					t.Fatalf("cold ExecuteStream err=%v", errStream)
				}

				// Turn 2: first stream chunk returns 429 QUOTA_EXHAUSTED on targetHigh.
				failBootstrap = true
				calls[authA.ID] = 0
				calls[authB.ID] = 0
				res2, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts)
				if mode == SessionAffinityRebindOnNever {
					gotErr := errStream
					if gotErr == nil && res2 != nil && res2.Chunks != nil {
						for chunk := range res2.Chunks {
							if chunk.Err != nil {
								gotErr = chunk.Err
								break
							}
						}
					}
					if gotErr == nil || statusCodeFromError(gotErr) != http.StatusTooManyRequests {
						t.Fatalf("rebind-on=never bootstrap 429 = %v (status=%d), want HTTP 429", gotErr, statusCodeFromError(gotErr))
					}
					var authErr *Error
					if errors.As(gotErr, &authErr) && authErr != nil && authErr.Code == "session_auth_retry" {
						t.Fatalf("rebind-on=never bootstrap 429 returned session_auth_retry: %v", gotErr)
					}
					if calls[authB.ID] != 0 {
						t.Fatalf("rebind-on=never called authB %d times, want 0", calls[authB.ID])
					}
					return
				}

				if errStream != nil {
					t.Fatalf("rebind-on=quota bootstrap 429 failover err=%v", errStream)
				}
				if got := readStream(res2); got != authB.ID {
					t.Fatalf("rebind-on=quota bootstrap 429 payload = %q, want %q", got, authB.ID)
				}
				boundID, okBound := affinity.boundAuthForRequest([]string{provider}, routeModel, opts)
				if !okBound || boundID != authB.ID {
					t.Fatalf("rebind-on=quota bootstrap 429 boundAuth = (%q, %v), want (%q, true)", boundID, okBound, authB.ID)
				}
			})
		}
	})

	t.Run("observed_healthy_variant_502_not_overridden_by_inferred_cooled_variant", func(t *testing.T) {
		for _, mode := range []string{SessionAffinityRebindOnQuota, SessionAffinityRebindOnNever} {
			for _, executedVariant := range []string{targetLow, routeModel} {
				t.Run(mode+"/"+executedVariant, func(t *testing.T) {
					manager, affinity, authA, authB := setupAntigravityLateVariantAffinityFixture(t, mode)
					ctx := context.Background()

					fail502 := false
					manager.RegisterExecutor(scriptedFailoverExecutor{
						provider: provider,
						execute: func(attemptCtx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
							// Simulate a post-auth interceptor routing away from cooled targetHigh
							// to a healthy variant (either targetLow or base routeModel).
							if executedVariant != routeModel {
								cliproxyexecutor.SetUpstreamAttemptModel(attemptCtx, executedVariant)
							}
							if auth.ID == authA.ID && fail502 {
								return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadGateway, Message: "502 bad gateway"}
							}
							return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
						},
					})

					// Original request specifies thinkingLevel=HIGH (which is cooled on authA).
					req := cliproxyexecutor.Request{Model: routeModel, Payload: highThinkingBody}
					opts := cliproxyexecutor.Options{
						Headers:         http.Header{"Session-Id": []string{"ag-observed-override-" + mode + "-" + executedVariant}},
						SourceFormat:    sdktranslator.FormatGemini,
						OriginalRequest: highThinkingBody,
					}

					// Turn 1: bind session to authA.
					resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
					if errExec != nil || string(resp.Payload) != authA.ID {
						t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authA.ID)
					}

					// Cool targetHigh on authA; executedVariant remains healthy.
					retryAfter := 155 * time.Hour
					coolCtx := manager.newUpstreamAttemptContext(ctx)
					cliproxyexecutor.SetUpstreamAttemptModel(coolCtx, targetHigh)
					manager.MarkResult(coolCtx, Result{
						AuthID:     authA.ID,
						Provider:   provider,
						Model:      routeModel,
						RouteModel: routeModel,
						Success:    false,
						RetryAfter: &retryAfter,
						Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "QUOTA_EXHAUSTED"},
					})

					// Turn 2: executedVariant hits transient 502. Classification must follow
					// observed executedVariant (transient), NOT inferred cooled targetHigh.
					fail502 = true
					resp, errExec = manager.Execute(ctx, []string{provider}, req, opts)
					if mode == SessionAffinityRebindOnNever {
						var authErr *Error
						if !errors.As(errExec, &authErr) || authErr == nil || authErr.Code != "session_auth_retry" || authErr.HTTPStatus != http.StatusServiceUnavailable {
							t.Fatalf("rebind-on=never 502 on %s = %v, want 503 session_auth_retry", executedVariant, errExec)
						}
						return
					}

					if errExec != nil || string(resp.Payload) != authB.ID {
						t.Fatalf("rebind-on=quota transient failover = %q (err=%v), want %q", string(resp.Payload), errExec, authB.ID)
					}
					boundID, okBound := affinity.boundAuthForRequest([]string{provider}, routeModel, opts)
					if !okBound || boundID != authA.ID {
						t.Fatalf("rebind-on=quota transient 502 on %s rebound to (%q, %v), want preserved (%q, true)", executedVariant, boundID, okBound, authA.ID)
					}
				})
			}
		}
	})

	t.Run("empty_model_states_skips_variant_catalog_lookup", func(t *testing.T) {
		ctx := context.Background()
		cleanAuth := &Auth{
			ID:       "ag-clean-empty-states",
			Provider: provider,
			Status:   StatusActive,
		}
		opts := cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatGemini,
			OriginalRequest: highThinkingBody,
		}
		if isAuthBlockedByQuotaOrAuthForAntigravityRequest(ctx, cleanAuth, routeModel, routeModel, opts, nil, time.Now()) {
			t.Fatalf("expected clean auth with empty ModelStates not to be quota-blocked")
		}
	})
}
