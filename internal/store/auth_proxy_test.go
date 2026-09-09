package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestStoresRestoreAccountProxyBeforeWatcherStarts(t *testing.T) {
	for _, backend := range []string{"git", "object", "postgres"} {
		for _, test := range []struct {
			name    string
			value   any
			present bool
			want    string
		}{
			{name: "HTTP proxy", value: "http://proxy.example:8080", present: true, want: "http://proxy.example:8080"},
			{name: "SOCKS proxy", value: "socks5://proxy.example:1080", present: true, want: "socks5://proxy.example:1080"},
			{name: "direct override", value: "direct", present: true, want: "direct"},
			{name: "whitespace trimmed", value: "  direct  ", present: true, want: "direct"},
			{name: "absent"},
			{name: "empty", value: "", present: true},
			{name: "null", present: true},
			{name: "non-string", value: 42, present: true},
		} {
			t.Run(backend+"/"+test.name, func(t *testing.T) {
				metadata := map[string]any{"type": "antigravity", "project_id": "fixture-project"}
				if test.present {
					metadata["proxy_url"] = test.value
				}
				payload, errMarshal := json.Marshal(metadata)
				if errMarshal != nil {
					t.Fatal(errMarshal)
				}
				dir := t.TempDir()
				var store cliproxyauth.Store
				switch backend {
				case "git":
					remote := setupGitRemoteRepository(t, dir, "main", testBranchSpec{name: "main", contents: "fixture\n"})
					gitStore := NewGitTokenStore(remote, "", "", "main")
					dir = filepath.Join(dir, "workspace", "auths")
					gitStore.SetBaseDir(dir)
					if errEnsure := gitStore.EnsureRepository(); errEnsure != nil {
						t.Fatal(errEnsure)
					}
					store = gitStore
				case "object":
					store = &ObjectTokenStore{authDir: dir}
				case "postgres":
					db := sql.OpenDB(&authProxyTestConnector{payload: string(payload)})
					t.Cleanup(func() {
						if errClose := db.Close(); errClose != nil {
							t.Errorf("close fixture database: %v", errClose)
						}
					})
					store = &PostgresStore{db: db, authDir: dir, cfg: PostgresStoreConfig{AuthTable: "auth_store"}}
				}
				if backend != "postgres" {
					if errWrite := os.WriteFile(filepath.Join(dir, "account.json"), payload, 0o600); errWrite != nil {
						t.Fatal(errWrite)
					}
				}
				manager := cliproxyauth.NewManager(store, nil, nil)
				defer manager.StopAutoRefresh()
				if errLoad := manager.Load(context.Background()); errLoad != nil {
					t.Fatal(errLoad)
				}
				auth, ok := manager.GetByID("account.json")
				if !ok {
					t.Fatal("account was not loaded")
				}
				if auth.ProxyURL != test.want {
					t.Errorf("ProxyURL = %q, want %q", auth.ProxyURL, test.want)
				}
			})
		}
	}
}

// Supply one persisted auth row without requiring a PostgreSQL server.
type authProxyTestConnector struct{ payload string }

func (c *authProxyTestConnector) Connect(context.Context) (driver.Conn, error) {
	return &authProxyTestConn{payload: c.payload}, nil
}
func (*authProxyTestConnector) Driver() driver.Driver { return authProxyTestDriver{} }

type authProxyTestDriver struct{}

func (authProxyTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("use the fixture connector")
}

type authProxyTestConn struct{ payload string }

func (*authProxyTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (*authProxyTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (*authProxyTestConn) Close() error { return nil }
func (c *authProxyTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &authProxyTestRows{payload: c.payload}, nil
}

type authProxyTestRows struct {
	payload string
	done    bool
}

func (*authProxyTestRows) Columns() []string {
	return []string{"id", "content", "created_at", "updated_at"}
}
func (*authProxyTestRows) Close() error { return nil }
func (r *authProxyTestRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0], dest[1] = "account.json", r.payload
	dest[2], dest[3] = time.Unix(1_700_000_000, 0), time.Unix(1_700_000_000, 0)
	return nil
}
