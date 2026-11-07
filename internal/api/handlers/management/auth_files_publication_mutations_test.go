package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/authpublication"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestManagementMutationsRetireCommittedPublication(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		for _, existing := range []bool{false, true} {
			for _, operation := range []string{"delete", "delete_all", "upload", "multipart"} {
				t.Run(fmt.Sprintf("%s/existing=%t/%s", provider, existing, operation), func(t *testing.T) {
					h, path, backend := newCommittedPublicationHandler(t, provider, existing)
					router := gin.New()
					router.DELETE("/credentials", h.DeleteAuthFile)
					router.POST("/credentials", h.UploadAuthFile)
					var req *http.Request
					deleting := strings.HasPrefix(operation, "delete")
					if deleting {
						query := "name=account.json"
						if operation == "delete_all" {
							query = "all=true"
						}
						req = httptest.NewRequest(http.MethodDelete, "/credentials?"+query, nil)
					} else {
						data := []byte(fmt.Sprintf(`{"type":%q,"access_token":"replacement"}`, provider))
						if operation == "multipart" {
							var body bytes.Buffer
							writer := multipart.NewWriter(&body)
							part, err := writer.CreateFormFile("file", "account.json")
							if err != nil {
								t.Fatal(err)
							}
							if _, err := part.Write(data); err != nil {
								t.Fatal(err)
							}
							if err := writer.Close(); err != nil {
								t.Fatal(err)
							}
							req = httptest.NewRequest(http.MethodPost, "/credentials", &body)
							req.Header.Set("Content-Type", writer.FormDataContentType())
						} else {
							req = httptest.NewRequest(http.MethodPost, "/credentials?name=account.json", bytes.NewReader(data))
							req.Header.Set("Content-Type", "application/json")
						}
					}
					response := httptest.NewRecorder()
					router.ServeHTTP(response, req)
					if response.Code != http.StatusOK {
						t.Fatalf("management mutation: status=%d body=%s", response.Code, response.Body)
					}
					if authpublication.HasPending(path) {
						t.Error("successful mutation left the previous publication authoritative")
					}
					if deleting {
						if len(backend()) != 0 || len(h.authManager.List()) != 0 {
							t.Fatal("delete retained the remote or active credential")
						}
						if _, err := authpublication.ReadFile(path); !os.IsNotExist(err) {
							t.Fatalf("delete left a readable credential: %v", err)
						}
					} else {
						active, ok := h.authManager.GetByID("account.json")
						if !ok || active.Metadata["access_token"] != "replacement" {
							t.Fatalf("upload returned success without activating replacement: %#v", active)
						}
						var remote map[string]any
						if err := json.Unmarshal(backend(), &remote); err != nil || remote["access_token"] != "replacement" {
							t.Fatalf("upload did not replace the remote credential: %#v, %v", remote, err)
						}
					}
					scanned, err := synthesizer.NewFileSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: h.cfg, AuthDir: h.cfg.AuthDir})
					if err != nil || (deleting && len(scanned) != 0) || (!deleting && (len(scanned) != 1 || scanned[0].Metadata["access_token"] != "replacement")) {
						t.Fatalf("full rescan reverted management mutation: %#v, %v", scanned, err)
					}
				})
			}
		}
	}
}

func newCommittedPublicationHandler(t *testing.T, provider string, existing bool) (*Handler, string, func() []byte) {
	t.Helper()
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	path := filepath.Join(authDir, "account.json")
	var mu sync.Mutex
	var remoteBytes []byte
	first := true
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path != "/credentials/auths/account.json" {
			t.Errorf("unexpected object path: %s", r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.Method {
		case http.MethodDelete:
			remoteBytes = nil
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPut:
			data, err := io.ReadAll(r.Body)
			if strings.Contains(r.Header.Get("X-Amz-Content-Sha256"), "STREAMING") && err == nil {
				data, err = io.ReadAll(httputil.NewChunkedReader(bytes.NewReader(data)))
			}
			if err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			remoteBytes = data
			if first {
				first = false
				staged, err := filepath.Glob(filepath.Join(authDir, ".auth-acquisition-*", "credential"))
				if err != nil || len(staged) != 1 {
					t.Errorf("staged credential: %v, %v", staged, err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				if err := os.Remove(staged[0]); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
			}
			w.Header().Set("ETag", `"committed"`)
		default:
			t.Errorf("unexpected object method: %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(remote.Close)
	tokenStore, err := store.NewObjectTokenStore(store.ObjectStoreConfig{
		Endpoint: strings.TrimPrefix(remote.URL, "http://"), Bucket: "credentials", AccessKey: "test-key",
		SecretKey: "test-secret", Region: "us-east-1", LocalRoot: root, PathStyle: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if existing {
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"type":%q,"access_token":"old"}`, provider)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manager := coreauth.NewManager(tokenStore, nil, nil)
	h := &Handler{cfg: &config.Config{AuthDir: authDir}, authManager: manager, tokenStore: tokenStore}
	h.postAuthPersistHook = func(ctx context.Context, auth *coreauth.Auth) error {
		return h.upsertAuthRecord(coreauth.WithSkipPersist(ctx), auth)
	}
	if _, err := h.saveTokenRecord(context.Background(), &coreauth.Auth{ID: "account.json", FileName: "account.json", Provider: provider,
		Metadata: map[string]any{"type": provider, "access_token": "committed"}}); err != nil {
		t.Fatal(err)
	}
	if !authpublication.HasPending(path) {
		t.Fatal("fixture did not reach remotely committed publication failure")
	}
	return h, path, func() []byte {
		mu.Lock()
		defer mu.Unlock()
		return bytes.Clone(remoteBytes)
	}
}
