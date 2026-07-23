package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type codexSearchCaptureExecutor struct {
	request      *http.Request
	body         []byte
	authIDs      []string
	prepareErr   error
	httpErr      error
	responseBody io.ReadCloser
}

func (e *codexSearchCaptureExecutor) Identifier() string { return "codex" }

func (e *codexSearchCaptureExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *codexSearchCaptureExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

func (e *codexSearchCaptureExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *codexSearchCaptureExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *codexSearchCaptureExecutor) PrepareRequest(req *http.Request, a *auth.Auth) error {
	if e.prepareErr != nil {
		return e.prepareErr
	}
	token, _ := a.Metadata["access_token"].(string)
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

type codexSearchGinContextSelector struct {
	ginContext *gin.Context
}

func (s *codexSearchGinContextSelector) Pick(ctx context.Context, _ string, _ string, _ coreexecutor.Options, auths []*auth.Auth) (*auth.Auth, error) {
	s.ginContext, _ = ctx.Value("gin").(*gin.Context)
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[0], nil
}

type codexSearchAPIKeyFirstSelector struct{}

type codexSearchModelRouter struct {
	response pluginapi.ModelRouteResponse
	handled  bool
	requests []pluginapi.ModelRouteRequest
}

func (r *codexSearchModelRouter) RouteModel(_ context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	r.requests = append(r.requests, req)
	return r.response, r.handled
}

func (s *codexSearchAPIKeyFirstSelector) Pick(_ context.Context, _ string, _ string, _ coreexecutor.Options, auths []*auth.Auth) (*auth.Auth, error) {
	for _, candidate := range auths {
		if candidate.AuthKind() == auth.AuthKindAPIKey {
			return candidate, nil
		}
	}
	if len(auths) == 0 {
		return nil, nil
	}
	return auths[0], nil
}

func (e *codexSearchCaptureExecutor) HttpRequest(_ context.Context, selected *auth.Auth, req *http.Request) (*http.Response, error) {
	if e.httpErr != nil {
		return nil, e.httpErr
	}
	e.request = req.Clone(req.Context())
	e.authIDs = append(e.authIDs, selected.ID)
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	e.body = body
	responseBody := e.responseBody
	if responseBody == nil {
		responseBody = io.NopCloser(strings.NewReader(`{"results":[{"url":"https://example.com"}]}`))
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       responseBody,
	}, nil
}

type codexSearchHomeDispatcher struct {
	calls atomic.Int32
}

func (*codexSearchHomeDispatcher) HeartbeatOK() bool { return true }

func (d *codexSearchHomeDispatcher) RPopAuth(_ context.Context, model string, _ string, _ http.Header, _ int) ([]byte, error) {
	d.calls.Add(1)
	return json.Marshal(map[string]any{
		"model":      model,
		"auth_index": "home-codex-search",
		"auth": map[string]any{
			"id":       "home-codex-search",
			"provider": "codex",
			"status":   "active",
			"metadata": map[string]any{"access_token": "home-search-token"},
		},
		"concurrency": map[string]any{
			"accounted":     true,
			"credential_id": "home-codex-search",
			"model":         model,
		},
	})
}

func (*codexSearchHomeDispatcher) AbortAmbiguousDispatch() {}

type codexSearchBusyHomeDispatcher struct{}

func (*codexSearchBusyHomeDispatcher) HeartbeatOK() bool { return true }
func (*codexSearchBusyHomeDispatcher) RPopAuth(context.Context, string, string, http.Header, int) ([]byte, error) {
	return []byte(`{"error":{"type":"credential_concurrency_exceeded","message":"busy","retry_after_ms":750}}`), nil
}
func (*codexSearchBusyHomeDispatcher) AbortAmbiguousDispatch() {}

type trackedSearchResponseBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *trackedSearchResponseBody) Close() error {
	b.closed.Store(true)
	return nil
}

type drainAwareSearchResponseBody struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newDrainAwareSearchResponseBody() *drainAwareSearchResponseBody {
	return &drainAwareSearchResponseBody{started: make(chan struct{}), closed: make(chan struct{})}
}

func (b *drainAwareSearchResponseBody) Read([]byte) (int, error) {
	b.startOnce.Do(func() { close(b.started) })
	<-b.closed
	return 0, io.EOF
}

func (b *drainAwareSearchResponseBody) Close() error {
	b.closeOnce.Do(func() { close(b.closed) })
	return nil
}

func TestAuditHomeBusyNormalAndStream429Headers(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "stream"}[stream], func(t *testing.T) {
			server := newTestServer(t)
			server.handlers.AuthManager.SetConfig(&proxyconfig.Config{Home: proxyconfig.HomeConfig{Enabled: true}})
			server.handlers.AuthManager.PublishHomeDispatch(&codexSearchBusyHomeDispatcher{}, executionregistry.New(), 1)

			body := `{"model":"gpt-5-codex","input":[]}`
			if stream {
				body = `{"model":"gpt-5-codex","input":[],"stream":true}`
			}
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer test-key")
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
			}
			if got := rr.Header().Get("Retry-After"); got != "1" {
				t.Fatalf("Retry-After = %q, want 1", got)
			}
		})
	}
}

func TestAuditHomeCodexSearchBusyReturnsTrustedRetryAfter(t *testing.T) {
	server := newTestServer(t)
	server.handlers.AuthManager.SetConfig(&proxyconfig.Config{Home: proxyconfig.HomeConfig{Enabled: true}})
	server.handlers.AuthManager.PublishHomeDispatch(&codexSearchBusyHomeDispatcher{}, executionregistry.New(), 1)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"model":"gpt-5-codex","query":"test"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusTooManyRequests, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got != "1" {
		t.Fatalf("Retry-After = %q, want 1", got)
	}
	if !strings.Contains(rr.Body.String(), "busy") {
		t.Fatalf("body = %q, want busy error", rr.Body.String())
	}
}

func TestAuditHomeCodexSearchBodyCloseBeforeRelease(t *testing.T) {
	server := newTestServer(t)
	dispatcher := &codexSearchHomeDispatcher{}
	registry := executionregistry.New()
	body := newDrainAwareSearchResponseBody()
	var releaseAfterBodyClose atomic.Bool
	var releaseCount atomic.Int32
	registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) {
		if group != (executionregistry.ReleaseGroup{CredentialID: "home-codex-search", Model: "gpt-5-codex"}) {
			t.Errorf("release group = %#v", group)
		}
		select {
		case <-body.closed:
			releaseAfterBodyClose.Store(true)
		default:
		}
		releaseCount.Add(1)
	})
	server.handlers.AuthManager.SetConfig(&proxyconfig.Config{Home: proxyconfig.HomeConfig{Enabled: true}})
	server.handlers.AuthManager.PublishHomeDispatch(dispatcher, registry, 1)
	executor := &codexSearchCaptureExecutor{responseBody: body}
	server.handlers.AuthManager.RegisterExecutor(executor)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"id":"home-search-drain","model":"gpt-5-codex","query":"test"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	handlerDone := make(chan struct{})
	go func() {
		server.engine.ServeHTTP(rr, req)
		close(handlerDone)
	}()

	select {
	case <-body.started:
	case <-time.After(time.Second):
		t.Fatal("search handler did not start reading the response body")
	}
	if errDrain := registry.Drain(context.Background()); errDrain != nil {
		t.Fatalf("Drain() error = %v", errDrain)
	}
	if got := releaseCount.Load(); got != 1 {
		t.Fatalf("accounted releases = %d, want 1", got)
	}
	if !releaseAfterBodyClose.Load() {
		t.Fatal("accounted Home selection released before the search response body closed")
	}
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("search handler remained blocked after Home drain")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestHomeCodexAlphaSearchEndsSelectionAcrossDirectHTTPPaths(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*codexSearchCaptureExecutor, *trackedSearchResponseBody)
		wantStatus int
		wantClosed bool
	}{
		{
			name: "request build failure",
			configure: func(executor *codexSearchCaptureExecutor, _ *trackedSearchResponseBody) {
				executor.prepareErr = errors.New("request preparation failed")
			},
			wantStatus: http.StatusBadGateway,
		},
		{
			name: "HTTP error",
			configure: func(executor *codexSearchCaptureExecutor, _ *trackedSearchResponseBody) {
				executor.httpErr = errors.New("upstream unavailable")
			},
			wantStatus: http.StatusBadGateway,
		},
		{
			name: "response body close",
			configure: func(executor *codexSearchCaptureExecutor, body *trackedSearchResponseBody) {
				executor.responseBody = body
			},
			wantStatus: http.StatusOK,
			wantClosed: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newTestServer(t)
			dispatcher := &codexSearchHomeDispatcher{}
			registry := executionregistry.New()
			server.handlers.AuthManager.SetConfig(&proxyconfig.Config{Home: proxyconfig.HomeConfig{Enabled: true}})
			server.handlers.AuthManager.PublishHomeDispatch(dispatcher, registry, 1)
			body := &trackedSearchResponseBody{Reader: strings.NewReader(`{"results":[]}`)}
			executor := &codexSearchCaptureExecutor{}
			test.configure(executor, body)
			server.handlers.AuthManager.RegisterExecutor(executor)

			req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"id":"home-search-session","model":"gpt-5-codex","query":"test"}`))
			req.Header.Set("Authorization", "Bearer test-key")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)
			if rr.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, test.wantStatus, rr.Body.String())
			}
			if got := dispatcher.calls.Load(); got != 1 {
				t.Fatalf("Home RPOP calls = %d, want 1", got)
			}
			if got := body.closed.Load(); got != test.wantClosed {
				t.Fatalf("response body closed = %t, want %t", got, test.wantClosed)
			}
			if errDrain := registry.Drain(context.Background()); errDrain != nil {
				t.Fatalf("Drain() error = %v", errDrain)
			}
		})
	}
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerWithOptions(t)
}

func newTestServerWithOptions(t *testing.T, opts ...ServerOption) *Server {
	t.Helper()

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := auth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	return NewServer(cfg, authManager, accessManager, configPath, opts...)
}

func TestHealthz(t *testing.T) {
	server := newTestServer(t)

	t.Run("GET", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Status != "ok" {
			t.Fatalf("unexpected response status: got %q want %q", resp.Status, "ok")
		}
	})

	t.Run("HEAD", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/healthz", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("unexpected status code: got %d want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("expected empty body for HEAD request, got %q", rr.Body.String())
		}
	})
}

func TestCodexAlphaSearchForwardsRequest(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token", "account_id": "account-123"},
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
		t.Fatalf("register Codex auth: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"GPT-5.6"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session_id", "session-123")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if executor.request == nil {
		t.Fatal("Codex executor did not receive a request")
	}
	if got, want := executor.request.URL.String(), "https://chatgpt.com/backend-api/codex/alpha/search"; got != want {
		t.Fatalf("upstream URL = %q, want %q", got, want)
	}
	if got, want := string(executor.body), `{"query":"GPT-5.6"}`; got != want {
		t.Fatalf("upstream body = %q, want %q", got, want)
	}
	if got := executor.request.Header.Get("Authorization"); got != "Bearer codex-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := executor.request.Header.Get("Chatgpt-Account-Id"); got != "account-123" {
		t.Fatalf("Chatgpt-Account-Id = %q", got)
	}
	if got := executor.request.Header.Get("Session_id"); got != "session-123" {
		t.Fatalf("Session_id = %q", got)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("response Content-Type = %q", got)
	}
	traceID := rr.Header().Get(internallogging.CPATraceIDHeader)
	parts := strings.Split(traceID, "-")
	if len(parts) != 3 || parts[1] != credential.Index || len(parts[2]) != 8 {
		t.Fatalf("trace ID = %q, want timestamp-%s-requestID", traceID, credential.Index)
	}
	if _, errParse := time.Parse("20060102150405", parts[0]); errParse != nil {
		t.Fatalf("trace timestamp = %q: %v", parts[0], errParse)
	}
}

func TestCodexAlphaSearchUsesPluginProviderTargetModel(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	router := &codexSearchModelRouter{
		response: pluginapi.ModelRouteResponse{
			Handled:     true,
			TargetKind:  pluginapi.ModelRouteTargetProvider,
			Target:      "codex",
			TargetModel: "team-b/gpt-5.6-sol",
		},
		handled: true,
	}
	server.handlers.SetModelRouterHost(router)

	for _, credential := range []*auth.Auth{
		{
			ID:       "codex-team-a",
			Provider: "codex",
			Prefix:   "team-a",
			Status:   auth.StatusActive,
			Metadata: map[string]any{"access_token": "token-a"},
		},
		{
			ID:       "codex-team-b",
			Provider: "codex",
			Prefix:   "team-b",
			Status:   auth.StatusActive,
			Metadata: map[string]any{"access_token": "token-b"},
		},
	} {
		if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
			t.Fatalf("register Codex auth %s: %v", credential.ID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: credential.Prefix + "/gpt-5.6-sol"}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(credential.ID)
		})
	}

	payload := `{"id":"session-123","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"golang"}]}}`
	paths := []string{"/v1/alpha/search?key=test-key", "/backend-api/codex/alpha/search?key=test-key"}
	for _, path := range paths {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d; body=%s", path, rr.Code, http.StatusOK, rr.Body.String())
		}
	}

	if got, want := executor.authIDs, []string{"codex-team-b", "codex-team-b"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("selected auth IDs = %v, want %v", got, want)
	}
	if got := string(executor.body); got != payload {
		t.Fatalf("upstream body = %q, want original unprefixed body %q", got, payload)
	}
	if got, want := len(router.requests), 2; got != want {
		t.Fatalf("model router requests = %d, want %d", got, want)
	}
	for index, routeReq := range router.requests {
		if routeReq.SourceFormat != "codex-alpha-search" {
			t.Fatalf("model router source format = %q", routeReq.SourceFormat)
		}
		if routeReq.RequestedModel != "gpt-5.6-sol" {
			t.Fatalf("model router requested model = %q", routeReq.RequestedModel)
		}
		if got := routeReq.Headers.Get("Authorization"); got != "Bearer test-key" {
			t.Fatalf("model router Authorization = %q", got)
		}
		if got := routeReq.Query.Get("key"); got != "test-key" {
			t.Fatalf("model router query key = %q", got)
		}
		if got, want := routeReq.Metadata[coreexecutor.RequestPathMetadataKey], strings.SplitN(paths[index], "?", 2)[0]; got != want {
			t.Fatalf("model router request path = %#v, want %q", got, want)
		}
		if got := string(routeReq.Body); got != payload {
			t.Fatalf("model router body = %q, want %q", got, payload)
		}
	}
}

func TestCodexAlphaSearchFallsBackWhenPluginDoesNotHandleRoute(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token"},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Codex auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(credential.ID)
	})
	router := &codexSearchModelRouter{}
	server.handlers.SetModelRouterHost(router)

	payload := `{"model":"gpt-5.6-sol"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(payload))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if got := executor.authIDs; len(got) != 1 || got[0] != credential.ID {
		t.Fatalf("selected auth IDs = %v, want [%s]", got, credential.ID)
	}
	if got := string(executor.body); got != payload {
		t.Fatalf("upstream body = %q, want %q", got, payload)
	}
	if got := len(router.requests); got != 1 {
		t.Fatalf("model router requests = %d, want 1", got)
	}
}

func TestCodexAlphaSearchRejectsUnsupportedPluginRouteTarget(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token"},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Codex auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(credential.ID)
	})
	server.handlers.SetModelRouterHost(&codexSearchModelRouter{
		response: pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
			Target:     "user-routing",
		},
		handled: true,
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"model":"gpt-5.6-sol"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	if executor.request != nil {
		t.Fatal("unsupported plugin route sent an upstream request")
	}
}

func TestCodexAlphaSearchSanitizesResponsesOnlyFields(t *testing.T) {
	server := newTestServer(t)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token"},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Codex auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: "gpt-5.6-sol"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(credential.ID)
	})

	payload := `{"id":"session-123","model":"gpt-5.6-sol","commands":{"search_query":[{"q":"golang channels"}]},"prompt_cache_key":"cache-123","prompt_cache_retention":"24h"}`
	for _, path := range []string{"/v1/alpha/search", "/backend-api/codex/alpha/search"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
			req.Header.Set("Authorization", "Bearer test-key")
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}
			var upstreamBody map[string]json.RawMessage
			if errUnmarshal := json.Unmarshal(executor.body, &upstreamBody); errUnmarshal != nil {
				t.Fatalf("unmarshal upstream body: %v; body=%s", errUnmarshal, executor.body)
			}
			if _, exists := upstreamBody["prompt_cache_key"]; exists {
				t.Fatalf("upstream body contains prompt_cache_key: %s", executor.body)
			}
			if _, exists := upstreamBody["prompt_cache_retention"]; exists {
				t.Fatalf("upstream body contains prompt_cache_retention: %s", executor.body)
			}
			for _, field := range []string{"id", "model", "commands"} {
				if _, exists := upstreamBody[field]; !exists {
					t.Fatalf("upstream body missing %s: %s", field, executor.body)
				}
			}
		})
	}
}

func TestCodexAlphaSearchRequiresOAuthCredential(t *testing.T) {
	newServer := func(t *testing.T, credentials ...*auth.Auth) (*Server, *codexSearchCaptureExecutor) {
		t.Helper()
		server := newTestServer(t)
		server.handlers.AuthManager.SetSelector(&codexSearchAPIKeyFirstSelector{})
		executor := &codexSearchCaptureExecutor{}
		server.handlers.AuthManager.RegisterExecutor(executor)
		for _, credential := range credentials {
			if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
				t.Fatalf("register Codex auth %s: %v", credential.ID, errRegister)
			}
		}
		return server, executor
	}
	apiKeyCredential := func() *auth.Auth {
		return &auth.Auth{
			ID:         "codex-api-key",
			Provider:   "codex",
			Status:     auth.StatusActive,
			Attributes: map[string]string{auth.AttributeAPIKey: "codex-key"},
		}
	}
	oauthCredential := func() *auth.Auth {
		return &auth.Auth{
			ID:       "codex-oauth",
			Provider: "codex",
			Status:   auth.StatusActive,
			Metadata: map[string]any{"access_token": "codex-token"},
		}
	}

	t.Run("mixed credentials", func(t *testing.T) {
		server, executor := newServer(t, apiKeyCredential(), oauthCredential())
		req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"GPT-5.6"}`))
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if got := executor.authIDs; len(got) != 1 || got[0] != "codex-oauth" {
			t.Fatalf("selected auth IDs = %v, want [codex-oauth]", got)
		}
	})

	t.Run("API key only", func(t *testing.T) {
		server, executor := newServer(t, apiKeyCredential())
		req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"GPT-5.6"}`))
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)

		if rr.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
		}
		if len(executor.authIDs) != 0 {
			t.Fatalf("selected auth IDs = %v, want none", executor.authIDs)
		}
	})
}

func TestCodexAlphaSearchPassesGinContextToAuthSelection(t *testing.T) {
	server := newTestServer(t)
	selector := &codexSearchGinContextSelector{}
	server.handlers.AuthManager.SetSelector(selector)
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token"},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Codex auth: %v", errRegister)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search?key=home-query-key", strings.NewReader(`{"query":"GPT-5.6"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	if selector.ginContext == nil {
		t.Fatal("auth selection did not receive the Gin context required by Home scheduling")
	}
	if got := selector.ginContext.Query("key"); got != "home-query-key" {
		t.Fatalf("Gin query key = %q, want %q", got, "home-query-key")
	}
}

func TestCodexAlphaSearchUsesRequestIDForSessionAffinity(t *testing.T) {
	server := newTestServer(t)
	server.handlers.AuthManager.SetSelector(auth.NewSessionAffinitySelector(&auth.RoundRobinSelector{}))
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	for _, id := range []string{"codex-auth-a", "codex-auth-b"} {
		registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: "gpt-5.6-luna"}})
		t.Cleanup(func() {
			registry.GetGlobalRegistry().UnregisterClient(id)
		})
		credential := &auth.Auth{
			ID:       id,
			Provider: "codex",
			Status:   auth.StatusActive,
			Metadata: map[string]any{"access_token": id},
		}
		if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
			t.Fatalf("register Codex auth: %v", errRegister)
		}
	}

	for _, payload := range []string{
		`{"id":"session-a","model":"gpt-5.6-luna"}`,
		`{"id":"session-b","model":"gpt-5.6-luna"}`,
		`{"id":"session-a","model":"gpt-5.6-luna"}`,
	} {
		req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer test-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
	}

	if got, want := len(executor.authIDs), 3; got != want {
		t.Fatalf("selected auth count = %d, want %d", got, want)
	}
	if executor.authIDs[0] == executor.authIDs[1] {
		t.Fatalf("different sessions selected the same auth %q", executor.authIDs[0])
	}
	if got, want := executor.authIDs[2], executor.authIDs[0]; got != want {
		t.Fatalf("session-affinity auth = %q, want %q", got, want)
	}
}

func TestCodexAlphaSearchRecordsRequestLog(t *testing.T) {
	server := newTestServer(t)
	server.cfg.RequestLog = true

	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token", "account_id": "account-123"},
	}
	if _, err := server.handlers.AuthManager.Register(context.Background(), credential); err != nil {
		t.Fatalf("register Codex auth: %v", err)
	}

	rr := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rr)
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"GPT-5.6"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	c.Request = req

	server.codexAlphaSearch(c)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	rawAPIRequest, okRequest := c.Get("API_REQUEST")
	if !okRequest {
		t.Fatal("API_REQUEST was not captured")
	}
	apiRequest, _ := rawAPIRequest.([]byte)
	if !strings.Contains(string(apiRequest), "=== API REQUEST 1 ===") {
		t.Fatalf("API_REQUEST missing request header section: %q", apiRequest)
	}
	if !strings.Contains(string(apiRequest), "https://chatgpt.com/backend-api/codex/alpha/search") {
		t.Fatalf("API_REQUEST missing upstream URL: %q", apiRequest)
	}
	if !strings.Contains(string(apiRequest), `{"query":"GPT-5.6"}`) {
		t.Fatalf("API_REQUEST missing body: %q", apiRequest)
	}
	rawAPIResponse, okResponse := c.Get("API_RESPONSE")
	if !okResponse {
		t.Fatal("API_RESPONSE was not captured")
	}
	apiResponse, _ := rawAPIResponse.([]byte)
	if !strings.Contains(string(apiResponse), "=== API RESPONSE 1 ===") {
		t.Fatalf("API_RESPONSE missing response header section: %q", apiResponse)
	}
	if !strings.Contains(string(apiResponse), `{"results":[{"url":"https://example.com"}]}`) {
		t.Fatalf("API_RESPONSE missing body: %q", apiResponse)
	}
}

func TestManagementResponseExposesPluginSupportHeaderForCORS(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
	req.Header.Set("Origin", "http://127.0.0.1:5173")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusUnauthorized, rr.Body.String())
	}
	if got := rr.Header().Get("X-CPA-SUPPORT-PLUGIN"); got != pluginhost.SupportPluginHeaderValue() {
		t.Fatalf("X-CPA-SUPPORT-PLUGIN = %q, want %q", got, pluginhost.SupportPluginHeaderValue())
	}

	exposedHeaders := make(map[string]struct{})
	for _, headerName := range strings.Split(rr.Header().Get("Access-Control-Expose-Headers"), ",") {
		headerName = strings.ToLower(strings.TrimSpace(headerName))
		if headerName != "" {
			exposedHeaders[headerName] = struct{}{}
		}
	}
	for _, headerName := range corsExposedResponseHeaders {
		if _, ok := exposedHeaders[strings.ToLower(headerName)]; !ok {
			t.Fatalf("Access-Control-Expose-Headers missing %s: %q", headerName, rr.Header().Get("Access-Control-Expose-Headers"))
		}
	}
}

func TestOAuthCallbackRouteSkipsManagementKeyMiddleware(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	state := "server-plugin-oauth-state"
	if errRegister := managementHandlers.RegisterPluginOAuthSession(state, "gemini-cli", nil); errRegister != nil {
		t.Fatalf("register plugin oauth session: %v", errRegister)
	}
	defer managementHandlers.CompleteOAuthSession(state)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/oauth-callback?state="+state+"&code=test-code", nil)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	callbackPath := filepath.Join(server.cfg.AuthDir, ".oauth-gemini-cli-"+state+".oauth")
	if _, errRead := os.ReadFile(callbackPath); errRead != nil {
		t.Fatalf("expected callback file to be written without management key: %v", errRead)
	}
}

func TestNewServerWithPluginHostInjectsHandlerInterceptors(t *testing.T) {
	host := pluginhost.New()
	server := newTestServerWithOptions(t, WithPluginHost(host))

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	got, ok := server.handlers.PluginHost.(*pluginhost.Host)
	if !ok || got != host {
		t.Fatalf("handler plugin host = %#v, want configured host", server.handlers.PluginHost)
	}
}

func TestNewServerWithoutPluginHostLeavesHandlerInterceptorsDisabled(t *testing.T) {
	server := newTestServer(t)

	if server.handlers == nil {
		t.Fatal("server handlers = nil")
	}
	if server.handlers.PluginHost != nil {
		t.Fatalf("handler plugin host = %#v, want nil", server.handlers.PluginHost)
	}
}

func TestManagementUsageRequiresManagementAuthAndPopsArray(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	t.Cleanup(func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	})

	server := newTestServer(t)

	redisqueue.Enqueue([]byte(`{"id":1}`))
	redisqueue.Enqueue([]byte(`{"id":2}`))

	missingKeyReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)
	missingKeyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(missingKeyRR, missingKeyReq)
	if missingKeyRR.Code != http.StatusUnauthorized {
		t.Fatalf("missing key status = %d, want %d body=%s", missingKeyRR.Code, http.StatusUnauthorized, missingKeyRR.Body.String())
	}

	legacyReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage?count=2", nil)
	legacyReq.Header.Set("Authorization", "Bearer test-management-key")
	legacyRR := httptest.NewRecorder()
	server.engine.ServeHTTP(legacyRR, legacyReq)
	if legacyRR.Code != http.StatusNotFound {
		t.Fatalf("legacy usage status = %d, want %d body=%s", legacyRR.Code, http.StatusNotFound, legacyRR.Body.String())
	}

	authReq := httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)
	authReq.Header.Set("Authorization", "Bearer test-management-key")
	authRR := httptest.NewRecorder()
	server.engine.ServeHTTP(authRR, authReq)
	if authRR.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, want %d body=%s", authRR.Code, http.StatusOK, authRR.Body.String())
	}

	var payload []json.RawMessage
	if errUnmarshal := json.Unmarshal(authRR.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v body=%s", errUnmarshal, authRR.Body.String())
	}
	if len(payload) != 2 {
		t.Fatalf("response records = %d, want 2", len(payload))
	}
	for i, raw := range payload {
		var record struct {
			ID int `json:"id"`
		}
		if errUnmarshal := json.Unmarshal(raw, &record); errUnmarshal != nil {
			t.Fatalf("unmarshal record %d: %v", i, errUnmarshal)
		}
		if record.ID != i+1 {
			t.Fatalf("record %d id = %d, want %d", i, record.ID, i+1)
		}
	}

	if remaining := redisqueue.PopOldest(1); len(remaining) != 0 {
		t.Fatalf("remaining queue = %q, want empty", remaining)
	}
}

func TestManagementPluginsRouteRegistered(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	enabled := true
	server.cfg.Plugins.Configs = map[string]proxyconfig.PluginInstanceConfig{
		"sample": {Enabled: &enabled, Priority: 4},
	}
	if errWrite := os.WriteFile(server.configFilePath, []byte("{}\n"), 0o600); errWrite != nil {
		t.Fatalf("failed to write config file: %v", errWrite)
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var payload struct {
		PluginsEnabled bool  `json:"plugins_enabled"`
		Plugins        []any `json:"plugins"`
	}
	if errUnmarshal := json.Unmarshal(rr.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v body=%s", errUnmarshal, rr.Body.String())
	}
	if payload.Plugins == nil {
		t.Fatalf("plugins field = nil, want array; body=%s", rr.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/v0/management/plugins/sample/config", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rr = httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("config status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	var configPayload struct {
		Enabled  bool `json:"enabled"`
		Priority int  `json:"priority"`
	}
	if errUnmarshal := json.Unmarshal(rr.Body.Bytes(), &configPayload); errUnmarshal != nil {
		t.Fatalf("unmarshal config response: %v body=%s", errUnmarshal, rr.Body.String())
	}
	if !configPayload.Enabled || configPayload.Priority != 4 {
		t.Fatalf("plugin config = %#v, want enabled true priority 4", configPayload)
	}

	req = httptest.NewRequest(http.MethodDelete, "/v0/management/plugins/sample", nil)
	req.Header.Set("Authorization", "Bearer test-management-key")
	rr = httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
}

func TestVideosRoutesKeepXAINativeAndExposeOpenAIPrefix(t *testing.T) {
	server := newTestServer(t)

	nativeReq := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"sora-2","prompt":"make a video"}`))
	nativeReq.Header.Set("Authorization", "Bearer test-key")
	nativeReq.Header.Set("Content-Type", "application/json")
	nativeRR := httptest.NewRecorder()
	server.engine.ServeHTTP(nativeRR, nativeReq)
	if nativeRR.Code != http.StatusBadRequest {
		t.Fatalf("native status = %d, want %d body=%s", nativeRR.Code, http.StatusBadRequest, nativeRR.Body.String())
	}
	if !strings.Contains(nativeRR.Body.String(), "/v1/videos/generations") {
		t.Fatalf("expected /v1/videos to keep xAI native validation, body=%s", nativeRR.Body.String())
	}

	openAIReq := httptest.NewRequest(http.MethodPost, "/openai/v1/videos", strings.NewReader(`{"model":`))
	openAIReq.Header.Set("Authorization", "Bearer test-key")
	openAIReq.Header.Set("Content-Type", "application/json")
	openAIRR := httptest.NewRecorder()
	server.engine.ServeHTTP(openAIRR, openAIReq)
	if openAIRR.Code != http.StatusBadRequest {
		t.Fatalf("openai create status = %d, want %d body=%s", openAIRR.Code, http.StatusBadRequest, openAIRR.Body.String())
	}
	if !strings.Contains(openAIRR.Body.String(), "body must be valid JSON") {
		t.Fatalf("expected /openai/v1/videos create handler, body=%s", openAIRR.Body.String())
	}

	contentReq := httptest.NewRequest(http.MethodGet, "/openai/v1/videos/video_123/content?variant=thumbnail", nil)
	contentReq.Header.Set("Authorization", "Bearer test-key")
	contentRR := httptest.NewRecorder()
	server.engine.ServeHTTP(contentRR, contentReq)
	if contentRR.Code != http.StatusBadRequest {
		t.Fatalf("content status = %d, want %d body=%s", contentRR.Code, http.StatusBadRequest, contentRR.Body.String())
	}
	if !strings.Contains(contentRR.Body.String(), "variant") {
		t.Fatalf("expected /openai/v1/videos content handler, body=%s", contentRR.Body.String())
	}
}

func TestHomeEnabledHidesManagementEndpointsAndControlPanel(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")

	server := newTestServer(t)
	server.cfg.Home.Enabled = true

	t.Run("management endpoints return 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		req.Header.Set("Authorization", "Bearer test-management-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
		}
	})

	t.Run("management control panel returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/management.html", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusNotFound, rr.Body.String())
		}
	})
}

func TestExampleAPIKeySafeModeShowsWarningAndKeepsManagement(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	staticDir := t.TempDir()
	t.Setenv("MANAGEMENT_STATIC_PATH", staticDir)
	if err := os.WriteFile(filepath.Join(staticDir, "management.html"), []byte("<html>management app</html>"), 0o600); err != nil {
		t.Fatalf("failed to write management asset: %v", err)
	}

	server := newTestServerWithOptions(t, WithExampleAPIKeySafeMode())
	cfg := *server.cfg
	cfg.APIKeys = []string{"your-api-key-1"}
	server.UpdateClients(&cfg)

	t.Run("root warning page includes management link", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		body := rr.Body.String()
		for _, want := range []string{"Example API key detected", "Open Management", `href="/management.html?safe-mode=configure"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("warning page missing %q: %s", want, body)
			}
		}
	})

	t.Run("management html defaults to warning page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/management.html", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "Example API key detected") {
			t.Fatalf("management.html did not show warning page: %s", rr.Body.String())
		}
	})

	t.Run("management html head stops at warning page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/management.html", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("HEAD body length = %d, want 0", rr.Body.Len())
		}
		if got := rr.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want no-store", got)
		}
	})

	t.Run("management button query opens control panel", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/management.html?safe-mode=configure", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "management app") {
			t.Fatalf("management panel body missing: %s", rr.Body.String())
		}
	})

	t.Run("proxy endpoints are blocked", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusForbidden, rr.Body.String())
		}
		if got := rr.Header().Get("X-CPA-SAFE-MODE"); got != "example-api-key" {
			t.Fatalf("X-CPA-SAFE-MODE = %q, want example-api-key", got)
		}
		if !strings.Contains(rr.Body.String(), "unsafe_example_api_key") {
			t.Fatalf("body missing safe-mode error: %s", rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "management_url") {
			t.Fatalf("body should not include management_url field: %s", rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "/management.html?safe-mode=configure") {
			t.Fatalf("body missing management link in message: %s", rr.Body.String())
		}
		if got := rr.Header().Get(internallogging.CPATraceIDHeader); got != "" {
			t.Fatalf("trace ID = %q, want empty before auth selection", got)
		}
	})

	t.Run("management endpoints still work", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v0/management/config", nil)
		req.Header.Set("Authorization", "Bearer test-management-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}
		if got := rr.Header().Get(internallogging.CPATraceIDHeader); got != "" {
			t.Fatalf("management trace ID = %q, want empty", got)
		}
	})

	t.Run("safe mode clears after key update", func(t *testing.T) {
		nextCfg := cfg
		nextCfg.APIKeys = []string{"real-key"}
		server.UpdateClients(&nextCfg)

		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer real-key")
		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code == http.StatusForbidden && strings.Contains(rr.Body.String(), "unsafe_example_api_key") {
			t.Fatalf("proxy endpoint still blocked after key update: %s", rr.Body.String())
		}
	})
}

func TestModelsDispatchByAnthropicVersionHeader(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-anthropic-version-dispatch"
	modelRegistry.RegisterClient(clientID, "claude", []*registry.ModelInfo{
		{
			ID:                  "claude-sonnet-4-6",
			Object:              "model",
			OwnedBy:             "anthropic",
			Type:                "claude",
			DisplayName:         "Claude 4.6 Sonnet",
			ContextLength:       200000,
			MaxCompletionTokens: 64000,
		},
		{
			ID:      "gpt-4o",
			Object:  "model",
			OwnedBy: "openai",
			Type:    "openai",
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	server := newTestServer(t)

	// Anthropic API request (Anthropic-Version header, non-claude-cli User-Agent) -> Claude format.
	t.Run("anthropic version header routes to claude format", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("User-Agent", "Zed/1.0")
		req.Header.Set("Anthropic-Version", "2023-06-01")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Object  string           `json:"object"`
			HasMore *bool            `json:"has_more"`
			Data    []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Object == "list" {
			t.Fatalf("expected Claude format (no object=list), got OpenAI format: %s", rr.Body.String())
		}
		if resp.HasMore == nil {
			t.Fatalf("expected Claude envelope with has_more, got %s", rr.Body.String())
		}

		var claudeModel map[string]any
		var rewrittenModel map[string]any
		for _, m := range resp.Data {
			id, _ := m["id"].(string)
			switch id {
			case "claude-sonnet-4-6":
				claudeModel = m
			case "claude-fable-5-dd-o4-tpg":
				rewrittenModel = m
			case "gpt-4o", "claude-gpt-4o":
				t.Fatalf("expected non-claude model id to be rewritten as claude-fable-5-dd-<reversed>, got %q", id)
			}
		}
		if claudeModel == nil {
			t.Fatalf("expected claude-sonnet-4-6 in response, got %s", rr.Body.String())
		}
		if rewrittenModel == nil {
			t.Fatalf("expected claude-fable-5-dd-o4-tpg in response, got %s", rr.Body.String())
		}
		for _, field := range []string{"max_input_tokens", "max_tokens", "display_name"} {
			if _, ok := claudeModel[field]; !ok {
				t.Fatalf("expected Claude model to include %q, got %v", field, claudeModel)
			}
		}
	})

	// Plain request (no Anthropic-Version, non-claude-cli User-Agent) -> OpenAI format, unaffected.
	t.Run("plain request stays on openai format", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("User-Agent", "Mozilla/5.0")

		rr := httptest.NewRecorder()
		server.engine.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
		}

		var resp struct {
			Object string           `json:"object"`
			Data   []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
		}
		if resp.Object != "list" {
			t.Fatalf("expected OpenAI format (object=list), got %s", rr.Body.String())
		}
		foundRawGPT := false
		for _, m := range resp.Data {
			if _, ok := m["max_input_tokens"]; ok {
				t.Fatalf("did not expect max_input_tokens in OpenAI format, got %v", m)
			}
			if id, _ := m["id"].(string); id == "gpt-4o" {
				foundRawGPT = true
			}
			if id, _ := m["id"].(string); id == "claude-gpt-4o" || id == "claude-fable-5-dd-o4-tpg" {
				t.Fatalf("did not expect Anthropic id rewrite on OpenAI format models, got %v", m)
			}
		}
		if !foundRawGPT {
			t.Fatalf("expected raw gpt-4o in OpenAI format response, got %s", rr.Body.String())
		}
	})
}

func TestModelsWithClientVersionReturnsCodexCatalog(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-client-version-catalog"
	modelRegistry.RegisterClient(clientID, "openai", []*registry.ModelInfo{
		{
			ID:            "gpt-5.5",
			Object:        "model",
			Created:       1776902400,
			OwnedBy:       "openai",
			Type:          "openai",
			DisplayName:   "GPT 5.5",
			Description:   "Frontier model for complex coding, research, and real-world work.",
			ContextLength: 272000,
			Thinking:      &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
		},
		{
			ID:            "custom-codex-model-test",
			Object:        "model",
			OwnedBy:       "test",
			Type:          "openai",
			DisplayName:   "Custom Codex Model",
			Description:   "Custom model from registry",
			ContextLength: 123456,
			Thinking:      &registry.ThinkingSupport{Levels: []string{"none", "minimal", "low", "medium", "unsupported", "high", "xhigh"}},
		},
		{ID: "grok-imagine-image-quality", Object: "model", OwnedBy: "xai", Type: "openai"},
		{ID: "gpt-image-2", Object: "model", OwnedBy: "openai", Type: "openai"},
		{ID: "grok-imagine-image", Object: "model", OwnedBy: "xai", Type: "openai"},
		{ID: "grok-imagine-video", Object: "model", OwnedBy: "xai", Type: "openai"},
		{ID: "grok-imagine-video-1.5-preview", Object: "model", OwnedBy: "xai", Type: "openai"},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models?client_version", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("User-Agent", "claude-cli/1.0")

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}

	var resp struct {
		Models []map[string]any `json:"models"`
		Object string           `json:"object"`
		Data   []any            `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response JSON: %v; body=%s", err, rr.Body.String())
	}
	if resp.Object != "" || resp.Data != nil {
		t.Fatalf("expected codex catalog format without object/data, got object=%q data=%v", resp.Object, resp.Data)
	}
	if len(resp.Models) == 0 {
		t.Fatal("expected codex catalog models")
	}

	var gpt55 map[string]any
	var custom map[string]any
	for _, model := range resp.Models {
		switch slug, _ := model["slug"].(string); slug {
		case "gpt-5.5":
			gpt55 = model
		case "custom-codex-model-test":
			custom = model
		}
	}
	if gpt55 == nil {
		t.Fatal("expected gpt-5.5 codex catalog entry")
	}
	if _, ok := gpt55["minimal_client_version"]; !ok {
		t.Fatal("expected minimal_client_version in codex catalog")
	}
	serviceTiers, ok := gpt55["service_tiers"].([]any)
	if !ok || len(serviceTiers) != 1 {
		t.Fatalf("expected gpt-5.5 priority service tier, got %#v", gpt55["service_tiers"])
	}
	if custom == nil {
		t.Fatal("expected custom model codex catalog entry")
	}
	if got, _ := custom["display_name"].(string); got != "Custom Codex Model" {
		t.Fatalf("custom display_name = %q, want Custom Codex Model", got)
	}
	wantCustomPriority := codexClientTestMaxTemplatePriority(t) + 100
	if got := int(codexClientTestPriority(custom["priority"])); got != wantCustomPriority {
		t.Fatalf("custom priority = %v, want %d", custom["priority"], wantCustomPriority)
	}
	if got, _ := custom["description"].(string); got != "Custom model from registry" {
		t.Fatalf("custom description = %q, want Custom model from registry", got)
	}
	if got, _ := custom["context_window"].(float64); got != 123456 {
		t.Fatalf("custom context_window = %v, want 123456", custom["context_window"])
	}
	assertCodexSupportedReasoningLevels(t, custom, []string{"none", "low", "medium", "high", "xhigh"})
	if custom["base_instructions"] != gpt55["base_instructions"] {
		t.Fatal("expected custom model to use gpt-5.5 base_instructions fallback")
	}
	if _, ok := custom["available_in_plans"].([]any); !ok {
		t.Fatalf("expected custom model to use gpt-5.5 available_in_plans fallback, got %#v", custom["available_in_plans"])
	}
	if got, _ := custom["prefer_websockets"].(bool); got {
		t.Fatalf("custom prefer_websockets = %v, want false", custom["prefer_websockets"])
	}
	customServiceTiers, ok := custom["service_tiers"].([]any)
	if !ok || len(customServiceTiers) != 0 {
		t.Fatalf("expected custom model service_tiers = [], got %#v", custom["service_tiers"])
	}
	if _, ok := custom["apply_patch_tool_type"]; ok {
		t.Fatal("expected custom model to omit apply_patch_tool_type")
	}
	if _, ok := custom["upgrade"]; ok {
		t.Fatal("expected custom model to omit upgrade")
	}
	if _, ok := custom["availability_nux"]; ok {
		t.Fatal("expected custom model to omit availability_nux")
	}

	hiddenModels := map[string]bool{
		"grok-imagine-image-quality":     false,
		"gpt-image-2":                    false,
		"grok-imagine-image":             false,
		"grok-imagine-video":             false,
		"grok-imagine-video-1.5-preview": false,
	}
	for _, model := range resp.Models {
		slug, _ := model["slug"].(string)
		if _, ok := hiddenModels[slug]; !ok {
			continue
		}
		if visibility, _ := model["visibility"].(string); visibility != "hide" {
			t.Fatalf("%s visibility = %q, want hide", slug, visibility)
		}
		hiddenModels[slug] = true
	}
	for slug, found := range hiddenModels {
		if !found {
			t.Fatalf("expected hidden model %s in codex catalog", slug)
		}
	}
}

func codexClientTestPriority(raw any) int {
	switch value := raw.(type) {
	case int:
		return value
	case float64:
		return int(value)
	default:
		return -1
	}
}

func codexClientTestMaxTemplatePriority(t *testing.T) int {
	t.Helper()
	var payload struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(registry.GetCodexClientModelsJSON(), &payload); err != nil {
		t.Fatalf("parse Codex client model templates: %v", err)
	}
	maxPriority := 0
	for _, model := range payload.Models {
		if priority := codexClientTestPriority(model["priority"]); priority > maxPriority {
			maxPriority = priority
		}
	}
	return maxPriority
}

func assertCodexSupportedReasoningLevels(t *testing.T, model map[string]any, want []string) {
	t.Helper()

	rawLevels, ok := model["supported_reasoning_levels"].([]any)
	if !ok {
		t.Fatalf("expected supported_reasoning_levels, got %#v", model["supported_reasoning_levels"])
	}
	if len(rawLevels) != len(want) {
		t.Fatalf("supported_reasoning_levels length = %d, want %d: %#v", len(rawLevels), len(want), rawLevels)
	}
	for index, rawLevel := range rawLevels {
		levelEntry, ok := rawLevel.(map[string]any)
		if !ok {
			t.Fatalf("supported_reasoning_levels[%d] = %#v, want object", index, rawLevel)
		}
		if got, _ := levelEntry["effort"].(string); got != want[index] {
			t.Fatalf("supported_reasoning_levels[%d].effort = %q, want %q", index, got, want[index])
		}
	}
}

func TestDefaultRequestLoggerFactory_UsesResolvedLogDirectory(t *testing.T) {
	t.Setenv("WRITABLE_PATH", "")
	t.Setenv("writable_path", "")

	originalWD, errGetwd := os.Getwd()
	if errGetwd != nil {
		t.Fatalf("failed to get current working directory: %v", errGetwd)
	}

	tmpDir := t.TempDir()
	if errChdir := os.Chdir(tmpDir); errChdir != nil {
		t.Fatalf("failed to switch working directory: %v", errChdir)
	}
	defer func() {
		if errChdirBack := os.Chdir(originalWD); errChdirBack != nil {
			t.Fatalf("failed to restore working directory: %v", errChdirBack)
		}
	}()

	// Force ResolveLogDirectory to fallback to auth-dir/logs by making ./logs not a writable directory.
	if errWriteFile := os.WriteFile(filepath.Join(tmpDir, "logs"), []byte("not-a-directory"), 0o644); errWriteFile != nil {
		t.Fatalf("failed to create blocking logs file: %v", errWriteFile)
	}

	configDir := filepath.Join(tmpDir, "config")
	if errMkdirConfig := os.MkdirAll(configDir, 0o755); errMkdirConfig != nil {
		t.Fatalf("failed to create config dir: %v", errMkdirConfig)
	}
	configPath := filepath.Join(configDir, "config.yaml")

	authDir := filepath.Join(tmpDir, "auth")
	if errMkdirAuth := os.MkdirAll(authDir, 0o700); errMkdirAuth != nil {
		t.Fatalf("failed to create auth dir: %v", errMkdirAuth)
	}

	cfg := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog: false,
		},
		AuthDir:           authDir,
		ErrorLogsMaxFiles: 10,
	}

	logger := defaultRequestLoggerFactory(cfg, configPath)
	fileLogger, ok := logger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected *FileRequestLogger, got %T", logger)
	}

	errLog := fileLogger.LogRequestWithOptions(
		"/v1/chat/completions",
		http.MethodPost,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"input":"hello"}`),
		http.StatusBadGateway,
		map[string][]string{"Content-Type": []string{"application/json"}},
		[]byte(`{"error":"upstream failure"}`),
		nil,
		nil,
		nil,
		nil,
		nil,
		true,
		"issue-1711",
		time.Now(),
		time.Now(),
	)
	if errLog != nil {
		t.Fatalf("failed to write forced error request log: %v", errLog)
	}

	authLogsDir := filepath.Join(authDir, "logs")
	authEntries, errReadAuthDir := os.ReadDir(authLogsDir)
	if errReadAuthDir != nil {
		t.Fatalf("failed to read auth logs dir %s: %v", authLogsDir, errReadAuthDir)
	}
	foundErrorLogInAuthDir := false
	for _, entry := range authEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			foundErrorLogInAuthDir = true
			break
		}
	}
	if !foundErrorLogInAuthDir {
		t.Fatalf("expected forced error log in auth fallback dir %s, got entries: %+v", authLogsDir, authEntries)
	}

	configLogsDir := filepath.Join(configDir, "logs")
	configEntries, errReadConfigDir := os.ReadDir(configLogsDir)
	if errReadConfigDir != nil && !os.IsNotExist(errReadConfigDir) {
		t.Fatalf("failed to inspect config logs dir %s: %v", configLogsDir, errReadConfigDir)
	}
	for _, entry := range configEntries {
		if strings.HasPrefix(entry.Name(), "error-") && strings.HasSuffix(entry.Name(), ".log") {
			t.Fatalf("unexpected forced error log in config dir %s", configLogsDir)
		}
	}
}

func TestFormatHomeClaudeModelIncludesAnthropicSchemaFields(t *testing.T) {
	withMetadata := formatHomeClaudeModel(homeModelEntry{
		id:                  "claude-sonnet-4-6",
		created:             1771372800,
		ownedBy:             "anthropic",
		displayName:         "Claude 4.6 Sonnet",
		contextLength:       200000,
		maxCompletionTokens: 64000,
	})
	if got := withMetadata["created_at"]; got != "2026-02-18T00:00:00Z" {
		t.Fatalf("created_at = %v, want RFC3339 timestamp", got)
	}
	if got := withMetadata["type"]; got != "model" {
		t.Fatalf("type = %v, want model", got)
	}
	if got := withMetadata["display_name"]; got != "Claude 4.6 Sonnet" {
		t.Fatalf("display_name = %v, want Claude 4.6 Sonnet", got)
	}
	if got := withMetadata["max_input_tokens"]; got != 200000 {
		t.Fatalf("max_input_tokens = %v, want 200000", got)
	}
	if got := withMetadata["max_tokens"]; got != 64000 {
		t.Fatalf("max_tokens = %v, want 64000", got)
	}

	withDefaults := formatHomeClaudeModel(homeModelEntry{id: "claude-no-limits"})
	if got := withDefaults["display_name"]; got != "claude-no-limits" {
		t.Fatalf("display_name fallback = %v, want claude-no-limits", got)
	}

	prefixed := formatHomeClaudeModel(homeModelEntry{id: "gpt-4o", displayName: "GPT-4o"})
	if got := prefixed["id"]; got != "claude-fable-5-dd-o4-tpg" {
		t.Fatalf("id = %v, want claude-fable-5-dd-o4-tpg", got)
	}
	if got := prefixed["display_name"]; got != "GPT-4o" {
		t.Fatalf("display_name = %v, want GPT-4o", got)
	}
	if got := withDefaults["max_input_tokens"]; got != registry.DefaultClaudeMaxInputTokens {
		t.Fatalf("max_input_tokens fallback = %v, want %d", got, registry.DefaultClaudeMaxInputTokens)
	}
	if got := withDefaults["max_tokens"]; got != registry.DefaultClaudeMaxOutputTokens {
		t.Fatalf("max_tokens fallback = %v, want %d", got, registry.DefaultClaudeMaxOutputTokens)
	}
	if _, ok := withDefaults["created_at"]; ok {
		t.Fatalf("created_at should be omitted when source created is missing, got %v", withDefaults)
	}
}

func TestFormatHomeClaudeModelsSortsByDisplayName(t *testing.T) {
	out := formatHomeClaudeModels([]homeModelEntry{
		{id: "claude-z", displayName: "Zebra"},
		{id: "gpt-4o", displayName: "Alpha"},
		{id: "claude-b", displayName: "Beta"},
	})
	if len(out) != 3 {
		t.Fatalf("len(out) = %d, want 3", len(out))
	}
	wantNames := []string{"Alpha", "Beta", "Zebra"}
	for i, want := range wantNames {
		got, _ := out[i]["display_name"].(string)
		if got != want {
			t.Fatalf("out[%d].display_name = %q, want %q", i, got, want)
		}
	}
}

func TestDecodeHomeModelsKeepsTokenMetadata(t *testing.T) {
	entries, errDecode := decodeHomeModels([]byte(`{
		"claude": [
			{
				"id": "claude-sonnet-4-6",
				"created": 1771372800,
				"owned_by": "anthropic",
				"context_length": 200000,
				"max_completion_tokens": 64000
			}
		],
		"gemini": [
			{
				"name": "models/gemini-3-pro",
				"inputTokenLimit": 1048576,
				"outputTokenLimit": 65536
			}
		]
	}`))
	if errDecode != nil {
		t.Fatalf("decodeHomeModels returned error: %v", errDecode)
	}

	byID := make(map[string]homeModelEntry, len(entries))
	for _, entry := range entries {
		byID[entry.id] = entry
	}
	claudeEntry, ok := byID["claude-sonnet-4-6"]
	if !ok {
		t.Fatalf("expected claude-sonnet-4-6 entry, got %v", byID)
	}
	if claudeEntry.contextLength != 200000 || claudeEntry.maxCompletionTokens != 64000 {
		t.Fatalf("claude token metadata = %d/%d, want 200000/64000", claudeEntry.contextLength, claudeEntry.maxCompletionTokens)
	}
	geminiEntry, ok := byID["gemini-3-pro"]
	if !ok {
		t.Fatalf("expected gemini-3-pro entry, got %v", byID)
	}
	if geminiEntry.contextLength != 1048576 || geminiEntry.maxCompletionTokens != 65536 {
		t.Fatalf("gemini token metadata = %d/%d, want 1048576/65536", geminiEntry.contextLength, geminiEntry.maxCompletionTokens)
	}
}

func TestHomeModelsAuthStatus(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantStatus  int
		wantHandled bool
	}{
		{"no credentials", `{"error":{"type":"no_credentials","message":"Missing API key"}}`, http.StatusUnauthorized, true},
		{"invalid credential", `{"error":{"type":"invalid_credential","message":"Invalid API key"}}`, http.StatusUnauthorized, true},
		{"internal error maps to bad gateway", `{"error":{"type":"internal_error","message":"boom"}}`, http.StatusBadGateway, true},
		{"models payload not an error", `{"openai":[{"id":"gpt-5.5"}]}`, 0, false},
		{"empty payload not an error", `{}`, 0, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, handled := homeModelsAuthStatus([]byte(tc.raw))
			if handled != tc.wantHandled {
				t.Fatalf("handled = %v, want %v (status=%d)", handled, tc.wantHandled, status)
			}
			if handled && status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
		})
	}
}

func TestHomeModelsErrorMessage(t *testing.T) {
	if msg := homeModelsErrorMessage([]byte(`{"error":{"type":"invalid_credential","message":"Invalid API key"}}`)); msg != "Invalid API key" {
		t.Fatalf("message = %q, want %q", msg, "Invalid API key")
	}
	if msg := homeModelsErrorMessage([]byte(`{"openai":[]}`)); msg != "home models request failed" {
		t.Fatalf("default message = %q, want fallback", msg)
	}
}

func TestInteractionsRouteRegistered(t *testing.T) {
	server := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/v1beta/interactions", strings.NewReader(`{"model":"gemini-3.5-flash","input":"hi"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)
	if rr.Code == http.StatusNotFound {
		t.Fatalf("status = %d, want route registered; body=%s", rr.Code, rr.Body.String())
	}
}
