package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

var refreshSQLDriverID atomic.Int64

type refreshSQLDriver struct {
	mu       sync.Mutex
	schemaMu sync.Mutex
	failDDL  bool
	ddlCalls int
	rows     map[string][]byte
}

type refreshSQLConn struct {
	database     *refreshSQLDriver
	schemaLocked bool
}
type refreshSQLTx struct{ conn *refreshSQLConn }

func (tx *refreshSQLTx) Commit() error {
	if tx.conn.schemaLocked {
		tx.conn.schemaLocked = false
		tx.conn.database.schemaMu.Unlock()
	}
	return nil
}
func (tx *refreshSQLTx) Rollback() error { return tx.Commit() }

type refreshSQLRows struct {
	receipt []byte
	found   bool
}

func (d *refreshSQLDriver) Open(string) (driver.Conn, error) {
	return &refreshSQLConn{database: d}, nil
}
func (c *refreshSQLConn) Close() error { return nil }
func (c *refreshSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("unexpected prepare")
}
func (c *refreshSQLConn) Begin() (driver.Tx, error) { return &refreshSQLTx{conn: c}, nil }
func (c *refreshSQLConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.HasPrefix(query, "SELECT pg_advisory_xact_lock(") {
		c.database.schemaMu.Lock()
		c.schemaLocked = true
		return driver.RowsAffected(0), nil
	}
	c.database.mu.Lock()
	defer c.database.mu.Unlock()
	if strings.HasPrefix(query, "CREATE TABLE IF NOT EXISTS ") {
		if !c.schemaLocked {
			return nil, fmt.Errorf("DDL must hold a database transaction lock")
		}
		c.database.ddlCalls++
		if c.database.failDDL {
			c.database.failDDL = false
			return nil, fmt.Errorf("temporary schema failure")
		}
		return driver.RowsAffected(0), nil
	}
	key := args[0].Value.(string)
	if strings.HasPrefix(query, "INSERT INTO ") && strings.Contains(query, "ON CONFLICT (id) DO NOTHING") {
		if _, exists := c.database.rows[key]; exists {
			return driver.RowsAffected(0), nil
		}
		c.database.rows[key] = nil
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "UPDATE ") && strings.Contains(query, "AND receipt IS NULL") {
		if data, exists := c.database.rows[key]; !exists || data != nil {
			return driver.RowsAffected(0), nil
		}
		c.database.rows[key] = append([]byte(nil), args[1].Value.([]byte)...)
		return driver.RowsAffected(1), nil
	}
	if strings.HasPrefix(query, "DELETE FROM ") && strings.Contains(query, "AND receipt IS NULL") {
		if data, exists := c.database.rows[key]; !exists || data != nil {
			return driver.RowsAffected(0), nil
		}
		delete(c.database.rows, key)
		return driver.RowsAffected(1), nil
	}
	return nil, fmt.Errorf("unexpected journal SQL: %s", query)
}
func (c *refreshSQLConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(query, "SELECT receipt FROM ") {
		return nil, fmt.Errorf("unexpected journal query: %s", query)
	}
	c.database.mu.Lock()
	defer c.database.mu.Unlock()
	data, exists := c.database.rows[args[0].Value.(string)]
	return &refreshSQLRows{receipt: append([]byte(nil), data...), found: exists}, nil
}
func (r *refreshSQLRows) Columns() []string { return []string{"receipt"} }
func (r *refreshSQLRows) Close() error      { return nil }
func (r *refreshSQLRows) Next(values []driver.Value) error {
	if !r.found {
		return io.EOF
	}
	r.found = false
	values[0] = r.receipt
	return nil
}

func TestPostgresRefreshJournalSurvivesMirrorReplacement(t *testing.T) {
	database := &refreshSQLDriver{rows: make(map[string][]byte)}
	name := fmt.Sprintf("refresh_journal_%d", refreshSQLDriverID.Add(1))
	sql.Register(name, database)
	var journals []codex.RefreshJournal
	for i := 0; i < 2; i++ {
		db, errOpen := sql.Open(name, "")
		if errOpen != nil {
			t.Fatal(errOpen)
		}
		t.Cleanup(func() {
			if errClose := db.Close(); errClose != nil {
				t.Error(errClose)
			}
		})
		journals = append(journals, &PostgresStore{db: db, cfg: PostgresStoreConfig{Schema: "isolated", AuthTable: "auths"}, authDir: t.TempDir()})
	}
	exerciseSharedRefreshJournal(t, journals)
	// A new client with a newly-created mirror still sees the database claim.
	prior := journals[0].(*PostgresStore)
	rebuilt := &PostgresStore{db: prior.db, cfg: prior.cfg, authDir: t.TempDir()}
	claimed, errClaim := rebuilt.RefreshClaim(context.Background(), strings.Repeat("b", 64))
	if claimed || errClaim != nil {
		t.Fatalf("mirror rebuild reclaimed grant: %v, %v", claimed, errClaim)
	}
}

func TestPostgresRefreshSchemaRetriesFailureAndCachesSuccess(t *testing.T) {
	database := &refreshSQLDriver{rows: make(map[string][]byte), failDDL: true}
	name := fmt.Sprintf("refresh_journal_%d", refreshSQLDriverID.Add(1))
	sql.Register(name, database)
	db, errOpen := sql.Open(name, "")
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	t.Cleanup(func() {
		if errClose := db.Close(); errClose != nil {
			t.Error(errClose)
		}
	})
	store := &PostgresStore{db: db, cfg: PostgresStoreConfig{AuthTable: "auths"}}
	key := strings.Repeat("a", 64)
	if claimed, errClaim := store.RefreshClaim(context.Background(), key); claimed || errClaim == nil {
		t.Fatalf("failed initialization allowed claim: %v, %v", claimed, errClaim)
	}
	if claimed, errClaim := store.RefreshClaim(context.Background(), key); !claimed || errClaim != nil {
		t.Fatalf("recovered initialization did not allow claim: %v, %v", claimed, errClaim)
	}
	if claimed, errClaim := store.RefreshClaim(context.Background(), strings.Repeat("b", 64)); !claimed || errClaim != nil {
		t.Fatalf("subsequent claim failed: %v, %v", claimed, errClaim)
	}
	if database.ddlCalls != 2 {
		t.Fatalf("DDL calls=%d, want one failure and one success", database.ddlCalls)
	}
}
