package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers/openai"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type catalogTestAuth struct{}

func (catalogTestAuth) Identifier() string { return "catalog-test" }
func (catalogTestAuth) Authenticate(_ context.Context, r *http.Request) (*sdkaccess.Result, *sdkaccess.AuthError) {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	result := &sdkaccess.Result{Principal: key, Provider: "catalog-test"}
	var policy string
	switch key {
	case "native":
		return result, nil
	case "empty":
		policy = `[]`
	case "allowed":
		policy = `["catalog-visible"]`
	case "invalid-policy":
		policy = `null`
	default:
		return nil, sdkaccess.NewInvalidCredentialError()
	}
	result.Metadata = map[string]string{pluginapi.ModelListAllowedIDsMetadataKey: policy}
	return result, nil
}

func TestAuthenticatedModelListPolicyRoutes(t *testing.T) {
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("catalog-policy-client", "openai", []*registry.ModelInfo{
		{ID: "catalog-visible", Object: "model", OwnedBy: "test"},
		{ID: "catalog-hidden", Object: "model", OwnedBy: "test"},
	})
	t.Cleanup(func() { registryRef.UnregisterClient("catalog-policy-client") })
	manager := sdkaccess.NewManager()
	manager.SetProviders([]sdkaccess.Provider{catalogTestAuth{}})
	base := &handlers.BaseAPIHandler{}
	server := &Server{}
	router := gin.New()
	router.Use(AuthMiddleware(manager))
	router.GET("/v1/models", server.unifiedModelsHandler(openai.NewOpenAIAPIHandler(base), claude.NewClaudeCodeAPIHandler(base)))
	router.GET("/v1beta/models", gemini.NewGeminiAPIHandler(base).GeminiModels)
	for _, route := range []struct{ name, path, ua string }{
		{"openai", "/v1/models", ""},
		{"claude", "/v1/models", "claude-cli"},
		{"codex", "/v1/models?client_version=0.100.0", ""},
		{"grok", "/v1/models", "grok-shell/0.2"},
		{"gemini", "/v1beta/models", ""},
	} {
		t.Run(route.name, func(t *testing.T) {
			for _, key := range []string{"empty", "invalid-policy", "bad-key"} {
				r := httptest.NewRequest("GET", route.path, nil)
				r.Header.Set("Authorization", "Bearer "+key)
				r.Header.Set("User-Agent", route.ua)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, r)
				wantStatus := 200
				if key == "invalid-policy" {
					wantStatus = 500
				}
				if key == "bad-key" {
					wantStatus = 401
				}
				if w.Code != wantStatus {
					t.Fatalf("%s: %d %s", key, w.Code, w.Body)
				}
				if key == "empty" {
					var response map[string]json.RawMessage
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						t.Fatal(err)
					}
					rows, exists := response["data"]
					if !exists {
						rows = response["models"]
					}
					if string(rows) != "[]" {
						t.Fatalf("unfiltered %s: %s", route.name, w.Body)
					}
				}
			}
		})
	}
	for _, key := range []string{"allowed", "native"} {
		r := httptest.NewRequest("GET", "/v1/models?model_list_allowed_ids=[]", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		r.Header.Set("model_list_allowed_ids", "[]")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), "catalog-visible") {
			t.Fatalf("%s: %d %s", key, w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "catalog-hidden") != (key == "native") {
			t.Fatalf("wrong catalog for %s: %s", key, w.Body)
		}
	}
}
