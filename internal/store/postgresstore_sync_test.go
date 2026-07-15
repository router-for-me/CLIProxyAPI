package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// newTestStore builds a PostgresStore backed by sqlmock with a temp auth dir.
func newTestStore(t *testing.T) (*PostgresStore, sqlmock.Sqlmock, func()) {
	t.Helper()

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}

	tmp := t.TempDir()
	authDir := filepath.Join(tmp, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("mkdir auths: %v", err)
	}

	s := &PostgresStore{
		db: db,
		cfg: PostgresStoreConfig{
			ConfigTable: defaultConfigTable,
			AuthTable:   defaultAuthTable,
		},
		spoolRoot:  tmp,
		configPath: filepath.Join(tmp, "config.yaml"),
		authDir:    authDir,
	}
	return s, mock, func() { _ = db.Close() }
}

func TestIncrementalAuthSync_WritesNewFile(t *testing.T) {
	s, mock, cleanup := newTestStore(t)
	defer cleanup()

	payload := `{"token":"abc"}`
	mock.ExpectQuery("SELECT id, content FROM").
		WillReturnRows(sqlmock.NewRows([]string{"id", "content"}).
			AddRow("claude/alice.json", payload))

	changed, err := s.incrementalAuthSync(context.Background())
	if err != nil {
		t.Fatalf("incrementalAuthSync: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true for a new file")
	}

	got, err := os.ReadFile(filepath.Join(s.authDir, "claude", "alice.json"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != payload {
		t.Fatalf("wrote %q, want %q", got, payload)
	}
}

func TestIncrementalAuthSync_SkipsUnchanged(t *testing.T) {
	s, mock, cleanup := newTestStore(t)
	defer cleanup()

	payload := `{"token":"same"}`
	path := filepath.Join(s.authDir, "claude", "alice.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(payload), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	mock.ExpectQuery("SELECT id, content FROM").
		WillReturnRows(sqlmock.NewRows([]string{"id", "content"}).
			AddRow("claude/alice.json", payload))

	changed, err := s.incrementalAuthSync(context.Background())
	if err != nil {
		t.Fatalf("incrementalAuthSync: %v", err)
	}
	if changed {
		t.Fatal("expected changed=false when local content matches DB")
	}
}

func TestIncrementalAuthSync_RemovesOrphans(t *testing.T) {
	s, mock, cleanup := newTestStore(t)
	defer cleanup()

	// Seed two local files: one mirrored in DB, one orphan.
	keep := filepath.Join(s.authDir, "claude", "keep.json")
	orphan := filepath.Join(s.authDir, "codex", "orphan.json")
	for _, p := range []string{keep, orphan} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(`{}`), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	mock.ExpectQuery("SELECT id, content FROM").
		WillReturnRows(sqlmock.NewRows([]string{"id", "content"}).
			AddRow("claude/keep.json", `{}`))

	changed, err := s.incrementalAuthSync(context.Background())
	if err != nil {
		t.Fatalf("incrementalAuthSync: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true when orphans are removed")
	}

	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("keep file should remain: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan file should be removed, stat err: %v", err)
	}
}

func TestIncrementalAuthSync_QueryError(t *testing.T) {
	s, mock, cleanup := newTestStore(t)
	defer cleanup()

	mock.ExpectQuery("SELECT id, content FROM").WillReturnError(sql.ErrConnDone)

	if _, err := s.incrementalAuthSync(context.Background()); err == nil {
		t.Fatal("expected error from failing query")
	}
}

func TestStartAuthSync_NonPositiveIntervalDisabled(t *testing.T) {
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	// Any non-positive interval must short-circuit: no goroutine started,
	// onChange must never fire.
	var called bool
	var mu sync.Mutex
	s.StartAuthSync(context.Background(), 0, func() {
		mu.Lock()
		called = true
		mu.Unlock()
	})

	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if called {
		t.Fatal("onChange was called with a non-positive interval; goroutine should not have started")
	}
}

func TestStartAuthSync_ContextCancelStops(t *testing.T) {
	s, mock, cleanup := newTestStore(t)
	defer cleanup()

	// The goroutine must exit when the supplied context is cancelled. We
	// can observe this by cancelling immediately and confirming that no
	// query is issued.
	mock.MatchExpectationsInOrder(false)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s.StartAuthSync(ctx, 10*time.Millisecond, nil)

	// Give the goroutine a chance to notice and exit.
	time.Sleep(40 * time.Millisecond)

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected queries after cancelled context: %v", err)
	}
}

func TestAbsoluteAuthPath_RejectsTraversal(t *testing.T) {
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	cases := []string{
		"../escape.json",
		"..",
		"nested/../../escape.json",
	}
	for _, id := range cases {
		if _, err := s.absoluteAuthPath(id); err == nil {
			t.Fatalf("absoluteAuthPath(%q) should have rejected traversal", id)
		}
	}
}

func TestAbsoluteAuthPath_AcceptsNested(t *testing.T) {
	s, _, cleanup := newTestStore(t)
	defer cleanup()

	got, err := s.absoluteAuthPath("claude/alice.json")
	if err != nil {
		t.Fatalf("absoluteAuthPath: %v", err)
	}
	want := filepath.Join(s.authDir, "claude", "alice.json")
	if got != want {
		t.Fatalf("absoluteAuthPath returned %q, want %q", got, want)
	}
}
