package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPostgresStoreConcurrentPublicationFailure(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN must point to a disposable PostgreSQL database")
	}
	for _, test := range []struct {
		name      string
		previous  string
		candidate string
		newer     string
		operation string
	}{
		{"same-content-update", `{"value":"old"}`, `{"value":"new"}`, `{"value":"new"}`, "save"},
		{"same-content-insert", "", `{"value":"new"}`, `{"value":"new"}`, "save"},
		{"jsonb-equivalent", `{"value":"old"}`, `{"a":1,"b":2}`, `{"b":2, "a":1}`, "save"},
		{"empty-save", `{"value":"old"}`, "", "", "save"},
		{"delete", `{"value":"old"}`, "", "", "delete"},
		{"watcher-update", `{"value":"old"}`, `{"value":"new"}`, `{"value":"new"}`, "watcher"},
		{"watcher-delete", `{"value":"old"}`, "", "", "watcher"},
		{"qualified-table", `{"value":"old"}`, `{"value":"new"}`, `{"value":"new"}`, "save"},
		{"cancelled-publication", `{"value":"old"}`, `{"value":"new"}`, `{"value":"new"}`, "save"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			table := fmt.Sprintf("auth_test_%d", time.Now().UnixNano())
			if _, err = db.ExecContext(ctx, "CREATE TABLE "+table+" (id TEXT PRIMARY KEY, content JSONB NOT NULL, created_at TIMESTAMPTZ DEFAULT NOW(), updated_at TIMESTAMPTZ DEFAULT NOW())"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, errDrop := db.ExecContext(context.Background(), "DROP TABLE "+table); errDrop != nil {
					t.Error(errDrop)
				}
			}()
			newStore := func() *PostgresStore {
				t.Helper()
				conn, errOpen := sql.Open("pgx", dsn)
				if errOpen != nil {
					t.Fatal(errOpen)
				}
				conn.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = conn.Close() })
				return &PostgresStore{db: conn, cfg: PostgresStoreConfig{AuthTable: table}, authDir: t.TempDir()}
			}
			a, b := newStore(), newStore()
			if test.name == "qualified-table" {
				if err = db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&b.cfg.Schema); err != nil {
					t.Fatal(err)
				}
			}
			saveCtx, cancelSave := context.WithCancel(ctx)
			defer cancelSave()
			const id = "credential.json"
			if test.previous != "" {
				if _, err = db.ExecContext(ctx, "INSERT INTO "+table+" (id, content) VALUES ($1, $2)", id, test.previous); err != nil {
					t.Fatal(err)
				}
				for _, store := range []*PostgresStore{a, b} {
					if err = os.WriteFile(filepath.Join(store.authDir, id), []byte(test.previous), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			var pid int
			if err = b.db.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				t.Fatal(err)
			}
			completed := make(chan error, 1)
			a.renameFile = func(string, string) error {
				go func() {
					var errWrite error
					switch test.operation {
					case "save":
						_, errWrite = b.Save(ctx, &cliproxyauth.Auth{ID: id, Storage: &postgresAuthTestStorage{data: []byte(test.newer)}})
					case "delete":
						errWrite = b.Delete(ctx, id)
					case "watcher":
						path := filepath.Join(b.authDir, id)
						if errWrite = os.WriteFile(path, []byte(test.newer), 0o600); errWrite == nil {
							errWrite = b.PersistAuthFiles(ctx, "", path)
						}
					}
					completed <- errWrite
				}()
				for {
					var waiting bool
					if errWait := db.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_locks WHERE pid = $1 AND locktype = 'advisory' AND NOT granted)", pid).Scan(&waiting); errWait != nil {
						t.Error(errWait)
						return errors.New("publish rejected")
					}
					if waiting {
						break
					}
					select {
					case errEarly := <-completed:
						t.Errorf("concurrent writer completed before compensation: %v", errEarly)
						completed <- errEarly
						return errors.New("publish rejected")
					default:
					}
				}
				if test.name == "cancelled-publication" {
					cancelSave()
				}
				return errors.New("publish rejected")
			}
			_, err = a.Save(saveCtx, &cliproxyauth.Auth{ID: id, Storage: &postgresAuthTestStorage{data: []byte(test.candidate)}})
			if err == nil || !strings.Contains(err.Error(), "publish rejected") || strings.Contains(err.Error(), "rollback failed") {
				t.Fatalf("Save() error = %v", err)
			}
			select {
			case err = <-completed:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var durable string
			err = db.QueryRowContext(ctx, "SELECT content FROM "+table+" WHERE id = $1", id).Scan(&durable)
			if test.newer == "" {
				if !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("record = %q, error = %v, want missing", durable, err)
				}
			} else if err != nil || !jsonEqual([]byte(durable), []byte(test.newer)) {
				t.Fatalf("record = %q, error = %v, want %s", durable, err, test.newer)
			}
			var previous []byte
			if test.previous != "" {
				previous = []byte(test.previous)
			}
			assertPostgresAuthLocal(t, filepath.Join(a.authDir, id), previous)
			assertPostgresAuthNoTemp(t, filepath.Join(a.authDir, id))
		})
	}
}
