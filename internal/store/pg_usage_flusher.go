package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// FlusherConfig tunes the UsageFlusher background loop.
type FlusherConfig struct {
	// QueueCap bounds the in-memory buffer for pending usage records. Once the
	// cap is reached, additional records are dropped (oldest-first) and counted
	// via Drops() so callers can alert on backpressure.
	QueueCap int
	// FlushInterval is the duration between automatic flushes. Smaller values
	// reduce the worst-case persistence delay at the cost of more DB round-trips.
	FlushInterval time.Duration
	// FlushBatchSize bounds the number of records written per flush.
	FlushBatchSize int
}

// DefaultFlusherConfig provides sane defaults when config is not specified.
func DefaultFlusherConfig() FlusherConfig {
	return FlusherConfig{
		QueueCap:       10000,
		FlushInterval:  10 * time.Second,
		FlushBatchSize: 100,
	}
}

// UsageFlusher is an asynchronous bridge from the in-memory usage pub/sub bus
// to a Postgres UsageStore. It implements coreusage.Plugin and is intended to
// be registered alongside (not as a replacement for) the hot-path redisqueue
// plugin: the redisqueue buffer serves sub-second dashboard reads, while this
// flusher powers long-term aggregate queries and budget enforcement.
type UsageFlusher struct {
	store *UsageStore
	cfg   FlusherConfig

	queue     chan coreusage.Record
	stop      chan struct{}
	stopped   chan struct{}
	startOnce sync.Once
	stopOnce  sync.Once

	// apiKeyStore resolves the api_key_id (and pricing) for a record from
	// its principal. Optional: when nil, api_key_id is left empty and only
	// api_key_principal is persisted.
	apiKeyStore *APIKeyStore

	dropCounter  atomic.Int64
	flushCounter atomic.Int64
}

// NewUsageFlusher constructs a flusher bound to the given usage store. The
// flusher does not start its background loop until Start(ctx) is invoked.
func NewUsageFlusher(store *UsageStore, apiKeyStore *APIKeyStore, cfg FlusherConfig) *UsageFlusher {
	if cfg.QueueCap <= 0 {
		cfg = DefaultFlusherConfig()
	}
	if cfg.FlushInterval <= 0 {
		cfg = DefaultFlusherConfig()
	}
	if cfg.FlushBatchSize <= 0 {
		cfg = DefaultFlusherConfig()
	}
	return &UsageFlusher{
		store:       store,
		cfg:         cfg,
		apiKeyStore: apiKeyStore,
		queue:       make(chan coreusage.Record, cfg.QueueCap),
		stop:        make(chan struct{}),
		stopped:     make(chan struct{}),
	}
}

// HandleUsage satisfies coreusage.Plugin. It is non-blocking: records are
// pushed onto a buffered queue and processed asynchronously by the flush
// goroutine. When the queue is full, the record is dropped (and counted)
// rather than blocking the request hot path.
func (f *UsageFlusher) HandleUsage(_ context.Context, record coreusage.Record) {
	if f == nil || f.store == nil {
		return
	}
	select {
	case f.queue <- record:
	default:
		f.dropCounter.Add(1)
		log.WithField("drops", f.dropCounter.Load()).Warn("postgres usage flusher: queue full; dropping usage record")
	}
}

// Drops returns the cumulative count of records dropped due to a full queue.
func (f *UsageFlusher) Drops() int64 {
	if f == nil {
		return 0
	}
	return f.dropCounter.Load()
}

// FlushCount returns the cumulative count of records successfully persisted.
func (f *UsageFlusher) FlushCount() int64 {
	if f == nil {
		return 0
	}
	return f.flushCounter.Load()
}

// Start launches the background flush loop exactly once. Returns an error if
// the flusher was already started or has been stopped. The supplied context
// is used only for the flush loop's DB writes; on Stop(), the loop drains
// remaining queued records before exiting.
func (f *UsageFlusher) Start(ctx context.Context) error {
	if f == nil {
		return errors.New("postgres usage flusher: nil receiver")
	}
	startErr := errors.New("postgres usage flusher: already started")
	started := false
	f.startOnce.Do(func() {
		started = true
		startErr = nil
		go f.run(ctx)
	})
	if !started {
		return startErr
	}
	return nil
}

// Stop signals the flush loop to terminate and blocks until remaining queued
// records have been drained (best-effort). The handle is no longer usable
// after Stop().
func (f *UsageFlusher) Stop() {
	if f == nil {
		return
	}
	f.stopOnce.Do(func() {
		close(f.stop)
		<-f.stopped
	})
}

func (f *UsageFlusher) run(ctx context.Context) {
	defer close(f.stopped)
	ticker := time.NewTicker(f.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-f.stop:
			f.drain(ctx)
			return
		case <-ticker.C:
			f.flushBatch(ctx)
		case rec := <-f.queue:
			// Coalesce: process the just-arrived record alongside any others
			// that the queue holds, up to the batch size, without waiting
			// for the next tick. This avoids latency spikes when the input
			// rate is high while still bounding DB round-trips.
			flushed := f.flushOne(ctx, rec)
			f.flushMore(ctx, f.cfg.FlushBatchSize-1, flushed)
		}
	}
}

func (f *UsageFlusher) flushBatch(ctx context.Context) {
	// Drain up to FlushBatchSize records; if the queue is empty, this is a
	// noop (which keeps the ticker cheap during quiet periods).
	flushed := 0
	for i := 0; i < f.cfg.FlushBatchSize; i++ {
		select {
		case rec := <-f.queue:
			flushed += f.flushOne(ctx, rec)
		default:
			i = f.cfg.FlushBatchSize // break out
		}
	}
}

func (f *UsageFlusher) flushMore(ctx context.Context, remaining, alreadyFlushed int) {
	if alreadyFlushed == 0 {
		return
	}
	for i := 0; i < remaining; i++ {
		select {
		case rec := <-f.queue:
			f.flushOne(ctx, rec)
		default:
			return
		}
	}
}

func (f *UsageFlusher) drain(ctx context.Context) {
	for {
		select {
		case rec := <-f.queue:
			f.flushOne(ctx, rec)
		default:
			return
		}
	}
}

// flushOne converts the in-memory record to its persisted form and writes it
// via the UsageStore. Failed attempts are routed to usage_errors so the
// success-table aggregates stay clean of failures; successful responses are
// written to usage_events as before. Errors are logged but not surfaced; the
// flusher is best-effort by design (the upstream usage pub/sub does not retry).
func (f *UsageFlusher) flushOne(ctx context.Context, record coreusage.Record) int {
	if record.Failed {
		err, ok := f.flushError(ctx, record)
		if !ok {
			return 0
		}
		if err != nil {
			log.WithError(err).Debug("postgres usage flusher: single error insert failed; will retry next tick")
			return 0
		}
		f.flushCounter.Add(1)
		return 1
	}
	event, apiKeyID, userID, ok := f.toEvent(ctx, record)
	if !ok {
		return 0
	}
	event.APIKeyID = apiKeyID
	event.UserID = userID
	if err := f.store.InsertEvent(ctx, event); err != nil {
		// Fallback for very high-throughput bursts: batch the next flush.
		log.WithError(err).Debug("postgres usage flusher: single insert failed; will retry next tick")
		return 0
	}
	f.flushCounter.Add(1)
	return 1
}

// flushError converts a failed-attempt record into a UsageError and persists
// it via InsertError. Returns (err, ok): ok=false means the record was
// malformed enough to skip; err non-nil means the insert failed and the
// caller may retry on the next tick.
func (f *UsageFlusher) flushError(ctx context.Context, record coreusage.Record) (error, bool) {
	event, apiKeyID, userID, ok := f.toError(ctx, record)
	if !ok {
		return nil, false
	}
	event.APIKeyID = apiKeyID
	event.UserID = userID
	if err := f.store.InsertError(ctx, event); err != nil {
		return err, true
	}
	return nil, true
}

// toEvent converts a coreusage.Record into the persisted UsageEvent shape and
// resolves the (api_key_id, user_id, cost_usd) tuple that the hot path does not
// know. Returns ok=false when the record is malformed enough to skip. Only
// called for successful responses — failed attempts are routed to toError.
func (f *UsageFlusher) toEvent(ctx context.Context, record coreusage.Record) (UsageEvent, string, string, bool) {
	model := record.Model
	if model == "" {
		model = record.Alias
	}
	if model == "" {
		model = "unknown"
	}
	now := record.RequestedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	total := record.Detail.TotalTokens
	if total == 0 {
		total = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens
	}
	principal := record.APIKey
	apiKeyID := ""
	userID := ""
	if f.apiKeyStore != nil && principal != "" {
		// Resolve api_key_id (and the owning internal user id) via the hashed
		// secret. Lookup failures (legacy file-only keys) leave the ids empty
		// but still persist the principal.
		hash := HashSecret(principal)
		if key, _, err := f.apiKeyStore.LookupByHash(ctx, hash); err == nil {
			apiKeyID = key.ID
			userID = key.UserID
		}
	}
	// Resolve cost via pricing row; missing pricing → 0 (no revenue lost).
	var cost float64
	if us := f.store; us != nil {
		if pricing, err := us.GetPricing(ctx, model); err == nil {
			// Use CacheReadTokens when the provider populates it (Anthropic
			// distinguishes cache-creation vs cache-read tokens). Fall back
			// to CachedTokens for providers like OpenAI that only emit one
			// cache counter, treated as cache-read by convention.
			cacheRead := record.Detail.CacheReadTokens
			if cacheRead == 0 {
				cacheRead = record.Detail.CachedTokens
			}
			cost = ComputeCost(pricing, record.Detail.InputTokens, record.Detail.OutputTokens,
				record.Detail.ReasoningTokens, record.Detail.CacheCreationTokens, cacheRead)
		}
	}
	return UsageEvent{
		RequestID:           record.RequestID,
		APIKeyPrincipal:     principal,
		Provider:            record.Provider,
		ExecutorType:        record.ExecutorType,
		Model:               model,
		Alias:               record.Alias,
		Endpoint:            "",
		AuthType:            record.AuthType,
		Source:              record.Source,
		ReasoningEffort:     record.ReasoningEffort,
		ServiceTier:         record.ServiceTier,
		ResponseServiceTier: record.ResponseServiceTier,
		InputTokens:         record.Detail.InputTokens,
		OutputTokens:        record.Detail.OutputTokens,
		ReasoningTokens:     record.Detail.ReasoningTokens,
		CachedTokens:        record.Detail.CachedTokens,
		CacheCreationTokens: record.Detail.CacheCreationTokens,
		TotalTokens:         total,
		CostUSD:             cost,
		LatencyMs:           record.Latency.Milliseconds(),
		TTFTMs:              record.TTFT.Milliseconds(),
		Failed:              false, // success path; failed attempts go to usage_errors
		FailStatusCode:      0,
		Generate:            generateEnabled(record.Generate),
		RequestedAt:         now,
	}, apiKeyID, userID, true
}

// toError converts a failed-attempt coreusage.Record into the persisted
// UsageError shape. Mirrors toEvent but carries record.Fail.Body into
// ErrorMessage and record.Fail.StatusCode into FailStatusCode rather than
// dropping them (which toEvent does for the success path).
func (f *UsageFlusher) toError(ctx context.Context, record coreusage.Record) (UsageError, string, string, bool) {
	model := record.Model
	if model == "" {
		model = record.Alias
	}
	if model == "" {
		model = "unknown"
	}
	now := record.RequestedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	total := record.Detail.TotalTokens
	if total == 0 {
		total = record.Detail.InputTokens + record.Detail.OutputTokens + record.Detail.ReasoningTokens
	}
	principal := record.APIKey
	apiKeyID := ""
	userID := ""
	if f.apiKeyStore != nil && principal != "" {
		hash := HashSecret(principal)
		if key, _, err := f.apiKeyStore.LookupByHash(ctx, hash); err == nil {
			apiKeyID = key.ID
			userID = key.UserID
		}
	}
	// Resolve cost via pricing row; missing pricing → 0. Failed attempts may
	// have partial token counts (e.g. only input tokens billed before the
	// upstream errored mid-stream), but we still attribute whatever the
	// executor observed so the dashboard can surface the partial cost.
	var cost float64
	if us := f.store; us != nil {
		if pricing, err := us.GetPricing(ctx, model); err == nil {
			cacheRead := record.Detail.CacheReadTokens
			if cacheRead == 0 {
				cacheRead = record.Detail.CachedTokens
			}
			cost = ComputeCost(pricing, record.Detail.InputTokens, record.Detail.OutputTokens,
				record.Detail.ReasoningTokens, record.Detail.CacheCreationTokens, cacheRead)
		}
	}
	failStatus := record.Fail.StatusCode
	if !record.Failed {
		failStatus = 0
	}
	return UsageError{
		RequestID:           record.RequestID,
		APIKeyPrincipal:     principal,
		Provider:            record.Provider,
		ExecutorType:        record.ExecutorType,
		Model:               model,
		Alias:               record.Alias,
		Endpoint:            "",
		AuthType:            record.AuthType,
		Source:              record.Source,
		ReasoningEffort:     record.ReasoningEffort,
		ServiceTier:         record.ServiceTier,
		ResponseServiceTier: record.ResponseServiceTier,
		InputTokens:         record.Detail.InputTokens,
		OutputTokens:        record.Detail.OutputTokens,
		ReasoningTokens:     record.Detail.ReasoningTokens,
		CachedTokens:        record.Detail.CachedTokens,
		CacheCreationTokens: record.Detail.CacheCreationTokens,
		TotalTokens:         total,
		CostUSD:             cost,
		LatencyMs:           record.Latency.Milliseconds(),
		TTFTMs:              record.TTFT.Milliseconds(),
		FailStatusCode:      failStatus,
		ErrorMessage:        record.Fail.Body,
		Generate:            generateEnabled(record.Generate),
		RequestedAt:         now,
	}, apiKeyID, userID, true
}

func generateEnabled(flag *bool) bool {
	if flag == nil {
		return true
	}
	return *flag
}

// Compile-time assertion that UsageFlusher satisfies the usage.Plugin contract.
var _ coreusage.Plugin = (*UsageFlusher)(nil)
