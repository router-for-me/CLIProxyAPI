package handlers

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// htmlErrorScanMaxBytes bounds tag stripping so a huge error page cannot
	// dominate the request that is already failing.
	htmlErrorScanMaxBytes = 32 * 1024
	// htmlErrorSummaryMaxRunes is the client-facing plain-text cap.
	htmlErrorSummaryMaxRunes = 200
)

var (
	htmlScriptStylePattern = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>|<style[^>]*>.*?</style>`)
	htmlCommentPattern     = regexp.MustCompile(`(?s)<!--.*?-->`)
	htmlTagPattern         = regexp.MustCompile(`<[^>]+>`)
)

// SummarizeHTMLClientMessage reports whether body is an HTML error page and,
// when it is, returns a short plain-text summary with tags removed. JSON
// documents are not HTML and return ok=false so callers can keep them intact.
func SummarizeHTMLClientMessage(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	trimmed = strings.TrimPrefix(trimmed, "\uFEFF")
	trimmed = strings.TrimSpace(trimmed)
	if json.Valid([]byte(trimmed)) || !isHTMLErrorBody(trimmed) {
		return "", false
	}

	scan := trimmed
	if len(scan) > htmlErrorScanMaxBytes {
		scan = scan[:htmlErrorScanMaxBytes]
		scan = trimPartialRune(scan)
	}
	cleaned := htmlScriptStylePattern.ReplaceAllString(scan, " ")
	cleaned = htmlCommentPattern.ReplaceAllString(cleaned, " ")
	cleaned = htmlTagPattern.ReplaceAllString(cleaned, " ")
	cleaned = html.UnescapeString(cleaned)
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	cleaned = strings.TrimSpace(cleaned)
	runes := []rune(cleaned)
	if len(runes) > htmlErrorSummaryMaxRunes {
		cleaned = strings.TrimSpace(string(runes[:htmlErrorSummaryMaxRunes]))
	}
	if cleaned == "" {
		cleaned = "upstream HTML error"
	}
	return cleaned, true
}

func clientFacingErrorMessage(errText string) string {
	if summary, ok := SummarizeHTMLClientMessage(errText); ok {
		return summary
	}
	return errText
}

func isHTMLErrorBody(trimmed string) bool {
	if trimmed == "" {
		return false
	}
	scan := trimmed
	if len(scan) > htmlErrorScanMaxBytes {
		scan = scan[:htmlErrorScanMaxBytes]
	}
	lower := strings.ToLower(scan)
	for _, marker := range []string{
		"<!doctype",
		"<html",
		"<head",
		"<body",
		"<title",
		"<h1",
		"<h2",
		"<h3",
		"<p>",
		"<p ",
		"<div",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func trimPartialRune(s string) string {
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}
