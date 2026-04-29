package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestFilterModelsForAPIKey_RestrictedKeyOnlySeesAllowedModels(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{SDKConfig: config.SDKConfig{
		APIKeys: []string{"sk-narrow"},
		APIKeyPolicies: []config.APIKeyPolicy{
			{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*", "claude-3-*"}},
		},
	}}
	models := []map[string]any{
		{"id": "gpt-4o-mini"},
		{"id": "claude-3-5-sonnet-20241022"},
		{"id": "gemini-2.0-flash"},
	}

	got := filterModelsForAPIKey(cfg, "sk-narrow", models)
	if len(got) != 2 {
		t.Fatalf("filtered model count = %d, want 2 (%#v)", len(got), got)
	}
	if got[0]["id"] != "gpt-4o-mini" || got[1]["id"] != "claude-3-5-sonnet-20241022" {
		t.Fatalf("filtered models = %#v", got)
	}
}

func TestFilterModelsForAPIKey_UnrestrictedKeySeesAllModels(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{SDKConfig: config.SDKConfig{
		APIKeys: []string{"sk-open", "sk-other"},
		APIKeyPolicies: []config.APIKeyPolicy{
			{Key: "sk-other", AllowedModels: []string{"gpt-4o*"}},
		},
	}}
	models := []map[string]any{{"id": "gpt-4o-mini"}, {"id": "gemini-2.0-flash"}}

	got := filterModelsForAPIKey(cfg, "sk-open", models)
	if len(got) != len(models) {
		t.Fatalf("unrestricted key saw %d models, want %d", len(got), len(models))
	}
}

func TestFilterModelsForAPIKey_DenyAllUnknownKeySeesNoModels(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{SDKConfig: config.SDKConfig{
		APIKeyDefaultPolicy: config.APIKeyDefaultPolicyDenyAll,
	}}
	models := []map[string]any{{"id": "gpt-4o-mini"}, {"id": "gemini-2.0-flash"}}

	got := filterModelsForAPIKey(cfg, "sk-unknown", models)
	if len(got) != 0 {
		t.Fatalf("deny-all unknown key saw %#v, want empty list", got)
	}
}

func TestUnifiedModelsHandler_FiltersOpenAIModelList(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{SDKConfig: config.SDKConfig{
		APIKeys: []string{"sk-narrow"},
		APIKeyPolicies: []config.APIKeyPolicy{
			{Key: "sk-narrow", AllowedModels: []string{"gpt-4o*"}},
		},
	}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("apiKey", "sk-narrow")
		c.Next()
	})
	router.GET("/v1/models", func(c *gin.Context) {
		models := filterModelsForAPIKey(cfg, apiKeyFromContext(c), []map[string]any{
			{"id": "gpt-4o-mini", "object": "model"},
			{"id": "gemini-2.0-flash", "object": "model"},
		})
		c.JSON(http.StatusOK, gin.H{"object": "list", "data": models})
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, w.Body.String())
	}
	if len(resp.Data) != 1 || resp.Data[0]["id"] != "gpt-4o-mini" {
		t.Fatalf("filtered /v1/models response = %#v", resp.Data)
	}
}
