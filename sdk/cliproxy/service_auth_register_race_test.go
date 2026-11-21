package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// A watcher snapshot of a new auth can find it absent, and an upload can register
// and save the same auth before the snapshot is registered. The snapshot must not
// replace the upload, and the upload's own (versioned) update must still apply.
func TestPrepareCoreAuthForModelRegistration_SnapshotDoesNotReplaceConcurrentRegistration(t *testing.T) {
	ctx := context.Background()
	authID := "codex-snapshot-vs-upload.json"
	reg := internalregistry.GetGlobalRegistry()
	reg.UnregisterClient(authID)
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	path := filepath.Join(t.TempDir(), authID)
	fileBytes := func(prefix string) []byte {
		return []byte(`{"type":"codex","access_token":"token","prefix":"` + prefix + `"}`)
	}

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
				coreauth.AttributePath:          path,
				coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
			},
		}
	}

	// The watcher read the old file before the upload replaced it.
	oldBytes := fileBytes("pfx-old")
	if errWrite := os.WriteFile(path, oldBytes, 0o600); errWrite != nil {
		t.Fatalf("write old auth file: %v", errWrite)
	}
	snapshot := newAuth("pfx-old")
	snapshot.BindFileSnapshot(path, oldBytes)

	var uploaded *coreauth.Auth
	original := beforeSnapshotRegister
	t.Cleanup(func() { beforeSnapshotRegister = original })
	beforeSnapshotRegister = func(string) {
		if errWrite := os.WriteFile(path, fileBytes("pfx-new"), 0o600); errWrite != nil {
			t.Fatalf("write uploaded auth file: %v", errWrite)
		}
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
