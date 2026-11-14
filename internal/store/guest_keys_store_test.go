package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

const guestKeysTestContent = `{"guests":[{"id":"a","key-hash":"h"}]}`

func TestPostgresStorePersistGuestKeysUpserts(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "pgstore", "config", "config.yaml")
	writeSeedTestFile(t, filepath.Join(filepath.Dir(configPath), GuestKeysFileName), guestKeysTestContent)
	postgresStore, database := newSeedTestPostgresStore(t, configPath)

	if err := postgresStore.PersistGuestKeys(context.Background()); err != nil {
		t.Fatalf("PersistGuestKeys() error = %v", err)
	}
	if got := database.recordedUpserts(); len(got) != 1 || got[0] != guestKeysTestContent {
		t.Fatalf("upserts = %q", got)
	}
}

func TestPostgresStoreSeedsEmptyDatabaseFromSpoolGuestKeys(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "pgstore", "config", "config.yaml")
	postgresStore, database := newSeedTestPostgresStore(t, configPath)

	if err := postgresStore.syncGuestKeysFromDatabase(context.Background()); err != nil {
		t.Fatalf("sync without a spool file: %v", err)
	}
	if got := database.recordedUpserts(); len(got) != 0 {
		t.Fatalf("upserts without a spool file = %q", got)
	}

	writeSeedTestFile(t, filepath.Join(filepath.Dir(configPath), GuestKeysFileName), guestKeysTestContent)
	if err := postgresStore.syncGuestKeysFromDatabase(context.Background()); err != nil {
		t.Fatalf("sync with a spool file: %v", err)
	}
	if got := database.recordedUpserts(); len(got) != 1 || got[0] != guestKeysTestContent {
		t.Fatalf("upserts = %q", got)
	}
}

func TestObjectTokenStorePersistGuestKeysUploads(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "objectstore", "config", "config.yaml")
	writeSeedTestFile(t, filepath.Join(filepath.Dir(configPath), GuestKeysFileName), guestKeysTestContent)
	objectStore, _ := newSeedTestObjectStore(t, configPath)
	uploads := map[string]string{}
	objectStore.putObjectFn = func(_ context.Context, key string, data []byte, _ string) error {
		uploads[key] = string(data)
		return nil
	}

	if err := objectStore.PersistGuestKeys(context.Background()); err != nil {
		t.Fatalf("PersistGuestKeys() error = %v", err)
	}
	if uploads[objectStoreGuestKeysKey] != guestKeysTestContent {
		t.Fatalf("uploads = %v", uploads)
	}
}

func TestObjectTokenStoreRestoresGuestKeysFromBucket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/"+objectStoreGuestKeysKey) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(guestKeysTestContent)))
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
		if r.Method != http.MethodHead {
			_, _ = w.Write([]byte(guestKeysTestContent))
		}
	}))
	t.Cleanup(server.Close)
	client, err := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{
		Creds:  credentials.NewStaticV4("access", "secret", ""),
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("minio.New() error = %v", err)
	}
	configPath := filepath.Join(t.TempDir(), "objectstore", "config", "config.yaml")
	objectStore := &ObjectTokenStore{client: client, cfg: ObjectStoreConfig{Bucket: "bucket"}, configPath: configPath}

	if err = objectStore.syncGuestKeysFromBucket(context.Background()); err != nil {
		t.Fatalf("syncGuestKeysFromBucket() error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(filepath.Dir(configPath), GuestKeysFileName))
	if err != nil || string(got) != guestKeysTestContent {
		t.Fatalf("restored file = %q, %v", got, err)
	}
}

func TestGitTokenStorePersistGuestKeysCommitsFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	remoteDir := setupGitRemoteRepository(t, root, "master",
		testBranchSpec{name: "master", contents: "remote master branch\n"},
	)
	store := NewGitTokenStore(remoteDir, "", "", "")
	store.SetBaseDir(filepath.Join(root, "workspace", "auths"))
	if err := store.EnsureRepository(); err != nil {
		t.Fatalf("EnsureRepository: %v", err)
	}
	guestPath := filepath.Join(filepath.Dir(store.ConfigPath()), GuestKeysFileName)
	if err := os.MkdirAll(filepath.Dir(guestPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(guestPath, []byte(guestKeysTestContent), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := store.PersistGuestKeys(context.Background()); err != nil {
		t.Fatalf("PersistGuestKeys: %v", err)
	}
	assertRemoteFileContents(t, remoteDir, "master", "config/"+GuestKeysFileName, guestKeysTestContent)
}
