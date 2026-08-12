package live

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestHandleHangupForwardsPinnedOAuthCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	executor := &captureExecutor{
		statusCode:   http.StatusOK,
		responseBody: io.NopCloser(strings.NewReader(`{"status":"ok"}`)),
	}
	manager.RegisterExecutor(executor)
	registerCredential(t, manager, &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token"},
	})
	handler := NewHandler(manager, nil)
	handler.sessions.put("call-123", liveSession{
		authID:         "codex-oauth",
		model:          defaultLiveModel,
		ownerPrincipal: "owner-key",
		ownerProvider:  "static",
	})

	router := gin.New()
	router.POST("/v1/realtime/calls/:call_id/hangup", func(c *gin.Context) {
		c.Set("userApiKey", "owner-key")
		c.Set("accessProvider", "static")
		c.Next()
	}, handler.HandleHangup)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if executor.request == nil || executor.request.URL.String() != "https://api.openai.com/v1/realtime/calls/call-123/hangup" {
		t.Fatalf("upstream request = %#v", executor.request)
	}
	if _, ok := handler.sessions.peek("call-123"); ok {
		t.Fatal("successful hangup retained session")
	}
}

func TestHandleHangupRejectsDifferentAPIPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(auth.NewManager(nil, nil, nil), nil)
	handler.sessions.put("call-123", liveSession{
		authID:         "codex-oauth",
		model:          defaultLiveModel,
		ownerPrincipal: "owner-key",
		ownerProvider:  "static",
	})
	router := gin.New()
	router.POST("/v1/realtime/calls/:call_id/hangup", func(c *gin.Context) {
		c.Set("userApiKey", "other-key")
		c.Set("accessProvider", "static")
		c.Next()
	}, handler.HandleHangup)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestUnsupportedRealtimeCapabilitiesUseStandardError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(nil, nil)
	router := gin.New()
	router.POST("/v1/realtime/transcription_sessions", handler.HandleTranscriptionSession)
	router.POST("/v1/realtime/calls/:call_id/accept", handler.HandleSIPControl)

	for _, path := range []string{"/v1/realtime/transcription_sessions", "/v1/realtime/calls/call-123/accept"} {
		request := httptest.NewRequest(http.MethodPost, path, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNotImplemented {
			t.Errorf("%s status = %d, want %d", path, recorder.Code, http.StatusNotImplemented)
		}
		if !strings.Contains(recorder.Body.String(), `"type":"not_supported_error"`) || !strings.Contains(recorder.Body.String(), `"code":"realtime_capability_not_supported"`) {
			t.Errorf("%s body = %s", path, recorder.Body.String())
		}
	}
}
