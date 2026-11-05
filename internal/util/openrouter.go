package util

import (
	"net/url"
	"strings"
)

// IsOpenRouterURL reports whether rawURL points at OpenRouter (openrouter.ai or a subdomain).
func IsOpenRouterURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai")
}
