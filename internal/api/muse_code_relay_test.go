package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// museRelayCaptureExecutor stands in for the Meta executor and records the
// request the relay sends upstream.
type museRelayCaptureExecutor struct {
	request      *http.Request
	body         []byte
	calls        int
	mints        int
	refreshes    int
	status       int
	responseBody string
	// revokedKey, when set, is answered with 401 like a revoked Meta API key.
	revokedKey string
}

// ShouldPrepareRequestAuth mirrors the Meta executor: a login holding only a
// DCA token needs its API key minted first.
func (e *museRelayCaptureExecutor) ShouldPrepareRequestAuth(a *auth.Auth) bool {
	return a != nil && a.Attributes["api_key"] == "" && a.Attributes["dca_token"] != ""
}

func (e *museRelayCaptureExecutor) PrepareRequestAuth(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	e.mints++
	a.Attributes["api_key"] = "minted-key"
	a.Attributes["base_url"] = "https://minted.example.com/v1"
	return a, nil
}

func (e *museRelayCaptureExecutor) Identifier() string { return "meta" }

func (e *museRelayCaptureExecutor) Execute(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *museRelayCaptureExecutor) ExecuteStream(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, nil
}

// Refresh mirrors the Meta executor: a login with a DCA token mints a new key.
func (e *museRelayCaptureExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	if a != nil && a.Attributes["dca_token"] != "" {
		e.refreshes++
		a.Attributes["api_key"] = "fresh-key"
	}
	return a, nil
}

func (e *museRelayCaptureExecutor) CountTokens(context.Context, *auth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, nil
}

func (e *museRelayCaptureExecutor) PrepareRequest(req *http.Request, a *auth.Auth) error {
	req.Header.Set("Authorization", "Bearer "+a.Attributes["api_key"])
	return nil
}

func (e *museRelayCaptureExecutor) HttpRequest(_ context.Context, _ *auth.Auth, req *http.Request) (*http.Response, error) {
	e.calls++
	e.request = req.Clone(req.Context())
	if req.Body != nil {
		body, errRead := io.ReadAll(req.Body)
		if errRead != nil {
			return nil, errRead
		}
		e.body = body
	}
	status := e.status
	if status == 0 {
		status = http.StatusOK
	}
	if e.revokedKey != "" && req.Header.Get("Authorization") == "Bearer "+e.revokedKey {
		status = http.StatusUnauthorized
	}
	responseBody := e.responseBody
	if responseBody == "" {
		responseBody = `{"rows":[{"model_id":"muse-test"}]}`
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "Etag": []string{`"catalog-1"`}, "Set-Cookie": []string{"session=upstream"}},
		Body:       io.NopCloser(strings.NewReader(responseBody)),
	}, nil
}

func museRelayTestServer(t *testing.T, attributes map[string]string) (*Server, *museRelayCaptureExecutor) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		t.Fatalf("create auth dir: %v", errMkdir)
	}
	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"sk-muse-test", "plain-key"},
		},
	}
	cfg.AuthDir = authDir
	cfg.LoggingToFile = false
	cfg.UsageStatisticsEnabled = false

	server := NewServer(cfg, auth.NewManager(nil, nil, nil), sdkaccess.NewManager(), filepath.Join(tmpDir, "config.yaml"))
	executor := &museRelayCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{ID: "meta-auth", Provider: "meta", Status: auth.StatusActive, Attributes: attributes}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Meta auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, "meta", []*registry.ModelInfo{{ID: "muse-relay-test", Object: "model", OwnedBy: "meta", Type: "meta"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
	return server, executor
}

func TestMuseCodeRelayForwardsCatalogRequest(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key", "base_url": "https://meta.example.com/v1"})

	req := httptest.NewRequest(http.MethodGet, "/muse-code/models?profile=tbh", nil)
	req.Header.Set("Authorization", "Bearer sk-muse-test")
	req.Header.Set("X-Tbh-Session-Id", "session-1")
	req.Header.Set("Cookie", "client=cookie")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if executor.request == nil {
		t.Fatal("the Meta executor did not receive a request")
	}
	if got, want := executor.request.URL.String(), "https://meta.example.com/muse-code/models?profile=tbh"; got != want {
		t.Fatalf("upstream URL = %q, want %q", got, want)
	}
	if got := executor.request.Method; got != http.MethodGet {
		t.Fatalf("upstream method = %q, want GET", got)
	}
	if got := executor.request.Header.Get("Authorization"); got != "Bearer meta-key" {
		t.Fatalf("upstream Authorization = %q, want the Meta credential", got)
	}
	if got := executor.request.Header.Get("X-Tbh-Session-Id"); got != "session-1" {
		t.Fatalf("X-Tbh-Session-Id = %q, want it forwarded", got)
	}
	if got := executor.request.Header.Get("Cookie"); got != "" {
		t.Fatalf("Cookie = %q, want it dropped", got)
	}
	if got := recorder.Body.String(); got != `{"rows":[{"model_id":"muse-test"}]}` {
		t.Fatalf("body = %s, want the upstream body unchanged", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("Etag"); got != `"catalog-1"` {
		t.Fatalf("Etag = %q, want it relayed", got)
	}
	if got := recorder.Header().Get("Set-Cookie"); got != "" {
		t.Fatalf("Set-Cookie = %q, want it dropped", got)
	}
}

func TestMuseCodeRelayPostsToTheDefaultOrigin(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key"})
	executor.status = http.StatusTooManyRequests

	req := httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(`{"query":"muse"}`))
	req.Header.Set("Authorization", "Bearer plain-key")
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want the upstream status 429", recorder.Code)
	}
	if got, want := executor.request.URL.String(), "https://api.meta.ai/muse-code/search"; got != want {
		t.Fatalf("upstream URL = %q, want %q", got, want)
	}
	if got := executor.request.Method; got != http.MethodPost {
		t.Fatalf("upstream method = %q, want POST", got)
	}
	if got := string(executor.body); got != `{"query":"muse"}` {
		t.Fatalf("upstream body = %q", got)
	}
	if got := executor.request.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("upstream Content-Type = %q", got)
	}
}

func TestMuseCodeRelayRequiresKeyAndAllowlist(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key"})

	for _, testCase := range []struct {
		name   string
		method string
		path   string
		key    string
		status int
	}{
		{name: "unknown key", method: http.MethodGet, path: "/muse-code/models", key: "wrong-key", status: http.StatusUnauthorized},
		{name: "credential minting is not relayed", method: http.MethodPost, path: "/muse-code/key", key: "sk-muse-test", status: http.StatusNotFound},
		{name: "telemetry is not relayed", method: http.MethodPost, path: "/muse-code/telemetry/logs", key: "sk-muse-test", status: http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			req := httptest.NewRequest(testCase.method, testCase.path, nil)
			req.Header.Set("Authorization", "Bearer "+testCase.key)
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, req)
			if recorder.Code != testCase.status {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, testCase.status, recorder.Body.String())
			}
		})
	}
	if executor.calls != 0 {
		t.Fatalf("the Meta executor received %d request(s), want none", executor.calls)
	}
}

func TestMuseCodeRelayRejectsOversizedBodies(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key"})
	oversized := strings.Repeat("a", museCodeRelayMaxBody+1)

	req := httptest.NewRequest(http.MethodPost, "/muse-code/search", strings.NewReader(oversized))
	req.Header.Set("Authorization", "Bearer plain-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized request: status = %d, want 413", recorder.Code)
	}
	if executor.calls != 0 {
		t.Fatal("an oversized request reached Meta")
	}

	executor.responseBody = oversized
	req = httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	req.Header.Set("Authorization", "Bearer plain-key")
	recorder = httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("oversized response: status = %d, want 502", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "aaaa") {
		t.Fatal("part of an oversized response was relayed")
	}
}

func TestMuseCodeRelayRefusesPlainlyWhileHomeIsEnabled(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key"})
	homeConfig := &proxyconfig.Config{}
	homeConfig.Home.Enabled = true
	server.handlers.AuthManager.SetConfig(homeConfig)

	req := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	req.Header.Set("Authorization", "Bearer plain-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "Muse Code endpoints are unavailable while Home is enabled") {
		t.Fatalf("status = %d, body = %s; want the Home refusal", recorder.Code, recorder.Body.String())
	}
	if executor.calls != 0 {
		t.Fatal("the Meta executor was called while Home is enabled")
	}
}

func TestMuseCodeRelayDropsProxyKeyFromQuery(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key", "base_url": "https://meta.example.com/v1"})

	for _, query := range []string{"profile=tbh&key=plain-key", "auth_token=plain-key&profile=tbh"} {
		req := httptest.NewRequest(http.MethodGet, "/muse-code/models?"+query, nil)
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200 (the query key authenticates); body=%s", query, recorder.Code, recorder.Body.String())
		}
		if got, want := executor.request.URL.String(), "https://meta.example.com/muse-code/models?profile=tbh"; got != want {
			t.Fatalf("%s: upstream URL = %q, want %q without the proxy key", query, got, want)
		}
	}
}

func TestMuseCodeRelaySkipsMetaCredentialsThatServeNoModel(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key"})
	// A config key turned off with a "*" exclusion is left with no models.
	disabled := &auth.Auth{ID: "meta-disabled", Provider: "meta", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "disabled-key", "excluded_models": "*"}}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), disabled); errRegister != nil {
		t.Fatalf("register disabled Meta auth: %v", errRegister)
	}

	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
		req.Header.Set("Authorization", "Bearer plain-key")
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, body=%s", i, recorder.Code, recorder.Body.String())
		}
		if got := executor.request.Header.Get("Authorization"); got != "Bearer meta-key" {
			t.Fatalf("request %d went out with %q, want the credential that serves models", i, got)
		}
	}

	registry.GetGlobalRegistry().UnregisterClient("meta-auth")
	executor.calls = 0
	req := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	req.Header.Set("Authorization", "Bearer plain-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusServiceUnavailable || executor.calls != 0 {
		t.Fatalf("with no credential serving a model: status = %d, Meta calls = %d; want 503 and none", recorder.Code, executor.calls)
	}
}

func TestMuseCodeRelayMintsDCAKeyBeforeChoosingOrigin(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"dca_token": "dca:test", "base_url": "https://stale.example.com/v1"})

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
		req.Header.Set("Authorization", "Bearer plain-key")
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, body=%s", i, recorder.Code, recorder.Body.String())
		}
		if got, want := executor.request.URL.String(), "https://minted.example.com/muse-code/models"; got != want {
			t.Fatalf("request %d: upstream URL = %q, want the origin the mint named", i, got)
		}
		if got := executor.request.Header.Get("Authorization"); got != "Bearer minted-key" {
			t.Fatalf("request %d: Authorization = %q, want the minted key", i, got)
		}
	}
	if executor.mints != 1 {
		t.Fatalf("mints = %d over two requests, want 1: the minted key must be saved", executor.mints)
	}
}

func TestMuseCodeRelayRefreshesARevokedKeyOnce(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "revoked-key", "dca_token": "dca:test"})
	executor.revokedKey = "revoked-key"

	req := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	req.Header.Set("Authorization", "Bearer plain-key")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after refreshing the revoked key; body=%s", recorder.Code, recorder.Body.String())
	}
	if executor.calls != 2 || executor.refreshes != 1 {
		t.Fatalf("Meta calls = %d, refreshes = %d; want 2 and 1", executor.calls, executor.refreshes)
	}
	if got := executor.request.Header.Get("Authorization"); got != "Bearer fresh-key" {
		t.Fatalf("retry Authorization = %q, want the refreshed key", got)
	}
}

func TestMuseCodeRelayRetiresARejectedCredential(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key"})
	executor.revokedKey = "dead-key"
	// A config key Meta rejects, with nothing to refresh it from, beside a good one.
	dead := &auth.Auth{ID: "meta-dead", Provider: "meta", Status: auth.StatusActive, Attributes: map[string]string{"api_key": "dead-key"}}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), dead); errRegister != nil {
		t.Fatalf("register rejected Meta auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(dead.ID, "meta", []*registry.ModelInfo{{ID: "muse-relay-test", Object: "model", OwnedBy: "meta", Type: "meta"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(dead.ID) })

	failures := 0
	for i := 0; i < 8; i++ {
		req := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
		req.Header.Set("Authorization", "Bearer plain-key")
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			failures++
		}
	}
	if failures > 1 {
		t.Fatalf("%d of 8 requests failed; want the rejected credential retired after its first 401", failures)
	}
}

func TestMuseCodeRelayIsNotHiddenByACoolingModel(t *testing.T) {
	server, executor := museRelayTestServer(t, map[string]string{"api_key": "meta-key"})
	// A second credential serves only a model that is cooling down on it.
	cooling := &auth.Auth{
		ID: "meta-cooling", Provider: "meta", Status: auth.StatusActive,
		Attributes: map[string]string{"api_key": "cooling-key"},
		ModelStates: map[string]*auth.ModelState{"muse-cooling": {
			Status: auth.StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour),
		}},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), cooling); errRegister != nil {
		t.Fatalf("register cooling Meta auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(cooling.ID, "meta", []*registry.ModelInfo{{ID: "muse-cooling", Object: "model", OwnedBy: "meta", Type: "meta"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(cooling.ID) })

	// The registry lists the two models in no fixed order, so send enough
	// requests that the cooling model comes first in some of them.
	for i := 0; i < 16; i++ {
		req := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
		req.Header.Set("Authorization", "Bearer plain-key")
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200 through the healthy credential; body=%s", i, recorder.Code, recorder.Body.String())
		}
		if got := executor.request.Header.Get("Authorization"); got != "Bearer meta-key" {
			t.Fatalf("request %d went out with %q, want the healthy credential", i, got)
		}
	}
}

func TestMuseCodeRelayIsBlockedInExampleKeySafeMode(t *testing.T) {
	server := newTestServerWithOptions(t, WithExampleAPIKeySafeMode())
	cfg := *server.cfg
	cfg.APIKeys = []string{"your-api-key-1"}
	server.UpdateClients(&cfg)

	req := httptest.NewRequest(http.MethodGet, "/muse-code/models", nil)
	req.Header.Set("Authorization", "Bearer your-api-key-1")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden || recorder.Header().Get("X-CPA-SAFE-MODE") != "example-api-key" {
		t.Fatalf("status = %d, safe-mode header = %q; want 403 from example-key safe mode", recorder.Code, recorder.Header().Get("X-CPA-SAFE-MODE"))
	}
}

func TestMuseCodeUpstreamURL(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		selected *auth.Auth
		want     string
		wantErr  bool
	}{
		{name: "no base URL uses Meta", selected: &auth.Auth{}, want: "https://api.meta.ai/muse-code/models?a=1"},
		{name: "attribute base URL", selected: &auth.Auth{Attributes: map[string]string{"base_url": "http://127.0.0.1:9000/custom/v1/"}}, want: "http://127.0.0.1:9000/muse-code/models?a=1"},
		{name: "metadata base URL", selected: &auth.Auth{Metadata: map[string]any{"api_base_url": "https://meta.example.com/v1"}}, want: "https://meta.example.com/muse-code/models?a=1"},
		{name: "base URL without a host", selected: &auth.Auth{Attributes: map[string]string{"base_url": "not a url"}}, wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, errURL := museCodeUpstreamURL(testCase.selected, "/muse-code/models", "a=1")
			if (errURL != nil) != testCase.wantErr {
				t.Fatalf("error = %v, wantErr %t", errURL, testCase.wantErr)
			}
			if got != testCase.want {
				t.Fatalf("URL = %q, want %q", got, testCase.want)
			}
		})
	}
}
