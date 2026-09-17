package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestPostgresStoreBootstrapDoesNotWriteSpoolConfig covers Phase 5 of the
// PG-first control plane: after Bootstrap, the legacy `pgstore/config/
// config.yaml` spool file MUST NOT be created. The runtime config flows
// through the runtimebridge bridge instead, which writes its own private
// config.yaml per process.
func TestPostgresStoreBootstrapDoesNotWriteSpoolConfig(t *testing.T) {
	skipIfNoPostgres(t)
	dsn := strings.TrimSpace(os.Getenv("PGSTORE_TEST_DSN"))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Use a tempdir for SpoolDir so we never touch a real spool location.
	tmpDir := t.TempDir()
	pg, err := store.NewPostgresStore(ctx, store.PostgresStoreConfig{
		DSN:    dsn,
		Schema: "test_p5_no_spool",
		SpoolDir: tmpDir,
	})
	if err != nil {
		t.Fatalf("NewPostgresStore: %v", err)
	}
	defer pg.Close()

	if err := pg.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}
	if err := pg.Bootstrap(ctx, ""); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	spoolPath := pg.ConfigPath()
	if _, err := os.Stat(spoolPath); err == nil {
		t.Fatalf("spool config.yaml was written at %s; Phase 5 expects it to be absent", spoolPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error on %s: %v", spoolPath, err)
	}

	// Sanity: the auth-dir mirror is still written (Phase 5 keeps it
	// because the bridge does not yet own the auth files; Core reads
	// them at runtime via fsnotify).
	if _, err := os.Stat(pg.AuthDir()); err != nil {
		t.Fatalf("auth-dir mirror missing at %s: %v", pg.AuthDir(), err)
	}

	// Sanity: the tempdir still contains the bridge-relevant structure
	// (no config.yaml under it).
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", tmpDir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "config.yaml") && filepath.Dir(filepath.Join(tmpDir, e.Name())) == tmpDir {
			t.Fatalf("unexpected config.yaml under spool root: %s", e.Name())
		}
	}
}
