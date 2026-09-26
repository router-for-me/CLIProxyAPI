package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// TestUpstreamEntryReenableExpiredAutoDisabled verifies the auto-re-enable
// sweeper's persistence primitive (ReenableExpiredAutoDisabled):
//  1. an auto-disabled entry whose provider cooldown (1s) has elapsed (entry
//     auto-disabled 2s+ ago) is re-enabled: auto_disabled=false, disabled=false,
//     reason/at nulled, and the sweep reports count=1;
//  2. a not-yet-expired entry (auto_disabled_at = now) is left untouched;
//     count=0;
//  3. a provider with cooldown=NULL (manual re-enable only) is not touched;
//     count=0;
//  4. a provider with cooldown=0 (manual re-enable only) is not touched;
//     count=0;
//  5. a genuine MANUAL-disabled entry (disabled=true, auto_disabled=false)
//     whose row also carries an expired-looking auto timestamp stays disabled —
//     the belt-and-suspenders guard never clears manual state, and its count is
//     not included in the sweep result.
//
// Gated on PGSTORE_TEST_DSN like the other store round-trips.
func TestUpstreamEntryReenableExpiredAutoDisabled(t *testing.T) {
	pg := newTestPostgresStore(t, "upstream_entry_auto_reenable")
	defer pg.Close()
	ensureMigrated(t, pg)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// newTestPostgresStore does not clean the upstream tables; the sweeper is
	// provider-agnostic (by design), so leftover auto-disabled rows from an
	// earlier run of this test would pollute the sweep counts. Deleting all
	// providers cascades to the child entries/models/headers/excluded tables.
	if _, err := pg.DB().ExecContext(ctx, `DELETE FROM `+pg.UpstreamProvidersTable()); err != nil {
		t.Fatalf("clean upstream providers: %v", err)
	}

	src := NewUpstreamProviderStore(pg)

	cooldown1 := 1
	createWithCooldown := func(prefix string, cooldown *int) (*UpstreamProvider, []int64) {
		t.Helper()
		created, err := src.Create(ctx, UpstreamProvider{
			ProviderType:               "openai-compatibility",
			Name:                       prefix,
			BaseURL:                    "https://api.example.test/v1",
			AutoDisableCooldownSeconds: cooldown,
			APIKeyEntries: []UpstreamProviderAPIKey{
				{APIKey: prefix + "-secret-a", Name: "alpha"},
				{APIKey: prefix + "-secret-b", Name: "beta"},
				{APIKey: prefix + "-secret-c", Name: "gamma"},
			},
		})
		if err != nil {
			t.Fatalf("Create(%s): %v", prefix, err)
		}
		if len(created.APIKeyEntries) != 3 {
			t.Fatalf("Create(%s) returned %d entries, want 3", prefix, len(created.APIKeyEntries))
		}
		ids := []int64{created.APIKeyEntries[0].ID, created.APIKeyEntries[1].ID, created.APIKeyEntries[2].ID}
		return created, ids
	}

	findEntry := func(providerID, entryID int64) *UpstreamProviderAPIKey {
		t.Helper()
		loaded, err := src.Get(ctx, providerID)
		if err != nil {
			t.Fatalf("Get(%d): %v", providerID, err)
		}
		for i := range loaded.APIKeyEntries {
			if loaded.APIKeyEntries[i].ID == entryID {
				return &loaded.APIKeyEntries[i]
			}
		}
		t.Fatalf("entry %d not found under provider %d", entryID, providerID)
		return nil
	}

	// --- 1. Expired auto-disabled entry (cooldown 1s, auto_disabled_at 2s+ ago)
	// is re-enabled and reported. The entry is auto-disabled directly via the
	// sink primitive, then its timestamp is back-dated past the cooldown by
	// bypassing the store (the test writes NOW() - 3s straight to the row).
	expiredProv, expiredIDs := createWithCooldown("expired", &cooldown1)
	if changed, err := src.SetEntryAutoDisabled(ctx, expiredIDs[0], "401"); err != nil || !changed {
		t.Fatalf("SetEntryAutoDisabled(expired) = changed=%v err=%v, want true/nil", changed, err)
	}
	if _, err := pg.DB().ExecContext(ctx,
		`UPDATE `+pg.UpstreamProviderEntriesTable()+` SET auto_disabled_at = NOW() - INTERVAL '3 seconds' WHERE id = $1`,
		expiredIDs[0]); err != nil {
		t.Fatalf("backdate expired entry: %v", err)
	}
	count, err := src.ReenableExpiredAutoDisabled(ctx)
	if err != nil {
		t.Fatalf("ReenableExpiredAutoDisabled: %v", err)
	}
	if count != 1 {
		t.Fatalf("ReenableExpiredAutoDisabled count = %d, want 1", count)
	}
	e := findEntry(expiredProv.ID, expiredIDs[0])
	if e.Disabled || e.AutoDisabled {
		t.Fatalf("expired entry after sweep = disabled=%v auto_disabled=%v, want false/false", e.Disabled, e.AutoDisabled)
	}
	if e.AutoDisabledAt != nil || e.AutoDisabledReason != "" {
		t.Fatalf("expired entry after sweep = at=%v reason=%q, want nil/empty", e.AutoDisabledAt, e.AutoDisabledReason)
	}

	// --- 2. Not-yet-expired (auto_disabled_at = now) stays; count=0.
	freshProv, freshIDs := createWithCooldown("fresh", &cooldown1)
	if changed, err := src.SetEntryAutoDisabled(ctx, freshIDs[0], "402"); err != nil || !changed {
		t.Fatalf("SetEntryAutoDisabled(fresh) = changed=%v err=%v, want true/nil", changed, err)
	}
	count, err = src.ReenableExpiredAutoDisabled(ctx)
	if err != nil {
		t.Fatalf("ReenableExpiredAutoDisabled (fresh): %v", err)
	}
	if count != 0 {
		t.Fatalf("ReenableExpiredAutoDisabled (fresh) count = %d, want 0", count)
	}
	e = findEntry(freshProv.ID, freshIDs[0])
	if !e.Disabled || !e.AutoDisabled {
		t.Fatalf("fresh entry after sweep = disabled=%v auto_disabled=%v, want true/true (not yet expired)", e.Disabled, e.AutoDisabled)
	}

	// --- 3. Provider cooldown NULL (manual re-enable only) → not touched.
	nullProv, nullIDs := createWithCooldown("nullcd", nil)
	if changed, err := src.SetEntryAutoDisabled(ctx, nullIDs[0], "403"); err != nil || !changed {
		t.Fatalf("SetEntryAutoDisabled(nullcd) = changed=%v err=%v, want true/nil", changed, err)
	}
	if _, err := pg.DB().ExecContext(ctx,
		`UPDATE `+pg.UpstreamProviderEntriesTable()+` SET auto_disabled_at = NOW() - INTERVAL '3 seconds' WHERE id = $1`,
		nullIDs[0]); err != nil {
		t.Fatalf("backdate nullcd entry: %v", err)
	}
	count, err = src.ReenableExpiredAutoDisabled(ctx)
	if err != nil {
		t.Fatalf("ReenableExpiredAutoDisabled (nullcd): %v", err)
	}
	if count != 0 {
		t.Fatalf("ReenableExpiredAutoDisabled (nullcd) count = %d, want 0", count)
	}
	e = findEntry(nullProv.ID, nullIDs[0])
	if !e.AutoDisabled {
		t.Fatalf("null-cooldown entry re-enabled; want still auto_disabled")
	}

	// --- 4. Provider cooldown 0 (manual re-enable only) → not touched.
	zeroCooldown := 0
	zeroProv, zeroIDs := createWithCooldown("zerocd", &zeroCooldown)
	if changed, err := src.SetEntryAutoDisabled(ctx, zeroIDs[0], "404"); err != nil || !changed {
		t.Fatalf("SetEntryAutoDisabled(zerocd) = changed=%v err=%v, want true/nil", changed, err)
	}
	if _, err := pg.DB().ExecContext(ctx,
		`UPDATE `+pg.UpstreamProviderEntriesTable()+` SET auto_disabled_at = NOW() - INTERVAL '3 seconds' WHERE id = $1`,
		zeroIDs[0]); err != nil {
		t.Fatalf("backdate zerocd entry: %v", err)
	}
	count, err = src.ReenableExpiredAutoDisabled(ctx)
	if err != nil {
		t.Fatalf("ReenableExpiredAutoDisabled (zerocd): %v", err)
	}
	if count != 0 {
		t.Fatalf("ReenableExpiredAutoDisabled (zerocd) count = %d, want 0", count)
	}
	e = findEntry(zeroProv.ID, zeroIDs[0])
	if !e.AutoDisabled {
		t.Fatalf("zero-cooldown entry re-enabled; want still auto_disabled")
	}

	// --- 5. Manual-disabled entry with an expired-looking auto timestamp
	// (disabled=true, auto_disabled=false) stays disabled and is NOT counted.
	manualProv, manualIDs := createWithCooldown("manual", &cooldown1)
	manualID := manualIDs[1]
	// Put the entry into a genuine manual-disabled row, then layer an expired
	// auto timestamp + reason onto it (e.g. a row that was auto-disabled long
	// ago and manually kept down). The sweep must not clear it.
	if _, err := src.Update(ctx, UpstreamProvider{
		ID:                         manualProv.ID,
		ProviderType:               "openai-compatibility",
		Name:                       "manual",
		BaseURL:                    "https://api.example.test/v1",
		AutoDisableCooldownSeconds: &cooldown1,
		APIKeyEntries: []UpstreamProviderAPIKey{
			{ID: manualIDs[0], APIKey: "manual-secret-a", Name: "alpha"},
			{ID: manualID, APIKey: "manual-secret-b", Name: "beta", Disabled: true},
			{ID: manualIDs[2], APIKey: "manual-secret-c", Name: "gamma"},
		},
	}); err != nil {
		t.Fatalf("Update manual disable: %v", err)
	}
	if _, err := pg.DB().ExecContext(ctx, fmt.Sprintf(
		`UPDATE %s SET auto_disabled = false, disabled = true, auto_disabled_at = NOW() - INTERVAL '3 seconds', auto_disabled_reason = 'old401' WHERE id = $1`,
		pg.UpstreamProviderEntriesTable()), manualID); err != nil {
		t.Fatalf("backdate manual entry: %v", err)
	}
	count, err = src.ReenableExpiredAutoDisabled(ctx)
	if err != nil {
		t.Fatalf("ReenableExpiredAutoDisabled (manual): %v", err)
	}
	if count != 0 {
		t.Fatalf("ReenableExpiredAutoDisabled (manual) count = %d, want 0 (manual disabled never counted)", count)
	}
	e = findEntry(manualProv.ID, manualID)
	if !e.Disabled {
		t.Fatal("manual-disabled entry was re-enabled by the sweep; want still disabled")
	}
	// The row already has auto_disabled=false (it is a genuine manual disable),
	// so it can never match the sweeper's WHERE (which requires auto_disabled=true)
	// and every column must be byte-for-byte untouched — including the stale
	// orphaned auto timestamp/reason planted onto it.
	if e.AutoDisabled {
		t.Fatal("manual-disabled entry has auto_disabled=true after sweep; want false")
	}
	if e.AutoDisabledAt == nil || !e.AutoDisabledAt.Before(time.Now().Add(-2*time.Second)) || e.AutoDisabledReason != "old401" {
		t.Fatalf("manual-disabled entry's orphaned auto metadata mutated by sweep: at=%v reason=%q", e.AutoDisabledAt, e.AutoDisabledReason)
	}
}
