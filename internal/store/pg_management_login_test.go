package store

import (
	"testing"
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
