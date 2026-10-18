package gemini

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// A model outside the client key's scope is answered like an unknown model by
// the Gemini model detail route, as it is by the model lists.
func TestGeminiModelDetailAppliesKeyScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const geminiModel, claudeModel = "key-scope-detail-gemini", "key-scope-detail-claude"
	registryRef := registry.GetGlobalRegistry()
	registryRef.RegisterClient("key-scope-detail-gemini-client", "gemini", []*registry.ModelInfo{{ID: geminiModel, Name: geminiModel}})
	registryRef.RegisterClient("key-scope-detail-claude-client", "claude", []*registry.ModelInfo{{ID: claudeModel, Name: claudeModel}})
	t.Cleanup(func() {
		registryRef.UnregisterClient("key-scope-detail-gemini-client")
		registryRef.UnregisterClient("key-scope-detail-claude-client")
	})
	cfg := &sdkconfig.SDKConfig{}
	cfg.Client.KeyScopes = &[]config.ClientKeyScope{{KeyPrefix: "sk-gemini-", Providers: []string{"gemini"}}}
	handler := NewGeminiAPIHandler(handlers.NewBaseAPIHandlers(cfg, nil))

	detail := func(apiKey, model string) int {
		router := gin.New()
		router.GET("/v1beta/models/*action", func(c *gin.Context) {
			c.Set("userApiKey", apiKey)
			handler.GeminiGetHandler(c)
		})
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1beta/models/"+model, nil))
		return recorder.Code
	}
	for _, tc := range []struct {
		apiKey, model string
		want          int
	}{
		{"sk-gemini-key", geminiModel, http.StatusOK},
		{"sk-gemini-key", claudeModel, http.StatusNotFound},
		{"unscoped-key", claudeModel, http.StatusOK},
	} {
		if got := detail(tc.apiKey, tc.model); got != tc.want {
			t.Errorf("%s asking for %s: status=%d want=%d", tc.apiKey, tc.model, got, tc.want)
		}
	}
}
