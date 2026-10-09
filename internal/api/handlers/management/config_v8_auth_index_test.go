package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestConfigV8APIKeysExposeAuthIndex_Issue6287(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	rawYAML := `config-version: 8
port: 8317
api-keys:
  codex:
    - name: codex-group
      base-url: https://api.openai.invalid
      keys:
        - api-key: sk-codex-1
        - api-key: sk-codex-2
  claude:
    - name: claude-group
      base-url: https://api.anthropic.invalid
      keys:
        - api-key: sk-claude-1
  openai-compatibility:
    - name: compat-provider
      base-url: https://api.compat.invalid
      keys:
        - api-key: sk-compat-1
    - name: keyless-provider
      base-url: https://api.keyless.invalid
      keys: []
`
	if err := os.WriteFile(path, []byte(rawYAML), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}

	synth := synthesizer.NewConfigSynthesizer()
	synthCtx := &synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(synthCtx)
	if err != nil {
		t.Fatalf("synthesize auths: %v", err)
	}

	manager := coreauth.NewManager(nil, nil, nil)
	expectedIndexes := make(map[string]string)
	for _, a := range auths {
		if _, errReg := manager.Register(context.Background(), a); errReg != nil {
			t.Fatalf("register auth: %v", errReg)
		}
		apiKey := a.Attributes["api_key"]
		idx := a.EnsureIndex()
		if idx == "" {
			t.Fatalf("auth %s has empty EnsureIndex", a.ID)
		}
		name := a.Attributes["compat_name"]
		if name == "" {
			name = a.Provider
		}
		if apiKey != "" {
			expectedIndexes[name+":"+apiKey] = idx
		} else {
			expectedIndexes[name+":keyless"] = idx
		}
	}

	h := &Handler{cfg: cfg, configFilePath: path, authManager: manager}

	router := gin.New()
	router.GET("/v8/management/config", h.ConfigV8)
	router.GET("/v8/management/config.yaml", h.ConfigV8)
	router.GET("/v8/management/config/*path", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)

	// 1. GET /v8/management/config/api-keys/codex
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/codex", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/codex: status=%d body=%s", rec.Code, rec.Body.String())
	}

	var codexGroups []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &codexGroups); err != nil {
		t.Fatalf("decode codex groups: %v", err)
	}
	if len(codexGroups) != 1 {
		t.Fatalf("expected 1 codex group, got %d", len(codexGroups))
	}
	keys, ok := codexGroups[0]["keys"].([]any)
	if !ok || len(keys) != 2 {
		t.Fatalf("expected 2 keys in codex group, got %#v", codexGroups[0]["keys"])
	}
	for i, k := range keys {
		keyMap, ok := k.(map[string]any)
		if !ok {
			t.Fatalf("key %d is not map: %#v", i, k)
		}
		apiKey, _ := keyMap["api-key"].(string)
		authIndex, _ := keyMap["auth_index"].(string)
		wantIndex := expectedIndexes["codex:"+apiKey]
		if wantIndex == "" {
			t.Fatalf("missing expected index for codex key %s", apiKey)
		}
		if authIndex != wantIndex {
			t.Fatalf("codex key %s: auth_index = %q, want %q", apiKey, authIndex, wantIndex)
		}
	}

	// 2. GET /v8/management/config/api-keys
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var allAPIKeys map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &allAPIKeys); err != nil {
		t.Fatalf("decode all api-keys: %v", err)
	}
	claudeVal, ok := allAPIKeys["claude"].([]any)
	if !ok || len(claudeVal) != 1 {
		t.Fatalf("expected claude in api-keys, got %#v", allAPIKeys["claude"])
	}
	claudeGroup := claudeVal[0].(map[string]any)
	claudeKeys := claudeGroup["keys"].([]any)
	claudeKeyMap := claudeKeys[0].(map[string]any)
	if got := claudeKeyMap["auth_index"]; got != expectedIndexes["claude:sk-claude-1"] {
		t.Fatalf("claude key auth_index = %q, want %q", got, expectedIndexes["claude:sk-claude-1"])
	}

	// Verify keyless openai-compatibility group has group-level auth_index
	compatVal, ok := allAPIKeys["openai-compatibility"].([]any)
	if !ok || len(compatVal) != 2 {
		t.Fatalf("expected 2 openai-compatibility groups, got %#v", allAPIKeys["openai-compatibility"])
	}
	keylessGroup := compatVal[1].(map[string]any)
	if got := keylessGroup["auth_index"]; got != expectedIndexes["keyless-provider:keyless"] {
		t.Fatalf("keyless group auth_index = %q, want %q", got, expectedIndexes["keyless-provider:keyless"])
	}

	// 3. GET /v8/management/config (root)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var fullConfig map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &fullConfig); err != nil {
		t.Fatalf("decode full config: %v", err)
	}
	configAPIKeys, ok := fullConfig["api-keys"].(map[string]any)
	if !ok {
		t.Fatalf("full config missing api-keys object: %#v", fullConfig["api-keys"])
	}
	codexFromRoot := configAPIKeys["codex"].([]any)[0].(map[string]any)["keys"].([]any)[0].(map[string]any)
	if got := codexFromRoot["auth_index"]; got != expectedIndexes["codex:sk-codex-1"] {
		t.Fatalf("root config codex auth_index = %q, want %q", got, expectedIndexes["codex:sk-codex-1"])
	}

	// 4. GET /v8/management/config.yaml should not contain auth_index
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config.yaml", nil)
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config.yaml: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "auth_index") {
		t.Fatal("config.yaml output must not contain auth_index")
	}

	// 5. PUT round-trip with payload containing auth_index
	codexJSON, _ := json.Marshal(codexGroups)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v8/management/config/api-keys/codex", strings.NewReader(string(codexJSON)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /v8/management/config/api-keys/codex with auth_index: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Verify disk config does not have auth_index
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "auth_index") {
		t.Fatal("saved config.yaml must not contain auth_index")
	}

	// 6. PATCH on root containing auth_index in api-keys
	patchBody := `{"api-keys":{"claude":[{"name":"claude-group","base-url":"https://api.anthropic.invalid","keys":[{"api-key":"sk-claude-1","auth_index":"arbitrary-ignore"}]}]}}`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(patchBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH /v8/management/config with auth_index: status=%d body=%s", rec.Code, rec.Body.String())
	}

	saved, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "auth_index") {
		t.Fatal("saved config.yaml after PATCH must not contain auth_index")
	}

	// 7. Fallback when authManager is nil
	hNoManager := &Handler{cfg: cfg, configFilePath: path, authManager: nil}
	routerNoManager := gin.New()
	routerNoManager.GET("/v8/management/config/*path", hNoManager.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/codex", nil)
	routerNoManager.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET with nil authManager: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var noManagerGroups []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &noManagerGroups); err != nil {
		t.Fatalf("decode codex groups: %v", err)
	}
	key0 := noManagerGroups[0]["keys"].([]any)[0].(map[string]any)
	if got := key0["auth_index"]; got != expectedIndexes["codex:sk-codex-1"] {
		t.Fatalf("fallback auth_index = %q, want %q", got, expectedIndexes["codex:sk-codex-1"])
	}

	// 8. Meta provider with omitted base-url defaults to https://api.meta.ai/v1 matching runtime
	metaYAML := `config-version: 8
port: 8317
api-keys:
  meta:
    - name: meta-group
      keys:
        - api-key: sk-meta-default-base
`
	metaPath := filepath.Join(dir, "meta-config.yaml")
	if errWrite := os.WriteFile(metaPath, []byte(metaYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	metaCfg, errLoad := config.LoadConfig(metaPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	metaAuths, errMetaSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      metaCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errMetaSynth != nil || len(metaAuths) != 1 {
		t.Fatalf("synthesize meta auth: %v, len=%d", errMetaSynth, len(metaAuths))
	}
	metaManager := coreauth.NewManager(nil, nil, nil)
	if _, errReg := metaManager.Register(context.Background(), metaAuths[0]); errReg != nil {
		t.Fatal(errReg)
	}
	expectedMetaIndex := metaAuths[0].EnsureIndex()

	hMeta := &Handler{cfg: metaCfg, configFilePath: metaPath, authManager: metaManager}
	routerMeta := gin.New()
	routerMeta.GET("/v8/management/config/*path", hMeta.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/meta", nil)
	routerMeta.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/meta: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var metaGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &metaGroups); errDecode != nil {
		t.Fatalf("decode meta groups: %v", errDecode)
	}
	metaKey0 := metaGroups[0]["keys"].([]any)[0].(map[string]any)
	if got := metaKey0["auth_index"]; got != expectedMetaIndex {
		t.Fatalf("meta key auth_index = %q, want %q", got, expectedMetaIndex)
	}

	// 9. Explicit empty proxy-url on key overrides group proxy-url
	proxyYAML := `config-version: 8
port: 8317
api-keys:
  vertex:
    - name: vertex-group
      base-url: https://api.vertex.invalid
      proxy-url: http://proxy.group.invalid:8080
      keys:
        - api-key: sk-vertex-inherited
        - api-key: sk-vertex-explicit-empty
          proxy-url: ""
`
	proxyPath := filepath.Join(dir, "proxy-config.yaml")
	if errWrite := os.WriteFile(proxyPath, []byte(proxyYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	proxyCfg, errLoadProxy := config.LoadConfig(proxyPath)
	if errLoadProxy != nil {
		t.Fatal(errLoadProxy)
	}
	proxyAuths, errProxySynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      proxyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errProxySynth != nil || len(proxyAuths) != 2 {
		t.Fatalf("synthesize vertex auths: %v, len=%d", errProxySynth, len(proxyAuths))
	}
	proxyManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range proxyAuths {
		if _, errReg := proxyManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantInheritedIndex := proxyAuths[0].EnsureIndex()
	wantExplicitEmptyIndex := proxyAuths[1].EnsureIndex()

	hProxy := &Handler{cfg: proxyCfg, configFilePath: proxyPath, authManager: proxyManager}
	routerProxy := gin.New()
	routerProxy.GET("/v8/management/config/*path", hProxy.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/vertex", nil)
	routerProxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/vertex: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var vertexGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &vertexGroups); errDecode != nil {
		t.Fatalf("decode vertex groups: %v", errDecode)
	}
	vertexKeys := vertexGroups[0]["keys"].([]any)
	if got := vertexKeys[0].(map[string]any)["auth_index"]; got != wantInheritedIndex {
		t.Fatalf("vertex key 0 auth_index = %q, want %q", got, wantInheritedIndex)
	}
	if got := vertexKeys[1].(map[string]any)["auth_index"]; got != wantExplicitEmptyIndex {
		t.Fatalf("vertex key 1 auth_index = %q, want %q", got, wantExplicitEmptyIndex)
	}

	// 10. Plugin options and headers retaining auth_index outside of credential level
	pluginYAML := `config-version: 8
port: 8317
plugins:
  configs:
    custom-plugin:
      enabled: true
      options:
        auth_index: keep-this-plugin-field
api-keys:
  codex:
    - name: codex-group
      base-url: https://api.openai.invalid
      headers:
        auth_index: keep-group-header
      keys:
        - api-key: sk-codex-headers
          headers:
            auth_index: keep-key-header
`
	pluginPath := filepath.Join(dir, "plugin-config.yaml")
	if errWrite := os.WriteFile(pluginPath, []byte(pluginYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	pluginCfg, errLoadPlugin := config.LoadConfig(pluginPath)
	if errLoadPlugin != nil {
		t.Fatal(errLoadPlugin)
	}
	hPlugin := &Handler{cfg: pluginCfg, configFilePath: pluginPath}
	routerPlugin := gin.New()
	routerPlugin.GET("/v8/management/config/*path", hPlugin.ConfigV8)
	routerPlugin.PUT("/v8/management/config/*path", hPlugin.ConfigV8)

	// Fetch codex keys with auth_index injected
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/codex", nil)
	routerPlugin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET codex with plugin: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var pluginCodexGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &pluginCodexGroups); errDecode != nil {
		t.Fatal(errDecode)
	}

	// PUT codex keys back (round-trip with auth_index)
	codexBytes, _ := json.Marshal(pluginCodexGroups)
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/v8/management/config/api-keys/codex", strings.NewReader(string(codexBytes)))
	req.Header.Set("Content-Type", "application/json")
	routerPlugin.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT codex: status=%d body=%s", rec.Code, rec.Body.String())
	}

	// Read saved config on disk: plugin option auth_index and headers auth_index MUST be intact!
	savedPluginData, errRead := os.ReadFile(pluginPath)
	if errRead != nil {
		t.Fatal(errRead)
	}
	savedPluginStr := string(savedPluginData)
	if !strings.Contains(savedPluginStr, "keep-this-plugin-field") {
		t.Fatalf("plugin option auth_index was stripped! saved config:\n%s", savedPluginStr)
	}
	if !strings.Contains(savedPluginStr, "keep-group-header") {
		t.Fatalf("group header auth_index was stripped! saved config:\n%s", savedPluginStr)
	}
	if !strings.Contains(savedPluginStr, "keep-key-header") {
		t.Fatalf("key header auth_index was stripped! saved config:\n%s", savedPluginStr)
	}

	// 11. Duplicate Vertex keys in YAML map to the same deduplicated runtime auth_index
	dupVertexYAML := `config-version: 8
port: 8317
api-keys:
  vertex:
    - name: vertex-group
      base-url: https://api.vertex.invalid
      keys:
        - api-key: sk-vertex-shared
        - api-key: sk-vertex-shared
`
	dupVertexPath := filepath.Join(dir, "dup-vertex-config.yaml")
	if errWrite := os.WriteFile(dupVertexPath, []byte(dupVertexYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	dupVertexCfg, errLoadDup := config.LoadConfig(dupVertexPath)
	if errLoadDup != nil {
		t.Fatal(errLoadDup)
	}
	dupAuths, errDupSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      dupVertexCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	// Runtime deduplicates identical vertex keys to 1 credential!
	if errDupSynth != nil || len(dupAuths) != 1 {
		t.Fatalf("synthesize dup vertex auth: %v, len=%d", errDupSynth, len(dupAuths))
	}
	dupManager := coreauth.NewManager(nil, nil, nil)
	if _, errReg := dupManager.Register(context.Background(), dupAuths[0]); errReg != nil {
		t.Fatal(errReg)
	}
	expectedDupVertexIndex := dupAuths[0].EnsureIndex()

	hDup := &Handler{cfg: dupVertexCfg, configFilePath: dupVertexPath, authManager: dupManager}
	routerDup := gin.New()
	routerDup.GET("/v8/management/config/*path", hDup.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/vertex", nil)
	routerDup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/vertex: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var dupVertexGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &dupVertexGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	dupKeys := dupVertexGroups[0]["keys"].([]any)
	if len(dupKeys) != 2 {
		t.Fatalf("expected 2 keys in YAML response, got %d", len(dupKeys))
	}
	if got0 := dupKeys[0].(map[string]any)["auth_index"]; got0 != expectedDupVertexIndex {
		t.Fatalf("dup key 0 auth_index = %q, want %q", got0, expectedDupVertexIndex)
	}
	if got1 := dupKeys[1].(map[string]any)["auth_index"]; got1 != expectedDupVertexIndex {
		t.Fatalf("dup key 1 auth_index = %q, want %q", got1, expectedDupVertexIndex)
	}

	// 12. Unnormalized prefix (prefix: /team/) matches live credential with normalized prefix
	unnormalizedPrefixYAML := `config-version: 8
port: 8317
api-keys:
  codex:
    - name: codex-team
      prefix: /team/
      base-url: https://api.openai.invalid
      keys:
        - api-key: sk-codex-team
`
	prefixPath := filepath.Join(dir, "prefix-config.yaml")
	if errWrite := os.WriteFile(prefixPath, []byte(unnormalizedPrefixYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	prefixCfg, errLoadPrefix := config.LoadConfig(prefixPath)
	if errLoadPrefix != nil {
		t.Fatal(errLoadPrefix)
	}
	prefixAuths, errPrefixSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      prefixCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errPrefixSynth != nil || len(prefixAuths) != 1 {
		t.Fatalf("synthesize prefix auth: %v, len=%d", errPrefixSynth, len(prefixAuths))
	}
	prefixManager := coreauth.NewManager(nil, nil, nil)
	if _, errReg := prefixManager.Register(context.Background(), prefixAuths[0]); errReg != nil {
		t.Fatal(errReg)
	}
	expectedPrefixIndex := prefixAuths[0].EnsureIndex()

	hPrefix := &Handler{cfg: prefixCfg, configFilePath: prefixPath, authManager: prefixManager}
	routerPrefix := gin.New()
	routerPrefix.GET("/v8/management/config/*path", hPrefix.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/codex", nil)
	routerPrefix.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/codex: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var prefixGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &prefixGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	prefixKey0 := prefixGroups[0]["keys"].([]any)[0].(map[string]any)
	if got := prefixKey0["auth_index"]; got != expectedPrefixIndex {
		t.Fatalf("prefix key auth_index = %q, want %q", got, expectedPrefixIndex)
	}

	// 13. Gemini with duplicate keys [A, A, B]: both A get A's index, B gets B's index
	geminiDupYAML := `config-version: 8
port: 8317
api-keys:
  gemini:
    - name: gemini-group
      base-url: https://api.gemini.invalid
      keys:
        - api-key: sk-gemini-a
        - api-key: sk-gemini-a
        - api-key: sk-gemini-b
`
	geminiPath := filepath.Join(dir, "gemini-dup-config.yaml")
	if errWrite := os.WriteFile(geminiPath, []byte(geminiDupYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	geminiCfg, errLoadGemini := config.LoadConfig(geminiPath)
	if errLoadGemini != nil {
		t.Fatal(errLoadGemini)
	}
	geminiAuths, errGeminiSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      geminiCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	// Runtime deduplicates identical gemini keys to 2 credentials: A and B
	if errGeminiSynth != nil || len(geminiAuths) != 2 {
		t.Fatalf("synthesize gemini dup auth: %v, len=%d", errGeminiSynth, len(geminiAuths))
	}
	geminiManager := coreauth.NewManager(nil, nil, nil)
	wantGeminiA := ""
	wantGeminiB := ""
	for _, a := range geminiAuths {
		if _, errReg := geminiManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
		if a.Attributes["api_key"] == "sk-gemini-a" {
			wantGeminiA = a.EnsureIndex()
		} else if a.Attributes["api_key"] == "sk-gemini-b" {
			wantGeminiB = a.EnsureIndex()
		}
	}
	if wantGeminiA == "" || wantGeminiB == "" || wantGeminiA == wantGeminiB {
		t.Fatalf("unexpected gemini auth indexes: A=%q, B=%q", wantGeminiA, wantGeminiB)
	}

	hGemini := &Handler{cfg: geminiCfg, configFilePath: geminiPath, authManager: geminiManager}
	routerGemini := gin.New()
	routerGemini.GET("/v8/management/config/*path", hGemini.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/gemini", nil)
	routerGemini.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/gemini: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var geminiGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &geminiGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	geminiKeys := geminiGroups[0]["keys"].([]any)
	if len(geminiKeys) != 3 {
		t.Fatalf("expected 3 keys in gemini response, got %d", len(geminiKeys))
	}
	if got0 := geminiKeys[0].(map[string]any)["auth_index"]; got0 != wantGeminiA {
		t.Fatalf("gemini key 0 auth_index = %q, want %q", got0, wantGeminiA)
	}
	if got1 := geminiKeys[1].(map[string]any)["auth_index"]; got1 != wantGeminiA {
		t.Fatalf("gemini key 1 auth_index = %q, want %q", got1, wantGeminiA)
	}
	if got2 := geminiKeys[2].(map[string]any)["auth_index"]; got2 != wantGeminiB {
		t.Fatalf("gemini key 2 auth_index = %q, want %q", got2, wantGeminiB)
	}

	// 14. OpenAI-compatible with [empty_key, B] and invalid group without base-url
	compatMixedYAML := `config-version: 8
port: 8317
api-keys:
  openai-compatibility:
    - name: invalid-group-no-base
      keys:
        - api-key: sk-invalid
    - name: mixed-group
      base-url: https://api.mixed.invalid
      keys:
        - api-key: ""
        - api-key: sk-compat-b
`
	compatMixedPath := filepath.Join(dir, "compat-mixed-config.yaml")
	if errWrite := os.WriteFile(compatMixedPath, []byte(compatMixedYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	compatMixedCfg, errLoadMixed := config.LoadConfig(compatMixedPath)
	if errLoadMixed != nil {
		t.Fatal(errLoadMixed)
	}
	compatMixedAuths, errMixedSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      compatMixedCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errMixedSynth != nil || len(compatMixedAuths) != 2 {
		t.Fatalf("synthesize compat mixed auths: %v, len=%d", errMixedSynth, len(compatMixedAuths))
	}
	mixedManager := coreauth.NewManager(nil, nil, nil)
	wantEmptyKeyIndex := ""
	wantKeyBIndex := ""
	for _, a := range compatMixedAuths {
		if _, errReg := mixedManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
		if a.Attributes["api_key"] == "" {
			wantEmptyKeyIndex = a.EnsureIndex()
		} else if a.Attributes["api_key"] == "sk-compat-b" {
			wantKeyBIndex = a.EnsureIndex()
		}
	}
	if wantEmptyKeyIndex == "" || wantKeyBIndex == "" || wantEmptyKeyIndex == wantKeyBIndex {
		t.Fatalf("unexpected mixed auth indexes: empty=%q, B=%q", wantEmptyKeyIndex, wantKeyBIndex)
	}

	hMixed := &Handler{cfg: compatMixedCfg, configFilePath: compatMixedPath, authManager: mixedManager}
	routerMixed := gin.New()
	routerMixed.GET("/v8/management/config/*path", hMixed.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai-compatibility", nil)
	routerMixed.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/openai-compatibility: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var mixedGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &mixedGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	// Group 0 had no base-url, so its key has no auth_index
	invalidKeys := mixedGroups[0]["keys"].([]any)
	if gotInvalid := invalidKeys[0].(map[string]any)["auth_index"]; gotInvalid != nil {
		t.Fatalf("invalid group key auth_index = %v, want nil", gotInvalid)
	}
	// Group 1 has [empty_key, B]
	validKeys := mixedGroups[1]["keys"].([]any)
	if gotEmpty := validKeys[0].(map[string]any)["auth_index"]; gotEmpty != wantEmptyKeyIndex {
		t.Fatalf("empty key auth_index = %q, want %q", gotEmpty, wantEmptyKeyIndex)
	}
	if gotB := validKeys[1].(map[string]any)["auth_index"]; gotB != wantKeyBIndex {
		t.Fatalf("key B auth_index = %q, want %q", gotB, wantKeyBIndex)
	}

	// 15. Empty API key with valid base-url (Gemini base-url only) produces non-empty auth_index
	emptyKeyGeminiYAML := `config-version: 8
port: 8317
api-keys:
  gemini:
    - name: gemini-emulator
      base-url: http://emulator.invalid:8080
      keys:
        - api-key: ""
`
	emptyKeyPath := filepath.Join(dir, "gemini-empty-key.yaml")
	if errWrite := os.WriteFile(emptyKeyPath, []byte(emptyKeyGeminiYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	emptyKeyCfg, errLoadEmpty := config.LoadConfig(emptyKeyPath)
	if errLoadEmpty != nil {
		t.Fatal(errLoadEmpty)
	}
	emptyKeyAuths, errEmptySynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      emptyKeyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errEmptySynth != nil || len(emptyKeyAuths) != 1 {
		t.Fatalf("synthesize empty key auths: %v, len=%d", errEmptySynth, len(emptyKeyAuths))
	}
	wantEmulatorIndex := emptyKeyAuths[0].EnsureIndex()
	if wantEmulatorIndex == "" {
		t.Fatal("empty key auth EnsureIndex must not be empty")
	}

	hEmpty := &Handler{cfg: emptyKeyCfg, configFilePath: emptyKeyPath}
	routerEmpty := gin.New()
	routerEmpty.GET("/v8/management/config/*path", hEmpty.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/gemini", nil)
	routerEmpty.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v8/management/config/api-keys/gemini: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var emptyKeyGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &emptyKeyGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	if got := emptyKeyGroups[0]["keys"].([]any)[0].(map[string]any)["auth_index"]; got != wantEmulatorIndex {
		t.Fatalf("emulator auth_index = %q, want %q", got, wantEmulatorIndex)
	}

	// 16. Multiple OpenAI-compatible empty-key entries with different proxy-url
	compatMultiEmptyYAML := `config-version: 8
port: 8317
api-keys:
  openai-compatibility:
    - name: multi-empty-group
      base-url: https://api.multi.invalid
      keys:
        - api-key: ""
          proxy-url: http://proxy1.invalid:8080
        - api-key: ""
          proxy-url: http://proxy2.invalid:8080
`
	multiEmptyPath := filepath.Join(dir, "compat-multi-empty.yaml")
	if errWrite := os.WriteFile(multiEmptyPath, []byte(compatMultiEmptyYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	multiEmptyCfg, errLoadMulti := config.LoadConfig(multiEmptyPath)
	if errLoadMulti != nil {
		t.Fatal(errLoadMulti)
	}
	multiEmptyAuths, errMultiSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      multiEmptyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errMultiSynth != nil || len(multiEmptyAuths) != 2 {
		t.Fatalf("synthesize multi empty auths: %v, len=%d", errMultiSynth, len(multiEmptyAuths))
	}
	wantMultiIdx1 := multiEmptyAuths[0].EnsureIndex()
	wantMultiIdx2 := multiEmptyAuths[1].EnsureIndex()
	if wantMultiIdx1 == "" || wantMultiIdx2 == "" || wantMultiIdx1 == wantMultiIdx2 {
		t.Fatalf("expected distinct non-empty auth indexes for different proxies: %q vs %q", wantMultiIdx1, wantMultiIdx2)
	}

	hMulti := &Handler{cfg: multiEmptyCfg, configFilePath: multiEmptyPath}
	routerMulti := gin.New()
	routerMulti.GET("/v8/management/config/*path", hMulti.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai-compatibility", nil)
	routerMulti.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET multi empty: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var multiGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &multiGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	multiKeys := multiGroups[0]["keys"].([]any)
	if len(multiKeys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(multiKeys))
	}
	if got1 := multiKeys[0].(map[string]any)["auth_index"]; got1 != wantMultiIdx1 {
		t.Fatalf("multi key 1 auth_index = %q, want %q", got1, wantMultiIdx1)
	}
	if got2 := multiKeys[1].(map[string]any)["auth_index"]; got2 != wantMultiIdx2 {
		t.Fatalf("multi key 2 auth_index = %q, want %q", got2, wantMultiIdx2)
	}

	// 17. Gemini with empty API keys and proxies [P1, P1, P2]: key 0 and 1 get P1, key 2 gets P2
	geminiProxyYAML := `config-version: 8
port: 8317
api-keys:
  gemini:
    - name: gemini-proxy-group
      base-url: http://emulator.invalid:8080
      keys:
        - api-key: ""
          proxy-url: http://proxy1.invalid:8080
        - api-key: ""
          proxy-url: http://proxy1.invalid:8080
        - api-key: ""
          proxy-url: http://proxy2.invalid:8080
`
	geminiProxyPath := filepath.Join(dir, "gemini-proxy-dup.yaml")
	if errWrite := os.WriteFile(geminiProxyPath, []byte(geminiProxyYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	geminiProxyCfg, errLoadGProxy := config.LoadConfig(geminiProxyPath)
	if errLoadGProxy != nil {
		t.Fatal(errLoadGProxy)
	}
	gProxyAuths, errGProxySynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      geminiProxyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	// Runtime deduplicates identical empty key + proxy entries to 2 credentials: P1 and P2
	if errGProxySynth != nil || len(gProxyAuths) != 2 {
		t.Fatalf("synthesize gemini proxy auths: %v, len=%d", errGProxySynth, len(gProxyAuths))
	}
	gProxyManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range gProxyAuths {
		if _, errReg := gProxyManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantGProxy1 := gProxyAuths[0].EnsureIndex()
	wantGProxy2 := gProxyAuths[1].EnsureIndex()
	if wantGProxy1 == "" || wantGProxy2 == "" || wantGProxy1 == wantGProxy2 {
		t.Fatalf("expected distinct indexes for P1 and P2: %q vs %q", wantGProxy1, wantGProxy2)
	}

	hGProxy := &Handler{cfg: geminiProxyCfg, configFilePath: geminiProxyPath, authManager: gProxyManager}
	routerGProxy := gin.New()
	routerGProxy.GET("/v8/management/config/*path", hGProxy.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/gemini", nil)
	routerGProxy.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET gemini proxy: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var gProxyGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &gProxyGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	gProxyKeys := gProxyGroups[0]["keys"].([]any)
	if len(gProxyKeys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(gProxyKeys))
	}
	if got0 := gProxyKeys[0].(map[string]any)["auth_index"]; got0 != wantGProxy1 {
		t.Fatalf("key 0 auth_index = %q, want %q", got0, wantGProxy1)
	}
	if got1 := gProxyKeys[1].(map[string]any)["auth_index"]; got1 != wantGProxy1 {
		t.Fatalf("key 1 auth_index = %q, want %q (should reuse P1, not jump to P2)", got1, wantGProxy1)
	}
	if got2 := gProxyKeys[2].(map[string]any)["auth_index"]; got2 != wantGProxy2 {
		t.Fatalf("key 2 auth_index = %q, want %q", got2, wantGProxy2)
	}

	// 18. Two OpenAI-compatible groups with identical name and base-url: no cross-group collision
	compatDupGroupYAML := `config-version: 8
port: 8317
api-keys:
  openai-compatibility:
    - name: shared-name
      base-url: https://api.shared.invalid
      keys:
        - api-key: ""
          proxy-url: http://proxyA.invalid:8080
    - name: shared-name
      base-url: https://api.shared.invalid
      keys:
        - api-key: ""
          proxy-url: http://proxyB.invalid:8080
`
	compatDupGroupPath := filepath.Join(dir, "compat-dup-group.yaml")
	if errWrite := os.WriteFile(compatDupGroupPath, []byte(compatDupGroupYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	compatDupGroupCfg, errLoadDupGroup := config.LoadConfig(compatDupGroupPath)
	if errLoadDupGroup != nil {
		t.Fatal(errLoadDupGroup)
	}
	dupGroupAuths, errDupGroupSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      compatDupGroupCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errDupGroupSynth != nil || len(dupGroupAuths) != 2 {
		t.Fatalf("synthesize dup group auths: %v, len=%d", errDupGroupSynth, len(dupGroupAuths))
	}
	dupGroupManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range dupGroupAuths {
		if _, errReg := dupGroupManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantGroup0Index := dupGroupAuths[0].EnsureIndex()
	wantGroup1Index := dupGroupAuths[1].EnsureIndex()
	if wantGroup0Index == "" || wantGroup1Index == "" || wantGroup0Index == wantGroup1Index {
		t.Fatalf("expected distinct indexes across groups: %q vs %q", wantGroup0Index, wantGroup1Index)
	}

	hDupGroup := &Handler{cfg: compatDupGroupCfg, configFilePath: compatDupGroupPath, authManager: dupGroupManager}
	routerDupGroup := gin.New()
	routerDupGroup.GET("/v8/management/config/*path", hDupGroup.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai-compatibility", nil)
	routerDupGroup.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET dup group: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var dupGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &dupGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(dupGroups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(dupGroups))
	}
	gotGroup0KeyIndex := dupGroups[0]["keys"].([]any)[0].(map[string]any)["auth_index"]
	gotGroup1KeyIndex := dupGroups[1]["keys"].([]any)[0].(map[string]any)["auth_index"]
	if gotGroup0KeyIndex != wantGroup0Index {
		t.Fatalf("group 0 key auth_index = %q, want %q", gotGroup0KeyIndex, wantGroup0Index)
	}
	if gotGroup1KeyIndex != wantGroup1Index {
		t.Fatalf("group 1 key auth_index = %q, want %q", gotGroup1KeyIndex, wantGroup1Index)
	}

	// 19. Gemini with empty API keys and different prefixes [A, A, B]: key 0 and 1 get A, key 2 gets B
	geminiPrefixYAML := `config-version: 8
port: 8317
api-keys:
  gemini:
    - name: gemini-prefix-group
      base-url: http://emulator.invalid:8080
      keys:
        - api-key: ""
          prefix: /team-a/
        - api-key: ""
          prefix: /team-a/
        - api-key: ""
          prefix: /team-b/
`
	geminiPrefixPath := filepath.Join(dir, "gemini-prefix-dup.yaml")
	if errWrite := os.WriteFile(geminiPrefixPath, []byte(geminiPrefixYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	geminiPrefixCfg, errLoadGPrefix := config.LoadConfig(geminiPrefixPath)
	if errLoadGPrefix != nil {
		t.Fatal(errLoadGPrefix)
	}
	gPrefixAuths, errGPrefixSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      geminiPrefixCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errGPrefixSynth != nil || len(gPrefixAuths) != 2 {
		t.Fatalf("synthesize gemini prefix auths: %v, len=%d", errGPrefixSynth, len(gPrefixAuths))
	}
	gPrefixManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range gPrefixAuths {
		if _, errReg := gPrefixManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantGPrefixA := gPrefixAuths[0].EnsureIndex()
	wantGPrefixB := gPrefixAuths[1].EnsureIndex()
	if wantGPrefixA == "" || wantGPrefixB == "" || wantGPrefixA == wantGPrefixB {
		t.Fatalf("expected distinct indexes for A and B: %q vs %q", wantGPrefixA, wantGPrefixB)
	}

	hGPrefix := &Handler{cfg: geminiPrefixCfg, configFilePath: geminiPrefixPath, authManager: gPrefixManager}
	routerGPrefix := gin.New()
	routerGPrefix.GET("/v8/management/config/*path", hGPrefix.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/gemini", nil)
	routerGPrefix.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET gemini prefix: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var gPrefixGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &gPrefixGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	gPrefixKeys := gPrefixGroups[0]["keys"].([]any)
	if len(gPrefixKeys) != 3 {
		t.Fatalf("expected 3 keys, got %d", len(gPrefixKeys))
	}
	if got0 := gPrefixKeys[0].(map[string]any)["auth_index"]; got0 != wantGPrefixA {
		t.Fatalf("key 0 auth_index = %q, want %q", got0, wantGPrefixA)
	}
	if got1 := gPrefixKeys[1].(map[string]any)["auth_index"]; got1 != wantGPrefixA {
		t.Fatalf("key 1 auth_index = %q, want %q (should reuse A, not jump to B)", got1, wantGPrefixA)
	}
	if got2 := gPrefixKeys[2].(map[string]any)["auth_index"]; got2 != wantGPrefixB {
		t.Fatalf("key 2 auth_index = %q, want %q", got2, wantGPrefixB)
	}

	// 20. Unnamed OpenAI-compatible groups (name omitted) expose correct auth_index
	unnamedCompatYAML := `config-version: 8
port: 8317
api-keys:
  openai-compatibility:
    - base-url: https://api.unnamed-keyless.invalid
      keys: []
    - base-url: https://api.unnamed-keyed.invalid
      keys:
        - api-key: sk-unnamed-key
`
	unnamedPath := filepath.Join(dir, "unnamed-compat.yaml")
	if errWrite := os.WriteFile(unnamedPath, []byte(unnamedCompatYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	unnamedCfg, errLoadUnnamed := config.LoadConfig(unnamedPath)
	if errLoadUnnamed != nil {
		t.Fatal(errLoadUnnamed)
	}
	unnamedAuths, errUnnamedSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      unnamedCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errUnnamedSynth != nil || len(unnamedAuths) != 2 {
		t.Fatalf("synthesize unnamed auths: %v, len=%d", errUnnamedSynth, len(unnamedAuths))
	}
	unnamedManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range unnamedAuths {
		if _, errReg := unnamedManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantUnnamedKeyless := unnamedAuths[0].EnsureIndex()
	wantUnnamedKeyed := unnamedAuths[1].EnsureIndex()
	if wantUnnamedKeyless == "" || wantUnnamedKeyed == "" || wantUnnamedKeyless == wantUnnamedKeyed {
		t.Fatalf("expected distinct indexes for unnamed compat: %q vs %q", wantUnnamedKeyless, wantUnnamedKeyed)
	}

	hUnnamed := &Handler{cfg: unnamedCfg, configFilePath: unnamedPath, authManager: unnamedManager}
	routerUnnamed := gin.New()
	routerUnnamed.GET("/v8/management/config/*path", hUnnamed.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/openai-compatibility", nil)
	routerUnnamed.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET unnamed compat: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var unnamedGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &unnamedGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	if len(unnamedGroups) != 2 {
		t.Fatalf("expected 2 unnamed groups, got %d", len(unnamedGroups))
	}
	if gotKeyless := unnamedGroups[0]["auth_index"]; gotKeyless != wantUnnamedKeyless {
		t.Fatalf("unnamed keyless group auth_index = %q, want %q", gotKeyless, wantUnnamedKeyless)
	}
	unnamedKeyMap := unnamedGroups[1]["keys"].([]any)[0].(map[string]any)
	if gotKeyed := unnamedKeyMap["auth_index"]; gotKeyed != wantUnnamedKeyed {
		t.Fatalf("unnamed keyed auth_index = %q, want %q", gotKeyed, wantUnnamedKeyed)
	}

	// 21. Claude group with empty key entry [{}, {key-a}, {key-b}]: empty key has no index, A gets A, B gets B
	claudeEmptyKeyYAML := `config-version: 8
port: 8317
api-keys:
  claude:
    - name: claude-group
      keys:
        - {}
        - api-key: sk-claude-real-a
        - api-key: sk-claude-real-b
`
	claudeEmptyPath := filepath.Join(dir, "claude-empty-key.yaml")
	if errWrite := os.WriteFile(claudeEmptyPath, []byte(claudeEmptyKeyYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	claudeEmptyCfg, errLoadClaudeEmpty := config.LoadConfig(claudeEmptyPath)
	if errLoadClaudeEmpty != nil {
		t.Fatal(errLoadClaudeEmpty)
	}
	claudeEmptyAuths, errClaudeSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      claudeEmptyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	// Empty Claude key with no base-url is skipped by synthesizeClaudeKeys, producing 2 auths: A and B
	if errClaudeSynth != nil || len(claudeEmptyAuths) != 2 {
		t.Fatalf("synthesize claude empty auths: %v, len=%d", errClaudeSynth, len(claudeEmptyAuths))
	}
	claudeEmptyManager := coreauth.NewManager(nil, nil, nil)
	for _, a := range claudeEmptyAuths {
		if _, errReg := claudeEmptyManager.Register(context.Background(), a); errReg != nil {
			t.Fatal(errReg)
		}
	}
	wantClaudeA := claudeEmptyAuths[0].EnsureIndex()
	wantClaudeB := claudeEmptyAuths[1].EnsureIndex()
	if wantClaudeA == "" || wantClaudeB == "" || wantClaudeA == wantClaudeB {
		t.Fatalf("expected distinct indexes for A and B: %q vs %q", wantClaudeA, wantClaudeB)
	}

	hClaudeEmpty := &Handler{cfg: claudeEmptyCfg, configFilePath: claudeEmptyPath, authManager: claudeEmptyManager}
	routerClaudeEmpty := gin.New()
	routerClaudeEmpty.GET("/v8/management/config/*path", hClaudeEmpty.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/claude", nil)
	routerClaudeEmpty.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET claude empty: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var claudeEmptyGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &claudeEmptyGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	emptyGroupKeys := claudeEmptyGroups[0]["keys"].([]any)
	if len(emptyGroupKeys) != 3 {
		t.Fatalf("expected 3 keys in claude response, got %d", len(emptyGroupKeys))
	}
	// Key 0 was empty, should have no auth_index
	if gotEmpty := emptyGroupKeys[0].(map[string]any)["auth_index"]; gotEmpty != nil {
		t.Fatalf("claude empty key auth_index = %v, want nil", gotEmpty)
	}
	// Key 1 is A
	if gotA := emptyGroupKeys[1].(map[string]any)["auth_index"]; gotA != wantClaudeA {
		t.Fatalf("claude key 1 auth_index = %q, want %q", gotA, wantClaudeA)
	}
	// Key 2 is B
	if gotB := emptyGroupKeys[2].(map[string]any)["auth_index"]; gotB != wantClaudeB {
		t.Fatalf("claude key 2 auth_index = %q, want %q", gotB, wantClaudeB)
	}

	// 22. Codex with multiple empty API keys in the same group with different models (non-deduplicated)
	codexEmptyKeysYAML := `config-version: 8
port: 8317
api-keys:
  codex:
    - name: codex-models-group
      base-url: https://api.openai.invalid
      keys:
        - api-key: ""
          models:
            - name: gpt-5
              alias: gpt-5
        - api-key: ""
          models:
            - name: gpt-6
              alias: gpt-6
`
	codexEmptyPath := filepath.Join(dir, "codex-empty-keys.yaml")
	if errWrite := os.WriteFile(codexEmptyPath, []byte(codexEmptyKeysYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	codexEmptyCfg, errLoadCodexEmpty := config.LoadConfig(codexEmptyPath)
	if errLoadCodexEmpty != nil {
		t.Fatal(errLoadCodexEmpty)
	}
	codexEmptyAuths, errCodexSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      codexEmptyCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errCodexSynth != nil || len(codexEmptyAuths) != 2 {
		t.Fatalf("synthesize codex empty auths: %v, len=%d", errCodexSynth, len(codexEmptyAuths))
	}
	wantCodexModel0 := codexEmptyAuths[0].EnsureIndex()
	wantCodexModel1 := codexEmptyAuths[1].EnsureIndex()
	if wantCodexModel0 == "" || wantCodexModel1 == "" || wantCodexModel0 == wantCodexModel1 {
		t.Fatalf("expected distinct indexes for codex empty keys: %q vs %q", wantCodexModel0, wantCodexModel1)
	}

	hCodexEmpty := &Handler{cfg: codexEmptyCfg, configFilePath: codexEmptyPath}
	routerCodexEmpty := gin.New()
	routerCodexEmpty.GET("/v8/management/config/*path", hCodexEmpty.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/codex", nil)
	routerCodexEmpty.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET codex empty keys: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var codexEmptyGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &codexEmptyGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	codexKeys := codexEmptyGroups[0]["keys"].([]any)
	if len(codexKeys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(codexKeys))
	}
	if got0 := codexKeys[0].(map[string]any)["auth_index"]; got0 != wantCodexModel0 {
		t.Fatalf("codex key 0 auth_index = %q, want %q", got0, wantCodexModel0)
	}
	if got1 := codexKeys[1].(map[string]any)["auth_index"]; got1 != wantCodexModel1 {
		t.Fatalf("codex key 1 auth_index = %q, want %q", got1, wantCodexModel1)
	}

	// 23. Meta with [empty key, dca key, valid A, valid B]: empty and dca skipped, A gets A, B gets B
	metaFilteredYAML := `config-version: 8
port: 8317
api-keys:
  meta:
    - name: meta-filtered-group
      keys:
        - api-key: ""
        - api-key: dca:some-oauth-token
        - api-key: sk-meta-real-a
        - api-key: sk-meta-real-b
`
	metaFilteredPath := filepath.Join(dir, "meta-filtered.yaml")
	if errWrite := os.WriteFile(metaFilteredPath, []byte(metaFilteredYAML), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	metaFilteredCfg, errLoadMetaFiltered := config.LoadConfig(metaFilteredPath)
	if errLoadMetaFiltered != nil {
		t.Fatal(errLoadMetaFiltered)
	}
	metaFilteredAuths, errMetaFilteredSynth := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      metaFilteredCfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errMetaFilteredSynth != nil || len(metaFilteredAuths) != 2 {
		t.Fatalf("synthesize meta filtered auths: %v, len=%d", errMetaFilteredSynth, len(metaFilteredAuths))
	}
	wantMetaRealA := metaFilteredAuths[0].EnsureIndex()
	wantMetaRealB := metaFilteredAuths[1].EnsureIndex()
	if wantMetaRealA == "" || wantMetaRealB == "" || wantMetaRealA == wantMetaRealB {
		t.Fatalf("expected distinct indexes for meta real keys: %q vs %q", wantMetaRealA, wantMetaRealB)
	}

	hMetaFiltered := &Handler{cfg: metaFilteredCfg, configFilePath: metaFilteredPath}
	routerMetaFiltered := gin.New()
	routerMetaFiltered.GET("/v8/management/config/*path", hMetaFiltered.ConfigV8)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/meta", nil)
	routerMetaFiltered.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET meta filtered: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var metaFilteredGroups []map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &metaFilteredGroups); errDecode != nil {
		t.Fatal(errDecode)
	}
	metaFilteredKeys := metaFilteredGroups[0]["keys"].([]any)
	if len(metaFilteredKeys) != 4 {
		t.Fatalf("expected 4 keys in response, got %d", len(metaFilteredKeys))
	}
	if gotEmpty := metaFilteredKeys[0].(map[string]any)["auth_index"]; gotEmpty != nil {
		t.Fatalf("empty key auth_index = %v, want nil", gotEmpty)
	}
	if gotDCA := metaFilteredKeys[1].(map[string]any)["auth_index"]; gotDCA != nil {
		t.Fatalf("dca key auth_index = %v, want nil", gotDCA)
	}
	if gotA := metaFilteredKeys[2].(map[string]any)["auth_index"]; gotA != wantMetaRealA {
		t.Fatalf("key A auth_index = %q, want %q", gotA, wantMetaRealA)
	}
	if gotB := metaFilteredKeys[3].(map[string]any)["auth_index"]; gotB != wantMetaRealB {
		t.Fatalf("key B auth_index = %q, want %q", gotB, wantMetaRealB)
	}
}
