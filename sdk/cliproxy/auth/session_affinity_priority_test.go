package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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

func TestManagerSessionAffinityRebindOnNeverRefusesFailoverAndPreservesBinding(t *testing.T) {
	for _, testCase := range []struct {
		name           string
		providerSuffix string
		pick           func(*Manager, context.Context, string, string, cliproxyexecutor.Options, map[string]struct{}) (*Auth, error)
	}{
		{
			name:           "single provider",
			providerSuffix: "single",
			pick: func(manager *Manager, ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, error) {
				auth, _, errPick := manager.pickNext(ctx, provider, model, opts, tried)
				return auth, errPick
			},
		},
		{
			name:           "mixed provider",
			providerSuffix: "mixed",
			pick: func(manager *Manager, ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, error) {
				auth, _, _, errPick := manager.pickNextMixed(ctx, []string{provider}, model, opts, tried)
				return auth, errPick
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			provider := "affinity-rebind-never-" + testCase.providerSuffix
			model := "affinity-rebind-never-model"
			highID := provider + "-high"
			lowID := provider + "-low"

			manager := NewManager(nil, nil, nil)
			affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
				Fallback: &RoundRobinSelector{},
				TTL:      time.Hour,
				RebindOn: SessionAffinityRebindOnNever,
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
				cliproxyexecutor.DerivedSessionIDMetadataKey: "never-session",
			}}
			otherOpts := cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.DerivedSessionIDMetadataKey: "never-other-session",
			}}

			got, errPick := testCase.pick(manager, ctx, provider, model, opts, nil)
			if errPick != nil || got == nil || got.ID != highID {
				t.Fatalf("cold binding = %v (err=%v), want %q", got, errPick, highID)
			}
			manager.MarkResult(ctx, Result{
				AuthID:   highID,
				Provider: provider,
				Model:    model,
				Success:  true,
				Options:  opts,
			})

			// Transient failure on highID must refuse failover with 503 session_auth_retry and keep binding.
			manager.MarkResult(ctx, Result{
				AuthID:   highID,
				Provider: provider,
				Model:    model,
				Success:  false,
				Error:    &Error{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"},
				Options:  opts,
			})
			if got, errPick = testCase.pick(manager, ctx, provider, model, opts, nil); errPick == nil {
				t.Fatalf("transient unavailable pick succeeded with %v, want 503 session_auth_retry error", got)
			} else {
				var authErr *Error
				if !errors.As(errPick, &authErr) || authErr == nil || authErr.Code != "session_auth_retry" || authErr.HTTPStatus != http.StatusServiceUnavailable {
					t.Fatalf("transient unavailable pick err = %v, want 503 session_auth_retry", errPick)
				}
			}

			// A new unbound session while highID is cooling down must still pick lowID.
			gotOther, errOther := testCase.pick(manager, ctx, provider, model, otherOpts, nil)
			if errOther != nil || gotOther == nil || gotOther.ID != lowID {
				t.Fatalf("new session pick while highID unavailable = %v (err=%v), want %q", gotOther, errOther, lowID)
			}

			expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
			got, errPick = testCase.pick(manager, ctx, provider, model, opts, nil)
			if errPick != nil || got == nil || got.ID != highID {
				t.Fatalf("pick after transient recovery = %v (err=%v), want %q", got, errPick, highID)
			}
			manager.MarkResult(ctx, Result{
				AuthID:   highID,
				Provider: provider,
				Model:    model,
				Success:  true,
				Options:  opts,
			})

			// Quota/auth failures (429, 403) on highID must refuse failover with 429 session_auth_unavailable and keep binding.
			for _, blockErr := range []*Error{
				{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit reached"},
				{HTTPStatus: http.StatusForbidden, Message: "forbidden"},
			} {
				manager.MarkResult(ctx, Result{
					AuthID:   highID,
					Provider: provider,
					Model:    model,
					Success:  false,
					Error:    blockErr,
					Options:  opts,
				})
				if got, errPick = testCase.pick(manager, ctx, provider, model, opts, nil); errPick == nil {
					t.Fatalf("blocked (%v) pick succeeded with %v, want 429 session_auth_unavailable error", blockErr, got)
				} else {
					var authErr *Error
					if !errors.As(errPick, &authErr) || authErr == nil || authErr.Code != "session_auth_unavailable" || authErr.HTTPStatus != http.StatusTooManyRequests {
						t.Fatalf("blocked (%v) pick err = %v, want 429 session_auth_unavailable", blockErr, errPick)
					}
				}
				expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
				got, errPick = testCase.pick(manager, ctx, provider, model, opts, nil)
				if errPick != nil || got == nil || got.ID != highID {
					t.Fatalf("pick after blocked (%v) recovery = %v (err=%v), want %q", blockErr, got, errPick, highID)
				}
				manager.MarkResult(ctx, Result{
					AuthID:   highID,
					Provider: provider,
					Model:    model,
					Success:  true,
					Options:  opts,
				})
			}
		})
	}
}

func TestManagerSessionAffinityRebindOnNeverLCPAndFallbackKeyPreserveBinding(t *testing.T) {
	ctx := context.Background()
	provider := "openai"
	model := "gpt-5-never"
	highID := "openai-never-high"
	lowID := "openai-never-low"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnNever,
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
				cliproxyexecutor.CallerScopeMetadataKey: "caller-lcp-never",
			},
		}
	}

	// 1. LCP cold binding on highID.
	turn1 := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"}]}`)
	got1, _, errPick := manager.pickNext(ctx, provider, model, turn1, nil)
	if errPick != nil || got1 == nil || got1.ID != highID {
		t.Fatalf("turn 1 pick = %v (err=%v), want %q", got1, errPick, highID)
	}
	manager.MarkResult(ctx, Result{AuthID: highID, Provider: provider, Model: model, Success: true, Options: turn1})

	// 2. LCP turn 2 fails on highID with 429 quota -> failover attempt must refuse with 429 session_auth_unavailable and preserve binding.
	turn2 := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"}]}`)
	got2, _, errPick := manager.pickNext(ctx, provider, model, turn2, nil)
	if errPick != nil || got2 == nil || got2.ID != highID {
		t.Fatalf("turn 2 pick = %v (err=%v), want %q", got2, errPick, highID)
	}
	manager.MarkResult(ctx, Result{
		AuthID:   highID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota"},
		Options:  turn2,
	})

	turn2Retry := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"}]}`)
	if gotFailover, _, errFailover := manager.pickNext(ctx, provider, model, turn2Retry, nil); errFailover == nil {
		t.Fatalf("LCP quota unavailable pick succeeded with %v, want 429 session_auth_unavailable", gotFailover)
	} else {
		var authErr *Error
		if !errors.As(errFailover, &authErr) || authErr == nil || authErr.Code != "session_auth_unavailable" || authErr.HTTPStatus != http.StatusTooManyRequests {
			t.Fatalf("LCP quota unavailable pick err = %v, want 429 session_auth_unavailable", errFailover)
		}
	}

	expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
	turn2Recovered := makeTurnOpts(`{"messages":[{"role":"user","content":"turn 1 hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"turn 2 question"}]}`)
	got2Rec, _, errPick := manager.pickNext(ctx, provider, model, turn2Recovered, nil)
	if errPick != nil || got2Rec == nil || got2Rec.ID != highID {
		t.Fatalf("LCP turn 2 after recovery = %v (err=%v), want %q", got2Rec, errPick, highID)
	}
	manager.MarkResult(ctx, Result{AuthID: highID, Provider: provider, Model: model, Success: true, Options: turn2Recovered})

	// 3. Fallback-key path (non-subagent, non-fork: conversation -> prompt_cache_key + conversation)
	// refuses failover when bound auth is transiently unavailable.
	fallbackInitOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"conversation":"conv-never-1"}`),
	}
	gotFB1, _, errPick := manager.pickNext(ctx, provider, model, fallbackInitOpts, nil)
	if errPick != nil || gotFB1 == nil || gotFB1.ID != highID {
		t.Fatalf("fallback init pick = %v (err=%v), want %q", gotFB1, errPick, highID)
	}
	manager.MarkResult(ctx, Result{AuthID: highID, Provider: provider, Model: model, Success: true, Options: fallbackInitOpts})

	manager.MarkResult(ctx, Result{
		AuthID:   highID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"},
		Options:  fallbackInitOpts,
	})
	fallbackLookupOpts := cliproxyexecutor.Options{
		OriginalRequest: []byte(`{"prompt_cache_key":"pck-never-1","conversation":"conv-never-1"}`),
	}
	if gotFB2, _, errFB2 := manager.pickNext(ctx, provider, model, fallbackLookupOpts, nil); errFB2 == nil {
		t.Fatalf("fallback key unavailable pick succeeded with %v, want 503 session_auth_retry", gotFB2)
	} else {
		var authErr *Error
		if !errors.As(errFB2, &authErr) || authErr == nil || authErr.Code != "session_auth_retry" || authErr.HTTPStatus != http.StatusServiceUnavailable {
			t.Fatalf("fallback key unavailable pick err = %v, want 503 session_auth_retry", errFB2)
		}
	}
	expireSessionAffinityPriorityModelCooldown(t, manager, highID, model)
	gotFBRec, _, errPick := manager.pickNext(ctx, provider, model, fallbackLookupOpts, nil)
	if errPick != nil || gotFBRec == nil || gotFBRec.ID != highID {
		t.Fatalf("fallback key pick after recovery = %v (err=%v), want %q", gotFBRec, errPick, highID)
	}
}

func TestManagerExecuteAndStreamRebindOnNeverEndToEnd(t *testing.T) {
	ctx := context.Background()
	provider := "exec-rebind-never"
	model := "exec-rebind-never-model"
	authAID := provider + "-a"
	authBID := provider + "-b"

	var failAuthErr map[string]func(call int) error
	calls := make(map[string]int)
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(2, 30*time.Second, 0)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnNever,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)
	manager.RegisterExecutor(scriptedFailoverExecutor{
		provider: provider,
		execute: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls[auth.ID]++
			if fn := failAuthErr[auth.ID]; fn != nil {
				if errFail := fn(calls[auth.ID]); errFail != nil {
					return cliproxyexecutor.Response{}, errFail
				}
			}
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
		stream: func(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			calls[auth.ID]++
			if fn := failAuthErr[auth.ID]; fn != nil {
				if errFail := fn(calls[auth.ID]); errFail != nil {
					return nil, errFail
				}
			}
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		},
	})

	for _, auth := range []*Auth{
		{ID: authAID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "1"}},
		{ID: authBID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	req := cliproxyexecutor.Request{Model: model}
	sessionAOpts := cliproxyexecutor.Options{
		Headers: http.Header{"Session-Id": []string{"never-e2e-session-a"}},
	}
	sessionBOpts := cliproxyexecutor.Options{
		Headers: http.Header{"Session-Id": []string{"never-e2e-session-b"}},
	}

	// 1. Bind session A to authAID.
	resp, errExec := manager.Execute(ctx, []string{provider}, req, sessionAOpts)
	if errExec != nil || string(resp.Payload) != authAID {
		t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authAID)
	}
	if calls[authBID] != 0 {
		t.Fatalf("authB calls = %d, want 0", calls[authBID])
	}

	// 2. Make authA return 429 usage limit reached -> returns 429 to client without calling authB or running extra retry rounds.
	calls[authAID] = 0
	failAuthErr = map[string]func(int) error{
		authAID: func(_ int) error {
			return &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached: quota exhausted"}
		},
	}
	_, errExec = manager.Execute(ctx, []string{provider}, req, sessionAOpts)
	if errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests || !strings.Contains(errExec.Error(), "usage_limit_reached") {
		t.Fatalf("Execute on 429 = %v, want 429 usage_limit_reached", errExec)
	}
	if calls[authAID] != 1 {
		t.Fatalf("authA calls on 429 = %d, want 1 (no extra retry rounds on quota error)", calls[authAID])
	}
	if calls[authBID] != 0 {
		t.Fatalf("authB calls after authA 429 = %d, want 0", calls[authBID])
	}

	// 3. Next request on session A while authA is in 429 cooldown returns 429 session_auth_unavailable without calling authA or authB.
	_, errExec = manager.Execute(ctx, []string{provider}, req, sessionAOpts)
	if errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
		t.Fatalf("subsequent Execute on cooled session A = %v, want 429", errExec)
	}
	if calls[authAID] != 1 || calls[authBID] != 0 {
		t.Fatalf("calls after cooled session A = (A=%d, B=%d), want (1, 0)", calls[authAID], calls[authBID])
	}

	// 4. A request with a new session ID (session B) goes to authB.
	respB, errExecB := manager.Execute(ctx, []string{provider}, req, sessionBOpts)
	if errExecB != nil || string(respB.Payload) != authBID {
		t.Fatalf("new session B Execute = %q (err=%v), want %q", string(respB.Payload), errExecB, authBID)
	}
	if calls[authBID] != 1 {
		t.Fatalf("authB calls = %d, want 1", calls[authBID])
	}

	// 5. Expire authA cooldown -> session A still bound to authA.
	failAuthErr = nil
	expireSessionAffinityPriorityModelCooldown(t, manager, authAID, model)
	resp, errExec = manager.Execute(ctx, []string{provider}, req, sessionAOpts)
	if errExec != nil || string(resp.Payload) != authAID {
		t.Fatalf("Execute after authA quota recovery = %q (err=%v), want %q", string(resp.Payload), errExec, authAID)
	}

	// 6. Transient 502 error on authA (with transient cooldown disabled) is retried on authA through retry rounds and never sent to authB.
	prevTransientSec := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(-1)
	defer SetTransientErrorCooldownSeconds(int(prevTransientSec))

	calls[authAID] = 0
	calls[authBID] = 0
	failAuthErr = map[string]func(int) error{
		authAID: func(call int) error {
			if call == 1 {
				return &Error{HTTPStatus: http.StatusBadGateway, Message: "502 bad gateway"}
			}
			return nil
		},
	}
	resp, errExec = manager.Execute(ctx, []string{provider}, req, sessionAOpts)
	if errExec != nil || string(resp.Payload) != authAID {
		t.Fatalf("Execute 502 retry on authA = %q (err=%v), want %q", string(resp.Payload), errExec, authAID)
	}
	if calls[authAID] != 2 || calls[authBID] != 0 {
		t.Fatalf("calls after 502 retry = (A=%d, B=%d), want (2, 0)", calls[authAID], calls[authBID])
	}

	// 7. When authA fails with 502 across all retry rounds (or cooldown exceeds maxWait), Execute returns 503 session_auth_retry and never calls authB.
	SetTransientErrorCooldownSeconds(-1)
	manager.SetRetryConfig(2, 30*time.Second, 0)
	calls[authAID] = 0
	calls[authBID] = 0
	failAuthErr = map[string]func(int) error{
		authAID: func(_ int) error {
			return &Error{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"}
		},
	}
	_, errExec = manager.Execute(ctx, []string{provider}, req, sessionAOpts)
	if errExec == nil || statusCodeFromError(errExec) != http.StatusServiceUnavailable {
		t.Fatalf("Execute exhausted 502 retry rounds = %v, want 503 session_auth_retry", errExec)
	}
	var exhaustedRetryErr *Error
	if !errors.As(errExec, &exhaustedRetryErr) || exhaustedRetryErr == nil || exhaustedRetryErr.Code != "session_auth_retry" {
		t.Fatalf("Execute exhausted 502 error = %v, want session_auth_retry", errExec)
	}
	if calls[authAID] != 3 || calls[authBID] != 0 {
		t.Fatalf("calls after exhausted 502 retry rounds = (A=%d, B=%d), want (3, 0)", calls[authAID], calls[authBID])
	}

	// 8. When authA is in transient 502 cooldown exceeding maxWait, both initial and subsequent requests on session A return 503 session_auth_retry and never call authB.
	SetTransientErrorCooldownSeconds(0)
	manager.SetRetryConfig(2, 0, 0)
	calls[authAID] = 0
	calls[authBID] = 0
	_, errExec = manager.Execute(ctx, []string{provider}, req, sessionAOpts)
	if errExec == nil || statusCodeFromError(errExec) != http.StatusServiceUnavailable {
		t.Fatalf("Execute initial 502 with cooldown = %v, want 503 session_auth_retry", errExec)
	}
	if calls[authAID] != 1 || calls[authBID] != 0 {
		t.Fatalf("calls after initial 502 = (A=%d, B=%d), want (1, 0)", calls[authAID], calls[authBID])
	}

	_, errExec = manager.Execute(ctx, []string{provider}, req, sessionAOpts)
	if errExec == nil || statusCodeFromError(errExec) != http.StatusServiceUnavailable {
		t.Fatalf("subsequent Execute while authA in 502 cooldown = %v, want 503", errExec)
	}
	var retryAuthErr *Error
	if !errors.As(errExec, &retryAuthErr) || retryAuthErr == nil || retryAuthErr.Code != "session_auth_retry" {
		t.Fatalf("subsequent Execute error = %v, want session_auth_retry", errExec)
	}
	if calls[authAID] != 1 || calls[authBID] != 0 {
		t.Fatalf("calls after cooled 502 session A = (A=%d, B=%d), want (1, 0)", calls[authAID], calls[authBID])
	}

	_, errStream := manager.ExecuteStream(ctx, []string{provider}, req, sessionAOpts)
	if errStream == nil || statusCodeFromError(errStream) != http.StatusServiceUnavailable {
		t.Fatalf("subsequent ExecuteStream while authA in 502 cooldown = %v, want 503", errStream)
	}
	if calls[authAID] != 1 || calls[authBID] != 0 {
		t.Fatalf("calls after cooled 502 ExecuteStream = (A=%d, B=%d), want (1, 0)", calls[authAID], calls[authBID])
	}
}

func TestManagerSessionAffinityRebindOnNeverBlocksAntigravityCreditsCrossAuthFallback(t *testing.T) {
	ctx := context.Background()
	provider := "antigravity"
	model := "claude-sonnet-4-6"
	authAID := "antigravity-never-a"
	authBID := "antigravity-never-b"

	calls := make(map[string]int)
	var failAuthA bool
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{AntigravityCredits: true},
	})
	manager.SetRetryConfig(0, 0, 0)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnNever,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)
	manager.RegisterExecutor(scriptedFailoverExecutor{
		provider: provider,
		execute: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			calls[auth.ID]++
			if auth.ID == authAID && failAuthA {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
			}
			return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
		},
		stream: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			calls[auth.ID]++
			if auth.ID == authAID && failAuthA {
				return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"}
			}
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		},
	})

	for _, auth := range []*Auth{
		{ID: authAID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "1"}},
		{ID: authBID, Provider: provider, Status: StatusActive, Attributes: map[string]string{"priority": "0"}},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}
	SetAntigravityCreditsHint(authAID, AntigravityCreditsHint{Known: true, Available: false, UpdatedAt: time.Now()})
	SetAntigravityCreditsHint(authBID, AntigravityCreditsHint{Known: true, Available: true, UpdatedAt: time.Now()})
	t.Cleanup(func() {
		SetAntigravityCreditsHint(authAID, AntigravityCreditsHint{})
		SetAntigravityCreditsHint(authBID, AntigravityCreditsHint{})
	})

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"Session-Id": []string{"antigravity-never-session"}},
	}

	// 1. Cold Execute binds session to authA.
	resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
	if errExec != nil || string(resp.Payload) != authAID {
		t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authAID)
	}

	// 2. authA returns 429 and has no credits; authB has available credits, but rebind-on: never must block switching to authB.
	failAuthA = true
	calls[authAID] = 0
	calls[authBID] = 0
	if _, errExec = manager.Execute(ctx, []string{provider}, req, opts); errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
		t.Fatalf("Execute on 429 = %v, want 429", errExec)
	}
	if calls[authBID] != 0 {
		t.Fatalf("authB calls after Execute 429 = %d, want 0", calls[authBID])
	}

	if _, errStream := manager.ExecuteStream(ctx, []string{provider}, req, opts); errStream == nil || statusCodeFromError(errStream) != http.StatusTooManyRequests {
		t.Fatalf("ExecuteStream on cooled authA = %v, want 429", errStream)
	}
	if calls[authBID] != 0 {
		t.Fatalf("authB calls after ExecuteStream 429 = %d, want 0", calls[authBID])
	}

	// 3. Explicit PinnedAuthMetadataKey pointing to authB on the same session must also be rejected and never call authB via credits fallback.
	pinnedBOpts := cliproxyexecutor.Options{
		Headers: http.Header{"Session-Id": []string{"antigravity-never-session"}},
		Metadata: map[string]any{
			cliproxyexecutor.PinnedAuthMetadataKey: authBID,
		},
	}
	if _, errExec = manager.Execute(ctx, []string{provider}, req, pinnedBOpts); errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
		t.Fatalf("Execute with conflicting PinnedAuthMetadataKey = %v, want 429", errExec)
	}
	if calls[authBID] != 0 {
		t.Fatalf("authB calls after conflicting pinned Execute = %d, want 0", calls[authBID])
	}
	if _, errStream := manager.ExecuteStream(ctx, []string{provider}, req, pinnedBOpts); errStream == nil || statusCodeFromError(errStream) != http.StatusTooManyRequests {
		t.Fatalf("ExecuteStream with conflicting PinnedAuthMetadataKey = %v, want 429", errStream)
	}
	if calls[authBID] != 0 {
		t.Fatalf("authB calls after conflicting pinned ExecuteStream = %d, want 0", calls[authBID])
	}
}

func TestManagerSessionAffinityRebindOnNeverSingleAuthDisabledOrModelRemovedReturns429(t *testing.T) {
	ctx := context.Background()
	provider := "single-never-empty-cand"
	model := "single-never-model"
	authID := provider + "-only"

	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
		RebindOn: SessionAffinityRebindOnNever,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)
	manager.RegisterExecutor(scriptedFailoverExecutor{provider: provider})

	auth := &Auth{ID: authID, Provider: provider, Status: StatusActive}
	if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
		t.Fatalf("Register(%s): %v", auth.ID, errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	req := cliproxyexecutor.Request{Model: model}
	opts := cliproxyexecutor.Options{
		Headers: http.Header{"Session-Id": []string{"single-never-session"}},
	}

	// 1. Bind session to authID.
	resp, errExec := manager.Execute(ctx, []string{provider}, req, opts)
	if errExec != nil || string(resp.Payload) != authID {
		t.Fatalf("cold Execute = %q (err=%v), want %q", string(resp.Payload), errExec, authID)
	}

	// 2. Disable the only credential -> candidate list is empty, bound session must return 429 session_auth_unavailable (not 503).
	disabled := auth.Clone()
	disabled.Disabled = true
	disabled.Status = StatusDisabled
	if _, errUpdate := manager.Update(WithSkipPersist(ctx), disabled); errUpdate != nil {
		t.Fatalf("Update(disabled): %v", errUpdate)
	}
	_, errExec = manager.Execute(ctx, []string{provider}, req, opts)
	if errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
		t.Fatalf("Execute when only bound auth is disabled = %v, want 429 session_auth_unavailable", errExec)
	}
	var authErr *Error
	if !errors.As(errExec, &authErr) || authErr == nil || authErr.Code != "session_auth_unavailable" {
		t.Fatalf("Execute disabled bound auth error = %v, want session_auth_unavailable", errExec)
	}

	// An unbound session when candidates is empty must still receive auth_not_found.
	unboundOpts := cliproxyexecutor.Options{
		Headers: http.Header{"Session-Id": []string{"single-never-unbound-session"}},
	}
	if _, errUnbound := manager.Execute(ctx, []string{provider}, req, unboundOpts); !isAuthNotFoundError(errUnbound) {
		t.Fatalf("Execute unbound session with empty candidates = %v, want auth_not_found", errUnbound)
	}

	// 3. Re-enable auth, but remove model support from registry -> bound session must also return 429 session_auth_unavailable.
	enabled := auth.Clone()
	enabled.Disabled = false
	enabled.Status = StatusActive
	if _, errUpdate := manager.Update(WithSkipPersist(ctx), enabled); errUpdate != nil {
		t.Fatalf("Update(enabled): %v", errUpdate)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: "other-model"}})
	_, errExec = manager.Execute(ctx, []string{provider}, req, opts)
	if errExec == nil || statusCodeFromError(errExec) != http.StatusTooManyRequests {
		t.Fatalf("Execute when bound auth loses model support = %v, want 429 session_auth_unavailable", errExec)
	}
	if !errors.As(errExec, &authErr) || authErr == nil || authErr.Code != "session_auth_unavailable" {
		t.Fatalf("Execute model-removed bound auth error = %v, want session_auth_unavailable", errExec)
	}
}
