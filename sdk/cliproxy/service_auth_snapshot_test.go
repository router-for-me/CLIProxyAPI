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

// A dropped snapshot may hold an external edit made after the save, which the
// watcher would then skip as unchanged; the Service asks the watcher to re-read
// that file.
func TestPrepareCoreAuthForModelRegistration_DroppedSnapshotRereadsFile(t *testing.T) {
	ctx := context.Background()
	authID := "codex-snapshot-dropped-reread.json"
	authPath := "/auths/" + authID
	reg := internalregistry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := coreauth.NewManager(snapshotTestStore{}, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	reread := make(chan string, 1)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	svc.setWatcher(&WatcherWrapper{rereadAuthFile: func(path string) { reread <- path }})

	registered, errRegister := manager.Register(ctx, &coreauth.Auth{
		ID:       authID,
		FileName: authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Prefix:   "pfx-1",
		Metadata: map[string]any{"type": "codex", "access_token": "token", "prefix": "pfx-1"},
		Attributes: map[string]string{
			coreauth.AttributePath:          authPath,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	// Observed explicitly before the save Register just made.
	dropped := registered.Clone()
	dropped.Generation = 0
	dropped.Prefix = "pfx-edited"
	dropped.Metadata = map[string]any{"type": "codex", "access_token": "token", "prefix": "pfx-edited"}
	dropped.UpdatedAt = time.Now().Add(-time.Second)

	svc.prepareCoreAuthForModelRegistration(ctx, dropped)

	if current, ok := manager.GetByID(authID); !ok || current == nil || current.Prefix != "pfx-1" {
		t.Fatalf("core auth = %+v, want the snapshot dropped", current)
	}
	hangGuard := time.NewTimer(5 * time.Second)
	defer hangGuard.Stop()
	select {
	case path := <-reread:
		if path != authPath {
			t.Fatalf("re-read path = %q, want %q", path, authPath)
		}
	case <-hangGuard.C:
		t.Fatal("dropped snapshot did not ask the watcher to re-read its file")
	}
}

// A watcher snapshot of a new auth can find it absent, and an upload can register
// and save the same auth before the snapshot is registered. The snapshot must not
// replace the upload, and the upload's own (versioned) update must still apply.
func TestPrepareCoreAuthForModelRegistration_SnapshotDoesNotReplaceConcurrentRegistration(t *testing.T) {
	ctx := context.Background()
	authID := "codex-snapshot-vs-upload.json"
	reg := internalregistry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := coreauth.NewManager(snapshotTestStore{}, nil, nil)
	manager.RegisterExecutor(&syncTestExecutor{})
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	newAuth := func(prefix string) *coreauth.Auth {
		return &coreauth.Auth{
			ID:       authID,
			FileName: authID,
			Provider: "codex",
			Status:   coreauth.StatusActive,
			Prefix:   prefix,
			Metadata: map[string]any{"type": "codex", "access_token": "token", "prefix": prefix},
			Attributes: map[string]string{
				coreauth.AttributePath:          "/auths/" + authID,
				coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			},
		}
	}

	// The watcher read the old file before the upload replaced it.
	snapshot := newAuth("pfx-old")
	snapshot.UpdatedAt = time.Now().Add(-time.Second)

	var uploaded *coreauth.Auth
	original := beforeSnapshotRegister
	t.Cleanup(func() { beforeSnapshotRegister = original })
	beforeSnapshotRegister = func(string) {
		var err error
		if uploaded, err = manager.Register(ctx, newAuth("pfx-new")); err != nil {
			t.Fatalf("upload Register() error = %v", err)
		}
	}

	svc.prepareCoreAuthForModelRegistration(ctx, snapshot)
	beforeSnapshotRegister = original
	if current, ok := manager.GetByID(authID); !ok || current.Prefix != "pfx-new" {
		t.Fatalf("core auth = %+v, want the upload's pfx-new kept", current)
	}

	// The upload's post-persist hook arrives afterwards and must not look stale.
	svc.prepareCoreAuthForModelRegistration(ctx, uploaded)
	current, ok := manager.GetByID(authID)
	if !ok || current.Prefix != "pfx-new" || current.RegistrationEpoch != uploaded.RegistrationEpoch {
		t.Fatalf("core auth = %+v, want pfx-new at the upload's registration epoch %d", current, uploaded.RegistrationEpoch)
	}
}
