package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestAttachWebsocketRouteDisabledInHomeMode(t *testing.T) {
	for _, homeEnabled := range []bool{false, true} {
		for _, wsAuth := range []bool{false, true} {
			t.Run(fmt.Sprintf("home=%t/ws-auth=%t", homeEnabled, wsAuth), func(t *testing.T) {
				cfg := &config.Config{
					SDKConfig:     config.SDKConfig{APIKeys: []string{"test-key"}},
					Home:          config.HomeConfig{Enabled: homeEnabled},
					WebsocketAuth: wsAuth,
				}
				if homeEnabled {
					// Home mode clears local API keys during startup.
					cfg.APIKeys = nil
				}
				server := newTestServerWithConfig(t, cfg)
				// Isolate route registration from the Home heartbeat readiness gate.
				server.engine = gin.New()
				handlerCalled := false
				server.AttachWebsocketRoute("/v1/ws", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					handlerCalled = true
					w.WriteHeader(http.StatusNoContent)
				}))

				for _, route := range server.engine.Routes() {
					if homeEnabled && route.Path == "/v1/ws" {
						t.Fatal("websocket route must not be registered in Home mode")
					}
				}

				for _, token := range []string{"", "test-key", "invalid-key"} {
					handlerCalled = false
					req := httptest.NewRequest(http.MethodGet, "/v1/ws", nil)
					req.Header.Set("Connection", "Upgrade")
					req.Header.Set("Upgrade", "websocket")
					if token != "" {
						req.Header.Set("Authorization", "Bearer "+token)
					}
					recorder := httptest.NewRecorder()
					server.engine.ServeHTTP(recorder, req)

					wantStatus := http.StatusNoContent
					if homeEnabled {
						wantStatus = http.StatusNotFound
					} else if wsAuth && token != "test-key" {
						wantStatus = http.StatusUnauthorized
					}
					if recorder.Code != wantStatus {
						t.Errorf("token=%q: status = %d, want %d", token, recorder.Code, wantStatus)
					}
					if wantCalled := wantStatus == http.StatusNoContent; handlerCalled != wantCalled {
						t.Errorf("token=%q: handler called = %t, want %t", token, handlerCalled, wantCalled)
					}
				}
			})
		}
	}
}
