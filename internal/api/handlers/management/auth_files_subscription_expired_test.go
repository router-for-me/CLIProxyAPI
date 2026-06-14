package management

import (
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestExtractCodexSubscriptionMetadata_RecomputesExpired(t *testing.T) {
	past := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	future := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339)

	t.Run("stale cached false is recomputed to true once expiry passed", func(t *testing.T) {
		auth := &coreauth.Auth{
			Provider: "codex",
			Metadata: map[string]any{
				"subscription_active_until": past,
				"subscription_expired":      false, // stale cached value
			},
		}
		got := extractCodexSubscriptionMetadata(auth)
		if got == nil {
			t.Fatalf("expected result")
		}
		if v, _ := got["subscription_expired"].(bool); !v {
			t.Fatalf("subscription_expired=%v, want true (recomputed from past expiry)", got["subscription_expired"])
		}
	})

	t.Run("future expiry yields not expired", func(t *testing.T) {
		auth := &coreauth.Auth{
			Provider: "codex",
			Metadata: map[string]any{
				"subscription_active_until": future,
				"subscription_expired":      true, // stale cached value
			},
		}
		got := extractCodexSubscriptionMetadata(auth)
		if v, _ := got["subscription_expired"].(bool); v {
			t.Fatalf("subscription_expired=%v, want false (recomputed from future expiry)", got["subscription_expired"])
		}
	})
}
