package policy

import (
	"net"
	"strings"
)

// IPMatches reports whether clientIP matches any entry in patterns. Each
// pattern is either a single IP address ("10.0.0.5") or a CIDR range
// ("10.0.0.0/8", "2001:db8::/32"). IPv4 and IPv6 are both supported. Returns
// the matched pattern (for diagnostics) when matched.
//
// This mirrors the management-token IP matcher
// (internal/api/handlers/management/mgmt_policy.go::ipMatches) so that API-key
// policy IP enforcement uses identical semantics. It is duplicated here to
// avoid an import cycle (middleware -> management).
//
// Malformed patterns are silently skipped so a single bad entry cannot lock
// out every caller. clientIP is the value from gin's c.ClientIP() (a string).
// It is parsed once here; invalid client-IP strings simply never match.
func IPMatches(clientIP string, patterns []string) (bool, string) {
	if clientIP == "" {
		return false, ""
	}
	ip := net.ParseIP(clientIP)
	if ip == nil {
		return false, ""
	}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// CIDR form: parse the network and test containment.
		if strings.Contains(p, "/") {
			_, network, err := net.ParseCIDR(p)
			if err != nil {
				continue
			}
			if network.Contains(ip) {
				return true, p
			}
			continue
		}
		// Single-IP form: compare canonical representations so e.g. "::1"
		// matches "::1" and "10.0.0.1" matches "10.0.0.1".
		patternIP := net.ParseIP(p)
		if patternIP != nil && patternIP.Equal(ip) {
			return true, p
		}
	}
	return false, ""
}

// ValidateIPPatterns returns an error message string when one or more entries
// in patterns are not valid single IPs or CIDR ranges. Returns "" when all
// entries are valid (or the list is empty). Used by the management API to
// reject malformed IP allowlist/blocklist entries at write time rather than
// silently skipping them at enforcement time.
func ValidateIPPatterns(patterns []string) string {
	for _, raw := range patterns {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if strings.Contains(p, "/") {
			if _, _, err := net.ParseCIDR(p); err != nil {
				return "invalid CIDR pattern: " + p
			}
			continue
		}
		if net.ParseIP(p) == nil {
			return "invalid IP pattern: " + p
		}
	}
	return ""
}
