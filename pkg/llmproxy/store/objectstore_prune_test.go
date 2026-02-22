package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildSafeAuthPrunePlan_PrunesUnchangedStaleJSON(t *testing.T) {
	t.Parallel()

	authDir := t.TempDir()
	stalePath := filepath.Join(authDir, "stale.json")
	if err := os.WriteFile(stalePath, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatalf("write stale file: %v", err)
	}

	baseline, err := snapshotLocalAuthFiles(authDir)
	if err != nil {
		t.Fatalf("snapshot baseline: %v", err)
	}

	stale, conflicts, err := buildSafeAuthPrunePlan(authDir, baseline, map[string]struct{}{})
	if err != nil {
		t.Fatalf("build prune plan: %v", err)
	}

	if len(stale) != 1 || stale[0] != stalePath {
		t.Fatalf("expected stale path %s, got %#v", stalePath, stale)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %#v", conflicts)
	}
}

func TestBuildSafeAuthPrunePlan_SkipsLocallyModifiedFileAsConflict(t *testing.T) {
	t.Parallel()

	authDir := t.TempDir()
	changedPath := filepath.Join(authDir, "changed.json")
	if err := os.WriteFile(changedPath, []byte(`{"v":1}`), 0o600); err != nil {
		t.Fatalf("write changed file: %v", err)
	}

	baseline, err := snapshotLocalAuthFiles(authDir)
	if err != nil {
		t.Fatalf("snapshot baseline: %v", err)
	}

	if err := os.WriteFile(changedPath, []byte(`{"v":2}`), 0o600); err != nil {
		t.Fatalf("rewrite changed file: %v", err)
	}
	now := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(changedPath, now, now); err != nil {
		t.Fatalf("chtimes changed file: %v", err)
	}

	stale, conflicts, err := buildSafeAuthPrunePlan(authDir, baseline, map[string]struct{}{})
	if err != nil {
		t.Fatalf("build prune plan: %v", err)
	}

	if len(stale) != 0 {
		t.Fatalf("expected no stale paths, got %#v", stale)
	}
	if len(conflicts) != 1 || conflicts[0] != changedPath {
		t.Fatalf("expected conflict path %s, got %#v", changedPath, conflicts)
	}
}

func TestBuildSafeAuthPrunePlan_SkipsNewLocalFileAsConflict(t *testing.T) {
	t.Parallel()

	authDir := t.TempDir()
	baseline, err := snapshotLocalAuthFiles(authDir)
	if err != nil {
		t.Fatalf("snapshot baseline: %v", err)
	}

	newPath := filepath.Join(authDir, "new.json")
	if err := os.WriteFile(newPath, []byte(`{"new":true}`), 0o600); err != nil {
		t.Fatalf("write new file: %v", err)
	}

	stale, conflicts, err := buildSafeAuthPrunePlan(authDir, baseline, map[string]struct{}{})
	if err != nil {
		t.Fatalf("build prune plan: %v", err)
	}

	if len(stale) != 0 {
		t.Fatalf("expected no stale paths, got %#v", stale)
	}
	if len(conflicts) != 1 || conflicts[0] != newPath {
		t.Fatalf("expected conflict path %s, got %#v", newPath, conflicts)
	}
}

func TestBuildSafeAuthPrunePlan_DoesNotPruneRemoteOrNonJSON(t *testing.T) {
	t.Parallel()

	authDir := t.TempDir()
	remotePath := filepath.Join(authDir, "remote.json")
	nonJSONPath := filepath.Join(authDir, "keep.txt")
	if err := os.WriteFile(remotePath, []byte(`{"remote":true}`), 0o600); err != nil {
		t.Fatalf("write remote file: %v", err)
	}
	if err := os.WriteFile(nonJSONPath, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write non-json file: %v", err)
	}

	baseline, err := snapshotLocalAuthFiles(authDir)
	if err != nil {
		t.Fatalf("snapshot baseline: %v", err)
	}

	remote := map[string]struct{}{"remote.json": {}}
	stale, conflicts, err := buildSafeAuthPrunePlan(authDir, baseline, remote)
	if err != nil {
		t.Fatalf("build prune plan: %v", err)
	}

	if len(stale) != 0 {
		t.Fatalf("expected no stale paths, got %#v", stale)
	}
	if len(conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %#v", conflicts)
	}
}
