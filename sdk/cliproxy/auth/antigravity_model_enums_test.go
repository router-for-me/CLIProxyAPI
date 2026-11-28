package auth

import (
	"strings"
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

func TestSelectAntigravityModelSkipsPrefixedExcludedThinkingVariant(t *testing.T) {
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatal(errShutdown)
	}
	t.Cleanup(func() {
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Error(errShutdown)
		}
	})

	for _, tc := range []struct {
		name     string
		id       string
		prefix   string
		excluded string
		wantID   string
		wantEnum string
	}{
		{name: "canonical exclusion", id: "select-canonical", prefix: "team", excluded: "gemini-review-low", wantID: "gemini-review-high", wantEnum: "HIGH_ENUM"},
		{name: "prefixed exact exclusion", id: "select-prefixed", prefix: "team", excluded: "team/gemini-review-low", wantID: "gemini-review-high", wantEnum: "HIGH_ENUM"},
		{name: "prefixed wildcard exclusion", id: "select-wildcard", prefix: "team", excluded: "team/gemini-review-*", wantID: "gemini-review-high", wantEnum: "HIGH_ENUM"},
		{name: "different prefix remains allowed", id: "select-different-prefix", prefix: "team", excluded: "other/gemini-review-low", wantID: "gemini-review-low", wantEnum: "LOW_ENUM"},
		{name: "normalized prefixed exclusion", id: "select-normalized-prefix", prefix: " /TEAM/ ", excluded: "team/gemini-review-low", wantID: "gemini-review-high", wantEnum: "HIGH_ENUM"},
		{name: "published prefix exclusion", id: "select-published-prefix", prefix: "team/", excluded: "team//gemini-review-low", wantID: "gemini-review-high", wantEnum: "HIGH_ENUM"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{
				ID:       tc.id,
				Provider: "antigravity",
				Prefix:   tc.prefix,
				Status:   StatusActive,
				Attributes: map[string]string{
					"excluded_models": tc.excluded,
				},
			}
			if !StoreAntigravityModelCatalog(t.Context(), auth,
				map[string]string{
					"gemini-review-high": "HIGH_ENUM",
					"gemini-review-low":  "LOW_ENUM",
				},
				map[string]map[string]int{
					"gemini-review": {
						"high": 32768,
						"low":  65536,
					},
				},
				nil,
			) {
				t.Fatal("StoreAntigravityModelCatalog() = false")
			}

			got := SelectAntigravityModel(t.Context(), auth, "gemini-review-high", "low", nil)
			if got.ID != tc.wantID || got.Enum != tc.wantEnum {
				t.Fatalf("selected model = %q (%q), want %q (%q)", got.ID, got.Enum, tc.wantID, tc.wantEnum)
			}
		})
	}
}

func TestSameAntigravityModelCatalogScopeCredentialPrefix(t *testing.T) {
	for _, scope := range []string{"local", "home"} {
		t.Run(scope, func(t *testing.T) {
			for _, tc := range []struct {
				name   string
				before string
				after  string
				want   bool
			}{
				{name: "unchanged", before: "team", after: "team", want: true},
				{name: "changed", before: "other", after: "team"},
				{name: "added", after: "team"},
				{name: "removed", before: "team"},
				{name: "case and whitespace normalized", before: "team", after: " TEAM ", want: true},
				{name: "slash-only prefix changes routes", after: " /// "},
				{name: "trailing slash changes routes", before: "team", after: "team/"},
				{name: "leading slash changes routes", before: "team", after: "/team"},
				{name: "published prefix unchanged", before: "team/", after: " TEAM/ ", want: true},
				{name: "normalized but different", before: "other", after: " /TEAM/ "},
			} {
				t.Run(tc.name, func(t *testing.T) {
					before := &Auth{
						ID:       t.Name(),
						Provider: "antigravity",
						Prefix:   tc.before,
						Status:   StatusActive,
						Attributes: map[string]string{
							"excluded_models": "team/gemini-review-low",
						},
						Metadata: map[string]any{"refresh_token": "same-grant"},
					}
					if scope == "home" {
						before.Attributes[AntigravityModelEnumHomeScopeAttribute] = strings.Repeat("a", 64)
					}
					after := before.Clone()
					after.Prefix = tc.after
					if got := SameAntigravityModelCatalogScope(before, after); got != tc.want {
						t.Fatalf("SameAntigravityModelCatalogScope(%q, %q) = %v, want %v", tc.before, tc.after, got, tc.want)
					}
				})
			}
		})
	}
}
