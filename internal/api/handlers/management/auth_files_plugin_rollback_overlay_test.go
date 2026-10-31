package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestPluginBatchRollbackRestoresCommittedOverlay(t *testing.T) {
	for _, runtimeLoaded := range []bool{false, true} {
		for _, priorOverlay := range []bool{false, true} {
			t.Run(fmt.Sprintf("runtime=%t/prior_overlay=%t", runtimeLoaded, priorOverlay), func(t *testing.T) {
				root := t.TempDir()
				authDir := filepath.Join(root, "auths")
				if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
					t.Fatal(errMkdir)
				}
				path := filepath.Join(authDir, "account.json")
				oldBytes := []byte(`{"type":"test-plugin","access_token":"previous"}`)
				mirrorBytes := oldBytes
				if priorOverlay {
					mirrorBytes = []byte(`{"type":"test-plugin","access_token":"older-mirror"}`)
				}
				if errWrite := os.WriteFile(path, mirrorBytes, 0o600); errWrite != nil {
					t.Fatal(errWrite)
				}
				if priorOverlay {
					pending, errPrepare := authpublication.Prepare(path, oldBytes)
					if errPrepare != nil {
						t.Fatal(errPrepare)
					}
					if errCommit := pending.Commit(); errCommit != nil {
						t.Fatal(errCommit)
					}
				}
				var mu sync.Mutex
				remoteBytes := bytes.Clone(oldBytes)
				uploads := 0
				remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPut || r.URL.Path != "/credentials/auths/account.json" {
						t.Errorf("unexpected object request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Error(errRead)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if strings.Contains(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING") {
						body, errRead = io.ReadAll(httputil.NewChunkedReader(bytes.NewReader(body)))
						if errRead != nil {
							t.Error(errRead)
							w.WriteHeader(http.StatusBadRequest)
							return
						}
					}
					mu.Lock()
					remoteBytes = bytes.Clone(body)
					uploads++
					first := uploads == 1
					mu.Unlock()
					if first {
						staged, errGlob := filepath.Glob(filepath.Join(authDir, ".auth-acquisition-*", "credential"))
						if errGlob != nil || len(staged) != 1 {
							t.Errorf("staged credential: %v, %v", staged, errGlob)
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						if errRemove := os.Remove(staged[0]); errRemove != nil {
							t.Error(errRemove)
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
					}
					w.Header().Set("ETag", `"saved"`)
					w.WriteHeader(http.StatusOK)
				}))
				defer remote.Close()
				tokenStore, errStore := store.NewObjectTokenStore(store.ObjectStoreConfig{Endpoint: strings.TrimPrefix(remote.URL, "http://"), Bucket: "credentials", AccessKey: "test-key", SecretKey: "test-secret", Region: "us-east-1", LocalRoot: root, PathStyle: true})
				if errStore != nil {
					t.Fatal(errStore)
				}
				cfg := &config.Config{AuthDir: authDir}
				manager := coreauth.NewManager(nil, nil, nil)
				manager.SetConfig(cfg)
				if runtimeLoaded {
					registerAuthForLookupTest(t, manager, &coreauth.Auth{ID: "account.json", FileName: "account.json", Provider: "test-plugin", Metadata: map[string]any{"type": "test-plugin", "access_token": "previous"}})
				}
				host := pluginhost.New()
				h := &Handler{cfg: cfg, authManager: manager, tokenStore: tokenStore, pluginHost: host}
				record := host.AuthDataToCoreAuth(pluginapi.AuthData{ID: "account.json", FileName: "account.json", Provider: "test-plugin", StorageJSON: []byte(`{"type":"test-plugin","access_token":"new-grant"}`)}, path, "account.json")
				records := []*coreauth.Auth{record, {ID: "invalid-second-record", Provider: "test-plugin"}}
				if errBatch := h.savePluginLoginRecords(context.Background(), records); errBatch == nil {
					t.Fatal("batch unexpectedly succeeded")
				}
				mu.Lock()
				gotRemote := bytes.Clone(remoteBytes)
				gotUploads := uploads
				mu.Unlock()
				var remoteMeta map[string]any
				if errDecode := json.Unmarshal(gotRemote, &remoteMeta); errDecode != nil {
					t.Fatal(errDecode)
				}
				if gotUploads != 2 || remoteMeta["access_token"] != "previous" {
					t.Fatalf("failed batch retained remote grant: uploads=%d remote=%s", gotUploads, gotRemote)
				}
				effective, errEffective := authpublication.ReadFile(path)
				var effectiveMeta map[string]any
				if errEffective != nil || json.Unmarshal(effective, &effectiveMeta) != nil || effectiveMeta["access_token"] != "previous" {
					t.Fatalf("failed batch retained durable publication: %s, %v", effective, errEffective)
				}
				if authpublication.HasPending(path) {
					t.Fatal("rollback left the failed login's publication marker")
				}
				reloaded, errList := tokenStore.List(context.Background())
				if errList != nil || len(reloaded) != 1 || reloaded[0].Metadata["access_token"] != "previous" {
					t.Fatalf("store reload resurrected failed batch: %#v, %v", reloaded, errList)
				}
				if runtimeLoaded {
					current, ok := manager.GetByID("account.json")
					if !ok || current.Metadata["access_token"] != "previous" {
						t.Fatal("rollback did not restore previous runtime auth")
					}
				} else if len(manager.List()) != 0 {
					t.Fatal("rollback left newly registered runtime auth")
				}
			})
		}
	}
}
