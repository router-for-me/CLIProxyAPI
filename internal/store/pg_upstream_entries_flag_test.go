package store

import (
	"context"
	"testing"
	"time"
)

// TestUpstreamEntryAutoDisabled verifies the server-side auto-disable sink's
// persistence primitive (SetEntryAutoDisabled):
//  1. a first fire flips the entry to disabled+auto_disabled with the matched
//     reason and a set timestamp;
//  2. a re-fire on an already-auto-disabled entry is an idempotent no-op that
//     reports changed=false and leaves the row (reason included) untouched;
//  3. an operator manually-disabled entry (disabled=true, auto_disabled=false)
//     is never clobbered — the UPDATE targets only rows that are not already
//     disabled-without-auto_disabled;
//  4. a nonexistent entry id reports changed=false with a nil error.
//
// Gated on PGSTORE_TEST_DSN like the other store round-trips.
func TestUpstreamEntryAutoDisabled(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_auto_disable")
	defer pg.Close()
	ensureMigrated(t, pg)

	src := NewUpstreamProviderStore(pg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	created, err := src.Create(ctx, UpstreamProvider{
		ProviderType: "openai-compatibility",
		Name:         "auto-disable-pool",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{APIKey: "entry-secret-a", Name: "alpha"},
			{APIKey: "entry-secret-b", Name: "beta"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(created.APIKeyEntries) != 2 {
		t.Fatalf("Create returned %d entries, want 2", len(created.APIKeyEntries))
	}
	firstID := created.APIKeyEntries[0].ID
	secondID := created.APIKeyEntries[1].ID

	// 1. First fire on a healthy entry: changed + flags + attribution set.
	changed, err := src.SetEntryAutoDisabled(ctx, firstID, "401")
	if err != nil {
		t.Fatalf("SetEntryAutoDisabled(first): %v", err)
	}
	if !changed {
		t.Fatal("SetEntryAutoDisabled(first) = changed=false, want true")
	}
	loaded, err := src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(loaded.APIKeyEntries) != 2 {
		t.Fatalf("Get returned %d entries, want 2", len(loaded.APIKeyEntries))
	}
	var first *UpstreamProviderAPIKey
	for i := range loaded.APIKeyEntries {
		if loaded.APIKeyEntries[i].ID == firstID {
			first = &loaded.APIKeyEntries[i]
		}
	}
	if first == nil {
		t.Fatal("auto-disabled entry not found in re-read")
	}
	if !first.Disabled || !first.AutoDisabled {
		t.Fatalf("auto-disabled entry = disabled=%v auto_disabled=%v, want true/true", first.Disabled, first.AutoDisabled)
	}
	if first.AutoDisabledReason != "401" {
		t.Fatalf("auto_disabled_reason = %q, want 401", first.AutoDisabledReason)
	}
	if first.AutoDisabledAt == nil || first.AutoDisabledAt.IsZero() {
		t.Fatal("auto_disabled_at not set")
	}

	// 2. Re-fire with a different code: idempotent no-op; the row (including
	// the reason from the first fire) must be unchanged.
	changed, err = src.SetEntryAutoDisabled(ctx, firstID, "402")
	if err != nil {
		t.Fatalf("SetEntryAutoDisabled(re-fire): %v", err)
	}
	if changed {
		t.Fatal("SetEntryAutoDisabled(re-fire) = changed=true, want false (idempotent)")
	}
	loaded, err = src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after re-fire: %v", err)
	}
	first = nil
	for i := range loaded.APIKeyEntries {
		if loaded.APIKeyEntries[i].ID == firstID {
			first = &loaded.APIKeyEntries[i]
		}
	}
	if first == nil {
		t.Fatal("auto-disabled entry missing after re-fire")
	}
	if first.AutoDisabledReason != "401" {
		t.Fatalf("re-fire overwrote reason = %q, want 401", first.AutoDisabledReason)
	}

	// 3. Manually-disabled entry (disabled=true, auto_disabled=false) is never
	// clobbered by the sink.
	manualID := secondID
	if _, err := src.Update(ctx, UpstreamProvider{
		ID:           created.ID,
		ProviderType: "openai-compatibility",
		Name:         "auto-disable-pool",
		BaseURL:      "https://api.example.test/v1",
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: firstID, APIKey: "entry-secret-a", Name: "alpha", Disabled: true},
			{ID: secondID, APIKey: "entry-secret-b", Name: "beta", Disabled: true},
		},
	}); err != nil {
		t.Fatalf("Update manual disable: %v", err)
	}
	changed, err = src.SetEntryAutoDisabled(ctx, manualID, "403")
	if err != nil {
		t.Fatalf("SetEntryAutoDisabled(manual): %v", err)
	}
	if changed {
		t.Fatal("SetEntryAutoDisabled(manual) = changed=true, want false (manual disable preserved)")
	}
	loaded, err = src.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after manual: %v", err)
	}
	for i := range loaded.APIKeyEntries {
		if loaded.APIKeyEntries[i].ID == manualID {
			e := &loaded.APIKeyEntries[i]
			if !e.Disabled || e.AutoDisabled || e.AutoDisabledAt != nil || e.AutoDisabledReason != "" {
				t.Fatalf("manual-disabled entry clobbered: %+v", e)
			}
		}
	}

	// 4. Nonexistent entry id: changed=false, nil error.
	changed, err = src.SetEntryAutoDisabled(ctx, 999999, "404")
	if err != nil {
		t.Fatalf("SetEntryAutoDisabled(unknown): %v", err)
	}
	if changed {
		t.Fatal("SetEntryAutoDisabled(unknown) = changed=true, want false")
	}
}
