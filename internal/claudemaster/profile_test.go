package claudemaster

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sdkauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func profileFixture(t *testing.T) *ProfileLock {
	t.Helper()
	lock, err := openProfileAt(filepath.Join(canonicalTestTempDir(t), "profiles"), "test-user", true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return lock
}

func canonicalTestTempDir(t *testing.T) string {
	t.Helper()
	// macOS commonly places TempDir below /var, a symlink to /private/var. Canonicalize only
	// the test fixture; production profile validation must continue rejecting linked paths.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func installProfileFixture(t *testing.T, lock *ProfileLock) {
	t.Helper()
	current := filepath.Join(lock.dir, "current")
	if err := os.Mkdir(current, 0o700); err != nil {
		t.Fatal(err)
	}
	authDir := filepath.Join(current, "auth")
	if err := os.Mkdir(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(authDir, "synthetic.json"), []byte(`{"type":"claude","access_token":"synthetic-not-a-login"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(profileMetadata{Provider: "claude", AuthID: "synthetic.json"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "profile.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProfileRejectsTraversal(t *testing.T) {
	root := filepath.Join(canonicalTestTempDir(t), "profiles")
	for _, name := range []string{"", ".", "..", "../other", "a/b", "a\\b", "/absolute", ".hidden", "a\nsecret"} {
		if lock, err := openProfileAt(root, name, true); err == nil {
			_ = lock.Close()
			t.Errorf("accepted unsafe name %q", name)
		}
	}
}

func TestProfileLockExclusive(t *testing.T) {
	lock := profileFixture(t)
	if other, err := openProfileAt(filepath.Dir(lock.dir), lock.name, false); err == nil {
		_ = other.Close()
		t.Fatal("second lock acquired")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := openProfileAt(filepath.Dir(lock.dir), lock.name, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = other.Close()
	if err := lock.Close(); err != nil {
		t.Fatal("close is not idempotent")
	}
}

func TestProfileMetadataAndPrivatePermissions(t *testing.T) {
	lock := profileFixture(t)
	installProfileFixture(t, lock)
	profile, err := lock.Profile()
	if err != nil {
		t.Fatal(err)
	}
	if profile.Provider != "claude" || profile.AuthID != "synthetic.json" || profile.Name != "test-user" {
		t.Fatalf("wrong non-secret profile: %#v", profile)
	}
	if err := os.Chmod(filepath.Join(profile.AuthDir, profile.AuthID), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Profile(); err == nil {
		t.Fatal("accepted world-readable credential")
	}
}

func TestProfileRejectsCredentialSymlinkAndHiddenDirectory(t *testing.T) {
	for _, kind := range []string{"symlink", "directory", "extra-file"} {
		t.Run(kind, func(t *testing.T) {
			lock := profileFixture(t)
			installProfileFixture(t, lock)
			authDir := filepath.Join(lock.dir, "current", "auth")
			switch kind {
			case "symlink":
				path := filepath.Join(authDir, "synthetic.json")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside.json"), path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(authDir, ".hidden"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "extra-file":
				if err := os.WriteFile(filepath.Join(authDir, "other.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := lock.Profile(); err == nil {
				t.Fatal("accepted unsafe auth directory")
			}
		})
	}
}

func TestProfileRejectsLinkedOrPublicDirectories(t *testing.T) {
	parent := canonicalTestTempDir(t)
	actual := filepath.Join(parent, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(parent, "linked")
	if err := os.Symlink(actual, linked); err != nil {
		t.Fatal(err)
	}
	if lock, err := openProfileAt(linked, "test", true); err == nil {
		_ = lock.Close()
		t.Fatal("accepted symlink root")
	}
	if err := os.Chmod(actual, 0o755); err != nil {
		t.Fatal(err)
	}
	if lock, err := openProfileAt(actual, "test", true); err == nil {
		_ = lock.Close()
		t.Fatal("accepted nonprivate root")
	}
}

func TestLoginNeverOverwritesExistingProfile(t *testing.T) {
	lock := profileFixture(t)
	installProfileFixture(t, lock)
	if err := lock.Login(context.Background(), "claude", nil); err == nil {
		t.Fatal("replaced existing login")
	}
	if _, err := lock.Profile(); err != nil {
		t.Fatalf("existing profile damaged: %v", err)
	}
}

func TestCancelledLoginLeavesNoSelectedOrStagedCredential(t *testing.T) {
	lock := profileFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lock.Login(ctx, "claude", nil); err == nil {
		t.Fatal("cancelled login succeeded")
	}
	entries, err := os.ReadDir(lock.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "profile.lock" {
		t.Fatal("failed login left credential files")
	}
	if _, err := lock.Profile(); err == nil {
		t.Fatal("failed login selected a profile")
	}
}

func TestProfileIgnoresUncommittedLoginStage(t *testing.T) {
	lock := profileFixture(t)
	stage, err := os.MkdirTemp(lock.dir, ".login-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "partial.json"), []byte("synthetic"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := lock.Profile(); err == nil {
		t.Fatal("adopted incomplete login")
	}
	installProfileFixture(t, lock)
	if _, err := lock.Profile(); err != nil {
		t.Fatalf("stage interfered with committed profile: %v", err)
	}
}

func TestLoginStoreRejectsOutsidePathsAndReservesPrivateFile(t *testing.T) {
	dir := canonicalTestTempDir(t)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	guard := &loginStore{dir: dir, store: store}
	if _, err := guard.Save(context.Background(), &coreauth.Auth{ID: "../escape.json", FileName: "../escape.json", Metadata: map[string]any{"type": "claude"}}); err == nil {
		t.Fatal("accepted escaping filename")
	}
	path, err := guard.Save(coreauth.WithAuthCreationIntent(context.Background()), &coreauth.Auth{ID: "test.json", FileName: "test.json", Provider: "claude", Metadata: map[string]any{"type": "claude", "access_token": "synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "test.json") {
		t.Fatal("wrong credential destination")
	}
	if err := validateAuthDirectory(dir, "test.json"); err != nil {
		t.Fatal(err)
	}
}
