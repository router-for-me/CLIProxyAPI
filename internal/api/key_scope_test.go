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

// codexKeyScopeClient scopes one key to Codex, one to Codex through an alias,
// and three to providers that never serve Codex web search or voice.
func codexKeyScopeClient() config.ClientConfig {
	return config.ClientConfig{KeyScopes: &[]config.ClientKeyScope{
		{KeyPrefix: "sk-codex-", Providers: []string{"codex"}},
		{KeyPrefix: "sk-openai-", Providers: []string{"openai"}},
		{KeyPrefix: "sk-claude-", Providers: []string{"claude"}},
		{KeyPrefix: "sk-muse-", Providers: []string{"meta"}},
		{KeyPrefix: "sk-grok-", Providers: []string{"xai"}},
	}}
}

func codexRoutePath(pattern string) string {
	return strings.ReplaceAll(pattern, ":call_id", "call-1")
}

func keyScopeErrorCode(body []byte) string {
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return ""
	}
	return payload.Error.Code
}

func TestKeyScopeMiddlewareOnCodexRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sdkCfg := &config.SDKConfig{Client: codexKeyScopeClient()}
	homeManager := auth.NewManager(nil, nil, nil)
	homeManager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	for _, mode := range []struct {
		name    string
		manager *auth.Manager
		want    map[string]int
	}{
		{name: "local", want: map[string]int{
			"sk-codex-test": 204, "sk-openai-test": 204, "legacy-key": 204,
			"sk-claude-test": 403, "sk-muse-test": 403, "sk-grok-test": 403,
		}},
		// Key scopes do not apply in Home mode.
		{name: "home", manager: homeManager, want: map[string]int{
			"sk-codex-test": 204, "sk-openai-test": 204, "legacy-key": 204,
			"sk-claude-test": 204, "sk-muse-test": 204, "sk-grok-test": 204,
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
			router.POST("/v1/responses", noContent)
			serve := func(method, path, key string) *httptest.ResponseRecorder {
				request := httptest.NewRequest(method, path, nil)
				request.Header.Set("Authorization", "Bearer "+key)
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				return recorder
			}
			for route := range codexOnlyRoutes {
				method, pattern, _ := strings.Cut(route, " ")
				for key, want := range mode.want {
					recorder := serve(method, codexRoutePath(pattern), key)
					if recorder.Code != want {
						t.Errorf("%s with %s: status=%d want=%d body=%s", route, key, recorder.Code, want, recorder.Body.String())
						continue
					}
					if want == http.StatusForbidden && keyScopeErrorCode(recorder.Body.Bytes()) != "provider_not_allowed" {
						t.Errorf("%s with %s: body=%s, want code provider_not_allowed", route, key, recorder.Body.String())
					}
				}
			}
			// Model routes are scoped when the model is resolved, after the middleware.
			if recorder := serve(http.MethodPost, "/v1/responses", "sk-muse-test"); recorder.Code != http.StatusNoContent {
				t.Errorf("Meta key on /v1/responses: status=%d", recorder.Code)
			}
		})
	}
}

func TestCodexOnlyRoutesMatchRegisteredRoutes(t *testing.T) {
	server := newTestServer(t)
	registered := make(map[string]bool)
	for _, route := range server.engine.Routes() {
		key := route.Method + " " + route.Path
		registered[key] = true
		usesCodex := strings.HasPrefix(route.Path, "/v1/realtime") || strings.HasPrefix(route.Path, "/v1/live") || strings.HasSuffix(route.Path, "/alpha/search")
		if usesCodex && !codexOnlyRoutes[key] {
			t.Errorf("%s always uses a Codex credential but is missing from codexOnlyRoutes", key)
		}
	}
	for key := range codexOnlyRoutes {
		if !registered[key] {
			t.Errorf("codexOnlyRoutes lists %s, which the server does not register", key)
		}
	}
}

// TestVoiceAndSearchKeyScopesOnServer runs the real route table, so it also
// fails if a Codex-only route loses keyScopeMiddleware. It never completes a
// WebSocket upgrade: those handlers dial the Codex upstream directly.
func TestVoiceAndSearchKeyScopesOnServer(t *testing.T) {
	keys := []string{"sk-codex-test", "sk-openai-test", "sk-claude-test", "sk-muse-test", "sk-grok-test", "legacy-key"}
	server := newTestServerWithConfig(t, &config.Config{SDKConfig: config.SDKConfig{APIKeys: keys}})
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
		ID:       "codex-login",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-login-token"},
	}); errRegister != nil {
		t.Fatal(errRegister)
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

	t.Run("keys without Codex are refused on every voice and search route", func(t *testing.T) {
		server.handlers.Cfg.Client = codexKeyScopeClient()
		for route := range codexOnlyRoutes {
			method, pattern, _ := strings.Cut(route, " ")
			for _, key := range []string{"sk-claude-test", "sk-muse-test", "sk-grok-test"} {
				recorder := serve(method, codexRoutePath(pattern), key, "")
				if recorder.Code != http.StatusForbidden || keyScopeErrorCode(recorder.Body.Bytes()) != "provider_not_allowed" {
					t.Errorf("%s with %s: status=%d body=%s", route, key, recorder.Code, recorder.Body.String())
				}
			}
		}
		if executor.httpCalls != 0 {
			t.Fatalf("refused keys reached the upstream %d times with %v", executor.httpCalls, executor.authIDs)
		}
	})

	t.Run("Codex keys and unscoped keys reach voice and search", func(t *testing.T) {
		server.handlers.Cfg.Client = codexKeyScopeClient()
		for _, key := range []string{"sk-codex-test", "sk-openai-test", "legacy-key"} {
			for _, route := range callRoutes {
				recorder, ids := selectedBy(http.MethodPost, route.path, key, route.body)
				if recorder.Code != http.StatusOK || !slices.Equal(ids, []string{"codex-login"}) {
					t.Errorf("%s with %s: status=%d used=%v body=%s", route.path, key, recorder.Code, ids, recorder.Body.String())
				}
			}
		}
	})

	t.Run("client secrets carry the minting key's scope", func(t *testing.T) {
		server.handlers.Cfg.Client = codexKeyScopeClient()
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
		if recorder, ids := useSecret(); recorder.Code != http.StatusOK || !slices.Equal(ids, []string{"codex-login"}) {
			t.Fatalf("secret call: status=%d used=%v body=%s", recorder.Code, ids, recorder.Body.String())
		}
		// Each use resolves the minting key's current scope.
		(*server.handlers.Cfg.Client.KeyScopes)[0].Providers = []string{"meta"}
		if recorder, ids := useSecret(); recorder.Code != http.StatusForbidden || len(ids) != 0 || keyScopeErrorCode(recorder.Body.Bytes()) != "provider_not_allowed" {
			t.Fatalf("secret call after the key lost Codex: status=%d used=%v body=%s", recorder.Code, ids, recorder.Body.String())
		}
	})
}
