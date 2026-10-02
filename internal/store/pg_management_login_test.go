package store

import (
	"context"
	"testing"
	"time"
)

func TestManagementLoginTableAccessorsDefault(t *testing.T) {
	var s *PostgresStore
	if got, want := s.ManagementLoginSettingsTable(), `"management_login_settings"`; got != want {
		t.Fatalf("ManagementLoginSettingsTable() = %q, want %q", got, want)
	}
	if got, want := s.ManagementLoginEventsTable(), `"management_login_events"`; got != want {
		t.Fatalf("ManagementLoginEventsTable() = %q, want %q", got, want)
	}
}

func TestClampLoginSecuritySettings(t *testing.T) {
	def := DefaultLoginSecuritySettings()
	if def.MaxFailedAttempts != 5 || def.BanDurationSeconds != 1800 ||
		def.CleanupIntervalSeconds != 3600 || def.IdleTimeoutSeconds != 7200 ||
		def.RetentionDays != 30 || !def.Enabled || !def.LogSuccesses {
		t.Fatalf("unexpected defaults: %+v", def)
	}

	got := ClampLoginSecuritySettings(LoginSecuritySettings{
		MaxFailedAttempts:      0,
		BanDurationSeconds:     99999999,
		FailureWindowSeconds:   -5,
		CleanupIntervalSeconds: 1,
		IdleTimeoutSeconds:     1,
		RetentionDays:          9999,
		LogSuccesses:           false,
	})
	if got.MaxFailedAttempts != 5 {
		t.Fatalf("MaxFailedAttempts = %d, want default 5", got.MaxFailedAttempts)
	}
	if got.BanDurationSeconds != 604800 {
		t.Fatalf("BanDurationSeconds = %d, want clamp 604800", got.BanDurationSeconds)
	}
	if got.FailureWindowSeconds != 0 {
		t.Fatalf("FailureWindowSeconds = %d, want 0", got.FailureWindowSeconds)
	}
	if got.CleanupIntervalSeconds != 3600 {
		t.Fatalf("CleanupIntervalSeconds = %d, want default 3600", got.CleanupIntervalSeconds)
	}
	if got.IdleTimeoutSeconds != 7200 {
		t.Fatalf("IdleTimeoutSeconds = %d, want default 7200", got.IdleTimeoutSeconds)
	}
	if got.RetentionDays != 365 {
		t.Fatalf("RetentionDays = %d, want clamp 365", got.RetentionDays)
	}
	if got.LogSuccesses {
		t.Fatal("LogSuccesses should be preserved as false")
	}
}

func TestManagementLoginStoreNilFailOpen(t *testing.T) {
	var st *ManagementLoginStore
	got, err := st.GetLoginSettings(context.Background())
	if err != nil {
		t.Fatalf("GetLoginSettings on nil store returned error: %v", err)
	}
	if got.MaxFailedAttempts != 5 || !got.Enabled || !got.LogSuccesses {
		t.Fatalf("nil store did not return defaults: %+v", got)
	}
}

func TestManagementLoginStoreRoundTrip(t *testing.T) {
	pg := newTestPostgresStore(t, "test_management_login")
	ctx := context.Background()
	st := NewManagementLoginStore(pg)
	if st == nil {
		t.Fatal("NewManagementLoginStore returned nil")
	}
	if _, err := st.ClearLoginEvents(ctx); err != nil {
		t.Fatalf("ClearLoginEvents: %v", err)
	}

	def, err := st.GetLoginSettings(ctx)
	if err != nil {
		t.Fatalf("GetLoginSettings: %v", err)
	}
	if def.MaxFailedAttempts != 5 {
		t.Fatalf("default MaxFailedAttempts = %d, want 5", def.MaxFailedAttempts)
	}

	saved, err := st.UpsertLoginSettings(ctx, LoginSecuritySettings{
		Enabled: true, MaxFailedAttempts: 7, BanDurationSeconds: 60,
		CleanupIntervalSeconds: 120, IdleTimeoutSeconds: 300,
		LogSuccesses: false, RetentionDays: 5,
	})
	if err != nil {
		t.Fatalf("UpsertLoginSettings: %v", err)
	}
	if saved.MaxFailedAttempts != 7 || saved.LogSuccesses {
		t.Fatalf("unexpected saved settings: %+v", saved)
	}
	got, err := st.GetLoginSettings(ctx)
	if err != nil {
		t.Fatalf("GetLoginSettings after upsert: %v", err)
	}
	if got.MaxFailedAttempts != 7 || got.RetentionDays != 5 || got.LogSuccesses {
		t.Fatalf("settings did not persist: %+v", got)
	}

	now := time.Now().UTC()
	if err := st.RecordLoginEvents(ctx, []LoginEvent{
		{CreatedAt: now, IP: "1.2.3.4", Outcome: LoginOutcomeInvalidKey, Reason: "invalid management key", AttemptCount: 1},
		{CreatedAt: now, IP: "1.2.3.4", Outcome: LoginOutcomeBanStarted, AttemptCount: 0},
	}); err != nil {
		t.Fatalf("RecordLoginEvents: %v", err)
	}

	events, total, err := st.ListLoginEvents(ctx, LoginEventFilter{IP: "1.2.3.4"}, 1, 10)
	if err != nil {
		t.Fatalf("ListLoginEvents: %v", err)
	}
	if total != 2 || len(events) != 2 {
		t.Fatalf("expected 2 events, got total=%d len=%d", total, len(events))
	}
	if events[0].Outcome != LoginOutcomeBanStarted {
		t.Fatalf("newest-first order broken: first outcome = %q", events[0].Outcome)
	}

	filtered, total, err := st.ListLoginEvents(ctx, LoginEventFilter{Outcome: LoginOutcomeInvalidKey}, 1, 10)
	if err != nil {
		t.Fatalf("ListLoginEvents filtered: %v", err)
	}
	if total != 1 || len(filtered) != 1 {
		t.Fatalf("outcome filter failed: total=%d len=%d", total, len(filtered))
	}

	n, err := st.PurgeLoginEventsBefore(ctx, now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("PurgeLoginEventsBefore: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 purged, got %d", n)
	}
	n, err = st.PurgeLoginEventsBefore(ctx, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("PurgeLoginEventsBefore future: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 purged, got %d", n)
	}
}
