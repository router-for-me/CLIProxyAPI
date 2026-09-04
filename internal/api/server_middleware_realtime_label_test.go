package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gin "github.com/gin-gonic/gin"
	codexlive "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/live"
)

// mintRealtimeClientSecret issues an ephemeral realtime secret for the given issuer identity.
func mintRealtimeClientSecret(t *testing.T, handler *codexlive.Handler, principal, provider, label string) string {
	t.Helper()
	router := gin.New()
	router.POST("/v1/realtime/client_secrets", func(c *gin.Context) {
		c.Set("userApiKey", principal)
		c.Set("accessProvider", provider)
		if label != "" {
			c.Set("userApiKeyLabel", label)
		}
		c.Next()
	}, handler.CreateClientSecret)

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{"session":{"type":"realtime","model":"gpt-realtime"}}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("mint status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var response struct {
		Value string `json:"value"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("unmarshal mint response: %v", errUnmarshal)
	}
	return response.Value
}

func TestRealtimeAuthMiddlewareRestoresIssuerLabel(t *testing.T) {
	tests := []struct {
		name      string
		label     string
		wantLabel string
		hasLabel  bool
	}{
		{name: "named issuer restores label", label: "alice", wantLabel: "alice", hasLabel: true},
		{name: "unnamed issuer sets no label", label: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			handler := codexlive.NewHandler(nil, nil)
			token := mintRealtimeClientSecret(t, handler, "sk-issuer", "config-api-key", tt.label)

			var (
				gotKey      any
				gotProvider any
				gotLabel    any
				labelExists bool
			)
			engine := gin.New()
			engine.Use(realtimeAuthMiddleware(nil, handler))
			engine.POST("/v1/realtime/calls", func(c *gin.Context) {
				gotKey, _ = c.Get("userApiKey")
				gotProvider, _ = c.Get("accessProvider")
				gotLabel, labelExists = c.Get("userApiKeyLabel")
				c.Status(http.StatusOK)
			})

			request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
			}
			if gotKey != "sk-issuer" {
				t.Fatalf("userApiKey = %v, want issuer principal %q", gotKey, "sk-issuer")
			}
			if gotProvider != "config-api-key" {
				t.Fatalf("accessProvider = %v, want %q", gotProvider, "config-api-key")
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
