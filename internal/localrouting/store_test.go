package localrouting

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRouteStoreRegisterListAndUnregister(t *testing.T) {
	stateDir := t.TempDir()
	store := NewRouteStore(stateDir, 1355)

	registered, errRegister := store.Register(RouteInfo{
		Name:       "app",
		Host:       "app.localhost",
		TargetHost: "127.0.0.1",
		TargetPort: 4101,
		PID:        os.Getpid(),
	}, false)
	if errRegister != nil {
		t.Fatalf("register route: %v", errRegister)
	}
	if registered.Host != "app.localhost" {
		t.Fatalf("registered host = %q", registered.Host)
	}

	routes, errList := store.List()
	if errList != nil {
		t.Fatalf("list routes: %v", errList)
	}
	if len(routes) != 1 {
		t.Fatalf("routes len = %d, want 1", len(routes))
	}

	if errUnregister := store.Unregister("app.localhost", os.Getpid()); errUnregister != nil {
		t.Fatalf("unregister route: %v", errUnregister)
	}
	routes, errList = store.List()
	if errList != nil {
		t.Fatalf("list routes after unregister: %v", errList)
	}
	if len(routes) != 0 {
		t.Fatalf("routes len after unregister = %d, want 0", len(routes))
	}
}

func TestRouteStorePrunesDeadPID(t *testing.T) {
	stateDir := t.TempDir()
	store := NewRouteStore(stateDir, 1355)
	_, errRegister := store.Register(RouteInfo{
		Name:       "dead",
		Host:       "dead.localhost",
		TargetHost: "127.0.0.1",
		TargetPort: 4102,
		PID:        999999,
	}, true)
	if errRegister != nil {
		t.Fatalf("register dead route: %v", errRegister)
	}
	routes, errList := store.List()
	if errList != nil {
		t.Fatalf("list routes: %v", errList)
	}
	if len(routes) != 0 {
		t.Fatalf("expected dead route to be pruned, got %d", len(routes))
	}
	if _, errStat := os.Stat(filepath.Join(stateDir, "routes.json")); errStat != nil {
		t.Fatalf("routes file missing: %v", errStat)
	}
}
