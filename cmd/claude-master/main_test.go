package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/claudemaster"
)

func TestInvalidArgumentsStopBeforeProfileOrLogin(t *testing.T) {
	for _, args := range [][]string{
		nil, {"help"}, {"unknown", "profile"},
		{"login", "profile", "--provider", "unknown"},
		{"login", "profile", "--provider", "codex"},
		{"login", "profile", "--provider", "claude", "--model", "test"},
		{"login", "profile", "--provider", "claude", "--", "unexpected"},
		{"run", "profile", "--model", "test"}, {"run", "profile", "--provider", "claude"},
		{"run", "profile", "--unknown-secret=canary"},
		{"probe", "profile"}, {"probe", "profile", "--model", "test", "--", "unexpected"},
		{"probe", "profile", "--model", "test", "--diagnostics"},
		{"login", "profile", "--provider", "claude", "--diagnostics"},
		{"login", "profile", "--provider", "claude", "--next-profile", "other"},
		{"probe", "profile", "--model", "test", "--next-profile", "other"},
		{"run", "profile", "--next-profile", "profile"},
		{"run", "profile", "--next-profile", ""},
		{"run", "profile", "--next-profile", "../unsafe"},
	} {
		code, err := run(args)
		if err == nil || code != 2 {
			t.Fatalf("invalid command accepted: %q, code=%d, err=%v", args, code, err)
		}
	}
}

func TestRunModelSelectionBelongsToNativeClaude(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, args := range [][]string{
		{"run", "missing"},
		{"run", "missing", "--", "--model", "sonnet", "--fallback-model", "haiku"},
	} {
		code, err := run(args)
		if err == nil || code != 1 {
			t.Fatalf("run arguments did not reach profile loading: %q, code=%d, err=%v", args, code, err)
		}
	}
}

func installRunProfileFixture(t *testing.T, home, name string) {
	t.Helper()
	current := filepath.Join(home, ".local", "share", "claude-master", "profiles", name, "current")
	authDir := filepath.Join(current, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(home, ".local"),
		filepath.Join(home, ".local", "share"),
		filepath.Join(home, ".local", "share", "claude-master"),
		filepath.Join(home, ".local", "share", "claude-master", "profiles"),
		filepath.Join(home, ".local", "share", "claude-master", "profiles", name),
		current,
		authDir,
	} {
		if err := os.Chmod(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(authDir, "synthetic.json"), []byte(`{"type":"claude"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(current, "profile.json"), []byte(`{"provider":"claude","auth_id":"synthetic.json"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRunProfilesPreservesRequestedOrderAndReleasesAllLocks(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	installRunProfileFixture(t, home, "zeta")

	profiles, locks, err := openRunProfiles([]string{"zeta", "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0].Name != "zeta" || profiles[1].Name != "alpha" {
		t.Fatalf("profile order changed: %#v", profiles)
	}
	if competing, errOpen := claudemaster.OpenProfile("alpha", false); errOpen == nil {
		_ = competing.Close()
		t.Fatal("series did not reserve every profile")
	}
	closeProfileLocks(locks)
	for _, name := range []string{"alpha", "zeta"} {
		lock, errOpen := claudemaster.OpenProfile(name, false)
		if errOpen != nil {
			t.Fatalf("profile %s remained locked: %v", name, errOpen)
		}
		_ = lock.Close()
	}
}

func TestOpenRunProfilesReleasesPartialLockSet(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	installRunProfileFixture(t, home, "alpha")
	if _, _, err := openRunProfiles([]string{"alpha", "missing"}); err == nil {
		t.Fatal("missing profile accepted")
	}
	lock, err := claudemaster.OpenProfile("alpha", false)
	if err != nil {
		t.Fatalf("partial acquisition leaked alpha lock: %v", err)
	}
	_ = lock.Close()
}
