package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestOriginModelsEndpointMiddlewareDefaultsToDeny(t *testing.T) {
	tests := []struct {
		name     string
		allow    bool
		provider string
		wantCode int
	}{
		{name: "native key denied by default", provider: access.DefaultAccessProviderName, wantCode: http.StatusUnauthorized},
		{name: "native key explicitly allowed", allow: true, provider: access.DefaultAccessProviderName, wantCode: http.StatusOK},
		{name: "plugin provider keeps plugin policy", provider: "plugin:cpa-key-policy:cpa-key-policy", wantCode: http.StatusOK},
		{name: "no access provider keeps unauthenticated legacy behavior", wantCode: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				if tt.provider != "" {
					c.Set("accessProvider", tt.provider)
				}
				c.Next()
			})
			server := &Server{cfg: &config.Config{SDKConfig: config.SDKConfig{AllowOriginModelsEndpoint: tt.allow}}}
			engine.GET("/v1/models", server.originModelsEndpointMiddleware(), func(c *gin.Context) {
				c.JSON(http.StatusOK, gin.H{"object": "list"})
			})

			req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, req)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d; body = %s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
		})
	}
}
