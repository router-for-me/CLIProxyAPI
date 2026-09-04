package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	gin "github.com/gin-gonic/gin"
	configaccess "github.com/router-for-me/CLIProxyAPI/v7/internal/access/config_access"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestAuthMiddlewareKeepsRawKeyAndExposesLabel(t *testing.T) {
	tests := []struct {
		name      string
		entries   []sdkaccess.APIKeyEntry
		key       string
		wantLabel string
		hasLabel  bool
	}{
		{
			name:      "named key exposes label",
			entries:   []sdkaccess.APIKeyEntry{{Key: "sk-alice", Name: "alice"}},
			key:       "sk-alice",
			wantLabel: "alice",
			hasLabel:  true,
		},
		{
			name:    "unnamed key sets no label",
			entries: []sdkaccess.APIKeyEntry{{Key: "sk-plain"}},
			key:     "sk-plain",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			configaccess.Register(&proxyconfig.SDKConfig{APIKeys: tt.entries})
			t.Cleanup(func() { configaccess.Register(nil) })

			manager := sdkaccess.NewManager()
			manager.SetProviders(sdkaccess.RegisteredProviders())

			gin.SetMode(gin.TestMode)
			engine := gin.New()
			engine.Use(AuthMiddleware(manager))

			var (
				gotKey      any
				gotLabel    any
				labelExists bool
			)
			engine.GET("/v1/models", func(c *gin.Context) {
				gotKey, _ = c.Get("userApiKey")
				gotLabel, labelExists = c.Get("userApiKeyLabel")
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			req.Header.Set("Authorization", "Bearer "+tt.key)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
			}
			if gotKey != tt.key {
				t.Fatalf("userApiKey = %v, want raw key %q", gotKey, tt.key)
			}
			if labelExists != tt.hasLabel {
				t.Fatalf("userApiKeyLabel present = %v, want %v", labelExists, tt.hasLabel)
			}
			if tt.hasLabel && gotLabel != tt.wantLabel {
				t.Fatalf("userApiKeyLabel = %v, want %q", gotLabel, tt.wantLabel)
			}
		})
	}
}
