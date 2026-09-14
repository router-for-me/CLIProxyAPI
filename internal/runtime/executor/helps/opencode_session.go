// Package helps provides shared helpers for provider executors.
package helps

import (
	"net/http"
	"net/url"
	"strings"
)

const openCodeSessionHeader = "x-opencode-session"

// isOpenCodeGoUpstream reports whether the target is the OpenCode (Go/Zen) gateway.
// The provider name covers self-hosted mirrors of the endpoint, the host covers
// entries configured under a different name.
func isOpenCodeGoUpstream(targetURL, provider string) bool {
	if parsed, errParse := url.Parse(strings.TrimSpace(targetURL)); errParse == nil {
		// Exact host or a subdomain: a bare suffix would also match lookalikes
		// such as evilopencode.ai, matching IsAnthropicUpstreamURL's exact-host policy.
		host := strings.ToLower(parsed.Hostname())
		if host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(provider), "opencode")
}

// ApplyOpenCodeSessionHeaders sets the session header an OpenCode upstream rejects
// requests for. The client's own header wins; otherwise the value comes from the
// routing session, because a client that did not name its provider "opencode*" sends
// x-session-affinity instead and CPA never forwards client headers on its own.
//
// A header already present (operator-configured) wins, and other upstreams are left
// untouched so session identifiers do not leak to them.
func ApplyOpenCodeSessionHeaders(r *http.Request, targetURL, provider string, incoming http.Header, sessionID string) {
	if r == nil || !isOpenCodeGoUpstream(targetURL, provider) || r.Header.Get(openCodeSessionHeader) != "" {
		return
	}
	value := strings.TrimSpace(incoming.Get(openCodeSessionHeader))
	if value == "" {
		// Strip the routing prefix (affinity:, opencode:, claude:, ...) so the upstream
		// sees the same identifier the client would have sent directly.
		value = strings.TrimSpace(sessionID)
		if _, rest, found := strings.Cut(value, ":"); found {
			value = strings.TrimSpace(rest)
		}
	}
	if value == "" {
		return
	}
	r.Header.Set(openCodeSessionHeader, value)
}
