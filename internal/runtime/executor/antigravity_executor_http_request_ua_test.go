package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// TestAntigravityHTTPRequestUserAgent keeps a native Antigravity client UA (used by the
// Cloud Code passthrough) and replaces any other UA with the credential's default.
func TestAntigravityHTTPRequestUserAgent(t *testing.T) {
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-ua-auth",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	tests := []struct {
		name, inbound, want string
	}{
		{name: "native client", inbound: "antigravity/cli/1.5.12 (aidev_client)", want: "antigravity/cli/1.5.12 (aidev_client)"},
		{name: "foreign client", inbound: "curl/8.0", want: resolveUserAgent(auth)},
		{name: "missing", inbound: "", want: resolveUserAgent(auth)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req, errRequest := http.NewRequest(http.MethodPost, server.URL, nil)
			if errRequest != nil {
				t.Fatalf("NewRequest() error = %v", errRequest)
			}
			if test.inbound != "" {
				req.Header.Set("User-Agent", test.inbound)
			}
			resp, errDo := exec.HttpRequest(context.Background(), auth, req)
			if errDo != nil {
				t.Fatalf("HttpRequest() error = %v", errDo)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatalf("close response body: %v", errClose)
			}
			if gotUA != test.want {
				t.Fatalf("upstream User-Agent = %q, want %q", gotUA, test.want)
			}
		})
	}
}
