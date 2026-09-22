package live

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCreateClientSecretMapsStandardRealtimeModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{clientSecrets: newClientSecretStore()}
	router := gin.New()
	router.POST("/v1/realtime/client_secrets", func(c *gin.Context) {
		c.Set("userApiKey", "issuer-key")
		c.Set("accessProvider", "static")
		c.Next()
	}, handler.CreateClientSecret)

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{
		"session":{"type":"realtime","model":"gpt-realtime","instructions":"help"},
		"expires_after":{"anchor":"created_at","seconds":60}
	}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}

	var response struct {
		Value     string `json:"value"`
		ExpiresAt int64  `json:"expires_at"`
		Session   struct {
			ID           string `json:"id"`
			Object       string `json:"object"`
			Type         string `json:"type"`
			Model        string `json:"model"`
			Instructions string `json:"instructions"`
		} `json:"session"`
	}
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if !strings.HasPrefix(response.Value, clientSecretPrefix) {
		t.Fatalf("client secret = %q", response.Value)
	}
	if response.ExpiresAt <= time.Now().Unix() {
		t.Fatalf("expires_at = %d", response.ExpiresAt)
	}
	if response.Session.ID == "" || response.Session.Object != "realtime.session" || response.Session.Type != "realtime" {
		t.Fatalf("session = %+v", response.Session)
	}
	if response.Session.Model != "gpt-realtime" || response.Session.Instructions != "help" {
		t.Fatalf("client session = %+v", response.Session)
	}

	authRequest := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)
	authRequest.Header.Set("Authorization", "Bearer "+response.Value)
	authorization, matched, errAuthenticate := handler.AuthenticateClientSecret(authRequest)
	if errAuthenticate != nil || !matched {
		t.Fatalf("AuthenticateClientSecret() matched=%t error=%v", matched, errAuthenticate)
	}
	if authorization.Principal != response.Session.ID {
		t.Fatalf("principal = %q, want %q", authorization.Principal, response.Session.ID)
	}
	if authorization.IssuerPrincipal != "issuer-key" || authorization.IssuerProvider != "static" {
		t.Fatalf("issuer = %q/%q", authorization.IssuerProvider, authorization.IssuerPrincipal)
	}
	if got := modelFromJSON(authorization.Session); got != defaultLiveModel {
		t.Fatalf("upstream session model = %q, want %q", got, defaultLiveModel)
	}
}

func TestStandardRealtimeCallMapsModelAndLocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	manager := auth.NewManager(nil, nil, nil)
	executor := &captureExecutor{responseBody: io.NopCloser(strings.NewReader("v=0\r\n"))}
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(context.Background(), &auth.Auth{
		ID:       "codex-oauth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "oauth-token"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	handler := NewHandler(manager, nil)
	router := gin.New()
	router.POST("/v1/realtime/calls", handler.Handle)

	const boundary = "standard-realtime-boundary"
	body := multipartBody(boundary, "v=0\r\n", `{"type":"realtime","model":"gpt-realtime"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", strings.NewReader(body))
	request.Header.Set("Content-Type", "multipart/form-data; boundary="+boundary)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if recorder.Header().Get("Location") != "/v1/realtime/calls/call-123" {
		t.Fatalf("Location = %q", recorder.Header().Get("Location"))
	}
	if got := modelFromJSON(executor.body); got != defaultLiveModel {
		t.Fatalf("upstream model = %q, want %q; body=%s", got, defaultLiveModel, executor.body)
	}
}

func TestClientSecretStoreRejectsExpiredToken(t *testing.T) {
	store := newClientSecretStore()
	now := time.Unix(1700000000, 0)
	store.now = func() time.Time { return now }
	token, _, _, errCreate := store.create(json.RawMessage(`{"type":"realtime","model":"gpt-live-1-codex"}`), time.Minute, "issuer", "test")
	if errCreate != nil {
		t.Fatalf("create() error = %v", errCreate)
	}
	if _, errAuthenticate := store.authenticate(token); errAuthenticate != nil {
		t.Fatalf("authenticate() error = %v", errAuthenticate)
	}
	now = now.Add(time.Minute)
	if _, errAuthenticate := store.authenticate(token); errAuthenticate == nil {
		t.Fatal("authenticate() accepted expired token")
	}
}

func TestNormalizeClientSecretSessionHandlesWhitespaceNullAndRejectsArrays(t *testing.T) {
	clientSession, upstreamSession, errNormalize := normalizeClientSecretSession(json.RawMessage("  null \n"))
	if errNormalize != nil {
		t.Fatalf("normalize whitespace null: %v", errNormalize)
	}
	if modelFromJSON(clientSession) != "gpt-realtime" || modelFromJSON(upstreamSession) != defaultLiveModel {
		t.Fatalf("client=%s upstream=%s", clientSession, upstreamSession)
	}
	if _, _, errNormalize = normalizeClientSecretSession(json.RawMessage(`[]`)); errNormalize == nil {
		t.Fatal("normalize accepted an array session")
	}
}

func TestReadClientSecretBodyRejectsOversizedSession(t *testing.T) {
	_, errRead := readClientSecretBody(bytes.NewReader(make([]byte, clientSecretMaxBodySize+1)))
	if !errors.Is(errRead, errBodyTooLarge) {
		t.Fatalf("readClientSecretBody() error = %v", errRead)
	}
}

func TestCreateClientSecretRejectsUnsupportedSessionType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{clientSecrets: newClientSecretStore()}
	router := gin.New()
	router.POST("/v1/realtime/client_secrets", handler.CreateClientSecret)

	request := httptest.NewRequest(http.MethodPost, "/v1/realtime/client_secrets", strings.NewReader(`{"session":{"type":"transcription","model":"gpt-4o-transcribe"}}`))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusNotImplemented, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "realtime_capability_not_supported") {
		t.Fatalf("body = %s", recorder.Body.String())
	}
}

func TestLiveSelectionHeadersRemoveLocalClientSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = httptest.NewRequest(http.MethodPost, "/v1/realtime/calls", nil)
	ginContext.Request.Header.Set("Authorization", "Bearer ek_secret")
	ginContext.Request.Header.Set("OpenAI-Safety-Identifier", "safe-user")
	ginContext.Set(ClientSecretPrincipalContextKey, "sess_123")
	headers := liveSelectionHeaders(ginContext)
	if headers.Get("Authorization") != "" {
		t.Fatalf("Authorization leaked: %q", headers.Get("Authorization"))
	}
	if headers.Get("OpenAI-Safety-Identifier") != "safe-user" {
		t.Fatalf("safety identifier = %q", headers.Get("OpenAI-Safety-Identifier"))
	}
}

func TestSidebandRejectsClientSecretScopeMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(auth.NewManager(nil, nil, nil), nil)
	handler.sessions.put("call-123", liveSession{
		authID:                "codex-oauth",
		model:                 defaultLiveModel,
		clientSecretPrincipal: "sess_expected",
	})
	router := gin.New()
	router.GET("/v1/realtime/calls/:call_id", func(c *gin.Context) {
		c.Set(ClientSecretPrincipalContextKey, "sess_other")
		c.Next()
	}, handler.HandleSideband)
	request := httptest.NewRequest(http.MethodGet, "/v1/realtime/calls/call-123", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
	claimed, claim := handler.sessions.claim("call-123")
	if claim != sessionClaimAcquired {
		t.Fatalf("session claim = %v", claim)
	}
	handler.sessions.release(claimed)
}

func TestSidebandRejectsStandardPrincipalScopeMismatch(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := NewHandler(auth.NewManager(nil, nil, nil), nil)
	handler.sessions.put("call-123", liveSession{
		authID:         "codex-oauth",
		model:          defaultLiveModel,
		ownerPrincipal: "owner-key",
		ownerProvider:  "static",
	})
	router := gin.New()
	router.GET("/v1/realtime/calls/:call_id", func(c *gin.Context) {
		c.Set("userApiKey", "other-key")
		c.Set("accessProvider", "static")
		c.Next()
	}, handler.HandleSideband)
	request := httptest.NewRequest(http.MethodGet, "/v1/realtime/calls/call-123", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusForbidden, recorder.Body.String())
	}
}

func TestApplyClientSecretCallSession(t *testing.T) {
	session := json.RawMessage(`{"type":"realtime","model":"gpt-live-1-codex","instructions":"help"}`)
	body, contentType, model, errApply := applyClientSecretCallSession([]byte("v=0\r\n"), "application/sdp", defaultLiveModel, session)
	if errApply != nil {
		t.Fatalf("applyClientSecretCallSession() error = %v", errApply)
	}
	if contentType != "application/json" || model != defaultLiveModel {
		t.Fatalf("contentType=%q model=%q", contentType, model)
	}
	var payload struct {
		SDP     string `json:"sdp"`
		Session struct {
			Instructions string `json:"instructions"`
		} `json:"session"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal body: %v", errUnmarshal)
	}
	if payload.SDP != "v=0\r\n" || payload.Session.Instructions != "help" {
		t.Fatalf("payload = %+v", payload)
	}
}
