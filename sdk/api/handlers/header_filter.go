package handlers

import (
	"net/http"
	"strings"
)

// gatewayHeaderPrefixes lists header name prefixes injected by known AI gateway
// proxies. Claude Code's client-side telemetry detects these and reports the
// gateway type, so we strip them from upstream responses to avoid detection.
var gatewayHeaderPrefixes = []string{
	"x-litellm-",
	"helicone-",
	"x-portkey-",
	"cf-aig-",
	"x-kong-",
	"x-bt-",
}

// hopByHopHeaders lists RFC 7230 Section 6.1 hop-by-hop headers that MUST NOT
// be forwarded by proxies, plus security-sensitive headers that should not leak.
var hopByHopHeaders = map[string]struct{}{
	// RFC 7230 hop-by-hop
	"Connection":          {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
	// Security-sensitive
	"Set-Cookie": {},
	// CPA-managed (set by handlers, not upstream)
	"Content-Length":   {},
	"Content-Encoding": {},
}

// FilterUpstreamHeaders returns a copy of src with hop-by-hop and security-sensitive
// headers removed. Returns nil if src is nil or empty after filtering.
func FilterUpstreamHeaders(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	connectionScoped := connectionScopedHeaders(src)
	dst := make(http.Header)
	for key, values := range src {
		canonicalKey := http.CanonicalHeaderKey(key)
		if _, blocked := hopByHopHeaders[canonicalKey]; blocked {
			continue
		}
		if _, scoped := connectionScoped[canonicalKey]; scoped {
			continue
		}
		// Strip headers injected by known AI gateway proxies to avoid
		// Claude Code client-side gateway detection.
		lowerKey := strings.ToLower(key)
		gatewayMatch := false
		for _, prefix := range gatewayHeaderPrefixes {
			if strings.HasPrefix(lowerKey, prefix) {
				gatewayMatch = true
				break
			}
		}
		if gatewayMatch {
			continue
		}
		dst[key] = values
	}
	if len(dst) == 0 {
		return nil
	}
	return dst
}

func connectionScopedHeaders(src http.Header) map[string]struct{} {
	scoped := make(map[string]struct{})
	for _, rawValue := range src.Values("Connection") {
		for _, token := range strings.Split(rawValue, ",") {
			headerName := strings.TrimSpace(token)
			if headerName == "" {
				continue
			}
			scoped[http.CanonicalHeaderKey(headerName)] = struct{}{}
		}
	}
	return scoped
}

// WriteUpstreamHeaders writes filtered upstream headers to the gin response writer.
// Headers already set by CPA (e.g., Content-Type) are NOT overwritten.
func WriteUpstreamHeaders(dst http.Header, src http.Header) {
	if src == nil {
		return
	}
	for key, values := range src {
		// Don't overwrite headers already set by CPA handlers
		if dst.Get(key) != "" {
			continue
		}
		for _, v := range values {
			dst.Add(key, v)
		}
	}
}
