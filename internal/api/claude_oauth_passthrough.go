package api

import (
	"crypto/subtle"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

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

func isConfiguredClientKey(key string, keys []string) bool {
	for _, k := range keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

// newAnthropicPassthroughProxy builds a reverse proxy that forwards requests unchanged
// to target. No timeouts are set after the upstream connection is established.
func newAnthropicPassthroughProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.Out.Host = target.Host
		},
		// Flush immediately so SSE streams reach the client without buffering.
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Errorf("claude oauth passthrough: upstream request %s %s failed: %v", r.Method, r.URL.Path, err)
			w.WriteHeader(http.StatusBadGateway)
		},
	}
}

// claudeOAuthPassthroughHandler forwards requests carrying the client's own Anthropic
// credential (OAuth token or Console API key) straight to api.anthropic.com when
// client.claude.anthropic-oauth-passthrough is enabled. Requests using a configured
// proxy API key, and all requests while the option is disabled, go to next.
func (s *Server) claudeOAuthPassthroughHandler(next http.Handler, proxy http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.getConfig()
		if cfg == nil || !cfg.Client.Claude.AnthropicOAuthPassthrough {
			next.ServeHTTP(w, r)
			return
		}
		credential := clientAnthropicCredential(r)
		if credential == "" || isConfiguredClientKey(credential, cfg.APIKeys) {
			next.ServeHTTP(w, r)
			return
		}
		log.Debugf("claude oauth passthrough: forwarding %s %s to %s", r.Method, r.URL.Path, anthropicPassthroughTarget.Host)
		proxy.ServeHTTP(w, r)
	})
}
