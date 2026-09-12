package live

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
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

func TestHandleHangupForwardsUnauthorizedHomeResponseWithoutRefresh(t *testing.T) {
	const upstreamError = `{"error":{"message":"access token expired"}}`
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(&homeDispatcher{}, executionregistry.New(), 1)
	executor := &captureExecutor{
		statusCode:   http.StatusUnauthorized,
		responseBody: io.NopCloser(strings.NewReader(upstreamError)),
	}
	manager.RegisterExecutor(executor)
	handler := NewHandler(manager, nil)
	handler.sessions.put("call-123", liveSession{authID: "home-codex-live", model: defaultLiveModel})

	router := gin.New()
	router.POST("/v1/realtime/calls/:call_id/hangup", handler.HandleHangup)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized || recorder.Body.String() != upstreamError {
		t.Fatalf("response = status %d body %q, want original upstream 401", recorder.Code, recorder.Body.String())
	}
	if executor.refreshCalls.Load() != 0 || executor.httpCalls.Load() != 1 {
		t.Fatalf("refresh/http calls = %d/%d, want 0/1", executor.refreshCalls.Load(), executor.httpCalls.Load())
	}
	if got := executor.request.Header.Get("Authorization"); got != "Bearer home-live-token" {
		t.Fatalf("Authorization = %q, want original Home token", got)
	}
}

func TestHandleHangupReportsUnauthorizedWhenResponseReadFails(t *testing.T) {
	const upstreamError = `{"error":{"message":"access token expired"}}`
	gin.SetMode(gin.TestMode)
	runtimeConfig := &config.Config{
		Home:      config.HomeConfig{Enabled: true},
		SDKConfig: config.SDKConfig{RequestLog: true},
	}
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(runtimeConfig)
	manager.PublishHomeDispatch(&homeDispatcher{}, executionregistry.New(), 1)
	executor := &captureExecutor{
		statusCode:   http.StatusUnauthorized,
		responseBody: &errorResponseBody{payload: []byte(upstreamError)},
	}
	manager.RegisterExecutor(executor)
	usageCapture := registerHomeUnauthorizedUsageCapture(t, t.Name(), "home-codex-live")
	handler := NewHandler(manager, runtimeConfig)
	handler.sessions.put("call-123", liveSession{authID: "home-codex-live", model: defaultLiveModel})

	router := gin.New()
	var apiResponse []byte
	router.Use(func(c *gin.Context) {
		c.Next()
		if raw, exists := c.Get("API_RESPONSE"); exists {
			apiResponse, _ = raw.([]byte)
		}
	})
	router.POST("/v1/realtime/calls/:call_id/hangup", handler.HandleHangup)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls/call-123/hangup", nil)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	record := usageCapture.wait(t)
	if record.Fail.StatusCode != http.StatusUnauthorized || record.Fail.Body != upstreamError {
		t.Fatalf("Home unauthorized failure = status %d body %q, want status 401 body %q", record.Fail.StatusCode, record.Fail.Body, upstreamError)
	}
	if !strings.Contains(string(apiResponse), upstreamError) || !strings.Contains(string(apiResponse), io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("API_RESPONSE = %q, want upstream body and read error", apiResponse)
	}
	if executor.refreshCalls.Load() != 0 || executor.httpCalls.Load() != 1 {
		t.Fatalf("refresh/http calls = %d/%d, want 0/1", executor.refreshCalls.Load(), executor.httpCalls.Load())
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
