package executor

import (
	"context"
	"net/http"
)

type roundTripperContextKey struct{}

// WithRoundTripper attaches a per-auth HTTP RoundTripper to the context so
// downstream executors can reuse the credential's configured transport (e.g. a
// proxy). It uses an unexported typed key to avoid cross-package collisions.
func WithRoundTripper(ctx context.Context, rt http.RoundTripper) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if rt == nil {
		return ctx
	}
	return context.WithValue(ctx, roundTripperContextKey{}, rt)
}

// RoundTripper returns the per-auth HTTP RoundTripper previously stored with
// WithRoundTripper, or nil when none is present.
func RoundTripper(ctx context.Context) http.RoundTripper {
	if ctx == nil {
		return nil
	}
	rt, _ := ctx.Value(roundTripperContextKey{}).(http.RoundTripper)
	return rt
}

type downstreamWebsocketContextKey struct{}

// WithDownstreamWebsocket marks the current request as coming from a downstream websocket connection.
func WithDownstreamWebsocket(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, downstreamWebsocketContextKey{}, true)
}

// DownstreamWebsocket reports whether the current request originates from a downstream websocket connection.
func DownstreamWebsocket(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(downstreamWebsocketContextKey{})
	enabled, ok := raw.(bool)
	return ok && enabled
}
