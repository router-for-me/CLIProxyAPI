package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

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

func TestClaudeOAuthPassthroughHandler(t *testing.T) {
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
	cfg.APIKeys = []string{"sk-ant-configured-proxy-key"}
	s := &Server{cfg: cfg}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	h := s.claudeOAuthPassthroughHandler(next, newAnthropicPassthroughProxy(target))

	send := func(header, value string) *httptest.ResponseRecorder {
		upstreamAuth, upstreamPath, upstreamBody, upstreamHost = "", "", "", ""
		r := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(`{"model":"claude-sonnet-5-5"}`))
		r.Header.Set(header, value)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Disabled: OAuth token falls through to the normal pipeline.
	if w := send("Authorization", "Bearer sk-ant-oat01-abc"); w.Code != http.StatusTeapot || upstreamPath != "" {
		t.Fatalf("disabled: code=%d upstreamPath=%q", w.Code, upstreamPath)
	}

	cfg.Client.Claude.AnthropicOAuthPassthrough = true

	// Enabled: OAuth token is forwarded unchanged.
	w := send("Authorization", "Bearer sk-ant-oat01-abc")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "message_stop") {
		t.Fatalf("passthrough: code=%d body=%q", w.Code, w.Body.String())
	}
	if upstreamAuth != "Bearer sk-ant-oat01-abc" || upstreamPath != "/v1/messages?beta=true" ||
		upstreamBody != `{"model":"claude-sonnet-5-5"}` || upstreamHost != target.Host {
		t.Fatalf("passthrough request mismatch: auth=%q path=%q body=%q host=%q", upstreamAuth, upstreamPath, upstreamBody, upstreamHost)
	}

	// Enabled: proxy keys (including one that happens to use the sk-ant- prefix) stay local.
	for _, key := range []string{"sk-cpa-123", "sk-ant-configured-proxy-key"} {
		if w := send("Authorization", "Bearer "+key); w.Code != http.StatusTeapot || upstreamPath != "" {
			t.Fatalf("proxy key %q: code=%d upstreamPath=%q", key, w.Code, upstreamPath)
		}
	}
}
