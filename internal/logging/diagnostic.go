package logging

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	diagnosticLogRuneLimit     = 300
	diagnosticLogScanRuneLimit = 600
)

var (
	accessTokenExpiredLogPattern  = regexp.MustCompile(`(?i)access token expired`)
	sensitiveLogAssignmentPattern = regexp.MustCompile(`(?i)(["']?(?:access[\s_-]*token|refresh[\s_-]*token|id[\s_-]*token|api[\s_-]*key|client[\s_-]*secret|private[\s_-]*key|proxy[\s_-]*authorization|authorization|password|credential|token|secret)["']?\s*[:=]\s*)(?:(?:bearer|basic)\s+[^\s,;]+|"(?:\\.|[^"])*"|'(?:\\.|[^'])*'|[^\s,;&}\]]+)`)
	authorizationLogPattern       = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[^\s,;]+`)
	urlUserinfoLogPattern         = regexp.MustCompile(`(?i)(https?://)[^/\s@]+@`)
)

// SafeDiagnosticForLog returns a bounded, single-line diagnostic suitable for
// ordinary application logs. It preserves the access-token-expired signal while
// redacting credential values and URL userinfo.
func SafeDiagnosticForLog(message string) string {
	excerpt, sourceTruncated := diagnosticRunePrefix(message, diagnosticLogScanRuneLimit)
	if sourceTruncated && !accessTokenExpiredLogPattern.MatchString(excerpt) {
		if marker := accessTokenExpiredLogPattern.FindString(message); marker != "" {
			excerpt += " ... " + marker
		}
	}

	excerpt = strings.Join(strings.Fields(excerpt), " ")
	if excerpt == "" {
		return ""
	}
	excerpt = urlUserinfoLogPattern.ReplaceAllString(excerpt, `${1}[REDACTED]@`)
	excerpt = sensitiveLogAssignmentPattern.ReplaceAllString(excerpt, `${1}"[REDACTED]"`)
	excerpt = authorizationLogPattern.ReplaceAllString(excerpt, `${1} [REDACTED]`)

	return truncateDiagnosticLogExcerpt(excerpt, sourceTruncated)
}

func diagnosticRunePrefix(value string, limit int) (string, bool) {
	if limit <= 0 {
		return "", value != ""
	}
	count := 0
	for index := range value {
		if count == limit {
			return value[:index], true
		}
		count++
	}
	return value, false
}

func truncateDiagnosticLogExcerpt(message string, sourceTruncated bool) string {
	runes := []rune(message)
	if len(runes) <= diagnosticLogRuneLimit {
		if sourceTruncated {
			return message + "..."
		}
		return message
	}

	output := string(runes[:diagnosticLogRuneLimit])
	if match := accessTokenExpiredLogPattern.FindStringIndex(message); match != nil {
		markerStart := utf8.RuneCountInString(message[:match[0]])
		markerEnd := markerStart + utf8.RuneCountInString(message[match[0]:match[1]])
		if markerEnd > diagnosticLogRuneLimit {
			separator := " ... "
			prefixLimit := diagnosticLogRuneLimit - len([]rune(separator)) - (markerEnd - markerStart)
			if prefixLimit < 0 {
				prefixLimit = 0
			}
			output = string(runes[:prefixLimit]) + separator + string(runes[markerStart:markerEnd])
		}
	}
	return output + "..."
}
