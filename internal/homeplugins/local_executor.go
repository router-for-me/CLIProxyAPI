package homeplugins

import "context"

const ACPModelPrefix = "acp/"

type LocalExecutorGrant struct {
	Plugin       string
	Version      string
	ConfigDigest string
	Model        string
	Valid        func() bool
}

type localExecutorGrantKey struct{}

func WithLocalExecutorGrant(ctx context.Context, grant LocalExecutorGrant) context.Context {
	return context.WithValue(ctx, localExecutorGrantKey{}, grant)
}

func ExecutorGrant(ctx context.Context) (LocalExecutorGrant, bool) {
	if ctx == nil {
		return LocalExecutorGrant{}, false
	}
	grant, ok := ctx.Value(localExecutorGrantKey{}).(LocalExecutorGrant)
	return grant, ok
}
