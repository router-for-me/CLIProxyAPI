package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestManagerSessionAffinityPreservesBindingAcrossHigherPriorityRecovery(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		providerSuffix string
		pick           func(*Manager, context.Context, string, string, cliproxyexecutor.Options) (*Auth, error)
	}{
		{
			name:           "single provider",
			providerSuffix: "single",
			pick: func(manager *Manager, ctx context.Context, provider, model string, opts cliproxyexecutor.Options) (*Auth, error) {
				auth, _, errPick := manager.pickNext(ctx, provider, model, opts, nil)
				return auth, errPick
			},
		},
		{
			name:           "mixed provider",
			providerSuffix: "mixed",
			pick: func(manager *Manager, ctx context.Context, provider, model string, opts cliproxyexecutor.Options) (*Auth, error) {
				auth, _, _, errPick := manager.pickNextMixed(ctx, []string{provider}, model, opts, nil)
				return auth, errPick
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			provider := "affinity-priority-" + testCase.providerSuffix
			model := "affinity-priority-model"
			highID := provider + "-high"
			lowID := provider + "-low"

			manager := NewManager(nil, nil, nil)
			affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: &RoundRobinSelector{},
				TTL:      time.Hour,
			})
			defer affinity.Stop()
			manager.SetSelector(affinity)
			manager.RegisterExecutor(schedulerTestExecutor{provider: provider})

			for _, auth := range []*Auth{
				{ID: highID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "1"}},
				{ID: lowID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
			} {
				if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
					t.Fatalf("Register(%s): %v", auth.ID, errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}

			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.DerivedSessionIDMetadataKey: "stable-session",
			}}
			pick := func(pickOpts cliproxyexecutor.Options) *Auth {
				t.Helper()
				auth, errPick := testCase.pick(manager, ctx, provider, model, pickOpts)
				if errPick != nil {
					t.Fatalf("pick: %v", errPick)
				}
				if auth == nil {
					t.Fatal("pick returned nil auth")
				}
				return auth
			}

			if got := pick(opts); got.ID != highID {
				t.Fatalf("cold binding = %q, want high priority %q", got.ID, highID)
			}

			manager.MarkResult(ctx, Result{
				AuthID:   highID,
				Provider: provider,
				Model:    model,
				Success:  false,
				Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
			})
			if got := pick(opts); got.ID != lowID {
				t.Fatalf("failover binding = %q, want %q", got.ID, lowID)
			}

			expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
			if got := pick(opts); got.ID != lowID {
				t.Fatalf("binding after higher-priority recovery = %q, want sticky %q", got.ID, lowID)
			}

			newSessionOpts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.DerivedSessionIDMetadataKey: "new-session",
			}}
			if got := pick(newSessionOpts); got.ID != highID {
				t.Fatalf("cold binding for new session = %q, want high priority %q", got.ID, highID)
			}

			manager.MarkResult(ctx, Result{
				AuthID:   lowID,
				Provider: provider,
				Model:    model,
				Success:  false,
				Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
			})
			if got := pick(opts); got.ID != highID {
				t.Fatalf("binding after bound auth became unavailable = %q, want %q", got.ID, highID)
			}
		})
	}
}

func TestSessionAffinityFallbackOnlyReceivesHighestAvailablePriority(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: lastAuthSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()

	high := &Auth{ID: "a-high", Provider: "test", Status: StatusActive, Attributes: map[string]string{"priority": "1"}}
	low := &Auth{ID: "z-low", Provider: "test", Status: StatusActive, Attributes: map[string]string{"priority": "0"}}
	auths := []*Auth{high, low}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "stable-session",
	}}

	assertPick := func(label string, pickOpts cliproxyexecutor.Options, wantID string) {
		t.Helper()
		got, errPick := selector.Pick(context.Background(), "test", "model", pickOpts, auths)
		if errPick != nil {
			t.Fatalf("%s: %v", label, errPick)
		}
		if got == nil {
			t.Fatalf("%s = nil, want %q", label, wantID)
		}
		if got.ID != wantID {
			t.Fatalf("%s = %q, want %q", label, got.ID, wantID)
		}
	}

	assertPick("cold binding", opts, high.ID)
	assertPick("no-session fallback", cliproxyexecutor.Options{}, high.ID)

	high.Unavailable = true
	assertPick("fallback after bound auth became unavailable", opts, low.ID)
}

type lastAuthSelector struct{}

func (lastAuthSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}
	return auths[len(auths)-1], nil
}

func expireSessionAffinityPriorityModelCooldown(t *testing.T, manager *Manager, authID, model string) {
	t.Helper()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	auth := manager.auths[authID]
	if auth == nil {
		t.Fatalf("auth %q not found", authID)
	}
	state := auth.ModelStates[model]
	if state == nil {
		t.Fatalf("model state %q not found for auth %q", model, authID)
	}
	expired := time.Now().Add(-time.Second)
	state.NextRetryAfter = expired
	state.Quota.NextRecoverAt = expired
}

func TestManagerSessionAffinityRebindOnQuotaPreservesBindingAcrossTransientFailover(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		providerSuffix string
		pick           func(*Manager, context.Context, string, string, cliproxyexecutor.Options) (*Auth, error)
	}{
		{
			name:           "single provider",
			providerSuffix: "single",
			pick: func(manager *Manager, ctx context.Context, provider, model string, opts cliproxyexecutor.Options) (*Auth, error) {
				auth, _, errPick := manager.pickNext(ctx, provider, model, opts, nil)
				return auth, errPick
			},
		},
		{
			name:           "mixed provider",
			providerSuffix: "mixed",
			pick: func(manager *Manager, ctx context.Context, provider, model string, opts cliproxyexecutor.Options) (*Auth, error) {
				auth, _, _, errPick := manager.pickNextMixed(ctx, []string{provider}, model, opts, nil)
				return auth, errPick
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			provider := "affinity-rebind-quota-" + testCase.providerSuffix
			model := "affinity-rebind-quota-model"
			highID := provider + "-high"
			lowID := provider + "-low"

			manager := NewManager(nil, nil, nil)
			affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: &RoundRobinSelector{},
				TTL:      time.Hour,
				RebindOn: SessionAffinityRebindOnQuota,
			})
			defer affinity.Stop()
			manager.SetSelector(affinity)
			manager.RegisterExecutor(schedulerTestExecutor{provider: provider})

			for _, auth := range []*Auth{
				{ID: highID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "1"}},
				{ID: lowID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
			} {
				if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
					t.Fatalf("Register(%s): %v", auth.ID, errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}

			opts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.DerivedSessionIDMetadataKey: "stable-session",
			}}
			pick := func(pickOpts cliproxyexecutor.Options) *Auth {
				t.Helper()
				auth, errPick := testCase.pick(manager, ctx, provider, model, pickOpts)
				if errPick != nil {
					t.Fatalf("pick: %v", errPick)
				}
				if auth == nil {
					t.Fatal("pick returned nil auth")
				}
				return auth
			}

			if got := pick(opts); got.ID != highID {
				t.Fatalf("cold binding = %q, want high priority %q", got.ID, highID)
			}
			manager.MarkResult(ctx, Result{
				AuthID:   highID,
				Provider: provider,
				Model:    model,
				Success:  true,
				Options:  opts,
			})

			for _, transientErr := range []*Error{
				{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"},
				{HTTPStatus: http.StatusServiceUnavailable, Message: "503 connect error"},
				{HTTPStatus: http.StatusRequestTimeout, Message: "408 timeout"},
				{HTTPStatus: http.StatusInsufficientStorage, Message: "507 buffer limit"},
				{Code: ErrorCodeForceCooldown, Message: "stream completed without any response data"},
			} {
				manager.MarkResult(ctx, Result{
					AuthID:   highID,
					Provider: provider,
					Model:    model,
					Success:  false,
					Error:    transientErr,
					Options:  opts,
				})
				if got := pick(opts); got.ID != lowID {
					t.Fatalf("transient (%v) failover pick = %q, want %q", transientErr, got.ID, lowID)
				}
				manager.MarkResult(ctx, Result{
					AuthID:   lowID,
					Provider: provider,
					Model:    model,
					Success:  true,
					Options:  opts,
				})

				expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
				if got := pick(opts); got.ID != highID {
					t.Fatalf("binding after transient (%v) recovery = %q, want original %q", transientErr, got.ID, highID)
				}
				manager.MarkResult(ctx, Result{
					AuthID:   highID,
					Provider: provider,
					Model:    model,
					Success:  true,
					Options:  opts,
				})
			}

			// Quota failure (429) must rebind to lowID and stay bound after highID recovers.
			manager.MarkResult(ctx, Result{
				AuthID:   highID,
				Provider: provider,
				Model:    model,
				Success:  false,
				Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
				Options:  opts,
			})
			if got := pick(opts); got.ID != lowID {
				t.Fatalf("quota failover pick = %q, want %q", got.ID, lowID)
			}
			manager.MarkResult(ctx, Result{
				AuthID:   lowID,
				Provider: provider,
				Model:    model,
				Success:  true,
				Options:  opts,
			})

			expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
			if got := pick(opts); got.ID != lowID {
				t.Fatalf("binding after quota recovery = %q, want sticky %q", got.ID, lowID)
			}

			// Auth failure (403) on a new session must also rebind and stay bound.
			authFailOpts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.DerivedSessionIDMetadataKey: "auth-error-session",
			}}
			if got := pick(authFailOpts); got.ID != highID {
				t.Fatalf("cold binding for auth-error-session = %q, want %q", got.ID, highID)
			}
			manager.MarkResult(ctx, Result{
				AuthID:   highID,
				Provider: provider,
				Model:    model,
				Success:  false,
				Error:    &Error{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
				Options:  authFailOpts,
			})
			if got := pick(authFailOpts); got.ID != lowID {
				t.Fatalf("auth failover pick = %q, want %q", got.ID, lowID)
			}
			manager.MarkResult(ctx, Result{
				AuthID:   lowID,
				Provider: provider,
				Model:    model,
				Success:  true,
				Options:  authFailOpts,
			})
			expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
			if got := pick(authFailOpts); got.ID != lowID {
				t.Fatalf("binding after auth error recovery = %q, want sticky %q", got.ID, lowID)
			}
		})
	}
}

func TestManagerSessionAffinityRebindOnQuotaLCPPreservesBindingAcrossTransientFailover(t *testing.T) {
	ctx := context.Background()
	provider := "openai"
	model := "gpt-5"
	highID := "openai-high"
	lowID := "openai-low"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnQuota,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})

	for _, auth := range []*Auth{
		{ID: highID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "1"}},
		{ID: lowID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	makeTurnOpts := func(payload string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatOpenAI,
			OriginalRequest: []byte(payload),
			Metadata: map[string]any{
				cliproxyexecutor.CallerScopeMetadataKey: "caller-lcp-quota",
			},
		}
	}

	turn1 := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"}]}`)
	got1, _, errPick := manager.pickNext(ctx, provider, model, turn1, nil)
	if errPick != nil || got1 == nil || got1.ID != highID {
		t.Fatalf("turn 1 pick = %v (err=%v), want %q", got1, errPick, highID)
	}
	manager.MarkResult(ctx, Result{AuthID: highID, Provider: provider, Model: model, Success: true, Options: turn1})

	// Turn 2 hits highID, fails with transient 502, fails over to lowID, and succeeds on lowID.
	turn2 := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"}]}`)
	got2Attempt1, _, errPick := manager.pickNext(ctx, provider, model, turn2, nil)
	if errPick != nil || got2Attempt1 == nil || got2Attempt1.ID != highID {
		t.Fatalf("turn 2 attempt 1 pick = %v (err=%v), want %q", got2Attempt1, errPick, highID)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   highID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"},
		Options:  turn2,
	})

	turn2Failover := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"}]}`)
	got2Attempt2, _, errPick := manager.pickNext(ctx, provider, model, turn2Failover, map[string]struct{}{highID: {}})
	if errPick != nil || got2Attempt2 == nil || got2Attempt2.ID != lowID {
		t.Fatalf("turn 2 failover pick = %v (err=%v), want %q", got2Attempt2, errPick, lowID)
	}
	manager.MarkResult(ctx, Result{AuthID: lowID, Provider: provider, Model: model, Success: true, Options: turn2Failover})

	// Once highID recovers from transient cooldown, turn 3 must return to highID.
	expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
	turn3 := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"},{"role":"assistant","content":"turn 2 answer"},{"role":"user","content":"turn 3 follow-up"}]}`)
	got3, _, errPick := manager.pickNext(ctx, provider, model, turn3, nil)
	if errPick != nil || got3 == nil || got3.ID != highID {
		t.Fatalf("turn 3 pick after transient recovery = %v (err=%v), want %q", got3, errPick, highID)
	}
	manager.MarkResult(ctx, Result{AuthID: highID, Provider: provider, Model: model, Success: true, Options: turn3})

	// Now turn 4 fails on highID with 429 quota, fails over to lowID, and must stay on lowID after highID recovers.
	turn4 := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"},{"role":"assistant","content":"turn 2 answer"},{"role":"user","content":"turn 3 follow-up"},{"role":"assistant","content":"turn 3 answer"},{"role":"user","content":"turn 4 quota"}]}`)
	got4Attempt1, _, errPick := manager.pickNext(ctx, provider, model, turn4, nil)
	if errPick != nil || got4Attempt1 == nil || got4Attempt1.ID != highID {
		t.Fatalf("turn 4 attempt 1 pick = %v (err=%v), want %q", got4Attempt1, errPick, highID)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   highID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		Options:  turn4,
	})
	turn4Failover := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"},{"role":"assistant","content":"turn 2 answer"},{"role":"user","content":"turn 3 follow-up"},{"role":"assistant","content":"turn 3 answer"},{"role":"user","content":"turn 4 quota"}]}`)
	got4Attempt2, _, errPick := manager.pickNext(ctx, provider, model, turn4Failover, map[string]struct{}{highID: {}})
	if errPick != nil || got4Attempt2 == nil || got4Attempt2.ID != lowID {
		t.Fatalf("turn 4 quota failover pick = %v (err=%v), want %q", got4Attempt2, errPick, lowID)
	}
	manager.MarkResult(ctx, Result{AuthID: lowID, Provider: provider, Model: model, Success: true, Options: turn4Failover})

	expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
	turn5 := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"},{"role":"assistant","content":"turn 2 answer"},{"role":"user","content":"turn 3 follow-up"},{"role":"assistant","content":"turn 3 answer"},{"role":"user","content":"turn 4 quota"},{"role":"assistant","content":"turn 4 answer"},{"role":"user","content":"turn 5 after quota recovery"}]}`)
	got5, _, errPick := manager.pickNext(ctx, provider, model, turn5, nil)
	if errPick != nil || got5 == nil || got5.ID != lowID {
		t.Fatalf("turn 5 pick after quota recovery = %v (err=%v), want sticky %q", got5, errPick, lowID)
	}
}

func TestSessionAffinitySelectorRebindOnQuotaDirectPickRebindsWhenAuthRemovedOrZeroWeight(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &WeightedRoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnQuota,
	})
	defer selector.Stop()

	authA := &Auth{ID: "auth-a", Status: StatusActive, Attributes: map[string]string{"weight": "10", "priority": "1"}}
	authB := &Auth{ID: "auth-b", Status: StatusActive, Attributes: map[string]string{"weight": "5", "priority": "0"}}
	authC := &Auth{ID: "auth-c", Status: StatusActive, Attributes: map[string]string{"weight": "5", "priority": "0"}}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "direct-pick-session",
	}}

	first, errPick := selector.Pick(ctx, "claude", "claude-3", opts, []*Auth{authA, authB, authC})
	if errPick != nil || first == nil || first.ID != authA.ID {
		t.Fatalf("initial pick = %v (err=%v), want %q", first, errPick, authA.ID)
	}

	// Weight of authA becomes 0 -> filtered out by positiveWeightAuths; must rebind and stick to the new auth.
	authAZero := &Auth{ID: "auth-a", Status: StatusActive, Attributes: map[string]string{"weight": "0", "priority": "1"}}
	second, errPick := selector.Pick(ctx, "claude", "claude-3", opts, []*Auth{authAZero, authB, authC})
	if errPick != nil || second == nil || second.ID == authA.ID {
		t.Fatalf("pick after zero weight = %v (err=%v), want non-%q", second, errPick, authA.ID)
	}
	third, errPick := selector.Pick(ctx, "claude", "claude-3", opts, []*Auth{authAZero, authB, authC})
	if errPick != nil || third == nil || third.ID != second.ID {
		t.Fatalf("subsequent pick after zero-weight rebind = %v (err=%v), want sticky %q", third, errPick, second.ID)
	}
}

func TestSessionAffinitySelectorRebindOnQuotaLCPCompactionPreservesMetadataOnTransientFailover(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnQuota,
	})
	defer selector.Stop()

	high := &Auth{ID: "auth-high", Status: StatusActive, Attributes: map[string]string{"priority": "1"}}
	low := &Auth{ID: "auth-low", Status: StatusActive, Attributes: map[string]string{"priority": "0"}}
	auths := []*Auth{high, low}

	firstPayload := []byte(`{"contents":[
		{"role":"user","parts":[{"text":"step 1"}]},
		{"role":"model","parts":[{"text":"ack 1"}]},
		{"role":"user","parts":[{"text":"step 2"}]},
		{"role":"model","parts":[{"text":"ack 2"}]},
		{"role":"user","parts":[{"text":"step 3"}]}
	]}`)
	firstOpts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: firstPayload,
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-compaction-quota",
		},
	}
	firstAuth, errFirst := selector.Pick(ctx, "google", "gemini-2.5-pro", firstOpts, auths)
	if errFirst != nil || firstAuth == nil || firstAuth.ID != high.ID {
		t.Fatalf("first Pick() = %v (err=%v), want %q", firstAuth, errFirst, high.ID)
	}
	selector.OnResult(Result{
		AuthID:   firstAuth.ID,
		Provider: "google",
		Model:    "gemini-2.5-pro",
		Options:  firstOpts,
		Success:  true,
	})

	// Put high into transient cooldown (502) and send a compacted continuation.
	highTransient := &Auth{
		ID:             "auth-high",
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(time.Minute),
		LastError:      &Error{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"},
		Attributes:     map[string]string{"priority": "1"},
	}
	compactedPayload := []byte(`{"contents":[
		{"role":"user","parts":[{"text":"<summary>Steps 1 and 2 completed</summary>"}]},
		{"role":"model","parts":[{"text":"ack 2"}]},
		{"role":"user","parts":[{"text":"step 3"}]},
		{"role":"user","parts":[{"text":"step 4"}]}
	]}`)
	compactedOpts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: compactedPayload,
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-compaction-quota",
		},
	}
	compactedAuth, errCompacted := selector.Pick(ctx, "google", "gemini-2.5-pro", compactedOpts, []*Auth{highTransient, low})
	if errCompacted != nil || compactedAuth == nil || compactedAuth.ID != low.ID {
		t.Fatalf("compacted transient failover Pick() = %v (err=%v), want %q", compactedAuth, errCompacted, low.ID)
	}
	if isCompaction, ok := compactedOpts.Metadata[cliproxyexecutor.IsCompactionMetadataKey].(bool); !ok || !isCompaction {
		t.Fatalf("expected is_compaction=true on transient failover, got %v", compactedOpts.Metadata[cliproxyexecutor.IsCompactionMetadataKey])
	}
	if nodeKind, ok := compactedOpts.Metadata[cliproxyexecutor.NodeKindMetadataKey].(string); !ok || nodeKind != "compaction" {
		t.Fatalf("expected node_kind=compaction on transient failover, got %v", compactedOpts.Metadata[cliproxyexecutor.NodeKindMetadataKey])
	}
	selector.OnResult(Result{
		AuthID:   compactedAuth.ID,
		Provider: "google",
		Model:    "gemini-2.5-pro",
		Options:  compactedOpts,
		Success:  true,
	})

	// Next turn after high recovers must still route back to high.
	nextPayload := []byte(`{"contents":[
		{"role":"user","parts":[{"text":"<summary>Steps 1 and 2 completed</summary>"}]},
		{"role":"model","parts":[{"text":"ack 2"}]},
		{"role":"user","parts":[{"text":"step 3"}]},
		{"role":"user","parts":[{"text":"step 4"}]},
		{"role":"model","parts":[{"text":"ack 4"}]}
	]}`)
	nextOpts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatGemini,
		OriginalRequest: nextPayload,
		Metadata: map[string]any{
			cliproxyexecutor.CallerScopeMetadataKey: "caller-compaction-quota",
		},
	}
	nextAuth, errNext := selector.Pick(ctx, "google", "gemini-2.5-pro", nextOpts, auths)
	if errNext != nil || nextAuth == nil || nextAuth.ID != high.ID {
		t.Fatalf("next Pick() after compaction transient recovery = %v (err=%v), want %q", nextAuth, errNext, high.ID)
	}
}

type scriptedFailoverExecutor struct {
	provider string
	execute  func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	stream   func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

func (e scriptedFailoverExecutor) Identifier() string { return e.provider }

func (e scriptedFailoverExecutor) PrepareRequest(_ *http.Request, _ *Auth) error { return nil }

func (e scriptedFailoverExecutor) HttpRequest(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e scriptedFailoverExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.execute != nil {
		return e.execute(ctx, auth, req, opts)
	}
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e scriptedFailoverExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.stream != nil {
		return e.stream(ctx, auth, req, opts)
	}
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e scriptedFailoverExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e scriptedFailoverExecutor) CountTokens(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func TestManagerExecuteAndStreamRebindOnQuotaEndToEnd(t *testing.T) {
	for _, disableCooling := range []bool{false, true} {
		name := "cooling_enabled"
		if disableCooling {
			name = "cooling_disabled"
		}
		t.Run(name, func(t *testing.T) {
			prevCooling := quotaCooldownDisabled.Load()
			SetQuotaCooldownDisabled(disableCooling)
			t.Cleanup(func() { SetQuotaCooldownDisabled(prevCooling) })

			ctx := context.Background()
			provider := "exec-rebind-quota-" + name
			model := "exec-rebind-quota-model"
			highID := provider + "-high"
			midID := provider + "-mid"
			lowID := provider + "-low"

			var failAuthErr map[string]error
			manager := NewManager(nil, nil, nil)
			affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: &RoundRobinSelector{},
				TTL:      time.Hour,
				RebindOn: SessionAffinityRebindOnQuota,
			})
			defer affinity.Stop()
			manager.SetSelector(affinity)
			manager.RegisterExecutor(scriptedFailoverExecutor{
				provider: provider,
				execute: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					if errFail := failAuthErr[auth.ID]; errFail != nil {
						return cliproxyexecutor.Response{}, errFail
					}
					return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
				},
				stream: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if errFail := failAuthErr[auth.ID]; errFail != nil {
						return nil, errFail
					}
					ch := make(chan cliproxyexecutor.StreamChunk, 1)
					ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
					close(ch)
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				},
			})

			for _, auth := range []*Auth{
				{ID: highID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "2"}},
				{ID: midID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "1"}},
				{ID: lowID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
			} {
				if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
					t.Fatalf("Register(%s): %v", auth.ID, errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}

			req := cliproxyexecutor.Request{Model: model}
			opts := cliproxyexecutor.Options{
				Headers: http.Header{"Session-Id": []string{"exec-session-1"}},
			}

			// 1. Cold Execute binds to highID.
			resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
			if errExec != nil || string(resp.Payload) != highID {
				t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, highID)
			}

			// 2. Cascade transient errors on highID (502) and midID (503) during Execute -> fails over to lowID.
			failAuthErr = map[string]error{
				highID: &Error{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"},
				midID:  &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "503 connect error"},
			}
			resp, errExec = manager.Execute(ctx, []string{provider}, req, opts)
			if errExec != nil || string(resp.Payload) != lowID {
				t.Fatalf("transient cascade Execute = %q (err=%v), want %q", string(resp.Payload), errExec, lowID)
			}

			// 3. Clear transient errors and expire cooldowns -> next ExecuteStream returns to highID.
			failAuthErr = nil
			if !disableCooling {
				expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
				expireSessionAffinityPriorityModelCooldown(t, manager, midID, model)
			}
			streamRes, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream after transient recovery error = %v", errStream)
			}
			chunk := <-streamRes.Chunks
			if string(chunk.Payload) != highID {
				t.Fatalf("ExecuteStream after transient recovery = %q, want %q", string(chunk.Payload), highID)
			}

			// 4. Transient error on highID during ExecuteStream -> fails over to midID, then returns to highID.
			failAuthErr = map[string]error{
				highID: &Error{Code: ErrorCodeForceCooldown, Message: "stream completed without any response data"},
			}
			streamRes, errStream = manager.ExecuteStream(ctx, []string{provider}, req, opts)
			if errStream != nil {
				t.Fatalf("ExecuteStream transient failover error = %v", errStream)
			}
			chunk = <-streamRes.Chunks
			if string(chunk.Payload) != midID {
				t.Fatalf("ExecuteStream transient failover = %q, want %q", string(chunk.Payload), midID)
			}

			// ErrorCodeForceCooldown always schedules a transient cooldown even when disableCooling is true.
			failAuthErr = nil
			expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
			resp, errExec = manager.Execute(ctx, []string{provider}, req, opts)
			if errExec != nil || string(resp.Payload) != highID {
				t.Fatalf("Execute after stream transient recovery = %q (err=%v), want %q", string(resp.Payload), errExec, highID)
			}

			// 5. Quota error (429) on highID -> fails over and rebinds to midID permanently.
			failAuthErr = map[string]error{
				highID: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
			}
			resp, errExec = manager.Execute(ctx, []string{provider}, req, opts)
			if errExec != nil || string(resp.Payload) != midID {
				t.Fatalf("Execute quota failover = %q (err=%v), want %q", string(resp.Payload), errExec, midID)
			}

			failAuthErr = nil
			if !disableCooling {
				expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
			}
			resp, errExec = manager.Execute(ctx, []string{provider}, req, opts)
			if errExec != nil || string(resp.Payload) != midID {
				t.Fatalf("Execute after quota recovery = %q (err=%v), want sticky %q", string(resp.Payload), errExec, midID)
			}
		})
	}
}
