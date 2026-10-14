package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
)

// seedTestDriver serves an empty config table and records config upserts.
type seedTestDriver struct {
	mu       sync.Mutex
	upserted []string
}

type seedTestConn struct{ driver *seedTestDriver }

type seedTestRows struct{}

func (d *seedTestDriver) Open(string) (driver.Conn, error) { return &seedTestConn{driver: d}, nil }

func (*seedTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (*seedTestConn) Close() error { return nil }

func (*seedTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *seedTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "INSERT INTO") && len(args) == 2 {
		content, ok := args[1].Value.(string)
		if !ok {
			return nil, errors.New("invalid config upsert arguments")
		}
		c.driver.mu.Lock()
		c.driver.upserted = append(c.driver.upserted, content)
		c.driver.mu.Unlock()
	}
	return driver.RowsAffected(1), nil
}

func (*seedTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return seedTestRows{}, nil
}

func (seedTestRows) Columns() []string { return []string{"content"} }

func (seedTestRows) Close() error { return nil }

func (seedTestRows) Next([]driver.Value) error { return io.EOF }

// A Postgres store whose config table is empty is seeded from the local file.
func TestPostgresStoreSeedsDatabaseWithoutPlaintextSecret(t *testing.T) {
	for name, tc := range seedCases {
		t.Run(name, func(t *testing.T) {
			seedDriver := &seedTestDriver{}
			driverName := fmt.Sprintf("postgres-config-seed-test-%d", cooldownTestDriverID.Add(1))
			sql.Register(driverName, seedDriver)
			db, errOpen := sql.Open(driverName, "")
			if errOpen != nil {
				t.Fatalf("sql.Open() error = %v", errOpen)
			}
			t.Cleanup(func() {
				if errClose := db.Close(); errClose != nil {
					t.Errorf("close db: %v", errClose)
				}
			})
			store := &PostgresStore{db: db, configPath: writeSeedConfig(t, tc.content, 0o600)}
			if err := store.syncConfigFromDatabase(context.Background(), ""); err != nil {
				t.Fatalf("syncConfigFromDatabase() error = %v", err)
			}
			seedDriver.mu.Lock()
			defer seedDriver.mu.Unlock()
			if len(seedDriver.upserted) != 1 {
				t.Fatalf("config upserts = %d, want 1", len(seedDriver.upserted))
			}
			checkSeeded(t, tc.content, tc.unchanged, []byte(seedDriver.upserted[0]))
		})
	}
}
