package store

import "testing"

func TestManagementLoginBackupResourceMapped(t *testing.T) {
	if !IsBackupDataResource(ResourceManagementLogin) {
		t.Fatal("ResourceManagementLogin should be a chunked data resource")
	}
	s := &PostgresStore{cfg: PostgresStoreConfig{
		ManagementLoginSettingsTable: "management_login_settings",
		ManagementLoginEventsTable:   "management_login_events",
	}}
	tables := s.resourceTables(ResourceManagementLogin)
	if len(tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(tables))
	}
	found := map[string]bool{}
	for _, tbl := range tables {
		found[tbl.name] = true
	}
	if !found[`"management_login_settings"`] || !found[`"management_login_events"`] {
		t.Fatalf("unexpected table mapping: %+v", tables)
	}
	foundList := false
	for _, r := range AllBackupResources {
		if r == ResourceManagementLogin {
			foundList = true
			break
		}
	}
	if !foundList {
		t.Fatal("ResourceManagementLogin missing from AllBackupResources")
	}
}
