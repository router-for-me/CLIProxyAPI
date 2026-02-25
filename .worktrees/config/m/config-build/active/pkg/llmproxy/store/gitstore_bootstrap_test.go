package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v6"
)

func TestOpenOrInitRepositoryAfterEmptyCloneArchivesExistingGitDir(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(gitDir, 0o700); err != nil {
		t.Fatalf("create git dir: %v", err)
	}
	markerPath := filepath.Join(gitDir, "marker.txt")
	if err := os.WriteFile(markerPath, []byte("keep-me"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	repo, err := openOrInitRepositoryAfterEmptyClone(repoDir)
	if err != nil {
		t.Fatalf("open/init repo: %v", err)
	}
	if repo == nil {
		t.Fatalf("expected repository instance")
	}

	if _, err := git.PlainOpen(repoDir); err != nil {
		t.Fatalf("open initialized repository: %v", err)
	}
	entries, err := os.ReadDir(repoDir)
	if err != nil {
		t.Fatalf("read repo dir: %v", err)
	}
	backupCount := 0
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".git.bootstrap-backup-") {
			continue
		}
		backupCount++
		archivedMarker := filepath.Join(repoDir, entry.Name(), "marker.txt")
		if _, err := os.Stat(archivedMarker); err != nil {
			t.Fatalf("expected archived marker file: %v", err)
		}
	}
	if backupCount != 1 {
		t.Fatalf("expected exactly one archived git dir, got %d", backupCount)
	}
}

func TestEnsureRepositoryBootstrapsEmptyRemoteClone(t *testing.T) {
	t.Parallel()

	remoteDir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := git.PlainInit(remoteDir, true); err != nil {
		t.Fatalf("init bare remote: %v", err)
	}

	repoRoot := filepath.Join(t.TempDir(), "local-repo")
	store := NewGitTokenStore(remoteDir, "", "")
	store.SetBaseDir(filepath.Join(repoRoot, "auths"))

	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("ensure repository: %v", err)
	}

	if _, err := os.Stat(filepath.Join(repoRoot, ".git")); err != nil {
		t.Fatalf("expected local .git directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "auths", ".gitkeep")); err != nil {
		t.Fatalf("expected auth placeholder: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, "config", ".gitkeep")); err != nil {
		t.Fatalf("expected config placeholder: %v", err)
	}

	repo, err := git.PlainOpen(repoRoot)
	if err != nil {
		t.Fatalf("open local repository: %v", err)
	}
	origin, err := repo.Remote("origin")
	if err != nil {
		t.Fatalf("origin remote: %v", err)
	}
	urls := origin.Config().URLs
	if len(urls) != 1 || urls[0] != remoteDir {
		t.Fatalf("unexpected origin URLs: %#v", urls)
	}
}
