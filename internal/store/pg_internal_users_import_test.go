package store

import (
	"context"
	"testing"
	"time"
)

// newTestImportStore opens a PostgresStore against PGSTORE_TEST_DSN, cleans both
// the runtime internal_users table and the Manage-LiteLLM tables, and returns
// the runtime UserStore + LiteLLMUserStore backing the "sync to NixLLM" push.
func newTestImportStore(t *testing.T, schema string) (*UserStore, *LiteLLMUserStore) {
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
		pg.cfg.UserWindowsTable,
		pg.cfg.APIKeysTable,
		pg.cfg.InternalUsersTable,
		pg.cfg.LiteLLMKeyPoliciesTable,
		pg.cfg.LiteLLMKeysTable,
		pg.cfg.LiteLLMUsersTable,
		pg.cfg.LiteLLMSyncSettingsTable,
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+pg.fullTableName(table)); err != nil {
			_ = err
		}
	}
	return NewUserStore(pg), NewLiteLLMUserStore(pg)
}

// countInternalUsers returns the total number of runtime internal_users rows.
func countInternalUsers(t *testing.T, s *UserStore) int {
	t.Helper()
	_, total, err := s.ListWithSpend(context.Background(), ListFilter{Page: 1, PageSize: 200})
	if err != nil {
		t.Fatalf("ListWithSpend: %v", err)
	}
	return int(total)
}

func TestImportLiteLLMUsersCreatesRuntimeRows(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_creates")
	ctx := context.Background()

	src := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole, RPMLimit: int64Ptr(60), TPMLimit: int64Ptr(1000)},
		{ID: "llu-bob", UserAlias: "bob", UserEmail: "bob@example.com", UserRole: LiteLLMUserRole, MaxBudget: float64Ptr(25.0), BudgetDuration: BudgetDuration7d},
	}
	imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src, false)
	if err != nil {
		t.Fatalf("ImportLiteLLMUsers: %v", err)
	}
	if imported != 2 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d; want 2/0", imported, skipped)
	}
	if got := countInternalUsers(t, runtimeUsers); got != 2 {
		t.Fatalf("runtime user count = %d; want 2", got)
	}

	alice, err := runtimeUsers.Get(ctx, "llu-alice")
	if err != nil {
		t.Fatalf("Get alice: %v", err)
	}
	if alice.UserAlias != "alice" || alice.UserEmail != "alice@example.com" {
		t.Fatalf("alice fields wrong: %+v", alice)
	}
	if alice.Spend != 0 {
		t.Fatalf("alice spend = %v; want 0 (fresh runtime user)", alice.Spend)
	}
	if alice.RPMLimit == nil || *alice.RPMLimit != 60 {
		t.Fatalf("alice rpm wrong: %+v", alice.RPMLimit)
	}

	bob, err := runtimeUsers.Get(ctx, "llu-bob")
	if err != nil {
		t.Fatalf("Get bob: %v", err)
	}
	if bob.MaxBudget == nil || *bob.MaxBudget != 25.0 || bob.BudgetDuration != BudgetDuration7d {
		t.Fatalf("bob budget fields wrong: %+v", bob)
	}
	if bob.BudgetResetAt == nil {
		t.Fatal("bob budget_reset_at should be armed from budget_duration")
	}
}

func TestImportLiteLLMUsersUpdatesByIdAndPreservesSpend(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_update_id")
	ctx := context.Background()

	src1 := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole, RPMLimit: int64Ptr(60)},
	}
	if imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src1, false); err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("first import: imported=%d skipped=%d err=%v", imported, skipped, err)
	}

	// The runtime row accrues spend through normal traffic.
	if err := runtimeUsers.IncrementSpend(ctx, "llu-alice", 4.25); err != nil {
		t.Fatalf("IncrementSpend: %v", err)
	}

	// Re-sync with changed alias + rpm (same id, same email).
	src2 := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice-smith", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole, RPMLimit: int64Ptr(120)},
	}
	imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src2, false)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("re-import imported=%d skipped=%d; want 1/0", imported, skipped)
	}
	if got := countInternalUsers(t, runtimeUsers); got != 1 {
		t.Fatalf("runtime user count = %d; want 1 (no duplicates)", got)
	}

	alice, err := runtimeUsers.Get(ctx, "llu-alice")
	if err != nil {
		t.Fatalf("Get alice: %v", err)
	}
	if alice.UserAlias != "alice-smith" || alice.RPMLimit == nil || *alice.RPMLimit != 120 {
		t.Fatalf("alice not updated in place: %+v", alice)
	}
	if alice.Spend != 4.25 {
		t.Fatalf("alice spend = %v; want 4.25 (runtime spend preserved)", alice.Spend)
	}
}

func TestImportLiteLLMUsersUpdatesByEmail(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_update_email")
	ctx := context.Background()

	// A runtime user already exists (different id) with the same email.
	existing, err := runtimeUsers.Create(ctx, InternalUser{
		ID:        "runtime-alice",
		UserAlias: "alice-local",
		UserEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Create runtime user: %v", err)
	}

	// The Manage-LiteLLM source user carries a different id.
	src := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice-remote", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole, RPMLimit: int64Ptr(90)},
	}
	imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src, false)
	if err != nil {
		t.Fatalf("ImportLiteLLMUsers: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d; want 1/0", imported, skipped)
	}
	if got := countInternalUsers(t, runtimeUsers); got != 1 {
		t.Fatalf("runtime user count = %d; want 1", got)
	}

	// The email-matched runtime row was updated in place; its id is preserved.
	got, err := runtimeUsers.Get(ctx, existing.ID)
	if err != nil {
		t.Fatalf("Get existing: %v", err)
	}
	if got.ID != "runtime-alice" {
		t.Fatalf("id = %q; want runtime-alice preserved", got.ID)
	}
	if got.UserAlias != "alice-remote" || got.RPMLimit == nil || *got.RPMLimit != 90 {
		t.Fatalf("email-matched row not updated: %+v", got)
	}
	// The source id must not be materialized as a second row.
	if _, err := runtimeUsers.Get(ctx, "llu-alice"); err == nil {
		t.Fatal("source id should not exist as a separate runtime row")
	}
}

func TestImportLiteLLMUsersUpdatesByIdWhenEmailChanges(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_email_change")
	ctx := context.Background()

	src1 := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole},
	}
	if imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src1, false); err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("first import: imported=%d skipped=%d err=%v", imported, skipped, err)
	}

	// The source user's email changed; the new email is free.
	src2 := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice", UserEmail: "alice.new@example.com", UserRole: LiteLLMUserRole},
	}
	if imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src2, false); err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("re-import: imported=%d skipped=%d err=%v", imported, skipped, err)
	}
	if got := countInternalUsers(t, runtimeUsers); got != 1 {
		t.Fatalf("runtime user count = %d; want 1", got)
	}
	alice, err := runtimeUsers.Get(ctx, "llu-alice")
	if err != nil {
		t.Fatalf("Get alice: %v", err)
	}
	if alice.UserEmail != "alice.new@example.com" {
		t.Fatalf("email = %q; want alice.new@example.com", alice.UserEmail)
	}
}

func TestImportLiteLLMUsersEmptySource(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_empty")
	ctx := context.Background()

	imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, nil, false)
	if err != nil {
		t.Fatalf("ImportLiteLLMUsers: %v", err)
	}
	if imported != 0 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d; want 0/0", imported, skipped)
	}
}

func TestImportLiteLLMUsersKeepsManualRuntimeUsers(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_manual")
	ctx := context.Background()

	manual, err := runtimeUsers.Create(ctx, InternalUser{
		UserAlias: "manual-user",
		UserEmail: "manual@example.com",
	})
	if err != nil {
		t.Fatalf("Create manual runtime user: %v", err)
	}

	src := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole},
	}
	imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src, false)
	if err != nil {
		t.Fatalf("ImportLiteLLMUsers: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d; want 1/0", imported, skipped)
	}
	// Upsert-only: the manual runtime user is untouched.
	got, err := runtimeUsers.Get(ctx, manual.ID)
	if err != nil {
		t.Fatalf("Get manual: %v", err)
	}
	if got.UserAlias != "manual-user" {
		t.Fatalf("manual user was modified: %+v", got)
	}
}

func TestLiteLLMUserToUpdateMirrorsSource(t *testing.T) {
	u := LiteLLMUser{
		ID:                  "u1",
		UserAlias:           "alice",
		UserEmail:           "alice@example.com",
		UserRole:            LiteLLMUserRole,
		Models:              []string{"gpt-4o"},
		MaxBudget:           float64Ptr(10.5),
		BudgetDuration:      BudgetDuration7d,
		RPMLimit:            int64Ptr(60),
		MaxParallelRequests: intPtr(3),
	}
	upd := liteLLMUserToUpdate(u)
	if upd.UserAlias == nil || *upd.UserAlias != "alice" {
		t.Fatalf("alias not mirrored: %+v", upd.UserAlias)
	}
	if upd.MaxBudget == nil || *upd.MaxBudget != 10.5 {
		t.Fatalf("max_budget not mirrored: %+v", upd.MaxBudget)
	}
	if upd.RPMLimit == nil || *upd.RPMLimit != 60 {
		t.Fatalf("rpm not mirrored: %+v", upd.RPMLimit)
	}
	if upd.MaxParallelRequests == nil || *upd.MaxParallelRequests != 3 {
		t.Fatalf("max_parallel_requests not mirrored: %+v", upd.MaxParallelRequests)
	}
	if upd.Models == nil || len(*upd.Models) != 1 {
		t.Fatalf("models not mirrored: %+v", upd.Models)
	}
}

func TestImportLiteLLMUsersOverwriteSpendOnCreate(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_overwrite_create")
	ctx := context.Background()

	src := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole, Spend: 42.5},
	}
	imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src, true)
	if err != nil {
		t.Fatalf("ImportLiteLLMUsers: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d; want 1/0", imported, skipped)
	}
	alice, err := runtimeUsers.Get(ctx, "llu-alice")
	if err != nil {
		t.Fatalf("Get alice: %v", err)
	}
	if alice.Spend != 42.5 {
		t.Fatalf("alice spend = %v; want 42.5 (overwritten on create)", alice.Spend)
	}
}

func TestImportLiteLLMUsersOverwriteSpendOnUpdate(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_overwrite_update")
	ctx := context.Background()

	// First import WITHOUT usage: spend stays 0.
	src1 := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole, Spend: 99.0},
	}
	if imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src1, false); err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("first import: imported=%d skipped=%d err=%v", imported, skipped, err)
	}
	alice, _ := runtimeUsers.Get(ctx, "llu-alice")
	if alice.Spend != 0 {
		t.Fatalf("spend after non-usage import = %v; want 0", alice.Spend)
	}

	// Re-import WITH usage: the runtime spend is overwritten by the source.
	imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src1, true)
	if err != nil {
		t.Fatalf("usage import: %v", err)
	}
	if imported != 1 || skipped != 0 {
		t.Fatalf("imported=%d skipped=%d; want 1/0", imported, skipped)
	}
	alice, _ = runtimeUsers.Get(ctx, "llu-alice")
	if alice.Spend != 99.0 {
		t.Fatalf("spend after usage import = %v; want 99.0", alice.Spend)
	}

	// A later usage import with Spend=0 overwrites back to zero.
	src0 := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole, Spend: 0},
	}
	if imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src0, true); err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("zero-spend import: imported=%d skipped=%d err=%v", imported, skipped, err)
	}
	alice, _ = runtimeUsers.Get(ctx, "llu-alice")
	if alice.Spend != 0 {
		t.Fatalf("spend after zero-spend import = %v; want 0", alice.Spend)
	}
}

func TestImportLiteLLMUsersOverwriteSpendByEmail(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "import_overwrite_email")
	ctx := context.Background()

	existing, err := runtimeUsers.Create(ctx, InternalUser{
		ID:        "runtime-alice",
		UserAlias: "alice-local",
		UserEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Create runtime user: %v", err)
	}

	src := []LiteLLMUser{
		{ID: "llu-alice", UserAlias: "alice-remote", UserEmail: "alice@example.com", UserRole: LiteLLMUserRole, Spend: 33.3},
	}
	if imported, skipped, err := runtimeUsers.ImportLiteLLMUsers(ctx, src, true); err != nil || imported != 1 || skipped != 0 {
		t.Fatalf("import: imported=%d skipped=%d err=%v", imported, skipped, err)
	}
	got, err := runtimeUsers.Get(ctx, existing.ID)
	if err != nil {
		t.Fatalf("Get existing: %v", err)
	}
	if got.ID != "runtime-alice" {
		t.Fatalf("id = %q; want runtime-alice preserved", got.ID)
	}
	if got.Spend != 33.3 {
		t.Fatalf("email-matched spend = %v; want 33.3 (overwritten)", got.Spend)
	}
}

func TestSetSpend(t *testing.T) {
	runtimeUsers, _ := newTestImportStore(t, "set_spend")
	ctx := context.Background()

	u, err := runtimeUsers.Create(ctx, InternalUser{
		UserAlias: "alice",
		UserEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := runtimeUsers.SetSpend(ctx, u.ID, 12.75); err != nil {
		t.Fatalf("SetSpend: %v", err)
	}
	got, _ := runtimeUsers.Get(ctx, u.ID)
	if got.Spend != 12.75 {
		t.Fatalf("spend = %v; want 12.75", got.Spend)
	}
}
