package config

import "context"

type mirrorFenceKey struct{}

// WithMirrorFence returns a copy of ctx carrying fence. A store's PersistConfig
// reads config.yaml when the mirror runs, under its own lock, and calls
// MirrorFenceHolds with those bytes; it mirrors them only if the fence holds. The
// watcher fences each mirror on the bytes its reload validated, so a save landing
// after that reload (an invalid or partial document, or a hashed write-back that
// leaves a plaintext secret at a YAML anchor) is left to the reload of that save.
func WithMirrorFence(ctx context.Context, fence func(data []byte) bool) context.Context {
	if fence == nil {
		return ctx
	}
	return context.WithValue(ctx, mirrorFenceKey{}, fence)
}

// MirrorFenceHolds reports whether the fence ctx carries, if any, allows mirroring
// data.
func MirrorFenceHolds(ctx context.Context, data []byte) bool {
	if ctx == nil {
		return true
	}
	fence, ok := ctx.Value(mirrorFenceKey{}).(func([]byte) bool)
	return !ok || fence(data)
}
