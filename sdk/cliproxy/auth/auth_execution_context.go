package auth

import "context"

type authExecutionStateKey struct{}

// WithAuthExecutionState attaches server-owned request policy state. State must
// not contain credentials or a reusable HTTP/Gin context. A nil state leaves ctx
// unchanged; a non-nil state with a nil ctx uses context.Background.
func WithAuthExecutionState(ctx context.Context, state any) context.Context {
	if state == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, authExecutionStateKey{}, state)
}

// AuthExecutionState returns server-owned policy state without reading request
// headers, RPC metadata, or a mutable HTTP/Gin context.
func AuthExecutionState(ctx context.Context) any {
	if ctx == nil {
		return nil
	}
	return ctx.Value(authExecutionStateKey{})
}

// InheritAuthExecutionState copies only policy state from request to parent.
// Current request state takes precedence. Without request state, parent is
// returned unchanged. Parent cancellation, deadlines, and other values remain
// authoritative; cancellation from request is handled by the HTTP handler.
func InheritAuthExecutionState(parent, request context.Context) context.Context {
	state := AuthExecutionState(request)
	if state == nil {
		return parent
	}
	return WithAuthExecutionState(parent, state)
}
