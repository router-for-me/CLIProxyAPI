package usage

import (
	"context"
	"sync"
)

type deferredFailuresContextKey struct{}

type deferredFailureScope struct {
	mu        sync.Mutex
	active    bool
	last      Record
	hasRecord bool
}

// WithDeferredFailures buffers failed usage records until finish is called.
func WithDeferredFailures(ctx context.Context) (context.Context, func(bool)) {
	if ctx == nil {
		ctx = context.Background()
	}
	scope := &deferredFailureScope{active: true}
	ctx = context.WithValue(ctx, deferredFailuresContextKey{}, scope)
	return ctx, func(flush bool) {
		var record Record
		hasRecord := false

		scope.mu.Lock()
		if !scope.active {
			scope.mu.Unlock()
			return
		}
		if flush && scope.hasRecord {
			record = scope.last
			hasRecord = true
		}
		scope.active = false
		scope.hasRecord = false
		scope.mu.Unlock()

		if hasRecord {
			PublishRecord(ctx, record)
		}
	}
}

// PublishRecordOrDefer publishes immediately unless the request is buffering failures.
func PublishRecordOrDefer(ctx context.Context, record Record) {
	if record.Failed && ctx != nil {
		scope, _ := ctx.Value(deferredFailuresContextKey{}).(*deferredFailureScope)
		if scope != nil {
			scope.mu.Lock()
			if scope.active {
				scope.last = record
				scope.hasRecord = true
				scope.mu.Unlock()
				return
			}
			scope.mu.Unlock()
		}
	}
	PublishRecord(ctx, record)
}
