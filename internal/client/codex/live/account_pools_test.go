package live

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Account pools reach the Codex voice handlers only through the request
// context (auth.WithAccountPools, set by the API layer); the handlers choose
// their own Codex login through selectOAuth. These tests pin that the choice
// stays inside the key's pools, in pool order, and never falls back to a login
// outside them.

// voicePoolConfig routes sk-codex- keys through the primary pool, then shared;
// sk-service- keys may only use shared.
func voicePoolConfig() config.ClientConfig {
	codexChain, serviceChain := []string{"primary", "shared"}, []string{"shared"}
	return config.ClientConfig{
		AccountPools: &[]config.ClientAccountPool{
			{Name: "primary", AuthIDs: []string{"codex-primary"}, AuthKind: auth.AuthKindOAuth},
			{Name: "shared", AuthIDs: []string{"codex-shared"}, AuthKind: auth.AuthKindOAuth},
		},
		KeyScopes: &[]config.ClientKeyScope{
			{KeyPrefix: "sk-codex-", Providers: []string{"codex"}, Pools: &codexChain},
			{KeyPrefix: "sk-service-", Providers: []string{"codex"}, Pools: &serviceChain},
		},
	}
}

// voicePoolManager registers three Codex logins. The one outside every pool
// has the highest priority, so an unpooled selection picks it and a pooled
// selection that reached it would show.
func voicePoolManager(t *testing.T) (*auth.Manager, *captureExecutor) {
	t.Helper()
	manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
	executor := &captureExecutor{}
	manager.RegisterExecutor(executor)
	for _, credential := range []struct{ id, priority string }{
		{"codex-primary", "0"}, {"codex-shared", "100"}, {"codex-outside", "1000"},
	} {
		registerCredential(t, manager, &auth.Auth{
			ID:         credential.id,
			Provider:   "codex",
			Status:     auth.StatusActive,
			Attributes: map[string]string{"priority": credential.priority},
			Metadata:   map[string]any{"access_token": credential.id + "-token"},
		})
	}
	return manager, executor
}

func voicePoolContext(t *testing.T, ctx context.Context, key string) context.Context {
	t.Helper()
	policy, errResolve := auth.ResolveAccountPools(voicePoolConfig(), key)
	if errResolve != nil {
		t.Fatalf("ResolveAccountPools(%q): %v", key, errResolve)
	}
	return auth.WithAccountPools(ctx, policy)
}

func voiceMarkUnavailable(t *testing.T, manager *auth.Manager, id string) {
	t.Helper()
	credential, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("credential %s is not registered", id)
	}
	credential.Unavailable = true
	credential.NextRetryAfter = time.Now().Add(time.Hour)
	if _, errUpdate := manager.Update(context.Background(), credential); errUpdate != nil {
		t.Fatalf("update %s: %v", id, errUpdate)
	}
}

func voiceAuthID(credential *auth.Auth) string {
	if credential == nil {
		return ""
	}
	return credential.ID
}

func voicePick(t *testing.T, handler *Handler, ctx context.Context, pinned string) (string, error) {
	t.Helper()
	opts := coreexecutor.Options{}
	if pinned != "" {
		opts.Metadata = map[string]any{coreexecutor.PinnedAuthMetadataKey: pinned}
	}
	selection, selected, errSelect := handler.selectOAuth(ctx, defaultLiveModel, opts)
	if selection != nil {
		selection.End("test_complete")
	}
	if errSelect != nil {
		return "", errSelect
	}
	if selected == nil {
		t.Fatal("selectOAuth returned neither a credential nor an error")
	}
	return selected.ID, nil
}

func TestVoiceSelectionStaysInAccountPools(t *testing.T) {
	manager, _ := voicePoolManager(t)
	handler := NewHandler(manager, nil)
	background := context.Background()
	codexKey := voicePoolContext(t, background, "sk-codex-test")
	serviceKey := voicePoolContext(t, background, "sk-service-test")

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		pinned string
		want   string
	}{
		{name: "unpooled key keeps priority order", ctx: background, want: "codex-outside"},
		{name: "pool order beats credential priority", ctx: codexKey, want: "codex-primary"},
		{name: "single-pool key", ctx: serviceKey, want: "codex-shared"},
		{name: "pin inside a later pool is kept", ctx: codexKey, pinned: "codex-shared", want: "codex-shared"},
		{name: "pin outside the pools is refused", ctx: codexKey, pinned: "codex-outside"},
		{name: "pin in another key's pool is refused", ctx: serviceKey, pinned: "codex-primary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errPick := voicePick(t, handler, tc.ctx, tc.pinned)
			if tc.want == "" {
				if errPick == nil {
					t.Fatalf("selected %q, want a refusal", got)
				}
				return
			}
			if errPick != nil || got != tc.want {
				t.Fatalf("selected %q (err %v), want %q", got, errPick, tc.want)
			}
		})
	}

	voiceMarkUnavailable(t, manager, "codex-primary")
	if got, errPick := voicePick(t, handler, codexKey, ""); errPick != nil || got != "codex-shared" {
		t.Fatalf("with primary unavailable selected %q (err %v), want codex-shared", got, errPick)
	}
	voiceMarkUnavailable(t, manager, "codex-shared")
	if got, errPick := voicePick(t, handler, codexKey, ""); errPick == nil {
		t.Fatalf("with every pool unavailable selected %q, want a refusal", got)
	}
	if got, errPick := voicePick(t, handler, background, ""); errPick != nil || got != "codex-outside" {
		t.Fatalf("unpooled selection = %q (err %v), want codex-outside", got, errPick)
	}
}

func TestVoiceHomeSelectionRejectsAccountPools(t *testing.T) {
	manager := auth.NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	dispatcher := &homeDispatcher{}
	registry := executionregistry.New()
	manager.PublishHomeDispatch(dispatcher, registry, 1)
	manager.RegisterExecutor(&captureExecutor{})
	handler := NewHandler(manager, nil)

	selection, selected, errSelect := handler.selectOAuth(voicePoolContext(t, context.Background(), "sk-codex-test"), defaultLiveModel, coreexecutor.Options{})
	if errSelect == nil || selection != nil || selected != nil {
		t.Fatalf("pooled Home selection returned a credential (selection %t, auth %t, err %v), want a refusal", selection != nil, selected != nil, errSelect)
	}
	if dispatcher.model != "" {
		t.Fatalf("pooled request reached Home dispatch for %q", dispatcher.model)
	}

	selection, selected, errSelect = handler.selectOAuth(context.Background(), defaultLiveModel, coreexecutor.Options{})
	if errSelect != nil || selection == nil || selected == nil {
		t.Fatalf("unpooled Home selection failed (selection %t, auth %t, err %v)", selection != nil, selected != nil, errSelect)
	}
	selection.End("test_complete")
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
}

// voicePoolRouter stands in for the API layer: it authenticates the key and
// attaches its pool policy, as keyScopeMiddleware in internal/api does.
func voicePoolRouter(t *testing.T, handler *Handler) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		key := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		policy, errResolve := auth.ResolveAccountPools(voicePoolConfig(), key)
		if errResolve != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
		c.Set("userApiKey", key)
		c.Set("accessProvider", "config-api-key")
		c.Request = c.Request.WithContext(auth.WithAccountPools(c.Request.Context(), policy))
	})
	router.POST("/v1/live", handler.Handle)
	router.POST("/v1/realtime/calls", handler.Handle)
	router.GET("/v1/live/:call_id", handler.HandleSideband)
	router.GET("/v1/realtime", handler.HandleRealtimeWebsocket)
	router.POST("/v1/realtime/calls/:call_id/hangup", handler.HandleHangup)
	return router
}

func voiceRequest(router http.Handler, method, path, key string, websocketUpgrade bool) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, strings.NewReader("v=0\r\n"))
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/sdp")
	if websocketUpgrade {
		request.Header.Set("Connection", "Upgrade")
		request.Header.Set("Upgrade", "websocket")
		request.Header.Set("Sec-WebSocket-Version", "13")
		request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

func TestVoiceCallsAndControlStayInAccountPools(t *testing.T) {
	manager, executor := voicePoolManager(t)
	handler := NewHandler(manager, nil)
	var upstreamDials atomic.Int32
	upstreamTokens := make(chan string, 4)
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamDials.Add(1)
		upstreamTokens <- request.Header.Get("Authorization")
		upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, errUpgrade := upgrader.Upgrade(writer, request, nil)
		if errUpgrade != nil {
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()
	handler.sidebandAPIBaseURL = "ws" + strings.TrimPrefix(upstream.URL, "http") + "/v1"
	router := voicePoolRouter(t, handler)

	// Creating a call uses the key's first pool even though a higher-priority login exists.
	for _, path := range []string{"/v1/live", "/v1/realtime/calls"} {
		executor.responseBody = io.NopCloser(strings.NewReader("v=0\r\n"))
		if recorder := voiceRequest(router, http.MethodPost, path, "sk-codex-test", false); recorder.Code != http.StatusCreated {
			t.Fatalf("%s status = %d; body=%s", path, recorder.Code, recorder.Body.String())
		}
		if voiceAuthID(executor.selectedAuth) != "codex-primary" {
			t.Fatalf("%s used %q, want codex-primary", path, voiceAuthID(executor.selectedAuth))
		}
	}
	stored, ok := handler.sessions.peek("call-123")
	if !ok || stored.authID != "codex-primary" || stored.ownerPrincipal != "sk-codex-test" {
		t.Fatalf("stored call = %#v (ok %t)", stored, ok)
	}

	// The sideband stays on the call's own login.
	server := httptest.NewServer(router)
	defer server.Close()
	client, response, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/live/call-123", http.Header{"Authorization": {"Bearer sk-codex-test"}})
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if errDial != nil {
		t.Fatalf("pooled sideband dial: %v", errDial)
	}
	_ = client.Close()
	select {
	case token := <-upstreamTokens:
		if token != "Bearer codex-primary-token" {
			t.Fatalf("sideband upstream Authorization = %q, want the call's login", token)
		}
	case <-time.After(time.Second):
		t.Fatal("sideband did not reach the upstream")
	}

	// Another key cannot join or hang up the call, even one with a pool of its own.
	handler.sessions.put("call-owned", liveSession{authID: "codex-shared", model: defaultLiveModel, ownerPrincipal: "sk-codex-test", ownerProvider: "config-api-key"})
	if recorder := voiceRequest(router, http.MethodGet, "/v1/live/call-owned", "sk-service-test", true); recorder.Code != http.StatusForbidden {
		t.Fatalf("foreign sideband status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := voiceRequest(router, http.MethodPost, "/v1/realtime/calls/call-owned/hangup", "sk-service-test", false); recorder.Code != http.StatusForbidden {
		t.Fatalf("foreign hangup status = %d, want 403; body=%s", recorder.Code, recorder.Body.String())
	}

	// A call whose login has left the key's pools (for example after a pool edit)
	// can no longer be joined or controlled through it.
	handler.sessions.put("call-outside", liveSession{authID: "codex-outside", model: defaultLiveModel, ownerPrincipal: "sk-codex-test", ownerProvider: "config-api-key"})
	dialsBefore, callsBefore := upstreamDials.Load(), executor.httpCalls.Load()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/live/call-outside"},
		{http.MethodGet, "/v1/realtime?call_id=call-outside"},
		{http.MethodPost, "/v1/realtime/calls/call-outside/hangup"},
	} {
		recorder := voiceRequest(router, tc.method, tc.path, "sk-codex-test", tc.method == http.MethodGet)
		if recorder.Code < http.StatusBadRequest {
			t.Fatalf("%s %s status = %d, want a refusal; body=%s", tc.method, tc.path, recorder.Code, recorder.Body.String())
		}
	}
	if upstreamDials.Load() != dialsBefore || executor.httpCalls.Load() != callsBefore {
		t.Fatalf("out-of-pool call reached the upstream: dials %d->%d, requests %d->%d", dialsBefore, upstreamDials.Load(), callsBefore, executor.httpCalls.Load())
	}

	// With every pool unavailable a new call fails instead of using the outside login.
	voiceMarkUnavailable(t, manager, "codex-primary")
	voiceMarkUnavailable(t, manager, "codex-shared")
	executor.responseBody = io.NopCloser(strings.NewReader("v=0\r\n"))
	callsBefore = executor.httpCalls.Load()
	if recorder := voiceRequest(router, http.MethodPost, "/v1/realtime/calls", "sk-codex-test", false); recorder.Code < http.StatusBadRequest {
		t.Fatalf("exhausted pools status = %d, want a refusal; body=%s", recorder.Code, recorder.Body.String())
	}
	if executor.httpCalls.Load() != callsBefore {
		t.Fatalf("exhausted pools reached the upstream with %q", voiceAuthID(executor.selectedAuth))
	}
}
