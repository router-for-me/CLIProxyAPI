package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestGetMirasimKeysEmpty(t *testing.T) {
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/mirasim-api-key", nil)
	h.GetMirasimKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal GET body: %v", err)
	}
	if _, ok := payload["mirasim-api-key"]; !ok {
		t.Fatalf("GET body missing mirasim-api-key envelope: %s", rec.Body.String())
	}
}

func TestPutMirasimKeysCreateReplaceAndBaseURLRequirement(t *testing.T) {
	configFile := writeTestConfigFile(t)
	h := &Handler{cfg: &config.Config{}, configFilePath: configFile}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/mirasim-api-key",
		strings.NewReader(`[
			{"api-key":"m1","base-url":"https://relay-a.example.com","prefix":"mira","models":[{"name":"claude-opus-4-1-20250805","alias":"mira-opus"}],"cloak":{"mode":"always"},"fingerprint-profile":"claude-code-cli","experimental-cch-signing":true},
			{"api-key":"m2"}
		]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutMirasimKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.MirasimKey) != 1 {
		t.Fatalf("MirasimKey length = %d, want 1 (no-base-url entry dropped): %+v", len(h.cfg.MirasimKey), h.cfg.MirasimKey)
	}
	entry := h.cfg.MirasimKey[0]
	if entry.APIKey != "m1" || entry.BaseURL != "https://relay-a.example.com" || entry.Prefix != "mira" {
		t.Fatalf("entry identity = %+v", entry)
	}
	if entry.Cloak != nil || entry.FingerprintProfile != "" || entry.ExperimentalCCHSigning {
		t.Fatalf("claude-only knobs must be stripped: cloak=%+v fp=%q cch=%v", entry.Cloak, entry.FingerprintProfile, entry.ExperimentalCCHSigning)
	}
	if len(entry.Models) != 1 || entry.Models[0].Name != "claude-opus-4-1-20250805" || entry.Models[0].Alias != "mira-opus" {
		t.Fatalf("models = %+v", entry.Models)
	}

	// Sanity: PUT is still replace semantics for the whole family.
	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/mirasim-api-key",
		strings.NewReader(`[{"api-key":"m3","base-url":"https://relay-b.example.com"}]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutMirasimKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT replace status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.MirasimKey) != 1 || h.cfg.MirasimKey[0].APIKey != "m3" {
		t.Fatalf("after replace: %+v", h.cfg.MirasimKey)
	}
}

func TestPutMirasimKeysRejectsInvalidWeight(t *testing.T) {
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/mirasim-api-key",
		strings.NewReader(`[
			{"api-key":"m1","base-url":"https://relay-a.example.com"},
			{"api-key":"m2","base-url":"https://relay-b.example.com","weight":1000001}
		]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutMirasimKeys(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "mirasim-api-key[1].weight") {
		t.Fatalf("error body = %s, want field index reference", rec.Body.String())
	}
	if len(cfg.MirasimKey) != 0 {
		t.Fatalf("MirasimKey = %+v, want the rejected write to change nothing", cfg.MirasimKey)
	}
}

func TestPatchMirasimKeyUpdatesFieldsAndStripsKnobs(t *testing.T) {
	cfg := &config.Config{
		MirasimKey: []config.MirasimKey{
			{APIKey: "m1", BaseURL: "https://relay-a.example.com"},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/mirasim-api-key",
		strings.NewReader(`{"index":0,"value":{
			"priority": 5,
			"weight": 3,
			"prefix": "mira",
			"proxy-url": "socks5://proxy.example.com:1080",
			"headers": {"X-Custom-Header": "v1"},
			"excluded-models": ["claude-never"],
			"models": [{"name":"claude-sonnet-4-6","alias":"s4-6"}],
			"rebuild-mid-system-message": true,
			"disable-cooling": true,
			"request-retry": 2,
			"request-scoped-errors": [{"status": 429, "match": ["quota"], "action": "stop"}]
		}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchMirasimKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	entry := cfg.MirasimKey[0]
	if entry.Priority != 5 || entry.Weight == nil || *entry.Weight != 3 {
		t.Fatalf("priority/weight = %d/%+v", entry.Priority, entry.Weight)
	}
	if entry.Prefix != "mira" || entry.ProxyURL != "socks5://proxy.example.com:1080" {
		t.Fatalf("prefix/proxy-url = %q/%q", entry.Prefix, entry.ProxyURL)
	}
	if entry.Headers["X-Custom-Header"] != "v1" || len(entry.ExcludedModels) != 1 {
		t.Fatalf("headers/excluded-models = %+v/%+v", entry.Headers, entry.ExcludedModels)
	}
	if len(entry.Models) != 1 || entry.Models[0].Alias != "s4-6" {
		t.Fatalf("models = %+v", entry.Models)
	}
	if !entry.RebuildMidSystemMessage {
		t.Fatal("rebuild-mid-system-message not applied")
	}
	if entry.DisableCooling == nil || !*entry.DisableCooling {
		t.Fatal("disable-cooling not applied")
	}
	if entry.RequestRetry == nil || *entry.RequestRetry != 2 {
		t.Fatal("request-retry not applied")
	}
	if len(entry.RequestScopedErrors) != 1 || entry.RequestScopedErrors[0].Status != 429 {
		t.Fatalf("request-scoped-errors = %+v", entry.RequestScopedErrors)
	}
	if entry.Cloak != nil || entry.FingerprintProfile != "" || entry.ExperimentalCCHSigning {
		t.Fatalf("claude-only knobs leaked: cloak=%+v fp=%q cch=%v", entry.Cloak, entry.FingerprintProfile, entry.ExperimentalCCHSigning)
	}
}

func TestPatchMirasimKeyNotFoundAndDuplicateMatch(t *testing.T) {
	cfg := &config.Config{MirasimKey: []config.MirasimKey{{APIKey: "m1", BaseURL: "https://relay-a.example.com"}}}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/mirasim-api-key",
		strings.NewReader(`{"index":4,"value":{"prefix":"mira"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchMirasimKey(ctx)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/mirasim-api-key",
		strings.NewReader(`{"match":"m1","value":{"prefix":"mira"}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchMirasimKey(ctx)
	if rec.Code != http.StatusOK || cfg.MirasimKey[0].Prefix != "mira" {
		t.Fatalf("match patch: status=%d prefix=%q", rec.Code, cfg.MirasimKey[0].Prefix)
	}
}

func TestPatchMirasimKeyEmptyBaseURLRemovesCredential(t *testing.T) {
	cfg := &config.Config{
		MirasimKey: []config.MirasimKey{
			{APIKey: "m1", BaseURL: "https://relay-a.example.com"},
			{APIKey: "m2", BaseURL: "https://relay-b.example.com"},
		},
	}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/mirasim-api-key",
		strings.NewReader(`{"index":0,"value":{"base-url":""}}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchMirasimKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if len(cfg.MirasimKey) != 1 || cfg.MirasimKey[0].APIKey != "m2" {
		t.Fatalf("MirasimKey after base-url clear = %+v, want only m2 remains", cfg.MirasimKey)
	}
}

func TestDeleteMirasimKeyBehaviors(t *testing.T) {
	newCfg := func() *config.Config {
		return &config.Config{
			MirasimKey: []config.MirasimKey{
				{APIKey: "dup", BaseURL: "https://a.example.com"},
				{APIKey: "dup", BaseURL: "https://b.example.com"},
				{APIKey: "other", BaseURL: "https://c.example.com"},
			},
		}
	}

	t.Run("base-url disambiguates duplicates", func(t *testing.T) {
		cfg := newCfg()
		h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		req := httptest.NewRequest(http.MethodDelete, "/v0/management/mirasim-api-key?api-key=dup&base-url=https%3A%2F%2Fa.example.com", nil)
		ctx.Request = req
		h.DeleteMirasimKey(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
		}
		if len(cfg.MirasimKey) != 2 || cfg.MirasimKey[0].BaseURL != "https://b.example.com" {
			t.Fatalf("MirasimKey = %+v", cfg.MirasimKey)
		}
	})

	t.Run("duplicate api-key without base-url errors", func(t *testing.T) {
		cfg := newCfg()
		h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/mirasim-api-key?api-key=dup", nil)
		h.DeleteMirasimKey(ctx)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "base-url is required") {
			t.Fatalf("status = %d body = %s; want 400 mentioning base-url", rec.Code, rec.Body.String())
		}
	})

	t.Run("index removal", func(t *testing.T) {
		cfg := newCfg()
		h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/mirasim-api-key?index=2", nil)
		h.DeleteMirasimKey(ctx)
		if rec.Code != http.StatusOK || len(cfg.MirasimKey) != 2 || cfg.MirasimKey[1].APIKey != "dup" {
			t.Fatalf("status=%d keys=%+v", rec.Code, cfg.MirasimKey)
		}
	})

	t.Run("missing selector", func(t *testing.T) {
		cfg := newCfg()
		h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/mirasim-api-key", nil)
		h.DeleteMirasimKey(ctx)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
		}
	})
}

func TestPutMirasimKeysPersistsV8GroupedLayout(t *testing.T) {
	dir := t.TempDir()
	configFile := dir + "/config.yaml"
	initialYAML := `config-version: 8
api-keys:
  mirasim:
    - name: ms-1
      base-url: "https://relay-a.example.com"
      keys:
        - api-key: "m1"
`
	if errWrite := os.WriteFile(configFile, []byte(initialYAML), 0o600); errWrite != nil {
		t.Fatalf("os.WriteFile() error = %v", errWrite)
	}

	cfg, errLoad := config.LoadConfig(configFile)
	if errLoad != nil {
		t.Fatalf("LoadConfig() error = %v", errLoad)
	}
	if len(cfg.MirasimKey) != 1 {
		t.Fatalf("initial MirasimKey = %+v", cfg.MirasimKey)
	}
	h := &Handler{cfg: cfg, configFilePath: configFile}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/mirasim-api-key",
		strings.NewReader(`[
			{"api-key":"m1-updated","base-url":"https://relay-a.example.com","models":[{"name":"claude-opus-4-1-20250805","alias":"mira-opus"}]},
			{"api-key":"m2","base-url":"https://relay-b.example.com"}
		]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutMirasimKeys(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d; body=%s", rec.Code, rec.Body.String())
	}

	savedBytes, errRead := os.ReadFile(configFile)
	if errRead != nil {
		t.Fatalf("os.ReadFile() error = %v", errRead)
	}
	savedText := string(savedBytes)
	if !strings.Contains(savedText, "mirasim:") {
		t.Fatalf("saved YAML lost the api-keys.mirasim group:\n%s", savedText)
	}
	if strings.Contains(savedText, "mirasim-api-key:") {
		t.Fatalf("saved YAML leaked legacy mirasim-api-key layout into a v8 file:\n%s", savedText)
	}
	// Changed family content is preserved as regenerated group layout (names
	// synthesized family-N, matching every other family on write).
	if !strings.Contains(savedText, "name: mirasim-1") || !strings.Contains(savedText, "name: mirasim-2") {
		t.Fatalf("saved YAML missing regenerated mirasim groups:\n%s", savedText)
	}
	if !strings.Contains(savedText, "keys:") {
		t.Fatalf("saved YAML groups lost per-group keys:\n%s", savedText)
	}

	reloaded, errReload := config.LoadConfig(configFile)
	if errReload != nil {
		t.Fatalf("LoadConfig(saved) error = %v", errReload)
	}
	if len(reloaded.MirasimKey) != 2 {
		t.Fatalf("reloaded MirasimKey length = %d, want 2: %+v", len(reloaded.MirasimKey), reloaded.MirasimKey)
	}
	found := map[string]bool{}
	for _, k := range reloaded.MirasimKey {
		found[k.APIKey] = true
	}
	if !found["m1-updated"] || !found["m2"] {
		t.Fatalf("reloaded keys = %+v", reloaded.MirasimKey)
	}
	for _, k := range reloaded.MirasimKey {
		if strings.TrimSpace(k.BaseURL) == "" {
			t.Fatalf("sanitized entry lost base-url: %+v", k)
		}
	}
}
