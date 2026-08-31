package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestSessionAffinityModelSuffixKeepsSingleBinding pins audit finding B2: the
// session-affinity cache key must canonicalize the model component. A session
// that alternates between "model" and "model(high)" (the same underlying model
// with a thinking suffix) is one logical session and must keep one binding —
// before the fix the raw model name produced two cache keys and the session was
// split across two credentials.
func TestSessionAffinityModelSuffixKeepsSingleBinding(t *testing.T) {
	ctx := context.Background()
	provider := "affinity-suffix-provider"
	baseModel := "affinity-suffix-model"

	manager := NewManager(nil, nil, nil)
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer affinity.Stop()
	manager.SetSelector(affinity)
	manager.RegisterExecutor(schedulerTestExecutor{provider: provider})

	for _, auth := range []*Auth{
		{ID: provider + "-a", Provider: provider, Status: StatusActive},
		{ID: provider + "-b", Provider: provider, Status: StatusActive},
	} {
		if _, errRegister := manager.Register(WithSkipPersist(ctx), auth); errRegister != nil {
			t.Fatalf("Register(%s): %v", auth.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: baseModel}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	}

	sessionOpts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.DerivedSessionIDMetadataKey: "suffix-session",
	}}
	pick := func(model string) *Auth {
		t.Helper()
		auth, _, errPick := manager.pickNext(ctx, provider, model, sessionOpts, nil)
		if errPick != nil {
			t.Fatalf("pick(%s): %v", model, errPick)
		}
		if auth == nil {
			t.Fatalf("pick(%s) returned nil auth", model)
		}
		return auth
	}

	first := pick(baseModel)
	// The same session requesting the model with a thinking suffix must reuse
	// the established binding rather than creating a second one.
	suffixed := pick(baseModel + "(high)")
	if first.ID != suffixed.ID {
		t.Fatalf("session split across credentials: base-model pick = %q, suffixed pick = %q; want the same binding", first.ID, suffixed.ID)
	}
	// And back to the unsuffixed name: still the same binding.
	again := pick(baseModel)
	if first.ID != again.ID {
		t.Fatalf("session split across credentials: base-model pick = %q, repeat = %q; want the same binding", first.ID, again.ID)
	}
}
