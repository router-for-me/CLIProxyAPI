package management

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// requestBodySweepInterval is how often the background request-body retention
// sweeper prunes captured bodies (1h). Bodies are bounded per row by the
// capture pipeline and per table by this retention window, so an hourly cadence
// is ample and keeps DB load trivial.
const requestBodySweepInterval = 1 * time.Hour

// requestBodyRetention is the maximum age of a captured request/response body
// before the sweeper is allowed to delete it (30 days).
const requestBodyRetention = 30 * 24 * time.Hour

// requestBodyRetentionStore is the minimal persistence surface the retention
// sweeper needs — a subset of store.UsageStore. Narrowing the sweeper to this
// interface keeps its unit tests hermetic (they inject a stub instead of a full
// store fake) and documents that the sweeper touches only the request_bodies
// retention primitive.
type requestBodyRetentionStore interface {
	DeleteRequestBodiesBefore(ctx context.Context, cutoff time.Time) (int64, error)
}

// RequestBodyRetentionSweeper periodically deletes captured request/response
// bodies older than requestBodyRetention. It bounds the storage growth of the
// request_bodies capture table independently of the (unwired) usage_events
// retention path.
//
// A nil store makes every tick a silent no-op so the server can construct and
// start the sweeper unconditionally — a PG-absent deployment gets a harmless
// parked goroutine, matching the sibling auto-disable/alert/health sweeps.
type RequestBodyRetentionSweeper struct {
	pg requestBodyRetentionStore

	// interval is the tick cadence (requestBodySweepInterval in production;
	// short in tests via the injectable constructor).
	interval time.Duration

	// started tracks whether the background goroutine was launched. A sweeper
	// is single-threaded by design — the server constructs a fresh instance
	// each time, never restarting an existing one — so no lock is needed
	// around the flag beyond the atomic (Start/Stop are not called
	// concurrently in practice; the atomic only makes Stop-before-Start safe).
	started atomic.Bool
	// stopOnce makes Stop idempotent (a second Stop after the first must not
	// double-close done).
	stopOnce sync.Once
	done     chan struct{}
	stopped  chan struct{}
}

// NewRequestBodyRetentionSweeper builds a production sweeper at the default 1h
// interval. pg may be nil (ticks then no-op).
func NewRequestBodyRetentionSweeper(pg *store.UsageStore) *RequestBodyRetentionSweeper {
	if pg == nil {
		return newRequestBodyRetentionSweeper(nil, requestBodySweepInterval)
	}
	return newRequestBodyRetentionSweeper(pg, requestBodySweepInterval)
}

// newRequestBodyRetentionSweeper is the interval-injectable constructor used by
// tests (a short interval so a lifecycle test does not wait an hour). It accepts
// the minimal retention surface; the production constructor passes the full
// store.UsageStore, which satisfies it. interval <= 0 falls back to
// requestBodySweepInterval.
func newRequestBodyRetentionSweeper(pg requestBodyRetentionStore, interval time.Duration) *RequestBodyRetentionSweeper {
	if interval <= 0 {
		interval = requestBodySweepInterval
	}
	return &RequestBodyRetentionSweeper{
		pg:       pg,
		interval: interval,
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Start launches the background sweep loop. Safe to call at most once per
// instance; a second Start is a no-op.
func (s *RequestBodyRetentionSweeper) Start() {
	if s == nil {
		return
	}
	if s.started.Swap(true) {
		return
	}
	go s.run()
}

// Stop terminates the sweep loop and blocks until the goroutine has exited. It
// is idempotent, and safe to call before Start (no-op). An in-flight tick
// completes first; the loop observes the closed done channel and exits.
func (s *RequestBodyRetentionSweeper) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if !s.started.Load() {
			return // never started: nothing to stop
		}
		close(s.done)
		<-s.stopped
	})
}

// run is the background loop: one sweep per interval until Stop closes done. A
// nil store makes each sweep a no-op; a panicking sweep is recovered inside
// sweep() so one bad tick cannot kill the loop.
func (s *RequestBodyRetentionSweeper) run() {
	defer close(s.stopped)
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.sweep()
		}
	}
}

// sweep runs one retention pass and logs the outcome. It is the single-tick
// trigger seam unit tests call directly (no waiting on real intervals).
func (s *RequestBodyRetentionSweeper) sweep() {
	defer func() {
		if r := recover(); r != nil {
			log.WithField("panic", r).Error("request-body retention: sweep panicked")
		}
	}()
	if s == nil || s.pg == nil {
		return // store absent: parked no-op tick
	}
	// A bounded context keeps one slow/broken store from stalling the loop; the
	// prune is a single bulk DELETE so 30s is ample, and a hung DB surfaces as
	// a logged warning rather than a stuck tick.
	wctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cutoff := time.Now().Add(-requestBodyRetention)
	count, err := s.pg.DeleteRequestBodiesBefore(wctx, cutoff)
	if err != nil {
		log.WithError(err).Warn("request-body retention: sweep failed")
		return
	}
	if count > 0 {
		log.WithField("count", count).Debug("request-body retention: pruned expired body rows")
	}
}
