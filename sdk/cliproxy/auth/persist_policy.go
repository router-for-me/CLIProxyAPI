package auth

import "context"

type skipPersistContextKey struct{}
type deferAPIKeyModelAliasRebuildContextKey struct{}

// WithSkipPersist returns a derived context that disables persistence for Manager Update/Register calls.
// It is intended for code paths that are reacting to file watcher events, where the file on disk is
// already the source of truth and persisting again would create a write-back loop.
func WithSkipPersist(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, skipPersistContextKey{}, true)
}

func shouldSkipPersist(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(skipPersistContextKey{})
	enabled, ok := v.(bool)
	return ok && enabled
}

// WithDeferredAPIKeyModelAliasRebuild returns a derived context that defers API-key model alias table rebuilds.
// Callers that use this for a batch of Register/Update/Remove operations must call RefreshAPIKeyModelAlias once.
func WithDeferredAPIKeyModelAliasRebuild(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, deferAPIKeyModelAliasRebuildContextKey{}, true)
}

func shouldDeferAPIKeyModelAliasRebuild(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v := ctx.Value(deferAPIKeyModelAliasRebuildContextKey{})
	enabled, ok := v.(bool)
	return ok && enabled
}
