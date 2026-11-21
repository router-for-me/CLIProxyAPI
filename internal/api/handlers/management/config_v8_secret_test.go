package management

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestConfigV8SecretWritesFinalizeBeforePublishing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ name, method, url, body string }{
		{"root PATCH", http.MethodPatch, "/v8/management/config", `{"management":{"secret-key":"v8-save-fixture"}}`},
		{"section PUT", http.MethodPut, "/v8/management/config/management", `{"secret-key":"v8-save-fixture"}`},
		{"section PATCH", http.MethodPatch, "/v8/management/config/management", `{"secret-key":"v8-save-fixture"}`},
		{"scalar PUT", http.MethodPut, "/v8/management/config/management/secret-key", `"v8-save-fixture"`},
		{"YAML merge", http.MethodPut, "/v8/management/config.yaml", "config-version: 8\nmanagement:\n  <<: {secret-key: v8-save-fixture}\nserver: {port: 8317}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, path, router := newFullDocumentHandler(t)
			router.GET("/v8/management/config.yaml", h.ConfigV8)
			original, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			do := func(method, url, body, etag string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, url, strings.NewReader(body))
				if etag != "" {
					req.Header.Set("If-Match", etag)
				}
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				return rec
			}
			etag := do(http.MethodGet, "/v8/management/config.yaml", "", "").Header().Get("ETag")
			rec := do(tc.method, tc.url, tc.body, etag)
			if rec.Code != http.StatusOK {
				t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			published := h.cfg.CloneForRuntime()
			if strings.Contains(string(saved), "v8-save-fixture") {
				t.Error("saved document still contains the plaintext management key")
			}
			if err := bcrypt.CompareHashAndPassword([]byte(published.RemoteManagement.SecretKey), []byte("v8-save-fixture")); err != nil {
				t.Fatal("published key does not match the submitted fixture")
			}
			loaded, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			afterLoad, err := os.ReadFile(path)
			if err != nil || string(afterLoad) != string(saved) || loaded.RemoteManagement.SecretKey != published.RemoteManagement.SecretKey {
				t.Fatal("reload changed finalized bytes or published management key")
			}
			if rec.Header().Get("ETag") != fmt.Sprintf(`"%x"`, sha256.Sum256(saved)) {
				t.Fatal("response ETag differs from saved document")
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(original, after) || original.Mode() != after.Mode() {
				t.Fatal("save changed config inode or permissions")
			}
			generation := h.reloadGeneration
			stale := do(tc.method, tc.url, tc.body, etag)
			if stale.Code != http.StatusPreconditionFailed || !reflect.DeepEqual(h.cfg, published) || h.reloadGeneration != generation {
				t.Fatal("stale precondition changed published state")
			}
			afterStale, err := os.ReadFile(path)
			if err != nil || string(afterStale) != string(saved) {
				t.Fatal("stale precondition changed disk")
			}
			// The returned ETag remains usable after a reload. An unrelated PATCH
			// must keep the persisted bcrypt value rather than generating a new one.
			chained := do(http.MethodPatch, "/v8/management/config", `{"server":{"port":8320}}`, rec.Header().Get("ETag"))
			if chained.Code != http.StatusOK || h.cfg.RemoteManagement.SecretKey != published.RemoteManagement.SecretKey {
				t.Fatal("chained PATCH did not preserve the saved key")
			}
		})
	}
}
