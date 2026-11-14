package management

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestWriteAuthFileAtomicWrite(t *testing.T) {
	t.Run("regular overwrite", func(t *testing.T) {
		authDir := t.TempDir()
		h, _ := newIdentityTestHandler(t)
		h.cfg.AuthDir = authDir
		name := "atomic.json"
		if err := h.writeAuthFile(context.Background(), name, []byte(`{"type":"codex","email":"a@b.c"}`)); err != nil {
			t.Fatalf("initial write: %v", err)
		}
		if err := h.writeAuthFile(context.Background(), name, []byte(`{"type":"codex","email":"d@e.f"}`)); err != nil {
			t.Fatalf("overwrite: %v", err)
		}
		data, err := os.ReadFile(filepath.Join(authDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "d@e.f") {
			t.Fatalf("final bytes = %s", data)
		}
		info, err := os.Lstat(filepath.Join(authDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
		entries, err := os.ReadDir(authDir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".atomic.json.tmp-") {
				t.Fatalf("leftover temp file %s", e.Name())
			}
		}
	})

	t.Run("symlink passthrough", func(t *testing.T) {
		authDir := t.TempDir()
		realDir := t.TempDir()
		target := filepath.Join(realDir, "real.json")
		if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(authDir, "linked.json")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		h, _ := newIdentityTestHandler(t)
		h.cfg.AuthDir = authDir
		payload := []byte(`{"type":"codex","email":"x@y.z"}`)
		if err := h.writeAuthFile(context.Background(), "linked.json", payload); err != nil {
			t.Fatalf("symlink write: %v", err)
		}
		data, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != string(payload) {
			t.Fatalf("target bytes = %s", data)
		}
		info, err := os.Lstat(link)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("symlink was replaced by a regular file")
		}
	})

	t.Run("long filename", func(t *testing.T) {
		authDir := t.TempDir()
		h, _ := newIdentityTestHandler(t)
		h.cfg.AuthDir = authDir
		name := strings.Repeat("a", 240) + ".json"
		if err := h.writeAuthFile(context.Background(), name, []byte(`{"type":"codex"}`)); err != nil {
			t.Fatalf("long-name write: %v", err)
		}
		if _, err := os.Stat(filepath.Join(authDir, name)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCleanAuthFilePathResolvesSymlinkedDir(t *testing.T) {
	realDir := t.TempDir()
	linkDir := t.TempDir()
	linked := filepath.Join(linkDir, "linkeddir")
	if err := os.Symlink(realDir, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	real := filepath.Join(realDir, "file.json")
	alias := filepath.Join(linked, "file.json")
	if gotReal, gotAlias := cleanAuthFilePath(real), cleanAuthFilePath(alias); gotReal != gotAlias {
		t.Fatalf("cleanAuthFilePath(%q)=%q != cleanAuthFilePath(%q)=%q", alias, gotAlias, real, gotReal)
	}
	if !sameAuthFilePath(real, alias) {
		t.Fatal("sameAuthFilePath must treat symlinked-dir alias as the same file")
	}
}

func TestShouldLockCodexAuthPathMatchesSymlinkedDirAlias(t *testing.T) {
	realDir := t.TempDir()
	aliasParent := t.TempDir()
	linked := filepath.Join(aliasParent, "linkeddir")
	if err := os.Symlink(realDir, linked); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	h, authDir := newIdentityTestHandler(t)
	real := filepath.Join(realDir, "codex.json")
	if _, err := h.authManager.Register(context.Background(), &coreauth.Auth{
		ID:       "codex.json",
		Provider: "codex",
		Attributes: map[string]string{
			coreauth.AttributePath:          real,
			coreauth.AttributeSource:        real,
			coreauth.AttributeSourceBackend: coreauth.AuthSourceFile,
		},
	}); err != nil {
		t.Fatal(err)
	}
	_ = authDir
	if !h.shouldLockCodexAuthPath(filepath.Join(linked, "codex.json")) {
		t.Fatal("symlinked-dir alias must still select the codex lock")
	}
}
