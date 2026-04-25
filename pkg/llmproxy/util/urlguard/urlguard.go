// Package urlguard provides outbound URL validation against a hostname
// allowlist to mitigate Server-Side Request Forgery (SSRF) — CodeQL
// rule go/request-forgery.
//
// Each subsystem that issues outbound HTTP from an URL whose components
// (region, base URL, etc.) originate from configuration or auth credentials
// MUST run the URL through ValidateOutboundURL before passing it to
// http.NewRequest / http.Get / client.Do.
//
// The allowlist is a set of suffix patterns matched against the parsed URL
// hostname. Patterns starting with "." match any subdomain
// (e.g. ".amazonaws.com" matches "oidc.us-east-1.amazonaws.com" but not
// "evilamazonaws.com"). Exact hostnames are also supported.
package urlguard

import (
	"fmt"
	"net/url"
	"strings"
)

// Allowlist is the canonical set of hostname suffix patterns the proxy is
// permitted to dial. Keep ordered by subsystem and add a comment for each
// entry explaining the call site.
//
// Patterns:
//   - ".example.com"  → any subdomain of example.com
//   - "host.example"  → exact hostname only
var Allowlist = []string{
	// AWS SSO OIDC + CodeWhisperer (Kiro auth flows)
	".amazonaws.com",
	// Google Antigravity / Cloud Code (executor base URLs)
	".googleapis.com",
	// Google OAuth token endpoint (antigravity refresh)
	"oauth2.googleapis.com",
}

// ValidateOutboundURL parses rawURL and returns it unchanged if the host
// matches any entry in Allowlist. Otherwise it returns an error describing
// the rejected host. Schemes other than http/https are always rejected.
//
// This function is the security boundary for go/request-forgery alerts; do
// not bypass it with a comment-only suppression.
func ValidateOutboundURL(rawURL string) (string, error) {
	return ValidateOutboundURLAgainst(rawURL, Allowlist)
}

// ValidateOutboundURLAgainst is the testable form of ValidateOutboundURL
// that accepts an explicit allowlist.
func ValidateOutboundURLAgainst(rawURL string, allowlist []string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("urlguard: empty URL")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("urlguard: parse %q: %w", rawURL, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", fmt.Errorf("urlguard: scheme %q not allowed", parsed.Scheme)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", fmt.Errorf("urlguard: missing host in %q", rawURL)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("urlguard: userinfo not allowed in outbound URL")
	}
	for _, pattern := range allowlist {
		p := strings.ToLower(strings.TrimSpace(pattern))
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, ".") {
			suffix := p[1:]
			if host == suffix || strings.HasSuffix(host, p) {
				return trimmed, nil
			}
			continue
		}
		if host == p {
			return trimmed, nil
		}
	}
	return "", fmt.Errorf("urlguard: host %q not in allowlist", host)
}
