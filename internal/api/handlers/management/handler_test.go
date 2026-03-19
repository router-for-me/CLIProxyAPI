package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestMiddlewareUsesTrustedProxyAwareLocalhostDetection(t *testing.T) {
	gin.SetMode(gin.TestMode)

	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("GenerateFromPassword: %v", err)
	}

	tests := []struct {
		name           string
		remoteAddr     string
		forwardedFor   string
		forwarded      string
		trustedProxies []string
		wantStatus     int
	}{
		{name: "localhost ipv4 allowed", remoteAddr: "127.0.0.1:12345", wantStatus: http.StatusOK},
		{name: "localhost ipv6 allowed", remoteAddr: "[::1]:12345", wantStatus: http.StatusOK},
		{name: "remote spoofed localhost denied", remoteAddr: "192.168.1.10:12345", forwardedFor: "127.0.0.1", wantStatus: http.StatusForbidden},
		{name: "untrusted loopback proxy denied", remoteAddr: "127.0.0.1:12345", forwardedFor: "198.51.100.10", wantStatus: http.StatusForbidden},
		{
			name:           "trusted loopback proxy respects forwarded remote client",
			remoteAddr:     "127.0.0.1:12345",
			forwardedFor:   "198.51.100.10",
			trustedProxies: []string{"127.0.0.1/32"},
			wantStatus:     http.StatusForbidden,
		},
		{
			name:           "trusted loopback proxy allows forwarded localhost client",
			remoteAddr:     "127.0.0.1:12345",
			forwarded:      "for=127.0.0.1",
			trustedProxies: []string{"127.0.0.1/32"},
			wantStatus:     http.StatusOK,
		},
		{name: "invalid remote addr denied", remoteAddr: "not-a-valid-remote-addr", wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandlerWithoutConfigFilePath(&config.Config{
				TrustedProxies: tt.trustedProxies,
				RemoteManagement: config.RemoteManagement{
					AllowRemote: false,
					SecretKey:   string(hash),
				},
			}, nil)

			r := gin.New()
			r.Use(h.Middleware())
			r.GET("/v0/management/test", func(c *gin.Context) {
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/v0/management/test", nil)
			req.RemoteAddr = tt.remoteAddr
			req.Header.Set("X-Management-Key", "secret")
			if tt.forwardedFor != "" {
				req.Header.Set("X-Forwarded-For", tt.forwardedFor)
			}
			if tt.forwarded != "" {
				req.Header.Set("Forwarded", tt.forwarded)
			}

			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d, body=%s", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}
