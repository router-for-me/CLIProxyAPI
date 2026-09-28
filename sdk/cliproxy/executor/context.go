package executor

import (
	"context"
	"sync/atomic"
)

type downstreamWebsocketContextKey struct{}
type requireUpstreamWebsocketContextKey struct{}
type upstreamAttemptTrackerContextKey struct{}

type upstreamAttemptTracker struct {
	attempted atomic.Bool
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

type wireContractContextKey struct{}

// WireContract is the read-only tool contract visible to the outbound guard.
// It carries only tool identifiers and sizes: never the attempt, original
// client request, credentials, arguments, or tool outputs.
type WireContract struct {
	// SearchBridgeActive marks an attempt that adapted client tool_search,
	// including history-only turns without an active search declaration.
	SearchBridgeActive bool
	// SearchAliases lists bridged search aliases that must survive to the wire.
	SearchAliases []string
	// CustomAliases lists bridged custom aliases that must survive to the wire.
	CustomAliases []string
	// HistoryAliases lists bridged history names that must retain their call
	// identity without requiring a current tool declaration.
	HistoryAliases []string
	// RequiredFunctionNames lists function declarations that must survive all
	// provider translation and post-rule normalization.
	RequiredFunctionNames []string
	// HistoryCalls lists bridged historical calls whose identity and outputs
	// must remain linked in the final outbound payload.
	HistoryCalls []WireToolHistoryReference
	// ActiveToolBytes is the normalized declaration size after adaptation.
	ActiveToolBytes int
	// MaxActiveToolBytes is the configured ceiling enforced after all
	// provider translation and post-rule normalization.
	MaxActiveToolBytes int
}

// WireToolHistoryReference is the minimal identity needed to validate one
// bridged historical tool call after provider translation.
type WireToolHistoryReference struct {
	Name           string
	AlternateNames []string
	CallID         string
}

// WithWireContract attaches the read-only wire contract for the final send
// guard. A nil contract leaves the context unchanged.
func WithWireContract(ctx context.Context, wire *WireContract) context.Context {
	if ctx == nil || wire == nil {
		return ctx
	}
	return context.WithValue(ctx, wireContractContextKey{}, wire)
}

// WireContractFromContext reads the guard contract back. Nil means the
// attempt is inactive and guards are no-ops.
func WireContractFromContext(ctx context.Context) *WireContract {
	if ctx == nil {
		return nil
	}
	wire, _ := ctx.Value(wireContractContextKey{}).(*WireContract)
	return wire
}
