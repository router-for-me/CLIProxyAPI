package home

import (
	"context"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
)

// ConcurrencyReleaseFrame is the cumulative release accepted by Home for one credential and model.
type ConcurrencyReleaseFrame struct {
	CredentialID string `json:"credential_id"`
	Model        string `json:"model"`
	ReleaseSeq   int64  `json:"release_seq"`
}

type releaseState struct {
	Latest  int64
	Acked   int64
	waiters map[int64][]chan struct{}
}

type releaseFlusher struct {
	mu             sync.Mutex
	groups         map[executionregistry.ReleaseGroup]releaseState
	pending        map[executionregistry.ReleaseGroup]int64
	flushInterval  time.Duration
	maxBackoff     time.Duration
	configProvider func() internalconfig.CredentialConcurrencyConfig
	send           func(context.Context, ConcurrencyReleaseFrame) error
	wake           chan struct{}
	force          chan context.Context
}

func newReleaseFlusher(flushInterval, maxBackoff time.Duration, send func(context.Context, ConcurrencyReleaseFrame) error) *releaseFlusher {
	return &releaseFlusher{
		groups:        make(map[executionregistry.ReleaseGroup]releaseState),
		pending:       make(map[executionregistry.ReleaseGroup]int64),
		flushInterval: flushInterval,
		maxBackoff:    maxBackoff,
		send:          send,
		wake:          make(chan struct{}, 1),
		force:         make(chan context.Context, 1),
	}
}

// NewReleaseFlusher creates a flusher that reads timing updates from the current limiter configuration.
func NewReleaseFlusher(configProvider func() internalconfig.CredentialConcurrencyConfig, send func(context.Context, ConcurrencyReleaseFrame) error) *releaseFlusher {
	flusher := newReleaseFlusher(0, 0, send)
	flusher.SetConfigProvider(configProvider)
	return flusher
}

func (f *releaseFlusher) SetConfigProvider(provider func() internalconfig.CredentialConcurrencyConfig) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.configProvider = provider
	f.mu.Unlock()
	f.signal()
}

// SetSender replaces the Home lifetime used for subsequent release attempts.
func (f *releaseFlusher) SetSender(send func(context.Context, ConcurrencyReleaseFrame) error) {
	if f == nil {
		return
	}
	f.mu.Lock()
	f.send = send
	f.mu.Unlock()
	f.signal()
}

// MarkDirty records the latest cumulative sequence for one release group and
// returns a ticket completed when Home acknowledges that sequence.
func (f *releaseFlusher) MarkDirty(group executionregistry.ReleaseGroup, sequence int64) *executionregistry.ReleaseTicket {
	if f == nil || sequence <= 0 || group.CredentialID == "" || group.Model == "" {
		return nil
	}

	done := make(chan struct{})
	f.mu.Lock()
	state := f.groups[group]
	if sequence <= state.Acked {
		close(done)
	} else {
		if state.waiters == nil {
			state.waiters = make(map[int64][]chan struct{})
		}
		state.waiters[sequence] = append(state.waiters[sequence], done)
		if sequence > state.Latest {
			state.Latest = sequence
		}
		f.groups[group] = state
		if current, ok := f.pending[group]; !ok || sequence > current {
			f.pending[group] = sequence
		}
	}
	f.mu.Unlock()
	f.signal()
	return executionregistry.NewReleaseTicket(group, sequence, done)
}

// Run sends dirty groups until its lifetime is cancelled.
func (f *releaseFlusher) Run(ctx context.Context) {
	if f == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	timerC := timer.C
	delay := f.timings().flushInterval
	backingOff := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.wake:
			if !backingOff {
				resetReleaseTimer(timer, 0)
				timerC = timer.C
			}
		case forceCtx := <-f.force:
			resetReleaseTimer(timer, 0)
			timerC = timer.C
			failed := f.flush(forceCtx)
			delay, backingOff = f.nextDelay(delay, failed)
			if f.idle() && !backingOff {
				timerC = nil
			} else {
				resetReleaseTimer(timer, delay)
				timerC = timer.C
			}
		case <-timerC:
			failed := f.flush(ctx)
			delay, backingOff = f.nextDelay(delay, failed)
			if f.idle() && !backingOff {
				timerC = nil
			} else {
				timer.Reset(delay)
				timerC = timer.C
			}
		}
	}
}

func (f *releaseFlusher) nextDelay(delay time.Duration, failed bool) (time.Duration, bool) {
	timings := f.timings()
	if !failed {
		return timings.flushInterval, false
	}
	delay *= 2
	if delay < timings.flushInterval {
		delay = timings.flushInterval
	}
	if delay > timings.maxBackoff {
		delay = timings.maxBackoff
	}
	return delay, true
}

func resetReleaseTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

type releaseFlusherTimings struct {
	flushInterval time.Duration
	maxBackoff    time.Duration
}

func (f *releaseFlusher) timings() releaseFlusherTimings {
	defaults := internalconfig.CredentialConcurrencyConfig{}.WithDefaults()
	timings := releaseFlusherTimings{flushInterval: f.flushInterval, maxBackoff: f.maxBackoff}

	f.mu.Lock()
	provider := f.configProvider
	f.mu.Unlock()
	if provider != nil {
		cfg := provider().WithDefaults()
		timings.flushInterval = cfg.ReleaseFlushInterval
		timings.maxBackoff = cfg.ReleaseMaxBackoff
	}
	if timings.flushInterval <= 0 {
		timings.flushInterval = defaults.ReleaseFlushInterval
	}
	if timings.maxBackoff < timings.flushInterval {
		timings.maxBackoff = timings.flushInterval
	}
	return timings
}

func (f *releaseFlusher) flush(ctx context.Context) bool {
	if f == nil {
		return false
	}

	f.mu.Lock()
	if len(f.pending) == 0 {
		f.mu.Unlock()
		return false
	}
	send := f.send
	pending := make(map[executionregistry.ReleaseGroup]int64, len(f.pending))
	for group, sequence := range f.pending {
		pending[group] = sequence
	}
	f.mu.Unlock()
	if send == nil {
		return false
	}

	failed := false
	for group, sequence := range pending {
		errSend := send(ctx, ConcurrencyReleaseFrame{
			CredentialID: group.CredentialID,
			Model:        group.Model,
			ReleaseSeq:   sequence,
		})
		f.mu.Lock()
		if errSend != nil {
			failed = true
			f.mu.Unlock()
			continue
		}
		state := f.groups[group]
		if sequence > state.Acked {
			state.Acked = sequence
			for waiterSequence, waiters := range state.waiters {
				if waiterSequence <= state.Acked {
					for _, done := range waiters {
						close(done)
					}
					delete(state.waiters, waiterSequence)
				}
			}
		}
		f.groups[group] = state
		if current, ok := f.pending[group]; ok && current <= state.Acked {
			delete(f.pending, group)
		}
		f.mu.Unlock()
	}
	return failed
}

// Flush waits for all currently dirty groups to be acknowledged within ctx.
func (f *releaseFlusher) Flush(ctx context.Context) error {
	if f == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	f.forceFlush(ctx)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if f.idle() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (f *releaseFlusher) idle() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pending) == 0
}

func (f *releaseFlusher) signal() {
	if f == nil {
		return
	}
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *releaseFlusher) forceFlush(ctx context.Context) {
	if f == nil {
		return
	}
	select {
	case f.force <- ctx:
	default:
	}
}
