package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAccountPoolsRouteGuard(t *testing.T) {
	gin.SetMode(gin.TestMode)
	chain := []string{"reviewer"}
	cfg := &config.SDKConfig{Client: config.ClientConfig{
		AccountPools: &[]config.ClientAccountPool{{Name: "reviewer"}},
		KeyScopes:    &[]config.ClientKeyScope{{KeyPrefix: "review-", Providers: []string{"claude"}, Pools: &chain}},
	}}
	s := &Server{handlers: handlers.NewBaseAPIHandlers(cfg, nil)}
	for _, tc := range []struct {
		method, pattern, path, key string
		want                       int
	}{
		{"POST", "/v1/messages", "/v1/messages", "review-key", 204},
		{"POST", "/v1/responses", "/v1/responses", "review-key", 204},
		{"GET", "/v1/responses", "/v1/responses", "review-key", 204},
		{"GET", "/v1/models", "/v1/models", "review-key", 204},
		{"GET", "/v1/models/*model", "/v1/models/claude-test", "review-key", 204},
		{"POST", "/v1/images/generations", "/v1/images/generations", "review-key", 403},
		{"POST", "/v1/videos", "/v1/videos", "review-key", 403},
		{"POST", "/v1beta/interactions", "/v1beta/interactions", "review-key", 403},
		{"POST", "/new-endpoint", "/new-endpoint", "review-key", 403},
		{"POST", "/new-endpoint", "/new-endpoint", "legacy-key", 204},
	} {
		t.Run(tc.method+tc.path+tc.key, func(t *testing.T) {
			router := gin.New()
			router.Use(func(c *gin.Context) { c.Set("userApiKey", tc.key) }, s.keyScopeMiddleware())
			router.Handle(tc.method, tc.pattern, func(c *gin.Context) { c.Status(http.StatusNoContent) })
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
		})
	}
}

func TestAccountPoolsDirectSearchUsesAuthenticatedPolicy(t *testing.T) {
	server := newTestServer(t)
	chain := []string{"reviewer"}
	server.handlers.Cfg.Client.AccountPools = &[]config.ClientAccountPool{{Name: "reviewer", AuthIDs: []string{"reviewer"}}}
	server.handlers.Cfg.Client.KeyScopes = &[]config.ClientKeyScope{{KeyPrefix: "test-", Providers: []string{"codex"}, Pools: &chain}}
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	for _, id := range []string{"reviewer", "personal"} {
		if _, err := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
			ID: id, Provider: "codex", Metadata: map[string]any{"access_token": "test-token"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	request := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/v1/alpha/search", strings.NewReader(`{"query":"test"}`))
		r.Header.Set("Authorization", "Bearer test-key")
		r.Header.Set("X-Account-Pool", "personal")
		w := httptest.NewRecorder()
		server.engine.ServeHTTP(w, r)
		return w
	}
	if w := request(); w.Code != 200 || len(executor.authIDs) != 1 || executor.authIDs[0] != "reviewer" {
		t.Fatalf("direct search escaped pool: status=%d ids=%v", w.Code, executor.authIDs)
	}
	(*server.handlers.Cfg.Client.AccountPools)[0].AuthIDs = nil
	if w := request(); w.Code == 200 || len(executor.authIDs) != 1 {
		t.Fatalf("empty reviewer pool used another account: status=%d ids=%v", w.Code, executor.authIDs)
	}
}

// codexPoolKeyClient routes Codex keys through pools, scopes one Codex key by
// an alias without pools, gives a Claude key pools that hold Codex logins, and
// leaves the Meta and xAI keys unpooled.
func codexPoolKeyClient() config.ClientConfig {
	codexChain, serviceChain := []string{"primary", "shared"}, []string{"shared"}
	return config.ClientConfig{
		AccountPools: &[]config.ClientAccountPool{
			{Name: "primary", AuthIDs: []string{"codex-primary"}},
			{Name: "shared", AuthIDs: []string{"codex-shared"}},
		},
		KeyScopes: &[]config.ClientKeyScope{
			{KeyPrefix: "sk-codex-", Providers: []string{"codex"}, Pools: &codexChain},
			{KeyPrefix: "sk-service-", Providers: []string{"codex"}, Pools: &serviceChain},
			{KeyPrefix: "sk-openai-", Providers: []string{"openai"}},
			{KeyPrefix: "sk-claude-", Providers: []string{"claude"}, Pools: &codexChain},
			{KeyPrefix: "sk-muse-", Providers: []string{"meta"}},
			{KeyPrefix: "sk-grok-", Providers: []string{"xai"}},
		},
	}
}

func TestCodexRoutesApplyKeyScopesAndPools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sdkCfg := &config.SDKConfig{Client: codexPoolKeyClient()}
	homeManager := auth.NewManager(nil, nil, nil)
	homeManager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	for _, mode := range []struct {
		name    string
		manager *auth.Manager
		want    map[string]int
	}{
		{name: "local", want: map[string]int{
			"sk-codex-test": 204, "sk-service-test": 204, "sk-openai-test": 204, "legacy-key": 204,
			"sk-claude-test": 403, "sk-muse-test": 403, "sk-grok-test": 403,
		}},
		// Key scopes do not apply in Home mode, and pooled keys are refused there.
		{name: "home", manager: homeManager, want: map[string]int{
			"sk-muse-test": 204, "sk-grok-test": 204, "sk-openai-test": 204, "legacy-key": 204,
			"sk-codex-test": 503, "sk-service-test": 503, "sk-claude-test": 503,
		}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			s := &Server{handlers: handlers.NewBaseAPIHandlers(sdkCfg, mode.manager)}
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("userApiKey", strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
			}, s.keyScopeMiddleware())
			noContent := func(c *gin.Context) { c.Status(http.StatusNoContent) }
			for route := range codexOnlyRoutes {
				method, pattern, _ := strings.Cut(route, " ")
				router.Handle(method, pattern, noContent)
			}
			for route := range codexOnlyRoutes {
				method, pattern, _ := strings.Cut(route, " ")
				for key, want := range mode.want {
					request := httptest.NewRequest(method, codexRoutePath(pattern), nil)
					request.Header.Set("Authorization", "Bearer "+key)
					recorder := httptest.NewRecorder()
					router.ServeHTTP(recorder, request)
					if recorder.Code != want {
						t.Errorf("%s with %s: status=%d want=%d body=%s", route, key, recorder.Code, want, recorder.Body.String())
						continue
					}
					if want == http.StatusForbidden && keyScopeErrorCode(recorder.Body.Bytes()) != "provider_not_allowed" {
						t.Errorf("%s with %s: body=%s, want code provider_not_allowed", route, key, recorder.Body.String())
					}
				}
			}
		})
	}
}

// TestVoiceAndSearchAccountPoolsOnServer runs the real route table. It never
// completes a WebSocket upgrade: those handlers dial the Codex upstream
// directly.
func TestVoiceAndSearchAccountPoolsOnServer(t *testing.T) {
	keys := []string{"sk-codex-test", "sk-service-test", "sk-openai-test", "sk-claude-test", "sk-muse-test", "sk-grok-test", "legacy-key"}
	server := newTestServerWithConfig(t, &config.Config{SDKConfig: config.SDKConfig{APIKeys: keys}})
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	// The login outside every pool has the highest priority, so unpooled keys pick it.
	for _, credential := range []struct{ id, priority string }{
		{"codex-primary", "0"}, {"codex-shared", "100"}, {"codex-outside", "1000"},
	} {
		if _, errRegister := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
			ID:         credential.id,
			Provider:   "codex",
			Status:     auth.StatusActive,
			Attributes: map[string]string{"priority": credential.priority},
			Metadata:   map[string]any{"access_token": credential.id + "-token"},
		}); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	serve := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+bearer)
		if strings.HasPrefix(body, "v=0") {
			request.Header.Set("Content-Type", "application/sdp")
		} else if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, request)
		return recorder
	}
	selectedBy := func(method, path, bearer, body string) (*httptest.ResponseRecorder, []string) {
		executor.authIDs = nil
		recorder := serve(method, path, bearer, body)
		return recorder, slices.Clone(executor.authIDs)
	}
	callRoutes := []struct{ path, body string }{
		{"/v1/live", "v=0\r\n"},
		{"/v1/realtime", "v=0\r\n"},
		{"/v1/realtime/calls", "v=0\r\n"},
		{"/v1/alpha/search", `{"query":"test"}`},
		{"/backend-api/codex/alpha/search", `{"query":"test"}`},
	}

	t.Run("Codex keys choose inside their pools and unpooled keys are unchanged", func(t *testing.T) {
		server.handlers.Cfg.Client = codexPoolKeyClient()
		for key, want := range map[string]string{
			"sk-codex-test":   "codex-primary",
			"sk-service-test": "codex-shared",
			"sk-openai-test":  "codex-outside",
			"legacy-key":      "codex-outside",
		} {
			for _, route := range callRoutes {
				recorder, ids := selectedBy(http.MethodPost, route.path, key, route.body)
				if recorder.Code != http.StatusOK || !slices.Equal(ids, []string{want}) {
					t.Errorf("%s with %s: status=%d used=%v want=%s body=%s", route.path, key, recorder.Code, ids, want, recorder.Body.String())
				}
			}
		}
	})

	t.Run("pooled keys reach the other voice routes", func(t *testing.T) {
		server.handlers.Cfg.Client = codexPoolKeyClient()
		callsBefore := executor.httpCalls
		for _, tc := range []struct {
			method, path string
			want         int
		}{
			{http.MethodGet, "/v1/realtime?model=gpt-realtime", http.StatusUpgradeRequired},
			{http.MethodGet, "/v1/realtime?call_id=call-1", http.StatusUpgradeRequired},
			{http.MethodGet, "/v1/realtime/calls/call-1", http.StatusUpgradeRequired},
			{http.MethodGet, "/v1/live/call-1", http.StatusUpgradeRequired},
			{http.MethodPost, "/v1/realtime/calls/call-1/hangup", http.StatusNotFound},
			{http.MethodPost, "/v1/realtime/sessions", http.StatusOK},
			{http.MethodPost, "/v1/realtime/client_secrets", http.StatusOK},
			// These answer 501 without choosing a credential, which is why pooled keys
			// may call them. A route that starts serving needs its credential selection
			// checked against pools before it stays in codexOnlyRoutes.
			{http.MethodPost, "/v1/realtime/transcription_sessions", http.StatusNotImplemented},
			{http.MethodGet, "/v1/realtime/translations", http.StatusNotImplemented},
			{http.MethodPost, "/v1/realtime/translations", http.StatusNotImplemented},
			{http.MethodPost, "/v1/realtime/translations/client_secrets", http.StatusNotImplemented},
			{http.MethodPost, "/v1/realtime/calls/call-1/accept", http.StatusNotImplemented},
			{http.MethodPost, "/v1/realtime/calls/call-1/reject", http.StatusNotImplemented},
			{http.MethodPost, "/v1/realtime/calls/call-1/refer", http.StatusNotImplemented},
		} {
			if recorder := serve(tc.method, tc.path, "sk-codex-test", ""); recorder.Code != tc.want {
				t.Errorf("%s %s: status=%d want=%d body=%s", tc.method, tc.path, recorder.Code, tc.want, recorder.Body.String())
			}
		}
		if executor.httpCalls != callsBefore {
			t.Fatalf("routes without a call reached the upstream with %v", executor.authIDs)
		}
	})

	t.Run("client secrets carry the minting key's pools", func(t *testing.T) {
		server.handlers.Cfg.Client = codexPoolKeyClient()
		mint := serve(http.MethodPost, "/v1/realtime/client_secrets", "sk-codex-test", `{"session":{"type":"realtime","model":"gpt-realtime"}}`)
		var minted struct {
			Value string `json:"value"`
		}
		if errUnmarshal := json.Unmarshal(mint.Body.Bytes(), &minted); mint.Code != http.StatusOK || errUnmarshal != nil || minted.Value == "" {
			t.Fatalf("mint: status=%d body=%s", mint.Code, mint.Body.String())
		}
		useSecret := func() (*httptest.ResponseRecorder, []string) {
			return selectedBy(http.MethodPost, "/v1/realtime/calls", minted.Value, "v=0\r\n")
		}
		if recorder, ids := useSecret(); recorder.Code != http.StatusOK || !slices.Equal(ids, []string{"codex-primary"}) {
			t.Fatalf("secret call: status=%d used=%v body=%s", recorder.Code, ids, recorder.Body.String())
		}
		// Each use resolves the minting key's current pools.
		pools := *server.handlers.Cfg.Client.AccountPools
		pools[0].AuthIDs = nil
		if recorder, ids := useSecret(); recorder.Code != http.StatusOK || !slices.Equal(ids, []string{"codex-shared"}) {
			t.Fatalf("secret call after the primary pool emptied: status=%d used=%v", recorder.Code, ids)
		}
		pools[1].AuthIDs = nil
		if recorder, ids := useSecret(); recorder.Code < http.StatusBadRequest || len(ids) != 0 {
			t.Fatalf("secret call with empty pools: status=%d used=%v", recorder.Code, ids)
		}
	})
}
