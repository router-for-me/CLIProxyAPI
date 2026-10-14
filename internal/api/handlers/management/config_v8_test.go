package management

import (
	"context"
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
	"gopkg.in/yaml.v3"
)

func TestConfigV8MigrationAndLegacyAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "# Keep this configuration\nport: 8317\nrequest-retry: 3\napi-keys: [client]\nws-auth: true\ntls: {}\npayload: null\ncodex: {live-media-relay: {}}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	reloads := make(chan *config.Config, 8)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloads <- cfg })
	router := gin.New()
	router.GET("/v8/management/config", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.PUT("/v8/management/config.yaml", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.DELETE("/v8/management/config/*path", h.ConfigV8)
	router.PUT("/v0/management/request-retry", h.PutRequestRetry)
	request := func(method, url, body string, status int) {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
		if recorder.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, url, recorder.Code, recorder.Body.String())
		}
	}
	request(http.MethodGet, "/v8/management/config", "", 200)
	saved, _ := os.ReadFile(path)
	if string(saved) != raw {
		t.Fatal("GET migrated the config")
	}
	request(http.MethodPatch, "/v8/management/config", `{"server":{"port":"invalid"}}`, 422)
	saved, _ = os.ReadFile(path)
	if string(saved) != raw {
		t.Fatal("failed write migrated the config")
	}
	request(http.MethodPut, "/v0/management/request-retry", `{"value":2}`, 200)
	saved, _ = os.ReadFile(path)
	if strings.Contains(string(saved), "config-version") || strings.Contains(string(saved), "routing:") {
		t.Fatal("v0 migrated a legacy file")
	}
	originalFile, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodPatch, "/v8/management/config", `{"routing":{"retry":{"request-retry":0}},"oauth":{"providers":{"aistudio":{"ws-auth":false}}}}`, 200)
	updatedFile, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(originalFile, updatedFile) {
		t.Fatal("v8 update replaced the mounted configuration inode")
	}
	saved, _ = os.ReadFile(path)
	if !strings.Contains(string(saved), "config-version: 8") || !strings.Contains(string(saved), "# Keep this configuration") {
		t.Fatal("migration or comment preservation failed")
	}
	if err = config.ValidateV8Config(saved); err != nil {
		t.Fatalf("saved migration retained an invalid legacy block: %v", err)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.RequestRetry != 0 || loaded.WebsocketAuth || len(loaded.APIKeys) != 1 {
		t.Fatal("v8 patch lost effective values")
	}
	request(http.MethodPut, "/v0/management/request-retry", `{"value":5}`, 200)
	loaded, err = config.LoadConfig(path)
	if err != nil || loaded.RequestRetry != 5 {
		t.Fatalf("v0 update to v8 config failed: %v", err)
	}
	saved, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.ValidateV8Config(saved); err != nil || !strings.Contains(string(saved), "config-version: 8") {
		t.Fatalf("v0 did not preserve the existing v8 layout: %v", err)
	}
	request(http.MethodPut, "/v8/management/config/requests/proxy-url", `"direct"`, 200)
	loaded, err = config.LoadConfig(path)
	if err != nil || loaded.ProxyURL != "direct" {
		t.Fatalf("path update failed: %v", err)
	}
	request(http.MethodPut, "/v8/management/config/server/unknown-option", `true`, 400)
	request(http.MethodPut, "/v8/management/config/credentials/concurrency/lifecycle-config-revision", `999`, 400)
	select {
	case <-reloads:
	case <-time.After(5 * time.Second):
		t.Fatal("config reload hook was not called")
	}
}

func TestConfigV8CommentsUnknownLegacySectionsOnWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "home: {enabled: true, host: ignored.example}\nenable-gemini-cli-endpoint: false\nformer-feature: {mode: old}\nserver: {port: 8317}\nproxy-url: \"\"\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Home = config.HomeConfig{Enabled: true, Host: "runtime.example"}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.GET("/v8/management/config", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.DELETE("/v8/management/config/*path", h.ConfigV8)
	request := func(method, url, body string, status int) {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
		if recorder.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, url, recorder.Code, recorder.Body.String())
		}
	}
	request(http.MethodGet, "/v8/management/config", "", http.StatusOK)
	request(http.MethodPatch, "/v8/management/config", `{"server":{"port":"invalid"}}`, http.StatusUnprocessableEntity)
	request(http.MethodPatch, "/v8/management/config", `{"home":{"enabled":true}}`, http.StatusBadRequest)
	request(http.MethodPatch, "/v8/management/config", `{"unknown-setting":true}`, http.StatusBadRequest)
	saved, err := os.ReadFile(path)
	if err != nil || string(saved) != raw {
		t.Fatalf("read or failed write changed the config: %v", err)
	}
	request(http.MethodPut, "/v8/management/config/requests/proxy-url", `"direct"`, http.StatusOK)
	saved, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.ValidateV8Config(saved); err != nil {
		t.Fatalf("saved file contains invalid legacy fields: %v", err)
	}
	var doc yaml.Node
	if err = yaml.Unmarshal(saved, &doc); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"home", "enable-gemini-cli-endpoint", "former-feature"} {
		if configV8Node(doc.Content[0], []string{key}) != nil || !strings.Contains(string(saved), "# "+key+":") {
			t.Fatalf("unknown legacy section %s was not commented on disk", key)
		}
	}
	if url := configV8Node(doc.Content[0], []string{"requests", "proxy-url"}); url == nil || url.Value != "direct" {
		t.Fatal("path update did not persist the proxy URL")
	}
	if h.cfg.Home.Host != "runtime.example" || !h.cfg.Home.Enabled || h.cfg.ProxyURL != "direct" {
		t.Fatal("path update changed runtime Home settings or missed the proxy URL")
	}
	request(http.MethodPut, "/v8/management/config/requests/proxy-url", `"none"`, http.StatusOK)
	request(http.MethodDelete, "/v8/management/config/server", "", http.StatusOK)
	saved, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"home", "enable-gemini-cli-endpoint", "former-feature"} {
		if strings.Count(string(saved), "# "+key+":") != 1 {
			t.Fatalf("subsequent write or deletion lost or duplicated %s comments", key)
		}
	}
}

func TestConfigV8CommentsUnknownNestedFieldsOnWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "server: {port: 8317}\noauth: {providers: {codex: {disable-codex-cloaking: true, retired-setting: false}}}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.GET("/v8/management/config", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.DELETE("/v8/management/config/*path", h.ConfigV8)
	request := func(method, url, body string, status int) {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
		if recorder.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, url, recorder.Code, recorder.Body.String())
		}
	}
	request(http.MethodGet, "/v8/management/config", "", http.StatusOK)
	if saved, errRead := os.ReadFile(path); errRead != nil || string(saved) != raw {
		t.Fatalf("GET changed existing config: %v", errRead)
	}
	request(http.MethodPatch, "/v8/management/config", `{"server":{"port":8318}}`, http.StatusOK)
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = config.ValidateV8Config(saved); err != nil {
		t.Fatalf("saved config is invalid: %v\n%s", err, saved)
	}
	if strings.Count(string(saved), "# oauth.providers.codex.retired-setting: false") != 1 {
		t.Fatalf("existing unknown field was not preserved as a comment: %s", saved)
	}
	loaded, err := config.LoadConfig(path)
	if err != nil || loaded.Port != 8318 || !loaded.Codex.DisableCodexCloaking {
		t.Fatalf("unrelated write changed known settings: cfg=%+v error=%v", loaded, err)
	}
	request(http.MethodPut, "/v8/management/config/oauth/providers/codex/new-setting", `true`, http.StatusBadRequest)
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(saved) {
		t.Fatalf("invalid new setting changed the config: %v", err)
	}
	request(http.MethodDelete, "/v8/management/config/upstream/codex/disable-codex-cloaking", "", http.StatusOK)
	saved, err = os.ReadFile(path)
	if err != nil || strings.Count(string(saved), "# oauth.providers.codex.retired-setting: false") != 1 {
		t.Fatalf("deleting the neighboring setting lost the archived comment: %v\n%s", err, saved)
	}
}

func TestV8NestedWriteMigratesOnlyOnSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		migrated   bool
	}{
		{"valid", `0`, 200, true},
		{"invalid", `"bad"`, 422, false},
		{"legacy envelope", `{"value":0}`, 422, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("request-retry: 3\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.PUT("/v8/management/config/*path", h.ConfigV8)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/v8/management/config/routing/retry/request-retry", strings.NewReader(tc.body)))
			if recorder.Code != tc.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			data, _ := os.ReadFile(path)
			if strings.Contains(string(data), "config-version: 8") != tc.migrated {
				t.Fatal("unexpected migration")
			}
		})
	}
}

func TestV8MigrationReloadSnapshotMatchesDisk(t *testing.T) {
	for _, tc := range []struct {
		name, route, path, body string
		handler                 func(*Handler) gin.HandlerFunc
		migrated                bool
	}{
		{"legacy", "/v0/management/debug", "/v0/management/debug", `{"value":true}`, func(h *Handler) gin.HandlerFunc { return h.PutDebug }, false},
		{"v8 logs", "/v8/management/config/*path", "/v8/management/config/observability/logs/debug", `true`, func(h *Handler) gin.HandlerFunc { return h.ConfigV8 }, true},
		{"v8 plugin", "/v8/management/config/*path", "/v8/management/config/plugins/configs/test-plugin/enabled", `false`, func(h *Handler) gin.HandlerFunc { return h.ConfigV8 }, true},
		{"v8 config", "/v8/management/config", "/v8/management/config", `{"observability":{"logs":{"debug":true}}}`, func(h *Handler) gin.HandlerFunc { return h.ConfigV8 }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("codex: {disable-codex-cloaking: true}\nxai: {inject-x-search: true}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			reloads := make(chan *config.Config, 1)
			h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { reloads <- next })
			router := gin.New()
			router.PATCH(tc.route, tc.handler(h))
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, tc.path, strings.NewReader(tc.body)))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			disk, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case snapshot := <-reloads:
				if !reflect.DeepEqual(snapshot.OAuthOnlyFields, disk.OAuthOnlyFields) {
					t.Fatal("reload snapshot has different OAuth scope from the saved file")
				}
				api := snapshot.ForAPIKey()
				if !api.Codex.DisableCodexCloaking || !api.XAI.InjectXSearch {
					t.Fatal("reload snapshot applied the wrong API-key configuration")
				}
				if tc.migrated {
					data, errRead := os.ReadFile(path)
					if errRead != nil {
						t.Fatal(errRead)
					}
					if errValidate := config.ValidateV8Config(data); errValidate != nil {
						t.Fatal(errValidate)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("missing config reload")
			}
		})
	}
}

func TestV8GroupedCredentialsSurviveLegacyWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `api-keys:
  codex:
    - name: production
      base-url: https://example.invalid
      headers: {X-Shared: value}
      request-retry: 2
      keys:
        - api-key: first
          request-retry: null
        - api-key: second
          request-retry: 0
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.PUT("/v0/management/debug", h.PutDebug)
	router.PUT("/v0/management/codex-api-key", h.PutCodexKeys)
	for _, tc := range []struct{ url, body string }{
		{"/v0/management/debug", `{"value":true}`},
		{"/v0/management/codex-api-key", `[{"api-key":"first","base-url":"https://example.invalid","request-retry":0}]`},
	} {
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, tc.url, strings.NewReader(tc.body)))
		if recorder.Code != 200 {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		data, _ := os.ReadFile(path)
		if tc.url == "/v0/management/debug" && !strings.Contains(string(data), "production") {
			t.Fatal("unrelated legacy write lost group identity")
		}
	}
	loaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.CodexKey) != 1 || loaded.CodexKey[0].RequestRetry == nil || *loaded.CodexKey[0].RequestRetry != 0 {
		t.Fatal("legacy credential replacement was not applied")
	}
}

func TestConfigV8DeleteLastField(t *testing.T) {
	for _, tc := range []struct {
		name  string
		raw   string
		path  string
		check func(*config.Config) bool
	}{
		{"retry", "request-retry: 3\n", "routing/retry/request-retry", func(cfg *config.Config) bool { return cfg.RequestRetry == 0 }},
		{"websocket", "ws-auth: false\n", "oauth/providers/aistudio/ws-auth", func(cfg *config.Config) bool { return cfg.WebsocketAuth }},
		{"debug", "debug: true\n", "observability/logs/debug", func(cfg *config.Config) bool { return !cfg.Debug }},
		{"sibling", "routing: {strategy: fill-first, retry: {request-retry: 3}}\n", "routing/retry/request-retry", func(cfg *config.Config) bool { return cfg.RequestRetry == 0 && cfg.Routing.Strategy == "fill-first" }},
		{"provider", "oauth: {providers: {codex: {disable-codex-cloaking: true}}}\n", "upstream/codex/disable-codex-cloaking", func(cfg *config.Config) bool { return !cfg.Codex.DisableCodexCloaking }},
		{"excluded models", "oauth: {excluded-models: {codex: [blocked-model]}}\n", "oauth/excluded-models", func(cfg *config.Config) bool { return len(cfg.OAuthExcludedModels) == 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			raw := tc.raw + "port: 8317\napi-keys: [client]\nplugins: {configs: {sample: {enabled: false, options: {}}}}\n"
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: file}
			router := gin.New()
			router.DELETE("/v8/management/config/*path", h.ConfigV8)
			router.GET("/v8/management/config/*path", h.ConfigV8)
			url := "/v8/management/config/" + tc.path
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, url, nil))
			if w.Code != http.StatusOK {
				t.Fatalf("delete last field: status=%d body=%s", w.Code, w.Body.String())
			}
			cfg, err = config.LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.check(cfg) || cfg.Port != 8317 || len(cfg.APIKeys) != 1 || cfg.APIKeys[0] != "client" {
				t.Fatal("delete did not restore defaults or changed unrelated settings")
			}
			if !reflect.DeepEqual(h.cfg.OAuthOnlyFields, cfg.OAuthOnlyFields) {
				t.Fatal("delete left the runtime OAuth scope out of sync with disk")
			}
			saved, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var doc yaml.Node
			if err = yaml.Unmarshal(saved, &doc); err != nil {
				t.Fatal(err)
			}
			if configV8Node(doc.Content[0], strings.Split(tc.path, "/")) != nil {
				t.Fatal("save reintroduced the deleted field")
			}
			if options := configV8Node(doc.Content[0], []string{"plugins", "configs", "sample", "options"}); options == nil || options.Kind != yaml.MappingNode || len(options.Content) != 0 {
				t.Fatal("delete removed an unrelated explicit empty mapping")
			}
			for _, method := range []string{http.MethodGet, http.MethodDelete} {
				w = httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest(method, url, nil))
				if w.Code != http.StatusNotFound {
					t.Fatalf("%s deleted field: status=%d body=%s", method, w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestConfigV8ReplaceEmptyGroup(t *testing.T) {
	for _, tc := range []struct{ path, raw string }{
		{"routing/retry", "routing: {retry: {request-retry: 3}}\n"},
		{"oauth/providers/aistudio", "oauth: {providers: {aistudio: {ws-auth: false}}}\n"},
		{"observability/logs", "observability: {logs: {debug: true}}\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			r := gin.New()
			r.PUT("/v8/management/config/*path", h.ConfigV8)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v8/management/config/"+tc.path, strings.NewReader(`{}`)))
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			loaded, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.RequestRetry != 0 || !loaded.WebsocketAuth || loaded.Debug {
				t.Fatal("empty replacement did not restore defaults")
			}
		})
	}
}

func TestConfigV8EmptyExcludedModelsSurvivesSave(t *testing.T) {
	for _, tc := range []struct {
		name, rules, path, body string
	}{
		{"unrelated v0 write", "{}", "/v0/management/debug", `{"value":true}`},
		{"unrelated v8 write", "{}", "/v8/management/config/observability/logs/debug", `true`},
		{"explicit v8 empty write", "{codex: [blocked-model]}", "/v8/management/config/oauth/excluded-models", `{}`},
		{"v0 clears migrated rules", "{codex: [blocked-model]}", "/v0/management/oauth-excluded-models", `{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := "port: 8317\noauth:\n  excluded-models: " + tc.rules + "\n"
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: path}
			router := gin.New()
			router.PUT("/v0/management/debug", h.PutDebug)
			router.PUT("/v0/management/oauth-excluded-models", h.PutOAuthExcludedModels)
			router.PUT("/v8/management/config/*path", h.ConfigV8)
			router.GET("/v8/management/config/*path", h.ConfigV8)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, tc.path, strings.NewReader(tc.body)))
			if response.Code != http.StatusOK {
				t.Fatalf("save: status=%d body=%s", response.Code, response.Body.String())
			}
			response = httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v8/management/config/oauth/excluded-models", nil))
			if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "{}" {
				t.Errorf("explicit empty setting not preserved: status=%d body=%s", response.Code, response.Body.String())
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			// Later manual legacy edits must remain shadowed by the explicit v8 empty map.
			saved = append(saved, []byte("\noauth-excluded-models: {codex: [legacy-blocked-model]}\n")...)
			if err = os.WriteFile(path, saved, 0600); err != nil {
				t.Fatal(err)
			}
			after, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if len(after.OAuthExcludedModels) != 0 {
				t.Errorf("legacy rules became effective after saving: %v", after.OAuthExcludedModels)
			}
			saved, err = os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc yaml.Node
			if err = yaml.Unmarshal(saved, &doc); err != nil {
				t.Fatal(err)
			}
			if configV8Node(doc.Content[0], []string{"oauth-excluded-models"}) != nil {
				t.Error("load retained conflicting legacy rules")
			}
		})
	}
}

func TestConfigV8JSONTURNSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte(`oauth:
  providers:
    codex:
      live-media-relay:
        ice-servers:
          - {urls: ['turn:example.invalid:3478'], username: test-relay-user, credential: test-relay-password}
          - {urls: ['turn:example.invalid:3478'], username: test-relay-user-2, credential: test-relay-password-2}
`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	r := gin.New()
	r.GET("/v8/management/config", h.ConfigV8)
	r.PUT("/v8/management/config", h.ConfigV8)
	r.GET("/v8/management/config.yaml", h.ConfigV8)
	r.GET("/v8/management/config/*path", h.ConfigV8)
	r.PUT("/v8/management/config/*path", h.ConfigV8)
	for _, url := range []string{"/v8/management/config", "/v8/management/config/oauth/providers/codex/live-media-relay/ice-servers"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, url, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("GET status=%d body=%s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "test-relay-user") || strings.Contains(w.Body.String(), "test-relay-password") {
			t.Fatal("JSON exposed TURN credentials")
		}
		body := w.Body.String()
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, url, strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
		}
		loaded, err := config.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		servers := loaded.Codex.LiveMediaRelay.ICEServers
		if len(servers) != 2 || servers[0].Username != "test-relay-user" || servers[0].Credential != "test-relay-password" || servers[1].Username != "test-relay-user-2" || servers[1].Credential != "test-relay-password-2" {
			t.Fatal("JSON round trip changed redacted credentials")
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v8/management/config.yaml", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "test-relay-password") {
		t.Fatal("YAML export lost TURN credentials")
	}
	for _, body := range []string{
		`[{"urls":["turn:replacement.invalid:3478"]}]`,
		`[{"urls":["turn:example.invalid:3478"],"username":"","credential":null}]`,
	} {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
		w = httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPut, "/v8/management/config/oauth/providers/codex/live-media-relay/ice-servers", strings.NewReader(body)))
		if w.Code != http.StatusOK {
			t.Fatalf("PUT status=%d body=%s", w.Code, w.Body.String())
		}
		loaded, err := config.LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if server := loaded.Codex.LiveMediaRelay.ICEServers[0]; server.Username != "" || server.Credential != "" {
			t.Fatal("unexpected inherited TURN credentials")
		}
	}
}

func TestConfigV8DeletePreservesDocumentPresence(t *testing.T) {
	gin.SetMode(gin.TestMode)
	file := filepath.Join(t.TempDir(), "config.yaml")
	raw := `# Keep document comment
config-version: 8
server: {port: 8317}
routing:
  retry:
    request-retry: 3
    max-retry-interval: 30
plugins:
  configs:
    sample:
      enabled: false
      options: {} # Keep empty mapping
      custom-null: null # Keep explicit null
      custom-tree: {unknown: [one, {two: 2}]}
`
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Home = config.HomeConfig{Enabled: true, Host: "runtime.example"}
	h := &Handler{cfg: cfg, configFilePath: file}
	reloads := make(chan *config.Config, 8)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloads <- cfg })
	router := gin.New()
	router.DELETE("/v8/management/config/*path", h.ConfigV8)
	router.GET("/v8/management/config/*path", h.ConfigV8)
	request := func(method, path string, status int, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(method, "/v8/management/config/"+path, nil))
		if w.Code != status || (body != "" && strings.TrimSpace(w.Body.String()) != body) {
			t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
		}
	}
	read := func() ([]byte, *yaml.Node) {
		t.Helper()
		data, errRead := os.ReadFile(file)
		if errRead != nil {
			t.Fatal(errRead)
		}
		var doc yaml.Node
		if errDecode := yaml.Unmarshal(data, &doc); errDecode != nil {
			t.Fatal(errDecode)
		}
		return data, doc.Content[0]
	}
	checkReload := func() {
		t.Helper()
		loaded, errLoad := config.LoadConfig(file)
		if errLoad != nil {
			t.Fatal(errLoad)
		}
		loaded.Home = cfg.Home
		select {
		case snapshot := <-reloads:
			if !reflect.DeepEqual(snapshot, loaded) || !reflect.DeepEqual(h.cfg, loaded) {
				t.Fatal("runtime config or reload snapshot differs from persisted config")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("missing reload")
		}
	}
	_, original := read()
	request(http.MethodDelete, "routing/retry/request-retry", http.StatusOK, "")
	checkReload()
	// Simulate a new sibling written on the server after the first deletion.
	// The next request must use the latest file, not a stale runtime projection.
	data, _ := read()
	data = []byte(strings.Replace(string(data), "max-retry-interval: 30", "max-retry-interval: 30\n        max-retry-credentials: 7 # Keep new sibling", 1))
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	request(http.MethodDelete, "routing/retry/max-retry-interval", http.StatusOK, "")
	checkReload()
	data, root := read()
	for _, path := range []string{"routing/retry/request-retry", "routing/retry/max-retry-interval", "observability", "server/host"} {
		if configV8Node(root, strings.Split(path, "/")) != nil {
			t.Fatalf("absent field was materialized: %s", path)
		}
		request(http.MethodGet, path, http.StatusNotFound, "")
	}
	request(http.MethodGet, "routing/retry/max-retry-credentials", http.StatusOK, "7")
	// Apart from the requested removals and the server-side sibling, the whole
	// document must retain the same presence and values (including null/maps).
	expected := cloneConfigV8Node(original)
	deleteConfigV8Path(expected, []string{"routing", "retry", "request-retry"})
	retry := configV8Node(expected, []string{"routing", "retry"})
	retry.Content = append(retry.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "max-retry-credentials"},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: "7"})
	deleteConfigV8Path(expected, []string{"routing", "retry", "max-retry-interval"})
	var expectedValue, actualValue any
	if err = expected.Decode(&expectedValue); err != nil {
		t.Fatal(err)
	}
	if err = root.Decode(&actualValue); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(expectedValue, actualValue) {
		t.Fatal("DELETE changed unrelated document fields or their presence")
	}
	pluginPath := []string{"plugins", "configs", "sample"}
	var beforePlugin, afterPlugin any
	if err = configV8Node(original, pluginPath).Decode(&beforePlugin); err != nil {
		t.Fatal(err)
	}
	if err = configV8Node(root, pluginPath).Decode(&afterPlugin); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforePlugin, afterPlugin) {
		t.Fatal("opaque plugin settings changed")
	}
	for _, comment := range []string{"# Keep document comment", "# Keep empty mapping", "# Keep explicit null", "# Keep new sibling"} {
		if !strings.Contains(string(data), comment) {
			t.Fatalf("lost comment %s", comment)
		}
	}
	for _, tc := range []struct{ name, body string }{{"options", "{}"}, {"custom-null", "null"}} {
		path := "plugins/configs/sample/" + tc.name
		request(http.MethodGet, path, http.StatusOK, tc.body)
		request(http.MethodDelete, path, http.StatusOK, "")
		checkReload()
		request(http.MethodGet, path, http.StatusNotFound, "")
		request(http.MethodDelete, path, http.StatusNotFound, "")
	}
}

func TestConfigV8SaveNormalizesCollectionStylesToBlockYAML(t *testing.T) {
	gin.SetMode(gin.TestMode)
	file := filepath.Join(t.TempDir(), "config.yaml")
	raw := `config-version: 8
server:
  port: 8317
api-keys:
  openai-compatibility:
    - name: compat-provider
      base-url: "https://api.example.com"
      headers:
        User-Agent: "$User-Agent"
      models:
        - alias: "coding"
          name: "gpt-4o"
      keys:
        - api-key: "sk-compat-1"
`
	if errWrite := os.WriteFile(file, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(file)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := &Handler{cfg: cfg, configFilePath: file}
	router := gin.New()
	router.PUT("/v8/management/config", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)

	// Send JSON update simulating v8 management panel saving complex items
	jsonUpdate := `[{"name":"compat-provider","base-url":"https://api.example.com","headers":{"User-Agent":"$User-Agent"},"models":[{"alias":"coding","name":"gpt-4o"}],"keys":[{"api-key":"sk-compat-1"}]}]`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/v8/management/config/api-keys/openai-compatibility", strings.NewReader(jsonUpdate))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT failed: status=%d body=%s", w.Code, w.Body.String())
	}

	saved, errRead := os.ReadFile(file)
	if errRead != nil {
		t.Fatal(errRead)
	}
	savedStr := string(saved)

	// Ensure the saved config does NOT contain inline JSON / flow style for complex collections
	if strings.Contains(savedStr, "[{") || strings.Contains(savedStr, `headers: {`) || strings.Contains(savedStr, `models: [{`) {
		t.Fatalf("config contains flow style / inline JSON collections:\n%s", savedStr)
	}
	if !strings.Contains(savedStr, "openai-compatibility:") || !strings.Contains(savedStr, "- name:") || !strings.Contains(savedStr, "base-url:") {
		t.Fatalf("expected block style sequence with unquoted keys for openai-compatibility, got:\n%s", savedStr)
	}
	if strings.Contains(savedStr, `"name":`) || strings.Contains(savedStr, `"base-url":`) {
		t.Fatalf("expected keys to be unquoted in block YAML, got:\n%s", savedStr)
	}

	// Also test PATCH at root /v8/management/config with JSON
	patchJSON := `{"api-keys":{"openai-compatibility":[{"name":"compat-provider-2","base-url":"https://api.example2.com","headers":{"User-Agent":"$User-Agent-2"},"models":[{"alias":"coding-2","name":"gpt-4o-mini"}],"keys":[{"api-key":"sk-compat-2"}]}]}}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(patchJSON))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH failed: status=%d body=%s", w.Code, w.Body.String())
	}
	saved, errRead = os.ReadFile(file)
	if errRead != nil {
		t.Fatal(errRead)
	}
	savedStr = string(saved)
	if strings.Contains(savedStr, "[{") || strings.Contains(savedStr, `headers: {`) || strings.Contains(savedStr, `models: [{`) {
		t.Fatalf("PATCH saved config contains flow style / inline JSON collections:\n%s", savedStr)
	}
	if !strings.Contains(savedStr, "openai-compatibility:") || !strings.Contains(savedStr, "- name:") {
		t.Fatalf("expected block style sequence after PATCH for openai-compatibility, got:\n%s", savedStr)
	}

	// Verify that empty sequence retains flow style []
	emptyJSON := `{"server":{"trusted-proxies":[]}}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(emptyJSON))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH with empty sequence failed: status=%d body=%s", w.Code, w.Body.String())
	}
	saved, errRead = os.ReadFile(file)
	if errRead != nil {
		t.Fatal(errRead)
	}
	savedStr = string(saved)
	if !strings.Contains(savedStr, "trusted-proxies: []") {
		t.Fatalf("expected empty sequence to retain flow style [], got:\n%s", savedStr)
	}

	// Also test root PUT with entire JSON document
	rootPutJSON := `{"config-version":8,"server":{"port":8317},"api-keys":{"openai-compatibility":[{"name":"compat-provider-root","base-url":"https://api.example3.com","headers":{"User-Agent":"$User-Agent-3"},"models":[{"alias":"coding-3","name":"gpt-4o"}],"keys":[{"api-key":"sk-compat-3"}]}]}}`
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v8/management/config", strings.NewReader(rootPutJSON))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("root PUT failed: status=%d body=%s", w.Code, w.Body.String())
	}
	saved, errRead = os.ReadFile(file)
	if errRead != nil {
		t.Fatal(errRead)
	}
	savedStr = string(saved)
	if strings.Contains(savedStr, "[{") || strings.Contains(savedStr, `headers: {`) || strings.Contains(savedStr, `models: [{`) {
		t.Fatalf("root PUT saved config contains flow style / inline JSON collections:\n%s", savedStr)
	}
	if strings.Contains(savedStr, `"config-version":`) || strings.Contains(savedStr, `"server":`) || strings.Contains(savedStr, `"api-keys":`) {
		t.Fatalf("root PUT saved config contains quoted keys:\n%s", savedStr)
	}
	if !strings.Contains(savedStr, "compat-provider-root") {
		t.Fatalf("expected compat-provider-root in saved config, got:\n%s", savedStr)
	}

	// Verify the final saved file loads correctly into Config
	loaded, errLoaded := config.LoadConfig(file)
	if errLoaded != nil {
		t.Fatalf("LoadConfig failed on normalized saved YAML: %v", errLoaded)
	}
	if len(loaded.OpenAICompatibility) != 1 || loaded.OpenAICompatibility[0].Name != "compat-provider-root" {
		t.Fatalf("unexpected loaded config content: %+v", loaded.OpenAICompatibility)
	}
	if loaded.OpenAICompatibility[0].Headers["User-Agent"] != "$User-Agent-3" {
		t.Fatalf("unexpected loaded headers: %+v", loaded.OpenAICompatibility[0].Headers)
	}
}
