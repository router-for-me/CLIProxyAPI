package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

var cooldownTestDriverID atomic.Uint64

type cooldownTestDriver struct {
	state *cooldownTestState
}

type cooldownTestState struct {
	mu      sync.Mutex
	rows    map[string]cooldownTestRow
	queries []string
}

type cooldownTestRow struct {
	content   []byte
	deleted   bool
	updatedAt time.Time
}

type cooldownTestConn struct {
	state *cooldownTestState
}

type cooldownTestTx struct{}

type cooldownTestRows struct {
	rows  []cooldownTestRow
	index int
}

func (d *cooldownTestDriver) Open(string) (driver.Conn, error) {
	return &cooldownTestConn{state: d.state}, nil
}

func (c *cooldownTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *cooldownTestConn) Close() error {
	return nil
}

func (c *cooldownTestConn) Begin() (driver.Tx, error) {
	return &cooldownTestTx{}, nil
}

func (c *cooldownTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.queries = append(c.state.queries, query)
	if !strings.Contains(query, "INSERT INTO") || (len(args) != 4 && len(args) != 5) {
		return driver.RowsAffected(1), nil
	}
	authID, okAuthID := args[0].Value.(string)
	model, okModel := args[1].Value.(string)
	content, okContent := args[2].Value.([]byte)
	updatedAt, okUpdatedAt := args[3].Value.(time.Time)
	if !okAuthID || !okModel || !okContent || !okUpdatedAt {
		return nil, errors.New("invalid cooldown query arguments")
	}
	key := authID + "\x00" + model
	current, exists := c.state.rows[key]
	if len(args) == 4 {
		if !exists || !current.updatedAt.After(updatedAt) {
			c.state.rows[key] = cooldownTestRow{content: append([]byte(nil), content...), updatedAt: updatedAt}
		}
		return driver.RowsAffected(1), nil
	}
	observedAt, okObservedAt := args[4].Value.(time.Time)
	if !okObservedAt {
		return nil, errors.New("invalid cooldown delete version")
	}
	if !exists || (!current.deleted && !current.updatedAt.After(observedAt)) {
		c.state.rows[key] = cooldownTestRow{content: append([]byte(nil), content...), deleted: true, updatedAt: updatedAt}
	}
	return driver.RowsAffected(1), nil
}

func (c *cooldownTestConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.queries = append(c.state.queries, query)
	rows := make([]cooldownTestRow, 0, len(c.state.rows))
	for _, row := range c.state.rows {
		if !row.deleted {
			row.content = append([]byte(nil), row.content...)
			rows = append(rows, row)
		}
	}
	return &cooldownTestRows{rows: rows}, nil
}

func (*cooldownTestTx) Commit() error {
	return nil
}

func (*cooldownTestTx) Rollback() error {
	return nil
}

func (r *cooldownTestRows) Columns() []string {
	return []string{"content", "updated_at"}
}

func (r *cooldownTestRows) Close() error {
	return nil
}

func (r *cooldownTestRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	dest[0] = r.rows[r.index].content
	dest[1] = r.rows[r.index].updatedAt
	r.index++
	return nil
}

func TestPostgresCooldownStateStore_SaveLoad(t *testing.T) {
	state := &cooldownTestState{rows: make(map[string]cooldownTestRow)}
	driverName := fmt.Sprintf("cliproxy_postgres_cooldown_test_%d", cooldownTestDriverID.Add(1))
	sql.Register(driverName, &cooldownTestDriver{state: state})
	db, errOpen := sql.Open(driverName, "")
	if errOpen != nil {
		t.Fatalf("sql.Open() error = %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Errorf("db.Close() error = %v", errClose)
		}
	})

	postgresStore := &PostgresStore{
		db: db,
		cfg: PostgresStoreConfig{
			ConfigTable:   defaultConfigTable,
			AuthTable:     defaultAuthTable,
			CooldownTable: defaultCooldownTable,
		},
	}
	cooldownStore := &postgresCooldownStateStore{store: postgresStore}
	postgresStore.cooldownStore = cooldownStore

	if errSchema := postgresStore.EnsureSchema(context.Background()); errSchema != nil {
		t.Fatalf("EnsureSchema() error = %v", errSchema)
	}
	if got := postgresStore.CooldownStateStore(); got != cooldownStore {
		t.Fatalf("CooldownStateStore() = %T, want configured PostgreSQL store", got)
	}

	nextRetry := time.Date(2026, time.March, 15, 12, 0, 0, 0, time.UTC)
	records := []cliproxyauth.CooldownStateRecord{
		{
			Provider:       "codex",
			AuthID:         "account-1",
			Model:          "gpt-test",
			Status:         string(cliproxyauth.StatusError),
			NextRetryAfter: nextRetry,
			Reason:         "rate limited",
			UpdatedAt:      nextRetry.Add(-time.Minute),
		},
	}
	if errSave := cooldownStore.Save(context.Background(), records); errSave != nil {
		t.Fatalf("Save() error = %v", errSave)
	}
	loaded, errLoad := cooldownStore.Load(context.Background())
	if errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	if !reflect.DeepEqual(loaded, records) {
		t.Fatalf("Load() = %#v, want %#v", loaded, records)
	}

	zeroTimeRecord := cliproxyauth.CooldownStateRecord{AuthID: "account-2", Model: "gpt-test"}
	if errSave := cooldownStore.Save(context.Background(), []cliproxyauth.CooldownStateRecord{zeroTimeRecord}); errSave != nil {
		t.Fatalf("Save() with zero UpdatedAt error = %v", errSave)
	}
	loaded, errLoad = cooldownStore.Load(context.Background())
	if errLoad != nil {
		t.Fatalf("Load() after zero UpdatedAt error = %v", errLoad)
	}
	if len(loaded) != 1 || loaded[0].UpdatedAt.IsZero() {
		t.Fatalf("Load() did not persist a normalized UpdatedAt: %#v", loaded)
	}

	if errSave := cooldownStore.Save(context.Background(), nil); errSave != nil {
		t.Fatalf("Save(nil) error = %v", errSave)
	}
	loaded, errLoad = cooldownStore.Load(context.Background())
	if errLoad != nil {
		t.Fatalf("Load() after Save(nil) error = %v", errLoad)
	}
	if len(loaded) != 0 {
		t.Fatalf("Load() after Save(nil) returned %d records, want 0", len(loaded))
	}

	state.mu.Lock()
	queries := strings.Join(state.queries, "\n")
	state.mu.Unlock()
	if !strings.Contains(queries, `CREATE TABLE IF NOT EXISTS "cooldown_store"`) {
		t.Fatalf("EnsureSchema() did not create cooldown table; queries:\n%s", queries)
	}
}

func TestPostgresCooldownStateStore_MergesConcurrentInstances(t *testing.T) {
	state := &cooldownTestState{rows: make(map[string]cooldownTestRow)}
	driverName := fmt.Sprintf("cliproxy_postgres_cooldown_merge_test_%d", cooldownTestDriverID.Add(1))
	sql.Register(driverName, &cooldownTestDriver{state: state})
	db, errOpen := sql.Open(driverName, "")
	if errOpen != nil {
		t.Fatalf("sql.Open() error = %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Errorf("db.Close() error = %v", errClose)
		}
	})
	postgresStore := &PostgresStore{
		db:  db,
		cfg: PostgresStoreConfig{CooldownTable: defaultCooldownTable},
	}
	storeA := &postgresCooldownStateStore{store: postgresStore}
	storeB := &postgresCooldownStateStore{store: postgresStore}
	staleStore := &postgresCooldownStateStore{store: postgresStore}

	for _, cooldownStore := range []*postgresCooldownStateStore{storeA, storeB} {
		if _, errLoad := cooldownStore.Load(context.Background()); errLoad != nil {
			t.Fatalf("initial Load() error = %v", errLoad)
		}
	}
	updatedAt := time.Now().UTC().Add(-time.Minute)
	recordA := cliproxyauth.CooldownStateRecord{AuthID: "account-a", Model: "model-a", UpdatedAt: updatedAt}
	recordB := cliproxyauth.CooldownStateRecord{AuthID: "account-b", Model: "model-b", UpdatedAt: updatedAt}
	if errSave := storeA.Save(context.Background(), []cliproxyauth.CooldownStateRecord{recordA}); errSave != nil {
		t.Fatalf("storeA.Save() error = %v", errSave)
	}
	if errSave := storeB.Save(context.Background(), []cliproxyauth.CooldownStateRecord{recordB}); errSave != nil {
		t.Fatalf("storeB.Save() error = %v", errSave)
	}
	staleRecords, errLoad := staleStore.Load(context.Background())
	if errLoad != nil {
		t.Fatalf("staleStore.Load() error = %v", errLoad)
	}
	if len(staleRecords) != 2 {
		t.Fatalf("merged Load() returned %d records, want 2", len(staleRecords))
	}

	newerRecordA := recordA
	newerRecordA.UpdatedAt = updatedAt.Add(time.Hour)
	if errSave := storeA.Save(context.Background(), []cliproxyauth.CooldownStateRecord{newerRecordA}); errSave != nil {
		t.Fatalf("storeA.Save(newer) error = %v", errSave)
	}
	if errSave := staleStore.Save(context.Background(), []cliproxyauth.CooldownStateRecord{recordB}); errSave != nil {
		t.Fatalf("staleStore.Save(without newer record) error = %v", errSave)
	}
	resurrectStore := &postgresCooldownStateStore{store: postgresStore}
	activeRecords, errLoad := resurrectStore.Load(context.Background())
	if errLoad != nil {
		t.Fatalf("resurrectStore.Load() error = %v", errLoad)
	}
	if len(activeRecords) != 2 {
		t.Fatalf("Load() after stale delete returned %d records, want 2", len(activeRecords))
	}

	if errSave := storeA.Save(context.Background(), nil); errSave != nil {
		t.Fatalf("storeA.Save(nil) error = %v", errSave)
	}
	if errSave := resurrectStore.Save(context.Background(), activeRecords); errSave != nil {
		t.Fatalf("resurrectStore.Save() error = %v", errSave)
	}
	reader := &postgresCooldownStateStore{store: postgresStore}
	loaded, errLoad := reader.Load(context.Background())
	if errLoad != nil {
		t.Fatalf("reader.Load() error = %v", errLoad)
	}
	if len(loaded) != 1 || loaded[0].AuthID != recordB.AuthID {
		t.Fatalf("Load() after stale save = %#v, want only account-b", loaded)
	}
}
