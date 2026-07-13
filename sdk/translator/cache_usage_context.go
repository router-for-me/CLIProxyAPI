package translator

import "context"

type codexClaudeCacheWriteEstimateContextKey struct{}

// WithCodexClaudeCacheWriteEstimate records whether Codex responses may expose
// an estimated Anthropic cache-creation counter for this request. Passing false
// explicitly overrides an enabled value inherited from a parent context.
func WithCodexClaudeCacheWriteEstimate(ctx context.Context, enabled bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexClaudeCacheWriteEstimateContextKey{}, enabled)
}

// CodexClaudeCacheWriteEstimateEnabled reports the request-scoped policy.
// The default is false when no policy has been attached.
func CodexClaudeCacheWriteEstimateEnabled(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	enabled, _ := ctx.Value(codexClaudeCacheWriteEstimateContextKey{}).(bool)
	return enabled
}
