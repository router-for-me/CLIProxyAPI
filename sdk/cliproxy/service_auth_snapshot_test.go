package cliproxy

import (
	"context"
	"testing"
	"time"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type snapshotTestStore struct{}

func (snapshotTestStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }

func (snapshotTestStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	return auth.ID, nil
}

func (snapshotTestStore) Delete(context.Context, string) error { return nil }

// A watcher rescan can read an auth file just before a management PATCH saves it and
// deliver that snapshot afterwards. The stale snapshot must not replace the saved
// auth, and the PATCH's own update must still apply (Issue6559 lost prefix).
func TestPrepareCoreAuthForModelRegistration_SnapshotReadBeforeSaveDoesNotWin(t *testing.T) {
	ctx := context.Background()
	authID := "codex-snapshot-before-save.json"
	reg := internalregistry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := coreauth.NewManager(snapshotTestStore{}, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	svc := &Service{cfg: &config.Config{}, coreManager: manager}

	registered, errRegister := manager.Register(ctx, &coreauth.Auth{
		ID:       authID,
		FileName: authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Prefix:   "pfx-8",
		Metadata: map[string]any{"type": "codex", "access_token": "token", "prefix": "pfx-8"},
		Attributes: map[string]string{
			coreauth.AttributePath:          "/auths/" + authID,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	// The rescan observes the file while it still holds pfx-8. Like the file
	// synthesizer, the snapshot is unversioned and stamped with the scan time.
	snapshot := registered.Clone()
	snapshot.Generation = 0
	snapshot.UpdatedAt = time.Now()

	// The PATCH saves pfx-9 after the rescan read the file.
	patched := registered.Clone()
	patched.Prefix = "pfx-9"
	patched.Metadata = map[string]any{"type": "codex", "access_token": "token", "prefix": "pfx-9"}
	saved, errUpdate := manager.Update(ctx, patched)
	if errUpdate != nil || saved == nil {
		t.Fatalf("Update() = %v, %v", saved, errUpdate)
	}

	// The stale snapshot reaches the auth update consumer, then the PATCH's own
	// persisted update follows it.
	svc.prepareCoreAuthForModelRegistration(ctx, snapshot)
	svc.prepareCoreAuthForModelRegistration(ctx, saved)

	current, ok := manager.GetByID(authID)
	if !ok || current == nil {
		t.Fatal("auth disappeared")
	}
	if current.Prefix != "pfx-9" {
		t.Fatalf("core auth prefix = %q, want pfx-9 (stale snapshot generation=%d replaced the saved auth)", current.Prefix, current.Generation)
	}
}

// A snapshot observed after the last save is a newer file state (for example an
// external edit) and must still apply.
func TestPrepareCoreAuthForModelRegistration_SnapshotReadAfterSaveApplies(t *testing.T) {
	ctx := context.Background()
	authID := "codex-snapshot-after-save.json"
	reg := internalregistry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := coreauth.NewManager(snapshotTestStore{}, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	svc := &Service{cfg: &config.Config{}, coreManager: manager}

	registered, errRegister := manager.Register(ctx, &coreauth.Auth{
		ID:       authID,
		FileName: authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Prefix:   "pfx-1",
		Metadata: map[string]any{"type": "codex", "access_token": "token", "prefix": "pfx-1"},
		Attributes: map[string]string{
			coreauth.AttributePath:          "/auths/" + authID,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	edited := registered.Clone()
	edited.Generation = 0
	edited.Prefix = "pfx-edited"
	edited.Metadata = map[string]any{"type": "codex", "access_token": "token", "prefix": "pfx-edited"}
	// Explicitly after the save: with a coarse clock, time.Now() can equal the save
	// time, which is deliberately treated as stale.
	edited.UpdatedAt = time.Now().Add(time.Second)

	svc.prepareCoreAuthForModelRegistration(ctx, edited)

	current, ok := manager.GetByID(authID)
	if !ok || current == nil || current.Prefix != "pfx-edited" {
		t.Fatalf("core auth = %+v, want the newer file snapshot applied", current)
	}
}
