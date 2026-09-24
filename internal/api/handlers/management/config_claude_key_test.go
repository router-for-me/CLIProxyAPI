package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPatchClaudeKeyFingerprintProfile(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "test-claude-key"},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	// Patch fingerprint-profile to claude-code-cli
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"fingerprint-profile":"claude-code-cli"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := cfg.ClaudeKey[0].FingerprintProfile; got != "claude-code-cli" {
		t.Fatalf("FingerprintProfile = %q, want %q", got, "claude-code-cli")
	}

	// Patch fingerprint-profile back to empty
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"fingerprint-profile":""}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := cfg.ClaudeKey[0].FingerprintProfile; got != "" {
		t.Fatalf("FingerprintProfile = %q, want empty", got)
	}

	// A legacy alias is stored in canonical form so the config file and the request
	// path agree on one spelling.
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"fingerprint-profile":"  OAuth-CLI "}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := cfg.ClaudeKey[0].FingerprintProfile; got != "claude-code-cli" {
		t.Fatalf("FingerprintProfile = %q, want canonical %q", got, "claude-code-cli")
	}
}

// A typo must fail the write instead of reaching the request path, where it can
// only be reported as a warning behind every later request.
func TestPatchClaudeKeyRejectsUnknownFingerprintProfile(t *testing.T) {
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "test-claude-key", FingerprintProfile: "claude-code-cli"},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"fingerprint-profile":"claude-code"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fingerprint-profile") {
		t.Fatalf("error body = %s, want it to name the field", rec.Body.String())
	}
	if got := cfg.ClaudeKey[0].FingerprintProfile; got != "claude-code-cli" {
		t.Fatalf("FingerprintProfile = %q, want the rejected patch to leave it unchanged", got)
	}
}

func TestPutClaudeKeysRejectsUnknownFingerprintProfile(t *testing.T) {
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(`[{"api-key":"k1"},{"api-key":"k2","fingerprint-profile":"claude-cli"}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "claude-api-key[1].fingerprint-profile") {
		t.Fatalf("error body = %s, want the offending index", rec.Body.String())
	}
	if len(cfg.ClaudeKey) != 0 {
		t.Fatalf("ClaudeKey = %+v, want the rejected write to change nothing", cfg.ClaudeKey)
	}
}

func TestPatchClaudeKeyCloak(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: test-claude-key
    cloak:
      mode: always
      strict-mode: true
      cache-user-id: true
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "test-claude-key",
				Cloak: &config.CloakConfig{
					Mode:        "always",
					StrictMode:  true,
					CacheUserID: new(bool),
				},
			},
		},
	}
	*cfg.ClaudeKey[0].Cloak.CacheUserID = true
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// 1. Partial patch: only update strict-mode to false. mode: always and cache-user-id: true must be preserved.
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"cloak":{"strict-mode":false}}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if cfg.ClaudeKey[0].Cloak == nil {
		t.Fatal("Cloak is nil, want non-nil")
	}
	if got := cfg.ClaudeKey[0].Cloak.Mode; got != "always" {
		t.Fatalf("Cloak.Mode = %q, want preserved %q", got, "always")
	}
	if got := cfg.ClaudeKey[0].Cloak.StrictMode; got != false {
		t.Fatalf("Cloak.StrictMode = %t, want false", got)
	}
	if cfg.ClaudeKey[0].Cloak.CacheUserID == nil || !*cfg.ClaudeKey[0].Cloak.CacheUserID {
		t.Fatalf("Cloak.CacheUserID = %v, want preserved true", cfg.ClaudeKey[0].Cloak.CacheUserID)
	}

	// Verify disk persistence after partial patch
	loadedCfg, errLoad := config.LoadConfigOptional(configFile, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if loadedCfg.ClaudeKey[0].Cloak.StrictMode != false {
		t.Errorf("persisted StrictMode = true, want false")
	}
	if loadedCfg.ClaudeKey[0].Cloak.Mode != "always" {
		t.Errorf("persisted Mode = %q, want %q", loadedCfg.ClaudeKey[0].Cloak.Mode, "always")
	}

	// 2. Partial patch: update cache-user-id to false
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"cloak":{"cache-user-id":false}}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if cfg.ClaudeKey[0].Cloak.CacheUserID == nil || *cfg.ClaudeKey[0].Cloak.CacheUserID != false {
		t.Fatalf("Cloak.CacheUserID = %v, want false", cfg.ClaudeKey[0].Cloak.CacheUserID)
	}

	// Verify disk persistence
	loadedCfg, errLoad = config.LoadConfigOptional(configFile, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if loadedCfg.ClaudeKey[0].Cloak.CacheUserID == nil || *loadedCfg.ClaudeKey[0].Cloak.CacheUserID != false {
		t.Errorf("persisted CacheUserID = %v, want false", loadedCfg.ClaudeKey[0].Cloak.CacheUserID)
	}

	// 3. Rejects invalid cloak JSON
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"cloak":"invalid-string"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for invalid cloak JSON", rec.Code)
	}

	// 4. Patch cloak to null (clear cloak)
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":0,"value":{"cloak":null}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if cfg.ClaudeKey[0].Cloak != nil {
		t.Fatalf("Cloak = %+v, want nil after clearing", cfg.ClaudeKey[0].Cloak)
	}

	// Verify disk persistence after null
	loadedCfg, errLoad = config.LoadConfigOptional(configFile, false)
	if errLoad != nil {
		t.Fatalf("LoadConfigOptional() error = %v", errLoad)
	}
	if loadedCfg.ClaudeKey[0].Cloak != nil {
		t.Errorf("persisted Cloak = %+v, want nil", loadedCfg.ClaudeKey[0].Cloak)
	}
}

func TestPatchClaudeKeyDoesNotInheritModeWhenIdentityChanged(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: key-a
    base-url: https://a.example.com
    prefix: teamA
    proxy-url: http://127.0.0.1:8080
    cloak:
      mode: always
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	testCases := []struct {
		name  string
		patch string
	}{
		{name: "api-key changed", patch: `{"index":0,"value":{"api-key":"key-b","cloak":{"mode":""}}}`},
		{name: "base-url changed", patch: `{"index":0,"value":{"base-url":"https://b.example.com","cloak":{"mode":""}}}`},
		{name: "prefix changed", patch: `{"index":0,"value":{"prefix":"teamB","cloak":{"mode":""}}}`},
		{name: "proxy-url changed", patch: `{"index":0,"value":{"proxy-url":"http://127.0.0.1:9090","cloak":{"mode":""}}}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				ClaudeKey: []config.ClaudeKey{
					{
						APIKey:   "key-a",
						BaseURL:  "https://a.example.com",
						Prefix:   "teamA",
						ProxyURL: "http://127.0.0.1:8080",
						Cloak:    &config.CloakConfig{Mode: "always"},
					},
				},
			}
			h := &Handler{cfg: cfg, configFilePath: configFile}

			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key", strings.NewReader(tc.patch))
			ctx.Request.Header.Set("Content-Type", "application/json")
			h.PatchClaudeKey(ctx)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if cfg.ClaudeKey[0].Cloak != nil && cfg.ClaudeKey[0].Cloak.Mode != "" {
				t.Fatalf("Cloak.Mode = %q, want empty (must not inherit mode across identity changes)", cfg.ClaudeKey[0].Cloak.Mode)
			}
		})
	}
}

func TestPutClaudeKeysCloakPersistence(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: sk-ant-test
    cloak:
      mode: always
      strict-mode: true
      cache-user-id: true
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "sk-ant-test",
				Cloak: &config.CloakConfig{
					Mode:       "always",
					StrictMode: true,
				},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// PUT with updated cloak where mode is empty (preserves original mode), strict-mode is false, cache-user-id is false
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(`[{"api-key":"sk-ant-test","cloak":{"mode":"","strict-mode":false,"cache-user-id":false}}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	savedBytes, errRead := os.ReadFile(configFile)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)
	// Empty mode preserves previous mode when left blank
	if !strings.Contains(savedText, "mode: always") {
		t.Fatalf("saved YAML lost preserved 'mode: always':\n%s", savedText)
	}
	// Switches: on is on, off is off (strict-mode: true is gone, cache-user-id: false is present)
	if strings.Contains(savedText, "strict-mode: true") {
		t.Fatalf("saved YAML still contains 'strict-mode: true':\n%s", savedText)
	}
	if strings.Contains(savedText, "cache-user-id: true") {
		t.Fatalf("saved YAML still contains 'cache-user-id: true':\n%s", savedText)
	}
	if !strings.Contains(savedText, "cache-user-id: false") {
		t.Fatalf("saved YAML missing 'cache-user-id: false':\n%s", savedText)
	}

	// Verify GET returns preserved mode and updated false values so the frontend continues to display the original value
	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-api-key", nil)
	h.GetClaudeKeys(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getRec.Code)
	}
	bodyStr := getRec.Body.String()
	if !strings.Contains(bodyStr, `"mode":"always"`) {
		t.Fatalf("GET response missing preserved mode: always; got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, `"cache-user-id":false`) {
		t.Fatalf("GET response missing cache-user-id: false; got: %s", bodyStr)
	}
}

func TestPutClaudeKeysPreservesModeByCredentialIdentity(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: key-a
    cloak:
      mode: always
  - api-key: key-b
    cloak:
      mode: never
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "key-a",
				Cloak:  &config.CloakConfig{Mode: "always"},
			},
			{
				APIKey: "key-b",
				Cloak:  &config.CloakConfig{Mode: "never"},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// Submit PUT where key-a is removed, and key-b is sent with empty mode.
	// key-b must retain its own "never" mode, not inherit key-a's "always".
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(`[{"api-key":"key-b","cloak":{"mode":""}}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(cfg.ClaudeKey) != 1 {
		t.Fatalf("ClaudeKey length = %d, want 1", len(cfg.ClaudeKey))
	}
	if got := cfg.ClaudeKey[0].Cloak.Mode; got != "never" {
		t.Fatalf("key-b Cloak.Mode = %q, want its own preserved %q", got, "never")
	}

	savedBytes, errRead := os.ReadFile(configFile)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)
	if !strings.Contains(savedText, "mode: never") {
		t.Fatalf("saved YAML missing 'mode: never':\n%s", savedText)
	}
	if strings.Contains(savedText, "mode: always") {
		t.Fatalf("saved YAML unexpectedly contains 'mode: always':\n%s", savedText)
	}
}

func TestPutClaudeKeysRealFrontendPayload(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: sk-ant-test
    cloak:
      mode: always
      strict-mode: true
      cache-user-id: true
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cacheTrue := true
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "sk-ant-test",
				Cloak: &config.CloakConfig{
					Mode:        "always",
					StrictMode:  true,
					CacheUserID: &cacheTrue,
				},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// Real frontend PUT request when user clears mode and unchecks cache-user-id:
	// mode is omitted, cache-user-id is omitted, strict-mode is false.
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(`[{"api-key":"sk-ant-test","cloak":{"strict-mode":false}}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	savedBytes, errRead := os.ReadFile(configFile)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)
	// Mode is preserved from previous config
	if !strings.Contains(savedText, "mode: always") {
		t.Fatalf("saved YAML lost preserved 'mode: always':\n%s", savedText)
	}
	// Disabled switches: strict-mode: true and cache-user-id: true must be pruned
	if strings.Contains(savedText, "strict-mode: true") {
		t.Fatalf("saved YAML still contains 'strict-mode: true':\n%s", savedText)
	}
	if strings.Contains(savedText, "cache-user-id: true") {
		t.Fatalf("saved YAML still contains 'cache-user-id: true':\n%s", savedText)
	}

	// Verify GET response
	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-api-key", nil)
	h.GetClaudeKeys(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", getRec.Code)
	}
	bodyStr := getRec.Body.String()
	if !strings.Contains(bodyStr, `"mode":"always"`) {
		t.Fatalf("GET response missing preserved mode: always; got: %s", bodyStr)
	}
	if strings.Contains(bodyStr, `"strict-mode":true`) {
		t.Fatalf("GET response still has strict-mode: true; got: %s", bodyStr)
	}
}

func TestPutClaudeKeysPreservesModeWithDifferentPrefix(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: shared-key
    prefix: team1
    cloak:
      mode: always
  - api-key: shared-key
    prefix: team2
    cloak:
      mode: never
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "shared-key",
				Prefix: "team1",
				Cloak:  &config.CloakConfig{Mode: "always"},
			},
			{
				APIKey: "shared-key",
				Prefix: "team2",
				Cloak:  &config.CloakConfig{Mode: "never"},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// Update team2 with empty mode. It must retain its own "never" mode and not team1's "always".
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(`[{"api-key":"shared-key","prefix":"team1","cloak":{"mode":"always"}},{"api-key":"shared-key","prefix":"team2","cloak":{"mode":""}}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(cfg.ClaudeKey) != 2 {
		t.Fatalf("ClaudeKey length = %d, want 2", len(cfg.ClaudeKey))
	}
	if got := cfg.ClaudeKey[1].Cloak.Mode; got != "never" {
		t.Fatalf("team2 Cloak.Mode = %q, want its own preserved %q", got, "never")
	}
}

func TestPutClaudeKeysAuthIndexCannotBypassStrictTupleIsolation(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: key-a
    cloak:
      mode: always
  - api-key: key-b
    cloak:
      mode: never
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "key-a",
				Cloak:  &config.CloakConfig{Mode: "always"},
			},
			{
				APIKey: "key-b",
				Cloak:  &config.CloakConfig{Mode: "never"},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// PUT sends key-b with key-a's auth-index and empty mode.
	// Strict tuple isolation must ensure key-b preserves its own mode ("never") and does not inherit key-a's mode.
	payload := `[{"api-key":"key-a","cloak":{"mode":"always"}},{"api-key":"key-b","auth-index":"index-a","cloak":{"mode":""}}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(cfg.ClaudeKey) != 2 {
		t.Fatalf("ClaudeKey length = %d, want 2", len(cfg.ClaudeKey))
	}
	if got := cfg.ClaudeKey[1].Cloak.Mode; got != "never" {
		t.Fatalf("key-b Cloak.Mode = %q, want its own preserved %q", got, "never")
	}
}

func TestPutClaudeKeysDoesNotInheritModeForNewCredential(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: old-key
    base-url: https://old.example.com
    cloak:
      mode: never
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey:  "old-key",
				BaseURL: "https://old.example.com",
				Cloak:   &config.CloakConfig{Mode: "never"},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// PUT replaces old-key with completely new-key (without auth-index) and empty mode.
	// new-key must NOT inherit old-key's "never" mode; its mode must remain empty.
	payload := `[{"api-key":"new-key","base-url":"https://new.example.com","cloak":{"mode":""}}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(cfg.ClaudeKey) != 1 {
		t.Fatalf("ClaudeKey length = %d, want 1", len(cfg.ClaudeKey))
	}
	if got := cfg.ClaudeKey[0].Cloak.Mode; got != "" {
		t.Fatalf("new credential Cloak.Mode = %q, want empty (must not inherit from old deleted credential)", got)
	}

	savedBytes, errRead := os.ReadFile(configFile)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)
	if strings.Contains(savedText, "mode: never") {
		t.Fatalf("saved YAML unexpectedly retained 'mode: never':\n%s", savedText)
	}
}

func TestPutClaudeKeysAmbiguousCredentialsDoNotInheritMode(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: shared-key
    cloak:
      mode: always
  - api-key: shared-key
    cloak:
      mode: never
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "shared-key",
				Cloak:  &config.CloakConfig{Mode: "always"},
			},
			{
				APIKey: "shared-key",
				Cloak:  &config.CloakConfig{Mode: "never"},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// PUT sends one shared-key with empty mode and no auth-index.
	// Since matching is ambiguous between the two duplicates, it must not guess or inherit.
	payload := `[{"api-key":"shared-key","cloak":{"mode":""}}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(cfg.ClaudeKey) != 1 {
		t.Fatalf("ClaudeKey length = %d, want 1", len(cfg.ClaudeKey))
	}
	if got := cfg.ClaudeKey[0].Cloak.Mode; got != "" {
		t.Fatalf("ambiguous credential Cloak.Mode = %q, want empty", got)
	}
}

func TestPutClaudeKeysNewCredentialWithDifferentPrefixDoesNotInheritMode(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: shared-key
    prefix: team1
    cloak:
      mode: never
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "shared-key",
				Prefix: "team1",
				Cloak:  &config.CloakConfig{Mode: "never"},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// PUT retains team1 and adds a second credential for team2 with empty mode without auth-index.
	// team1 must retain its "never" mode, and newly added team2 must NOT inherit team1's "never" mode.
	payload := `[{"api-key":"shared-key","prefix":"team1","cloak":{"mode":""}},{"api-key":"shared-key","prefix":"team2","cloak":{"mode":""}}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(cfg.ClaudeKey) != 2 {
		t.Fatalf("ClaudeKey length = %d, want 2", len(cfg.ClaudeKey))
	}
	if got := cfg.ClaudeKey[0].Cloak.Mode; got != "never" {
		t.Fatalf("team1 Cloak.Mode = %q, want preserved %q", got, "never")
	}
	if got := cfg.ClaudeKey[1].Cloak.Mode; got != "" {
		t.Fatalf("team2 Cloak.Mode = %q, want empty (new credential must not inherit mode from team1)", got)
	}
}

func TestPutClaudeKeysOmittedCloakPreservesExistingMode(t *testing.T) {
	configFile := writeTestConfigFile(t)
	initialYAML := `claude-api-key:
  - api-key: sk-ant-test
    cloak:
      mode: always
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{
				APIKey: "sk-ant-test",
				Cloak:  &config.CloakConfig{Mode: "always"},
			},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	// PUT sends the credential without a cloak block. Existing mode: always must be preserved.
	payload := `[{"api-key":"sk-ant-test"}]`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key",
		strings.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutClaudeKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(cfg.ClaudeKey) != 1 {
		t.Fatalf("ClaudeKey length = %d, want 1", len(cfg.ClaudeKey))
	}
	if cfg.ClaudeKey[0].Cloak == nil || cfg.ClaudeKey[0].Cloak.Mode != "always" {
		t.Fatalf("Cloak = %+v, want preserved mode: always", cfg.ClaudeKey[0].Cloak)
	}

	savedBytes, errRead := os.ReadFile(configFile)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)
	if !strings.Contains(savedText, "mode: always") {
		t.Fatalf("saved YAML lost preserved 'mode: always':\n%s", savedText)
	}
}

func TestPatchClaudeKeyPriority(t *testing.T) {
	configFile := writeTestConfigFile(t)
	cfg := &config.Config{
		ClaudeKey: []config.ClaudeKey{
			{APIKey: "key-0", Priority: 0},
			{APIKey: "key-1", Priority: 5},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":1,"value":{"priority":20}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := cfg.ClaudeKey[1].Priority; got != 20 {
		t.Fatalf("ClaudeKey[1].Priority = %d, want 20", got)
	}

	savedBytes, errRead := os.ReadFile(configFile)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)
	if !strings.Contains(savedText, "priority: 20") {
		t.Fatalf("saved YAML missing 'priority: 20':\n%s", savedText)
	}

	// Verify GET returns the updated priority with structured unmarshaling
	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/claude-api-key", nil)
	h.GetClaudeKeys(getCtx)

	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	var resp struct {
		ClaudeKey []config.ClaudeKey `json:"claude-api-key"`
	}
	if errJSON := json.Unmarshal(getRec.Body.Bytes(), &resp); errJSON != nil {
		t.Fatalf("unmarshal GET response: %v", errJSON)
	}
	if len(resp.ClaudeKey) != 2 {
		t.Fatalf("GET response ClaudeKey length = %d, want 2", len(resp.ClaudeKey))
	}
	if resp.ClaudeKey[1].Priority != 20 {
		t.Fatalf("GET response ClaudeKey[1].Priority = %d, want 20", resp.ClaudeKey[1].Priority)
	}

	// Omitting priority must preserve existing non-zero value (20)
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":1,"value":{"prefix":"team-test"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := cfg.ClaudeKey[1].Priority; got != 20 {
		t.Fatalf("ClaudeKey[1].Priority = %d, want preserved 20", got)
	}
	if got := cfg.ClaudeKey[1].Prefix; got != "team-test" {
		t.Fatalf("ClaudeKey[1].Prefix = %q, want %q", got, "team-test")
	}

	// Reset priority to 0 explicitly
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":1,"value":{"priority":0}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := cfg.ClaudeKey[1].Priority; got != 0 {
		t.Fatalf("ClaudeKey[1].Priority = %d, want 0", got)
	}

	// Invalid priority type returns 400
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key",
		strings.NewReader(`{"index":1,"value":{"priority":"invalid"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchClaudeKey(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}
