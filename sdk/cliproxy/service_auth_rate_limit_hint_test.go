package cliproxy

import (
	"context"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// These cover the hint through the Service entry point a config-file credential
// swap actually takes — the watcher raises Modify and
// Service.applyCoreAuthAddOrUpdate upserts through coreManager.Update. The
// Manager-level tests drive Update directly with hand-built structs, so without
// these nothing exercises the production path.
//
// Invalidation lives on the Manager now, so what is asserted here is the
// behaviour that path must produce, not the presence of a Service-level hook.

func seedHint(t *testing.T, authID, email string) {
	t.Helper()
	coreauth.DeleteAnthropicRateLimitHint(authID)
	t.Cleanup(func() {
		coreauth.DeleteAnthropicRateLimitHint(authID)
		GlobalModelRegistry().UnregisterClient(authID)
	})
	fp := coreauth.AnthropicAccountFingerprint(claudeAuth(authID, email))
	coreauth.SetAnthropicRateLimitHint(authID, coreauth.AnthropicRateLimitHint{
		Known:              true,
		Status:             "allowed",
		ObservedAt:         time.Unix(1777500000, 0).UTC(),
		AccountFingerprint: fp,
	})
}

func claudeAuth(authID, email string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{"email": email},
	}
}

// A token refresh rewrites the auth file with the same account; the quota state
// must survive it. Scrubbing on update was the original defect here.
func TestServiceUpsert_TokenRefreshKeepsHint(t *testing.T) {
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	const authID = "svc-refresh@example.com"
	ctx := context.Background()

	service.applyCoreAuthAddOrUpdate(ctx, claudeAuth(authID, "same@example.com"))
	seedHint(t, authID, "same@example.com")

	// Same account, new token — the routine refresh shape.
	refreshed := claudeAuth(authID, "same@example.com")
	refreshed.Metadata["access_token"] = "rotated-token"
	service.applyCoreAuthAddOrUpdate(ctx, refreshed)

	// Read back what the manager actually stored, not a hand-built Auth —
	// otherwise the assertion holds even if the upsert did nothing.
	stored, ok := service.coreManager.GetByID(authID)
	if !ok || stored == nil {
		t.Fatal("auth missing from the manager after update")
	}
	if got := stored.Metadata["access_token"]; got != "rotated-token" {
		t.Fatalf("update did not reach the manager: access_token = %v", got)
	}
	if _, ok := coreauth.AnthropicRateLimitHintFor(stored); !ok {
		t.Error("hint was lost across a same-account update; an ordinary token refresh must not drop quota state")
	}
}

// The same entry point with a different account behind the ID must not serve
// the previous account's quota.
func TestServiceUpsert_RotationToNewAccountIsRejectedOnRead(t *testing.T) {
	service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
	const authID = "svc-rotation@example.com"
	ctx := context.Background()

	service.applyCoreAuthAddOrUpdate(ctx, claudeAuth(authID, "old@example.com"))
	seedHint(t, authID, "old@example.com")

	service.applyCoreAuthAddOrUpdate(ctx, claudeAuth(authID, "new@example.com"))

	// Read back the manager's Auth so the rejection is proven against the
	// credential the upsert actually installed.
	stored, ok := service.coreManager.GetByID(authID)
	if !ok || stored == nil {
		t.Fatal("auth missing from the manager after update")
	}
	if got := stored.Metadata["email"]; got != "new@example.com" {
		t.Fatalf("rotation did not reach the manager: email = %v", got)
	}
	if _, ok := coreauth.AnthropicRateLimitHintFor(stored); ok {
		t.Error("the previous account's quota is served under the rotated credential")
	}
}
