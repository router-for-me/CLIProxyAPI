package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// newTestLiteLLMStore opens a PostgresStore against PGSTORE_TEST_DSN in the
// supplied schema, ensures the schema (including the litellm_* tables), and
// cleans the litellm tables before returning the LiteLLM stores.
func newTestLiteLLMStore(t *testing.T, schema string) (*LiteLLMUserStore, *LiteLLMKeyStore) {
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
	// Clean the litellm tables before each run (policies first due to FK).
	for _, table := range []string{
		pg.cfg.LiteLLMKeyPoliciesTable,
		pg.cfg.LiteLLMKeysTable,
		pg.cfg.LiteLLMUsersTable,
	} {
		if _, err := pg.DB().ExecContext(ctx, "DELETE FROM "+pg.fullTableName(table)); err != nil {
			_ = err
		}
	}
	return NewLiteLLMUserStore(pg), NewLiteLLMKeyStore(pg)
}

func TestLiteLLMUserCRUD(t *testing.T) {
	users, _ := newTestLiteLLMStore(t, "litellm_users_crud")
	ctx := context.Background()

	created, err := users.Create(ctx, LiteLLMUser{
		UserAlias:      "alice",
		UserEmail:      "alice@example.com",
		MaxBudget:      float64Ptr(10.5),
		BudgetDuration: BudgetDuration7d,
		RPMLimit:       int64Ptr(60),
		TPMLimit:       int64Ptr(1000),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create: expected generated id")
	}
	if created.Spend != 0 {
		t.Fatalf("Create: spend = %v; want 0", created.Spend)
	}

	got, err := users.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserAlias != "alice" || got.UserEmail != "alice@example.com" {
		t.Fatalf("Get: alias/email mismatch: %+v", got)
	}
	if got.MaxBudget == nil || *got.MaxBudget != 10.5 {
		t.Fatalf("Get: max_budget mismatch: %+v", got.MaxBudget)
	}

	byEmail, err := users.GetByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("GetByEmail: %v", err)
	}
	if byEmail.ID != created.ID {
		t.Fatalf("GetByEmail: id = %q; want %q", byEmail.ID, created.ID)
	}

	// List with search filter.
	rows, total, err := users.List(ctx, LiteLLMListFilter{Search: "alice", Page: 1, PageSize: 25})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("List: total=%d rows=%d; want 1/1", total, len(rows))
	}
	if rows[0].KeyCount != 0 {
		t.Fatalf("List: key_count = %d; want 0", rows[0].KeyCount)
	}

	// Update.
	newAlias := "alice2"
	if err := users.Update(ctx, created.ID, LiteLLMUserUpdate{
		UserAlias: &newAlias,
		TPMLimit:  int64Ptr(2000),
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	updated, _ := users.Get(ctx, created.ID)
	if updated.UserAlias != "alice2" || updated.TPMLimit == nil || *updated.TPMLimit != 2000 {
		t.Fatalf("Update: fields not applied: %+v", updated)
	}

	// Duplicate email surfaces the sentinel.
	if _, err := users.Create(ctx, LiteLLMUser{UserEmail: "alice@example.com"}); !errors.Is(err, ErrLiteLLMUserEmailExists) {
		t.Fatalf("Create duplicate email: err = %v; want ErrLiteLLMUserEmailExists", err)
	}

	// Reset spend re-arms the budget window.
	if err := users.ResetSpend(ctx, created.ID); err != nil {
		t.Fatalf("ResetSpend: %v", err)
	}

	// Delete.
	if err := users.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := users.Get(ctx, created.ID); !errors.Is(err, ErrLiteLLMUserNotFound) {
		t.Fatalf("Get after delete: err = %v; want ErrLiteLLMUserNotFound", err)
	}
}

func TestLiteLLMKeyCRUDWithCompletePolicy(t *testing.T) {
	users, keys := newTestLiteLLMStore(t, "litellm_keys_crud")
	ctx := context.Background()

	user, err := users.Create(ctx, LiteLLMUser{UserAlias: "bob", UserEmail: "bob@example.com"})
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}

	policy := LiteLLMPolicy{
		RPMLimit:            intPtr(30),
		TPMLimit:            intPtr(500),
		BudgetUSD:           float64Ptr(25.0),
		BudgetDuration:      BudgetDuration1d,
		MaxParallelRequests: intPtr(3),
		AllowedModels:       []string{"gpt-4o"},
		BlockedModels:       []string{"gpt-4o-mini"},
		Aliases:             map[string]string{"gpt-4o": "primary"},
		AllowedIPs:          []string{"10.0.0.5"},
	}
	createdKey, secret, err := keys.Create(ctx, "prod", "prod-alias", "", nil, nil, []string{"prod", "critical"}, &policy)
	if err != nil {
		t.Fatalf("Create key: %v", err)
	}
	if secret == "" {
		t.Fatal("Create key: expected plaintext secret")
	}
	if createdKey.Status != LiteLLMKeyStatusActive {
		t.Fatalf("Create key: status = %q; want active", createdKey.Status)
	}

	// Attach the owner.
	if err := keys.UpdateUserID(ctx, createdKey.ID, user.ID); err != nil {
		t.Fatalf("UpdateUserID: %v", err)
	}

	got, gotPolicy, err := keys.LookupByID(ctx, createdKey.ID)
	if err != nil {
		t.Fatalf("LookupByID: %v", err)
	}
	if got.UserID != user.ID || got.UserAlias != "bob" {
		t.Fatalf("LookupByID: owner join mismatch: %+v", got)
	}
	if gotPolicy == nil {
		t.Fatal("LookupByID: expected policy")
	}
	if gotPolicy.TPMLimit == nil || *gotPolicy.TPMLimit != 500 {
		t.Fatalf("LookupByID: tpm_limit mismatch: %+v", gotPolicy.TPMLimit)
	}
	if gotPolicy.BudgetUSD == nil || *gotPolicy.BudgetUSD != 25.0 {
		t.Fatalf("LookupByID: budget_usd mismatch: %+v", gotPolicy.BudgetUSD)
	}
	if gotPolicy.Aliases["gpt-4o"] != "primary" {
		t.Fatalf("LookupByID: aliases mismatch: %+v", gotPolicy.Aliases)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "prod" {
		t.Fatalf("LookupByID: tags mismatch: %+v", got.Tags)
	}

	// List paged with native user_id filter — total must be exactly 1.
	rows, total, err := keys.ListPaged(ctx, 1, 25, LiteLLMKeyListFilter{UserID: user.ID})
	if err != nil {
		t.Fatalf("ListPaged: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("ListPaged: total=%d rows=%d; want 1/1", total, len(rows))
	}
	if rows[0].ID != createdKey.ID {
		t.Fatalf("ListPaged: id mismatch: %q", rows[0].ID)
	}

	// Update policy (replace) and verify.
	newPolicy := LiteLLMPolicy{RPMLimit: intPtr(99), TPMLimit: intPtr(777), AllowedModels: []string{"claude-sonnet-4-5"}}
	if err := keys.UpdatePolicy(ctx, createdKey.ID, newPolicy); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}
	_, pol2, _ := keys.LookupByID(ctx, createdKey.ID)
	if pol2.RPMLimit == nil || *pol2.RPMLimit != 99 {
		t.Fatalf("UpdatePolicy: rpm_limit not replaced: %+v", pol2.RPMLimit)
	}
	if pol2.BudgetUSD != nil {
		t.Fatalf("UpdatePolicy: budget_usd should be cleared on replace, got %+v", pol2.BudgetUSD)
	}

	// Regenerate rotates the secret.
	newSecret, err := keys.Regenerate(ctx, createdKey.ID, "")
	if err != nil {
		t.Fatalf("Regenerate: %v", err)
	}
	if newSecret == "" || newSecret == secret {
		t.Fatal("Regenerate: expected a fresh secret")
	}

	// Delete.
	if err := keys.Delete(ctx, createdKey.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := keys.LookupByID(ctx, createdKey.ID); !errors.Is(err, ErrLiteLLMKeyNotFound) {
		t.Fatalf("LookupByID after delete: err = %v; want ErrLiteLLMKeyNotFound", err)
	}
}

func TestLiteLLMUserDeleteDetachesKeys(t *testing.T) {
	users, keys := newTestLiteLLMStore(t, "litellm_user_delete")
	ctx := context.Background()

	user, _ := users.Create(ctx, LiteLLMUser{UserAlias: "carol"})
	k, _, err := keys.Create(ctx, "carol-key", "", "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Create key: %v", err)
	}
	if err := keys.UpdateUserID(ctx, k.ID, user.ID); err != nil {
		t.Fatalf("UpdateUserID: %v", err)
	}

	// Deleting the user detaches (not deletes) the key.
	if err := users.Delete(ctx, user.ID); err != nil {
		t.Fatalf("Delete user: %v", err)
	}
	got, _, err := keys.LookupByID(ctx, k.ID)
	if err != nil {
		t.Fatalf("LookupByID after user delete: %v", err)
	}
	if got.UserID != "" {
		t.Fatalf("user_id after delete = %q; want detached (empty)", got.UserID)
	}
}

func TestLiteLLMKeyListUserIDFilterCountsPerUser(t *testing.T) {
	users, keys := newTestLiteLLMStore(t, "litellm_key_user_filter")
	ctx := context.Background()

	u1, _ := users.Create(ctx, LiteLLMUser{UserAlias: "u1"})
	u2, _ := users.Create(ctx, LiteLLMUser{UserAlias: "u2"})
	for i := 0; i < 2; i++ {
		k, _, err := keys.Create(ctx, "u1-key", "", "", nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("Create u1 key: %v", err)
		}
		if err := keys.UpdateUserID(ctx, k.ID, u1.ID); err != nil {
			t.Fatalf("UpdateUserID: %v", err)
		}
	}
	k, _, err := keys.Create(ctx, "u2-key", "", "", nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("Create u2 key: %v", err)
	}
	if err := keys.UpdateUserID(ctx, k.ID, u2.ID); err != nil {
		t.Fatalf("UpdateUserID: %v", err)
	}

	// Native user_id filter: totals reflect only that user's keys.
	_, total1, err := keys.ListPaged(ctx, 1, 25, LiteLLMKeyListFilter{UserID: u1.ID})
	if err != nil {
		t.Fatalf("ListPaged u1: %v", err)
	}
	if total1 != 2 {
		t.Fatalf("ListPaged u1: total = %d; want 2", total1)
	}
	_, total2, err := keys.ListPaged(ctx, 1, 25, LiteLLMKeyListFilter{UserID: u2.ID})
	if err != nil {
		t.Fatalf("ListPaged u2: %v", err)
	}
	if total2 != 1 {
		t.Fatalf("ListPaged u2: total = %d; want 1", total2)
	}
}

func TestLiteLLMKeySpendSort(t *testing.T) {
	users, keys := newTestLiteLLMStore(t, "litellm_key_spend_sort")
	ctx := context.Background()

	user, _ := users.Create(ctx, LiteLLMUser{UserAlias: "spender"})
	for _, name := range []string{"low", "high", "mid"} {
		k, _, err := keys.Create(ctx, name, "", "", nil, nil, nil, nil)
		if err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
		if err := keys.UpdateUserID(ctx, k.ID, user.ID); err != nil {
			t.Fatalf("UpdateUserID: %v", err)
		}
	}
	// Bump spend directly so the sort has something to order by.
	if _, err := keys.db.ExecContext(ctx,
		`UPDATE `+keys.keysTable+` SET key_spend = CASE name WHEN 'high' THEN 3.0 WHEN 'mid' THEN 1.5 WHEN 'low' THEN 0.5 ELSE key_spend END`); err != nil {
		t.Fatalf("seed spend: %v", err)
	}

	rows, _, err := keys.ListPaged(ctx, 1, 25, LiteLLMKeyListFilter{SortBy: "spend", SortOrder: "desc"})
	if err != nil {
		t.Fatalf("ListPaged: %v", err)
	}
	if len(rows) != 3 || rows[0].Name != "high" || rows[1].Name != "mid" || rows[2].Name != "low" {
		t.Fatalf("spend desc order wrong: %+v", namesOf(rows))
	}
}

func namesOf(keys []*LiteLLMKey) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != nil {
			out = append(out, k.Name)
		}
	}
	return out
}

func float64Ptr(v float64) *float64 { return &v }
func int64Ptr(v int64) *int64       { return &v }
func intPtr(v int) *int             { return &v }
