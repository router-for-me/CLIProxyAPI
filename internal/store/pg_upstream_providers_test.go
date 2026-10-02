package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func uint16PtrForRetry(v uint16) *uint16 { return &v }
func uint32PtrForRetry(v uint32) *uint32 { return &v }

// TestParsePGTextArrayLiteral exercises the TEXT[] brace-literal parser used
// to read auto_disable_error_codes through database/sql. It is a pure unit
// test: it calls the helper directly and needs no live PG.
func TestParsePGTextArrayLiteral(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "unquoted elements",
			input: `{401,insufficient_quota}`,
			want:  []string{"401", "insufficient_quota"},
		},
		{
			name:  "empty array",
			input: `{}`,
			want:  []string{},
		},
		{
			name:  "empty element",
			input: `{,b}`,
			want:  []string{"", "b"},
		},
		{
			name:  "quoted comma in element",
			input: `{"a,b","c d"}`,
			want:  []string{"a,b", "c d"},
		},
		{
			name:  "escaped quote and backslash in quoted element",
			input: "{\"quote\\\"x\",\"back\\\\slash\"}",
			want:  []string{"quote\"x", "back\\slash"},
		},
		{
			name:  "SQL NULL text is not a literal",
			input: `NULL`,
			want:  nil,
		},
		{
			name:  "empty string",
			input: ``,
			want:  nil,
		},
		{
			name:  "unbalanced open brace",
			input: `{401,insufficient_quota`,
			want:  nil,
		},
		{
			name:  "unbalanced close brace",
			input: `401,insufficient_quota}`,
			want:  nil,
		},
		{
			name:  "stray closing brace",
			input: `}`,
			want:  nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parsePGTextArrayLiteral(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("parsePGTextArrayLiteral(%q) = %#v, want %#v", tc.input, got, tc.want)
			}
		})
	}
}

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

// TestUpstreamProviderStoreCircuitBreakerRoundTrip pins the schema contract
// for the pool-level circuit breaker opt-in (design G3):
// upstream_providers.circuit_breaker is a NOT NULL BOOLEAN column defaulting
// to FALSE — legacy rows survive the migration opted out — and the flag
// round-trips through Create/Update/Get, including clearing it back to false.
// Like the other store round-trips this is gated on PGSTORE_TEST_DSN.
func TestUpstreamProviderStoreCircuitBreakerRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_circuit_breaker")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The migration must add a NOT NULL BOOLEAN circuit_breaker column
	// defaulting to FALSE so legacy rows stay opted out after the upgrade.
	var cbType, cbNullable, cbDefault string
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'circuit_breaker'
	`, pg.cfg.Schema, pg.cfg.UpstreamProvidersTable).Scan(&cbType, &cbNullable, &cbDefault); err != nil {
		t.Fatalf("query circuit_breaker column: %v", err)
	}
	if cbType != "boolean" || cbNullable != "NO" {
		t.Fatalf("circuit_breaker column = %s/%s, want boolean/NO", cbType, cbNullable)
	}
	if !strings.Contains(cbDefault, "false") {
		t.Fatalf("circuit_breaker column default = %q, want false", cbDefault)
	}

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType:   "claude-api-key",
		Name:           "breaker-pool",
		BaseURL:        "https://claude.example.test",
		CircuitBreaker: true,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "breaker-secret-a", Name: "alpha"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created.CircuitBreaker {
		t.Fatal("created circuit_breaker = false, want true")
	}

	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !loaded.CircuitBreaker {
		t.Fatal("circuit_breaker round-trip = false, want true")
	}

	// Clearing the opt-in back to default must persist.
	_, err = src.Update(ctx, UpstreamProvider{
		ID:             created.ID,
		ProviderType:   "claude-api-key",
		Name:           "breaker-pool",
		BaseURL:        "https://claude.example.test",
		CircuitBreaker: false,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: created.APIKeyEntries[0].ID, APIKey: "breaker-secret-a", Name: "alpha"},
		},
	})
	if err != nil {
		t.Fatalf("Update clearing circuit_breaker: %v", err)
	}
	reloaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if reloaded.CircuitBreaker {
		t.Fatal("circuit_breaker after clear = true, want false")
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

// TestUpstreamProviderStoreRetryColumnsRoundTrip pins the per-entry retry
// override schema: SMALLINT retry_max_attempts + INTEGER retry_max_time_ms +
// INTEGER retry_backoff_ms on upstream_provider_api_key_entries, all nullable
// (NULL = fall back to global config). Round-trips through Create/Update/Get
// and stays distinct from "inherit" (zero test exercises the null path).
// Mirrors the routing_strategy / priority round-trip test shape.
func TestUpstreamProviderStoreRetryColumnsRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_retry")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Schema contract: nullable SMALLINT retry_max_attempts + INTEGER
	// retry_max_time_ms / retry_backoff_ms, no DEFAULT so legacy rows
	// survive the upgrade with NULL (= inherit).
	expected := []struct {
		table, column, wantType, wantNullable string
	}{
		{pg.cfg.UpstreamProviderEntriesTable, "retry_max_attempts", "smallint", "YES"},
		{pg.cfg.UpstreamProviderEntriesTable, "retry_max_time_ms", "integer", "YES"},
		{pg.cfg.UpstreamProviderEntriesTable, "retry_backoff_ms", "integer", "YES"},
	}
	for _, e := range expected {
		var gotType, gotNullable, gotDefault string
		if err := pg.DB().QueryRowContext(ctx, `
			SELECT data_type, is_nullable, COALESCE(column_default, '')
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
		`, pg.cfg.Schema, e.table, e.column).Scan(&gotType, &gotNullable, &gotDefault); err != nil {
			t.Fatalf("query %s.%s column: %v", e.table, e.column, err)
		}
		if gotType != e.wantType || gotNullable != e.wantNullable {
			t.Fatalf("%s.%s = %s/%s, want %s/%s", e.table, e.column, gotType, gotNullable, e.wantType, e.wantNullable)
		}
		if gotDefault != "" {
			t.Fatalf("%s.%s default = %q, want empty (NULL default so 'inherit' survives)", e.table, e.column, gotDefault)
		}
	}

	maxAttempts := uint16(5)
	maxTime := uint32(8000)
	backoff := uint32(300)

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "openai-compatibility",
		Name:         "retry-rt",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "retry-secret-a", Name: "alpha", RetryMaxAttempts: uint16PtrForRetry(maxAttempts), RetryMaxTimeMS: uint32PtrForRetry(maxTime), RetryBackoffMS: uint32PtrForRetry(backoff)},
			{APIKey: "retry-secret-b", Name: "beta"}, // nil retry fields = inherit
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Create returned zero provider ID")
	}
	firstID := created.APIKeyEntries[0].ID

	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.APIKeyEntries) != 2 {
		t.Fatalf("Get returned %d entries, want 2", len(loaded.APIKeyEntries))
	}

	// alpha: round-trip with all three retry fields populated.
	if loaded.APIKeyEntries[0].RetryMaxAttempts == nil || *loaded.APIKeyEntries[0].RetryMaxAttempts != maxAttempts {
		t.Fatalf("entry 0 RetryMaxAttempts = %#v, want pointer to %d", loaded.APIKeyEntries[0].RetryMaxAttempts, maxAttempts)
	}
	if loaded.APIKeyEntries[0].RetryMaxTimeMS == nil || *loaded.APIKeyEntries[0].RetryMaxTimeMS != maxTime {
		t.Fatalf("entry 0 RetryMaxTimeMS = %#v, want pointer to %d", loaded.APIKeyEntries[0].RetryMaxTimeMS, maxTime)
	}
	if loaded.APIKeyEntries[0].RetryBackoffMS == nil || *loaded.APIKeyEntries[0].RetryBackoffMS != backoff {
		t.Fatalf("entry 0 RetryBackoffMS = %#v, want pointer to %d", loaded.APIKeyEntries[0].RetryBackoffMS, backoff)
	}

	// beta: nil retry fields must round-trip as nil ("fall back to global").
	if loaded.APIKeyEntries[1].RetryMaxAttempts != nil {
		t.Fatalf("entry 1 RetryMaxAttempts = %#v, want nil (inherit)", loaded.APIKeyEntries[1].RetryMaxAttempts)
	}
	if loaded.APIKeyEntries[1].RetryMaxTimeMS != nil {
		t.Fatalf("entry 1 RetryMaxTimeMS = %#v, want nil (inherit)", loaded.APIKeyEntries[1].RetryMaxTimeMS)
	}
	if loaded.APIKeyEntries[1].RetryBackoffMS != nil {
		t.Fatalf("entry 1 RetryBackoffMS = %#v, want nil (inherit)", loaded.APIKeyEntries[1].RetryBackoffMS)
	}

	// Clearing the retry fields back to nil must round-trip; explicit 0
	// stays distinct from "inherit" (just like the priority column).
	zeroAttempts := uint16(0)
	updated, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "retry-secret-a", Name: "alpha", RetryMaxAttempts: uint16PtrForRetry(zeroAttempts)},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.APIKeyEntries[0].RetryMaxAttempts == nil || *updated.APIKeyEntries[0].RetryMaxAttempts != 0 {
		t.Fatalf("explicit 0 should round-trip as pointer-to-0, got %#v", updated.APIKeyEntries[0].RetryMaxAttempts)
	}
	reloaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if reloaded.APIKeyEntries[0].RetryMaxAttempts == nil || *reloaded.APIKeyEntries[0].RetryMaxAttempts != 0 {
		t.Fatalf("after reload: explicit 0 collapsed to nil (inherit) — schema does not distinguish explicit zero from unset")
	}
	if reloaded.APIKeyEntries[0].RetryMaxTimeMS != nil || reloaded.APIKeyEntries[0].RetryBackoffMS != nil {
		t.Fatalf("after reload: cleared retry_max_time_ms/backoff should be nil, got max_time_ms=%#v backoff=%#v", reloaded.APIKeyEntries[0].RetryMaxTimeMS, reloaded.APIKeyEntries[0].RetryBackoffMS)
	}
}

// TestUpstreamProviderStoreMaxConcurrentWaitRoundTrip pins the per-entry Max
// Concurrent feature schema: nullable INTEGER max_concurrent + max_wait_ms on
// upstream_provider_api_key_entries (NULL = unlimited / default wait budget).
// Verifies the values round-trip through Create/Get/Update (entry ID retained)
// and that omitted entries stay nil (feature off) distinct from an explicit
// cap. Mirrors the retry-columns round-trip test shape.
func TestUpstreamProviderStoreMaxConcurrentWaitRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_max_concurrent")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Schema contract: nullable INTEGER max_concurrent/max_wait_ms with no
	// DEFAULT so legacy rows survive the upgrade as unlimited/default-wait
	// (feature off) and "user did not set a cap" stays distinct from "cap 0".
	expected := []struct {
		table, column, wantType, wantNullable string
	}{
		{pg.cfg.UpstreamProviderEntriesTable, "max_concurrent", "integer", "YES"},
		{pg.cfg.UpstreamProviderEntriesTable, "max_wait_ms", "integer", "YES"},
	}
	for _, e := range expected {
		var gotType, gotNullable, gotDefault string
		if err := pg.DB().QueryRowContext(ctx, `
			SELECT data_type, is_nullable, COALESCE(column_default, '')
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
		`, pg.cfg.Schema, e.table, e.column).Scan(&gotType, &gotNullable, &gotDefault); err != nil {
			t.Fatalf("query %s.%s column: %v", e.table, e.column, err)
		}
		if gotType != e.wantType || gotNullable != e.wantNullable {
			t.Fatalf("%s.%s = %s/%s, want %s/%s", e.table, e.column, gotType, gotNullable, e.wantType, e.wantNullable)
		}
		if gotDefault != "" {
			t.Fatalf("%s.%s default = %q, want empty (NULL default so 'unlimited' survives)", e.table, e.column, gotDefault)
		}
	}

	maxConcurrent := 3
	maxWait := 1500
	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "openai-compatibility",
		Name:         "max-concurrent-rt",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "mc-secret-a", Name: "alpha", MaxConcurrent: &maxConcurrent, MaxWaitMs: &maxWait},
			{APIKey: "mc-secret-b", Name: "beta"}, // nil = unlimited / default wait
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Create returned zero provider ID")
	}
	firstID := created.APIKeyEntries[0].ID

	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.APIKeyEntries) != 2 {
		t.Fatalf("Get returned %d entries, want 2", len(loaded.APIKeyEntries))
	}
	if loaded.APIKeyEntries[0].MaxConcurrent == nil || *loaded.APIKeyEntries[0].MaxConcurrent != maxConcurrent {
		t.Fatalf("entry 0 MaxConcurrent = %#v, want pointer to %d", loaded.APIKeyEntries[0].MaxConcurrent, maxConcurrent)
	}
	if loaded.APIKeyEntries[0].MaxWaitMs == nil || *loaded.APIKeyEntries[0].MaxWaitMs != maxWait {
		t.Fatalf("entry 0 MaxWaitMs = %#v, want pointer to %d", loaded.APIKeyEntries[0].MaxWaitMs, maxWait)
	}
	if loaded.APIKeyEntries[1].MaxConcurrent != nil || loaded.APIKeyEntries[1].MaxWaitMs != nil {
		t.Fatalf("entry 1 (nil) MaxConcurrent/MaxWaitMs = %#v/%#v, want nil", loaded.APIKeyEntries[1].MaxConcurrent, loaded.APIKeyEntries[1].MaxWaitMs)
	}

	// Update round-trips the values with the entry ID retained and unchanged
	// siblings deleted (mirrors the retry test's replace-children behavior).
	newMax := 5
	newWait := 2500
	updated, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "max-concurrent-rt",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "mc-secret-a", Name: "alpha", MaxConcurrent: &newMax, MaxWaitMs: &newWait},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.APIKeyEntries) != 1 || updated.APIKeyEntries[0].ID != firstID {
		t.Fatalf("Update returned entries %#v, want single entry with retained ID %d", updated.APIKeyEntries, firstID)
	}
	if updated.APIKeyEntries[0].MaxConcurrent == nil || *updated.APIKeyEntries[0].MaxConcurrent != newMax {
		t.Fatalf("updated MaxConcurrent = %#v, want pointer to %d", updated.APIKeyEntries[0].MaxConcurrent, newMax)
	}
	if updated.APIKeyEntries[0].MaxWaitMs == nil || *updated.APIKeyEntries[0].MaxWaitMs != newWait {
		t.Fatalf("updated MaxWaitMs = %#v, want pointer to %d", updated.APIKeyEntries[0].MaxWaitMs, newWait)
	}
	reloaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	if len(reloaded.APIKeyEntries) != 1 {
		t.Fatalf("Get after Update returned %d entries, want 1", len(reloaded.APIKeyEntries))
	}
	if reloaded.APIKeyEntries[0].MaxConcurrent == nil || *reloaded.APIKeyEntries[0].MaxConcurrent != newMax {
		t.Fatalf("after reload MaxConcurrent = %#v, want pointer to %d", reloaded.APIKeyEntries[0].MaxConcurrent, newMax)
	}
	if reloaded.APIKeyEntries[0].MaxWaitMs == nil || *reloaded.APIKeyEntries[0].MaxWaitMs != newWait {
		t.Fatalf("after reload MaxWaitMs = %#v, want pointer to %d", reloaded.APIKeyEntries[0].MaxWaitMs, newWait)
	}

	// Explicit cap 0 must survive write -> re-read as 0 (not normalized to
	// nil): nullableInt binds a non-nil &0 as the integer 0, so "cap 0" stays
	// distinct from NULL = unlimited.
	zero := 0
	zeroCap, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "openai-compatibility",
		Name:         "max-concurrent-zero",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "mc-zero-secret", Name: "alpha", MaxConcurrent: &zero},
		},
	})
	if err != nil {
		t.Fatalf("Create zero cap: %v", err)
	}
	zeroLoaded, err := src.Get(ctx, zeroCap.ID)
	if err != nil {
		t.Fatalf("Get zero cap: %v", err)
	}
	if len(zeroLoaded.APIKeyEntries) != 1 {
		t.Fatalf("Get zero cap returned %d entries, want 1", len(zeroLoaded.APIKeyEntries))
	}
	if zeroLoaded.APIKeyEntries[0].MaxConcurrent == nil || *zeroLoaded.APIKeyEntries[0].MaxConcurrent != 0 {
		t.Fatalf("explicit MaxConcurrent 0 re-read as %#v, want pointer to 0", zeroLoaded.APIKeyEntries[0].MaxConcurrent)
	}
}

// TestUpstreamProviderStoreAutoDisableColumnsRoundTrip pins the Auto-Disable
// feature schema: nullable TEXT[] auto_disable_error_codes + nullable INTEGER
// auto_disable_cooldown_seconds on the provider row, and the runtime-written
// entry flags (auto_disabled BOOLEAN NOT NULL DEFAULT FALSE, auto_disabled_at
// TIMESTAMPTZ, auto_disabled_reason TEXT). Verifies full Create/Get/Update
// round-trips (entry IDs retained), that Migrate is idempotent for the new
// columns, and — per plan decision #7 (re-enable = dashboard PUT) — that an
// operator PUT carrying auto_disabled=false on an entry CLEARS its runtime
// auto-disable flags (auto_disabled always written; false/nil/empty clears),
// while the positive runtime write still lands.
func TestUpstreamProviderStoreAutoDisableColumnsRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_auto_disable")
	defer pg.Close()
	ensureMigrated(t, pg)
	// Migrate must be idempotent: a second run is a no-op and the new columns
	// still exist (mirrors TestMigrateIdempotent).
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Schema contract for the auto-disable columns.
	expected := []struct {
		table, column, wantType, wantNullable, wantDefault string
	}{
		{pg.cfg.UpstreamProviderEntriesTable, "auto_disabled", "boolean", "NO", "false"},
		{pg.cfg.UpstreamProviderEntriesTable, "auto_disabled_at", "timestamp with time zone", "YES", ""},
		{pg.cfg.UpstreamProviderEntriesTable, "auto_disabled_reason", "text", "YES", ""},
		{pg.cfg.UpstreamProvidersTable, "auto_disable_error_codes", "ARRAY", "YES", ""},
		{pg.cfg.UpstreamProvidersTable, "auto_disable_cooldown_seconds", "integer", "YES", ""},
	}
	for _, e := range expected {
		var gotType, gotNullable, gotDefault string
		if err := pg.DB().QueryRowContext(ctx, `
			SELECT data_type, is_nullable, COALESCE(column_default, '')
			FROM information_schema.columns
			WHERE table_schema = $1 AND table_name = $2 AND column_name = $3
		`, pg.cfg.Schema, e.table, e.column).Scan(&gotType, &gotNullable, &gotDefault); err != nil {
			t.Fatalf("query %s.%s column: %v", e.table, e.column, err)
		}
		if gotType != e.wantType || gotNullable != e.wantNullable || gotDefault != e.wantDefault {
			t.Fatalf("%s.%s = %s/%s default %q, want %s/%s default %q", e.table, e.column,
				gotType, gotNullable, gotDefault, e.wantType, e.wantNullable, e.wantDefault)
		}
	}
	// auto_disable_error_codes must be a genuine TEXT[] (udt _text), not JSONB.
	var udt string
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT udt_name FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'auto_disable_error_codes'
	`, pg.cfg.Schema, pg.cfg.UpstreamProvidersTable).Scan(&udt); err != nil {
		t.Fatalf("query auto_disable_error_codes udt: %v", err)
	}
	if udt != "_text" {
		t.Fatalf("auto_disable_error_codes udt = %q, want _text (TEXT[])", udt)
	}

	codes := []string{"401", "account_suspended"}
	cooldown := 3600
	disabledAt := time.Now().UTC().Truncate(time.Second)
	disabledReason := "matched 401 upstream"

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType:               "openai-compatibility",
		Name:                       "auto-disable-rt",
		AutoDisableErrorCodes:      codes,
		AutoDisableCooldownSeconds: &cooldown,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{
				APIKey:             "ad-secret-a",
				Name:               "alpha",
				AutoDisabled:       true,
				AutoDisabledAt:     &disabledAt,
				AutoDisabledReason: disabledReason,
			},
			{APIKey: "ad-secret-b", Name: "beta"}, // pristine: not auto-disabled
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("Create returned zero provider ID")
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
	// Provider-level config round-trips through Get.
	if len(loaded.AutoDisableErrorCodes) != len(codes) {
		t.Fatalf("Get AutoDisableErrorCodes = %#v, want %#v", loaded.AutoDisableErrorCodes, codes)
	}
	for i, c := range codes {
		if loaded.AutoDisableErrorCodes[i] != c {
			t.Fatalf("Get AutoDisableErrorCodes[%d] = %q, want %q", i, loaded.AutoDisableErrorCodes[i], c)
		}
	}
	if loaded.AutoDisableCooldownSeconds == nil || *loaded.AutoDisableCooldownSeconds != cooldown {
		t.Fatalf("Get AutoDisableCooldownSeconds = %#v, want pointer to %d", loaded.AutoDisableCooldownSeconds, cooldown)
	}
	// Entry A: runtime flags round-trip.
	entryA := loaded.APIKeyEntries[0]
	if !entryA.AutoDisabled {
		t.Fatalf("entry A AutoDisabled = false, want true")
	}
	if entryA.AutoDisabledAt == nil || !entryA.AutoDisabledAt.Equal(disabledAt) {
		t.Fatalf("entry A AutoDisabledAt = %#v, want %s", entryA.AutoDisabledAt, disabledAt)
	}
	if entryA.AutoDisabledReason != disabledReason {
		t.Fatalf("entry A AutoDisabledReason = %q, want %q", entryA.AutoDisabledReason, disabledReason)
	}
	// Entry B: pristine, not auto-disabled.
	if loaded.APIKeyEntries[1].AutoDisabled {
		t.Fatalf("entry B AutoDisabled = true, want false")
	}
	if loaded.APIKeyEntries[1].AutoDisabledAt != nil || loaded.APIKeyEntries[1].AutoDisabledReason != "" {
		t.Fatalf("entry B runtime flags = at %#v reason %q, want nil/empty", loaded.APIKeyEntries[1].AutoDisabledAt, loaded.APIKeyEntries[1].AutoDisabledReason)
	}

	// Update round-trips the provider codes/cooldown and the entry flags, with
	// entry IDs retained. Entry A is the Re-enable path (plan decision #7: the
	// dashboard PUTs auto_disabled=false + empty at/reason to bring a
	// server-auto-disabled entry back): its runtime flags must be CLEARED by
	// the operator PUT. Entry B is explicitly auto-disabled in the same PUT:
	// the positive write must land. This is the new contract — a zero-false
	// auto_disabled on an operator PUT now means "clear the runtime flag"
	// (which is what Re-enable needs), replacing the old preserve-on-false
	// behavior.
	secondDisabledAt := time.Now().UTC().Truncate(time.Second)
	newMax := 3
	newCodes := []string{"429", "upstream_error"}
	newCooldown := 7200
	updated, err := src.Update(ctx, UpstreamProvider{
		ID:                         created.ID,
		ProviderType:               "openai-compatibility",
		Name:                       "auto-disable-rt",
		AutoDisableErrorCodes:      newCodes,
		AutoDisableCooldownSeconds: &newCooldown,
		APIKeyEntries: []UpstreamProviderAPIKey{
			// Re-enable PUT: max_concurrent changed AND the runtime auto flags
			// explicitly cleared (auto_disabled=false, nil at, empty reason).
			{ID: firstID, APIKey: "ad-secret-a", Name: "alpha", MaxConcurrent: &newMax,
				AutoDisabled: false},
			// Positive runtime write: auto-disable entry B.
			{ID: secondID, APIKey: "ad-secret-b", Name: "beta",
				AutoDisabled: true, AutoDisabledAt: &secondDisabledAt, AutoDisabledReason: "matched 429 upstream"},
		},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.APIKeyEntries) != 2 {
		t.Fatalf("Update returned %d entries, want 2", len(updated.APIKeyEntries))
	}

	reloaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after Update: %v", err)
	}
	// Provider codes/cooldown updated.
	if len(reloaded.AutoDisableErrorCodes) != len(newCodes) || reloaded.AutoDisableErrorCodes[0] != newCodes[0] || reloaded.AutoDisableErrorCodes[1] != newCodes[1] {
		t.Fatalf("Get after Update AutoDisableErrorCodes = %#v, want %#v", reloaded.AutoDisableErrorCodes, newCodes)
	}
	if reloaded.AutoDisableCooldownSeconds == nil || *reloaded.AutoDisableCooldownSeconds != newCooldown {
		t.Fatalf("Get after Update AutoDisableCooldownSeconds = %#v, want pointer to %d", reloaded.AutoDisableCooldownSeconds, newCooldown)
	}
	var gotA, gotB UpstreamProviderAPIKey
	for i := range reloaded.APIKeyEntries {
		switch reloaded.APIKeyEntries[i].ID {
		case firstID:
			gotA = reloaded.APIKeyEntries[i]
		case secondID:
			gotB = reloaded.APIKeyEntries[i]
		}
	}
	if gotA.ID == 0 {
		t.Fatalf("entry A lost: %#v", reloaded.APIKeyEntries)
	}
	if gotB.ID == 0 {
		t.Fatalf("entry B lost: %#v", reloaded.APIKeyEntries)
	}
	// Entry A: re-enable PUT cleared the runtime flags, and its operator-editable
	// max_concurrent still written.
	if gotA.AutoDisabled {
		t.Fatalf("entry A AutoDisabled still true after re-enable PUT; want cleared false")
	}
	if gotA.AutoDisabledAt != nil {
		t.Fatalf("entry A AutoDisabledAt = %#v after re-enable PUT; want nil (cleared)", gotA.AutoDisabledAt)
	}
	if gotA.AutoDisabledReason != "" {
		t.Fatalf("entry A AutoDisabledReason = %q after re-enable PUT; want empty (cleared)", gotA.AutoDisabledReason)
	}
	if gotA.MaxConcurrent == nil || *gotA.MaxConcurrent != newMax {
		t.Fatalf("entry A MaxConcurrent = %#v, want pointer to %d", gotA.MaxConcurrent, newMax)
	}
	// Entry B: positive auto-disable write landed.
	if !gotB.AutoDisabled {
		t.Fatalf("entry B AutoDisabled = false, want true (positive write)")
	}
	if gotB.AutoDisabledAt == nil || !gotB.AutoDisabledAt.Equal(secondDisabledAt) {
		t.Fatalf("entry B AutoDisabledAt = %#v, want %s", gotB.AutoDisabledAt, secondDisabledAt)
	}
	if gotB.AutoDisabledReason != "matched 429 upstream" {
		t.Fatalf("entry B AutoDisabledReason = %q, want %q", gotB.AutoDisabledReason, "matched 429 upstream")
	}

	// Empty-but-present codes round-trip as an empty non-nil slice: '{}' must
	// stay distinct from NULL at the SQL layer (nil slice = feature off, empty
	// slice = feature on with no codes yet). marshalTextArray writes '{}' for
	// an empty non-nil slice and SQL NULL for nil, so the re-read proves the
	// distinction survives both the write and the TEXT[] scan.
	emptyCodes, err := src.Create(ctx, UpstreamProvider{
		ProviderType:          "openai-compatibility",
		Name:                  "auto-disable-empty-codes",
		AutoDisableErrorCodes: []string{},
	})
	if err != nil {
		t.Fatalf("Create empty codes: %v", err)
	}
	emptyLoaded, err := src.Get(ctx, emptyCodes.ID)
	if err != nil {
		t.Fatalf("Get empty codes: %v", err)
	}
	if emptyLoaded.AutoDisableErrorCodes == nil {
		t.Fatal("empty []string{} AutoDisableErrorCodes re-read as nil — '{}' collapsed to NULL")
	}
	if len(emptyLoaded.AutoDisableErrorCodes) != 0 {
		t.Fatalf("empty codes re-read = %#v, want empty slice", emptyLoaded.AutoDisableErrorCodes)
	}
}

// TestUpstreamProviderStoreAutoDisableProviderCoalesce pins the Update-path
// preserve/clear contract for the provider-level auto-disable config (the
// reviewer-flagged defect: a sparse/legacy PUT must not wipe codes/cooldown,
// while an explicit clear must). The DTO pointer (nil=absent) + COALESCE on
// the Update SQL mean:
//
//   - codes/cooldown nil on the store struct → binds NULL → COALESCE preserves
//   - codes empty-but-present ([]string{}) → marshalTextArray binds '{}' →
//     COALESCE passes it through → clears; cooldown 0 → binds 0 → clears
//
// This is exactly the dashboard round-trip: a save carries the current codes
// (non-nil), and an operator clearing every code sends []. A legacy PUT omits
// the keys and must be a no-op.
func TestUpstreamProviderStoreAutoDisableProviderCoalesce(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_auto_disable_provider_coalesce")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	codes := []string{"401", "403"}
	cooldown := 3600
	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType:               "openai-compatibility",
		Name:                       "coalesce-rt",
		AutoDisableErrorCodes:      codes,
		AutoDisableCooldownSeconds: &cooldown,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "coalesce-secret", Name: "alpha"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	entryID := created.APIKeyEntries[0].ID

	// Sparse PUT (legacy/non-dashboard): provider-level codes/cooldown omitted
	// (nil on the store struct — the DTO pointer is nil), but entries present.
	// The stored config must be PRESERVED, not wiped.
	sparse, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "coalesce-rt",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: entryID, APIKey: "coalesce-secret", Name: "alpha"},
		},
	})
	if err != nil {
		t.Fatalf("sparse Update: %v", err)
	}
	if sparse.AutoDisableErrorCodes == nil || len(sparse.AutoDisableErrorCodes) != len(codes) ||
		sparse.AutoDisableErrorCodes[0] != codes[0] || sparse.AutoDisableErrorCodes[1] != codes[1] {
		t.Fatalf("sparse Update wiped codes: got %#v, want %#v preserved", sparse.AutoDisableErrorCodes, codes)
	}
	if sparse.AutoDisableCooldownSeconds == nil || *sparse.AutoDisableCooldownSeconds != cooldown {
		t.Fatalf("sparse Update wiped cooldown: got %#v, want pointer to %d preserved", sparse.AutoDisableCooldownSeconds, cooldown)
	}

	// Explicit clear: PUT carrying an empty-but-present codes slice + cooldown 0
	// (the operator removed every code / set 0) must CLEAR the stored config.
	cleared, err := src.Update(ctx, UpstreamProvider{
		ID:                         created.ID,
		ProviderType:               "openai-compatibility",
		Name:                       "coalesce-rt",
		AutoDisableErrorCodes:      []string{},
		AutoDisableCooldownSeconds: intPtr(0),
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: entryID, APIKey: "coalesce-secret", Name: "alpha"},
		},
	})
	if err != nil {
		t.Fatalf("clear Update: %v", err)
	}
	if cleared.AutoDisableErrorCodes == nil || len(cleared.AutoDisableErrorCodes) != 0 {
		t.Fatalf("explicit [] did not clear codes: got %#v, want non-nil empty", cleared.AutoDisableErrorCodes)
	}
	if cleared.AutoDisableCooldownSeconds == nil || *cleared.AutoDisableCooldownSeconds != 0 {
		t.Fatalf("explicit 0 did not clear cooldown: got %#v, want pointer to 0", cleared.AutoDisableCooldownSeconds)
	}

	// Re-read from the DB to prove the clear actually persisted (the Update
	// RETURNING row already reflects it, but the raw scan is the authoritative
	// check that '{}'/0 landed, not a COALESCE-masked projection).
	reloaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if reloaded.AutoDisableErrorCodes == nil || len(reloaded.AutoDisableErrorCodes) != 0 {
		t.Fatalf("Get after clear codes = %#v, want non-nil empty", reloaded.AutoDisableErrorCodes)
	}
	if reloaded.AutoDisableCooldownSeconds == nil || *reloaded.AutoDisableCooldownSeconds != 0 {
		t.Fatalf("Get after clear cooldown = %#v, want pointer to 0", reloaded.AutoDisableCooldownSeconds)
	}
}

// TestUpstreamProviderStoreRequestBodiesRoundTrip pins the schema contract for
// the per-provider request/response capture privacy toggle:
// upstream_providers.store_request_bodies is a NOT NULL BOOLEAN column
// defaulting to FALSE — legacy rows survive the migration opted out — and the
// flag round-trips through Create/Update/Get, including clearing it back to
// false. Like the other store round-trips this is gated on PGSTORE_TEST_DSN.
func TestUpstreamProviderStoreRequestBodiesRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_store_request_bodies")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// The migration must add a NOT NULL BOOLEAN store_request_bodies column
	// defaulting to FALSE so legacy rows stay opted out after the upgrade.
	var colType, colNullable, colDefault string
	if err := pg.DB().QueryRowContext(ctx, `
		SELECT data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2 AND column_name = 'store_request_bodies'
	`, pg.cfg.Schema, pg.cfg.UpstreamProvidersTable).Scan(&colType, &colNullable, &colDefault); err != nil {
		t.Fatalf("query store_request_bodies column: %v", err)
	}
	if colType != "boolean" || colNullable != "NO" {
		t.Fatalf("store_request_bodies column = %s/%s, want boolean/NO", colType, colNullable)
	}
	if !strings.Contains(colDefault, "false") {
		t.Fatalf("store_request_bodies column default = %q, want false", colDefault)
	}

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType:       "claude-api-key",
		Name:               "privacy-on",
		BaseURL:            "https://claude.example.test",
		StoreRequestBodies: true,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "privacy-secret-a", Name: "alpha"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !created.StoreRequestBodies {
		t.Fatal("created store_request_bodies = false, want true")
	}

	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !loaded.StoreRequestBodies {
		t.Fatal("store_request_bodies round-trip = false, want true")
	}

	// Clearing the opt-in back to default must persist.
	_, err = src.Update(ctx, UpstreamProvider{
		ID:                 created.ID,
		ProviderType:       "claude-api-key",
		Name:               "privacy-on",
		BaseURL:            "https://claude.example.test",
		StoreRequestBodies: false,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: created.APIKeyEntries[0].ID, APIKey: "privacy-secret-a", Name: "alpha"},
		},
	})
	if err != nil {
		t.Fatalf("Update clearing store_request_bodies: %v", err)
	}
	reloaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after clear: %v", err)
	}
	if reloaded.StoreRequestBodies {
		t.Fatal("store_request_bodies after clear = true, want false")
	}
}
