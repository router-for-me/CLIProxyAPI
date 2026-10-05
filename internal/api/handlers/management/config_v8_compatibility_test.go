package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8HistoricalFieldPaths(t *testing.T) {
	for _, tc := range []struct{ historical, current, value string }{
		{"oauth/providers/codex/disable-codex-cloaking", "upstream/codex/disable-codex-cloaking", "true"},
		{"oauth/providers/codex/stream-bootstrap-buffering", "upstream/codex/stream-bootstrap-buffering", "true"},
		{"oauth/providers/codex/stream-bootstrap-timeout", "upstream/codex/stream-bootstrap-timeout", `"10s"`},
		{"oauth/providers/codex/orphan-delegation-compatibility", "upstream/codex/orphan-delegation-compatibility", "true"},
		{"oauth/providers/codex/model-level-cooling", "upstream/codex/model-level-cooling", "true"},
		{"oauth/providers/codex/response-steering", "upstream/codex/response-steering", "true"},
		{"oauth/providers/claude/model-level-cooling", "upstream/claude/model-level-cooling", "true"},
		{"oauth/providers/claude/disable-claude-cloak-mode", "upstream/claude/disable-claude-cloak-mode", "true"},
		{"oauth/providers/claude/header-defaults/user-agent", "upstream/claude/header-defaults/user-agent", `"test-agent"`},
		{"oauth/providers/claude/header-defaults/package-version", "upstream/claude/header-defaults/package-version", `"0.2.0"`},
		{"oauth/providers/claude/header-defaults/runtime-version", "upstream/claude/header-defaults/runtime-version", `"v22"`},
		{"oauth/providers/claude/header-defaults/os", "upstream/claude/header-defaults/os", `"Windows"`},
		{"oauth/providers/claude/header-defaults/arch", "upstream/claude/header-defaults/arch", `"amd64"`},
		{"oauth/providers/claude/header-defaults/timeout", "upstream/claude/header-defaults/timeout", `"300"`},
		{"oauth/providers/claude/header-defaults/timezone", "upstream/claude/header-defaults/timezone", `"Asia/Shanghai"`},
		{"oauth/providers/claude/header-defaults/stabilize-device-profile", "upstream/claude/header-defaults/stabilize-device-profile", "true"},
		{"oauth/providers/claude/claude-code/disable-cloaking-model-list", "upstream/claude/disable-cloaking-model-list", "false"},
		{"oauth/providers/xai/inject-x-search", "upstream/xai/inject-x-search", "true"},
		{"oauth/providers/codex/optimize-multi-agent-v2", "client/codex/optimize-multi-agent-v2", "true"},
	} {
		t.Run(tc.historical, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(file, []byte("server: {port: 8317}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: file}
			router := gin.New()
			for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
				router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
			}
			request := func(method, path, body string, want int) string {
				t.Helper()
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(method, "/v8/management/config/"+path, strings.NewReader(body)))
				if response.Code != want {
					t.Fatalf("%s %s: %d, want %d: %s", method, path, response.Code, want, response.Body.String())
				}
				return response.Body.String()
			}
			request(http.MethodPut, tc.current, tc.value, http.StatusOK)
			if got := request(http.MethodGet, tc.historical, "", http.StatusOK); got != tc.value {
				t.Fatalf("historical GET returned %s, want %s", got, tc.value)
			}
			request(http.MethodPatch, tc.historical, tc.value, http.StatusOK)
			request(http.MethodPut, tc.historical, "null", http.StatusOK)
			if got := request(http.MethodGet, tc.current, "", http.StatusOK); got != "null" {
				t.Fatalf("historical PUT lost explicit null: %s", got)
			}
			request(http.MethodDelete, tc.historical, "", http.StatusOK)
			request(http.MethodGet, tc.current, "", http.StatusNotFound)
			if got := request(http.MethodGet, "server/port", "", http.StatusOK); got != "8317" {
				t.Fatalf("historical DELETE changed unrelated settings: %s", got)
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err = config.ValidateV8Config(data); err != nil {
				t.Fatalf("saved historical aliases: %v", err)
			}
		})
	}
}

func TestConfigV8HistoricalProviderSubtrees(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body      string
		steering, buffering, optimize bool
		agent                         string
	}{
		{"patch old provider", http.MethodPatch, "oauth/providers/codex", `{"response-steering":false,"header-defaults":{"user-agent":"updated"}}`, false, true, true, "updated"},
		{"replace old provider", http.MethodPut, "oauth/providers/codex", `{"response-steering":true}`, true, false, false, ""},
		{"delete old provider", http.MethodDelete, "oauth/providers/codex", "", false, false, false, ""},
		{"replace shared provider", http.MethodPut, "upstream/codex", `{"response-steering":false}`, false, false, true, "oauth-agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			raw := "upstream: {codex: {response-steering: true, stream-bootstrap-buffering: true}, xai: {inject-x-search: true}}\noauth: {providers: {codex: {header-defaults: {user-agent: oauth-agent}}}}\nclient: {codex: {optimize-multi-agent-v2: true, enable-apply-patch: true}}\n"
			if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: file}
			router := gin.New()
			router.GET("/v8/management/config/*path", h.ConfigV8)
			router.Handle(tc.method, "/v8/management/config/*path", h.ConfigV8)
			view := httptest.NewRecorder()
			router.ServeHTTP(view, httptest.NewRequest(http.MethodGet, "/v8/management/config/oauth/providers/codex", nil))
			var provider map[string]any
			if view.Code != http.StatusOK || json.Unmarshal(view.Body.Bytes(), &provider) != nil || provider["response-steering"] != true || provider["optimize-multi-agent-v2"] != true {
				t.Fatalf("historical provider view omitted shared/client values: %s", view.Body.String())
			}
			if data, errRead := os.ReadFile(file); errRead != nil || string(data) != raw {
				t.Fatalf("historical GET changed the file: %v", errRead)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(tc.method, "/v8/management/config/"+tc.path, strings.NewReader(tc.body)))
			if response.Code != http.StatusOK {
				t.Fatalf("historical mutation failed: %s", response.Body.String())
			}
			loaded, err := config.LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.Codex.ResponseSteering != tc.steering || loaded.Codex.StreamBootstrapBuffering != tc.buffering || loaded.Client.Codex.OptimizeMultiAgentV2 != tc.optimize || loaded.CodexHeaderDefaults.UserAgent != tc.agent {
				t.Fatal("historical subtree mutation did not preserve replacement/merge semantics")
			}
			if !loaded.XAI.InjectXSearch || !loaded.Client.Codex.EnableApplyPatch || loaded.ForAPIKey().CodexHeaderDefaults.UserAgent != "" {
				t.Fatal("historical subtree mutation changed unrelated settings or OAuth scope")
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err = config.ValidateV8Config(data); err != nil {
				t.Fatalf("saved historical provider layout: %v", err)
			}
		})
	}
}

func TestConfigV8HistoricalConfigurationBodies(t *testing.T) {
	for _, tc := range []struct {
		name, method, route, body string
		steering, buffering       bool
	}{
		{"patch old body", http.MethodPatch, "config", `{"oauth":{"providers":{"codex":{"response-steering":false}}}}`, false, true},
		{"canonical false wins", http.MethodPatch, "config", `{"oauth":{"providers":{"codex":{"response-steering":true}}},"upstream":{"codex":{"response-steering":false}}}`, false, true},
		{"canonical null wins", http.MethodPatch, "config", `{"oauth":{"providers":{"codex":{"response-steering":true}}},"upstream":{"codex":{"response-steering":null}}}`, false, true},
		{"replace old JSON", http.MethodPut, "config", `{"oauth":{"providers":{"codex":{"response-steering":true}}}}`, true, false},
		{"replace old YAML", http.MethodPut, "config.yaml", "# Keep this comment\noauth: {providers: {codex: {response-steering: false}}}\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(file, []byte("upstream: {codex: {response-steering: true, stream-bootstrap-buffering: true}}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: file}
			router := gin.New()
			router.Handle(tc.method, "/v8/management/"+tc.route, h.ConfigV8)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(tc.method, "/v8/management/"+tc.route, strings.NewReader(tc.body)))
			if response.Code != http.StatusOK {
				t.Fatalf("historical body failed: %s", response.Body.String())
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if err = config.ValidateV8Config(data); err != nil {
				t.Fatalf("saved historical request body: %v", err)
			}
			if !strings.Contains(string(data), "config-version: 8") {
				t.Fatal("historical v8 request did not save the latest version")
			}
			loaded, err := config.LoadConfig(file)
			if err != nil || loaded.Codex.ResponseSteering != tc.steering || loaded.Codex.StreamBootstrapBuffering != tc.buffering {
				t.Fatalf("historical body lost effective values: %v", err)
			}
			if tc.route == "config.yaml" && !strings.Contains(string(data), "# Keep this comment") {
				t.Fatal("YAML alias normalization lost comments")
			}
		})
	}
}

func TestLegacyConfigYAMLSavesBySubmittedVersion(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		steering   bool
		v8         bool
	}{
		{"legacy", "# Keep this comment\ncodex: {response-steering: true}\n", true, false},
		{"historical v8", "# Keep this comment\noauth: {providers: {codex: {response-steering: true}}}\n", true, true},
		{"latest v8", "# Keep this comment\nconfig-version: 8\nupstream: {codex: {response-steering: false}}\n", false, true},
		{"mixed", "# Keep this comment\nrequest-retry: 3\nupstream: {codex: {response-steering: false}}\n", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(file, []byte("server: {port: 8317}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			h := &Handler{configFilePath: file}
			router := gin.New()
			router.PUT("/v0/management/config.yaml", h.PutConfigYAML)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v0/management/config.yaml", strings.NewReader(tc.body)))
			if response.Code != http.StatusOK {
				t.Fatalf("legacy YAML update failed: %s", response.Body.String())
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.v8 && string(data) != tc.body {
				t.Fatalf("v0 changed the submitted layout:\n%s", data)
			}
			if tc.v8 {
				if err = config.ValidateV8Config(data); err != nil || !strings.Contains(string(data), "config-version: 8") {
					t.Fatalf("v0 did not normalize the submitted v8 document: %v", err)
				}
			}
			if !strings.Contains(string(data), "# Keep this comment") {
				t.Fatal("v0 YAML write lost comments")
			}
			if h.cfg.ForAPIKey().Codex.ResponseSteering != tc.steering {
				t.Fatal("legacy YAML update changed shared runtime values")
			}
		})
	}
}

func TestV0SetterSavesByExistingVersion(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		v8        bool
	}{
		{"legacy", "request-retry: 3\ncodex: {response-steering: true}\nxai: {inject-x-search: true}\nrouting: {strategy: fill-first}\n", false},
		{"historical v8", "config-version: 8\noauth: {providers: {codex: {response-steering: true, header-defaults: {user-agent: oauth-agent}}, xai: {inject-x-search: true}}}\n", true},
		{"unversioned v8", "oauth: {providers: {codex: {response-steering: true}, xai: {inject-x-search: true}}}\n", true},
		{"latest v8", "config-version: 8\nupstream: {codex: {response-steering: true}, xai: {inject-x-search: true}}\nrouting: {retry: {request-retry: 3}}\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(file, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(file)
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{cfg: cfg, configFilePath: file}
			router := gin.New()
			router.PUT("/v0/management/request-retry", h.PutRequestRetry)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v0/management/request-retry", strings.NewReader(`{"value":2}`)))
			if response.Code != http.StatusOK {
				t.Fatalf("v0 setter failed: %s", response.Body.String())
			}
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if tc.v8 {
				if err = config.ValidateV8Config(data); err != nil || !strings.Contains(string(data), "config-version: 8") {
					t.Fatalf("v0 saved historical/legacy paths in a v8 file: %v", err)
				}
			} else if strings.Contains(string(data), "config-version:") || strings.Contains(string(data), "upstream:") {
				t.Fatal("v0 migrated a legacy-only file")
			}
			loaded, err := config.LoadConfig(file)
			if err != nil || loaded.RequestRetry != 2 || !loaded.ForAPIKey().Codex.ResponseSteering || !loaded.ForAPIKey().XAI.InjectXSearch || loaded.CodexHeaderDefaults.UserAgent != cfg.CodexHeaderDefaults.UserAgent {
				t.Fatalf("v0 save changed effective settings: %v", err)
			}
		})
	}
}

func TestConfigV8HistoricalNullContainerPatch(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	raw := "upstream: {claude: {model-level-cooling: true, header-defaults: {user-agent: agent, timezone: UTC, stabilize-device-profile: true}}}\n"
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: file}
	router := gin.New()
	router.PATCH("/v8/management/config", h.ConfigV8)
	response := httptest.NewRecorder()
	body := `{"oauth":{"providers":{"claude":{"header-defaults":null}}}}`
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(body)))
	if response.Code != http.StatusOK {
		t.Fatalf("historical null container failed: %s", response.Body.String())
	}
	loaded, err := config.LoadConfig(file)
	if err != nil || loaded.ClaudeHeaderDefaults.UserAgent != "" || loaded.ClaudeHeaderDefaults.Timezone != "" || !loaded.Claude.ModelLevelCooling {
		t.Fatalf("historical null container failed to reset defaults or changed cooling: %v", err)
	}
}

func TestV0RepeatedSavesKeepLegacyClientAlias(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	raw := "# LEGACY DOCUMENT\ncodex:\n  # OPTIMIZE HEAD\n  optimize-multi-agent-v2: true # OPTIMIZE INLINE\n  response-steering: true\nrequest-retry: 3\n"
	if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: file}
	router := gin.New()
	router.PUT("/v0/management/request-retry", h.PutRequestRetry)
	for _, body := range []string{`{"value":2}`, `{"value":1}`, `{"value":0}`} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPut, "/v0/management/request-retry", strings.NewReader(body)))
		if response.Code != http.StatusOK {
			t.Fatalf("v0 setter failed: %s", response.Body.String())
		}
		data, errRead := os.ReadFile(file)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if strings.Contains(string(data), "config-version:") || strings.Contains(string(data), "upstream:") || strings.Contains(string(data), "client:") || !strings.Contains(string(data), "optimize-multi-agent-v2: true") {
			t.Fatalf("v0 save changed the legacy layout: %s", data)
		}
		for _, marker := range []string{"LEGACY DOCUMENT", "OPTIMIZE HEAD", "OPTIMIZE INLINE"} {
			if count := strings.Count(string(data), marker); count != 1 {
				t.Fatalf("legacy comment %s occurs %d times: %s", marker, count, data)
			}
		}
		loaded, errLoad := config.LoadConfig(file)
		if errLoad != nil || !loaded.Client.Codex.OptimizeMultiAgentV2 || !loaded.ForAPIKey().Codex.ResponseSteering {
			t.Fatalf("v0 save changed effective settings: %v", errLoad)
		}
	}
}

func TestConfigV8FieldUpdatesKeepComments(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		for _, path := range []string{"oauth/providers/codex/response-steering", "upstream/codex/response-steering"} {
			t.Run(method+path, func(t *testing.T) {
				file := filepath.Join(t.TempDir(), "config.yaml")
				raw := "oauth:\n  providers:\n    codex: # PROVIDER INLINE\n      # FIELD HEAD\n      response-steering: true # FIELD INLINE\n\n      # FIELD FOOT\n"
				if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
				cfg, err := config.LoadConfig(file)
				if err != nil {
					t.Fatal(err)
				}
				h := &Handler{cfg: cfg, configFilePath: file}
				router := gin.New()
				router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
				for _, body := range []string{"false", "null", "true"} {
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(method, "/v8/management/config/"+path, strings.NewReader(body)))
					if response.Code != http.StatusOK {
						t.Fatalf("v8 field update failed: %s", response.Body.String())
					}
					data, errRead := os.ReadFile(file)
					if errRead != nil {
						t.Fatal(errRead)
					}
					for _, marker := range []string{"PROVIDER INLINE", "FIELD HEAD", "FIELD INLINE", "FIELD FOOT"} {
						if count := strings.Count(string(data), marker); count != 1 {
							t.Fatalf("field update changed %s (%d): %s", marker, count, data)
						}
					}
				}
			})
		}
	}
}
