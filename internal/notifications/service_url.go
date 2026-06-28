package notifications

import (
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
)

type serviceURLState struct {
	mu       sync.RWMutex
	observed string
}

var globalServiceURL serviceURLState

// ObserveHTTPRequest learns the externally visible service URL from a request.
func ObserveHTTPRequest(req *http.Request) {
	serviceURL := serviceURLFromRequest(req)
	if serviceURL == "" {
		return
	}
	globalServiceURL.mu.Lock()
	globalServiceURL.observed = serviceURL
	globalServiceURL.mu.Unlock()
}

// CurrentServiceURL returns the most recently observed public service URL.
func CurrentServiceURL() string {
	globalServiceURL.mu.RLock()
	defer globalServiceURL.mu.RUnlock()
	return globalServiceURL.observed
}

func serviceURLFromRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	proto, host := forwardedProtoHost(req.Header.Get("Forwarded"))
	if proto == "" {
		proto = firstHeaderValue(req.Header.Get("X-Forwarded-Proto"))
	}
	if proto == "" {
		proto = firstHeaderValue(req.Header.Get("X-Forwarded-Scheme"))
	}
	if proto == "" && strings.EqualFold(firstHeaderValue(req.Header.Get("X-Forwarded-Ssl")), "on") {
		proto = "https"
	}
	if proto == "" {
		if req.TLS != nil {
			proto = "https"
		} else {
			proto = "http"
		}
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	if proto != "http" && proto != "https" {
		return ""
	}

	if host == "" {
		host = firstHeaderValue(req.Header.Get("X-Forwarded-Host"))
	}
	if host == "" {
		host = strings.TrimSpace(req.Host)
	}
	host = normalizeObservedHost(host)
	if host == "" {
		return ""
	}
	return proto + "://" + host
}

func forwardedProtoHost(header string) (string, string) {
	first := firstHeaderValue(header)
	if first == "" {
		return "", ""
	}
	var proto, host string
	for _, part := range strings.Split(first, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"`)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "proto":
			proto = value
		case "host":
			host = value
		}
	}
	return proto, host
}

func firstHeaderValue(value string) string {
	if value == "" {
		return ""
	}
	first, _, _ := strings.Cut(value, ",")
	return strings.Trim(strings.TrimSpace(first), `"`)
}

func normalizeObservedHost(host string) string {
	host = firstHeaderValue(host)
	if host == "" || strings.Contains(host, "/") || strings.Contains(host, "\\") || strings.ContainsAny(host, "\r\n\t ") {
		return ""
	}
	parsed, err := url.Parse("http://" + host)
	if err != nil || parsed.Host == "" {
		return ""
	}
	if !isPublicObservedHostname(parsed.Hostname()) {
		return ""
	}
	return parsed.Host
}

func isPublicObservedHostname(hostname string) bool {
	hostname = strings.Trim(strings.ToLower(hostname), "[]")
	if hostname == "" || hostname == "localhost" || strings.HasSuffix(hostname, ".localhost") || strings.HasSuffix(hostname, ".local") {
		return false
	}
	addr, err := netip.ParseAddr(hostname)
	if err != nil {
		return true
	}
	return addr.IsGlobalUnicast() && !addr.IsPrivate() && !addr.IsLoopback() && !addr.IsLinkLocalUnicast()
}

func resetServiceURLForTest() {
	globalServiceURL.mu.Lock()
	globalServiceURL.observed = ""
	globalServiceURL.mu.Unlock()
}
