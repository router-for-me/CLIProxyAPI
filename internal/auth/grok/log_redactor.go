package grok

import (
	"regexp"

	log "github.com/sirupsen/logrus"
)

// jwtRe matches 3-segment JWT-like tokens (xxx.yyy.zzz where each segment is
// base64url-safe). xAI returns JWT access tokens; this pattern matches them.
var jwtRe = regexp.MustCompile(`(?:[A-Za-z0-9_-]{20,}\.){2}[A-Za-z0-9_-]{20,}`)

// bearerRe matches "Authorization: Bearer <opaque>" for non-JWT bearer
// tokens that wouldn't otherwise match jwtRe.
var bearerRe = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[A-Za-z0-9._\-+/=]+`)

// refreshTokenFieldRe redacts the value of any "refresh_token":"..." JSON-ish
// substring that might appear if a caller naively logs a TokenResponse.
var refreshTokenFieldRe = regexp.MustCompile(`(?i)(refresh_token"?\s*[:=]\s*"?)[A-Za-z0-9._\-+/=]+`)

// RedactTokens scans a string for token-shaped substrings and replaces them
// with "<redacted>". Public for unit testing.
func RedactTokens(s string) string {
	s = jwtRe.ReplaceAllString(s, "<redacted-jwt>")
	s = bearerRe.ReplaceAllString(s, "${1}<redacted-bearer>")
	s = refreshTokenFieldRe.ReplaceAllString(s, "${1}<redacted-refresh>")
	return s
}

// LogRedactorHook is a logrus.Hook that redacts token-shaped substrings from
// log message text and known token-named fields. It does not depend on log
// level — it runs on every fired entry.
type LogRedactorHook struct{}

// Levels reports that the hook should run on all log levels.
func (LogRedactorHook) Levels() []log.Level {
	return log.AllLevels
}

// Fire redacts the message text and any field whose key is in a small
// allowlist of "field names that obviously hold tokens". This is best-effort
// — callers should not log tokens at all, but we defend in depth.
func (LogRedactorHook) Fire(entry *log.Entry) error {
	entry.Message = RedactTokens(entry.Message)
	tokenFields := []string{"access_token", "refresh_token", "id_token", "token", "Authorization", "authorization"}
	for _, k := range tokenFields {
		if v, ok := entry.Data[k]; ok {
			if s, isStr := v.(string); isStr {
				redacted := RedactTokens(s)
				if redacted == s && len(s) > 12 {
					// String didn't match a pattern; redact anyway since the
					// field name explicitly suggests a token.
					redacted = "<redacted>"
				}
				entry.Data[k] = redacted
			} else if v != nil {
				entry.Data[k] = "<redacted>"
			}
		}
	}
	return nil
}

// InstallRedactor wires LogRedactorHook into the supplied logger. If the
// logger is nil, the package-default logrus logger is used. This is
// idempotent: re-installing does not stack hooks.
//
// Callers should invoke this once at process startup before any sensitive
// logging takes place.
func InstallRedactor(logger *log.Logger) {
	if logger == nil {
		logger = log.StandardLogger()
	}
	// Walk hooks at any level to detect an existing installation.
	for _, existing := range logger.Hooks[log.InfoLevel] {
		if _, ok := existing.(LogRedactorHook); ok {
			return
		}
	}
	logger.AddHook(LogRedactorHook{})
}
