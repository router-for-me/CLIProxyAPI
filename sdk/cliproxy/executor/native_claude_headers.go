package executor

import (
	"context"
	"net/http"
	"strings"
)

type nativeClaudeHeadersKey struct{}

var nativeClaudeExcludedHeaders = map[string]struct{}{
	// The selected subscription supplies these credentials and account identities.
	"Authorization":                   {},
	"Anthropic-Organization-Id":       {},
	"Anthropic-User-Profile-Id":       {},
	"Anthropic-Workspace-Id":          {},
	"Cookie":                          {},
	"Cookie2":                         {},
	"Forwarded":                       {},
	"Proxy-Authorization":             {},
	"Via":                             {},
	"X-Account-Id":                    {},
	"X-Api-Key":                       {},
	"X-Claude-Code-Ide-Authorization": {},
	"X-Organization-Uuid":             {},
	"X-Real-Ip":                       {},
	"X-Trusted-Device-Token":          {},

	// Hop-by-hop and framing headers belong to each HTTP connection, not the
	// end-to-end native request.
	"Connection":         {},
	"Content-Length":     {},
	"Keep-Alive":         {},
	"Proxy-Authenticate": {},
	"Proxy-Connection":   {},
	"Te":                 {},
	"Trailer":            {},
	"Transfer-Encoding":  {},
	"Upgrade":            {},
}

// NativeClaudeProtocolHeaders copies the native client's end-to-end request
// headers without maintaining a version-specific allowlist. The selected
// subscription replaces only credentials and account identity; HTTP framing,
// hop-by-hop headers, and headers named by Connection remain scoped to the
// incoming connection. The Claude Code session ID is conversation state, not
// account identity, and remains attached when the selected credential changes.
func NativeClaudeProtocolHeaders(src http.Header) http.Header {
	dst := make(http.Header)
	scoped := make(map[string]struct{})
	for name, values := range src {
		if strings.EqualFold(name, "Connection") {
			for _, value := range values {
				for _, token := range strings.Split(value, ",") {
					if token = strings.TrimSpace(token); token != "" {
						scoped[http.CanonicalHeaderKey(token)] = struct{}{}
					}
				}
			}
		}
	}
	for name, values := range src {
		key := http.CanonicalHeaderKey(name)
		if _, excluded := nativeClaudeExcludedHeaders[key]; excluded {
			continue
		}
		if strings.HasPrefix(strings.ToLower(key), "x-forwarded-") {
			continue
		}
		if _, connectionScoped := scoped[key]; connectionScoped {
			continue
		}
		dst[key] = append(dst[key], values...)
	}
	return dst
}

// WithNativeClaudeProtocolHeaders is an explicit in-process opt-in for a native
// Claude adapter, not a client-controlled HTTP flag. The Claude executor keeps
// these measured headers instead of synthesizing a different client version.
// It still supplies the selected credential and its own account/session identity.
func WithNativeClaudeProtocolHeaders(ctx context.Context, headers http.Header) context.Context {
	return context.WithValue(ctx, nativeClaudeHeadersKey{}, NativeClaudeProtocolHeaders(headers))
}

// NativeClaudeProtocolHeadersFromContext returns a copy to prevent concurrent
// execution or retries from modifying the immutable incoming request snapshot.
func NativeClaudeProtocolHeadersFromContext(ctx context.Context) (http.Header, bool) {
	headers, ok := ctx.Value(nativeClaudeHeadersKey{}).(http.Header)
	if !ok {
		return nil, false
	}
	return headers.Clone(), true
}
