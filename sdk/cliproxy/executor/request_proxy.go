package executor

import (
	"context"
	"strings"
)

type requestProxyContextKey struct{}

// WithRequestProxyURL replaces the execution-scoped proxy override.
// An empty proxy clears any override inherited from the parent context.
func WithRequestProxyURL(ctx context.Context, proxyURL string) context.Context {
	proxyURL = strings.TrimSpace(proxyURL)
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestProxyContextKey{}, proxyURL)
}

// WithoutRequestProxyURL removes an execution-scoped proxy override.
// Credential refresh uses this so token exchange keeps the account or global proxy.
func WithoutRequestProxyURL(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, requestProxyContextKey{}, "")
}

// RequestProxyURL returns the execution-scoped proxy override, or empty when unset.
func RequestProxyURL(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	proxyURL, _ := ctx.Value(requestProxyContextKey{}).(string)
	return strings.TrimSpace(proxyURL)
}
