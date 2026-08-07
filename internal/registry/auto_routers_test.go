package registry

import (
	"testing"
)

func TestAutoRouterModelInfo(t *testing.T) {
	info := AutoRouterModelInfo("router:test", "", 0)
	if info.ID != "router:test" {
		t.Fatalf("id = %q, want router:test", info.ID)
	}
	if info.DisplayName != "router:test" {
		t.Fatalf("display = %q, want fallback to id", info.DisplayName)
	}
	if info.OwnedBy != AutoRouterProvider || info.Type != AutoRouterProvider {
		t.Fatalf("owned_by/type not synthetic: %q/%q", info.OwnedBy, info.Type)
	}
	if !info.UserDefined {
		t.Fatal("expected user_defined=true so auto-syncs never clobber the row")
	}
	if info.Created <= 0 {
		t.Fatal("expected created timestamp")
	}

	named := AutoRouterModelInfo("router:test", "My Router", 0)
	if named.DisplayName != "My Router" {
		t.Fatalf("display = %q, want My Router", named.DisplayName)
	}
}

func TestRegisterUnregisterAutoRouterInRegistry(t *testing.T) {
	const id = "router:reg-test"
	// Clean up any prior state.
	UnregisterAutoRouterFromRegistry(id)
	defer UnregisterAutoRouterFromRegistry(id)

	RegisterAutoRouterInRegistry(id, "Reg Test", 0)
	if !IsAutoRouterModel(id) {
		t.Fatalf("IsAutoRouterModel(%q) = false after register", id)
	}
	available := false
	for _, m := range GetGlobalRegistry().AvailableModelIDList() {
		if m == id {
			available = true
			break
		}
	}
	if !available {
		t.Fatalf("model %q not in AvailableModelIDList after register", id)
	}

	UnregisterAutoRouterFromRegistry(id)
	if IsAutoRouterModel(id) {
		t.Fatalf("IsAutoRouterModel(%q) = true after unregister", id)
	}
}

func TestIsAutoRouterModelBlankAndUnknown(t *testing.T) {
	if IsAutoRouterModel("") {
		t.Fatal("blank id should not be an auto-router model")
	}
	if IsAutoRouterModel("definitely-not-a-router-xyz") {
		t.Fatal("unknown id should not be an auto-router model")
	}
}
