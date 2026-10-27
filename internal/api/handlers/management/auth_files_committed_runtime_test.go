package management

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAcquisitionRuntimeSyncUsesCommittedCredential(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			authDir := filepath.Join(root, "auths")
			var uploaded []byte
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.Path != "/credentials/auths/acquired.json" {
					t.Errorf("unexpected object request: %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				data, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				uploaded = data
				staged, err := filepath.Glob(filepath.Join(authDir, ".auth-acquisition-*", "credential"))
				if err != nil || len(staged) != 1 {
					t.Errorf("staged credential: %v, %v", staged, err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				// Lose the staged mirror after the remote received it. The final rename
				// must fail while the previous credential remains readable.
				if err := os.Remove(staged[0]); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.Header().Set("ETag", `"committed"`)
				w.WriteHeader(http.StatusOK)
			}))
			defer remote.Close()
			tokenStore, err := store.NewObjectTokenStore(store.ObjectStoreConfig{
				Endpoint: strings.TrimPrefix(remote.URL, "http://"), Bucket: "credentials",
				AccessKey: "test-key", SecretKey: "test-secret", Region: "us-east-1", LocalRoot: root, PathStyle: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(authDir, "acquired.json")
			original := []byte(fmt.Sprintf(`{"type":%q,"access_token":"previous"}`, provider))
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			record := &coreauth.Auth{ID: "acquired.json", FileName: "acquired.json", Provider: provider}
			if provider == "codex" {
				record.Storage = &codex.CodexTokenStorage{AccessToken: "committed", RefreshToken: "committed-refresh"}
			} else {
				record.Storage = &claude.ClaudeTokenStorage{AccessToken: "committed", RefreshToken: "committed-refresh"}
			}
			var runtimeRecord *coreauth.Auth
			h := &Handler{cfg: &config.Config{AuthDir: authDir}, tokenStore: tokenStore,
				postAuthPersistHook: func(_ context.Context, auth *coreauth.Auth) error { runtimeRecord = auth; return nil },
			}
			if _, err := h.saveTokenRecord(context.Background(), record); err != nil {
				t.Fatalf("remote commit was reported as failure: %v", err)
			}
			if !bytes.Contains(uploaded, []byte(`"access_token":"committed"`)) {
				t.Fatalf("remote did not receive the acquired credential: %q", uploaded)
			}
			mirror, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(mirror, original) {
				t.Fatalf("publication failure did not leave the old mirror readable: %q, %v", mirror, err)
			}
			if runtimeRecord == nil || runtimeRecord.Metadata["access_token"] != "committed" || runtimeRecord.Metadata["refresh_token"] != "committed-refresh" {
				t.Fatalf("runtime received stale or incomplete credentials: %#v", runtimeRecord)
			}
		})
	}
}
