package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestClientAnthropicCredential(t *testing.T) {
	cases := []struct {
		name   string
		header map[string]string
		want   string
	}{
		{"oauth bearer", map[string]string{"Authorization": "Bearer sk-ant-oat01-abc"}, "sk-ant-oat01-abc"},
		{"lowercase bearer", map[string]string{"Authorization": "bearer sk-ant-oat01-abc"}, "sk-ant-oat01-abc"},
		{"console x-api-key", map[string]string{"X-Api-Key": "sk-ant-api03-xyz"}, "sk-ant-api03-xyz"},
		{"proxy key", map[string]string{"Authorization": "Bearer sk-cpa-123"}, ""},
		{"no credential", nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			for k, v := range tc.header {
				r.Header.Set(k, v)
			}
			if got := clientAnthropicCredential(r); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIsConfiguredClientKeyTrimsConfiguredKeys(t *testing.T) {
	keys := []string{"  sk-ant-configured \t", "", "   "}
	if !isConfiguredClientKey("sk-ant-configured", keys) {
		t.Fatal("whitespace-padded configured key must match its trimmed form")
	}
	if isConfiguredClientKey("sk-ant-other", keys) {
		t.Fatal("unrelated key must not match")
	}
}

// passthroughTestEngine builds a Gin engine with the same ordering as NewServer: server-wide
// gates first, then the passthrough middleware, then a local handler standing in for routes.
func passthroughTestEngine(s *Server, target *url.URL, transport http.RoundTripper) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(s.maxRequestBodyBytesMiddleware())
	engine.Use(s.homeHeartbeatMiddleware())
	engine.Use(s.claudeOAuthPassthroughMiddleware(newAnthropicPassthroughProxy(target, transport)))
	engine.NoRoute(func(c *gin.Context) { c.Status(http.StatusTeapot) })
	return engine
}

// serveRequest sends one request through engine on a real listener; a ResponseRecorder is not
// enough because the reverse proxy needs the server's CloseNotifier-capable writer.
func serveRequest(t *testing.T, engine http.Handler, method, path, body string, header map[string]string) (int, string) {
	t.Helper()
	srv := httptest.NewServer(wrapUnconsumedRequestBodyHandler(engine))
	defer srv.Close()
	req, errNew := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	if errNew != nil {
		t.Fatal(errNew)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		t.Fatal(errDo)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestClaudeOAuthPassthroughMiddleware(t *testing.T) {
	var upstreamAuth, upstreamPath, upstreamBody, upstreamHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamAuth = r.Header.Get("Authorization")
		upstreamPath = r.URL.RequestURI()
		upstreamHost = r.Host
		b, _ := io.ReadAll(r.Body)
		upstreamBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_stop\n\n")
	}))
	defer upstream.Close()
	target, errParse := url.Parse(upstream.URL)
	if errParse != nil {
		t.Fatal(errParse)
	}

	cfg := &config.Config{}
	cfg.APIKeys = []string{"  sk-ant-configured-proxy-key  "}
	s := &Server{cfg: cfg}
	h := passthroughTestEngine(s, target, http.DefaultTransport)

	send := func(header, value string) (int, string) {
		upstreamAuth, upstreamPath, upstreamBody, upstreamHost = "", "", "", ""
		return serveRequest(t, h, http.MethodPost, "/v1/messages?beta=true", `{"model":"claude-sonnet-5-5"}`, map[string]string{header: value})
	}

	// Disabled: OAuth token falls through to the normal pipeline.
	if code, _ := send("Authorization", "Bearer sk-ant-oat01-abc"); code != http.StatusTeapot || upstreamPath != "" {
		t.Fatalf("disabled: code=%d upstreamPath=%q", code, upstreamPath)
	}

	cfg.Client.Claude.AnthropicOAuthPassthrough = true

	// Enabled: OAuth token is forwarded unchanged.
	code, body := send("Authorization", "Bearer sk-ant-oat01-abc")
	if code != http.StatusOK || !strings.Contains(body, "message_stop") {
		t.Fatalf("passthrough: code=%d body=%q", code, body)
	}
	if upstreamAuth != "Bearer sk-ant-oat01-abc" || upstreamPath != "/v1/messages?beta=true" ||
		upstreamBody != `{"model":"claude-sonnet-5-5"}` || upstreamHost != target.Host {
		t.Fatalf("passthrough request mismatch: auth=%q path=%q body=%q host=%q", upstreamAuth, upstreamPath, upstreamBody, upstreamHost)
	}

	// Enabled: proxy keys stay local, including a configured sk-ant- key that carries
	// surrounding whitespace in the configuration.
	for _, key := range []string{"sk-cpa-123", "sk-ant-configured-proxy-key"} {
		if code, _ := send("Authorization", "Bearer "+key); code != http.StatusTeapot || upstreamPath != "" {
			t.Fatalf("proxy key %q: code=%d upstreamPath=%q", key, code, upstreamPath)
		}
	}
}

func TestClaudeOAuthPassthroughHonorsServerWideGates(t *testing.T) {
	var upstreamHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	target, _ := url.Parse(upstream.URL)

	cfg := &config.Config{}
	cfg.Client.Claude.AnthropicOAuthPassthrough = true
	cfg.MaxRequestBodyBytes = 16
	s := &Server{cfg: cfg}
	h := passthroughTestEngine(s, target, http.DefaultTransport)

	// Body above server.max-request-body-bytes gets 413 and never reaches Anthropic.
	auth := map[string]string{"Authorization": "Bearer sk-ant-oat01-abc"}
	code, _ := serveRequest(t, h, http.MethodPost, "/v1/messages", strings.Repeat("x", 64), auth)
	if code != http.StatusRequestEntityTooLarge || upstreamHits.Load() != 0 {
		t.Fatalf("oversized body: code=%d upstreamHits=%d", code, upstreamHits.Load())
	}

	// Home mode without a healthy heartbeat gets 503 and never reaches Anthropic.
	cfg.MaxRequestBodyBytes = 0
	cfg.Home.Enabled = true
	code, _ = serveRequest(t, h, http.MethodPost, "/v1/messages", "{}", auth)
	if code != http.StatusServiceUnavailable || upstreamHits.Load() != 0 {
		t.Fatalf("home heartbeat: code=%d upstreamHits=%d", code, upstreamHits.Load())
	}
}

func TestPassthroughTransportFollowsProxyURLAndReloads(t *testing.T) {
	var upstreamHits, viaProxyHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	// Plain HTTP forward proxy: records the request and answers itself.
	forward := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		viaProxyHits.Add(1)
		w.Header().Set("X-Via-Proxy", "1")
		w.WriteHeader(http.StatusOK)
	}))
	defer forward.Close()

	proxyURL := ""
	transport := &passthroughTransport{proxyURL: func() string { return proxyURL }}
	do := func() *http.Response {
		req, _ := http.NewRequest(http.MethodGet, upstream.URL+"/v1/messages", nil)
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}

	// No requests.proxy-url: direct to upstream.
	if resp := do(); resp.Header.Get("X-Via-Proxy") != "" || upstreamHits.Load() != 1 {
		t.Fatalf("direct: viaProxy=%q upstreamHits=%d", resp.Header.Get("X-Via-Proxy"), upstreamHits.Load())
	}

	// Config reload sets requests.proxy-url: the same transport now goes through the proxy.
	proxyURL = forward.URL
	if resp := do(); resp.Header.Get("X-Via-Proxy") != "1" || viaProxyHits.Load() != 1 || upstreamHits.Load() != 1 {
		t.Fatalf("proxy: viaProxy=%q proxyHits=%d upstreamHits=%d", resp.Header.Get("X-Via-Proxy"), viaProxyHits.Load(), upstreamHits.Load())
	}

	// Reload back to empty: direct again.
	proxyURL = ""
	if resp := do(); resp.Header.Get("X-Via-Proxy") != "" || upstreamHits.Load() != 2 {
		t.Fatalf("direct again: viaProxy=%q upstreamHits=%d", resp.Header.Get("X-Via-Proxy"), upstreamHits.Load())
	}
}

// TestNewServerAppliesGatesBeforePassthrough guards the registration order in NewServer without
// contacting Anthropic: an oversized body must be rejected before it could be forwarded, and with
// the option disabled a native token still falls through to local authentication (401).
func TestNewServerAppliesGatesBeforePassthrough(t *testing.T) {
	cfg := &config.Config{}
	cfg.APIKeys = []string{"test-key"}
	cfg.MaxRequestBodyBytes = 16
	cfg.Client.Claude.AnthropicOAuthPassthrough = true
	server := newTestServerWithConfig(t, cfg)
	srv := httptest.NewServer(server.Handler())
	defer srv.Close()

	do := func(body string) int {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer sk-ant-oat01-abc")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	if code := do(strings.Repeat("x", 64)); code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized passthrough request: code=%d, want 413", code)
	}

	server.cfgMu.Lock()
	server.cfg.Client.Claude.AnthropicOAuthPassthrough = false
	server.cfgMu.Unlock()
	if code := do("{}"); code != http.StatusUnauthorized {
		t.Fatalf("disabled passthrough with native token: code=%d, want 401", code)
	}
}
