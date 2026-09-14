package cliproxy

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// seedAnthropicHint stores a hint for authID and registers cleanup. The hint
// store is package-global, so every test here uses a distinct auth ID.
func seedAnthropicHint(t *testing.T, authID string) {
	t.Helper()
	coreauth.DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() {
		coreauth.DeleteAnthropicRateLimitHint(authID)
		GlobalModelRegistry().UnregisterClient(authID)
	})
	coreauth.SetAnthropicRateLimitHint(authID, coreauth.AnthropicRateLimitHint{
		Known:      true,
		Status:     "allowed",
		ObservedAt: time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC),
	})
	if _, ok := coreauth.GetAnthropicRateLimitHint(authID); !ok {
		t.Fatalf("seed failed: no hint stored for %q", authID)
	}
}

// TestApplyCoreAuthAddOrUpdate_ScrubsHintOnSuccessfulUpdate covers the
// success-gated scrub. An in-place upsert of an existing auth ID can be a
// credential rotation (same ID, new underlying account), so the hint must not
// survive it and report the previous credential's quota.
func TestApplyCoreAuthAddOrUpdate_ScrubsHintOnSuccessfulUpdate(t *testing.T) {
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	const authID = "hint-scrub-on-update"
	ctx := context.Background()

	// Register first: the scrub is on the update branch, not the register one.
	service.applyCoreAuthAddOrUpdate(ctx, &coreauth.Auth{
		ID: authID, Provider: "claude", Status: coreauth.StatusActive,
	})
	if _, ok := service.coreManager.GetByID(authID); !ok {
		t.Fatalf("expected auth %q to be registered", authID)
	}

	seedAnthropicHint(t, authID)

	// Second upsert takes the update branch.
	service.applyCoreAuthAddOrUpdate(ctx, &coreauth.Auth{
		ID: authID, Provider: "claude", Status: coreauth.StatusActive,
	})

	if _, ok := coreauth.GetAnthropicRateLimitHint(authID); ok {
		t.Error("hint survived a successful in-place update; a rotated credential's quota would still be served")
	}
}

// TestApplyCoreAuthAddOrUpdate_KeepsHintWhenRegisteringNewAuth guards the
// other side of the branch: registering an auth that is not already present
// is not a rotation, so an unrelated seeded hint must not be disturbed.
func TestApplyCoreAuthAddOrUpdate_KeepsHintWhenRegisteringNewAuth(t *testing.T) {
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	const authID = "hint-kept-on-register"
	seedAnthropicHint(t, authID)

	service.applyCoreAuthAddOrUpdate(context.Background(), &coreauth.Auth{
		ID: authID, Provider: "claude", Status: coreauth.StatusActive,
	})

	if _, ok := coreauth.GetAnthropicRateLimitHint(authID); !ok {
		t.Error("hint was scrubbed on the register branch; only a successful in-place update should scrub")
	}
}

// TestApplyCoreAuthRemoval_ScrubsHint covers the removal hook. The scrub is
// deliberately provider-agnostic — the store is keyed by auth ID and the call
// is a no-op for IDs without a hint.
func TestApplyCoreAuthRemoval_ScrubsHint(t *testing.T) {
	for _, provider := range []string{"claude", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
			authID := "hint-scrub-on-removal-" + provider
			ctx := context.Background()

			service.applyCoreAuthAddOrUpdate(ctx, &coreauth.Auth{
				ID: authID, Provider: provider, Status: coreauth.StatusActive,
			})
			seedAnthropicHint(t, authID)

			service.applyCoreAuthRemoval(ctx, authID)

			if _, ok := coreauth.GetAnthropicRateLimitHint(authID); ok {
				t.Error("hint survived auth removal; a recreated auth with the same ID would inherit it")
			}
		})
	}
}

// TestApplyCoreAuthRemoval_ScrubIsNoOpForUnknownID pins that the scrub
// tolerates IDs it has never seen, which is the common case for non-Claude
// providers flowing through the same removal path.
func TestApplyCoreAuthRemoval_ScrubIsNoOpForUnknownID(t *testing.T) {
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	service.applyCoreAuthRemoval(context.Background(), "hint-never-seen")
}
