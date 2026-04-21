package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/modules"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestMCPModule_Name(t *testing.T) {
	m := New(nil)
	if got := m.Name(); got != "mcp-forwarding" {
		t.Fatalf("unexpected module name: %s", got)
	}
}

func TestMCPModule_Register_WithoutUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	m := New(func(c *gin.Context) { c.Next() })

	ctx := modules.Context{
		Engine:         r,
		Config:         &config.Config{},
		AuthMiddleware: func(c *gin.Context) { c.Next() },
	}
	if err := m.Register(ctx); err != nil {
		t.Fatalf("register error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, w.Code)
	}
}

func TestMCPModule_Register_InvalidUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	m := New(func(c *gin.Context) { c.Next() })

	ctx := modules.Context{
		Engine: r,
		Config: &config.Config{MCP: config.MCPConfig{
			UpstreamURL: "://bad",
		}},
		AuthMiddleware: func(c *gin.Context) { c.Next() },
	}
	if err := m.Register(ctx); err == nil {
		t.Fatal("expected register error for invalid upstream URL")
	}
}

func TestMCPModule_Forwarding_StripsPrefixAndInjectsUpstreamAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type captured struct {
		path   string
		query  string
		auth   string
		apiKey string
	}
	capturedCh := make(chan captured, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedCh <- captured{
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			auth:   r.Header.Get("Authorization"),
			apiKey: r.Header.Get("X-Api-Key"),
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer upstream.Close()

	r := gin.New()
	m := New(func(c *gin.Context) { c.Next() })

	ctx := modules.Context{
		Engine: r,
		Config: &config.Config{MCP: config.MCPConfig{
			UpstreamURL:    upstream.URL,
			UpstreamAPIKey: "upstream-key",
		}},
		AuthMiddleware: func(c *gin.Context) { c.Next() },
	}
	if err := m.Register(ctx); err != nil {
		t.Fatalf("register error: %v", err)
	}
	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/mcp/v1/tools/list?x=1", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer client-key")
	req.Header.Set("X-Api-Key", "client-key")
	req.Header.Set("X-Goog-Api-Key", "client-goog-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp.StatusCode)
	}

	got := <-capturedCh
	if got.path != "/v1/tools/list" {
		t.Fatalf("unexpected upstream path: %s", got.path)
	}
	if got.query != "x=1" {
		t.Fatalf("unexpected upstream query: %s", got.query)
	}
	if got.auth != "Bearer upstream-key" {
		t.Fatalf("unexpected upstream authorization: %s", got.auth)
	}
	if got.apiKey != "upstream-key" {
		t.Fatalf("unexpected upstream x-api-key: %s", got.apiKey)
	}
}

func TestMCPModule_OnConfigUpdated_UpdatesAPIKeyWithoutRestart(t *testing.T) {
	gin.SetMode(gin.TestMode)

	auths := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auths <- r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer upstream.Close()

	r := gin.New()
	m := New(func(c *gin.Context) { c.Next() })
	ctx := modules.Context{
		Engine: r,
		Config: &config.Config{MCP: config.MCPConfig{
			UpstreamURL:    upstream.URL,
			UpstreamAPIKey: "key-1",
		}},
		AuthMiddleware: func(c *gin.Context) { c.Next() },
	}
	if err := m.Register(ctx); err != nil {
		t.Fatalf("register error: %v", err)
	}
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp1, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("first request error: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp1.StatusCode)
	}
	if got := <-auths; got != "Bearer key-1" {
		t.Fatalf("unexpected auth before update: %s", got)
	}

	if err := m.OnConfigUpdated(&config.Config{MCP: config.MCPConfig{UpstreamURL: upstream.URL, UpstreamAPIKey: "key-2"}}); err != nil {
		t.Fatalf("OnConfigUpdated error: %v", err)
	}

	resp2, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("second request error: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp2.StatusCode)
	}
	if got := <-auths; got != "Bearer key-2" {
		t.Fatalf("unexpected auth after update: %s", got)
	}
}

func TestMCPModule_OnConfigUpdated_SwitchesUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstreamOne := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("one"))
	}))
	defer upstreamOne.Close()

	upstreamTwo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("two"))
	}))
	defer upstreamTwo.Close()

	r := gin.New()
	m := New(func(c *gin.Context) { c.Next() })
	ctx := modules.Context{
		Engine: r,
		Config: &config.Config{MCP: config.MCPConfig{
			UpstreamURL: upstreamOne.URL,
		}},
		AuthMiddleware: func(c *gin.Context) { c.Next() },
	}
	if err := m.Register(ctx); err != nil {
		t.Fatalf("register error: %v", err)
	}
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp1, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("first request error: %v", err)
	}
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp1.StatusCode)
	}
	body1, err := io.ReadAll(resp1.Body)
	if err != nil {
		t.Fatalf("failed to read first response body: %v", err)
	}
	if string(body1) != "one" {
		t.Fatalf("unexpected first response body: %s", string(body1))
	}

	if err := m.OnConfigUpdated(&config.Config{MCP: config.MCPConfig{UpstreamURL: upstreamTwo.URL}}); err != nil {
		t.Fatalf("OnConfigUpdated error: %v", err)
	}

	resp2, err := http.Get(srv.URL + "/mcp")
	if err != nil {
		t.Fatalf("second request error: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, resp2.StatusCode)
	}
	body2, err := io.ReadAll(resp2.Body)
	if err != nil {
		t.Fatalf("failed to read second response body: %v", err)
	}
	if string(body2) != "two" {
		t.Fatalf("unexpected second response body: %s", string(body2))
	}
}

func TestStripMCPPrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: "/"},
		{name: "root", in: "/mcp", want: "/"},
		{name: "root-slash", in: "/mcp/", want: "/"},
		{name: "subpath", in: "/mcp/tools", want: "/tools"},
		{name: "non-mcp", in: "/v1", want: "/v1"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripMCPPrefix(tc.in); got != tc.want {
				t.Fatalf("stripMCPPrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
