package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestSyncAuthFromDatabase_PreservesLocalOnlyFiles(t *testing.T) {
	t.Parallel()

	store, db := newSQLitePostgresStore(t)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO "auth_store"(id, content) VALUES (?, ?)`, "nested/provider.json", `{"token":"db"}`); err != nil {
		t.Fatalf("insert auth row: %v", err)
	}

	localOnly := filepath.Join(store.authDir, "local-only.json")
	if err := os.WriteFile(localOnly, []byte(`{"token":"local"}`), 0o600); err != nil {
		t.Fatalf("seed local-only file: %v", err)
	}

	if err := store.syncAuthFromDatabase(context.Background()); err != nil {
		t.Fatalf("sync auth from database: %v", err)
	}

	if _, err := os.Stat(localOnly); err != nil {
		t.Fatalf("expected local-only file to be preserved: %v", err)
	}

	mirrored := filepath.Join(store.authDir, "nested", "provider.json")
	got, err := os.ReadFile(mirrored)
	if err != nil {
		t.Fatalf("read mirrored auth file: %v", err)
	}
	if string(got) != `{"token":"db"}` {
		t.Fatalf("unexpected mirrored content: %s", got)
	}
}

func TestSyncAuthFromDatabase_ContinuesOnPathConflict(t *testing.T) {
	t.Parallel()

	store, db := newSQLitePostgresStore(t)
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`INSERT INTO "auth_store"(id, content) VALUES (?, ?)`, "conflict.json", `{"token":"db-conflict"}`); err != nil {
		t.Fatalf("insert conflict auth row: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO "auth_store"(id, content) VALUES (?, ?)`, "healthy.json", `{"token":"db-healthy"}`); err != nil {
		t.Fatalf("insert healthy auth row: %v", err)
	}

	conflictPath := filepath.Join(store.authDir, "conflict.json")
	if err := os.MkdirAll(conflictPath, 0o700); err != nil {
		t.Fatalf("seed conflicting directory: %v", err)
	}

	if err := store.syncAuthFromDatabase(context.Background()); err != nil {
		t.Fatalf("sync auth from database: %v", err)
	}

	if info, err := os.Stat(conflictPath); err != nil {
		t.Fatalf("stat conflict path: %v", err)
	} else if !info.IsDir() {
		t.Fatalf("expected conflict path to remain a directory")
	}

	healthyPath := filepath.Join(store.authDir, "healthy.json")
	got, err := os.ReadFile(healthyPath)
	if err != nil {
		t.Fatalf("read healthy mirrored auth file: %v", err)
	}
	if string(got) != `{"token":"db-healthy"}` {
		t.Fatalf("unexpected healthy mirrored content: %s", got)
	}
}

func newSQLitePostgresStore(t *testing.T) (*PostgresStore, *sql.DB) {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err = db.Exec(`CREATE TABLE "auth_store" (id TEXT PRIMARY KEY, content TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		t.Fatalf("create auth table: %v", err)
	}

	spool := t.TempDir()
	authDir := filepath.Join(spool, "auths")
	if err = os.MkdirAll(authDir, 0o700); err != nil {
		_ = db.Close()
		t.Fatalf("create auth dir: %v", err)
	}

	store := &PostgresStore{
		db:      db,
		cfg:     PostgresStoreConfig{AuthTable: "auth_store"},
		authDir: authDir,
	}
	return store, db
}
