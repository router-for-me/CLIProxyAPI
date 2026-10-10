package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestLoopbackOnlyMiddleware(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)

	cases := []struct {
		name         string
		remoteAddr   string
		origin       string
		host         string
		hostSet      bool
		allowedHosts []string
		want         int
	}{
		{name: "ipv4 loopback", remoteAddr: "127.0.0.1:55555", want: http.StatusOK},
		{name: "ipv6 loopback", remoteAddr: "[::1]:55555", want: http.StatusOK},
		{name: "lan address", remoteAddr: "192.168.1.50:55555", want: http.StatusForbidden},
		{name: "public address", remoteAddr: "203.0.113.9:55555", want: http.StatusForbidden},
		{name: "loopback hostname not spoofed via header", remoteAddr: "10.0.0.8:55555", want: http.StatusForbidden},
		{name: "empty remote addr", remoteAddr: "", want: http.StatusForbidden},
		{name: "malformed remote addr", remoteAddr: "not-an-ip", want: http.StatusForbidden},
		{name: "loopback with browser origin", remoteAddr: "127.0.0.1:55555", origin: "http://localhost:8080", want: http.StatusForbidden},
		{name: "remote with origin", remoteAddr: "192.168.1.50:55555", origin: "https://example.com", want: http.StatusForbidden},
		{name: "localhost host header", remoteAddr: "127.0.0.1:55555", host: "localhost:8317", hostSet: true, want: http.StatusOK},
		{name: "localhost without port", remoteAddr: "127.0.0.1:55555", host: "localhost", hostSet: true, want: http.StatusOK},
		{name: "localhost uppercase", remoteAddr: "127.0.0.1:55555", host: "LOCALHOST:8317", hostSet: true, want: http.StatusOK},
		{name: "loopback ipv4 host header", remoteAddr: "127.0.0.1:55555", host: "127.0.0.1:8317", hostSet: true, want: http.StatusOK},
		{name: "loopback ipv6 host header", remoteAddr: "[::1]:55555", host: "[::1]:8317", hostSet: true, want: http.StatusOK},
		{name: "rebound hostile hostname", remoteAddr: "127.0.0.1:55555", host: "attacker.example.com", hostSet: true, want: http.StatusForbidden},
		{name: "rebound hostname with port", remoteAddr: "127.0.0.1:55555", host: "attacker.example.com:443", hostSet: true, want: http.StatusForbidden},
		{name: "empty host header", remoteAddr: "127.0.0.1:55555", hostSet: true, want: http.StatusForbidden},
		{name: "nonloopback ip host", remoteAddr: "127.0.0.1:55555", host: "192.168.1.1:8317", hostSet: true, want: http.StatusForbidden},
		{name: "configured custom hostname", remoteAddr: "127.0.0.1:55555", host: "status.lan:8317", hostSet: true, allowedHosts: []string{"status.lan"}, want: http.StatusOK},
		{name: "configured hostname entry with port", remoteAddr: "127.0.0.1:55555", host: "status.lan", hostSet: true, allowedHosts: []string{"status.lan:9443"}, want: http.StatusOK},
		{name: "configured bracketed hostname entry", remoteAddr: "127.0.0.1:55555", host: "status.lan:8317", hostSet: true, allowedHosts: []string{"[status.lan]"}, want: http.StatusOK},
		{name: "unconfigured custom hostname", remoteAddr: "127.0.0.1:55555", host: "status.lan:8317", hostSet: true, allowedHosts: []string{"other.lan"}, want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{cfg: &config.Config{PoolStatusAllowedHosts: tc.allowedHosts}}
			router := gin.New()
			router.GET("/v1/pool/status", s.loopbackOnlyMiddleware(), func(c *gin.Context) {
				c.Status(http.StatusOK)
			})
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.hostSet {
				req.Host = tc.host
			} else {
				req.Host = "localhost"
			}
			// X-Forwarded-For must never grant loopback access.
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			router.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("RemoteAddr %q origin %q host %q → status %d, want %d", tc.remoteAddr, tc.origin, tc.host, recorder.Code, tc.want)
			}
		})
	}
}

func TestIsExampleAPIKeySafeModeProxyPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/pool/status", false},
		{"/v1", true},
		{"/v1/chat/completions", true},
		{"/v1beta/models", true},
		{"/v1beta/models/gpt-5:generateContent", true},
		{"/openai/v1/videos", true},
		{"/openai/v1/video/generations", true},
		{"/healthz", false},
	}
	for _, tc := range cases {
		if got := isExampleAPIKeySafeModeProxyPath(tc.path); got != tc.want {
			t.Fatalf("isExampleAPIKeySafeModeProxyPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestHomeHeartbeatMiddlewarePoolStatusExempt(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)
	s := &Server{cfg: &config.Config{Home: config.HomeConfig{Enabled: true}}}
	router := gin.New()
	router.GET("/v1/pool/status", s.homeHeartbeatMiddleware(), func(c *gin.Context) { c.Status(http.StatusOK) })
	router.GET("/v1/models", s.homeHeartbeatMiddleware(), func(c *gin.Context) { c.Status(http.StatusOK) })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("/v1/pool/status under down heartbeat = %d, want %d", recorder.Code, http.StatusOK)
	}

	recorder = httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("/v1/models under down heartbeat = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}
