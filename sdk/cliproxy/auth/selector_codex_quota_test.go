package auth

import (
	"testing"
	"time"
)

func TestIsAuthBlockedForModel_CodexQuotaMetadataDoesNotBlockSelection(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	auth := &Auth{
		ID:       "codex-1",
		Provider: "codex",
		Metadata: map[string]any{
			"codex_quota": map[string]any{
				"rate_limit": map[string]any{
					"primary_window": map[string]any{
						"used_percent": 99,
						"reset_at":     now.Add(2 * time.Hour).Format(time.RFC3339),
					},
				},
			},
		},
	}

	blocked, reason, _ := isAuthBlockedForModel(auth, "gpt-5-codex", now)
	if blocked {
		t.Fatalf("expected codex quota metadata not to block selection, got reason %v", reason)
	}
}

func TestIsAuthBlockedForModel_CodexQuotaMetadataWithExpiredResetDoesNotBlockSelection(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	auth := &Auth{
		ID:       "codex-1",
		Provider: "codex",
		Metadata: map[string]any{
			"codex_quota": map[string]any{
				"rate_limit": map[string]any{
					"primary_window": map[string]any{
						"used_percent": 95,
						"reset_at":     now.Add(-1 * time.Minute).Format(time.RFC3339),
					},
				},
			},
		},
	}

	blocked, _, _ := isAuthBlockedForModel(auth, "gpt-5-codex", now)
	if blocked {
		t.Fatal("expected codex auth to become available after reset_at has passed")
	}
}

func TestIsAuthBlockedForModel_CodexQuotaMetadataDoesNotAffectOtherProviders(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	auth := &Auth{
		ID:       "claude-1",
		Provider: "claude",
		Metadata: map[string]any{
			"codex_quota": map[string]any{
				"rate_limit": map[string]any{
					"primary_window": map[string]any{
						"used_percent": 100,
						"reset_at":     now.Add(2 * time.Hour).Format(time.RFC3339),
					},
				},
			},
		},
	}

	blocked, _, _ := isAuthBlockedForModel(auth, "claude-sonnet-4", now)
	if blocked {
		t.Fatal("expected non-codex providers to ignore codex quota metadata")
	}
}
