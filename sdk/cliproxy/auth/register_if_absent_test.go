package auth

import (
	"context"
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
