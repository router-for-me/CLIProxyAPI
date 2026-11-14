package executor

import (
	"context"
	"sync/atomic"
)

type downstreamWebsocketContextKey struct{}
type requireUpstreamWebsocketContextKey struct{}
type upstreamAttemptTrackerContextKey struct{}
type localWebsocketTurnTrackerContextKey struct{}

type upstreamAttemptTracker struct {
	attempted atomic.Bool
}

type localWebsocketTurnTracker struct {
	executed atomic.Bool
}

// WithDownstreamWebsocket marks the current request as coming from a downstream websocket connection.
func WithDownstreamWebsocket(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, downstreamWebsocketContextKey{}, true)
}

// DownstreamWebsocket reports whether the current request originates from a downstream websocket connection.
func DownstreamWebsocket(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(downstreamWebsocketContextKey{})
	enabled, ok := raw.(bool)
	return ok && enabled
}

// WithRequiredUpstreamWebsocket marks a request whose incremental context is valid only on the current upstream websocket.
func WithRequiredUpstreamWebsocket(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requireUpstreamWebsocketContextKey{}, true)
}

// RequiredUpstreamWebsocket reports whether falling back to an HTTP upstream would lose request context.
func RequiredUpstreamWebsocket(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(requireUpstreamWebsocketContextKey{})
	enabled, ok := raw.(bool)
	return ok && enabled
}

// WithUpstreamAttemptTracker installs a fresh tracker for one provider execution attempt.
func WithUpstreamAttemptTracker(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, upstreamAttemptTrackerContextKey{}, &upstreamAttemptTracker{})
}

// MarkUpstreamAttempt records that the provider execution reached an upstream transport boundary.
func MarkUpstreamAttempt(ctx context.Context) {
	if ctx == nil {
		return
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	if !ok || tracker == nil {
		return
	}
	tracker.attempted.Store(true)
}

// UpstreamAttempted reports whether the tracked provider execution reached an upstream transport boundary.
func UpstreamAttempted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	tracker, ok := ctx.Value(upstreamAttemptTrackerContextKey{}).(*upstreamAttemptTracker)
	return ok && tracker != nil && tracker.attempted.Load()
}

// WithLocalWebsocketTurnTracker installs a tracker that records when a downstream
// websocket turn was fulfilled locally over HTTP rather than establishing or
// owning an upstream websocket response.
func WithLocalWebsocketTurnTracker(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, localWebsocketTurnTrackerContextKey{}, &localWebsocketTurnTracker{})
}

// MarkLocalWebsocketTurn records that the current downstream websocket turn was
// answered locally without creating an upstream websocket response.
func MarkLocalWebsocketTurn(ctx context.Context) {
	if ctx == nil {
		return
	}
	tracker, ok := ctx.Value(localWebsocketTurnTrackerContextKey{}).(*localWebsocketTurnTracker)
	if !ok || tracker == nil {
		return
	}
	tracker.executed.Store(true)
}

// ResetLocalWebsocketTurn clears the local websocket turn marker before a new auth attempt.
func ResetLocalWebsocketTurn(ctx context.Context) {
	if ctx == nil {
		return
	}
	tracker, ok := ctx.Value(localWebsocketTurnTrackerContextKey{}).(*localWebsocketTurnTracker)
	if !ok || tracker == nil {
		return
	}
	tracker.executed.Store(false)
}

// LocalWebsocketTurnExecuted reports whether the current downstream websocket turn
// was fulfilled locally rather than on an upstream websocket.
func LocalWebsocketTurnExecuted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	tracker, ok := ctx.Value(localWebsocketTurnTrackerContextKey{}).(*localWebsocketTurnTracker)
	return ok && tracker != nil && tracker.executed.Load()
}
