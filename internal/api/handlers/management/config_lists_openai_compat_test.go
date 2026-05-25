package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPutOpenAICompat_NormalizesKind(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", strings.NewReader(`[
		{"name":"demo","kind":" NewAPI ","base-url":"https://compat.example.com","api-key-entries":[{"api-key":"sk-demo"}]}
	]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.OpenAICompatibility); got != 1 {
		t.Fatalf("openai compatibility len = %d, want 1", got)
	}
	if got := h.cfg.OpenAICompatibility[0].Kind; got != "newapi" {
		t.Fatalf("kind = %q, want %q", got, "newapi")
	}
}

func TestPutClaudeKeys_RemovesOmittedKeysByDefault(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "sk-a", BaseURL: "https://a.example.com"},
				{APIKey: "sk-b", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", strings.NewReader(`[
		{"api-key":"sk-a","base-url":"https://a.example.com","disabled":true}
	]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutClaudeKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude-api-key len = %d, want 1", got)
	}
	if !h.cfg.ClaudeKey[0].Disabled {
		t.Fatal("incoming key update should be applied")
	}
	if got := h.cfg.ClaudeKey[0].APIKey; got != "sk-a" {
		t.Fatalf("remaining key = %q, want sk-a", got)
	}
}

func TestPutClaudeKeys_PreservesServerOnlyFieldsByDefault(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{{
				APIKey:       "sk-a",
				BaseURL:      "https://a.example.com",
				RoutingGroup: "rg-a",
				Disabled:     true,
			}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key", strings.NewReader(`[
		{"api-key":"sk-a","base-url":"https://a.example.com","models":[{"name":"claude-sonnet-4","alias":"sonnet"}]}
	]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutClaudeKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.ClaudeKey[0].RoutingGroup; got != "rg-a" {
		t.Fatalf("routing-group = %q, want rg-a", got)
	}
	if !h.cfg.ClaudeKey[0].Disabled {
		t.Fatal("disabled state from config-backed auth status should be preserved")
	}
	if got := len(h.cfg.ClaudeKey[0].Models); got != 1 {
		t.Fatalf("models len = %d, want 1", got)
	}
}

func TestMergeClaudeKeysPreservingMissingKeepsDuplicateIdentities(t *testing.T) {
	t.Parallel()

	existing := []config.ClaudeKey{
		{APIKey: "sk-same", BaseURL: "https://a.example.com", ProxyURL: "http://proxy.example.com", Prefix: "first"},
		{APIKey: "sk-same", BaseURL: "https://a.example.com", ProxyURL: "http://proxy.example.com", Prefix: "second"},
	}
	incoming := []config.ClaudeKey{
		{APIKey: "sk-same", BaseURL: "https://a.example.com", ProxyURL: "http://proxy.example.com", Prefix: "updated"},
	}

	merged := mergeClaudeKeysPreservingMissing(existing, incoming)

	if got := len(merged); got != 2 {
		t.Fatalf("merged len = %d, want 2", got)
	}
	if got := merged[0].Prefix; got != "updated" {
		t.Fatalf("incoming duplicate prefix = %q, want updated", got)
	}
	if got := merged[1].Prefix; got != "second" {
		t.Fatalf("preserved duplicate prefix = %q, want second", got)
	}
}

func TestPutClaudeKeys_ReplaceQueryAllowsRemoval(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "sk-a", BaseURL: "https://a.example.com"},
				{APIKey: "sk-b", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/claude-api-key?replace=true", strings.NewReader(`[
		{"api-key":"sk-a","base-url":"https://a.example.com"}
	]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutClaudeKeys(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude-api-key len = %d, want 1", got)
	}
	if got := h.cfg.ClaudeKey[0].APIKey; got != "sk-a" {
		t.Fatalf("remaining key = %q, want sk-a", got)
	}
}

func TestPatchClaudeKey_EmptyAPIKeyDeletesEntry(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "sk-a", BaseURL: "https://a.example.com", RoutingGroup: "rg-a"},
				{APIKey: "sk-b", BaseURL: "https://b.example.com", RoutingGroup: "rg-b"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key", strings.NewReader(`{
		"index":0,
		"value":{"api-key":""}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchClaudeKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := len(h.cfg.ClaudeKey); got != 1 {
		t.Fatalf("claude-api-key len = %d, want 1", got)
	}
	if got := h.cfg.ClaudeKey[0].APIKey; got != "sk-b" {
		t.Fatalf("remaining key = %q, want sk-b", got)
	}
}

func TestPatchClaudeKey_UpdatesRoutingFields(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{{
				APIKey:       "sk-a",
				BaseURL:      "https://a.example.com",
				RoutingGroup: "old",
			}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/claude-api-key", strings.NewReader(`{
		"index":0,
		"value":{"routing-group":" new-group ","priority":7,"disabled":true}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchClaudeKey(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.ClaudeKey[0]
	if entry.RoutingGroup != "new-group" {
		t.Fatalf("routing-group = %q, want new-group", entry.RoutingGroup)
	}
	if entry.Priority != 7 {
		t.Fatalf("priority = %d, want 7", entry.Priority)
	}
	if !entry.Disabled {
		t.Fatal("disabled = false, want true")
	}
}

func TestPutOpenAICompat_PreservesProviderFieldsByDefault(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:         "demo",
				Kind:         "newapi",
				Disabled:     true,
				RoutingGroup: "compat-rg",
				BaseURL:      "https://compat.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
					APIKey:       "sk-a",
					RoutingGroup: "key-rg",
				}},
			}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", strings.NewReader(`[
		{"name":"demo","base-url":"https://compat.example.com","api-key-entries":[{"api-key":"sk-a"}],"models":[{"name":"demo-model","alias":"demo"}]}
	]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	entry := h.cfg.OpenAICompatibility[0]
	if got := entry.Kind; got != "newapi" {
		t.Fatalf("kind = %q, want newapi", got)
	}
	if got := entry.RoutingGroup; got != "compat-rg" {
		t.Fatalf("routing-group = %q, want compat-rg", got)
	}
	if !entry.Disabled {
		t.Fatal("provider disabled state should be preserved")
	}
	if got := entry.APIKeyEntries[0].RoutingGroup; got != "key-rg" {
		t.Fatalf("key routing-group = %q, want key-rg", got)
	}
	if got := len(entry.Models); got != 1 {
		t.Fatalf("models len = %d, want 1", got)
	}
}

func TestPutOpenAICompat_RemovesOmittedAPIKeyEntriesByDefault(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "demo",
				BaseURL: "https://compat.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-a", RoutingGroup: "key-rg"},
					{APIKey: "sk-b"},
				},
			}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", strings.NewReader(`[
		{"name":"demo","base-url":"https://compat.example.com","api-key-entries":[{"api-key":"sk-a"}]}
	]`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	keys := h.cfg.OpenAICompatibility[0].APIKeyEntries
	if got := len(keys); got != 1 {
		t.Fatalf("api-key-entries len = %d, want 1", got)
	}
	if got := keys[0].APIKey; got != "sk-a" {
		t.Fatalf("remaining key = %q, want sk-a", got)
	}
	if got := keys[0].RoutingGroup; got != "key-rg" {
		t.Fatalf("key routing-group = %q, want key-rg", got)
	}
}

func TestPatchOpenAICompat_MergesAPIKeyEntriesByDefault(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "demo",
				BaseURL: "https://compat.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-a"},
					{APIKey: "sk-b"},
				},
			}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{
		"name":"demo",
		"value":{"api-key-entries":[{"api-key":"sk-a","disabled":true}]}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	keys := h.cfg.OpenAICompatibility[0].APIKeyEntries
	if got := len(keys); got != 2 {
		t.Fatalf("api-key-entries len = %d, want 2", got)
	}
	if !keys[0].Disabled {
		t.Fatal("incoming key update should be applied")
	}
	if got := keys[1].APIKey; got != "sk-b" {
		t.Fatalf("omitted key = %q, want sk-b", got)
	}
}

func TestPatchOpenAICompat_ReplacesMultipleAPIKeyEntriesByDefault(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "demo",
				BaseURL: "https://compat.example.com",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "sk-a", RoutingGroup: "key-rg"},
					{APIKey: "sk-b"},
					{APIKey: "sk-c"},
				},
			}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{
		"name":"demo",
		"value":{"api-key-entries":[{"api-key":"sk-a"},{"api-key":"sk-c"}]}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	keys := h.cfg.OpenAICompatibility[0].APIKeyEntries
	if got := len(keys); got != 2 {
		t.Fatalf("api-key-entries len = %d, want 2", got)
	}
	if got := keys[0].APIKey; got != "sk-a" {
		t.Fatalf("first key = %q, want sk-a", got)
	}
	if got := keys[0].RoutingGroup; got != "key-rg" {
		t.Fatalf("key routing-group = %q, want key-rg", got)
	}
	if got := keys[1].APIKey; got != "sk-c" {
		t.Fatalf("second key = %q, want sk-c", got)
	}
}

func TestPatchOpenAICompat_RejectsAmbiguousName(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{Name: "demo", BaseURL: "https://a.example.com"},
				{Name: "demo", BaseURL: "https://b.example.com"},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{
		"name":"demo",
		"value":{"disabled":true}
	}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchOpenAICompat(c)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if h.cfg.OpenAICompatibility[0].Disabled || h.cfg.OpenAICompatibility[1].Disabled {
		t.Fatal("ambiguous patch should not update either matching channel")
	}
}

func TestGetOpenAICompat_IncludesPersistedFields(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:         "demo",
				Kind:         "newapi",
				RoutingGroup: "compat-group",
				BaseURL:      "https://compat.example.com",
			}},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)

	h.GetOpenAICompat(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body struct {
		OpenAICompatibility []struct {
			Kind         string `json:"kind"`
			RoutingGroup string `json:"routing-group"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got := body.OpenAICompatibility[0].Kind; got != "newapi" {
		t.Fatalf("kind = %q, want newapi", got)
	}
	if got := body.OpenAICompatibility[0].RoutingGroup; got != "compat-group" {
		t.Fatalf("routing-group = %q, want compat-group", got)
	}
}
