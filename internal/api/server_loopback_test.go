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
	router := gin.New()
	router.GET("/v1/pool/status", loopbackOnlyMiddleware(), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	cases := []struct {
		name       string
		remoteAddr string
		origin     string
		want       int
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
			req.RemoteAddr = tc.remoteAddr
			// X-Forwarded-For must never grant loopback access.
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			router.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("RemoteAddr %q origin %q → status %d, want %d", tc.remoteAddr, tc.origin, recorder.Code, tc.want)
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
