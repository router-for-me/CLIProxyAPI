package usage

import (
	"context"
	"sync"
	"sync/atomic"
)

type streamDeliveryKey struct{}

type streamDelivery struct {
	supported atomic.Int32
	once      sync.Once
	done      chan struct{}
	err       error
}

// WithStreamDelivery lets the HTTP consumer acknowledge delivery independently
// of request cancellation. The consumer must resolve the outcome on every exit.
func WithStreamDelivery(ctx context.Context) (context.Context, func(error)) {
	delivery := &streamDelivery{done: make(chan struct{})}
	return context.WithValue(ctx, streamDeliveryKey{}, delivery), func(err error) {
		delivery.once.Do(func() {
			delivery.err = err
			close(delivery.done)
		})
	}
}

// WithoutStreamDelivery isolates an internal execution from its caller's HTTP
// acknowledgment. Internal consumers retain EOF-based accounting completion;
// cancellation and all unrelated context values remain inherited.
func WithoutStreamDelivery(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	return context.WithValue(ctx, streamDeliveryKey{}, struct{}{})
}

// StreamDeliveryTracked reports whether a consumer will acknowledge delivery.
func StreamDeliveryTracked(ctx context.Context) bool {
	_, ok := ctx.Value(streamDeliveryKey{}).(*streamDelivery)
	return ok
}

// WaitStreamDelivery waits for the consumer, not the upstream reader, to finish.
// Producers must close their output channel before waiting to avoid deadlocks.
func WaitStreamDelivery(ctx context.Context) (error, bool) {
	delivery, ok := ctx.Value(streamDeliveryKey{}).(*streamDelivery)
	if !ok {
		return nil, false
	}
	<-delivery.done
	return delivery.err, true
}

// SupportStreamDelivery opts an active producer into early terminal cancellation.
// The producer must reconcile cancellation with WaitStreamDelivery.
func SupportStreamDelivery(ctx context.Context) func() {
	delivery, ok := ctx.Value(streamDeliveryKey{}).(*streamDelivery)
	if !ok {
		return func() {}
	}
	delivery.supported.Add(1)
	var once sync.Once
	return func() { once.Do(func() { delivery.supported.Add(-1) }) }
}

// StreamDeliverySupported reports whether an active producer understands delivery.
func StreamDeliverySupported(ctx context.Context) bool {
	delivery, ok := ctx.Value(streamDeliveryKey{}).(*streamDelivery)
	return ok && delivery.supported.Load() > 0
}
