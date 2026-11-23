package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func objectConfigTestStore(t *testing.T, path string, handler http.HandlerFunc) *ObjectTokenStore {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, errClient := minio.New(strings.TrimPrefix(server.URL, "http://"), &minio.Options{
		Creds: credentials.NewStaticV4("access", "secret", ""), Region: "us-east-1",
	})
	if errClient != nil {
		t.Fatal(errClient)
	}
	return &ObjectTokenStore{client: client, cfg: ObjectStoreConfig{Bucket: "bucket"}, configPath: path}
}

func respondObjectConfig(t *testing.T, w http.ResponseWriter, etag, data string) {
	t.Helper()
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	w.Header().Set("Last-Modified", "Mon, 02 Jan 2006 15:04:05 GMT")
	if _, errWrite := io.WriteString(w, data); errWrite != nil {
		t.Error(errWrite)
	}
}

func TestObjectConfigCASUsesBodyAndETagFromOneGet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	const tail = "---\r\n# unchanged trailing document\r\nkeep: true\r\n"
	old := seedTestPlaintextConfig("management") + tail
	const winnerID = "aaaaaaaa-1111-4444-8888-bbbbbbbbbbbb"
	winner := "# winning YAML\r\nport: 8888\r\nserver:\r\n  server-id: " + winnerID + "\r\n" + tail
	var gets, puts atomic.Int32
	store := objectConfigTestStore(t, path, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			if gets.Add(1) == 1 {
				respondObjectConfig(t, w, `"legacy"`, old)
			} else {
				respondObjectConfig(t, w, `"winner"`, winner)
			}
		case http.MethodPut:
			puts.Add(1)
			if r.Header.Get("If-Match") != `"legacy"` || r.Header.Get("If-None-Match") != "" {
				t.Errorf("PUT condition = %q / %q", r.Header.Get("If-Match"), r.Header.Get("If-None-Match"))
			}
			body, errRead := io.ReadAll(r.Body)
			if errRead != nil || strings.Contains(string(body), seedTestPlaintextSecret) || !strings.HasSuffix(string(body), tail) {
				t.Errorf("unexpected CAS candidate: %q, %v", body, errRead)
			}
			w.WriteHeader(http.StatusPreconditionFailed)
		default:
			t.Errorf("unexpected method %s; want only GET and PUT", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	cfg, errSync := store.syncConfigFromBucket(context.Background(), "")
	if errSync != nil || cfg.ServerID != winnerID || cfg.SourceHash != config.ComputeSourceHash([]byte(winner)) {
		t.Fatalf("did not load winning snapshot: %v", errSync)
	}
	if gets.Load() != 2 || puts.Load() != 1 || string(readSeedTestFile(t, path)) != winner {
		t.Fatalf("GETs = %d, PUTs = %d; mirror should equal winner", gets.Load(), puts.Load())
	}
}

func TestObjectConfigCASRejectsMissingETagAndBoundsConflicts(t *testing.T) {
	for _, tt := range []struct {
		name     string
		etag     string
		wantGets int32
		wantPuts int32
	}{
		{name: "empty ETag", wantGets: 1},
		{name: "repeated conflict", etag: `"old"`, wantGets: configSyncAttempts, wantPuts: configSyncAttempts},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			const local = "port: 7777\n"
			writeSeedTestFile(t, path, local)
			var gets, puts atomic.Int32
			store := objectConfigTestStore(t, path, func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodGet:
					gets.Add(1)
					respondObjectConfig(t, w, tt.etag, "port: 8317\n")
				case http.MethodPut:
					puts.Add(1)
					w.WriteHeader(http.StatusConflict)
				default:
					w.WriteHeader(http.StatusMethodNotAllowed)
				}
			})
			if _, errSync := store.syncConfigFromBucket(context.Background(), ""); errSync == nil {
				t.Fatal("sync unexpectedly succeeded")
			}
			if got := string(readSeedTestFile(t, path)); got != local || gets.Load() != tt.wantGets || puts.Load() != tt.wantPuts {
				t.Fatalf("local = %q, GETs = %d, PUTs = %d", got, gets.Load(), puts.Load())
			}
		})
	}
}

func TestObjectPersistConfigUploadsExactVerifiedSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	const edit = "port: 8888\nmanagement:\n  secret-key: kept-locally\n"
	var puts atomic.Int32
	store := objectConfigTestStore(t, path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			respondObjectConfig(t, w, "current", mirrorTestInitial)
			return
		}
		if r.Header.Get("If-Match") != `"current"` {
			t.Errorf("persistence PUT If-Match = %q", r.Header.Get("If-Match"))
		}
		if r.Method != http.MethodPut {
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		puts.Add(1)
		snapshot, errRead := config.ReadConfigFileSnapshot(path)
		if errRead != nil {
			t.Error(errRead)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if _, errWrite := config.WriteConfigFileCAS(path, snapshot, []byte(edit)); errWrite != nil {
			t.Error(errWrite)
		}
		uploaded, errBody := io.ReadAll(r.Body)
		if errBody != nil || string(uploaded) != string(snapshot.Bytes()) || strings.Contains(string(uploaded), seedTestPlaintextSecret) || !strings.HasSuffix(string(uploaded), "---\r\nkeep: true\r\n") {
			t.Errorf("unexpected upload: %q, %v", uploaded, errBody)
		}
		w.Header().Set("ETag", "published")
		w.WriteHeader(http.StatusOK)
	})
	if _, errSync := store.syncConfigFromBucket(ctx, ""); errSync != nil {
		t.Fatal(errSync)
	}
	writeSeedTestFile(t, path, seedTestPlaintextConfig("management")+"---\r\nkeep: true\r\n")
	if errPersist := store.PersistConfig(ctx); errPersist != nil {
		t.Fatal(errPersist)
	}
	if puts.Load() != 1 || string(readSeedTestFile(t, path)) != edit {
		t.Fatal("verified upload changed a concurrent local edit")
	}
}

func TestObjectPersistConfigRejectsStaleMirror(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	body, etag := mirrorTestInitial, "\"initial\""
	var gets int
	var matches []string
	a := objectConfigTestStore(t, filepath.Join(t.TempDir(), "config.yaml"), func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/bucket/"+objectStoreConfigKey {
			t.Errorf("unexpected object path: %s", r.URL.Path)
		}
		switch r.Method {
		case http.MethodGet:
			gets++
			respondObjectConfig(t, w, etag, body)
		case http.MethodPut:
			match := r.Header.Get("If-Match")
			matches = append(matches, match)
			if r.Header.Get("If-None-Match") != "" {
				t.Error("ordinary save sent a create precondition")
			}
			if match != etag {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			data, errRead := io.ReadAll(r.Body)
			if errRead != nil {
				t.Error(errRead)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			body, etag = string(data), fmt.Sprintf("\"saved-%d\"", len(matches))
			w.Header().Set("ETag", etag)
			w.WriteHeader(http.StatusOK)
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	b := &ObjectTokenStore{client: a.client, cfg: a.cfg, configPath: filepath.Join(t.TempDir(), "config.yaml")}
	for _, replica := range []*ObjectTokenStore{a, b} {
		if _, errSync := replica.syncConfigFromBucket(ctx, ""); errSync != nil {
			t.Fatal(errSync)
		}
	}
	winner := assertSequentialConfigConflict(t, a, b)
	mu.Lock()
	defer mu.Unlock()
	wantMatches := []string{"\"initial\"", "\"saved-1\"", "\"initial\"", "\"initial\""}
	if gets != 2 || !slices.Equal(matches, wantMatches) || body != winner {
		t.Fatalf("GETs = %d, If-Match = %q, remote = %q", gets, matches, body)
	}
}

func TestObjectPersistConfigKeepsBaselineWithoutPutETag(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "config.yaml")
	var gets, puts atomic.Int32
	store := objectConfigTestStore(t, path, func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			gets.Add(1)
			respondObjectConfig(t, w, `"initial"`, mirrorTestInitial)
		case http.MethodPut:
			if match := r.Header.Get("If-Match"); match != `"initial"` {
				t.Errorf("If-Match = %q, want original ETag", match)
			}
			if puts.Add(1) == 1 {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusPreconditionFailed)
			}
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	if _, errSync := store.syncConfigFromBucket(ctx, ""); errSync != nil {
		t.Fatal(errSync)
	}
	writeSeedTestFile(t, path, "port: 8999\n"+mirrorTestServer)
	if errPersist := store.PersistConfig(ctx); errPersist == nil || !strings.Contains(errPersist.Error(), "the version is unknown") {
		t.Fatalf("save without a PUT ETag: %v", errPersist)
	}
	if errPersist := store.PersistConfig(ctx); !errors.Is(errPersist, errRemoteConfigChanged) {
		t.Fatalf("retry without a confirmed baseline: %v", errPersist)
	}
	if gets.Load() != 1 || puts.Load() != 2 {
		t.Fatalf("GETs = %d, PUTs = %d; saves must not re-read the remote", gets.Load(), puts.Load())
	}
}

func TestObjectOptionalConfigStandsByWithoutUploading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeSeedTestFile(t, path, "port: [\n")
	store := objectConfigTestStore(t, path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected %s in standby", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
	})
	store.cfg.OptionalConfig = true
	cfg, errSync := store.syncConfigFromBucket(context.Background(), "")
	if errSync != nil || cfg.ServerID != "" || cfg.SourceHash != "" {
		t.Fatalf("invalid config did not enter standby: %v", errSync)
	}
	if errPersist := store.PersistConfig(context.Background()); errPersist != nil {
		t.Fatal(errPersist)
	}
	if info, errStat := os.Stat(path); errStat != nil || info.Size() != int64(len("port: [\n")) {
		t.Fatalf("standby modified local input: %v", errStat)
	}
}
