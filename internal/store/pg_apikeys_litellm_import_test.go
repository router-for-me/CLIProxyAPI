package store

import (
	"context"
	"testing"
	"time"
)

// newTestKeyImportStore opens a PostgresStore against PGSTORE_TEST_DSN, cleans
// both the runtime api_keys/api_key_policies and the Manage-LiteLLM key tables,
// and returns the runtime APIKeyStore + LiteLLMKeyStore backing the key
// migration.
func newTestKeyImportStore(t *testing.T, schema string) (*APIKeyStore, *LiteLLMKeyStore) {
	t.Helper()
	skipIfNoPostgres(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pg, err := NewPostgresStore(ctx, PostgresStoreConfig{DSN: pgTestDSN(), Schema: schema})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	for _, table := range []string{
		pg.cfg.APIKeysTable,
		pg.cfg.PoliciesTable,
		pg.cfg.InternalUsersTable,
		pg.cfg.LiteLLMKeyPoliciesTable,
		pg.cfg.LiteLLMKeysTable,
		pg.cfg.LiteLLMUsersTable,
		pg.cfg.UsageEventsTable,
		pg.cfg.LiteLLMSyncSettingsTable,
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+pg.fullTableName(table)); err != nil {
			_ = err
		}
	}
	return NewAPIKeyStore(pg), NewLiteLLMKeyStore(pg)
}

func TestImportLiteLLMKeysCreatesRuntimeKeys(t *testing.T) {
	runtimeKeys, _ := newTestKeyImportStore(t, "key_import_create")
	ctx := context.Background()

	src := []LiteLLMKey{
		{ID: "llk-1", Name: "prod-key", KeyAlias: "alias-1", KeyHash: "hash-abc", KeyPrefix: "sk-prod", Status: "active", UserID: "user-1", Spend: 5.5, Tags: []string{"prod"}},
		{ID: "llk-2", Name: "revoked-key", KeyHash: "hash-def", KeyPrefix: "sk-rev", Status: "revoked"},
	}
	policies := map[string]*LiteLLMPolicy{
		"llk-1": {RPMLimit: intPtr(120), BudgetUSD: float64Ptr(50.0), BudgetDuration: BudgetDuration7d, AllowedModels: []string{"gpt-4o"}},
	}
	imported, skipped, err := runtimeKeys.ImportLiteLLMKeys(ctx, src, policies)
	if err != nil {
		t.Fatalf("ImportLiteLLMKeys: %v", err)
	}
	if imported != 2 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d; want 2/0", imported, skipped)
	}

	k1, pol1, err := runtimeKeys.LookupByHash(ctx, "hash-abc")
	if err != nil {
		t.Fatalf("LookupByHash hash-abc: %v", err)
	}
	if k1.ID != "llk-1" || k1.KeyPrefix != "sk-prod" || k1.Status != "active" || k1.UserID != "user-1" {
		t.Fatalf("k1 wrong: %+v", k1)
	}
	// Tags folded into metadata.
	tags, _ := k1.Metadata["litellm_tags"].([]any)
	if len(tags) != 1 || tags[0] != "prod" {
		t.Fatalf("tags not folded into metadata: %+v", k1.Metadata)
	}
	// Policy mapped: rpm + weekly budget from 7d duration.
	if pol1 == nil || pol1.RPMLimit == nil || *pol1.RPMLimit != 120 {
		t.Fatalf("pol1 rpm wrong: %+v", pol1)
	}
	if pol1.BudgetWeeklyUSD == nil || *pol1.BudgetWeeklyUSD != 50.0 {
		t.Fatalf("pol1 weekly budget wrong: %+v", pol1.BudgetWeeklyUSD)
	}
	if len(pol1.AllowedModels) != 1 || pol1.AllowedModels[0] != "gpt-4o" {
		t.Fatalf("pol1 allowed models wrong: %+v", pol1.AllowedModels)
	}

	k2, _, err := runtimeKeys.LookupByHash(ctx, "hash-def")
	if err != nil {
		t.Fatalf("LookupByHash hash-def: %v", err)
	}
	if k2.Status != "revoked" {
		t.Fatalf("k2 status = %q; want revoked", k2.Status)
	}
}

func TestImportLiteLLMKeysResyncIdempotent(t *testing.T) {
	runtimeKeys, _ := newTestKeyImportStore(t, "key_import_resync")
	ctx := context.Background()

	src := []LiteLLMKey{
		{ID: "llk-1", Name: "prod-key", KeyHash: "hash-abc", KeyPrefix: "sk-prod", Status: "active", UserID: "user-1"},
	}
	if imported, skipped, err := runtimeKeys.ImportLiteLLMKeys(ctx, src, nil); err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("first import: imported=%d skipped=%d err=%v", imported, skipped, err)
	}

	// Re-sync with a changed name + status; the row is updated in place by id.
	src2 := []LiteLLMKey{
		{ID: "llk-1", Name: "prod-key-renamed", KeyHash: "hash-abc", KeyPrefix: "sk-prod", Status: "disabled", UserID: "user-1"},
	}
	imported, skipped, err := runtimeKeys.ImportLiteLLMKeys(ctx, src2, nil)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("re-import imported=%d skipped=%d; want 1/0", imported, skipped)
	}
	k, _, err := runtimeKeys.LookupByHash(ctx, "hash-abc")
	if err != nil {
		t.Fatalf("LookupByHash: %v", err)
	}
	if k.Name != "prod-key-renamed" || k.Status != "disabled" {
		t.Fatalf("key not updated in place: %+v", k)
	}
	if k.ID != "llk-1" {
		t.Fatalf("key id = %q; want llk-1 preserved", k.ID)
	}
}

func TestLiteLLMPolicyToRuntimePolicyBudgetMapping(t *testing.T) {
	// 7d → weekly (exact window counterpart).
	p2 := liteLLMPolicyToRuntimePolicy(LiteLLMPolicy{BudgetUSD: float64Ptr(20.0), BudgetDuration: BudgetDuration7d})
	if p2.BudgetWeeklyUSD == nil || *p2.BudgetWeeklyUSD != 20.0 {
		t.Fatalf("7d should map to weekly: %+v", p2.BudgetWeeklyUSD)
	}
	// 30d → monthly (exact window counterpart).
	p3 := liteLLMPolicyToRuntimePolicy(LiteLLMPolicy{BudgetUSD: float64Ptr(30.0), BudgetDuration: "30d"})
	if p3.BudgetMonthlyUSD == nil || *p3.BudgetMonthlyUSD != 30.0 {
		t.Fatalf("30d should map to monthly: %+v", p3.BudgetMonthlyUSD)
	}
	// Budgets without a precisely-mappable window (1d/24h/12h/8h or no
	// duration) are NOT enforced at runtime — no window cap is set.
	for _, duration := range []string{BudgetDuration1d, "24h", "12h", "8h", ""} {
		p := liteLLMPolicyToRuntimePolicy(LiteLLMPolicy{BudgetUSD: float64Ptr(10.0), BudgetDuration: duration})
		if p.BudgetHourlyUSD != nil || p.BudgetWeeklyUSD != nil || p.BudgetMonthlyUSD != nil {
			t.Fatalf("duration %q should not set any runtime window: %+v", duration, p)
		}
	}
	// Zero budget → no window caps.
	p4 := liteLLMPolicyToRuntimePolicy(LiteLLMPolicy{BudgetUSD: float64Ptr(0), BudgetDuration: BudgetDuration7d})
	if p4.BudgetWeeklyUSD != nil || p4.BudgetHourlyUSD != nil || p4.BudgetMonthlyUSD != nil {
		t.Fatalf("zero budget should not set any window: %+v", p4)
	}
}

func TestLiteLLMKeyStoreListAllPolicies(t *testing.T) {
	_, keys := newTestKeyImportStore(t, "key_listall_policies")
	ctx := context.Background()

	pol := LiteLLMPolicy{RPMLimit: intPtr(60), BudgetUSD: float64Ptr(15.0), BudgetDuration: BudgetDuration7d}
	if _, _, err := keys.Create(ctx, "k-1", "alias", "sk-secret-key-0123456789", nil, nil, []string{"prod"}, &pol); err != nil {
		t.Fatalf("Create: %v", err)
	}
	all, err := keys.ListAll(ctx)
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 1 || all[0].Name != "k-1" {
		t.Fatalf("ListAll wrong: %+v", all)
	}
	pols, err := keys.ListAllPolicies(ctx)
	if err != nil {
		t.Fatalf("ListAllPolicies: %v", err)
	}
	if len(pols) != 1 {
		t.Fatalf("ListAllPolicies len = %d; want 1", len(pols))
	}
	for id, p := range pols {
		if p.RPMLimit == nil || *p.RPMLimit != 60 {
			t.Fatalf("policy %s rpm wrong: %+v", id, p)
		}
		if p.BudgetUSD == nil || *p.BudgetUSD != 15.0 || p.BudgetDuration != BudgetDuration7d {
			t.Fatalf("policy %s budget wrong: %+v", id, p)
		}
	}
}
