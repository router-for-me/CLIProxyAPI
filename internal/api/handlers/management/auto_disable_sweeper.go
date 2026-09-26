package management

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// autoDisableSweepInterval is how often the background auto-re-enable sweeper
// re-scans for expired auto-disabled entries (60s, matching the alert and
// model-health sweep cadences).
const autoDisableSweepInterval = 60 * time.Second

// autoDisableReenabler is the minimal persistence surface the auto-re-enable
// sweeper needs — a subset of store.UpstreamProviderStore. Narrowing the
// sweeper to this interface keeps its unit tests hermetic (they inject a stub
// instead of a full store fake) and documents that the sweeper touches only
// the expiry-re-enable column family. The PG-backed store satisfies it via the
// exported ReenableExpiredAutoDisabled primitive.
type autoDisableReenabler interface {
	ReenableExpiredAutoDisabled(ctx context.Context) (int64, error)
}

// AutoDisableSweeper periodically re-enables auto-disabled upstream API-key
// entries whose provider-configured cooldown has elapsed. It is the server-side
// companion to the auto-disable sink (auto_disable_sink.go): the sink flips an
// entry to disabled+auto_disabled on a matched upstream error, and this sweeper
// clears the runtime flags once auto_disabled_at passes NOW() - cooldown. Manual
// disables are never touched (the store's WHERE owns that invariant).
//
// A nil pg makes every tick a silent no-op so the server can construct and start
// the sweeper unconditionally — a PG-absent deployment gets a harmless parked
// goroutine, matching the sibling alert/health sweeps (StartAlertSweep etc.).
// render must be non-nil: it is the re-render step invoked only after rows were
// actually re-enabled so the entry drops back into routing. In production it is
// Handler.applyUpstreamProviders, which must NOT be called while h.mu is held —
// the sweeper goroutine never acquires h.mu; applyUpstreamProviders manages its
// own locking (its doc comment: "Caller must NOT hold h.mu").
type AutoDisableSweeper struct {
	pg     autoDisableReenabler
	render func()

	// interval is the tick cadence (autoDisableSweepInterval in production;
	// short in tests via the injectable constructor).
	interval time.Duration

	// started tracks whether the background goroutine was launched. A sweeper
	// is single-threaded by design — the server relaying/reloading constructs a
	// fresh instance each time, never restarting an existing one — so no lock
	// is needed around the flag beyond the atomic (Start/Stop are not called
	// concurrently in practice; the atomic only makes Stop-before-Start safe).
	started atomic.Bool
	// stopOnce makes Stop idempotent (a second Stop after the first must not
	// double-close done).
	stopOnce sync.Once
	done     chan struct{}
	stopped  chan struct{}
}

// NewAutoDisableSweeper builds a production sweeper at the default 60s
// interval. pg may be nil (ticks then no-op); render is required and must be
// non-nil.
func NewAutoDisableSweeper(pg store.UpstreamProviderStore, render func()) *AutoDisableSweeper {
	return newAutoDisableSweeper(pg, render, autoDisableSweepInterval)
}

// newAutoDisableSweeper is the interval-injectable constructor used by tests
// (a short interval so a lifecycle test does not wait 60s). It accepts the
// minimal reenabler surface; the production constructor passes the full
// store.UpstreamProviderStore, which satisfies it. interval <= 0 falls back to
// autoDisableSweepInterval.
func newAutoDisableSweeper(pg autoDisableReenabler, render func(), interval time.Duration) *AutoDisableSweeper {
	if interval <= 0 {
		interval = autoDisableSweepInterval
	}
	return &AutoDisableSweeper{
		pg:       pg,
		render:   render,
		interval: interval,
		done:     make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// Start launches the background sweep loop. Safe to call at most once per
// instance; a second Start is a no-op.
func (s *AutoDisableSweeper) Start() {
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
func (s *AutoDisableSweeper) Stop() {
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
// nil pg makes each sweep a no-op; a panicking sweep is recovered inside
// sweep() so one bad tick cannot kill the loop.
func (s *AutoDisableSweeper) run() {
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

// sweep runs one re-enable pass and logs the outcome. It is the single-tick
// trigger seam unit tests call directly (no waiting on real intervals).
func (s *AutoDisableSweeper) sweep() {
	defer func() {
		if r := recover(); r != nil {
			log.WithField("panic", r).Error("auto-disable: re-enable sweep panicked")
		}
	}()
	if s == nil || s.pg == nil {
		return // PG absent: parked no-op tick
	}
	// A bounded context keeps one slow/broken store from stalling the loop; the
	// re-enable is a single bulk UPDATE so 5s is ample, and a hung DB surfaces
	// as a logged warning rather than a stuck tick.
	wctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	count, err := s.pg.ReenableExpiredAutoDisabled(wctx)
	if err != nil {
		log.WithError(err).Warn("auto-disable: re-enable sweep failed")
		return
	}
	if count > 0 {
		log.WithField("count", count).Debug("auto-disable: re-enabled expired entry rows")
		if s.render != nil {
			s.render()
		}
	}
}
