package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// TestValidateUpstreamProviderEntryNameRules exercises the validation rules for
// per-entry identity on an OpenAI Compatibility provider. The suite covers the
// shared helpers (slug syntax, reserved name, case-insensitive duplicates,
// duplicate ids, non-positive ids) and the constructor path rejecting blank
// api keys. Names carrying whitespace must normalize to a valid slug; invalid
// characters and the reserved key-<digits> form must be rejected.
func TestValidateUpstreamProviderEntryNameRules(t *testing.T) {
	type tcase struct {
		name    string
		entries []UpstreamProviderAPIKey
		wantSub string
	}
	cases := []tcase{
		{
			name: "blank api key",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "", Name: "team-a"},
			},
			wantSub: "api_key",
		},
		{
			name: "whitespace name normalizes",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-1", Name: "  Team-A  "},
			},
		},
		{
			name: "invalid characters rejected",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-2", Name: "team a!"},
			},
			wantSub: "name",
		},
		{
			name: "reserved key-42 rejected",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-3", Name: "key-42"},
			},
			wantSub: "reserved",
		},
		{
			name: "reserved Key-42 rejected",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-4", Name: "Key-42"},
			},
			wantSub: "reserved",
		},
		{
			name: "duplicate names case-insensitive",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-5", Name: "Team-A"},
				{APIKey: "placeholder-secret-6", Name: "team-a"},
			},
			wantSub: "duplicate",
		},
		{
			name: "duplicate positive ids",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-7", Name: "alpha", ID: 9},
				{APIKey: "placeholder-secret-8", Name: "beta", ID: 9},
			},
			wantSub: "duplicate",
		},
		{
			name: "non-positive id rejected",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-9", Name: "alpha", ID: -3},
			},
			wantSub: "positive",
		},
		{
			name: "valid entry",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-10", Name: "team_a"},
			},
		},
		{
			name: "valid entry with no name",
			entries: []UpstreamProviderAPIKey{
				{APIKey: "placeholder-secret-11"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := UpstreamProvider{
				ProviderType:  "openai-compatibility",
				APIKeyEntries: append([]UpstreamProviderAPIKey(nil), tc.entries...),
			}
			err := validateUpstreamProvider(p)
			if tc.wantSub == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantSub)
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.wantSub) {
				t.Fatalf("expected error containing %q, got %q", tc.wantSub, err.Error())
			}
			if strings.Contains(err.Error(), "placeholder-secret") || strings.Contains(err.Error(), "secret-") {
				t.Fatalf("error must not leak api key value, got %q", err.Error())
			}
		})
	}
}

// TestNormalizeUpstreamProviderCopiesEntryValues verifies that persistence
// normalization trims the API key/proxy and lowercases the name in a copy,
// leaving caller-owned values unchanged.
func TestNormalizeUpstreamProviderCopiesEntryValues(t *testing.T) {
	input := UpstreamProvider{
		ProviderType: "openai-compatibility",
		APIKeyEntries: []UpstreamProviderAPIKey{{
			APIKey:   "  placeholder-secret-copy  ",
			Name:     "  Team-A  ",
			ProxyURL: "  http://proxy.example  ",
		}},
	}

	got, err := normalizeUpstreamProvider(input)
	if err != nil {
		t.Fatalf("normalizeUpstreamProvider() error = %v", err)
	}
	if got.APIKeyEntries[0].APIKey != "placeholder-secret-copy" {
		t.Fatalf("normalized API key = %q, want trimmed value", got.APIKeyEntries[0].APIKey)
	}
	if got.APIKeyEntries[0].Name != "team-a" {
		t.Fatalf("normalized name = %q, want team-a", got.APIKeyEntries[0].Name)
	}
	if got.APIKeyEntries[0].ProxyURL != "http://proxy.example" {
		t.Fatalf("normalized proxy URL = %q, want trimmed value", got.APIKeyEntries[0].ProxyURL)
	}
	if input.APIKeyEntries[0].APIKey != "  placeholder-secret-copy  " {
		t.Fatalf("input API key was mutated: %q", input.APIKeyEntries[0].APIKey)
	}
	if input.APIKeyEntries[0].Name != "  Team-A  " {
		t.Fatalf("input name was mutated: %q", input.APIKeyEntries[0].Name)
	}
}

func TestUpstreamProviderStoreAPIKeyEntryIdentitySync(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_sync")
	defer pg.Close()
	ensureMigrated(t, pg)

	store := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := store.Create(ctx, UpstreamProvider{
		ProviderType: "openai-compatibility",
		Name:         "primary",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "  entry-secret-a  ", Name: "  Alpha  ", ProxyURL: "  http://proxy-a.example  "},
			{APIKey: "entry-secret-b", Name: "Beta", ProxyURL: "http://proxy-b.example"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Create returned zero provider ID")
	}
	if len(created.APIKeyEntries) != 2 {
		t.Fatalf("Create returned %d entries, want 2", len(created.APIKeyEntries))
	}
	firstID := created.APIKeyEntries[0].ID
	secondID := created.APIKeyEntries[1].ID
	if firstID == 0 || secondID == 0 || firstID == secondID {
		t.Fatalf("Create returned entry IDs %d and %d, want distinct positive IDs", firstID, secondID)
	}
	if created.APIKeyEntries[0].APIKey != "entry-secret-a" || created.APIKeyEntries[0].Name != "alpha" ||
		created.APIKeyEntries[0].ProxyURL != "http://proxy-a.example" {
		t.Fatalf("Create did not return normalized first entry: %+v", created.APIKeyEntries[0])
	}
	if created.APIKeyEntries[0].ProviderID != created.ID || created.APIKeyEntries[1].ProviderID != created.ID {
		t.Fatalf("Create returned wrong entry provider IDs: %+v", created.APIKeyEntries)
	}

	updated, err := store.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "primary-renamed",
		BaseURL:      "https://api.example.test/v2",
		// Reordering existing rows must preserve their IDs. The zero ID is a new
		// row, and the omitted firstID must be deleted.
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: secondID, APIKey: "  entry-secret-b-updated  ", Name: " BETA-UPDATED ", ProxyURL: " http://proxy-b-new.example "},
			{APIKey: " entry-secret-c ", Name: " Gamma "},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "primary-renamed" || updated.BaseURL != "https://api.example.test/v2" {
		t.Fatalf("Update did not return updated parent: %+v", updated)
	}
	if len(updated.APIKeyEntries) != 2 {
		t.Fatalf("Update returned %d entries, want 2", len(updated.APIKeyEntries))
	}
	if updated.APIKeyEntries[0].ID != secondID {
		t.Fatalf("updated existing entry ID = %d, want preserved ID %d", updated.APIKeyEntries[0].ID, secondID)
	}
	newID := updated.APIKeyEntries[1].ID
	if newID == 0 || newID == firstID || newID == secondID {
		t.Fatalf("updated inserted entry ID = %d, want a new positive ID", newID)
	}
	if updated.APIKeyEntries[0].APIKey != "entry-secret-b-updated" || updated.APIKeyEntries[0].Name != "beta-updated" ||
		updated.APIKeyEntries[0].ProxyURL != "http://proxy-b-new.example" {
		t.Fatalf("Update did not normalize existing entry: %+v", updated.APIKeyEntries[0])
	}
	if updated.APIKeyEntries[0].SortOrder != 0 || updated.APIKeyEntries[1].SortOrder != 1 {
		t.Fatalf("Update sort order = %d, %d; want 0, 1", updated.APIKeyEntries[0].SortOrder, updated.APIKeyEntries[1].SortOrder)
	}

	loaded, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if len(loaded.APIKeyEntries) != 2 || loaded.APIKeyEntries[0].ID != secondID || loaded.APIKeyEntries[1].ID != newID {
		t.Fatalf("Get returned entries %+v, want IDs [%d %d] in request order", loaded.APIKeyEntries, secondID, newID)
	}
	if loaded.APIKeyEntries[0].ProviderID != created.ID || loaded.APIKeyEntries[1].ProviderID != created.ID {
		t.Fatalf("Get returned wrong provider IDs: %+v", loaded.APIKeyEntries)
	}

	other, err := store.Create(ctx, UpstreamProvider{
		ProviderType:  "openai-compatibility",
		Name:          "other",
		BaseURL:       "https://other.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{{APIKey: "other-secret", Name: "other-key"}},
	})
	if err != nil {
		t.Fatalf("Create other provider: %v", err)
	}
	foreignID := other.APIKeyEntries[0].ID
	beforeRejectedUpdate, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get before rejected update: %v", err)
	}
	_, err = store.Update(ctx, UpstreamProvider{
		ID:            created.ID,
		ProviderType:  "openai-compatibility",
		Name:          "must-rollback",
		BaseURL:       "https://must-rollback.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{{ID: foreignID, APIKey: "not-written", Name: "foreign"}},
	})
	if err == nil || !strings.Contains(err.Error(), "belongs to upstream provider") {
		t.Fatalf("cross-provider ID error = %v, want ownership error", err)
	}
	afterRejectedUpdate, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after rejected update: %v", err)
	}
	if afterRejectedUpdate.Name != beforeRejectedUpdate.Name || afterRejectedUpdate.BaseURL != beforeRejectedUpdate.BaseURL {
		t.Fatalf("parent changed after rejected child sync: before=%+v after=%+v", beforeRejectedUpdate, afterRejectedUpdate)
	}
	if len(afterRejectedUpdate.APIKeyEntries) != len(beforeRejectedUpdate.APIKeyEntries) {
		t.Fatalf("children changed after rejected child sync: before=%+v after=%+v", beforeRejectedUpdate.APIKeyEntries, afterRejectedUpdate.APIKeyEntries)
	}
	for i := range beforeRejectedUpdate.APIKeyEntries {
		if afterRejectedUpdate.APIKeyEntries[i] != beforeRejectedUpdate.APIKeyEntries[i] {
			t.Fatalf("child %d changed after rejected child sync: before=%+v after=%+v", i, beforeRejectedUpdate.APIKeyEntries[i], afterRejectedUpdate.APIKeyEntries[i])
		}
	}

	var missingID int64
	if err := pg.DB().QueryRowContext(ctx, fmt.Sprintf(
		`SELECT COALESCE(MAX(id), 0) + 1 FROM %s`, pg.fullTableName(pg.cfg.UpstreamProviderEntriesTable)),
	).Scan(&missingID); err != nil {
		t.Fatalf("find missing entry ID: %v", err)
	}
	_, err = store.Update(ctx, UpstreamProvider{
		ID:            created.ID,
		ProviderType:  "openai-compatibility",
		Name:          "missing-id",
		BaseURL:       "https://missing-id.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{{ID: missingID, APIKey: "missing-secret", Name: "missing"}},
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing ID error = %v, want not-found error", err)
	}

	_, err = store.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "duplicate-id",
		BaseURL:      "https://duplicate-id.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: secondID, APIKey: "duplicate-one", Name: "duplicate-one"},
			{ID: secondID, APIKey: "duplicate-two", Name: "duplicate-two"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate ID error = %v, want duplicate error", err)
	}
}
