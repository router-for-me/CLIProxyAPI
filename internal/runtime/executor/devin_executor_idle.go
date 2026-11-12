package executor

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// defaultDevinUpstreamStreamIdle aborts a Devin connect stream when no frame
// bytes arrive for this long. It is a liveness gap, not a request deadline:
// any read progress restarts it. 90s sits well above a normal inter-frame gap
// and below the multi-minute hang left when GetChatMessage accepts a turn,
// emits nothing, and never closes the stream.
const defaultDevinUpstreamStreamIdle = 90 * time.Second

const devinStreamIdleMessage = "devin stream error: upstream stream stalled without frames"

// errDevinStreamIdle is the reader-level signal for an idle abort. Callers map
// it to a 502 status error so credential failover can rotate before any
// client payload is committed.
var errDevinStreamIdle = errors.New("devin upstream stream idle")

func devinIdleTimeoutOrDefault(idle time.Duration) time.Duration {
	if idle > 0 {
		return idle
	}
	return defaultDevinUpstreamStreamIdle
}

func (e *DevinExecutor) devinUpstreamIdleTimeout() time.Duration {
	if e != nil && e.streamIdleTimeout > 0 {
		return e.streamIdleTimeout
	}
	return defaultDevinUpstreamStreamIdle
}

func newDevinStreamIdleError() devinTransientStreamError {
	return newDevinTransientStreamError(devinStreamIdleMessage, errDevinStreamIdle)
}

// devinStreamIdleWatch closes the upstream body when a read makes no progress
// for the idle interval. A newer arm invalidates an in-flight callback so a
// frame that arrives as the timer expires is not discarded.
type devinStreamIdleWatch struct {
	ctx     context.Context
	closer  io.Closer
	idle    time.Duration
	mu      sync.Mutex
	timer   *time.Timer
	gen     atomic.Uint64
	stopped bool
	fired   atomic.Bool
}

func newDevinStreamIdleWatch(ctx context.Context, body io.Reader, idle time.Duration) *devinStreamIdleWatch {
	watch := &devinStreamIdleWatch{ctx: ctx, idle: idle}
	if closer, ok := body.(io.Closer); ok {
		watch.closer = closer
	}
	return watch
}

func (w *devinStreamIdleWatch) arm() {
	if w == nil || w.idle <= 0 {
		return
	}
	generation := w.gen.Add(1)
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopped || w.fired.Load() {
		return
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.idle, func() {
		if w.gen.Load() != generation {
			return
		}
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.stopped || w.fired.Load() || w.gen.Load() != generation {
			return
		}
		if w.ctx != nil && w.ctx.Err() != nil {
			return
		}
		w.fired.Store(true)
		if w.closer != nil {
			_ = w.closer.Close()
		}
	})
}

func (w *devinStreamIdleWatch) stop() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stopped = true
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
}

func (w *devinStreamIdleWatch) idleFired() bool {
	return w != nil && w.fired.Load()
}

// devinIdleReader turns an idle gap on the Devin connect body into
// errDevinStreamIdle. Client cancellation wins over the watchdog so a
// disconnected caller is not failed over.
type devinIdleReader struct {
	ctx   context.Context
	r     io.Reader
	watch *devinStreamIdleWatch
}

func watchDevinUpstream(ctx context.Context, body io.Reader, idle time.Duration) (io.Reader, func()) {
	if body == nil {
		return nil, func() {}
	}
	watch := newDevinStreamIdleWatch(ctx, body, devinIdleTimeoutOrDefault(idle))
	return &devinIdleReader{ctx: ctx, r: body, watch: watch}, watch.stop
}

func (r *devinIdleReader) Read(p []byte) (int, error) {
	if r == nil || r.r == nil {
		return 0, io.EOF
	}
	if r.ctx != nil && r.ctx.Err() != nil {
		r.watch.stop()
		return 0, r.ctx.Err()
	}
	if r.watch.idleFired() {
		return 0, errDevinStreamIdle
	}
	r.watch.arm()
	n, errRead := r.r.Read(p)
	if r.ctx != nil && r.ctx.Err() != nil {
		r.watch.stop()
		if errRead != nil {
			return n, errRead
		}
		return n, r.ctx.Err()
	}
	if errRead != nil && r.watch.idleFired() {
		if n > 0 {
			return n, nil
		}
		return 0, errDevinStreamIdle
	}
	if errRead != nil {
		r.watch.stop()
	}
	return n, errRead
}
