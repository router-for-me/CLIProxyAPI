package logging

import "context"

type deferredAPIRequestCaptureDisabledKey struct{}

// WithDeferredAPIRequestCaptureDisabled skips error-only upstream request capture
// for this context and its descendants. Full request logging is unaffected.
// Connection-scoped contexts keep this policy even when asynchronous callbacks
// outlive the handler and its pooled Gin context is reused.
func WithDeferredAPIRequestCaptureDisabled(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, deferredAPIRequestCaptureDisabledKey{}, true)
}

// DeferredAPIRequestCaptureDisabled reports whether error-only capture is disabled.
func DeferredAPIRequestCaptureDisabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	disabled, _ := ctx.Value(deferredAPIRequestCaptureDisabledKey{}).(bool)
	return disabled
}
