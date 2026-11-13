// Package httptransport preserves selected transports while observing requests
// and preventing replay after ambiguous provider submission.
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
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, decoratorKey{}, decorator)
}

// WithoutDecorator suppresses intermediate helper decoration. A specialized
// executor that further configures its helper's transport must apply Decorate
// once, after its own protocol/pool policy is complete. Submission tracking is
// suppressed here too so it wraps that final transport exactly once.
func WithoutDecorator(ctx context.Context) context.Context {
	return context.WithValue(WithDecorator(ctx, nil), submissionKey{}, (*submissionState)(nil))
}

// InheritDecorator copies a decorator from fallback into ctx when ctx does not
// already specify a decorator or explicit suppression via WithoutDecorator.
func InheritDecorator(ctx, fallback context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if fallback == nil {
		return ctx
	}
	if _, ok := ctx.Value(decoratorKey{}).(Decorator); ok {
		return ctx
	}
	if decorator, ok := fallback.Value(decoratorKey{}).(Decorator); ok && decorator != nil {
		return WithDecorator(ctx, decorator)
	}
	return ctx
}

// Decorate applies observation and submission tracking after transport selection.
// Without either, the exact original client is returned. The selected pool, proxy
// and protocol policy survive; only the client value is copied.
func Decorate(ctx context.Context, client *http.Client) *http.Client {
	if ctx == nil || client == nil {
		return client
	}
	decorator, _ := ctx.Value(decoratorKey{}).(Decorator)
	state, _ := ctx.Value(submissionKey{}).(*submissionState)
	if decorator == nil && state == nil {
		return client
	}
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	wrapped := next
	if decorator != nil {
		if observed := decorator(next); observed != nil {
			wrapped = observed
		} else if state == nil {
			return client
		}
	}
	if state != nil {
		wrapped = &submissionTransport{next: wrapped, state: state}
	}
	clone := *client
	clone.Transport = wrapped
	return &clone
}
