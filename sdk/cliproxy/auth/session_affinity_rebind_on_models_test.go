package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSessionAffinityRebindOnModelsCanonicalizesModel(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		RebindOn:       SessionAffinityRebindOnAny,
		RebindOnModels: []string{" Claude-Fable-5-2 "},
	})
	defer selector.Stop()

	for _, model := range []string{"claude-fable-5-2", "CLAUDE-FABLE-5-2", "claude-fable-5-2(high)"} {
		if !selector.rebindNeverForModel(model) {
			t.Errorf("rebindNeverForModel(%q) = false, want true", model)
		}
	}
	if selector.rebindNeverForModel("claude-opus-5-5") {
		t.Error("rebindNeverForModel(claude-opus-5-5) = true, want false")
	}
}

func TestManagerSessionAffinityRebindOnModelsScopesNeverPolicy(t *testing.T) {
	ctx := context.Background()
	provider := "affinity-rebind-models"
	fable := provider + "-fable"
	opus := provider + "-opus"
	highID := provider + "-high"
	lowID := provider + "-low"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:       &RoundRobinSelector{},
		TTL:            time.Hour,
		RebindOn:       SessionAffinityRebindOnAny,
		RebindOnModels: []string{fable},
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
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: fable}, {ID: opus}})
		authID := auth.ID
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}

	newOpts := func(session string) cliproxyexecutor.Options {
		return cliproxyexecutor.Options{Metadata: map[string]any{
			cliproxyexecutor.DerivedSessionIDMetadataKey: session,
		}}
	}
	pick := func(model string, opts cliproxyexecutor.Options) (*Auth, error) {
		auth, _, errPick := manager.pickNext(ctx, provider, model, opts, nil)
		return auth, errPick
	}
	mark := func(authID, model string, opts cliproxyexecutor.Options, success bool, resultErr *Error) {
		t.Helper()
		manager.MarkResult(ctx, Result{
			AuthID:   authID,
			Provider: provider,
			Model:    model,
			Success:  success,
			Error:    resultErr,
			Options:  opts,
		})
	}

	// Fable is listed: a 429 on the bound credential must not fail over.
	fableOpts := newOpts("rebind-models-fable")
	got, errPick := pick(fable, fableOpts)
	if errPick != nil || got == nil || got.ID != highID {
		t.Fatalf("fable cold binding = %v (err=%v), want %s", got, errPick, highID)
	}
	mark(highID, fable, fableOpts, true, nil)
	mark(highID, fable, fableOpts, false, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit reached"})
	got, errPick = pick(fable, fableOpts)
	if errPick == nil {
		t.Fatalf("fable unavailable pick succeeded with %v, want session_auth_unavailable", got)
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr == nil || authErr.Code != "session_auth_unavailable" || authErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("fable unavailable pick err = %v, want 429 session_auth_unavailable", errPick)
	}
	if got, errPick = pick(fable, newOpts("rebind-models-fable-new")); errPick != nil || got == nil || got.ID != lowID {
		t.Fatalf("new fable session = %v (err=%v), want %s", got, errPick, lowID)
	}
	expireSessionAffinityPriorityModelCooldown(t, manager, highID, fable)
	if got, errPick = pick(fable, fableOpts); errPick != nil || got == nil || got.ID != highID {
		t.Fatalf("fable after recovery = %v (err=%v), want %s", got, errPick, highID)
	}

	// Opus is not listed: the same 429 must fail over and rebind.
	opusOpts := newOpts("rebind-models-opus")
	got, errPick = pick(opus, opusOpts)
	if errPick != nil || got == nil || got.ID != highID {
		t.Fatalf("opus cold binding = %v (err=%v), want %s", got, errPick, highID)
	}
	mark(highID, opus, opusOpts, true, nil)
	mark(highID, opus, opusOpts, false, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit reached"})
	if got, errPick = pick(opus, opusOpts); errPick != nil || got == nil || got.ID != lowID {
		t.Fatalf("opus failover = %v (err=%v), want %s", got, errPick, lowID)
	}
	mark(lowID, opus, opusOpts, true, nil)
	expireSessionAffinityPriorityModelCooldown(t, manager, highID, opus)
	if got, errPick = pick(opus, opusOpts); errPick != nil || got == nil || got.ID != lowID {
		t.Fatalf("opus after recovery = %v (err=%v), want rebinding to stay on %s", got, errPick, lowID)
	}
}

func TestManagerSessionAffinityRebindOnModelsScopesTerminalError(t *testing.T) {
	ctx := context.Background()
	provider := "affinity-rebind-models-terminal"
	fable := provider + "-fable"
	opus := provider + "-opus"
	authID := provider + "-auth"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback:       &RoundRobinSelector{},
		TTL:            time.Hour,
		RebindOn:       SessionAffinityRebindOnAny,
		RebindOnModels: []string{fable},
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})
	if _, errRegister := manager.Register(WithSkipPersist(ctx), &Auth{ID: authID, Provider: provider, Status: StatusActive}); errRegister != nil {
		t.Fatalf("Register(%s): %v", authID, errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{{ID: fable}, {ID: opus}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	bind := func(model string) cliproxyexecutor.Options {
		opts := cliproxyexecutor.Options{Metadata: map[string]any{
			cliproxyexecutor.DerivedSessionIDMetadataKey: "terminal-" + model,
		}}
		got, _, errPick := manager.pickNext(ctx, provider, model, opts, nil)
		if errPick != nil || got == nil || got.ID != authID {
			t.Fatalf("bind %s = %v (err=%v), want %s", model, got, errPick, authID)
		}
		manager.MarkResult(ctx, Result{AuthID: authID, Provider: provider, Model: model, Success: true, Options: opts})
		return opts
	}
	fableOpts := bind(fable)
	opusOpts := bind(opus)

	fableErr := &Error{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"}
	normalized := manager.normalizeSessionAffinityNeverTerminalError(fableErr, []string{provider}, fable, fableOpts)
	var authErr *Error
	if !errors.As(normalized, &authErr) || authErr == nil || authErr.Code != "session_auth_retry" || authErr.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("fable terminal error = %v, want 503 session_auth_retry", normalized)
	}

	opusErr := &Error{HTTPStatus: http.StatusBadGateway, Message: "502 overloaded"}
	normalized = manager.normalizeSessionAffinityNeverTerminalError(opusErr, []string{provider}, opus, opusOpts)
	if normalized != opusErr {
		t.Fatalf("opus terminal error = %v, want the original error unchanged", normalized)
	}
}
