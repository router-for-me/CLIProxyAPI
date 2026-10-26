package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestResponsesSteeringDisabledAccountCannotSendAnotherFrame(t *testing.T) {
	var frames atomic.Int32
	done := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
		if _, _, err := c.ReadMessage(); err != nil {
			t.Error(err)
			return
		}
		frames.Add(1)
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r1"}}`))
		_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r1","output":[]}}`))
		if _, _, err := c.ReadMessage(); err == nil {
			frames.Add(1)
		}
	}))
	defer upstream.Close()
	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	id := "steering-disable-test"
	auth, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"api_key": "test-key", "base_url": upstream.URL, "websockets": "true"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "steering-disable-model"}})
	defer registry.GetGlobalRegistry().UnregisterClient(id)
	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"steering-disable-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := c.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	disabled := auth.Clone()
	disabled.Disabled = true
	disabled.Status = coreauth.StatusDisabled
	if _, err := manager.Update(context.Background(), disabled); err != nil {
		t.Fatal(err)
	}
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer","previous_response_id":"r1","input":"must not be sent"}`)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("disabled account connection remained usable")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream close stalled")
	}
	if frames.Load() != 1 {
		t.Fatalf("disabled credential sent %d frames", frames.Load())
	}
}

func TestResponsesSteeringRevokedClientKeyCannotSendSecondCreate(t *testing.T) {
	var upstreamCreates atomic.Int32
	var upstreamSteers atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = c.Close() }()
		for {
			_, raw, errRead := c.ReadMessage()
			if errRead != nil {
				return
			}
			if gjson.GetBytes(raw, "type").String() == "response.steer" {
				upstreamSteers.Add(1)
				continue
			}
			n := upstreamCreates.Add(1)
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"r`+strings.Repeat("1", int(n))+`"}}`))
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"r`+strings.Repeat("1", int(n))+`","output":[]}}`))
		}
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	id := "steering-revoke-client-key-test"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         id,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"base_url": upstream.URL, "websockets": "true"},
		Metadata:   map[string]any{"access_token": "test-token"},
	}); err != nil {
		t.Fatal(err)
	}
	manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{
		"codex": {{Name: "steering-revoke-upstream-model", Alias: "steering-revoke-model"}},
	})
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{
		{ID: "steering-revoke-upstream-model"},
		{ID: "steering-revoke-model"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(id)

	provider := &revocableWebsocketAccessProvider{}
	access := sdkaccess.NewManager()
	access.SetProviders([]sdkaccess.Provider{provider})

	h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	h.AccessManager = access
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()

	hdr := http.Header{"Authorization": []string{"Bearer key-1"}}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))

	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"steering-revoke-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := c.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}

	provider.revoked.Store(true)
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer","previous_response_id":"r1","input":"must not be forwarded"}`)); err != nil {
		t.Fatal(err)
	}
	_, steerErrMsg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(steerErrMsg, "type").String(); got != "error" {
		t.Fatalf("steer after revoke event = %q, want error (%s)", got, steerErrMsg)
	}
	if got := int(gjson.GetBytes(steerErrMsg, "status").Int()); got != http.StatusForbidden {
		t.Fatalf("steer after revoke status = %d, want %d (%s)", got, http.StatusForbidden, steerErrMsg)
	}
	if got := upstreamSteers.Load(); got != 0 {
		t.Fatalf("upstream steers = %d, want 0", got)
	}

	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"steering-revoke-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	_, msg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(msg, "type").String(); got != "error" {
		t.Fatalf("second create after revoke event = %q, want error (%s)", got, msg)
	}
	if got := int(gjson.GetBytes(msg, "status").Int()); got != http.StatusForbidden {
		t.Fatalf("second create status = %d, want %d (%s)", got, http.StatusForbidden, msg)
	}
	if got := upstreamCreates.Load(); got != 1 {
		t.Fatalf("upstream creates = %d, want 1", got)
	}

	provider.revoked.Store(false)
	provider.deniedModel = "steering-revoke-model"
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer","previous_response_id":"r1","model":"allowed-other-model","input":"must not bypass model quota"}`)); err != nil {
		t.Fatal(err)
	}
	_, steerModelDeniedMsg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(steerModelDeniedMsg, "type").String(); got != "error" {
		t.Fatalf("steer with spoofed model on denied bound model event = %q, want error (%s)", got, steerModelDeniedMsg)
	}
	if got := int(gjson.GetBytes(steerModelDeniedMsg, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("steer with spoofed model status = %d, want %d (%s)", got, http.StatusTooManyRequests, steerModelDeniedMsg)
	}
	if got := upstreamSteers.Load(); got != 0 {
		t.Fatalf("upstream steers after denied bound model = %d, want 0", got)
	}

	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	_, aliasDeniedMsg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(aliasDeniedMsg, "type").String(); got != "error" {
		t.Fatalf("omitted-model continuation with denied client alias event = %q, want error (%s)", got, aliasDeniedMsg)
	}
	if got := int(gjson.GetBytes(aliasDeniedMsg, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("omitted-model continuation with denied client alias status = %d, want %d (%s)", got, http.StatusTooManyRequests, aliasDeniedMsg)
	}
	if got := upstreamCreates.Load(); got != 1 {
		t.Fatalf("upstream creates after denied client alias = %d, want 1", got)
	}

	provider.deniedModel = ""
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"steering-revoke-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := c.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}
	if got := upstreamCreates.Load(); got != 2 {
		t.Fatalf("upstream creates after un-revoke = %d, want 2", got)
	}

	provider.rotated.Store(true)
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer","previous_response_id":"r11","input":"x"}`)); err != nil {
		t.Fatal(err)
	}
	_, steerRotatedMsg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(steerRotatedMsg, "type").String(); got != "error" {
		t.Fatalf("steer after key rotation event = %q, want error (%s)", got, steerRotatedMsg)
	}
	if got := int(gjson.GetBytes(steerRotatedMsg, "status").Int()); got != http.StatusUnauthorized {
		t.Fatalf("steer after key rotation status = %d, want %d (%s)", got, http.StatusUnauthorized, steerRotatedMsg)
	}
	if got := upstreamSteers.Load(); got != 0 {
		t.Fatalf("upstream steers after key rotation = %d, want 0", got)
	}

	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"steering-revoke-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	_, createRotatedMsg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(createRotatedMsg, "type").String(); got != "error" {
		t.Fatalf("create after key rotation event = %q, want error (%s)", got, createRotatedMsg)
	}
	if got := int(gjson.GetBytes(createRotatedMsg, "status").Int()); got != http.StatusUnauthorized {
		t.Fatalf("create after key rotation status = %d, want %d (%s)", got, http.StatusUnauthorized, createRotatedMsg)
	}
	if got := upstreamCreates.Load(); got != 2 {
		t.Fatalf("upstream creates after key rotation = %d, want 2", got)
	}
}

type stripModelInterceptorHost struct {
	handlers.PluginInterceptorHost
}

func (*stripModelInterceptorHost) HasStreamInterceptors() bool { return false }

func (*stripModelInterceptorHost) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	body, _ := sjson.DeleteBytes(req.Body, "model")
	return pluginapi.RequestInterceptResponse{Body: body}
}

func (*stripModelInterceptorHost) InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return pluginapi.RequestInterceptResponse{}
}

func TestResponsesSteeringInterceptorRewrittenBodyBindsClientModel(t *testing.T) {
	var upstreamCreates atomic.Int32
	var upstreamSteers atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer func() { _ = c.Close() }()
		for {
			_, raw, errRead := c.ReadMessage()
			if errRead != nil {
				return
			}
			if gjson.GetBytes(raw, "type").String() == "response.steer" {
				upstreamSteers.Add(1)
				continue
			}
			n := upstreamCreates.Add(1)
			respID := "r" + strings.Repeat("1", int(n))
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.created","response":{"id":"`+respID+`"}}`))
			_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"`+respID+`","output":[]}}`))
		}
	}))
	defer upstream.Close()

	cfg := &config.Config{}
	cfg.Codex.ResponseSteering = true
	cfg.CodexResponseSteering = true
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexAutoExecutor(cfg))
	id := "steering-intercepted-model-test"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:         id,
		Provider:   "codex",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"base_url": upstream.URL, "websockets": "true"},
		Metadata:   map[string]any{"access_token": "test-token"},
	}); err != nil {
		t.Fatal(err)
	}
	manager.SetOAuthModelAlias(map[string][]config.OAuthModelAlias{
		"codex": {{Name: "steering-intercepted-upstream-model", Alias: "steering-intercepted-model"}},
	})
	registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{
		{ID: "steering-intercepted-upstream-model"},
		{ID: "steering-intercepted-model"},
	})
	defer registry.GetGlobalRegistry().UnregisterClient(id)

	provider := &revocableWebsocketAccessProvider{}
	access := sdkaccess.NewManager()
	access.SetProviders([]sdkaccess.Provider{provider})

	base := handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager)
	base.SetPluginHost(&stripModelInterceptorHost{})
	h := NewOpenAIResponsesAPIHandler(base)
	h.AccessManager = access
	router := gin.New()
	router.GET("/v1/responses", h.ResponsesWebsocket)
	downstream := httptest.NewServer(router)
	defer downstream.Close()

	hdr := http.Header{"Authorization": []string{"Bearer key-1"}}
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(downstream.URL, "http")+"/v1/responses", hdr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(8 * time.Second))

	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"steering-intercepted-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, _, err := c.ReadMessage(); err != nil {
			t.Fatal(err)
		}
	}

	provider.deniedModel = "steering-intercepted-model"
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	_, aliasDeniedMsg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(aliasDeniedMsg, "type").String(); got != "error" {
		t.Fatalf("omitted-model continuation for denied client alias = %s, want 429 error", aliasDeniedMsg)
	}
	if got := int(gjson.GetBytes(aliasDeniedMsg, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("omitted-model continuation status = %d, want %d (%s)", got, http.StatusTooManyRequests, aliasDeniedMsg)
	}
	if got := upstreamCreates.Load(); got != 1 {
		t.Fatalf("upstream creates after denied client alias = %d, want 1", got)
	}

	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.steer","previous_response_id":"r1","input":"must not bypass client model quota"}`)); err != nil {
		t.Fatal(err)
	}
	_, steerDeniedMsg, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(steerDeniedMsg, "type").String(); got != "error" {
		t.Fatalf("steer for denied client alias = %s, want 429 error", steerDeniedMsg)
	}
	if got := int(gjson.GetBytes(steerDeniedMsg, "status").Int()); got != http.StatusTooManyRequests {
		t.Fatalf("steer status = %d, want %d (%s)", got, http.StatusTooManyRequests, steerDeniedMsg)
	}
	if got := upstreamSteers.Load(); got != 0 {
		t.Fatalf("upstream steers after denied client alias = %d, want 0", got)
	}

	provider.deniedModel = "steering-intercepted-upstream-model"
	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, msg, errRead := c.ReadMessage()
		if errRead != nil {
			t.Fatal(errRead)
		}
		if got := gjson.GetBytes(msg, "type").String(); got == "error" {
			t.Fatalf("omitted-model continuation rejected when only upstream model is denied: %s", msg)
		}
	}
	if got := upstreamCreates.Load(); got != 2 {
		t.Fatalf("upstream creates when only upstream model is denied = %d, want 2", got)
	}

	if err := c.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"steering-intercepted-model","input":[]}`)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, msg, errRead := c.ReadMessage()
		if errRead != nil {
			t.Fatalf("explicit client alias continuation failed: %v", errRead)
		}
		if got := gjson.GetBytes(msg, "type").String(); got == "error" {
			t.Fatalf("explicit client alias continuation returned error: %s", msg)
		}
	}
	if got := upstreamCreates.Load(); got != 3 {
		t.Fatalf("upstream creates after explicit client alias continuation = %d, want 3", got)
	}
}
