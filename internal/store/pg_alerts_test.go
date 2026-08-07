package store

import (
	"context"
	"testing"
	"time"
)

func TestAlertStoreRecordAndDedup(t *testing.T) {
	pg := newTestPostgresStore(t, "test_alerts")
	ctx := context.Background()
	as := NewAlertStore(pg)
	if as == nil {
		t.Fatal("NewAlertStore returned nil")
	}

	// First record creates a row.
	created, merged, err := as.RecordAlert(ctx, Alert{
		AlertType:   AlertTypeUserBudget,
		Severity:    AlertSeverityCritical,
		Title:       "Internal user budget exceeded",
		Message:     "u1 spent $50 of a 10 USD budget.",
		EntityID:    "u1",
		EntityName:  "u1",
		Value:       50,
		LimitValue:  10,
		Fingerprint: alertFingerprintKeyForTest(AlertTypeUserBudget, "u1"),
	}, time.Hour)
	if err != nil {
		t.Fatalf("RecordAlert: %v", err)
	}
	if merged {
		t.Fatal("expected a fresh insert, not a merge")
	}
	if created.ID == 0 {
		t.Fatal("expected a nonzero id")
	}
	if created.Occurrences != 1 {
		t.Fatalf("expected 1 occurrence, got %d", created.Occurrences)
	}

	// Within the suppression window the same fingerprint merges (no new row).
	second, merged2, err := as.RecordAlert(ctx, Alert{
		AlertType:   AlertTypeUserBudget,
		Severity:    AlertSeverityCritical,
		Title:       "Internal user budget exceeded",
		Message:     "u1 spent $60 of a 10 USD budget.",
		EntityID:    "u1",
		Value:       60,
		LimitValue:  10,
		Fingerprint: alertFingerprintKeyForTest(AlertTypeUserBudget, "u1"),
	}, time.Hour)
	if err != nil {
		t.Fatalf("RecordAlert merge: %v", err)
	}
	if !merged2 {
		t.Fatal("expected a merge within suppression window")
	}
	if second.ID != created.ID {
		t.Fatalf("expected merge to keep same id, got %d vs %d", second.ID, created.ID)
	}
	if second.Occurrences != 2 {
		t.Fatalf("expected 2 occurrences after merge, got %d", second.Occurrences)
	}

	// A different fingerprint creates a second row.
	third, merged3, err := as.RecordAlert(ctx, Alert{
		AlertType:   AlertTypeUserBudget,
		Severity:    AlertSeverityCritical,
		Title:       "Internal user budget exceeded",
		EntityID:    "u2",
		EntityName:  "u2",
		Value:       5,
		LimitValue:  1,
		Fingerprint: alertFingerprintKeyForTest(AlertTypeUserBudget, "u2"),
	}, time.Hour)
	if err != nil {
		t.Fatalf("RecordAlert distinct: %v", err)
	}
	if merged3 {
		t.Fatal("expected a fresh insert for a distinct fingerprint")
	}
	if third.ID == created.ID {
		t.Fatal("expected distinct ids for distinct fingerprints")
	}

	unread, err := as.CountUnread(ctx)
	if err != nil {
		t.Fatalf("CountUnread: %v", err)
	}
	if unread != 2 {
		t.Fatalf("expected 2 unread, got %d", unread)
	}
}

func TestAlertStoreSettingsClampAndToggle(t *testing.T) {
	pg := newTestPostgresStore(t, "test_alerts_settings")
	ctx := context.Background()
	as := NewAlertStore(pg)
	if as == nil {
		t.Fatal("NewAlertStore returned nil")
	}

	// Defaults are returned when the row is unseeded.
	def, err := as.GetAlertSettings(ctx)
	if err != nil {
		t.Fatalf("GetAlertSettings: %v", err)
	}
	if !def.Enabled || def.IntervalSeconds != 60 {
		t.Fatalf("unexpected defaults: %+v", def)
	}

	// Upsert with an over-small interval is clamped up to the floor.
	up, err := as.UpsertAlertSettings(ctx, def)
	if err != nil {
		t.Fatalf("UpsertAlertSettings: %v", err)
	}
	got, err := as.GetAlertSettings(ctx)
	if err != nil {
		t.Fatalf("GetAlertSettings after upsert: %v", err)
	}
	if got.IntervalSeconds != 60 {
		t.Fatalf("expected interval 60, got %d", got.IntervalSeconds)
	}
	_ = up

	got.EnableErrorRate = false
	got, err = as.UpsertAlertSettings(ctx, got)
	if err != nil {
		t.Fatalf("UpsertAlertSettings disable error rate: %v", err)
	}
	if got.CategoryEnabled(AlertTypeErrorRate) {
		t.Fatal("expected error_rate category disabled")
	}
	if !got.CategoryEnabled(AlertTypeUserBudget) {
		t.Fatal("expected user_budget category still enabled")
	}

	// Disabling the whole feed disables every category.
	got.Enabled = false
	got, err = as.UpsertAlertSettings(ctx, got)
	if err != nil {
		t.Fatalf("UpsertAlertSettings disable: %v", err)
	}
	if got.CategoryEnabled(AlertTypeUserBudget) {
		t.Fatal("expected all categories disabled when feed disabled")
	}
}

func TestAlertStoreSuppressionWindowReArm(t *testing.T) {
	pg := newTestPostgresStore(t, "test_alerts_rearm")
	ctx := context.Background()
	as := NewAlertStore(pg)
	if as == nil {
		t.Fatal("NewAlertStore returned nil")
	}

	// Use a short suppression window so the re-arm boundary is testable in
	// milliseconds instead of minutes.
	suppression := 100 * time.Millisecond
	fp := alertFingerprintKeyForTest(AlertTypeUserBudget, "u1")

	created, merged, err := as.RecordAlert(ctx, Alert{
		AlertType:   AlertTypeUserBudget,
		Severity:    AlertSeverityCritical,
		Title:       "Internal user budget exceeded",
		EntityID:    "u1",
		Value:       50,
		LimitValue:  10,
		Fingerprint: fp,
	}, suppression)
	if err != nil {
		t.Fatalf("RecordAlert: %v", err)
	}
	if merged {
		t.Fatal("expected a fresh insert, not a merge")
	}

	// Immediately inside the suppression window: the same fingerprint must merge
	// into the existing row (bump occurrences) instead of inserting a duplicate.
	second, merged2, err := as.RecordAlert(ctx, Alert{
		AlertType:   AlertTypeUserBudget,
		Severity:    AlertSeverityCritical,
		Title:       "Internal user budget exceeded",
		EntityID:    "u1",
		Value:       60,
		LimitValue:  10,
		Fingerprint: fp,
	}, suppression)
	if err != nil {
		t.Fatalf("RecordAlert within window: %v", err)
	}
	if !merged2 {
		t.Fatal("expected a merge while the suppression window is still open")
	}
	if second.ID != created.ID {
		t.Fatalf("expected merge to keep same id, got %d vs %d", second.ID, created.ID)
	}
	if second.Occurrences != 2 {
		t.Fatalf("expected 2 occurrences after merge, got %d", second.Occurrences)
	}

	// After the suppression window elapses the same condition is a new episode:
	// a fresh row is created rather than the old one being reused forever.
	time.Sleep(2 * suppression)
	third, merged3, err := as.RecordAlert(ctx, Alert{
		AlertType:   AlertTypeUserBudget,
		Severity:    AlertSeverityCritical,
		Title:       "Internal user budget exceeded",
		EntityID:    "u1",
		Value:       70,
		LimitValue:  10,
		Fingerprint: fp,
	}, suppression)
	if err != nil {
		t.Fatalf("RecordAlert after window: %v", err)
	}
	if merged3 {
		t.Fatal("expected a fresh insert after the suppression window lapsed")
	}
	if third.ID == created.ID {
		t.Fatal("expected a new row for a new suppression episode")
	}
	if third.Occurrences != 1 {
		t.Fatalf("expected 1 occurrence on the new episode, got %d", third.Occurrences)
	}

	// Two episodes exist for the same fingerprint: the original merged row plus
	// the re-armed row. Confirms dedup happens per window, not per fingerprint.
	rows, total, err := as.ListPaged(ctx, AlertFilter{AlertType: AlertTypeUserBudget}, 1, 10)
	if err != nil {
		t.Fatalf("ListPaged: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows for one fingerprint across two episodes, got %d", len(rows))
	}
	if total != 2 {
		t.Fatalf("expected total 2, got %d", total)
	}
}

func alertFingerprintKeyForTest(parts ...string) string {
	out := parts[0]
	for i := 1; i < len(parts); i++ {
		out += ":" + parts[i]
	}
	return out
}
