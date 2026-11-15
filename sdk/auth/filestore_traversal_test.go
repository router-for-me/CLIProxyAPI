package auth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestFileTokenStoreSaveRejectsTraversalFileName is the defense-in-depth half of the
// codex_import path traversal fix: even if a relative file name carrying ".."
// reaches the store, the resolved path must stay inside the configured auth dir.
func TestFileTokenStoreSaveRejectsTraversalFileName(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	outside := filepath.Join(root, "escaped.json")

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)

	// "codex-abc12345-.." is a single component, not a parent reference, so the
	// escape needs one ".." to pop it and one more to leave the auth directory.
	hostile := "codex-abc12345-../../../escaped.json"
	auth := &cliproxyauth.Auth{
		ID:       hostile,
		Provider: "codex",
		FileName: hostile,
		Metadata: map[string]any{"email": "attacker@example.com"},
	}

	path, err := store.Save(context.Background(), auth)
	if err == nil {
		t.Fatalf("Save() returned nil error for a traversing file name (wrote %q)", path)
	}
	if !strings.Contains(err.Error(), "auth directory") {
		t.Fatalf("Save() error = %v, want an auth-directory containment error", err)
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatalf("Save() wrote %q outside the auth directory", outside)
	}
}

// TestFileTokenStoreSaveRejectsTraversalID covers the auth.ID branch of resolveAuthPath,
// used when no explicit FileName is set.
func TestFileTokenStoreSaveRejectsTraversalID(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	outside := filepath.Join(root, "escaped-id.json")

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)

	auth := &cliproxyauth.Auth{
		ID:       "../escaped-id.json",
		Provider: "codex",
		Metadata: map[string]any{"email": "attacker@example.com"},
	}

	if path, err := store.Save(context.Background(), auth); err == nil {
		t.Fatalf("Save() returned nil error for a traversing id (wrote %q)", path)
	}
	if _, statErr := os.Stat(outside); statErr == nil {
		t.Fatalf("Save() wrote %q outside the auth directory", outside)
	}
}

// TestFileTokenStoreSaveAcceptsContainedFileName is the positive control: the guard
// must not reject a legitimate credential file name.
func TestFileTokenStoreSaveAcceptsContainedFileName(t *testing.T) {
	authDir := t.TempDir()

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)

	auth := &cliproxyauth.Auth{
		ID:       "codex-abc12345-user@example.com-plus.json",
		Provider: "codex",
		FileName: "codex-abc12345-user@example.com-plus.json",
		Metadata: map[string]any{"email": "user@example.com"},
	}

	path, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save() on a contained file name failed: %v", err)
	}
	want := filepath.Join(authDir, "codex-abc12345-user@example.com-plus.json")
	if path != want {
		t.Fatalf("Save() path = %q, want %q", path, want)
	}
	if _, statErr := os.Stat(want); statErr != nil {
		t.Fatalf("Save() did not create %q: %v", want, statErr)
	}
}

// TestFileTokenStoreSaveAcceptsNestedContainedFileName keeps sub-directory layouts
// working: containment is the rule, not "no separators at all".
func TestFileTokenStoreSaveAcceptsNestedContainedFileName(t *testing.T) {
	authDir := t.TempDir()

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)

	auth := &cliproxyauth.Auth{
		ID:       filepath.Join("nested", "codex-abc12345-user@example.com.json"),
		Provider: "codex",
		FileName: filepath.Join("nested", "codex-abc12345-user@example.com.json"),
		Metadata: map[string]any{"email": "user@example.com"},
	}

	if _, err := store.Save(context.Background(), auth); err != nil {
		t.Fatalf("Save() on a nested contained file name failed: %v", err)
	}
}

// TestSaveWithHostileCodexEmailStaysInsideAuthDir is the end-to-end regression for
// the reported traversal: an attacker-controlled Codex JWT email claim flows through
// codex.CredentialFileName into the file store, and must never produce a write
// outside the configured auth directory.
func TestSaveWithHostileCodexEmailStaysInsideAuthDir(t *testing.T) {
	hostileEmails := []string{
		"../../../../escaped.json",
		"../../escaped",
		"a/../../../escaped",
		"..\\..\\..\\escaped",
	}

	for _, email := range hostileEmails {
		t.Run(email, func(t *testing.T) {
			root := t.TempDir()
			authDir := filepath.Join(root, "nested", "auths")
			if err := os.MkdirAll(authDir, 0o700); err != nil {
				t.Fatalf("mkdir auth dir: %v", err)
			}

			store := NewFileTokenStore()
			store.SetBaseDir(authDir)

			fileName := codex.CredentialFileName(email, "plus", "abc12345", true)
			auth := &cliproxyauth.Auth{
				ID:       fileName,
				Provider: "codex",
				FileName: fileName,
				Metadata: map[string]any{"email": email},
			}

			path, err := store.Save(context.Background(), auth)
			absDir, errDir := filepath.Abs(authDir)
			if errDir != nil {
				t.Fatalf("abs auth dir: %v", errDir)
			}
			// Refusing the write is an acceptable outcome; escaping is not. The
			// scan for stray files below runs in both cases.
			if err == nil {
				absPath, errPath := filepath.Abs(path)
				if errPath != nil {
					t.Fatalf("abs saved path: %v", errPath)
				}
				if !strings.HasPrefix(absPath, absDir+string(filepath.Separator)) {
					t.Fatalf("email %q produced %q, outside the auth directory %q", email, absPath, absDir)
				}
			}

			// Nothing may exist anywhere above the auth directory either.
			var strays []string
			walkErr := filepath.Walk(root, func(p string, info os.FileInfo, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if info.IsDir() {
					return nil
				}
				if !strings.HasPrefix(p, absDir+string(filepath.Separator)) {
					strays = append(strays, p)
				}
				return nil
			})
			if walkErr != nil {
				t.Fatalf("walk %q: %v", root, walkErr)
			}
			if len(strays) > 0 {
				t.Fatalf("email %q wrote files outside the auth directory: %v", email, strays)
			}
		})
	}
}

// TestFileTokenStore_Save_RefusesPreExistingSymlink covers the vector the lexical
// path check cannot see. It supplies auth.Storage so that the record takes the
// auth.Storage != nil branch of Save, which the table test above never could - it
// builds its record without Storage.
//
// NOTE, corrected after review: with the guard PRESENT this test does not actually
// reach that writer, because resolveAuthPath rejects the symlink first. The writer
// is exercised by the positive control below, and by this test only under a mutant
// with the guard disabled - which is exactly when it must go red.
//
// The double's SaveTokenToFile uses os.WriteFile, which FOLLOWS symlinks. That is
// deliberate: without the guard in joinWithinDir this test writes the credential to
// the link's target and fails, which is what makes it able to go red.
func TestFileTokenStore_Save_RefusesPreExistingSymlink(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	outside := filepath.Join(root, "outside.json")

	fileName := codex.CredentialFileName("user@example.com", "plus", "abc12345", true)
	if err := os.Symlink(outside, filepath.Join(authDir, fileName)); err != nil {
		t.Skipf("symlinks unavailable on this platform: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)

	auth := &cliproxyauth.Auth{
		ID:       fileName,
		Provider: "codex",
		FileName: fileName,
		Storage:  &testTokenStorage{meta: map[string]any{"email": "user@example.com"}},
		Metadata: map[string]any{"email": "user@example.com"},
	}

	if _, err := store.Save(context.Background(), auth); err == nil {
		t.Fatalf("Save through a pre-existing symlink was allowed")
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatalf("credential was written outside the auth dir at %s", outside)
	}
}

// TestFileTokenStore_Save_StorageWriterSucceedsWithoutSymlink is the POSITIVE
// CONTROL for the test above: same Storage-backed path, no symlink in the way.
// Without it, a Save() broken for any reason would make the refusal test pass
// vacuously.
func TestFileTokenStore_Save_StorageWriterSucceedsWithoutSymlink(t *testing.T) {
	authDir := t.TempDir()
	fileName := codex.CredentialFileName("user@example.com", "plus", "abc12345", true)

	store := NewFileTokenStore()
	store.SetBaseDir(authDir)

	auth := &cliproxyauth.Auth{
		ID:       fileName,
		Provider: "codex",
		FileName: fileName,
		Storage:  &testTokenStorage{meta: map[string]any{"email": "user@example.com"}},
		Metadata: map[string]any{"email": "user@example.com"},
	}

	path, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save via Storage failed on a clean auth dir: %v", err)
	}
	if _, errStat := os.Stat(path); errStat != nil {
		t.Fatalf("Save reported %q but nothing is there: %v", path, errStat)
	}
}
