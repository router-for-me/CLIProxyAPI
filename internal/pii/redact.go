package pii

import (
	"regexp"
)

var (
	// bearerToken matches Authorization: Bearer <token> or just the bare token
	// in body text. Covers OpenAI-style sk-*, Anthropic-style sk-ant-*, and
	// generic base64-ish tokens.
	bearerToken = regexp.MustCompile(`(?i)(Bearer\s+)(sk-[A-Za-z0-9]{8,}|sk-ant-[A-Za-z0-9]{8,}|[A-Za-z0-9+/=]{40,})`)

	// apiKeyInline matches standalone API key patterns that appear in JSON
	// bodies or query strings ("key": "sk-...", api_key=...).
	apiKeyInline = regexp.MustCompile(`"([A-Za-z0-9+/=]{32,})"`)

	// email matches common email addresses.
	email = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

	// ipv4 matches dotted-decimal IP addresses (but not version numbers or
	// dotted-quad timestamps — keep it simple for the common case).
	ipv4 = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

	// urlWithAuth matches URLs that contain embedded credentials
	// (scheme://user:pass@host).
	urlWithAuth = regexp.MustCompile(`(https?://)[^:@\s]+:[^@\s]+@`)
)

// RedactPII replaces common PII patterns in s with placeholders.
// Designed for use on captured request/response bodies before returning
// them to the dashboard. Idempotent — safe to call on already-redacted text.
func RedactPII(s string) string {
	s = bearerToken.ReplaceAllString(s, `$1[REDACTED]`)
	s = apiKeyInline.ReplaceAllString(s, `"[REDACTED]"`)
	s = email.ReplaceAllString(s, `[EMAIL-REDACTED]`)
	s = ipv4.ReplaceAllString(s, `[IP-REDACTED]`)
	s = urlWithAuth.ReplaceAllString(s, `${1}[REDACTED]:[REDACTED]@`)
	return s
}

// IsLikelyPII returns true when the string contains any of the PII patterns
// that RedactPII would replace. Useful for the dashboard to show a hint that
// redaction was applied.
func IsLikelyPII(s string) bool {
	return bearerToken.MatchString(s) ||
		apiKeyInline.MatchString(s) ||
		email.MatchString(s) ||
		ipv4.MatchString(s) ||
		urlWithAuth.MatchString(s)
}

// RedactHeaders applies PII redaction to each value in a header map
// (map[string][]string, matching the bodySection struct used by the
// usage_event_bodies handler). Header names are not redacted.
func RedactHeaders(headers map[string][]string) map[string][]string {
	out := make(map[string][]string, len(headers))
	for k, vals := range headers {
		redacted := make([]string, len(vals))
		for i, v := range vals {
			redacted[i] = RedactPII(v)
		}
		out[k] = redacted
	}
	return out
}
