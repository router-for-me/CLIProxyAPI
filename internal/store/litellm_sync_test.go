package store

import (
	"context"
	"testing"
	"time"
)

// newTestLiteLLMSyncStore opens a PostgresStore against PGSTORE_TEST_DSN in the
// supplied schema and returns the LiteLLM sync + user/key stores over a single
// cleaned connection.
func newTestLiteLLMSyncStore(t *testing.T, schema string) (*LiteLLMSyncStore, *LiteLLMUserStore, *LiteLLMKeyStore) {
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
		pg.cfg.LiteLLMKeyPoliciesTable,
		pg.cfg.LiteLLMKeysTable,
		pg.cfg.LiteLLMUsersTable,
		pg.cfg.LiteLLMSyncSettingsTable,
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+pg.fullTableName(table)); err != nil {
			_ = err
		}
	}
	// Re-seed the singleton settings row (EnsureSchema normally does this) so
	// update-only methods like RecordSync have a row to hit.
	if _, err := pg.DB().ExecContext(ctx, "INSERT INTO "+pg.fullTableName(pg.cfg.LiteLLMSyncSettingsTable)+" (id) VALUES (1) ON CONFLICT (id) DO NOTHING"); err != nil {
		t.Fatalf("re-seed sync settings: %v", err)
	}
	return NewLiteLLMSyncStore(pg), NewLiteLLMUserStore(pg), NewLiteLLMKeyStore(pg)
}

func TestLiteLLMSyncSettingsRoundTrip(t *testing.T) {
	sync, _, _ := newTestLiteLLMSyncStore(t, "litellm_sync_settings")
	ctx := context.Background()

	// Defaults when unconfigured: disabled, safe interval, no key.
	def, err := sync.Get(ctx)
	if err != nil {
		t.Fatalf("Get default: %v", err)
	}
	if def.Enabled {
		t.Fatal("default should be disabled")
	}
	if def.MasterKeySet {
		t.Fatal("default should not have a master key")
	}

	// Upsert with a master key; it must be stored sealed + masked in Get.
	set := LiteLLMSyncSettings{Enabled: true, IntervalSeconds: 120, BaseURL: "https://litellm.example.com"}
	masterKey := "sk-LitellmMasterKey0123456789"
	persisted, err := sync.Upsert(ctx, set, &masterKey)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if !persisted.Enabled || persisted.BaseURL != "https://litellm.example.com" {
		t.Fatalf("Upsert persisted wrong: %+v", persisted)
	}
	if !persisted.MasterKeySet {
		t.Fatal("MasterKeySet should be true after setting a key")
	}
	if persisted.MasterKeyPrefix == "" {
		t.Fatal("expected a masked master_key_prefix")
	}

	// MasterKey() must return the plaintext for the sync runner.
	plain, err := sync.MasterKey(ctx)
	if err != nil {
		t.Fatalf("MasterKey: %v", err)
	}
	if plain != masterKey {
		t.Fatalf("MasterKey = %q; want %q", plain, masterKey)
	}

	// Interval reflects enabled state.
	if iv := sync.Interval(ctx); iv != 120*time.Second {
		t.Fatalf("Interval = %v; want 120s", iv)
	}

	// Update without masterKey must preserve the stored key.
	set2 := LiteLLMSyncSettings{Enabled: true, IntervalSeconds: 240, BaseURL: "https://litellm.example.com"}
	persisted2, err := sync.Upsert(ctx, set2, nil)
	if err != nil {
		t.Fatalf("Upsert no-key: %v", err)
	}
	if !persisted2.MasterKeySet {
		t.Fatal("MasterKeySet should be preserved when masterKey is nil")
	}
	plain2, _ := sync.MasterKey(ctx)
	if plain2 != masterKey {
		t.Fatalf("MasterKey after nil update = %q; want %q (must be kept)", plain2, masterKey)
	}

	// Clearing the key: pass empty string.
	set3 := LiteLLMSyncSettings{Enabled: false, IntervalSeconds: 300, BaseURL: "https://litellm.example.com"}
	empty := ""
	persisted3, err := sync.Upsert(ctx, set3, &empty)
	if err != nil {
		t.Fatalf("Upsert clear-key: %v", err)
	}
	if persisted3.MasterKeySet {
		t.Fatal("MasterKeySet should be false after clearing")
	}
	if p3, _ := sync.MasterKey(ctx); p3 != "" {
		t.Fatalf("MasterKey after clear = %q; want empty", p3)
	}
}

func TestLiteLLMSyncRecordOutcome(t *testing.T) {
	sync, _, _ := newTestLiteLLMSyncStore(t, "litellm_sync_outcome")
	ctx := context.Background()

	if err := sync.RecordSync(ctx, LiteLLMSyncStatusOK, "", 12, 7, 3); err != nil {
		t.Fatalf("RecordSync ok: %v", err)
	}
	set, err := sync.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if set.LastSyncStatus != LiteLLMSyncStatusOK || set.LastSyncUsers != 12 || set.LastSyncKeys != 7 {
		t.Fatalf("outcome not recorded: %+v", set)
	}
	if set.LastSyncLogs != 3 {
		t.Fatalf("last_sync_logs = %d; want 3", set.LastSyncLogs)
	}
	if set.LastSyncAt == nil {
		t.Fatal("LastSyncAt should be set")
	}

	if err := sync.RecordSync(ctx, LiteLLMSyncStatusFailed, "boom", 0, 0, 0); err != nil {
		t.Fatalf("RecordSync err: %v", err)
	}
	set, _ = sync.Get(ctx)
	if set.LastSyncStatus != LiteLLMSyncStatusFailed || set.LastSyncError != "boom" {
		t.Fatalf("error outcome not recorded: %+v", set)
	}
}

func TestLiteLLMSyncUpsertUserAndKey(t *testing.T) {
	sync, users, keys := newTestLiteLLMSyncStore(t, "litellm_sync_upsert")
	ctx := context.Background()

	// Upsert a user (insert).
	u, err := users.Upsert(ctx, LiteLLMUser{ID: "sync-user-1", UserAlias: "alice", UserEmail: "a@b.c", Spend: 3.5, TPMLimit: int64Ptr(100)})
	if err != nil {
		t.Fatalf("Upsert user insert: %v", err)
	}
	if u.ID != "sync-user-1" || u.Spend != 3.5 {
		t.Fatalf("user insert wrong: %+v", u)
	}

	// Upsert the same user again (update, spend carried over).
	u2, err := users.Upsert(ctx, LiteLLMUser{ID: "sync-user-1", UserAlias: "alice2", UserEmail: "a@b.c", Spend: 9.0, RPMLimit: int64Ptr(10)})
	if err != nil {
		t.Fatalf("Upsert user update: %v", err)
	}
	if u2.UserAlias != "alice2" || u2.Spend != 9.0 {
		t.Fatalf("user update wrong: %+v", u2)
	}

	// Upsert a key with a policy (insert).
	policy := LiteLLMPolicy{TPMLimit: intPtr(250), BudgetUSD: float64Ptr(20.0), AllowedModels: []string{"gpt-4o"}, Aliases: map[string]string{"gpt-4o": "primary"}}
	k, err := keys.Upsert(ctx, LiteLLMKey{ID: "sync-key-1", Name: "sk-1", UserID: "sync-user-1", Status: "active", Spend: 1.25, Tags: []string{"prod"}}, "", "hashabc", "prefix", &policy)
	if err != nil {
		t.Fatalf("Upsert key insert: %v", err)
	}
	if k.ID != "sync-key-1" || k.Spend != 1.25 {
		t.Fatalf("key insert wrong: %+v", k)
	}

	// Update the same key with a new policy.
	policy2 := LiteLLMPolicy{TPMLimit: intPtr(500), BudgetUSD: float64Ptr(50.0)}
	k2, err := keys.Upsert(ctx, LiteLLMKey{ID: "sync-key-1", Name: "sk-1b", UserID: "sync-user-1", Status: "active", Spend: 2.5}, "", "hashabc", "prefix", &policy2)
	if err != nil {
		t.Fatalf("Upsert key update: %v", err)
	}
	if k2.Name != "sk-1b" || k2.Spend != 2.5 {
		t.Fatalf("key update wrong: %+v", k2)
	}
	_, pol, err := keys.LookupByID(ctx, "sync-key-1")
	if err != nil {
		t.Fatalf("LookupByID: %v", err)
	}
	if pol == nil || pol.TPMLimit == nil || *pol.TPMLimit != 500 {
		t.Fatalf("policy not updated: %+v", pol)
	}
	if pol.BudgetUSD == nil || *pol.BudgetUSD != 50.0 {
		t.Fatalf("policy budget not updated: %+v", pol.BudgetUSD)
	}
	// syncStore is only used to hold the DB connection alive in this test.
	_ = sync
}

func TestLiteLLMSyncRecordNixLLMOutcome(t *testing.T) {
	sync, _, _ := newTestLiteLLMSyncStore(t, "litellm_sync_nixllm_outcome")
	ctx := context.Background()

	// Defaults: no NixLLM sync recorded yet.
	def, err := sync.Get(ctx)
	if err != nil {
		t.Fatalf("Get default: %v", err)
	}
	if def.LastNixLLMSyncAt != nil || def.LastNixLLMSyncStatus != "" || def.LastNixLLMSyncUsers != 0 {
		t.Fatalf("unexpected default NixLLM sync fields: %+v", def)
	}
	if def.LastNixLLMSyncUsage {
		t.Fatalf("unexpected default nixllm usage flag: %+v", def)
	}
	if def.LastLiteLLMUsersUpdatedAt != nil {
		t.Fatalf("unexpected default source update: %+v", def.LastLiteLLMUsersUpdatedAt)
	}

	srcUpdate := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if err := sync.RecordNixLLMSync(ctx, NixLLMSyncOutcome{
		Status: LiteLLMSyncStatusOK, Users: 5, Keys: 3, Logs: 42,
		SourceUpdatedAt: &srcUpdate, IncludedUsage: true,
	}); err != nil {
		t.Fatalf("RecordNixLLMSync ok: %v", err)
	}
	set, err := sync.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if set.LastNixLLMSyncStatus != LiteLLMSyncStatusOK || set.LastNixLLMSyncUsers != 5 {
		t.Fatalf("nixllm outcome not recorded: %+v", set)
	}
	if set.LastNixLLMSyncKeys != 3 || set.LastNixLLMSyncLogs != 42 {
		t.Fatalf("nixllm keys/logs outcome not recorded: %+v", set)
	}
	if !set.LastNixLLMSyncUsage {
		t.Fatal("LastNixLLMSyncUsage should be true after an include-usage push")
	}
	if set.LastNixLLMSyncAt == nil {
		t.Fatal("LastNixLLMSyncAt should be set")
	}
	if set.LastLiteLLMUsersUpdatedAt == nil || !set.LastLiteLLMUsersUpdatedAt.Equal(srcUpdate) {
		t.Fatalf("source update not recorded: %+v", set.LastLiteLLMUsersUpdatedAt)
	}

	// A failed push overwrites status + error; source update may be nil.
	if err := sync.RecordNixLLMSync(ctx, NixLLMSyncOutcome{Status: LiteLLMSyncStatusFailed, Error: "boom"}); err != nil {
		t.Fatalf("RecordNixLLMSync err: %v", err)
	}
	set, _ = sync.Get(ctx)
	if set.LastNixLLMSyncStatus != LiteLLMSyncStatusFailed || set.LastNixLLMSyncError != "boom" {
		t.Fatalf("failed outcome not recorded: %+v", set)
	}
	if set.LastNixLLMSyncUsage {
		t.Fatalf("nixllm usage flag should reflect the latest push (false): %+v", set)
	}
	if set.LastNixLLMSyncKeys != 0 || set.LastNixLLMSyncLogs != 0 {
		t.Fatalf("nixllm keys/logs should reset on failed push: %+v", set)
	}
	if set.LastLiteLLMUsersUpdatedAt != nil {
		t.Fatalf("source update should be nil after failed push: %+v", set.LastLiteLLMUsersUpdatedAt)
	}

	// The external-sync fields are independent of the NixLLM fields.
	if err := sync.RecordSync(ctx, LiteLLMSyncStatusOK, "", 3, 2, 0); err != nil {
		t.Fatalf("RecordSync: %v", err)
	}
	set, _ = sync.Get(ctx)
	if set.LastSyncUsers != 3 || set.LastSyncKeys != 2 {
		t.Fatalf("external outcome clobbered nixllm fields? %+v", set)
	}
	if set.LastNixLLMSyncStatus != LiteLLMSyncStatusFailed {
		t.Fatalf("nixllm status was clobbered by RecordSync: %+v", set)
	}
}
