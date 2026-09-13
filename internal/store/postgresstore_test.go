package store

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/empty"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type postgresAuthTestCall struct {
	query string
	args  []driver.NamedValue
}

type postgresAuthTestBackend struct {
	mu            sync.Mutex
	calls         []postgresAuthTestCall
	failAt        int
	inspect       func(postgresAuthTestCall)
	content       []byte
	hasContent    bool
	unlockMissing bool
	unlockErr     error
}

func (b *postgresAuthTestBackend) call(query string, args []driver.NamedValue) error {
	if strings.Contains(query, "pg_advisory_") {
		return nil
	}
	call := postgresAuthTestCall{query: query, args: clonePostgresAuthTestArgs(args)}
	b.mu.Lock()
	b.calls = append(b.calls, call)
	callIndex := len(b.calls)
	inspect := b.inspect
	fail := b.failAt == callIndex
	b.mu.Unlock()
	if inspect != nil {
		inspect(call)
	}
	if fail {
		return errors.New("database rejected operation")
	}
	return nil
}

func (b *postgresAuthTestBackend) exec(query string, args []driver.NamedValue) (driver.Result, error) {
	if err := b.call(query, args); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return postgresAuthTestExec(query, args, &b.content, &b.hasContent)
}

func (b *postgresAuthTestBackend) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "hashtextextended") {
		return &postgresAuthTestRows{columns: []string{"key"}, values: [][]driver.Value{{int64(1)}}}, nil
	}
	if strings.Contains(query, "pg_advisory_unlock") {
		if b.unlockErr != nil {
			return nil, b.unlockErr
		}
		return &postgresAuthTestRows{columns: []string{"unlocked"}, values: [][]driver.Value{{!b.unlockMissing}}}, nil
	}
	if err := b.call(query, args); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return postgresAuthTestQuery(query, &b.content, &b.hasContent)
}

func (b *postgresAuthTestBackend) setContent(data []byte) {
	b.mu.Lock()
	b.content = append([]byte(nil), data...)
	b.hasContent = true
	b.mu.Unlock()
}

func (b *postgresAuthTestBackend) durableSnapshot() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.content...), b.hasContent
}

func (b *postgresAuthTestBackend) snapshotCalls() []postgresAuthTestCall {
	b.mu.Lock()
	defer b.mu.Unlock()
	calls := make([]postgresAuthTestCall, len(b.calls))
	copy(calls, b.calls)
	return calls
}

func postgresAuthTestExec(query string, args []driver.NamedValue, content *[]byte, hasContent *bool) (driver.Result, error) {
	trimmed := strings.TrimSpace(query)
	switch {
	case strings.HasPrefix(trimmed, "INSERT INTO"):
		if strings.Contains(query, "DO NOTHING") && *hasContent {
			return driver.RowsAffected(0), nil
		}
		*content = postgresAuthTestValue(args, 1)
		*hasContent = true
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(trimmed, "UPDATE"):
		if !*hasContent {
			return driver.RowsAffected(0), nil
		}
		if len(args) > 2 && !bytes.Equal(*content, postgresAuthTestValue(args, 2)) {
			return driver.RowsAffected(0), nil
		}
		*content = postgresAuthTestValue(args, 1)
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(trimmed, "DELETE") && len(args) > 1:
		if !*hasContent || !bytes.Equal(*content, postgresAuthTestValue(args, 1)) {
			return driver.RowsAffected(0), nil
		}
		*content = nil
		*hasContent = false
		return driver.RowsAffected(1), nil
	case strings.HasPrefix(trimmed, "DELETE"):
		*content = nil
		*hasContent = false
		return driver.RowsAffected(1), nil
	default:
		return driver.RowsAffected(1), nil
	}
}

func postgresAuthTestQuery(query string, content *[]byte, hasContent *bool) (driver.Rows, error) {
	trimmed := strings.TrimSpace(query)
	rows := &postgresAuthTestRows{columns: []string{"content"}}
	switch {
	case strings.HasPrefix(trimmed, "SELECT") && strings.Contains(query, "FOR UPDATE"):
		if *hasContent {
			rows.values = [][]driver.Value{{string(*content)}}
		}
	case strings.HasPrefix(trimmed, "DELETE") && strings.Contains(query, "RETURNING content"):
		if *hasContent {
			rows.values = [][]driver.Value{{string(*content)}}
			*content = nil
			*hasContent = false
		}
	default:
		return nil, errors.New("query unsupported")
	}
	return rows, nil
}

type postgresAuthTestRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *postgresAuthTestRows) Columns() []string { return r.columns }

func (*postgresAuthTestRows) Close() error { return nil }

func (r *postgresAuthTestRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

type postgresAuthTestConnector struct {
	backend *postgresAuthTestBackend
}

func (c *postgresAuthTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &postgresAuthTestConn{backend: c.backend}, nil
}

func (*postgresAuthTestConnector) Driver() driver.Driver { return postgresAuthTestDriver{} }

type postgresAuthTestDriver struct{}

func (postgresAuthTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use connector")
}

type postgresAuthTestConn struct {
	backend *postgresAuthTestBackend
	tx      *postgresAuthTestTx
}

func (*postgresAuthTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}

func (*postgresAuthTestConn) Close() error { return nil }

func (c *postgresAuthTestConn) Begin() (driver.Tx, error) {
	return c.beginTx()
}

func (c *postgresAuthTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if c.tx != nil {
		return c.tx.exec(query, args)
	}
	return c.backend.exec(query, args)
}

func (c *postgresAuthTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.tx != nil {
		return c.tx.query(query, args)
	}
	return c.backend.query(query, args)
}

func (c *postgresAuthTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.beginTx()
}

func (c *postgresAuthTestConn) beginTx() (driver.Tx, error) {
	if c.tx != nil {
		return nil, errors.New("transaction already active")
	}
	c.backend.mu.Lock()
	tx := &postgresAuthTestTx{
		conn:       c,
		content:    append([]byte(nil), c.backend.content...),
		hasContent: c.backend.hasContent,
	}
	c.backend.mu.Unlock()
	c.tx = tx
	return tx, nil
}

type postgresAuthTestTx struct {
	conn       *postgresAuthTestConn
	content    []byte
	hasContent bool
	done       bool
}

func (tx *postgresAuthTestTx) exec(query string, args []driver.NamedValue) (driver.Result, error) {
	if err := tx.conn.backend.call(query, args); err != nil {
		return nil, err
	}
	if strings.HasPrefix(strings.TrimSpace(query), "INSERT INTO") && strings.Contains(query, "DO NOTHING") && !tx.hasContent {
		tx.conn.backend.mu.Lock()
		if tx.conn.backend.hasContent {
			tx.content = append([]byte(nil), tx.conn.backend.content...)
			tx.hasContent = true
			tx.conn.backend.mu.Unlock()
			return driver.RowsAffected(0), nil
		}
		tx.conn.backend.mu.Unlock()
	}
	return postgresAuthTestExec(query, args, &tx.content, &tx.hasContent)
}

func (tx *postgresAuthTestTx) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	if err := tx.conn.backend.call(query, args); err != nil {
		return nil, err
	}
	return postgresAuthTestQuery(query, &tx.content, &tx.hasContent)
}

func (tx *postgresAuthTestTx) Commit() error {
	if tx.done {
		return errors.New("transaction already done")
	}
	tx.conn.backend.mu.Lock()
	tx.conn.backend.content = append([]byte(nil), tx.content...)
	tx.conn.backend.hasContent = tx.hasContent
	tx.conn.backend.mu.Unlock()
	tx.done = true
	tx.conn.tx = nil
	return nil
}

func (tx *postgresAuthTestTx) Rollback() error {
	if tx.done {
		return sql.ErrTxDone
	}
	tx.done = true
	tx.conn.tx = nil
	return nil
}

type postgresAuthTestStorage struct {
	path string
	data []byte
	mode fs.FileMode
}

func (s *postgresAuthTestStorage) SaveTokenToFile(path string) error {
	s.path = path
	mode := s.mode
	if mode == 0 {
		mode = 0o600
	}
	return os.WriteFile(path, s.data, mode)
}

func TestPostgresStoreSavePersistsBeforeLocalPublication(t *testing.T) {
	previous := []byte(`{"value":"previous"}`)
	backend := &postgresAuthTestBackend{}
	backend.setContent(previous)
	store := newPostgresAuthStoreForTest(t, backend)
	path := filepath.Join(store.authDir, "credential.json")
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatalf("write previous auth: %v", err)
	}
	store.renameFile = func(oldPath, newPath string) error {
		assertPostgresAuthLocal(t, path, previous)
		durable, exists := backend.durableSnapshot()
		if !exists {
			t.Fatal("database record missing before local publication")
		}
		assertPostgresAuthJSONValue(t, durable, "candidate")
		return os.Rename(oldPath, newPath)
	}

	_, err := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:       "credential.json",
		Metadata: map[string]any{"value": "candidate"},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	published, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read published auth: %v", errRead)
	}
	assertPostgresAuthJSONValue(t, published, "candidate")
	assertPostgresAuthNoTemp(t, path)
}

func TestPostgresStoreSaveDatabaseFailureDoesNotPublishLocalFile(t *testing.T) {
	for _, test := range []struct {
		name     string
		previous []byte
	}{
		{name: "existing", previous: []byte(`{"value":"previous"}`)},
		{name: "new"},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &postgresAuthTestBackend{failAt: 1}
			if test.previous != nil {
				backend.setContent(test.previous)
			}
			store := newPostgresAuthStoreForTest(t, backend)
			path := filepath.Join(store.authDir, "credential.json")
			if test.previous != nil {
				if err := os.WriteFile(path, test.previous, 0o600); err != nil {
					t.Fatalf("write previous auth: %v", err)
				}
			}

			_, err := store.Save(context.Background(), &cliproxyauth.Auth{
				ID:       "credential.json",
				Metadata: map[string]any{"value": "candidate"},
			})
			if err == nil {
				t.Fatal("Save() succeeded, want database error")
			}
			assertPostgresAuthLocal(t, path, test.previous)
			assertPostgresAuthNoTemp(t, path)
		})
	}
}

func TestPostgresStoreSavePublishFailureRestoresDatabase(t *testing.T) {
	t.Run("existing", func(t *testing.T) {
		localPrevious := []byte(`{"value":"local"}`)
		durablePrevious := []byte(`{"value":"durable"}`)
		backend := &postgresAuthTestBackend{}
		backend.setContent(durablePrevious)
		store := newPostgresAuthStoreForTest(t, backend)
		path := filepath.Join(store.authDir, "credential.json")
		if err := os.WriteFile(path, localPrevious, 0o600); err != nil {
			t.Fatalf("write previous auth: %v", err)
		}
		store.renameFile = func(string, string) error { return errors.New("publish rejected") }

		_, err := store.Save(context.Background(), &cliproxyauth.Auth{
			ID:       "credential.json",
			Metadata: map[string]any{"value": "candidate"},
		})
		if err == nil || !strings.Contains(err.Error(), "publish rejected") {
			t.Fatalf("Save() error = %v, want publish error", err)
		}
		durable, exists := backend.durableSnapshot()
		if !exists || !bytes.Equal(durable, durablePrevious) {
			t.Fatalf("database record = (%q, %t), want (%q, true)", durable, exists, durablePrevious)
		}
		calls := backend.snapshotCalls()
		if len(calls) != 3 || !strings.Contains(calls[2].query, "content = $3") {
			t.Fatalf("database calls = %#v, want conditional restore", calls)
		}
		assertPostgresAuthLocal(t, path, localPrevious)
		assertPostgresAuthNoTemp(t, path)
	})

	t.Run("new", func(t *testing.T) {
		backend := &postgresAuthTestBackend{}
		store := newPostgresAuthStoreForTest(t, backend)
		path := filepath.Join(store.authDir, "credential.json")
		store.renameFile = func(string, string) error { return errors.New("publish rejected") }

		_, err := store.Save(context.Background(), &cliproxyauth.Auth{
			ID:       "credential.json",
			Metadata: map[string]any{"value": "candidate"},
		})
		if err == nil || !strings.Contains(err.Error(), "publish rejected") {
			t.Fatalf("Save() error = %v, want publish error", err)
		}
		if durable, exists := backend.durableSnapshot(); exists {
			t.Fatalf("database record = %q, want missing", durable)
		}
		calls := backend.snapshotCalls()
		if len(calls) != 3 || !strings.Contains(calls[2].query, "content = $2") {
			t.Fatalf("database calls = %#v, want candidate-bound delete", calls)
		}
		assertPostgresAuthLocal(t, path, nil)
		assertPostgresAuthNoTemp(t, path)
	})
}

func TestPostgresStoreSaveRollbackConflictPreservesNewerDatabaseRecord(t *testing.T) {
	previous := []byte(`{"value":"previous"}`)
	newer := []byte(`{"value":"newer"}`)
	backend := &postgresAuthTestBackend{}
	backend.setContent(previous)
	backend.inspect = func(call postgresAuthTestCall) {
		if strings.HasPrefix(strings.TrimSpace(call.query), "UPDATE") && strings.Contains(call.query, "content = $3") {
			backend.setContent(newer)
		}
	}
	store := newPostgresAuthStoreForTest(t, backend)
	path := filepath.Join(store.authDir, "credential.json")
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatalf("write previous auth: %v", err)
	}
	store.renameFile = func(string, string) error { return errors.New("publish rejected") }

	_, err := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:       "credential.json",
		Metadata: map[string]any{"value": "candidate"},
	})
	if err == nil || !strings.Contains(err.Error(), "database rollback failed") {
		t.Fatalf("Save() error = %v, want rollback conflict", err)
	}
	durable, exists := backend.durableSnapshot()
	if !exists || !bytes.Equal(durable, newer) {
		t.Fatalf("database record = (%q, %t), want (%q, true)", durable, exists, newer)
	}
	assertPostgresAuthLocal(t, path, previous)
	assertPostgresAuthNoTemp(t, path)
}

func TestPostgresStoreSaveInsertConflictRestoresConcurrentRecord(t *testing.T) {
	localPrevious := []byte(`{"value":"local"}`)
	concurrent := []byte(`{"value":"concurrent"}`)
	backend := &postgresAuthTestBackend{}
	backend.inspect = func(call postgresAuthTestCall) {
		if strings.HasPrefix(strings.TrimSpace(call.query), "INSERT INTO") {
			backend.setContent(concurrent)
		}
	}
	store := newPostgresAuthStoreForTest(t, backend)
	path := filepath.Join(store.authDir, "credential.json")
	if err := os.WriteFile(path, localPrevious, 0o600); err != nil {
		t.Fatalf("write previous auth: %v", err)
	}
	store.renameFile = func(string, string) error { return errors.New("publish rejected") }

	_, err := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:       "credential.json",
		Metadata: map[string]any{"value": "candidate"},
	})
	if err == nil {
		t.Fatal("Save() succeeded, want publish error")
	}
	durable, exists := backend.durableSnapshot()
	if !exists || !bytes.Equal(durable, concurrent) {
		t.Fatalf("database record = (%q, %t), want (%q, true)", durable, exists, concurrent)
	}
	calls := backend.snapshotCalls()
	if len(calls) != 5 || !strings.Contains(calls[0].query, "FOR UPDATE") || !strings.Contains(calls[1].query, "DO NOTHING") || !strings.Contains(calls[2].query, "FOR UPDATE") || !strings.HasPrefix(strings.TrimSpace(calls[3].query), "UPDATE") || !strings.Contains(calls[4].query, "content = $3") {
		t.Fatalf("database calls = %#v, want insert conflict retry and conditional restore", calls)
	}
	assertPostgresAuthLocal(t, path, localPrevious)
	assertPostgresAuthNoTemp(t, path)
}

func TestPostgresStoreUnlockFailureDiscardsConnection(t *testing.T) {
	for _, test := range []struct {
		name    string
		missing bool
		err     error
	}{
		{name: "missing", missing: true},
		{name: "query-error", err: errors.New("unlock rejected")},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &postgresAuthTestBackend{unlockMissing: test.missing, unlockErr: test.err}
			store := newPostgresAuthStoreForTest(t, backend)
			err := store.withAuthLock(context.Background(), "credential.json", func(*sql.Conn) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "unlock auth record") {
				t.Fatalf("withAuthLock() error = %v", err)
			}
			if got := store.db.Stats().OpenConnections; got != 0 {
				t.Fatalf("open connections = %d, want failed session discarded", got)
			}
		})
	}
}

func TestPostgresStoreSaveUnchangedLocalPersistsDurableRecord(t *testing.T) {
	for _, test := range []struct {
		name         string
		previous     string
		metadataOnly bool
	}{
		{name: "storage-missing"},
		{name: "storage-stale", previous: `{"value":"old"}`},
		{name: "metadata-missing", metadataOnly: true},
		{name: "metadata-stale", previous: `{"value":"old"}`, metadataOnly: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &postgresAuthTestBackend{}
			if test.previous != "" {
				backend.setContent([]byte(test.previous))
			}
			store := newPostgresAuthStoreForTest(t, backend)
			candidate := []byte(`{"disabled":false,"value":"candidate"}`)
			path := filepath.Join(store.authDir, "credential.json")
			if err := os.WriteFile(path, candidate, 0o600); err != nil {
				t.Fatal(err)
			}
			store.renameFile = func(string, string) error {
				t.Fatal("unchanged local file should not be replaced")
				return nil
			}
			auth := &cliproxyauth.Auth{ID: "credential.json", Storage: &postgresAuthTestStorage{data: candidate}}
			if test.metadataOnly {
				auth.Storage = nil
				auth.Metadata = map[string]any{"value": "candidate"}
			}
			if _, err := store.Save(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			durable, exists := backend.durableSnapshot()
			if !exists || !bytes.Equal(durable, candidate) {
				t.Fatalf("durable record = %q, exists = %t", durable, exists)
			}
			assertPostgresAuthNoTemp(t, path)
		})
	}
}

func TestPostgresStoreSaveMissingOutputFails(t *testing.T) {
	backend := &postgresAuthTestBackend{}
	store := newPostgresAuthStoreForTest(t, backend)
	auth := &cliproxyauth.Auth{ID: "credential.json", Storage: &empty.EmptyStorage{}}
	path, err := store.Save(context.Background(), auth)
	if err == nil || path != "" || auth.Attributes[cliproxyauth.AttributeSourceBackend] != "" {
		t.Fatalf("Save() = (%q, %v), attributes = %v", path, err, auth.Attributes)
	}
	if len(backend.snapshotCalls()) != 0 {
		t.Fatal("missing output must not reach the database")
	}
	assertPostgresAuthNoTemp(t, filepath.Join(store.authDir, auth.ID))
}

func TestPostgresStoreSaveUsesTemporaryPathForTokenStorage(t *testing.T) {
	backend := &postgresAuthTestBackend{}
	store := newPostgresAuthStoreForTest(t, backend)
	storage := &postgresAuthTestStorage{data: []byte(`{"type":"codex","token":"value"}`), mode: 0o644}
	store.renameFile = func(oldPath, newPath string) error {
		if got, want := oldPath, newPath+".tmp"; got != want {
			t.Fatalf("temporary path = %q, want %q", got, want)
		}
		info, errStat := os.Stat(oldPath)
		if errStat != nil {
			t.Fatalf("stat temporary auth: %v", errStat)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("temporary auth mode = %04o, want 0600", got)
		}
		return os.Rename(oldPath, newPath)
	}
	auth := &cliproxyauth.Auth{
		ID:       "credential.json",
		Metadata: map[string]any{"type": "codex"},
		Storage:  storage,
	}

	path, err := store.Save(context.Background(), auth)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if storage.path != path+".tmp" {
		t.Fatalf("storage path = %q, want %q", storage.path, path+".tmp")
	}
	assertPostgresAuthLocal(t, path, storage.data)
	if got := auth.Attributes[cliproxyauth.AttributeSourceBackend]; got != cliproxyauth.AuthSourcePostgres {
		t.Fatalf("source backend = %q, want %q", got, cliproxyauth.AuthSourcePostgres)
	}
	assertPostgresAuthNoTemp(t, path)
}

func TestPostgresStoreSaveEmptyPayloadDeletesDurableRecord(t *testing.T) {
	previous := []byte(`{"value":"previous"}`)
	backend := &postgresAuthTestBackend{}
	backend.setContent(previous)
	store := newPostgresAuthStoreForTest(t, backend)
	path := filepath.Join(store.authDir, "credential.json")
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatalf("write previous auth: %v", err)
	}

	gotPath, err := store.Save(context.Background(), &cliproxyauth.Auth{
		ID:      "credential.json",
		Storage: &postgresAuthTestStorage{},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if gotPath != path {
		t.Fatalf("Save() path = %q, want %q", gotPath, path)
	}
	if durable, exists := backend.durableSnapshot(); exists {
		t.Fatalf("database record = %q, want missing", durable)
	}
	assertPostgresAuthLocal(t, path, []byte{})
	assertPostgresAuthNoTemp(t, path)
}

func newPostgresAuthStoreForTest(t *testing.T, backend *postgresAuthTestBackend) *PostgresStore {
	t.Helper()
	authDir := filepath.Join(t.TempDir(), "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("create auth dir: %v", err)
	}
	db := sql.OpenDB(&postgresAuthTestConnector{backend: backend})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Errorf("close database: %v", errClose)
		}
	})
	return &PostgresStore{
		db:      db,
		cfg:     PostgresStoreConfig{AuthTable: defaultAuthTable},
		authDir: authDir,
	}
}

func assertPostgresAuthJSONValue(t *testing.T, data []byte, want string) {
	t.Helper()
	if got := string(data); !strings.Contains(got, `"value":"`+want+`"`) {
		t.Fatalf("auth JSON = %q, want value %q", got, want)
	}
}

func assertPostgresAuthLocal(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if want == nil {
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("read local auth error = %v, want not exist", err)
		}
		return
	}
	if err != nil {
		t.Fatalf("read local auth: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("local auth bytes = %q, want %q", got, want)
	}
}

func assertPostgresAuthNoTemp(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat temp auth error = %v, want not exist", err)
	}
}

func clonePostgresAuthTestArgs(args []driver.NamedValue) []driver.NamedValue {
	cloned := make([]driver.NamedValue, len(args))
	copy(cloned, args)
	for i := range cloned {
		if value, ok := cloned[i].Value.([]byte); ok {
			cloned[i].Value = append([]byte(nil), value...)
		}
	}
	return cloned
}

func postgresAuthTestValue(args []driver.NamedValue, index int) []byte {
	if index >= len(args) {
		return nil
	}
	switch value := args[index].Value.(type) {
	case []byte:
		return append([]byte(nil), value...)
	case string:
		return []byte(value)
	default:
		return nil
	}
}

var _ driver.ExecerContext = (*postgresAuthTestConn)(nil)
var _ driver.QueryerContext = (*postgresAuthTestConn)(nil)
var _ driver.ConnBeginTx = (*postgresAuthTestConn)(nil)
var _ driver.Connector = (*postgresAuthTestConnector)(nil)
