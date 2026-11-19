package management

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"golang.org/x/crypto/bcrypt"
	"gopkg.in/yaml.v3"
)

func TestFullDocumentSavePublishesConsistentSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, url, body string
		legacy          bool
	}{
		{"v0 legacy YAML", "/v0/management/config.yaml", "# saved comment\nhost: 127.0.0.1\nport: 8320\nremote-management: {secret-key: snapshot-fixture}\noauth-model-alias: {' CODEX ': [{name: upstream, alias: public}]}\n", true},
		{"v0 v8 YAML", "/v0/management/config.yaml", "# saved comment\nconfig-version: 8\nserver: {host: 127.0.0.1, port: 8320}\nmanagement: {secret-key: snapshot-fixture}\noauth: {model-alias: {' CODEX ': [{name: upstream, alias: public}]}}\n", true},
		{"v0 mixed YAML", "/v0/management/config.yaml", "# saved comment\nserver: {host: 127.0.0.1, port: 8320}\nremote-management: {secret-key: snapshot-fixture}\nws-auth: false\noauth-model-alias: {' CODEX ': [{name: upstream, alias: public}]}\n", true},
		{"v8 YAML", "/v8/management/config.yaml", "# saved comment\nconfig-version: 8\nserver: {host: 127.0.0.1, port: 8320}\nmanagement: {secret-key: snapshot-fixture}\noauth: {model-alias: {' CODEX ': [{name: upstream, alias: public}]}}\n", false},
		{"v8 JSON", "/v8/management/config", `{"server":{"host":"127.0.0.1","port":8320},"management":{"secret-key":"snapshot-fixture"},"oauth":{"model-alias":{" CODEX ":[{"name":"upstream","alias":"public"}]}}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, path, router := newFullDocumentHandler(t)
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			reloads := make(chan *config.Config, 1)
			h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { reloads <- next })
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, tc.url, strings.NewReader(tc.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
			}
			published := h.cfg.CloneForRuntime()
			if published.Host != "127.0.0.1" || published.Port != 8320 || len(published.OAuthModelAlias["codex"]) != 1 || published.OAuthModelAlias["codex"][0].Name != "upstream" {
				t.Fatal("handler did not publish the normalized request")
			}
			if err := bcrypt.CompareHashAndPassword([]byte(published.RemoteManagement.SecretKey), []byte("snapshot-fixture")); err != nil {
				t.Fatal("handler did not publish the prepared management key")
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var disk config.Config
			if err := yaml.Unmarshal(saved, &disk); err != nil {
				t.Fatal(err)
			}
			if disk.Host != "127.0.0.1" || disk.Port != 8320 || len(disk.OAuthModelAlias[" CODEX "]) != 1 {
				t.Fatal("saved document lost request values")
			}
			if !reflect.DeepEqual(disk.OAuthOnlyFields, h.cfg.OAuthOnlyFields) {
				t.Fatal("published OAuth scope differs from the saved document")
			}
			if strings.Contains(tc.body, "# saved comment") && !strings.Contains(string(saved), "# saved comment") {
				t.Fatal("save dropped the operator comment")
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) {
				t.Fatal("save replaced the mounted config inode")
			}
			if disk.RemoteManagement.SecretKey != published.RemoteManagement.SecretKey {
				t.Fatal("save did not persist the published management-key hash")
			}
			if tc.legacy {
				if h.cfg.SourceHash != config.ComputeSourceHash(saved) {
					t.Fatal("legacy snapshot source hash does not identify the saved bytes")
				}
			} else {
				if rec.Header().Get("ETag") != fmt.Sprintf(`"%x"`, sha256.Sum256(saved)) {
					t.Fatal("save ETag does not identify finalized disk bytes")
				}
				loaded, err := config.LoadConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				reloaded, err := os.ReadFile(path)
				if err != nil || string(reloaded) != string(saved) || loaded.RemoteManagement.SecretKey != published.RemoteManagement.SecretKey {
					t.Fatal("reload changed the saved key or finalized bytes")
				}
				select {
				case next := <-reloads:
					if !reflect.DeepEqual(next, published) {
						t.Fatal("reload received a different config from the published snapshot")
					}
				case <-time.After(2 * time.Second):
					t.Fatal("save did not publish its reload snapshot")
				}
			}
		})
	}
}

func newFullDocumentHandler(t *testing.T) (*Handler, string, *gin.Engine) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("host: 127.0.0.1\nport: 8317\nrequest-retry: 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v0/management/config.yaml", h.PutConfigYAML)
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		router.Handle(method, "/v8/management/config", h.ConfigV8)
		router.Handle(method, "/v8/management/config.yaml", h.ConfigV8)
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}
	return h, path, router
}

func TestFullDocumentSaveRejectsAmbiguousOAuthChannels(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, field := range []struct{ name, value string }{
		{"model-alias", `[{"name":"upstream","alias":"public"}]`},
		{"excluded-models", `["private-*"]`},
		{"settings", `[{"name":"upstream","max-context-length":8192}]`},
		{"request-scoped-errors", `[{"status":400,"match":["invalid input"],"action":"cooldown"}]`},
	} {
		for _, endpoint := range []struct{ name, method, url, template string }{
			{"v0 legacy YAML", http.MethodPut, "/v0/management/config.yaml", "oauth-%s: %s\n"},
			{"v0 v8 YAML", http.MethodPut, "/v0/management/config.yaml", "config-version: 8\noauth:\n  %s: %s\n"},
			{"v8 YAML", http.MethodPut, "/v8/management/config.yaml", "config-version: 8\noauth:\n  %s: %s\n"},
			{"v8 JSON", http.MethodPut, "/v8/management/config", `{"oauth":{"%s":%s}}`},
			{"v8 PATCH", http.MethodPatch, "/v8/management/config", `{"oauth":{"%s":%s}}`},
			{"v8 map PUT", http.MethodPut, "/v8/management/config/oauth/" + field.name, `%[2]s`},
			{"v8 map PATCH", http.MethodPatch, "/v8/management/config/oauth/" + field.name, `%[2]s`},
		} {
			t.Run(field.name+"/"+endpoint.name, func(t *testing.T) {
				h, path, router := newFullDocumentHandler(t)
				before := h.cfg.CloneForRuntime()
				original, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				channels := fmt.Sprintf(`{"codex":%s," CODEX ":%s}`, field.value, field.value)
				body := fmt.Sprintf(endpoint.template, field.name, channels)
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, httptest.NewRequest(endpoint.method, endpoint.url, strings.NewReader(body)))
				if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "normalize to") {
					t.Fatalf("ambiguous save status=%d body=%s", rec.Code, rec.Body.String())
				}
				after, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(after) != string(original) || !reflect.DeepEqual(h.cfg, before) || h.reloadGeneration != 0 {
					t.Fatal("rejected save changed disk, handler, or reload generation")
				}
			})
		}
	}
}

func TestFullDocumentSaveWriteFailureDoesNotPublish(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logger := log.StandardLogger()
	previousHooks := logger.ReplaceHooks(make(log.LevelHooks))
	previousLevel := logger.GetLevel()
	previousOut := logger.Out
	hook := logtest.NewLocal(logger)
	logger.SetLevel(log.WarnLevel)
	t.Cleanup(func() {
		logger.ReplaceHooks(previousHooks)
		logger.SetLevel(previousLevel)
		logger.SetOutput(previousOut)
	})
	h, path, router := newFullDocumentHandler(t)
	before := h.cfg.CloneForRuntime()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Make the destination unwritable without relying on file permissions,
	// which privileged test processes can bypass. Validation can still run.
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	body := "config-version: 8\nserver: {port: 8320}\nretired-option: true\n"
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(body)))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "write_failed") {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, err := os.ReadFile(path + ".original")
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) || !reflect.DeepEqual(h.cfg, before) || h.reloadGeneration != 0 {
		t.Fatal("failed save changed disk, handler, or reload generation")
	}
	if entries := hook.AllEntries(); len(entries) != 0 {
		t.Fatal("failed save emitted a migration warning for the temporary file")
	}
	// The same document should warn once when saved to a writable destination.
	_, _, writableRouter := newFullDocumentHandler(t)
	rec = httptest.NewRecorder()
	writableRouter.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
	}
	if entries := hook.AllEntries(); len(entries) != 1 || !strings.Contains(entries[0].Message, `"retired-option" commented out during v8 migration`) {
		t.Fatal("successful save did not emit exactly one migration warning")
	}
}

func TestFullDocumentSaveValidatesRawScalarContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ name, url, body string }{
		{"legacy", "/v0/management/config.yaml", "trusted-proxies:\n  - |-\n    127.0.0.1\n    # invalid proxy suffix\n"},
		{"v0 v8", "/v0/management/config.yaml", "config-version: 8\nserver:\n  trusted-proxies:\n    - |-\n      127.0.0.1\n      # invalid proxy suffix\n"},
		{"v8", "/v8/management/config.yaml", "config-version: 8\nserver:\n  trusted-proxies:\n    - |-\n      127.0.0.1\n      # invalid proxy suffix\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, path, router := newFullDocumentHandler(t)
			before := h.cfg.CloneForRuntime()
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, tc.url, strings.NewReader(tc.body)))
			if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "invalid trusted-proxies entry") {
				t.Errorf("raw scalar validation status=%d, want invalid_config rejection", rec.Code)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(original) || !reflect.DeepEqual(h.cfg, before) || h.reloadGeneration != 0 {
				t.Fatal("rejected raw scalar changed disk, handler, or reload generation")
			}
		})
	}
}

func TestFullDocumentSavePreservesScalarContent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct{ name, url, body string }{
		{"legacy", "/v0/management/config.yaml", "host: |-\n  first\n  # scalar data\nport: 8320\n"},
		{"v0 v8", "/v0/management/config.yaml", "config-version: 8\nserver:\n  host: |-\n    first\n    # scalar data\n  port: 8320\n"},
		{"v8", "/v8/management/config.yaml", "config-version: 8\nserver:\n  host: |-\n    first\n    # scalar data\n  port: 8320\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, path, router := newFullDocumentHandler(t)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, tc.url, strings.NewReader(tc.body)))
			if rec.Code != http.StatusOK {
				t.Fatalf("save status=%d body=%s", rec.Code, rec.Body.String())
			}
			loaded, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			const want = "first\n# scalar data"
			if loaded.Host != want || h.cfg.Host != want || loaded.Port != 8320 || h.cfg.Port != 8320 {
				t.Fatal("save changed scalar contents on disk or in the published config")
			}
		})
	}
}
