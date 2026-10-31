package common

import (
	"context"
	"os"
)

type codexCommentaryKey struct{}

// WithVisibleCodexCommentary snapshots the display policy for one request.
func WithVisibleCodexCommentary(ctx context.Context, enabled bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, codexCommentaryKey{}, enabled)
}

// VisibleCodexCommentary reports the per-request display policy. The environment
// fallback supports SDK-only users and existing deployments of the opt-in mode.
func VisibleCodexCommentary(ctx context.Context) bool {
	if ctx != nil {
		if enabled, ok := ctx.Value(codexCommentaryKey{}).(bool); ok {
			return enabled
		}
	}
	return os.Getenv("CPA_VISIBLE_CODEX_COMMENTARY") == "1"
}
