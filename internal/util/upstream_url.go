package util

import "strings"

// JoinOpenAICompatUpstreamURL joins baseURL with an OpenAI-style endpoint
// suffix (e.g. "/chat/completions", "/v1/models", "/images/generations").
// Unlike a plain string concat, it auto-inserts the conventional "/v1"
// version segment when the configured base URL does not already carry one.
//
// This removes a long-standing foot-gun: the dashboard hint only said "the
// external OpenAI-compatible API endpoint", so some operators entered a
// base URL already carrying "/v1" (e.g. "https://openrouter.ai/api/v1")
// while others entered one without (e.g. "https://api.example.com"). The
// OpenAI-Compat executor and the remote-probe handler used to ALWAYS append
// their endpoint suffix verbatim, which meant one group hit a 404 from the
// upstream (double "/v1" or missing "/v1"). JoinOpenAICompatUpstreamURL
// makes the behaviour symmetric:
//
//   - Base URLs already ending in a version segment ("/v1", "/v2", ...) are
//     used unchanged, so existing configurations keep working.
//   - Base URLs without a version segment get "/v1" inserted before the
//     endpoint suffix.
//
// Callers should pass the suffix WITH a leading slash (it is normalized if
// missing).
func JoinOpenAICompatUpstreamURL(baseURL, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		// No endpoint to append — just normalize the trailing slash.
		return strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return suffix
	}
	// A version segment is the final path component matching v<number>
	// (e.g. "v1", "v2"). When present, the operator already pinned the API
	// version in their base URL and we must not add another "/v1".
	if seg := lastPathSegment(base); len(seg) >= 2 && seg[0] == 'v' && isAllDigits(seg[1:]) {
		return base + suffix
	}
	return base + "/v1" + suffix
}

// lastPathSegment returns the substring after the final "/" in path, or path
// itself when it contains no separator. Used by JoinOpenAICompatUpstreamURL
// to detect a trailing version segment without pulling in net/url.
func lastPathSegment(path string) string {
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		return path[i+1:]
	}
	return path
}

// isAllDigits reports whether s is non-empty and consists only of ASCII
// digits. Used by JoinOpenAICompatUpstreamURL to accept any "/vN" version.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}
