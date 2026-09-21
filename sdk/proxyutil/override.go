package proxyutil

import (
	"context"
	"strings"
)

// overrideKey is the context key used to carry a request-level proxy override.
type overrideKey struct{}

// WithOverride attaches a request-level proxy override to ctx.
//
// The override is used for a single outbound execution (for example a plugin
// driven host.model.execute call) and takes precedence over both the
// auth-specific proxy and the global proxy configuration.
func WithOverride(ctx context.Context, proxyURL string) context.Context {
	trimmed := strings.TrimSpace(proxyURL)
	if ctx == nil || trimmed == "" {
		return ctx
	}
	return context.WithValue(ctx, overrideKey{}, trimmed)
}

// Override returns the request-level proxy override carried by ctx, if any.
func Override(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(overrideKey{}).(string)
	return strings.TrimSpace(value)
}
