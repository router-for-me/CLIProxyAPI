//go:build solaris

package live

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestHandlerRejectsUnsupportedMediaRelayAndPreservesSDPForwarding(t *testing.T) {
	const offer = "v=0\r\no=client-offer\r\n"
	const answer = "v=0\r\no=upstream-answer\r\n"
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	executor := &captureExecutor{responseBody: &trackedResponseBody{Reader: strings.NewReader(answer)}}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	handler := NewHandler(manager, nil)
	t.Cleanup(handler.Close)
	router := gin.New()
	router.POST("/v1/live", handler.Handle)
	call := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/live", strings.NewReader(offer))
		req.Header.Set("Content-Type", "application/sdp")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, req)
		return recorder
	}

	enabled := &config.Config{}
	enabled.Codex.LiveMediaRelay.Enabled = true
	if errUpdate := handler.UpdateConfig(enabled); !errors.Is(errUpdate, errors.ErrUnsupported) {
		t.Fatalf("enable media relay: got %v, want unsupported error", errUpdate)
	}
	if response := call(); response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), errors.ErrUnsupported.Error()) {
		t.Fatalf("enabled media relay: status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := executor.httpCalls.Load(); got != 0 {
		t.Fatalf("upstream requests with unsupported media relay = %d, want 0", got)
	}

	if errUpdate := handler.UpdateConfig(&config.Config{}); errUpdate != nil {
		t.Fatalf("disable media relay: %v", errUpdate)
	}
	if response := call(); response.Code != http.StatusCreated || response.Body.String() != answer {
		t.Fatalf("direct SDP forwarding: status = %d, body = %q", response.Code, response.Body.String())
	}
	if got := string(executor.body); got != offer {
		t.Fatalf("upstream SDP = %q, want %q", got, offer)
	}
}
