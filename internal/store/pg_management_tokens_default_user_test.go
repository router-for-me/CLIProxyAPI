package store

import (
	"context"
	"reflect"
	"testing"
)

// TestManagementTokenDefaultUserRoundTrip verifies the default-user fallback
// columns persist and read back through both LookupByID and UpdateDefaultUserID.
func TestManagementTokenDefaultUserRoundTrip(t *testing.T) {
	parent := newTestPostgresStore(t, "mgmt_token_default_user")
	ctx := context.Background()
	if _, err := parent.DB().ExecContext(ctx, "DELETE FROM "+parent.fullTableName(parent.cfg.ManagementTokensTable)); err != nil {
		t.Fatalf("clean management_tokens: %v", err)
	}

	tokens := NewManagementTokenStore(parent)
	if tokens == nil {
		t.Fatal("NewManagementTokenStore returned nil")
	}

	endpoints := []string{"/v0/management/api-keys-pg", "/v0/management/litellm/*"}
	created, secret, err := tokens.Create(ctx, "ci-writer", MgmtTokenScopeWrite, "user-42", endpoints, nil, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if secret == "" {
		t.Fatal("Create returned empty secret")
	}
	if created.DefaultUserID != "user-42" {
		t.Fatalf("created.DefaultUserID = %q, want user-42", created.DefaultUserID)
	}
	if !reflect.DeepEqual(created.DefaultUserIDEndpoints, endpoints) {
		t.Fatalf("created endpoints = %v, want %v", created.DefaultUserIDEndpoints, endpoints)
	}

	got, _, err := tokens.LookupByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("LookupByID: %v", err)
	}
	if got.DefaultUserID != "user-42" || !reflect.DeepEqual(got.DefaultUserIDEndpoints, endpoints) {
		t.Fatalf("LookupByID default config = (%q, %v), want (user-42, %v)",
			got.DefaultUserID, got.DefaultUserIDEndpoints, endpoints)
	}

	if err := tokens.UpdateDefaultUserID(ctx, created.ID, "user-99", []string{"/v0/management/internal-users"}); err != nil {
		t.Fatalf("UpdateDefaultUserID: %v", err)
	}
	got, _, err = tokens.LookupByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("LookupByID after update: %v", err)
	}
	if got.DefaultUserID != "user-99" || !reflect.DeepEqual(got.DefaultUserIDEndpoints, []string{"/v0/management/internal-users"}) {
		t.Fatalf("after update = (%q, %v)", got.DefaultUserID, got.DefaultUserIDEndpoints)
	}

	if err := tokens.UpdateDefaultUserID(ctx, created.ID, "", nil); err != nil {
		t.Fatalf("UpdateDefaultUserID clear: %v", err)
	}
	got, _, err = tokens.LookupByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("LookupByID after clear: %v", err)
	}
	if got.DefaultUserID != "" || len(got.DefaultUserIDEndpoints) != 0 {
		t.Fatalf("after clear = (%q, %v), want empty", got.DefaultUserID, got.DefaultUserIDEndpoints)
	}

	// LookupByHash must surface the same columns the middleware relies on.
	if err := tokens.UpdateDefaultUserID(ctx, created.ID, "user-7", endpoints); err != nil {
		t.Fatalf("UpdateDefaultUserID: %v", err)
	}
	byHash, _, err := tokens.LookupByHash(ctx, HashSecret(secret))
	if err != nil {
		t.Fatalf("LookupByHash: %v", err)
	}
	if byHash.DefaultUserID != "user-7" || !reflect.DeepEqual(byHash.DefaultUserIDEndpoints, endpoints) {
		t.Fatalf("LookupByHash default config = (%q, %v)", byHash.DefaultUserID, byHash.DefaultUserIDEndpoints)
	}
}
