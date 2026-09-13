package executor

import (
	"context"
	"net/http"
	"strings"
)

type nativeClaudeHeadersKey struct{}

// NativeClaudeProtocolHeaders copies the reviewed native client's protocol and
// request-correlation headers. Account credentials, session identity, arbitrary
// extensions and connection-scoped headers are never part of this boundary.
// Keep this list explicit: new client headers require compatibility review.
func NativeClaudeProtocolHeaders(src http.Header) http.Header {
	dst := make(http.Header)
	scoped := make(map[string]bool)
	for name, values := range src {
		if strings.EqualFold(name, "Connection") {
			for _, value := range values {
				for _, token := range strings.Split(value, ",") {
					scoped[http.CanonicalHeaderKey(strings.TrimSpace(token))] = true
				}
			}
		}
	}
	for name, values := range src {
		key := http.CanonicalHeaderKey(name)
		if scoped[key] {
			continue
		}
		switch key {
		case "Accept", "Accept-Encoding", "Content-Type", "User-Agent",
			"Anthropic-Version", "Anthropic-Beta", "Anthropic-Dangerous-Direct-Browser-Access",
			"X-App", "X-Client-Request-Id", "X-Client-App",
			"X-Stainless-Lang", "X-Stainless-Package-Version", "X-Stainless-Os", "X-Stainless-Arch",
			"X-Stainless-Runtime", "X-Stainless-Runtime-Version", "X-Stainless-Retry-Count",
			"X-Stainless-Timeout", "X-Stainless-Async",
			"X-Claude-Code-Agent-Id", "X-Claude-Code-Parent-Agent-Id":
			dst[key] = append(dst[key], values...)
		}
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
