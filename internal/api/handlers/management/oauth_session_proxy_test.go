package management

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/antigravity"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type proxyTestCodexOAuthService struct {
	client   *http.Client
	tokenURL string
}

func (s *proxyTestCodexOAuthService) GenerateAuthURL(state string, _ *codex.PKCECodes) (string, error) {
	return "https://auth.example.test/oauth?state=" + state, nil
}

func (s *proxyTestCodexOAuthService) ExchangeCodeForTokens(ctx context.Context, code string, _ *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader("code="+url.QueryEscape(code)))
	if errReq != nil {
		return nil, errReq
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	body, errRead := io.ReadAll(resp.Body)
	if errRead != nil {
		return nil, errRead
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &antigravity.HTTPStatusError{StatusCodeValue: resp.StatusCode, Message: string(body)}
	}
	var payload struct {
		Email string `json:"email"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Email == "" {
		payload.Email = "codex-" + code + "@example.test"
	}
	now := time.Now()
	return &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      makeOAuthTestJWT("plus"),
			AccessToken:  "access-" + code,
			RefreshToken: "refresh-" + code,
			AccountID:    "acc-" + code,
			Email:        payload.Email,
			PlanType:     "plus",
			Expire:       now.Add(time.Hour).Format(time.RFC3339),
		},
		LastRefresh: now.Format(time.RFC3339),
	}, nil
}

func (s *proxyTestCodexOAuthService) CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage {
	return &codex.CodexTokenStorage{
		IDToken:      bundle.TokenData.IDToken,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
		PlanType:     bundle.TokenData.PlanType,
	}
}

func startForwardHTTPProxy(t *testing.T, hits *atomic.Int32, expectedProxyAuth string, dropBody bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if expectedProxyAuth != "" {
			if r.Header.Get("Proxy-Authorization") != expectedProxyAuth {
				w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
				http.Error(w, "proxy auth required", http.StatusProxyAuthRequired)
				return
			}
		}
		if dropBody {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, errHijack := hj.Hijack()
				if errHijack == nil {
					_ = conn.Close()
					return
				}
			}
			http.Error(w, "proxy dropped connection", http.StatusBadGateway)
			return
		}
		outReq, errReq := http.NewRequestWithContext(r.Context(), r.Method, r.RequestURI, r.Body)
		if errReq != nil {
			http.Error(w, errReq.Error(), http.StatusBadGateway)
			return
		}
		outReq.Header = r.Header.Clone()
		outReq.Header.Del("Proxy-Authorization")
		outReq.Header.Set("X-Via-Session-Proxy", "1")
		resp, errDo := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(outReq)
		if errDo != nil {
			http.Error(w, errDo.Error(), http.StatusBadGateway)
			return
		}
		defer func() {
			_ = resp.Body.Close()
		}()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
}

func waitForOAuthStatus(t *testing.T, router http.Handler, state string) oauthStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp := performOAuthStatusRequest(t, router, state)
		if resp.Status != "wait" {
			return resp
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for OAuth session %s status", state)
	return oauthStatusResponse{}
}

func TestStartOAuthV8SessionScopedProxyAndPersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var proxyHits atomic.Int32
	var directHits atomic.Int32
	proxyServer := startForwardHTTPProxy(t, &proxyHits, "", false)
	defer proxyServer.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Via-Session-Proxy") != "1" {
			directHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"proxied-codex@example.test"}`))
	}))
	defer upstream.Close()

	originalFactory := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		var sdkCfg config.SDKConfig
		if cfg != nil {
			sdkCfg = cfg.SDKConfig
		}
		return &proxyTestCodexOAuthService{
			client:   util.SetProxy(&sdkCfg, &http.Client{}),
			tokenURL: upstream.URL + "/oauth/token",
		}
	}
	defer func() {
		newCodexOAuthService = originalFactory
	}()

	authDir := t.TempDir()
	unrelatedPath := filepath.Join(authDir, "unrelated.json")
	if errWrite := os.WriteFile(unrelatedPath, []byte(`{"type":"codex","email":"other@example.test","access_token":"a","refresh_token":"r"}`), 0o600); errWrite != nil {
		t.Fatalf("write unrelated auth file: %v", errWrite)
	}

	cfg := &config.Config{AuthDir: authDir}
	h := NewHandlerWithoutConfigFilePath(cfg, nil)

	var hookAuth *coreauth.Auth
	var diskProxyAtHookTime string
	h.SetPostAuthPersistHook(func(_ context.Context, auth *coreauth.Auth) error {
		hookAuth = auth.Clone()
		if path := strings.TrimSpace(auth.Attributes[coreauth.AttributePath]); path != "" {
			if raw, errRead := os.ReadFile(path); errRead == nil {
				var m map[string]any
				if json.Unmarshal(raw, &m) == nil {
					diskProxyAtHookTime, _ = m["proxy_url"].(string)
				}
			}
		}
		return nil
	})

	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/status", h.GetAuthStatus)

	reqBody := `{"provider":"codex","proxy_url":"` + proxyServer.URL + `"}`
	req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /v8/management/oauth/auth-url status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var startResp struct {
		State string `json:"state"`
		URL   string `json:"url"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &startResp); errDecode != nil {
		t.Fatalf("decode start response: %v", errDecode)
	}
	if startResp.State == "" || startResp.URL == "" {
		t.Fatalf("unexpected start response: %s", rec.Body.String())
	}
	t.Cleanup(func() { CancelOAuthSession(startResp.State) })

	if _, errCallback := WriteOAuthCallbackFileForPendingSession(authDir, "codex", startResp.State, "code-1", ""); errCallback != nil {
		t.Fatalf("write callback file: %v", errCallback)
	}

	status := waitForOAuthStatus(t, router, startResp.State)
	if status.Status != "ok" {
		t.Fatalf("oauth status = %#v, want ok", status)
	}

	if proxyHits.Load() == 0 {
		t.Fatal("expected token exchange to use session proxy, got 0 proxy hits")
	}
	if directHits.Load() != 0 {
		t.Fatalf("expected 0 direct hits, got %d", directHits.Load())
	}
	if h.cfg.ProxyURL != "" {
		t.Fatalf("global cfg.ProxyURL mutated to %q, want empty", h.cfg.ProxyURL)
	}
	if hookAuth == nil {
		t.Fatal("expected postAuthPersistHook to be called")
	}
	if hookAuth.ProxyURL != proxyServer.URL {
		t.Fatalf("hookAuth.ProxyURL = %q, want %q", hookAuth.ProxyURL, proxyServer.URL)
	}
	if gotMeta, _ := hookAuth.Metadata["proxy_url"].(string); gotMeta != proxyServer.URL {
		t.Fatalf("hookAuth.Metadata[proxy_url] = %q, want %q", gotMeta, proxyServer.URL)
	}
	if diskProxyAtHookTime != proxyServer.URL {
		t.Fatalf("disk proxy_url at activation hook time = %q, want %q", diskProxyAtHookTime, proxyServer.URL)
	}

	unrelatedBytes, errReadUnrelated := os.ReadFile(unrelatedPath)
	if errReadUnrelated != nil {
		t.Fatalf("read unrelated auth file: %v", errReadUnrelated)
	}
	if strings.Contains(string(unrelatedBytes), "proxy_url") {
		t.Fatalf("unrelated auth file was modified: %s", string(unrelatedBytes))
	}
}

func TestStartOAuthV8RejectsInvalidOrQueryProxyURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)

	invalidProxies := []string{
		"://bad-url",
		"not-a-url",
		"ftp://127.0.0.1:21",
		"http://:8080",
		"http://127.0.0.1:99999",
		"http://127.0.0.1:0",
		"http://127.0.0.1:",
		"socks5://127.0.0.1",
		"direct",
		"none",
	}
	for _, badProxy := range invalidProxies {
		body, _ := json.Marshal(map[string]any{
			"provider":  "codex",
			"proxy_url": badProxy,
		})
		req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("proxy_url=%q: status = %d, want 400; body = %s", badProxy, rec.Code, rec.Body.String())
		}
	}

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		req := httptest.NewRequest(method, "/v8/management/oauth/auth-url?provider=codex&proxy_url=http://127.0.0.1:8080", nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s with query proxy_url: status = %d, want 400; body = %s", method, rec.Code, rec.Body.String())
		}
	}
}

func TestStartOAuthV8ProxyFailuresDoNotFallbackDirectAndRedactCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var directHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Via-Session-Proxy") != "1" {
			directHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"should-not-succeed@example.test"}`))
	}))
	defer upstream.Close()

	originalFactory := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		var sdkCfg config.SDKConfig
		if cfg != nil {
			sdkCfg = cfg.SDKConfig
		}
		return &proxyTestCodexOAuthService{
			client:   util.SetProxy(&sdkCfg, &http.Client{}),
			tokenURL: upstream.URL + "/oauth/token",
		}
	}
	defer func() {
		newCodexOAuthService = originalFactory
	}()

	var authRejectHits atomic.Int32
	authRejectProxy := startForwardHTTPProxy(t, &authRejectHits, "Basic dXNlcjpexpected-secret", false)
	defer authRejectProxy.Close()

	var dropHits atomic.Int32
	dropProxy := startForwardHTTPProxy(t, &dropHits, "", true)
	defer dropProxy.Close()

	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen temp port: %v", errListen)
	}
	closedAddr := ln.Addr().String()
	_ = ln.Close()

	authRejectURL, _ := url.Parse(authRejectProxy.URL)
	authRejectURL.User = url.UserPassword("user", "wrong-secret-pass")

	cases := []struct {
		name     string
		proxyURL string
	}{
		{name: "unreachable proxy", proxyURL: "http://user:unreachable-secret-pass@" + closedAddr},
		{name: "proxy auth rejected", proxyURL: authRejectURL.String()},
		{name: "proxy connection dropped", proxyURL: dropProxy.URL},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authDir := t.TempDir()
			h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
			router := gin.New()
			router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
			router.GET("/status", h.GetAuthStatus)

			body, _ := json.Marshal(map[string]string{
				"provider":  "codex",
				"proxy_url": tc.proxyURL,
			})
			req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("start status = %d, want 200; body = %s", rec.Code, rec.Body.String())
			}
			var startResp struct {
				State string `json:"state"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &startResp)
			t.Cleanup(func() { CancelOAuthSession(startResp.State) })

			if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", startResp.State, "code-fail", ""); errWrite != nil {
				t.Fatalf("write callback: %v", errWrite)
			}

			status := waitForOAuthStatus(t, router, startResp.State)
			if status.Status != "error" {
				t.Fatalf("status = %#v, want error", status)
			}
			if strings.Contains(status.Error, "secret-pass") {
				t.Fatalf("status error leaked proxy password: %q", status.Error)
			}
			if directHits.Load() != 0 {
				t.Fatalf("direct fallback occurred: directHits = %d", directHits.Load())
			}
			entries, _ := os.ReadDir(authDir)
			for _, e := range entries {
				if strings.HasSuffix(e.Name(), ".json") {
					t.Fatalf("unexpected credential file saved on failed proxy exchange: %s", e.Name())
				}
			}
		})
	}
}

func TestStartOAuthV8ParallelSessionsWithDifferentProxiesOutOfOrderCallbacks(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var hitsA, hitsB, directHits atomic.Int32
	proxyA := startForwardHTTPProxy(t, &hitsA, "", false)
	defer proxyA.Close()
	proxyB := startForwardHTTPProxy(t, &hitsB, "", false)
	defer proxyB.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Via-Session-Proxy") != "1" {
			directHits.Add(1)
		}
		_ = r.ParseForm()
		code := r.FormValue("code")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"user-` + code + `@example.test"}`))
	}))
	defer upstream.Close()

	originalFactory := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		var sdkCfg config.SDKConfig
		if cfg != nil {
			sdkCfg = cfg.SDKConfig
		}
		return &proxyTestCodexOAuthService{
			client:   util.SetProxy(&sdkCfg, &http.Client{}),
			tokenURL: upstream.URL + "/oauth/token",
		}
	}
	defer func() {
		newCodexOAuthService = originalFactory
	}()

	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/status", h.GetAuthStatus)

	startWithProxy := func(proxyURL string) string {
		body, _ := json.Marshal(map[string]string{"provider": "codex", "proxy_url": proxyURL})
		req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)
		t.Cleanup(func() { CancelOAuthSession(resp.State) })
		return resp.State
	}

	stateA := startWithProxy(proxyA.URL)
	stateB := startWithProxy(proxyB.URL)

	// Deliver callback B first, then callback A (out of order)
	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", stateB, "b", ""); errWrite != nil {
		t.Fatalf("write callback B: %v", errWrite)
	}
	if statusB := waitForOAuthStatus(t, router, stateB); statusB.Status != "ok" {
		t.Fatalf("session B status = %#v, want ok", statusB)
	}
	if hitsB.Load() != 1 || hitsA.Load() != 0 {
		t.Fatalf("after B callback: hitsA=%d hitsB=%d, want 0 and 1", hitsA.Load(), hitsB.Load())
	}

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", stateA, "a", ""); errWrite != nil {
		t.Fatalf("write callback A: %v", errWrite)
	}
	if statusA := waitForOAuthStatus(t, router, stateA); statusA.Status != "ok" {
		t.Fatalf("session A status = %#v, want ok", statusA)
	}
	if hitsA.Load() != 1 || hitsB.Load() != 1 || directHits.Load() != 0 {
		t.Fatalf("after A callback: hitsA=%d hitsB=%d direct=%d, want 1, 1, 0", hitsA.Load(), hitsB.Load(), directHits.Load())
	}

	entries, errDir := os.ReadDir(authDir)
	if errDir != nil {
		t.Fatalf("read authDir: %v", errDir)
	}
	foundByEmail := make(map[string]string)
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, errRead := os.ReadFile(filepath.Join(authDir, entry.Name()))
		if errRead != nil {
			t.Fatalf("read %s: %v", entry.Name(), errRead)
		}
		var data map[string]any
		if errJSON := json.Unmarshal(raw, &data); errJSON != nil {
			t.Fatalf("unmarshal %s: %v", entry.Name(), errJSON)
		}
		email, _ := data["email"].(string)
		proxyURL, _ := data["proxy_url"].(string)
		foundByEmail[email] = proxyURL
	}
	if foundByEmail["user-a@example.test"] != proxyA.URL {
		t.Fatalf("user-a proxy_url = %q, want %q", foundByEmail["user-a@example.test"], proxyA.URL)
	}
	if foundByEmail["user-b@example.test"] != proxyB.URL {
		t.Fatalf("user-b proxy_url = %q, want %q", foundByEmail["user-b@example.test"], proxyB.URL)
	}
}

type proxyTestAntigravityOAuthService struct {
	client  *http.Client
	baseURL string
}

func (s *proxyTestAntigravityOAuthService) BuildAuthURL(state, _ string) string {
	return s.baseURL + "/auth?state=" + state
}

func (s *proxyTestAntigravityOAuthService) ExchangeCodeForTokens(ctx context.Context, code, _ string) (*antigravity.TokenResponse, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/token", strings.NewReader("code="+url.QueryEscape(code)))
	if errReq != nil {
		return nil, errReq
	}
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() { _ = resp.Body.Close() }()
	var token antigravity.TokenResponse
	if errDecode := json.NewDecoder(resp.Body).Decode(&token); errDecode != nil {
		return nil, errDecode
	}
	return &token, nil
}

func (s *proxyTestAntigravityOAuthService) FetchUserInfo(ctx context.Context, accessToken string) (string, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL+"/userinfo", nil)
	if errReq != nil {
		return "", errReq
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() { _ = resp.Body.Close() }()
	var info struct {
		Email string `json:"email"`
	}
	if errDecode := json.NewDecoder(resp.Body).Decode(&info); errDecode != nil {
		return "", errDecode
	}
	return info.Email, nil
}

func (s *proxyTestAntigravityOAuthService) FetchProjectID(ctx context.Context, accessToken string) (string, error) {
	req, errReq := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/project", nil)
	if errReq != nil {
		return "", errReq
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, errDo := s.client.Do(req)
	if errDo != nil {
		return "", errDo
	}
	defer func() { _ = resp.Body.Close() }()
	var proj struct {
		ProjectID string `json:"project_id"`
	}
	if errDecode := json.NewDecoder(resp.Body).Decode(&proj); errDecode != nil {
		return "", errDecode
	}
	return proj.ProjectID, nil
}

func TestStartOAuthV8AntigravityAndPluginSessionProxyAndRestartPersistence(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var proxyHits, directHits atomic.Int32
	proxyServer := startForwardHTTPProxy(t, &proxyHits, "", false)
	defer proxyServer.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Via-Session-Proxy") != "1" {
			directHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"ag-access","refresh_token":"ag-refresh","expires_in":3600,"token_type":"Bearer"}`))
		case "/userinfo":
			_, _ = w.Write([]byte(`{"email":"ag-user@example.test"}`))
		case "/project":
			_, _ = w.Write([]byte(`{"project_id":"ag-proj-123"}`))
		case "/plugin-start", "/plugin-poll", "/runtime-traffic":
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	originalAgFactory := newAntigravityOAuthService
	newAntigravityOAuthService = func(cfg *config.Config) antigravityOAuthService {
		var sdkCfg config.SDKConfig
		if cfg != nil {
			sdkCfg = cfg.SDKConfig
		}
		return &proxyTestAntigravityOAuthService{
			client:  util.SetProxy(&sdkCfg, &http.Client{}),
			baseURL: upstream.URL,
		}
	}
	defer func() {
		newAntigravityOAuthService = originalAgFactory
	}()

	authDir := t.TempDir()
	host := pluginhost.New()
	host.RegisterPluginForTest("proxy-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &testAuthProvider{
				identifier: "custom-sso",
				startLogin: func(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
					if req.Host.ProxyURL != proxyServer.URL {
						return pluginapi.AuthLoginStartResponse{}, &antigravity.HTTPStatusError{StatusCodeValue: 500, Message: "unexpected host proxy"}
					}
					if _, errDo := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: upstream.URL + "/plugin-start"}); errDo != nil {
						return pluginapi.AuthLoginStartResponse{}, errDo
					}
					return pluginapi.AuthLoginStartResponse{
						Provider: "custom-sso",
						URL:      "https://sso.example.test/auth",
						State:    "plugin-proxy-state-1",
					}, nil
				},
				pollLogin: func(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
					if req.Host.ProxyURL != proxyServer.URL {
						return pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "missing poll proxy"}, nil
					}
					if _, errDo := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: upstream.URL + "/plugin-poll"}); errDo != nil {
						return pluginapi.AuthLoginPollResponse{}, errDo
					}
					return pluginapi.AuthLoginPollResponse{
						Status: pluginapi.AuthLoginStatusSuccess,
						Auth: pluginapi.AuthData{
							Provider:    "custom-sso",
							ID:          "custom-sso-user.json",
							FileName:    "custom-sso-user.json",
							StorageJSON: []byte(`{"type":"custom-sso","access_token":"p-tok"}`),
						},
					}, nil
				},
			},
		},
	})

	cfg := &config.Config{AuthDir: authDir, Port: 8317}
	h := NewHandlerWithoutConfigFilePath(cfg, nil)
	h.SetPluginHost(host)

	router := gin.New()
	router.GET("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/status", h.GetAuthStatus)
	router.DELETE("/session", h.CancelAuthSession)

	// 1. Antigravity session with proxy_url covers token exchange, userinfo, and project discovery
	agBody, _ := json.Marshal(map[string]string{"provider": "antigravity", "proxy_url": proxyServer.URL})
	agRec := httptest.NewRecorder()
	router.ServeHTTP(agRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(agBody))))
	if agRec.Code != http.StatusOK {
		t.Fatalf("antigravity start status = %d, body = %s", agRec.Code, agRec.Body.String())
	}
	var agStart struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(agRec.Body.Bytes(), &agStart)
	t.Cleanup(func() { CancelOAuthSession(agStart.State) })

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", agStart.State, "ag-code", ""); errWrite != nil {
		t.Fatalf("write antigravity callback: %v", errWrite)
	}
	if agStatus := waitForOAuthStatus(t, router, agStart.State); agStatus.Status != "ok" {
		t.Fatalf("antigravity status = %#v, want ok", agStatus)
	}
	if proxyHits.Load() != 3 || directHits.Load() != 0 {
		t.Fatalf("antigravity proxyHits=%d directHits=%d, want 3 and 0", proxyHits.Load(), directHits.Load())
	}

	// Simulate restart by synthesizing the saved Antigravity credential from disk and routing traffic
	agPath := filepath.Join(authDir, antigravity.CredentialFileName("ag-user@example.test"))
	agRaw, errReadAg := os.ReadFile(agPath)
	if errReadAg != nil {
		t.Fatalf("read saved antigravity credential: %v", errReadAg)
	}
	synthAuths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Config:      cfg,
		AuthDir:     authDir,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}, agPath, agRaw)
	if errSynth != nil || len(synthAuths) != 1 {
		t.Fatalf("SynthesizeAuthFile err=%v len=%d", errSynth, len(synthAuths))
	}
	if synthAuths[0].ProxyURL != proxyServer.URL {
		t.Fatalf("synthesized ProxyURL after restart = %q, want %q", synthAuths[0].ProxyURL, proxyServer.URL)
	}
	runtimeClient := helps.NewProxyAwareHTTPClient(context.Background(), cfg, synthAuths[0], 0)
	runtimeResp, errRuntime := runtimeClient.Get(upstream.URL + "/runtime-traffic")
	if errRuntime != nil {
		t.Fatalf("runtime request with synthesized auth failed: %v", errRuntime)
	}
	_ = runtimeResp.Body.Close()
	if proxyHits.Load() != 4 || directHits.Load() != 0 {
		t.Fatalf("after runtime request: proxyHits=%d directHits=%d, want 4 and 0", proxyHits.Load(), directHits.Load())
	}

	// 2. Plugin OAuth session with proxy_url covers StartLogin, PollLogin, and saved credential
	pluginBody, _ := json.Marshal(map[string]string{"provider": "custom-sso", "proxy_url": proxyServer.URL})
	pluginRec := httptest.NewRecorder()
	router.ServeHTTP(pluginRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(pluginBody))))
	if pluginRec.Code != http.StatusOK {
		t.Fatalf("plugin start status = %d, body = %s", pluginRec.Code, pluginRec.Body.String())
	}
	t.Cleanup(func() { CancelOAuthSession("plugin-proxy-state-1") })
	if pluginStatus := performOAuthStatusRequest(t, router, "plugin-proxy-state-1"); pluginStatus.Status != "ok" {
		t.Fatalf("plugin poll status = %#v, want ok", pluginStatus)
	}
	if proxyHits.Load() != 6 || directHits.Load() != 0 {
		t.Fatalf("after plugin login: proxyHits=%d directHits=%d, want 6 and 0", proxyHits.Load(), directHits.Load())
	}
	pluginCredRaw, errReadPlugin := os.ReadFile(filepath.Join(authDir, "custom-sso-user.json"))
	if errReadPlugin != nil {
		t.Fatalf("read plugin credential: %v", errReadPlugin)
	}
	var pluginCredMap map[string]any
	_ = json.Unmarshal(pluginCredRaw, &pluginCredMap)
	if pluginCredMap["proxy_url"] != proxyServer.URL {
		t.Fatalf("plugin credential proxy_url = %v, want %q", pluginCredMap["proxy_url"], proxyServer.URL)
	}

	// 3. Cancellation and expiry discard temporary session proxy data
	RegisterOAuthSessionWithProxy("cancel-proxy-state", "codex", proxyServer.URL)
	if got := GetOAuthSessionProxyURL("cancel-proxy-state"); got != proxyServer.URL {
		t.Fatalf("GetOAuthSessionProxyURL before cancel = %q, want %q", got, proxyServer.URL)
	}
	cancelRec := httptest.NewRecorder()
	router.ServeHTTP(cancelRec, httptest.NewRequest(http.MethodDelete, "/session?state=cancel-proxy-state", nil))
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, body = %s", cancelRec.Code, cancelRec.Body.String())
	}
	if got := GetOAuthSessionProxyURL("cancel-proxy-state"); got != "" {
		t.Fatalf("GetOAuthSessionProxyURL after cancel = %q, want empty", got)
	}

	// Verify session expiry purges temporary proxy data
	oauthSessions.mu.Lock()
	oauthSessions.sessions["expired-proxy-state"] = oauthSession{
		Provider:  "codex",
		Source:    oauthSessionSourceBuiltin,
		ProxyURL:  proxyServer.URL,
		CreatedAt: time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	oauthSessions.mu.Unlock()
	if got := GetOAuthSessionProxyURL("expired-proxy-state"); got != "" {
		t.Fatalf("GetOAuthSessionProxyURL for expired session = %q, want empty", got)
	}
}

func TestStartOAuthV8AntigravityProjectDiscoveryProxyFailureFailsSession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var proxyHits, directHits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if strings.HasSuffix(r.RequestURI, "/project") {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, errHijack := hj.Hijack()
				if errHijack == nil {
					_ = conn.Close()
					return
				}
			}
			http.Error(w, "proxy dropped on project discovery", http.StatusBadGateway)
			return
		}
		outReq, errReq := http.NewRequestWithContext(r.Context(), r.Method, r.RequestURI, r.Body)
		if errReq != nil {
			http.Error(w, errReq.Error(), http.StatusBadGateway)
			return
		}
		outReq.Header = r.Header.Clone()
		outReq.Header.Set("X-Via-Session-Proxy", "1")
		resp, errDo := (&http.Client{Transport: &http.Transport{Proxy: nil}}).Do(outReq)
		if errDo != nil {
			http.Error(w, errDo.Error(), http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxyServer.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Via-Session-Proxy") != "1" {
			directHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"ag-access","refresh_token":"ag-refresh","expires_in":3600,"token_type":"Bearer"}`))
		case "/userinfo":
			_, _ = w.Write([]byte(`{"email":"ag-fail@example.test"}`))
		case "/project":
			_, _ = w.Write([]byte(`{"project_id":"ag-proj-should-not-reach"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	originalAgFactory := newAntigravityOAuthService
	newAntigravityOAuthService = func(cfg *config.Config) antigravityOAuthService {
		var sdkCfg config.SDKConfig
		if cfg != nil {
			sdkCfg = cfg.SDKConfig
		}
		return &proxyTestAntigravityOAuthService{
			client:  util.SetProxy(&sdkCfg, &http.Client{}),
			baseURL: upstream.URL,
		}
	}
	defer func() {
		newAntigravityOAuthService = originalAgFactory
	}()

	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/status", h.GetAuthStatus)

	agBody, _ := json.Marshal(map[string]string{"provider": "antigravity", "proxy_url": proxyServer.URL})
	agRec := httptest.NewRecorder()
	router.ServeHTTP(agRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(agBody))))
	if agRec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", agRec.Code, agRec.Body.String())
	}
	var agStart struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(agRec.Body.Bytes(), &agStart)
	t.Cleanup(func() { CancelOAuthSession(agStart.State) })

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "antigravity", agStart.State, "ag-code", ""); errWrite != nil {
		t.Fatalf("write callback: %v", errWrite)
	}
	agStatus := waitForOAuthStatus(t, router, agStart.State)
	if agStatus.Status != "error" {
		t.Fatalf("expected error status when proxy disconnects during project discovery, got %#v", agStatus)
	}
	if directHits.Load() != 0 {
		t.Fatalf("expected 0 direct hits, got %d", directHits.Load())
	}
	entries, _ := os.ReadDir(authDir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("unexpected credential file saved after project discovery proxy failure: %s", e.Name())
		}
	}
}

func TestStartOAuthV8PluginPollCancelRaceDoesNotDropProxyOrSaveCancelledSession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var proxyHits, directHits atomic.Int32
	proxyServer := startForwardHTTPProxy(t, &proxyHits, "", false)
	defer proxyServer.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Via-Session-Proxy") != "1" {
			directHits.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	authDir := t.TempDir()
	host := pluginhost.New()
	const raceState = "plugin-cancel-race-state"
	host.RegisterPluginForTest("race-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &testAuthProvider{
				identifier: "race-sso",
				startLogin: func(_ context.Context, _ pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
					return pluginapi.AuthLoginStartResponse{
						Provider: "race-sso",
						URL:      "https://sso.example.test/auth",
						State:    raceState,
					}, nil
				},
				pollLogin: func(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
					if req.Host.ProxyURL != proxyServer.URL {
						t.Errorf("PollLogin Host.ProxyURL = %q, want %q", req.Host.ProxyURL, proxyServer.URL)
					}
					if _, errDo := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: upstream.URL + "/poll"}); errDo != nil {
						return pluginapi.AuthLoginPollResponse{}, errDo
					}
					// Simulate concurrent session cancellation while PollLogin is in flight
					CancelOAuthSession(raceState)
					return pluginapi.AuthLoginPollResponse{
						Status: pluginapi.AuthLoginStatusSuccess,
						Auth: pluginapi.AuthData{
							Provider:    "race-sso",
							ID:          "race-sso-user.json",
							FileName:    "race-sso-user.json",
							StorageJSON: []byte(`{"type":"race-sso","access_token":"tok"}`),
						},
					}, nil
				},
			},
		},
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, nil)
	h.SetPluginHost(host)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/status", h.GetAuthStatus)

	body, _ := json.Marshal(map[string]string{"provider": "race-sso", "proxy_url": proxyServer.URL})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(body))))
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() { CancelOAuthSession(raceState) })

	pollStatus := performOAuthStatusRequest(t, router, raceState)
	if pollStatus.Status != "error" {
		t.Fatalf("expected error status when session is cancelled during PollLogin, got %#v", pollStatus)
	}
	if proxyHits.Load() != 1 || directHits.Load() != 0 {
		t.Fatalf("proxyHits=%d directHits=%d, want 1 and 0", proxyHits.Load(), directHits.Load())
	}
	if _, errStat := os.Stat(filepath.Join(authDir, "race-sso-user.json")); !os.IsNotExist(errStat) {
		t.Fatalf("expected cancelled plugin session not to save credential file, stat err = %v", errStat)
	}

	// 2. Verify that once commit begins (postAuthHook barrier), CancelOAuthSession returns false
	// so cancellation and credential commit are mutually exclusive.
	const commitState = "plugin-commit-barrier-state"
	host.RegisterPluginForTest("commit-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &testAuthProvider{
				identifier: "commit-sso",
				startLogin: func(_ context.Context, _ pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
					return pluginapi.AuthLoginStartResponse{
						Provider: "commit-sso",
						URL:      "https://sso.example.test/commit",
						State:    commitState,
					}, nil
				},
				pollLogin: func(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
					if _, errDo := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: upstream.URL + "/poll"}); errDo != nil {
						return pluginapi.AuthLoginPollResponse{}, errDo
					}
					return pluginapi.AuthLoginPollResponse{
						Status: pluginapi.AuthLoginStatusSuccess,
						Auth: pluginapi.AuthData{
							Provider:    "commit-sso",
							ID:          "commit-sso-user.json",
							FileName:    "commit-sso-user.json",
							StorageJSON: []byte(`{"type":"commit-sso","access_token":"tok"}`),
						},
					}, nil
				},
			},
		},
	})
	var cancelDuringCommitResult atomic.Bool
	h.postAuthHook = func(_ context.Context, _ *coreauth.Auth) error {
		cancelDuringCommitResult.Store(CancelOAuthSession(commitState))
		return nil
	}
	defer func() { h.postAuthHook = nil }()

	commitBody, _ := json.Marshal(map[string]string{"provider": "commit-sso", "proxy_url": proxyServer.URL})
	commitRec := httptest.NewRecorder()
	router.ServeHTTP(commitRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(commitBody))))
	if commitRec.Code != http.StatusOK {
		t.Fatalf("commit start status = %d, body = %s", commitRec.Code, commitRec.Body.String())
	}
	t.Cleanup(func() { CancelOAuthSession(commitState) })

	commitPoll := performOAuthStatusRequest(t, router, commitState)
	if commitPoll.Status != "ok" {
		t.Fatalf("expected commitPoll status = ok, got %#v", commitPoll)
	}
	if cancelDuringCommitResult.Load() {
		t.Fatal("expected CancelOAuthSession during in-flight commit to return false")
	}

	// 3. Verify that two concurrent in-flight PollLogin requests (Poll #1 returning success,
	// Poll #2 returning error while Poll #1 is committing in postAuthHook) cannot overwrite
	// the committing session.
	const interleaveState = "plugin-interleave-poll-state"
	var pollCall atomic.Int32
	poll1Entered := make(chan struct{})
	poll2Entered := make(chan struct{})
	releasePoll2 := make(chan struct{})
	poll2Done := make(chan oauthStatusResponse, 1)

	host.RegisterPluginForTest("interleave-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{
			AuthProvider: &testAuthProvider{
				identifier: "interleave-sso",
				startLogin: func(_ context.Context, _ pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
					return pluginapi.AuthLoginStartResponse{
						Provider: "interleave-sso",
						URL:      "https://sso.example.test/interleave",
						State:    interleaveState,
					}, nil
				},
				pollLogin: func(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
					callNum := pollCall.Add(1)
					if callNum == 1 {
						close(poll1Entered)
						<-poll2Entered
						return pluginapi.AuthLoginPollResponse{
							Status: pluginapi.AuthLoginStatusSuccess,
							Auth: pluginapi.AuthData{
								Provider:    "interleave-sso",
								ID:          "interleave-sso-user.json",
								FileName:    "interleave-sso-user.json",
								StorageJSON: []byte(`{"type":"interleave-sso","access_token":"tok"}`),
							},
						}, nil
					}
					close(poll2Entered)
					<-releasePoll2
					return pluginapi.AuthLoginPollResponse{
						Status:  pluginapi.AuthLoginStatusError,
						Message: "stale concurrent poll error",
					}, nil
				},
			},
		},
	})

	interleaveBody, _ := json.Marshal(map[string]string{"provider": "interleave-sso", "proxy_url": proxyServer.URL})
	interleaveRec := httptest.NewRecorder()
	router.ServeHTTP(interleaveRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(interleaveBody))))
	if interleaveRec.Code != http.StatusOK {
		t.Fatalf("interleave start status = %d, body = %s", interleaveRec.Code, interleaveRec.Body.String())
	}
	t.Cleanup(func() { CancelOAuthSession(interleaveState) })

	h.postAuthHook = func(_ context.Context, _ *coreauth.Auth) error {
		close(releasePoll2)
		poll2Status := <-poll2Done
		if poll2Status.Status != "wait" {
			t.Errorf("concurrent in-flight error poll during commit = %#v, want wait", poll2Status)
		}
		return nil
	}

	go func() {
		<-poll1Entered
		poll2Done <- performOAuthStatusRequest(t, router, interleaveState)
	}()

	firstPoll := performOAuthStatusRequest(t, router, interleaveState)
	if firstPoll.Status != "ok" {
		t.Fatalf("firstPoll status = %#v, want ok", firstPoll)
	}
	finalPoll := performOAuthStatusRequest(t, router, interleaveState)
	if finalPoll.Status != "ok" {
		t.Fatalf("finalPoll status = %#v, want ok", finalPoll)
	}
}

type proxyRefreshTestAuthProvider struct {
	identifier  string
	startLogin  func(context.Context, pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error)
	pollLogin   func(context.Context, pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error)
	parseAuth   func(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error)
	refreshAuth func(context.Context, pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error)
}

func (p *proxyRefreshTestAuthProvider) Identifier() string { return p.identifier }
func (p *proxyRefreshTestAuthProvider) ParseAuth(ctx context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
	if p.parseAuth != nil {
		return p.parseAuth(ctx, req)
	}
	return pluginapi.AuthParseResponse{}, nil
}
func (p *proxyRefreshTestAuthProvider) StartLogin(ctx context.Context, req pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
	if p.startLogin != nil {
		return p.startLogin(ctx, req)
	}
	return pluginapi.AuthLoginStartResponse{}, nil
}
func (p *proxyRefreshTestAuthProvider) PollLogin(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
	if p.pollLogin != nil {
		return p.pollLogin(ctx, req)
	}
	return pluginapi.AuthLoginPollResponse{}, nil
}
func (p *proxyRefreshTestAuthProvider) RefreshAuth(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
	if p.refreshAuth != nil {
		return p.refreshAuth(ctx, req)
	}
	return pluginapi.AuthRefreshResponse{}, nil
}

func TestStartOAuthV8WithoutProxyPreservesDefaultBehaviorAndPersistedProxyRefresh(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var proxyHits, directHits atomic.Int32
	proxyServer := startForwardHTTPProxy(t, &proxyHits, "", false)
	defer proxyServer.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Via-Session-Proxy") != "1" {
			directHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"email":"default-codex@example.test"}`))
	}))
	defer upstream.Close()

	originalFactory := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		var sdkCfg config.SDKConfig
		if cfg != nil {
			sdkCfg = cfg.SDKConfig
		}
		return &proxyTestCodexOAuthService{
			client:   util.SetProxy(&sdkCfg, &http.Client{}),
			tokenURL: upstream.URL + "/oauth/token",
		}
	}
	defer func() {
		newCodexOAuthService = originalFactory
	}()

	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 8317}, nil)
	router := gin.New()
	router.POST("/v8/management/oauth/auth-url", h.StartOAuthV8)
	router.GET("/status", h.GetAuthStatus)

	// 1. Default behavior without proxy_url override
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(`{"provider":"codex"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var startResp struct {
		State string `json:"state"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &startResp)
	t.Cleanup(func() { CancelOAuthSession(startResp.State) })
	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", startResp.State, "no-proxy", ""); errWrite != nil {
		t.Fatalf("write callback: %v", errWrite)
	}
	if status := waitForOAuthStatus(t, router, startResp.State); status.Status != "ok" {
		t.Fatalf("status = %#v, want ok", status)
	}
	if directHits.Load() != 1 || proxyHits.Load() != 0 {
		t.Fatalf("without override: directHits=%d proxyHits=%d, want 1 and 0", directHits.Load(), proxyHits.Load())
	}
	entries, _ := os.ReadDir(authDir)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, _ := os.ReadFile(filepath.Join(authDir, e.Name()))
		if strings.Contains(string(raw), "proxy_url") {
			t.Fatalf("credential without session proxy should not contain proxy_url: %s", string(raw))
		}
	}

	// 2. End-to-end OAuth login with session proxy -> persist to disk -> synthesize from disk -> production RefreshAuth()
	refreshProvider := &proxyRefreshTestAuthProvider{
		identifier: "refresh-sso",
		startLogin: func(_ context.Context, _ pluginapi.AuthLoginStartRequest) (pluginapi.AuthLoginStartResponse, error) {
			return pluginapi.AuthLoginStartResponse{
				Provider: "refresh-sso",
				URL:      "https://sso.example.test/refresh-auth",
				State:    "refresh-sso-state",
			}, nil
		},
		pollLogin: func(ctx context.Context, req pluginapi.AuthLoginPollRequest) (pluginapi.AuthLoginPollResponse, error) {
			if _, errDo := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodGet, URL: upstream.URL + "/poll"}); errDo != nil {
				return pluginapi.AuthLoginPollResponse{}, errDo
			}
			return pluginapi.AuthLoginPollResponse{
				Status: pluginapi.AuthLoginStatusSuccess,
				Auth: pluginapi.AuthData{
					Provider:    "refresh-sso",
					ID:          "refresh-sso-user.json",
					FileName:    "refresh-sso-user.json",
					StorageJSON: []byte(`{"type":"refresh-sso","access_token":"tok-1","refresh_token":"ref-1"}`),
				},
			}, nil
		},
		parseAuth: func(_ context.Context, req pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
			var parsed map[string]any
			if errUnmarshal := json.Unmarshal(req.RawJSON, &parsed); errUnmarshal != nil {
				return pluginapi.AuthParseResponse{}, nil
			}
			if parsed["type"] != "refresh-sso" {
				return pluginapi.AuthParseResponse{}, nil
			}
			proxyVal, _ := parsed["proxy_url"].(string)
			return pluginapi.AuthParseResponse{
				Handled: true,
				Auth: pluginapi.AuthData{
					Provider:    "refresh-sso",
					ID:          req.FileName,
					FileName:    req.FileName,
					ProxyURL:    proxyVal,
					Metadata:    parsed,
					StorageJSON: req.RawJSON,
				},
			}, nil
		},
		refreshAuth: func(ctx context.Context, req pluginapi.AuthRefreshRequest) (pluginapi.AuthRefreshResponse, error) {
			if _, errDo := req.HTTPClient.Do(ctx, pluginapi.HTTPRequest{Method: http.MethodPost, URL: upstream.URL + "/refresh"}); errDo != nil {
				return pluginapi.AuthRefreshResponse{}, errDo
			}
			return pluginapi.AuthRefreshResponse{
				Auth: pluginapi.AuthData{
					Provider:    "refresh-sso",
					ID:          req.AuthID,
					StorageJSON: []byte(`{"type":"refresh-sso","access_token":"tok-2","refresh_token":"ref-2"}`),
				},
			}, nil
		},
	}
	loginHost := pluginhost.New()
	loginHost.RegisterPluginForTest("refresh-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{AuthProvider: refreshProvider},
	})
	h.SetPluginHost(loginHost)

	refreshStartBody, _ := json.Marshal(map[string]string{
		"provider":  "refresh-sso",
		"proxy_url": proxyServer.URL,
	})
	refreshStartRec := httptest.NewRecorder()
	router.ServeHTTP(refreshStartRec, httptest.NewRequest(http.MethodPost, "/v8/management/oauth/auth-url", strings.NewReader(string(refreshStartBody))))
	if refreshStartRec.Code != http.StatusOK {
		t.Fatalf("refresh-sso start status = %d, body = %s", refreshStartRec.Code, refreshStartRec.Body.String())
	}
	t.Cleanup(func() { CancelOAuthSession("refresh-sso-state") })
	if pollStatus := performOAuthStatusRequest(t, router, "refresh-sso-state"); pollStatus.Status != "ok" {
		t.Fatalf("refresh-sso poll status = %#v, want ok", pollStatus)
	}
	hitsAfterLogin := proxyHits.Load()
	if hitsAfterLogin != 1 || directHits.Load() != 1 {
		t.Fatalf("after refresh-sso login: proxyHits=%d directHits=%d, want 1 and 1", hitsAfterLogin, directHits.Load())
	}

	// Load the saved credential file from disk with a blank global config and fresh plugin host (simulating server restart)
	savedCredPath := filepath.Join(authDir, "refresh-sso-user.json")
	rawSavedCred, errRead := os.ReadFile(savedCredPath)
	if errRead != nil {
		t.Fatalf("read saved refresh-sso credential file: %v", errRead)
	}
	freshHost := pluginhost.New()
	freshHost.RegisterPluginForTest("refresh-plugin", pluginapi.Plugin{
		Capabilities: pluginapi.Capabilities{AuthProvider: refreshProvider},
	})
	synthAuths, errSynth := synthesizer.SynthesizeAuthFile(&synthesizer.SynthesisContext{
		Config:           &config.Config{AuthDir: authDir},
		AuthDir:          authDir,
		Now:              time.Now(),
		IDGenerator:      synthesizer.NewStableIDGenerator(),
		PluginAuthParser: freshHost,
	}, savedCredPath, rawSavedCred)
	if errSynth != nil || len(synthAuths) != 1 {
		t.Fatalf("SynthesizeAuthFile refresh-sso err = %v, len = %d", errSynth, len(synthAuths))
	}
	if synthAuths[0].ProxyURL != proxyServer.URL {
		t.Fatalf("synthesized refresh-sso Auth.ProxyURL = %q, want %q", synthAuths[0].ProxyURL, proxyServer.URL)
	}

	// Invoke real production RefreshAuth on freshHost (no global ProxyURL, no session context)
	refreshedAuth, handled, errRefresh := freshHost.RefreshAuth(context.Background(), synthAuths[0])
	if errRefresh != nil || !handled {
		t.Fatalf("production RefreshAuth via persisted proxy_url: handled=%v err=%v", handled, errRefresh)
	}
	if refreshedAuth == nil || refreshedAuth.ProxyURL != proxyServer.URL {
		t.Fatalf("refreshedAuth = %#v, want ProxyURL=%q", refreshedAuth, proxyServer.URL)
	}
	if proxyHits.Load() != hitsAfterLogin+1 || directHits.Load() != 1 {
		t.Fatalf("expected production RefreshAuth to route via persisted proxy: proxyHits=%d (want %d), directHits=%d (want 1)", proxyHits.Load(), hitsAfterLogin+1, directHits.Load())
	}
}
