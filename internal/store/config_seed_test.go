package store

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

const seedTestPlaintextSecret = "seed-plaintext-secret"

// seedTestConfigsKeepingPlaintext hold a plaintext secret that cannot be removed before
// seeding: the file cannot be parsed, or a YAML anchor keeps the plaintext after the
// referencing key is hashed or dropped as a conflicting legacy field.
var seedTestConfigsKeepingPlaintext = map[string]string{
	"unparsable":      "remote-management:\n  secret-key: " + seedTestPlaintextSecret + "\nport: [8317\n",
	"anchored secret": "defaults: &management\n  secret-key: " + seedTestPlaintextSecret + "\nmanagement: *management\n",
	"anchored legacy secret beside v8 secret": "defaults: &legacy\n  secret-key: " + seedTestPlaintextSecret + "\nremote-management: *legacy\nmanagement:\n  secret-key: current-plaintext-secret\n",
}

var seedTestDriverID atomic.Uint64

func seedTestPlaintextConfig(section string) string {
	return "# operator note\nport: 8317\n" + section + ":\n  secret-key: " + seedTestPlaintextSecret + "\n"
}

// seedTestSettledConfigs hold no plaintext secret, so seeding must upload them unchanged.
func seedTestSettledConfigs(t *testing.T) map[string]string {
	t.Helper()
	hashed, errHash := bcrypt.GenerateFromPassword([]byte(seedTestPlaintextSecret), bcrypt.MinCost)
	if errHash != nil {
		t.Fatalf("bcrypt.GenerateFromPassword() error = %v", errHash)
	}
	return map[string]string{
		"hashed secret": "# operator note\nport: 8317\nremote-management:\n  secret-key: '" + string(hashed) + "'\n",
		"null secret":   "# operator note\nport: 8317\nmanagement:\n  secret-key: null\n",
	}
}

func writeSeedTestFile(t *testing.T, path, content string) {
	t.Helper()
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		t.Fatalf("create directory: %v", errMkdir)
	}
	if errWrite := os.WriteFile(path, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("write %s: %v", path, errWrite)
	}
}

func readSeedTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read %s: %v", path, errRead)
	}
	return data
}

// assertSeededWithHashedSecret checks that a seeded config carries the bcrypt hash of
// the plaintext secret under section and matches the local file, because later starts
// overwrite the local file with the remote copy.
func assertSeededWithHashedSecret(t *testing.T, seeded []byte, configPath, section string) {
	t.Helper()
	if bytes.Contains(seeded, []byte(seedTestPlaintextSecret)) {
		t.Fatalf("remote store seeded with the plaintext management secret:\n%s", seeded)
	}
	var doc map[string]any
	if errParse := yaml.Unmarshal(seeded, &doc); errParse != nil {
		t.Fatalf("parse seeded config: %v", errParse)
	}
	management, _ := doc[section].(map[string]any)
	secret, _ := management["secret-key"].(string)
	if errCompare := bcrypt.CompareHashAndPassword([]byte(secret), []byte(seedTestPlaintextSecret)); errCompare != nil {
		t.Fatalf("seeded %s.secret-key %q is not the bcrypt hash of the plaintext secret: %v", section, secret, errCompare)
	}
	if local := readSeedTestFile(t, configPath); !bytes.Equal(local, seeded) {
		t.Fatalf("seeded config differs from the local file:\nremote:\n%s\nlocal:\n%s", seeded, local)
	}
}

// newSeedTestObjectStore returns an object store whose bucket has no config object and
// records every config upload.
func newSeedTestObjectStore(t *testing.T, configPath string) (*ObjectTokenStore, *[][]byte) {
	t.Helper()
	// Every request answers 404, so the bucket holds no config.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	client, errClient := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Region: "us-east-1",
	})
	if errClient != nil {
		t.Fatalf("minio.New() error = %v", errClient)
	}
	uploads := &[][]byte{}
	objectStore := &ObjectTokenStore{
		client:     client,
		cfg:        ObjectStoreConfig{Bucket: "bucket"},
		configPath: configPath,
		putObjectFn: func(_ context.Context, key string, data []byte, _ string) error {
			if key != objectStoreConfigKey {
				return fmt.Errorf("unexpected object upload %s", key)
			}
			*uploads = append(*uploads, append([]byte(nil), data...))
			return nil
		},
	}
	return objectStore, uploads
}

func TestObjectTokenStoreSeedHashesPlaintextManagementSecret(t *testing.T) {
	dir := t.TempDir()
	examplePath := filepath.Join(dir, "config.example.yaml")
	writeSeedTestFile(t, examplePath, seedTestPlaintextConfig("remote-management"))
	configPath := filepath.Join(dir, "objectstore", "config", "config.yaml")
	objectStore, uploads := newSeedTestObjectStore(t, configPath)

	if errSync := objectStore.syncConfigFromBucket(context.Background(), examplePath); errSync != nil {
		t.Fatalf("syncConfigFromBucket() error = %v", errSync)
	}
	if len(*uploads) != 1 {
		t.Fatalf("config uploads = %d, want 1", len(*uploads))
	}
	assertSeededWithHashedSecret(t, (*uploads)[0], configPath, "remote-management")
}

func TestObjectTokenStoreSeedUploadsSettledConfigUnchanged(t *testing.T) {
	for name, original := range seedTestSettledConfigs(t) {
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			writeSeedTestFile(t, configPath, original)
			objectStore, uploads := newSeedTestObjectStore(t, configPath)

			if errSync := objectStore.syncConfigFromBucket(context.Background(), ""); errSync != nil {
				t.Fatalf("syncConfigFromBucket() error = %v", errSync)
			}
			if len(*uploads) != 1 || string((*uploads)[0]) != original {
				t.Fatalf("config uploads = %q, want exactly the original config %q", *uploads, original)
			}
			if local := readSeedTestFile(t, configPath); string(local) != original {
				t.Fatalf("local config rewritten to %q, want it untouched", local)
			}
		})
	}
}

// Bootstrap must fail rather than seed, and keep the local file as it was: a later
// start finds the same file and has to refuse it again.
func TestObjectTokenStoreSeedRefusesConfigThatKeepsPlaintextSecret(t *testing.T) {
	for name, content := range seedTestConfigsKeepingPlaintext {
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			writeSeedTestFile(t, configPath, content)
			objectStore, uploads := newSeedTestObjectStore(t, configPath)

			for attempt := 1; attempt <= 2; attempt++ {
				if errSync := objectStore.syncConfigFromBucket(context.Background(), ""); errSync == nil {
					t.Fatalf("attempt %d: syncConfigFromBucket() succeeded, want bootstrap to fail", attempt)
				}
			}
			if len(*uploads) != 0 {
				t.Fatalf("uploaded a config that still holds the plaintext secret:\n%s", (*uploads)[0])
			}
			if local := readSeedTestFile(t, configPath); string(local) != content {
				t.Fatalf("local config rewritten to %q, want the original kept", local)
			}
		})
	}
}

// seedTestDatabase is a database/sql driver whose config table is empty; it records
// every config upsert.
type seedTestDatabase struct {
	mu      sync.Mutex
	upserts []string
}

type seedTestConn struct {
	database *seedTestDatabase
}

type seedTestRows struct{}

func (d *seedTestDatabase) Open(string) (driver.Conn, error) {
	return &seedTestConn{database: d}, nil
}

func (d *seedTestDatabase) recordedUpserts() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.upserts...)
}

func (*seedTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (*seedTestConn) Close() error {
	return nil
}

func (*seedTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *seedTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.Contains(query, "INSERT INTO") || len(args) != 2 {
		return nil, fmt.Errorf("unexpected statement %q", query)
	}
	content, ok := args[1].Value.(string)
	if !ok {
		return nil, errors.New("config content is not a string")
	}
	c.database.mu.Lock()
	c.database.upserts = append(c.database.upserts, content)
	c.database.mu.Unlock()
	return driver.RowsAffected(1), nil
}

// QueryContext serves an empty config table.
func (*seedTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return seedTestRows{}, nil
}

func (seedTestRows) Columns() []string {
	return []string{"content"}
}

func (seedTestRows) Close() error {
	return nil
}

func (seedTestRows) Next([]driver.Value) error {
	return io.EOF
}

func newSeedTestPostgresStore(t *testing.T, configPath string) (*PostgresStore, *seedTestDatabase) {
	t.Helper()
	database := &seedTestDatabase{}
	driverName := fmt.Sprintf("cliproxy_postgres_config_seed_test_%d", seedTestDriverID.Add(1))
	sql.Register(driverName, database)
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
		db:         db,
		cfg:        PostgresStoreConfig{ConfigTable: defaultConfigTable},
		configPath: configPath,
	}
	return postgresStore, database
}

func TestPostgresStoreSeedHashesPlaintextManagementSecret(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "pgstore", "config", "config.yaml")
	writeSeedTestFile(t, configPath, seedTestPlaintextConfig("management"))
	postgresStore, database := newSeedTestPostgresStore(t, configPath)

	if errSync := postgresStore.syncConfigFromDatabase(context.Background(), ""); errSync != nil {
		t.Fatalf("syncConfigFromDatabase() error = %v", errSync)
	}
	upserts := database.recordedUpserts()
	if len(upserts) != 1 {
		t.Fatalf("config upserts = %d, want 1", len(upserts))
	}
	assertSeededWithHashedSecret(t, []byte(upserts[0]), configPath, "management")
}

func TestPostgresStoreSeedUploadsSettledConfigUnchanged(t *testing.T) {
	for name, original := range seedTestSettledConfigs(t) {
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			writeSeedTestFile(t, configPath, original)
			postgresStore, database := newSeedTestPostgresStore(t, configPath)

			if errSync := postgresStore.syncConfigFromDatabase(context.Background(), ""); errSync != nil {
				t.Fatalf("syncConfigFromDatabase() error = %v", errSync)
			}
			if upserts := database.recordedUpserts(); len(upserts) != 1 || upserts[0] != original {
				t.Fatalf("config upserts = %q, want exactly the original config %q", upserts, original)
			}
			if local := readSeedTestFile(t, configPath); string(local) != original {
				t.Fatalf("local config rewritten to %q, want it untouched", local)
			}
		})
	}
}

// Bootstrap must fail rather than seed, and keep the local file as it was: a later
// start finds the same file and has to refuse it again.
func TestPostgresStoreSeedRefusesConfigThatKeepsPlaintextSecret(t *testing.T) {
	for name, content := range seedTestConfigsKeepingPlaintext {
		t.Run(name, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.yaml")
			writeSeedTestFile(t, configPath, content)
			postgresStore, database := newSeedTestPostgresStore(t, configPath)

			for attempt := 1; attempt <= 2; attempt++ {
				if errSync := postgresStore.syncConfigFromDatabase(context.Background(), ""); errSync == nil {
					t.Fatalf("attempt %d: syncConfigFromDatabase() succeeded, want bootstrap to fail", attempt)
				}
			}
			if upserts := database.recordedUpserts(); len(upserts) != 0 {
				t.Fatalf("seeded a config that still holds the plaintext secret:\n%s", upserts[0])
			}
			if local := readSeedTestFile(t, configPath); string(local) != content {
				t.Fatalf("local config rewritten to %q, want the original kept", local)
			}
		})
	}
}
