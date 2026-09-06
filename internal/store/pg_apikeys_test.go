package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// pgTestDSN returns the connection string to a real Postgres instance for
// integration tests. When unset, the PG-backed tests are skipped so unit tests
// remain hermetic.
func pgTestDSN() string {
	return strings.TrimSpace(os.Getenv("PGSTORE_TEST_DSN"))
}

func skipIfNoPostgres(t *testing.T) {
	t.Helper()
	if pgTestDSN() == "" {
		t.Skip("PGSTORE_TEST_DSN not set; skipping Postgres integration test")
	}
}

// newTestPostgresStore opens a PostgresStore against PGSTORE_TEST_DSN and
// cleans the managed tables before returning control to the caller.
func newTestPostgresStore(t *testing.T, schema string) *PostgresStore {
	t.Helper()
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := NewPostgresStore(ctx, PostgresStoreConfig{
		DSN:    pgTestDSN(),
		Schema: schema,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := store.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	// Clean tables before each run.
	for _, table := range []string{
		store.cfg.UsageWindowsTable,
		store.cfg.UsageEventsTable,
		store.cfg.UsageErrorsTable,
		store.cfg.UsageStatDayTable,
		store.cfg.PoliciesTable,
		store.cfg.ModelsTable,
		store.cfg.ModelPricingTable,
		store.cfg.APIKeysTable,
		store.cfg.InternalUsersTable,
		store.cfg.ModelGroupsTable,
		store.cfg.AlertsTable,
		store.cfg.AlertSettingsTable,
		store.cfg.ProxyPoolsTable,
	} {
		if _, err := store.DB().ExecContext(ctx, "DELETE FROM "+store.fullTableName(table)); err != nil {
			// Best-effort: dependencies may fail; ignore.
			_ = err
		}
	}
	return store
}

func TestHashSecretStable(t *testing.T) {
	want := HashSecret("sk-secret123456")
	got := HashSecret("sk-secret123456")
	if want != got {
		t.Fatalf("HashSecret not deterministic: %q vs %q", want, got)
	}
}

func TestHashSecretUnique(t *testing.T) {
	if HashSecret("sk-secret123456") == HashSecret("sk-secret123457") {
		t.Fatalf("HashSecret collided for different inputs")
	}
}

func TestPrefixOfHandlesMarkers(t *testing.T) {
	cases := map[string]string{
		"sk-abcdef0123456789": "abcdef01",
		"abcdef0123456789":    "abcdef01",
		"short":               "short",
		"":                    "",
	}
	for in, want := range cases {
		if got := prefixOf(in); got != want {
			t.Errorf("prefixOf(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestGenerateSecretShape(t *testing.T) {
	s, err := GenerateSecret()
	if err != nil {
		t.Fatalf("GenerateSecret: %v", err)
	}
	if !strings.HasPrefix(s, SecretPrefix) {
		t.Fatalf("secret missing %q prefix: %q", SecretPrefix, s)
	}
	if len(s) < len(SecretPrefix)+16 {
		t.Fatalf("secret too short: %q", s)
	}
}

func TestValidateSecret(t *testing.T) {
	if err := validateSecret("short"); err == nil {
		t.Fatal("expected error for short secret")
	}
	if err := validateSecret("sk-abcdef0123456789"); err != nil {
		t.Fatalf("expected no error for sufficiently long secret: %v", err)
	}
}

func TestNormalizeStringSlice(t *testing.T) {
	got := normalizeStringSlice([]string{"a", "", "b", "", "c"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("normalizeStringSlice len = %d; want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("normalizeStringSlice[%d] = %q; want %q", i, got[i], want[i])
		}
	}
	if len(normalizeStringSlice(nil)) != 0 {
		t.Fatal("normalizeStringSlice(nil) must be empty (not nil) for JSON marshalling")
	}
}

func TestNormalizeModelRoutesStrategy(t *testing.T) {
	t.Run("preserves strategy and allowlisted priorities", func(t *testing.T) {
		got := normalizeModelRoutes([]ModelRoute{{
			Model:      "gpt-4o",
			Providers:  []string{"opencode", "cometapi"},
			Strategy:   "Priority",
			Priorities: []ProviderPriority{{Provider: "opencode", Priority: 10}, {Provider: "cometapi", Priority: 1}},
		}})
		if len(got) != 1 {
			t.Fatalf("want 1 route; got %d", len(got))
		}
		if got[0].Strategy != "priority" {
			t.Errorf("strategy = %q; want %q", got[0].Strategy, "priority")
		}
		if len(got[0].Priorities) != 2 {
			t.Fatalf("want 2 priorities; got %d", len(got[0].Priorities))
		}
	})
	t.Run("drops priorities referencing non-allowlisted providers", func(t *testing.T) {
		got := normalizeModelRoutes([]ModelRoute{{
			Model:      "gpt-4o",
			Providers:  []string{"opencode"},
			Strategy:   "failover",
			Priorities: []ProviderPriority{{Provider: "opencode", Priority: 5}, {Provider: "ghost", Priority: 9}, {Provider: "", Priority: 1}},
		}})
		if len(got) != 1 || got[0].Strategy != "failover" {
			t.Fatalf("unexpected route: %+v", got)
		}
		if len(got[0].Priorities) != 1 || got[0].Priorities[0].Provider != "opencode" {
			t.Fatalf("want only opencode priority; got %+v", got[0].Priorities)
		}
	})
	t.Run("empty strategy and priorities omitted", func(t *testing.T) {
		got := normalizeModelRoutes([]ModelRoute{{
			Model:     "gpt-4o",
			Providers: []string{"opencode"},
		}})
		if len(got) != 1 {
			t.Fatalf("want 1 route; got %d", len(got))
		}
		if got[0].Strategy != "" || len(got[0].Priorities) != 0 {
			t.Errorf("want empty strategy/priorities; got %+v", got[0])
		}
	})
	t.Run("dedups duplicate providers", func(t *testing.T) {
		got := normalizeModelRoutes([]ModelRoute{{
			Model:     "gpt-4o",
			Providers: []string{"opencode", "OpenCode", "cometapi"},
		}})
		if len(got[0].Providers) != 2 {
			t.Errorf("want 2 deduped providers; got %v", got[0].Providers)
		}
	})
	t.Run("keeps cap-only routes without providers", func(t *testing.T) {
		rpm := 60
		got := normalizeModelRoutes([]ModelRoute{{
			Model:    "gpt-4o",
			RPMLimit: &rpm,
		}})
		if len(got) != 1 {
			t.Fatalf("want 1 cap-only route preserved; got %d", len(got))
		}
		if got[0].RPMLimit == nil || *got[0].RPMLimit != 60 {
			t.Errorf("rpm_limit lost: %+v", got[0])
		}
	})
	t.Run("preserves rpm and budget alongside providers", func(t *testing.T) {
		rpm, budget := 120, 42.5
		got := normalizeModelRoutes([]ModelRoute{{
			Model:        "gpt-4o",
			Providers:    []string{"openai"},
			RPMLimit:     &rpm,
			MaxBudgetUSD: &budget,
		}})
		if len(got) != 1 || got[0].RPMLimit == nil || *got[0].RPMLimit != 120 {
			t.Fatalf("rpm_limit lost: %+v", got)
		}
		if got[0].MaxBudgetUSD == nil || *got[0].MaxBudgetUSD != 42.5 {
			t.Errorf("max_budget_usd lost: %+v", got[0])
		}
	})
}

func TestAPIKeyCreateLookupAndGetLifecycle(t *testing.T) {
	store := newTestPostgresStore(t, "policy_test")
	ctx := cancelableTestCtx(t)
	apiKeys := NewAPIKeyStore(store)

	// Create a key with policy + metadata.
	rpm := 60
	hourly := 1200
	hourlyBudget := 5.0
	weeklyBudget := 50.0
	monthlyBudget := 150.0
	expires := time.Now().Add(24 * time.Hour).UTC()
	policy := Policy{
		RPMLimit:         &rpm,
		HourlyRateLimit:  &hourly,
		BudgetHourlyUSD:  &hourlyBudget,
		BudgetWeeklyUSD:  &weeklyBudget,
		BudgetMonthlyUSD: &monthlyBudget,
		AllowedModels:    []string{"gpt-4o", "claude-3-5-sonnet"},
		BlockedModels:    []string{"claude-opus-4"},
	}
	key, secret, err := apiKeys.Create(ctx, "test-key", "alias-1", "", &expires, map[string]any{"owner": "ops"}, &policy)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(secret, SecretPrefix) {
		t.Fatalf("returned secret invalid: %q", secret)
	}
	if key.Status != APIKeyStatusActive {
		t.Fatalf("status = %q; want %q", key.Status, APIKeyStatusActive)
	}
	if key.KeyPrefix != prefixOf(secret) {
		t.Fatalf("KeyPrefix mismatch: %q vs %q", key.KeyPrefix, prefixOf(secret))
	}

	// Lookup by hash should return same key + policy.
	hashed := HashSecret(secret)
	gotKey, gotPolicy, err := apiKeys.LookupByHash(ctx, hashed)
	if err != nil {
		t.Fatalf("LookupByHash: %v", err)
	}
	if gotKey.ID != key.ID {
		t.Errorf("Lookup ID mismatch: %q vs %q", gotKey.ID, key.ID)
	}
	if gotPolicy == nil {
		t.Fatal("expected policy, got nil")
	}
	if gotPolicy.RPMLimit == nil || *gotPolicy.RPMLimit != rpm {
		t.Errorf("RPM mismatch: got %v; want %d", gotPolicy.RPMLimit, rpm)
	}
	if gotPolicy.BudgetMonthlyUSD == nil || *gotPolicy.BudgetMonthlyUSD != monthlyBudget {
		t.Errorf("Monthly budget mismatch: got %v; want %v", gotPolicy.BudgetMonthlyUSD, monthlyBudget)
	}
	if len(gotPolicy.AllowedModels) != 2 {
		t.Errorf("AllowedModels len = %d; want 2", len(gotPolicy.AllowedModels))
	}

	// Lookup by ID.
	if _, _, err := apiKeys.LookupByID(ctx, key.ID); err != nil {
		t.Errorf("LookupByID: %v", err)
	}

	// List should include the new key.
	keys, err := apiKeys.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) == 0 {
		t.Fatal("List returned no keys")
	}

	// Lifecycle: disable, rename, update metadata, update expiry.
	if err := apiKeys.UpdateStatus(ctx, key.ID, APIKeyStatusDisabled); err != nil {
		t.Fatalf("UpdateStatus: %v", err)
	}
	if err := apiKeys.Rename(ctx, key.ID, "renamed-key"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if err := apiKeys.UpdateMetadata(ctx, key.ID, map[string]any{"env": "test"}); err != nil {
		t.Fatalf("UpdateMetadata: %v", err)
	}
	if err := apiKeys.UpdateExpiry(ctx, key.ID, nil); err != nil {
		t.Fatalf("UpdateExpiry: %v", err)
	}

	// Touch last used should not error.
	apiKeys.TouchLastUsed(ctx, key.ID)

	// Regenerate should produce a new secret and invalidate hash-based lookup
	// of the old secret while preserving the key ID. Passing an empty secret
	// triggers the auto-generate path (the historical behavior).
	newSecret, err := apiKeys.Regenerate(ctx, key.ID, "")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if newSecret == secret {
		t.Fatal("Regenerate returned the same secret")
	}
	if _, _, err := apiKeys.LookupByHash(ctx, hashed); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("LookupByHash with old hash should fail; got %v", err)
	}
	if _, _, err := apiKeys.LookupByHash(ctx, HashSecret(newSecret)); err != nil {
		t.Fatalf("LookupByHash with new secret should succeed; got %v", err)
	}

	// Regenerate with a custom secret should use it verbatim (after passing
	// validation), rotate the hash/prefix accordingly, and reject too-short
	// values via ErrInvalidSecret.
	const customSecret = "sk-custom-rotate-key-0123456789"
	customNew, err := apiKeys.Regenerate(ctx, key.ID, customSecret)
	if err != nil {
		t.Fatalf("Regenerate custom: %v", err)
	}
	if customNew != customSecret {
		t.Fatalf("Regenerate custom returned %q; want %q", customNew, customSecret)
	}
	if _, _, err := apiKeys.LookupByHash(ctx, HashSecret(customSecret)); err != nil {
		t.Fatalf("LookupByHash with custom secret should succeed; got %v", err)
	}
	if _, err := apiKeys.Regenerate(ctx, key.ID, "short"); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Regenerate with short secret should return ErrInvalidSecret; got %v", err)
	}

	// Delete the key.
	if err := apiKeys.Delete(ctx, key.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := apiKeys.LookupByID(ctx, key.ID); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("LookupByID after delete should fail; got %v", err)
	}
}

func TestAPIKeyUpdatePolicyReplaces(t *testing.T) {
	store := newTestPostgresStore(t, "policy_test2")
	ctx := cancelableTestCtx(t)
	apiKeys := NewAPIKeyStore(store)

	key, _, err := apiKeys.Create(ctx, "pkey", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Initially no policy.
	_, p, err := apiKeys.LookupByID(ctx, key.ID)
	if err != nil {
		t.Fatalf("LookupByID (init): %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil policy initially; got %+v", p)
	}

	rpm := 30
	updated := Policy{RPMLimit: &rpm, AllowedModels: []string{"gpt-4o-mini"}}
	if err := apiKeys.UpdatePolicy(ctx, key.ID, updated); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	_, p2, err := apiKeys.LookupByID(ctx, key.ID)
	if err != nil {
		t.Fatalf("LookupByID (after update): %v", err)
	}
	if p2 == nil || p2.RPMLimit == nil || *p2.RPMLimit != 30 {
		t.Fatalf("policy not updated: %+v", p2)
	}
	if len(p2.AllowedModels) != 1 || p2.AllowedModels[0] != "gpt-4o-mini" {
		t.Fatalf("AllowedModels mismatch: %v", p2.AllowedModels)
	}
	if p2.BudgetMonthlyUSD != nil {
		t.Fatalf("BudgetMonthlyUSD should be nil; got %v", *p2.BudgetMonthlyUSD)
	}
}

func TestAPIKeyLookupByAlias(t *testing.T) {
	store := newTestPostgresStore(t, "alias_test")
	ctx := cancelableTestCtx(t)
	apiKeys := NewAPIKeyStore(store)

	// Give alpha a policy so we can verify the policy decodes through
	// LookupByAlias too (matching LookupByHash semantics).
	alphaRPM := 77
	alphaPolicy := &Policy{RPMLimit: &alphaRPM, AllowedModels: []string{"gpt-4o"}}
	alphaKey, _, err := apiKeys.Create(ctx, "a", "alpha", "", nil, nil, alphaPolicy)
	if err != nil {
		t.Fatalf("create alpha: %v", err)
	}
	if _, _, err := apiKeys.Create(ctx, "b", "Beta", "", nil, nil, nil); err != nil {
		t.Fatalf("create beta: %v", err)
	}
	if _, _, err := apiKeys.Create(ctx, "c1", "shared", "", nil, nil, nil); err != nil {
		t.Fatalf("create shared 1: %v", err)
	}
	if _, _, err := apiKeys.Create(ctx, "c2", "shared", "", nil, nil, nil); err != nil {
		t.Fatalf("create shared 2: %v", err)
	}

	key, policy, err := apiKeys.LookupByAlias(ctx, "ALPHA")
	if err != nil {
		t.Fatalf("LookupByAlias(ALPHA): %v", err)
	}
	if key.KeyAlias != "alpha" {
		t.Errorf("resolved key alias = %q; want %q", key.KeyAlias, "alpha")
	}
	if key.ID != alphaKey.ID {
		t.Errorf("resolved key ID = %q; want %q", key.ID, alphaKey.ID)
	}
	if policy == nil || policy.RPMLimit == nil || *policy.RPMLimit != alphaRPM {
		t.Errorf("resolved policy = %+v; want RPMLimit %d", policy, alphaRPM)
	}
	if policy == nil || len(policy.AllowedModels) != 1 || policy.AllowedModels[0] != "gpt-4o" {
		t.Errorf("resolved AllowedModels = %v; want [gpt-4o]", policy.AllowedModels)
	}

	if _, _, err := apiKeys.LookupByAlias(ctx, "shared"); !errors.Is(err, ErrAmbiguousAlias) {
		t.Fatalf("LookupByAlias(shared) = %v; want ErrAmbiguousAlias", err)
	}
	if _, _, err := apiKeys.LookupByAlias(ctx, "ghost"); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("LookupByAlias(ghost) = %v; want ErrAPIKeyNotFound", err)
	}
	if _, _, err := apiKeys.LookupByAlias(ctx, ""); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("LookupByAlias(\"\") = %v; want ErrAPIKeyNotFound", err)
	}
}

func cancelableTestCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
