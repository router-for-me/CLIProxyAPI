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

// TestUpstreamProviderStoreAPIKeyEntryWeightRoundTrip verifies the nullable
// Weight field on UpstreamProviderAPIKey is faithfully persisted on insert,
// preserved on update (in place), cleared back to nil, and round-trips through
// Get without leaking the entry api key. NULL/omitted weights must round-trip
// as nil so the dashboard can distinguish "user did not pick a weight" from
// "user picked weight N". The migration must add a nullable INTEGER column
// (not NOT NULL) so existing rows stay readable.
func TestUpstreamProviderStoreAPIKeyEntryWeightRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_weight")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The migration must add a nullable INTEGER weight column. NOT NULL with a
	// default would hide the "user did not pick a weight" signal from the
	// dashboard, and DEFAULT 0 would silently change routing for legacy rows.
	colType, colNullable, colDefault := "", "", ""
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'weight'
	`, pg.cfg.Schema, pg.cfg.UpstreamProviderEntriesTable).Scan(&colType, &colNullable, &colDefault); err != nil {
		t.Fatalf("query weight column: %v", err)
	}
	if colType != "integer" {
		t.Fatalf("weight column type = %q, want integer", colType)
	}
	if colNullable != "YES" {
		t.Fatalf("weight column nullable = %q, want YES", colNullable)
	}
	if colDefault != "" {
		t.Fatalf("weight column default = %q, want empty (NULL default)", colDefault)
	}

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "openai-compatibility",
		Name:         "weighted",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "weighted-secret-1", Name: "alpha"},
			{APIKey: "weighted-secret-2", Name: "beta"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	firstID := created.APIKeyEntries[0].ID
	secondID := created.APIKeyEntries[1].ID
	if created.APIKeyEntries[0].Weight != nil {
		t.Fatalf("Create returned Weight = %d for omitted entry, want nil", *created.APIKeyEntries[0].Weight)
	}
	if created.APIKeyEntries[1].Weight != nil {
		t.Fatalf("Create returned Weight = %d for omitted entry, want nil", *created.APIKeyEntries[1].Weight)
	}

	positive := 25
	updated, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "weighted",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "weighted-secret-1", Name: "alpha", Weight: &positive},
			{ID: secondID, APIKey: "weighted-secret-2", Name: "beta"},
		},
	})
	if err != nil {
		t.Fatalf("Update with weight: %v", err)
	}
	if updated.APIKeyEntries[0].Weight == nil || *updated.APIKeyEntries[0].Weight != positive {
		t.Fatalf("updated Weight = %v, want pointer to %d", updated.APIKeyEntries[0].Weight, positive)
	}
	if updated.APIKeyEntries[1].Weight != nil {
		t.Fatalf("updated second entry Weight = %v, want nil", updated.APIKeyEntries[1].Weight)
	}

	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after weighted update: %v", err)
	}
	if len(loaded.APIKeyEntries) != 2 {
		t.Fatalf("Get returned %d entries, want 2", len(loaded.APIKeyEntries))
	}
	var loadedFirst, loadedSecond UpstreamProviderAPIKey
	for _, e := range loaded.APIKeyEntries {
		switch e.ID {
		case firstID:
			loadedFirst = e
		case secondID:
			loadedSecond = e
		}
	}
	if loadedFirst.Weight == nil || *loadedFirst.Weight != positive {
		t.Fatalf("loaded first entry Weight = %v, want pointer to %d", loadedFirst.Weight, positive)
	}
	if loadedSecond.Weight != nil {
		t.Fatalf("loaded second entry Weight = %v, want nil", loadedSecond.Weight)
	}

	// Clearing an explicit weight back to nil must round-trip — that's the
	// dashboard's "reset to default" affordance.
	cleared, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "weighted",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "weighted-secret-1", Name: "alpha"},
			{ID: secondID, APIKey: "weighted-secret-2", Name: "beta", Weight: &positive},
		},
	})
	if err != nil {
		t.Fatalf("Update clearing weight: %v", err)
	}
	if cleared.APIKeyEntries[0].Weight != nil {
		t.Fatalf("cleared entry Weight = %v, want nil", cleared.APIKeyEntries[0].Weight)
	}
	if cleared.APIKeyEntries[1].Weight == nil || *cleared.APIKeyEntries[1].Weight != positive {
		t.Fatalf("moved Weight = %v, want pointer to %d", cleared.APIKeyEntries[1].Weight, positive)
	}

	reloaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	for _, e := range reloaded.APIKeyEntries {
		switch e.ID {
		case firstID:
			if e.Weight != nil {
				t.Fatalf("reloaded first entry Weight = %v, want nil", e.Weight)
			}
		case secondID:
			if e.Weight == nil || *e.Weight != positive {
				t.Fatalf("reloaded second entry Weight = %v, want pointer to %d", e.Weight, positive)
			}
		}
	}

	// Inserting a brand-new entry with an explicit weight must also round-trip.
	newSecret := "weighted-secret-3"
	bigger := 500_000
	fresh, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "weighted",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "weighted-secret-1", Name: "alpha"},
			{ID: secondID, APIKey: "weighted-secret-2", Name: "beta"},
			{APIKey: newSecret, Name: "gamma", Weight: &bigger},
		},
	})
	if err != nil {
		t.Fatalf("Update inserting weighted entry: %v", err)
	}
	if len(fresh.APIKeyEntries) != 3 {
		t.Fatalf("fresh returned %d entries, want 3", len(fresh.APIKeyEntries))
	}
	var newID int64
	for _, e := range fresh.APIKeyEntries {
		if e.ID != firstID && e.ID != secondID {
			newID = e.ID
			if e.Weight == nil || *e.Weight != bigger {
				t.Fatalf("new entry Weight = %v, want pointer to %d", e.Weight, bigger)
			}
		}
	}
	if newID == 0 {
		t.Fatal("could not find new entry ID after insert")
	}

	final, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after insert: %v", err)
	}
	for _, e := range final.APIKeyEntries {
		if e.ID != newID {
			continue
		}
		if e.Weight == nil || *e.Weight != bigger {
			t.Fatalf("final new entry Weight = %v, want pointer to %d", e.Weight, bigger)
		}
		if e.APIKey != newSecret {
			t.Fatalf("final new entry api key mismatch: got %q", e.APIKey)
		}
	}
}

// TestUpstreamProviderStoreClaudeAPIKeyEntryIdentitySync exercises the
// child-entry CRUD/update/delete/id-sync semantics for a "claude-api-key"
// upstream provider row. The PG store's syncAPIKeyEntriesTx contract must
// hold across provider types: an existing child id survives an update, a
// brand-new child (zero id) receives a fresh positive id, and an omitted
// child is deleted in the same transaction. This is the persistence-side
// mirror of Task 2's renderer fan-out — without stable child ids the
// downstream `claude:<rowID>:key-<entryID>` route identity cannot survive a
// PUT, so the test guards the contract end-to-end. Fake redacted credentials
// are used throughout; secret values never appear in failure messages.
//
// The test is environment-gated on PGSTORE_TEST_DSN (mirroring the existing
// helper); when unset it is skipped so unit-test runs remain hermetic.
func TestUpstreamProviderStoreClaudeAPIKeyEntryIdentitySync(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_claude_entry_sync")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Create a Claude provider with two api_key_entries; both should get
	// positive, distinct ids and ProviderID stamped onto the returned rows.
	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "claude-api-key",
		Name:         "primary",
		Prefix:       "teamA/",
		BaseURL:      "https://claude.example.test",
		ProxyURL:     "http://provider-proxy",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "  claude-entry-secret-a  ", Name: "  Alpha  ", ProxyURL: "  http://proxy-a.example  "},
			{APIKey: "claude-entry-secret-b", Name: "Beta", ProxyURL: "http://proxy-b.example"},
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
	if created.APIKeyEntries[0].ProviderID != created.ID || created.APIKeyEntries[1].ProviderID != created.ID {
		t.Fatalf("Create returned wrong entry provider IDs: %+v", created.APIKeyEntries)
	}
	// The Claude-specific scalar fields must round-trip on the parent row
	// (the renderer copies them onto every emitted config.ClaudeKey).
	if created.Prefix != "teamA/" || created.BaseURL != "https://claude.example.test" || created.ProxyURL != "http://provider-proxy" {
		t.Fatalf("Create did not persist Claude row-level fields: %+v", created)
	}

	// Update: keep the second child id, drop the first (id omitted from the
	// request), and add a brand-new third entry. The retained id must
	// survive, the new entry must receive a positive id distinct from the
	// retained one, and the dropped id must no longer be present after Get.
	positive := 7
	updated, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "claude-api-key",
		Name:         "primary-renamed",
		BaseURL:      "https://claude.example.test/v2",
		ProxyURL:     "http://provider-proxy-v2",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: secondID, APIKey: "claude-entry-secret-b-updated", Name: "BETA-UPDATED", ProxyURL: "http://proxy-b-new.example", Weight: &positive},
			{APIKey: "claude-entry-secret-c", Name: "Gamma", ProxyURL: "http://proxy-c.example"},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Name != "primary-renamed" || updated.BaseURL != "https://claude.example.test/v2" {
		t.Fatalf("Update did not return updated parent: %+v", updated)
	}
	if len(updated.APIKeyEntries) != 2 {
		t.Fatalf("Update returned %d entries, want 2 (retained + new)", len(updated.APIKeyEntries))
	}
	if updated.APIKeyEntries[0].ID != secondID {
		t.Fatalf("updated existing entry ID = %d, want preserved ID %d", updated.APIKeyEntries[0].ID, secondID)
	}
	newID := updated.APIKeyEntries[1].ID
	if newID == 0 || newID == firstID || newID == secondID {
		t.Fatalf("updated inserted entry ID = %d, want a new positive ID", newID)
	}
	if updated.APIKeyEntries[0].Weight == nil || *updated.APIKeyEntries[0].Weight != positive {
		t.Fatalf("updated retained entry Weight = %v, want pointer to %d", updated.APIKeyEntries[0].Weight, positive)
	}
	if updated.APIKeyEntries[1].Weight != nil {
		t.Fatalf("updated new entry Weight = %v, want nil", updated.APIKeyEntries[1].Weight)
	}
	if updated.APIKeyEntries[0].SortOrder != 0 || updated.APIKeyEntries[1].SortOrder != 1 {
		t.Fatalf("Update sort order = %d, %d; want 0, 1", updated.APIKeyEntries[0].SortOrder, updated.APIKeyEntries[1].SortOrder)
	}

	// Get after Update must reflect the synced shape: retained id present,
	// firstID gone, newID present, weights preserved.
	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if len(loaded.APIKeyEntries) != 2 {
		t.Fatalf("Get returned %d entries, want 2", len(loaded.APIKeyEntries))
	}
	if loaded.APIKeyEntries[0].ID != secondID || loaded.APIKeyEntries[1].ID != newID {
		t.Fatalf("Get returned IDs %d, %d; want [%d %d] in request order",
			loaded.APIKeyEntries[0].ID, loaded.APIKeyEntries[1].ID, secondID, newID)
	}
	for _, e := range loaded.APIKeyEntries {
		if e.ID == firstID {
			t.Fatalf("Get still contains dropped child id=%d", firstID)
		}
		if e.ProviderID != created.ID {
			t.Fatalf("Get child ProviderID = %d, want parent %d", e.ProviderID, created.ID)
		}
	}
	if loaded.APIKeyEntries[0].Weight == nil || *loaded.APIKeyEntries[0].Weight != positive {
		t.Fatalf("Get retained entry Weight = %v, want pointer to %d", loaded.APIKeyEntries[0].Weight, positive)
	}

	// Legacy Claude rows: a provider with NO api_key_entries must still
	// load with an empty slice (the renderer falls back to the parent's
	// api_key + proxy in this case).
	legacy, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "claude-api-key",
		Name:         "legacy",
		BaseURL:      "https://claude-legacy.example.test",
		APIKey:       "claude-legacy-parent-secret",
	})
	if err != nil {
		t.Fatalf("Create legacy: %v", err)
	}
	reloaded, err := src.Get(ctx, legacy.ID)
	if err != nil {
		t.Fatalf("Get legacy: %v", err)
	}
	if len(reloaded.APIKeyEntries) != 0 {
		t.Fatalf("legacy Claude row fabricated child entries: %+v", reloaded.APIKeyEntries)
	}
	if reloaded.APIKey != "claude-legacy-parent-secret" {
		t.Fatalf("legacy Claude row stripped parent api key: %q", reloaded.APIKey)
	}
}

// TestUpstreamProviderStoreRoutingStrategyAndEntryPriorityRoundTrip pins the
// schema contract for the pool routing strategy and the per-entry priority
// tier: upstream_providers.routing_strategy is a nullable TEXT column
// (NULL/empty = unset = today's behavior) and the entries priority column is
// a nullable INTEGER (NULL = inherit the provider row priority). Both must
// round-trip through Create/Update/Get, clearing either back to unset must
// persist, and an explicit tier 0 must stay distinct from "inherit". Like the
// other store round-trips this is gated on PGSTORE_TEST_DSN; fake redacted
// credentials are used throughout and never appear in failure messages.
func TestUpstreamProviderStoreRoutingStrategyAndEntryPriorityRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_routing")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The migration must add a nullable TEXT routing_strategy column. A NOT
	// NULL constraint would break legacy rows, and a non-NULL DEFAULT would
	// silently opt existing pools into the new failover behavior.
	var strategyType, strategyNullable, strategyDefault string
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'routing_strategy'
	`, pg.cfg.Schema, pg.cfg.UpstreamProvidersTable).Scan(&strategyType, &strategyNullable, &strategyDefault); err != nil {
		t.Fatalf("query routing_strategy column: %v", err)
	}
	if strategyType != "text" || strategyNullable != "YES" {
		t.Fatalf("routing_strategy column = %s/%s, want text/YES", strategyType, strategyNullable)
	}
	if strategyDefault != "" {
		t.Fatalf("routing_strategy column default = %q, want empty (NULL default)", strategyDefault)
	}
	// The entries priority column must be nullable INTEGER so "inherit the
	// row priority" stays distinct from an explicit tier 0.
	var prioType, prioNullable, prioDefault string
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'priority'
	`, pg.cfg.Schema, pg.cfg.UpstreamProviderEntriesTable).Scan(&prioType, &prioNullable, &prioDefault); err != nil {
		t.Fatalf("query entries priority column: %v", err)
	}
	if prioType != "integer" || prioNullable != "YES" {
		t.Fatalf("entries priority column = %s/%s, want integer/YES", prioType, prioNullable)
	}
	if prioDefault != "" {
		t.Fatalf("entries priority column default = %q, want empty (NULL default)", prioDefault)
	}

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType:    "claude-api-key",
		Name:            "pooled",
		BaseURL:         "https://claude.example.test",
		RoutingStrategy: "failover",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "routing-secret-a", Name: "alpha", Priority: intPtr(10)},
			{APIKey: "routing-secret-b", Name: "beta"}, // nil priority = inherit
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Create returned zero provider ID")
	}
	if created.RoutingStrategy != "failover" {
		t.Fatalf("created strategy = %q, want failover", created.RoutingStrategy)
	}
	firstID := created.APIKeyEntries[0].ID
	secondID := created.APIKeyEntries[1].ID
	if firstID == 0 || secondID == 0 || firstID == secondID {
		t.Fatalf("Create returned entry IDs %d and %d, want distinct positive IDs", firstID, secondID)
	}

	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded.RoutingStrategy != "failover" {
		t.Fatalf("strategy round-trip = %q, want failover", loaded.RoutingStrategy)
	}
	if len(loaded.APIKeyEntries) != 2 {
		t.Fatalf("Get returned %d entries, want 2", len(loaded.APIKeyEntries))
	}
	if loaded.APIKeyEntries[0].Priority == nil || *loaded.APIKeyEntries[0].Priority != 10 {
		t.Fatalf("entry 0 priority = %#v, want pointer to 10", loaded.APIKeyEntries[0].Priority)
	}
	if loaded.APIKeyEntries[1].Priority != nil {
		t.Fatalf("entry 1 priority = %#v, want nil (inherit)", loaded.APIKeyEntries[1].Priority)
	}

	// Clearing the strategy back to unset must round-trip, and an explicit
	// tier 0 must persist as a non-nil pointer rather than collapsing to
	// "inherit". Both are the dashboard's "back to default" and "tier 0"
	// affordances.
	_, err = src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "claude-api-key",
		Name:         "pooled",
		BaseURL:      "https://claude.example.test",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "routing-secret-a", Name: "alpha", Priority: intPtr(0)},
			{ID: secondID, APIKey: "routing-secret-b", Name: "beta"},
		},
	})
	if err != nil {
		t.Fatalf("Update clearing strategy: %v", err)
	}
	reloaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if reloaded.RoutingStrategy != "" {
		t.Fatalf("strategy after clear = %q, want empty", reloaded.RoutingStrategy)
	}
	if len(reloaded.APIKeyEntries) != 2 {
		t.Fatalf("Get after clear returned %d entries, want 2", len(reloaded.APIKeyEntries))
	}
	if reloaded.APIKeyEntries[0].Priority == nil || *reloaded.APIKeyEntries[0].Priority != 0 {
		t.Fatalf("entry 0 priority after clear = %#v, want pointer to explicit 0", reloaded.APIKeyEntries[0].Priority)
	}
	if reloaded.APIKeyEntries[1].Priority != nil {
		t.Fatalf("entry 1 priority after clear = %#v, want nil (inherit)", reloaded.APIKeyEntries[1].Priority)
	}
}

func TestUpstreamProviderStoreAPIKeyEntryDisabledRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_disabled")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The migration must add a NOT NULL disabled column defaulting to false
	// so legacy rows survive the upgrade with unchanged routing behavior
	// and no NULL-scan handling is needed.
	colType, colNullable, colDefault := "", "", ""
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'disabled'
	`, pg.cfg.Schema, pg.cfg.UpstreamProviderEntriesTable).Scan(&colType, &colNullable, &colDefault); err != nil {
		t.Fatalf("query disabled column: %v", err)
	}
	if colType != "boolean" {
		t.Fatalf("disabled column type = %q, want boolean", colType)
	}
	if colNullable != "NO" {
		t.Fatalf("disabled column nullable = %q, want NO (NOT NULL)", colNullable)
	}
	if colDefault != "false" {
		t.Fatalf("disabled column default = %q, want false", colDefault)
	}

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "openai-compatibility",
		Name:         "toggleable",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "toggle-secret-1", Name: "alpha"},
			{APIKey: "toggle-secret-2", Name: "beta", Disabled: true},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.APIKeyEntries[0].Disabled {
		t.Fatal("Create returned Disabled=true for omitted entry, want false")
	}
	if !created.APIKeyEntries[1].Disabled {
		t.Fatal("Create returned Disabled=false for entry with Disabled=true, want true")
	}

	firstID := created.APIKeyEntries[0].ID
	secondID := created.APIKeyEntries[1].ID

	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.APIKeyEntries) != 2 {
		t.Fatalf("Get returned %d entries, want 2", len(loaded.APIKeyEntries))
	}
	for _, e := range loaded.APIKeyEntries {
		want := e.ID == secondID
		if e.Disabled != want {
			t.Fatalf("loaded entry %d Disabled = %v, want %v", e.ID, e.Disabled, want)
		}
	}

	// Toggle back: the dashboard's enable path must round-trip too.
	updated, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "toggleable",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "toggle-secret-1", Name: "alpha", Disabled: true},
			{ID: secondID, APIKey: "toggle-secret-2", Name: "beta"},
		},
	})
	if err != nil {
		t.Fatalf("Update with toggled disabled: %v", err)
	}
	for _, e := range updated.APIKeyEntries {
		want := e.ID == firstID
		if e.Disabled != want {
			t.Fatalf("updated entry %d Disabled = %v, want %v", e.ID, e.Disabled, want)
		}
	}
}

func TestUpstreamProviderStoreModelWireFormatRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_model_wire_format")
	defer pg.Close()
	ensureMigrated(t, pg)

	store := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := store.Create(ctx, UpstreamProvider{
		ProviderType: "opencode-go",
		Name:         "ocgo",
		BaseURL:      "https://opencode.ai/zen/go/v1",
		Models: []UpstreamProviderModel{
			{Name: "glm-5.2"},
			{Name: "minimax-m3", WireFormat: "anthropic"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.Models) != 2 {
		t.Fatalf("Create returned %d models, want 2", len(created.Models))
	}

	loaded, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.Models) != 2 {
		t.Fatalf("Get returned %d models, want 2", len(loaded.Models))
	}
	if got := loaded.Models[0].WireFormat; got != "" && got != "openai" {
		t.Fatalf("default model WireFormat = %q, want empty/openai", got)
	}
	if got := loaded.Models[1].WireFormat; got != "anthropic" {
		t.Fatalf("anthropic model WireFormat = %q, want anthropic", got)
	}

	// Update clears the wire format back to the openai default (the store
	// normalizes empty to 'openai' on write).
	updated, err := store.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "opencode-go",
		Name:         "ocgo",
		BaseURL:      "https://opencode.ai/zen/go/v1",
		Models:       []UpstreamProviderModel{{Name: "minimax-m3"}},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Models) != 1 || (updated.Models[0].WireFormat != "" && updated.Models[0].WireFormat != "openai") {
		t.Fatalf("Update did not reset WireFormat: %+v", updated.Models)
	}
}
