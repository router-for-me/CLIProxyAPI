package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// RegisterIfAbsent registers a missing auth, but leaves a registered one alone so
// an unversioned snapshot cannot replace it or bump its registration epoch.
func TestRegisterIfAbsentLeavesRegisteredAuthUntouched(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)

	first, registered, err := manager.RegisterIfAbsent(ctx, &Auth{ID: "absent.json", Provider: "codex", Prefix: "pfx-first"})
	if err != nil || !registered || first == nil {
		t.Fatalf("RegisterIfAbsent(absent) = %v, %v, %v; want it registered", first, registered, err)
	}

	again, registered, err := manager.RegisterIfAbsent(ctx, &Auth{ID: "absent.json", Provider: "codex", Prefix: "pfx-snapshot"})
	if err != nil || registered || again != nil {
		t.Fatalf("RegisterIfAbsent(present) = %v, %v, %v; want nothing registered", again, registered, err)
	}
	current, ok := manager.GetByID("absent.json")
	if !ok || current.Prefix != "pfx-first" || current.RegistrationEpoch != first.RegistrationEpoch {
		t.Fatalf("registered auth = %+v, want pfx-first at epoch %d unchanged", current, first.RegistrationEpoch)
	}
}

// A file snapshot whose file is gone or rewritten is not registered, even though
// its ID is absent: it predates whatever removed or replaced the file.
func TestRegisterIfAbsentSkipsStaleFileSnapshot(t *testing.T) {
	for name, change := range map[string]func(path string) error{
		"file removed":   os.Remove,
		"file rewritten": func(path string) error { return os.WriteFile(path, []byte(`{"type":"codex","prefix":"new"}`), 0o600) },
	} {
		t.Run(name, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			path := filepath.Join(t.TempDir(), "stale.json")
			oldBytes := []byte(`{"type":"codex","prefix":"old"}`)
			if errWrite := os.WriteFile(path, oldBytes, 0o600); errWrite != nil {
				t.Fatalf("write: %v", errWrite)
			}
			snapshot := &Auth{ID: "stale.json", Provider: "codex", Attributes: map[string]string{AttributePath: path}}
			snapshot.BindFileSnapshot(path, oldBytes)
			if errChange := change(path); errChange != nil {
				t.Fatalf("change file: %v", errChange)
			}

			current, registered, err := m.RegisterIfAbsent(context.Background(), snapshot)
			if err != nil || registered || current != nil {
				t.Fatalf("RegisterIfAbsent() = %v, %v, %v; want nil, false, nil", current, registered, err)
			}
			if _, ok := m.GetByID("stale.json"); ok {
				t.Fatal("stale snapshot was registered")
			}
		})
	}
}
