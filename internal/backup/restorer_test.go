package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

type stubImporter struct {
	called     bool
	lastBundle store.BackupBundle
	lastOpts   store.BackupImportOpts
}

func (s *stubImporter) ImportData(_ context.Context, b store.BackupBundle, opts store.BackupImportOpts) (store.BackupImportReport, error) {
	s.called = true
	s.lastBundle = b
	s.lastOpts = opts
	return store.BackupImportReport{}, nil
}

type stubPersister struct {
	authCalls   [][]string
	configCalls int
}

func (s *stubPersister) PersistConfig(_ context.Context) error {
	s.configCalls++
	return nil
}

func (s *stubPersister) PersistAuthFiles(_ context.Context, _ string, paths ...string) error {
	s.authCalls = append(s.authCalls, paths)
	return nil
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestRestoreMergeKeepsExistingAuthFiles(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(authDir, "existing.json")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	imp := &stubImporter{}
	pers := &stubPersister{}
	r := NewRestorer(imp, pers, "", authDir)
	bundle := store.BackupBundle{
		AuthFiles: []store.BackupAuthFile{
			{Path: "existing.json", Content: "new"},
			{Path: "added.json", Content: "added"},
		},
	}
	res, err := r.Restore(context.Background(), bundle, RestoreModeMerge)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFile(t, existing); got != "old" {
		t.Errorf("existing.json = %q, want %q (merge must not overwrite)", got, "old")
	}
	if got := readFile(t, filepath.Join(authDir, "added.json")); got != "added" {
		t.Errorf("added.json = %q, want %q", got, "added")
	}
	if res.Files != 1 {
		t.Errorf("Files = %d, want 1", res.Files)
	}
	if !imp.called {
		t.Error("importer not called")
	}
	for _, resName := range imp.lastOpts.Resources {
		if !store.IsBackupDataResource(resName) {
			t.Errorf("merge opts.Resources contains non-data resource %q", resName)
		}
	}
}

func TestRestoreReplaceOverwritesAuthFiles(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(authDir, "existing.json")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	imp := &stubImporter{}
	pers := &stubPersister{}
	r := NewRestorer(imp, pers, "", authDir)
	bundle := store.BackupBundle{
		AuthFiles: []store.BackupAuthFile{
			{Path: "existing.json", Content: "new"},
			{Path: "added.json", Content: "added"},
		},
	}
	res, err := r.Restore(context.Background(), bundle, RestoreModeReplace)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFile(t, existing); got != "new" {
		t.Errorf("existing.json = %q, want %q", got, "new")
	}
	if got := readFile(t, filepath.Join(authDir, "added.json")); got != "added" {
		t.Errorf("added.json = %q, want %q", got, "added")
	}
	if res.Files != 2 {
		t.Errorf("Files = %d, want 2", res.Files)
	}
	if imp.lastOpts.Resources != nil {
		t.Errorf("replace opts.Resources = %v, want nil (all bundle resources)", imp.lastOpts.Resources)
	}
	if len(pers.authCalls) != 1 || len(pers.authCalls[0]) != 2 {
		t.Errorf("PersistAuthFiles calls = %v, want one call with 2 paths", pers.authCalls)
	}
}

func TestRestoreWriteConfigOnlyWhenMissing(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	cfgPath := filepath.Join(dir, "config.yaml")
	bundle := store.BackupBundle{ConfigYAML: "fresh: true\n"}

	// Merge with existing config: untouched.
	if err := os.WriteFile(cfgPath, []byte("old: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRestorer(&stubImporter{}, &stubPersister{}, cfgPath, authDir)
	if _, err := r.Restore(context.Background(), bundle, RestoreModeMerge); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFile(t, cfgPath); got != "old: true\n" {
		t.Errorf("merge kept config = %q, want untouched", got)
	}

	// Merge with missing config: written.
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Restore(context.Background(), bundle, RestoreModeMerge); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFile(t, cfgPath); got != "fresh: true\n" {
		t.Errorf("merge wrote config = %q, want bundle contents", got)
	}

	// Replace always writes.
	if err := os.WriteFile(cfgPath, []byte("old: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Restore(context.Background(), bundle, RestoreModeReplace); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFile(t, cfgPath); got != "fresh: true\n" {
		t.Errorf("replace config = %q, want bundle contents", got)
	}
}

func TestRestoreRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	r := NewRestorer(&stubImporter{}, &stubPersister{}, "", authDir)
	bundle := store.BackupBundle{
		AuthFiles: []store.BackupAuthFile{{Path: "../escape.json", Content: "x"}},
	}
	_, err := r.Restore(context.Background(), bundle, RestoreModeReplace)
	if err == nil {
		t.Fatal("Restore succeeded, want path-escape error")
	}
	if !strings.Contains(err.Error(), "escape") {
		t.Errorf("error = %q, want mention of escaping auth dir", err.Error())
	}
	if _, statErr := os.Stat(filepath.Join(dir, "escape.json")); !os.IsNotExist(statErr) {
		t.Error("escape.json was written outside authDir")
	}
}

func TestRestoreNilImporterSkipsPG(t *testing.T) {
	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	r := NewRestorer(nil, &stubPersister{}, "", authDir)
	bundle := store.BackupBundle{
		AuthFiles: []store.BackupAuthFile{{Path: "a.json", Content: "a"}},
	}
	res, err := r.Restore(context.Background(), bundle, RestoreModeReplace)
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.Report != nil {
		t.Errorf("Report = %+v, want nil when importer is nil", res.Report)
	}
	if res.Files != 1 {
		t.Errorf("Files = %d, want 1", res.Files)
	}
	if got := readFile(t, filepath.Join(authDir, "a.json")); got != "a" {
		t.Errorf("a.json = %q, want %q", got, "a")
	}
}
