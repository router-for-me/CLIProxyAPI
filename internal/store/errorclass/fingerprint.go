package errorclass

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

var (
	reUUID  = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	reHex   = regexp.MustCompile(`[0-9a-fA-F]{16,}`)
	reQuote = regexp.MustCompile(`"[^"]{24,}"`)
	// reVarID matches short variable identifiers like "abc-12", "req_42", or
	// "job9" — a letter-led token glued to a digit run by an optional separator.
	// These are request/job ids embedded in provider messages, not prose.
	reVarID = regexp.MustCompile(`(?i)\b[a-z][a-z0-9]{0,15}[-_]?[0-9]+\b`)
	reDigit = regexp.MustCompile(`\d+`)
	reWS    = regexp.MustCompile(`\s+`)
)

// Fingerprint returns a short stable hash grouping structurally-identical
// errors. The message is normalised first so messages that differ only in
// embedded ids/numbers/tokens collapse to the same fingerprint.
func Fingerprint(class, provider, model, message string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		class, provider, model, normalizeMessage(message),
	}, "\x00")))
	return hex.EncodeToString(h[:])[:16]
}

// normalizeMessage replaces variable tokens with placeholders, lowercases, and
// collapses whitespace so two messages with the same structure compare equal.
// Placeholder substitution order: <uuid>, <str>, <hex>, <id>, then # for bare
// digit runs.
func normalizeMessage(msg string) string {
	s := msg
	s = reUUID.ReplaceAllString(s, "<uuid>")
	s = reQuote.ReplaceAllString(s, "<str>")
	s = reHex.ReplaceAllString(s, "<hex>")
	s = reVarID.ReplaceAllString(s, "<id>")
	s = reDigit.ReplaceAllString(s, "#")
	s = strings.ToLower(s)
	s = reWS.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}
