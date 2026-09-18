// Package httptransport exposes an additive, request-scoped observation seam.
// A decorator wraps the transport already selected by the executor: it must not
// replace provider fingerprints, proxy routing, pools, or HTTP protocol policy.
package httptransport

import (
	"context"
	"net/http"
)

type decoratorKey struct{}

// Decorator may wrap next to observe final HTTP requests and response bodies.
// It is called during client construction; implementations must be concurrency
// safe, must delegate to next, and must not retain secrets or consume bodies.
// A decorator does not observe WebSocket messages after an upgrade.
type Decorator func(next http.RoundTripper) http.RoundTripper

// WithDecorator attaches a decorator to one execution context. It does not
// enable request logging or modify executor credential/selection policy.
func WithDecorator(ctx context.Context, decorator Decorator) context.Context {
	return context.WithValue(ctx, decoratorKey{}, decorator)
}

// WithoutDecorator suppresses intermediate helper decoration. A specialized
// executor that further configures its helper's transport must apply Decorate
// once, after its own protocol/pool policy is complete.
func WithoutDecorator(ctx context.Context) context.Context { return WithDecorator(ctx, nil) }

// Decorate applies a context decorator AFTER transport selection. With no
// decorator, the exact original client is returned. Only the client value is
// copied; the original selected transport/pool and all client policy survive.
func Decorate(ctx context.Context, client *http.Client) *http.Client {
	if ctx == nil || client == nil {
		return client
	}
	decorator, _ := ctx.Value(decoratorKey{}).(Decorator)
	if decorator == nil {
		return client
	}
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	wrapped := decorator(next)
	if wrapped == nil {
		return client
	}
	clone := *client
	clone.Transport = wrapped
	return &clone
}
