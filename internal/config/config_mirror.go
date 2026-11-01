package config

import "context"

type mirrorFenceKey struct{}

// WithMirrorFence returns a copy of ctx carrying current, which reports whether a
// config mirror is still current. A store's PersistConfig calls MirrorFenceHolds
// after it reads config.yaml, under its own lock, and skips the mirror when the
// fence no longer holds. The watcher uses it so a mirror queued before a reload
// that hashed a plaintext secret does not publish the rewritten file, in which a
// YAML anchor or merge source can still hold that plaintext.
func WithMirrorFence(ctx context.Context, current func() bool) context.Context {
	if current == nil {
		return ctx
	}
	return context.WithValue(ctx, mirrorFenceKey{}, current)
}

// MirrorFenceHolds reports whether the fence ctx carries, if any, still holds.
func MirrorFenceHolds(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	current, ok := ctx.Value(mirrorFenceKey{}).(func() bool)
	return !ok || current()
}
