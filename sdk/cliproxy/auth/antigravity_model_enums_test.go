package auth

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
)

func TestAntigravityModelEnumSnapshotMissingWithoutActiveKV(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	t.Cleanup(func() {
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Error(errShutdown)
		}
	})

	newAuth := func(id, registration string) *Auth {
		return &Auth{
			ID:                          id,
			Provider:                    "antigravity",
			Attributes:                  make(map[string]string),
			Metadata:                    map[string]any{"refresh_token": "refresh-token"},
			antigravityEnumRegistration: registration,
		}
	}

	t.Run("missing", func(t *testing.T) {
		missing, errMissing := AntigravityModelEnumSnapshotMissing(t.Context(), newAuth("snapshot-missing-memory-mode", "registration-memory-mode"))
		if errMissing != nil {
			t.Fatalf("AntigravityModelEnumSnapshotMissing() error = %v", errMissing)
		}
		if !missing {
			t.Fatal("AntigravityModelEnumSnapshotMissing() = false, want true for an empty standalone snapshot cache")
		}
	})

	t.Run("matching", func(t *testing.T) {
		auth := newAuth("snapshot-present-memory-mode", "registration-memory-mode")
		if !StoreAntigravityModelCatalog(t.Context(), auth, map[string]string{"gemini": "MODEL_GEMINI"}, nil, nil) {
			t.Fatal("StoreAntigravityModelCatalog() = false")
		}
		missing, errMissing := AntigravityModelEnumSnapshotMissing(t.Context(), auth)
		if errMissing != nil {
			t.Fatalf("AntigravityModelEnumSnapshotMissing() error = %v", errMissing)
		}
		if missing {
			t.Fatal("AntigravityModelEnumSnapshotMissing() = true, want false for a matching standalone snapshot")
		}
	})

	t.Run("registration mismatch", func(t *testing.T) {
		stored := newAuth("snapshot-mismatch-memory-mode", "old-registration")
		if !StoreAntigravityModelCatalog(t.Context(), stored, map[string]string{"gemini": "MODEL_GEMINI"}, nil, nil) {
			t.Fatal("StoreAntigravityModelCatalog() = false")
		}
		current := stored.Clone()
		current.antigravityEnumRegistration = "new-registration"
		missing, errMissing := AntigravityModelEnumSnapshotMissing(t.Context(), current)
		if errMissing != nil {
			t.Fatalf("AntigravityModelEnumSnapshotMissing() error = %v", errMissing)
		}
		if !missing {
			t.Fatal("AntigravityModelEnumSnapshotMissing() = false, want true for a stale standalone snapshot")
		}
	})
}
