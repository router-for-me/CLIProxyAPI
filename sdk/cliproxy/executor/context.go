package executor

import (
	"context"
	"runtime"
	"sync/atomic"
)

type downstreamWebsocketContextKey struct{}

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

type deferredFailureContextKey struct{}
type deferredFailurePublisher func(context.Context)

const (
	deferredFailureOpen int32 = iota
	deferredFailureUpdating
	deferredFailureClosed
)

// DeferredFailure holds a request-scoped failed-attempt publisher until the
// caller knows whether the logical request ultimately succeeds or fails.
type DeferredFailure struct {
	state atomic.Int32
	last  atomic.Value
}

// WithDeferredFailure returns a context that can defer failed attempt
// publishers. It is used by retrying callers to avoid reporting intermediate
// attempt failures as final request failures.
func WithDeferredFailure(ctx context.Context) (context.Context, *DeferredFailure) {
	if ctx == nil {
		ctx = context.Background()
	}
	deferred := &DeferredFailure{}
	return context.WithValue(ctx, deferredFailureContextKey{}, deferred), deferred
}

// DeferFailure stores the latest failed-attempt publisher. It returns true when
// the failure was deferred and false when callers should publish immediately.
func DeferFailure(ctx context.Context, publish func(context.Context)) bool {
	if ctx == nil || publish == nil {
		return false
	}
	deferred, _ := ctx.Value(deferredFailureContextKey{}).(*DeferredFailure)
	if deferred == nil {
		return false
	}
	for {
		switch deferred.state.Load() {
		case deferredFailureClosed:
			return false
		case deferredFailureUpdating:
			runtime.Gosched()
		default:
			if deferred.state.CompareAndSwap(deferredFailureOpen, deferredFailureUpdating) {
				deferred.last.Store(deferredFailurePublisher(publish))
				deferred.state.Store(deferredFailureOpen)
				return true
			}
		}
	}
}

// Discard drops any deferred failure and closes the deferral scope.
func (d *DeferredFailure) Discard() {
	if d == nil {
		return
	}
	for {
		switch d.state.Load() {
		case deferredFailureClosed:
			return
		case deferredFailureUpdating:
			runtime.Gosched()
		default:
			if d.state.CompareAndSwap(deferredFailureOpen, deferredFailureClosed) {
				return
			}
		}
	}
}

// Flush publishes the latest deferred failure, if any, and closes the scope.
func (d *DeferredFailure) Flush(ctx context.Context) {
	if d == nil {
		return
	}
	for {
		switch d.state.Load() {
		case deferredFailureClosed:
			return
		case deferredFailureUpdating:
			runtime.Gosched()
		default:
			if d.state.CompareAndSwap(deferredFailureOpen, deferredFailureClosed) {
				if raw := d.last.Load(); raw != nil {
					if publish, ok := raw.(deferredFailurePublisher); ok && publish != nil {
						publish(ctx)
					}
				}
				return
			}
		}
	}
}
