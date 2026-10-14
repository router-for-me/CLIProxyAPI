package store

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v6"
)

// A save can leave config.yaml empty for a moment. Mirroring that state would wipe
// the remote config that restarts and other replicas load, so it must be skipped.

func TestObjectTokenStorePersistConfigKeepsRemoteWhenLocalConfigEmpty(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	var puts int
	store := &ObjectTokenStore{
		configPath: configPath,
		putObjectFn: func(context.Context, string, []byte, string) error {
			puts++
			return nil
		},
	}
	// No client is configured, so a delete of the remote object would fail here.
	if err := store.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig() error = %v, want the empty local config skipped", err)
	}
	if puts != 0 {
		t.Fatalf("uploaded the empty config %d times, want 0", puts)
	}
}

func TestObjectTokenStorePersistConfigUploadsNonEmptyConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var uploaded []byte
	store := &ObjectTokenStore{
		configPath: configPath,
		putObjectFn: func(_ context.Context, _ string, data []byte, _ string) error {
			uploaded = data
			return nil
		},
	}
	if err := store.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig() error = %v", err)
	}
	if string(uploaded) != "port: 8317\n" {
		t.Fatalf("uploaded %q, want the local config", uploaded)
	}
}

func TestPostgresStorePersistConfigKeepsRemoteWhenLocalConfigEmpty(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatalf("write empty config: %v", err)
	}
	// No database is configured, so an upsert of the empty config would fail here.
	store := &PostgresStore{configPath: configPath}
	if err := store.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig() error = %v, want the empty local config skipped", err)
	}
}

func TestGitTokenStorePersistConfigKeepsCommittedConfigWhenLocalConfigEmpty(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(filepath.Join(root, "workspace", "auths"))
	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	configPath := store.ConfigPath()
	if err := os.WriteFile(configPath, []byte("version: one\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := store.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig initial: %v", err)
	}
	if err := os.WriteFile(configPath, nil, 0o600); err != nil {
		t.Fatalf("truncate config: %v", err)
	}
	if err := store.PersistConfig(context.Background()); err != nil {
		t.Fatalf("PersistConfig empty: %v", err)
	}

	workspace := filepath.Join(root, "workspace")
	repo, err := git.PlainOpen(workspace)
	if err != nil {
		t.Fatalf("open workspace repo: %v", err)
	}
	defer func() { _ = repo.Close() }()
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("head commit: %v", err)
	}
	rel, err := filepath.Rel(workspace, configPath)
	if err != nil {
		t.Fatalf("rel config path: %v", err)
	}
	file, err := commit.File(filepath.ToSlash(rel))
	if err != nil {
		t.Fatalf("committed config: %v", err)
	}
	contents, err := file.Contents()
	if err != nil {
		t.Fatalf("read committed config: %v", err)
	}
	if contents != "version: one\n" {
		t.Fatalf("committed config = %q, want the last non-empty config kept", contents)
	}
}
