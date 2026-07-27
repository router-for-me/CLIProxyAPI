package home

import (
	"context"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
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
	delay := f.timings().flushInterval
	backingOff := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.wake:
			if !backingOff {
				resetReleaseTimer(timer, 0)
			}
		case forceCtx := <-f.force:
			resetReleaseTimer(timer, 0)
			failed := f.flush(forceCtx)
			delay, backingOff = f.nextDelay(delay, failed)
			resetReleaseTimer(timer, delay)
		case <-timer.C:
			failed := f.flush(ctx)
			delay, backingOff = f.nextDelay(delay, failed)
			timer.Reset(delay)
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
	if f == nil || f.send == nil {
		return false
	}

	f.mu.Lock()
	pending := make(map[executionregistry.ReleaseGroup]int64, len(f.groups))
	for group, state := range f.groups {
		if state.Latest > state.Acked {
			pending[group] = state.Latest
		}
	}
	f.mu.Unlock()

	failed := false
	for group, sequence := range pending {
		errSend := f.send(ctx, ConcurrencyReleaseFrame{
			CredentialID: group.CredentialID,
			Model:        group.Model,
			ReleaseSeq:   sequence,
		})
		if errSend != nil {
			failed = true
			continue
		}
		f.mu.Lock()
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
	for _, state := range f.groups {
		if state.Latest > state.Acked {
			return false
		}
	}
	return true
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
