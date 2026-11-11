package api

import (
	"crypto/subtle"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// anthropicPassthroughTarget is the fixed upstream for client-owned Anthropic credentials.
var anthropicPassthroughTarget = &url.URL{Scheme: "https", Host: "api.anthropic.com"}

// anthropicCredentialPrefix marks Anthropic OAuth tokens and Console API keys.
const anthropicCredentialPrefix = "sk-ant-"

// clientAnthropicCredential returns the Anthropic credential carried by the request,
// or an empty string when the request does not carry one.
func clientAnthropicCredential(r *http.Request) string {
	if key := strings.TrimSpace(r.Header.Get("X-Api-Key")); strings.HasPrefix(key, anthropicCredentialPrefix) {
		return key
	}
	authz := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(authz) > len("Bearer ") && strings.EqualFold(authz[:len("Bearer ")], "Bearer ") {
		if token := strings.TrimSpace(authz[len("Bearer "):]); strings.HasPrefix(token, anthropicCredentialPrefix) {
			return token
		}
	}
	return ""
}

// isConfiguredClientKey reports whether key matches a configured access key. Configured
// keys are trimmed first, like the built-in access provider does before accepting them.
func isConfiguredClientKey(key string, keys []string) bool {
	for _, k := range keys {
		trimmed := strings.TrimSpace(k)
		if trimmed == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(trimmed), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

// passthroughTransport is a config-aware http.RoundTripper. It follows requests.proxy-url
// (including "direct" and SOCKS5/HTTP(S) proxies) and rebuilds itself when the value
// changes through a configuration reload.
type passthroughTransport struct {
	proxyURL func() string

	mu      sync.Mutex
	current string
	rt      http.RoundTripper
	built   bool
}

func (t *passthroughTransport) roundTripper() http.RoundTripper {
	raw := strings.TrimSpace(t.proxyURL())

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.built && raw == t.current {
		return t.rt
	}
	if old, ok := t.rt.(interface{ CloseIdleConnections() }); ok && t.rt != http.DefaultTransport {
		old.CloseIdleConnections()
	}
	transport, _, errBuild := proxyutil.BuildHTTPTransport(raw)
	switch {
	case errBuild != nil:
		log.Errorf("claude oauth passthrough: invalid requests.proxy-url, using default transport: %v", errBuild)
		t.rt = http.DefaultTransport
	case transport == nil:
		t.rt = http.DefaultTransport
	default:
		t.rt = transport
	}
	t.current = raw
	t.built = true
	return t.rt
}

// RoundTrip implements http.RoundTripper.
func (t *passthroughTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.roundTripper().RoundTrip(req)
}

// newAnthropicPassthroughProxy builds a reverse proxy that forwards requests unchanged
// to target through transport. No timeouts are set after the upstream connection is
// established.
func newAnthropicPassthroughProxy(target *url.URL, transport http.RoundTripper) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
		Transport: transport,
		// Flush immediately so SSE streams reach the client without buffering.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Errorf("claude oauth passthrough: upstream request %s %s failed: %v", r.Method, r.URL.Path, err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
}

// newDefaultAnthropicPassthroughProxy builds the production proxy whose transport follows
// the live requests.proxy-url setting.
func (s *Server) newDefaultAnthropicPassthroughProxy() *httputil.ReverseProxy {
	transport := &passthroughTransport{proxyURL: func() string {
		if cfg := s.getConfig(); cfg != nil {
			return cfg.ProxyURL
		}
		return ""
	}}
	return newAnthropicPassthroughProxy(anthropicPassthroughTarget, transport)
}

// claudeOAuthPassthroughMiddleware forwards requests carrying the client's own Anthropic
// credential (OAuth token or Console API key) straight to api.anthropic.com when
// client.claude.anthropic-oauth-passthrough is enabled. It is registered after the
// server-wide gates (request body limit, Home heartbeat, safe mode, CORS, logging) so
// those still apply, and before routing, so route authentication and translation are
// skipped. Requests using a configured proxy API key, and all requests while the option
// is disabled, continue down the normal chain.
func (s *Server) claudeOAuthPassthroughMiddleware(proxy http.Handler) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := s.getConfig()
		if cfg == nil || !cfg.Client.Claude.AnthropicOAuthPassthrough {
			c.Next()
			return
		}
		credential := clientAnthropicCredential(c.Request)
		if credential == "" || isConfiguredClientKey(credential, cfg.APIKeys) {
			c.Next()
			return
		}
		log.Debugf("claude oauth passthrough: forwarding %s %s to %s", c.Request.Method, c.Request.URL.Path, anthropicPassthroughTarget.Host)
		proxy.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}
}
