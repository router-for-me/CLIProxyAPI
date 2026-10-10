package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
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
		want       int
	}{
		{name: "ipv4 loopback", remoteAddr: "127.0.0.1:55555", want: http.StatusOK},
		{name: "ipv6 loopback", remoteAddr: "[::1]:55555", want: http.StatusOK},
		{name: "lan address", remoteAddr: "192.168.1.50:55555", want: http.StatusForbidden},
		{name: "public address", remoteAddr: "203.0.113.9:55555", want: http.StatusForbidden},
		{name: "loopback hostname not spoofed via header", remoteAddr: "10.0.0.8:55555", want: http.StatusForbidden},
		{name: "empty remote addr", remoteAddr: "", want: http.StatusForbidden},
		{name: "malformed remote addr", remoteAddr: "not-an-ip", want: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/v1/pool/status", nil)
			req.RemoteAddr = tc.remoteAddr
			// X-Forwarded-For must never grant loopback access.
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			router.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("RemoteAddr %q → status %d, want %d", tc.remoteAddr, recorder.Code, tc.want)
			}
		})
	}
}
