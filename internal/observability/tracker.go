// Package observability normalizes provider usage into request events and an
// in-memory process-lifetime snapshot for the management API and TUI.
package observability

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	defaultRecentEventLimit = 200
	operationInference      = "inference"
	operationCompaction     = "compaction"
)

// CacheOutcome describes whether upstream cache telemetry reported a hit, a
// miss, or no result at all.
type CacheOutcome string

const (
	CacheOutcomeUnknown CacheOutcome = "unknown"
	CacheOutcomeHit     CacheOutcome = "hit"
	CacheOutcomeMiss    CacheOutcome = "miss"
)

// RequestEvent is the normalized, non-secret telemetry for one upstream call.
type RequestEvent struct {
	Sequence                  uint64       `json:"sequence"`
	Timestamp                 time.Time    `json:"timestamp"`
	Provider                  string       `json:"provider"`
	Model                     string       `json:"model"`
	Operation                 string       `json:"operation"`
	InputTokens               int64        `json:"input_tokens"`
	OutputTokens              int64        `json:"output_tokens"`
	CacheReadTokens           int64        `json:"cache_read_tokens"`
	CacheWriteTokens          int64        `json:"cache_write_tokens"`
	CacheTelemetryPresent     bool         `json:"cache_telemetry_present"`
	CacheOutcome              CacheOutcome `json:"cache_outcome"`
	CacheMiss                 bool         `json:"cache_miss"`
	Compaction                bool         `json:"compaction"`
	Failed                    bool         `json:"failed"`
	LatencyMilliseconds       int64        `json:"latency_ms"`
	EstimatedCostUSD          float64      `json:"estimated_cost_usd"`
	EstimatedCostAvailable    bool         `json:"estimated_cost_available"`
	EstimatedCostCatalogModel string       `json:"estimated_cost_catalog_model,omitempty"`
	EstimatedCostTier         string       `json:"estimated_cost_tier,omitempty"`
}

// Snapshot contains process-lifetime totals plus a bounded recent event list.
// Costs are estimates based on the built-in context-tiered catalog.
type Snapshot struct {
	GeneratedAt        time.Time      `json:"generated_at"`
	StartedAt          time.Time      `json:"started_at"`
	BootID             string         `json:"boot_id"`
	ProcessID          int            `json:"process_id"`
	Requests           uint64         `json:"requests"`
	FailedRequests     uint64         `json:"failed_requests"`
	InputTokens        int64          `json:"input_tokens"`
	OutputTokens       int64          `json:"output_tokens"`
	CacheHits          uint64         `json:"cache_hits"`
	CacheMisses        uint64         `json:"cache_misses"`
	CacheUnknown       uint64         `json:"cache_unknown"`
	Compactions        uint64         `json:"compactions"`
	CompactionAttempts uint64         `json:"compaction_attempts"`
	EstimatedCostUSD   float64        `json:"estimated_cost_usd"`
	CostEstimated      bool           `json:"cost_estimated"`
	PricedRequests     uint64         `json:"priced_requests"`
	UnpricedRequests   uint64         `json:"unpriced_requests"`
	EarliestSequence   uint64         `json:"earliest_sequence"`
	LatestSequence     uint64         `json:"latest_sequence"`
	NextAfter          uint64         `json:"next_after"`
	EventGap           bool           `json:"event_gap"`
	GapFromSequence    uint64         `json:"gap_from_sequence,omitempty"`
	GapToSequence      uint64         `json:"gap_to_sequence,omitempty"`
	CursorReset        bool           `json:"cursor_reset,omitempty"`
	RecentEvents       []RequestEvent `json:"recent_events"`
}

// Tracker implements usage.Plugin.
type Tracker struct {
	mu        sync.RWMutex
	maxEvents int
	next      uint64
	snapshot  Snapshot
	events    []RequestEvent
}

// NewTracker constructs a process-lifetime tracker. maxEvents bounds the
// recent event list; values below one keep totals without retaining events.
func NewTracker(maxEvents int) *Tracker {
	now := time.Now().UTC()
	return &Tracker{
		maxEvents: maxEvents,
		snapshot: Snapshot{
			StartedAt:     now,
			BootID:        uuid.NewString(),
			ProcessID:     os.Getpid(),
			CostEstimated: true,
		},
	}
}

// HandleUsage normalizes, records, and logs one provider usage record.
func (t *Tracker) HandleUsage(_ context.Context, record usage.Record) {
	if t == nil {
		return
	}

	event := normalizeRecord(record)
	t.mu.Lock()
	t.next++
	event.Sequence = t.next
	t.snapshot.Requests++
	if event.Failed {
		t.snapshot.FailedRequests++
	}
	t.snapshot.InputTokens += event.InputTokens
	t.snapshot.OutputTokens += event.OutputTokens
	switch event.CacheOutcome {
	case CacheOutcomeHit:
		t.snapshot.CacheHits++
	case CacheOutcomeMiss:
		t.snapshot.CacheMisses++
	default:
		t.snapshot.CacheUnknown++
	}
	if event.Compaction {
		t.snapshot.CompactionAttempts++
		if !event.Failed {
			t.snapshot.Compactions++
		}
	}
	if event.EstimatedCostAvailable {
		t.snapshot.EstimatedCostUSD += event.EstimatedCostUSD
		t.snapshot.PricedRequests++
	} else {
		t.snapshot.UnpricedRequests++
	}
	t.appendEventLocked(event)
	t.mu.Unlock()

	logRequestEvent(event)
}

// Snapshot returns a defensive copy of the current process-lifetime totals.
func (t *Tracker) Snapshot() Snapshot {
	if t == nil {
		return Snapshot{GeneratedAt: time.Now().UTC(), CostEstimated: true}
	}
	return t.SnapshotAfter(0, t.maxEvents)
}

// BootID identifies this tracker instance. It changes on every process start,
// allowing cursor consumers to distinguish a restart from an empty poll.
func (t *Tracker) BootID() string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	bootID := t.snapshot.BootID
	t.mu.RUnlock()
	return bootID
}

// SnapshotAfter returns process totals and, in ascending sequence order, up to
// limit retained events newer than after. EventGap identifies sequence numbers
// that fell out of the bounded buffer before a consumer read them.
func (t *Tracker) SnapshotAfter(after uint64, limit int) Snapshot {
	if t == nil {
		return Snapshot{GeneratedAt: time.Now().UTC(), CostEstimated: true}
	}
	if limit < 0 {
		limit = 0
	}

	t.mu.RLock()
	snapshot := t.snapshot
	snapshot.LatestSequence = t.next
	if len(t.events) > 0 {
		snapshot.EarliestSequence = t.events[0].Sequence
	}

	readAfter := after
	if snapshot.LatestSequence > after {
		switch {
		case len(t.events) == 0:
			snapshot.EventGap = true
			snapshot.GapFromSequence = after + 1
			snapshot.GapToSequence = snapshot.LatestSequence
			readAfter = snapshot.LatestSequence
		case snapshot.EarliestSequence > after+1:
			snapshot.EventGap = true
			snapshot.GapFromSequence = after + 1
			snapshot.GapToSequence = snapshot.EarliestSequence - 1
			readAfter = snapshot.EarliestSequence - 1
		}
	}

	if limit > 0 {
		snapshot.RecentEvents = make([]RequestEvent, 0, min(limit, len(t.events)))
		for _, event := range t.events {
			if event.Sequence <= readAfter {
				continue
			}
			snapshot.RecentEvents = append(snapshot.RecentEvents, event)
			if len(snapshot.RecentEvents) == limit {
				break
			}
		}
	}
	t.mu.RUnlock()

	snapshot.NextAfter = readAfter
	if len(snapshot.RecentEvents) > 0 {
		snapshot.NextAfter = snapshot.RecentEvents[len(snapshot.RecentEvents)-1].Sequence
	}
	snapshot.GeneratedAt = time.Now().UTC()
	return snapshot
}

func (t *Tracker) appendEventLocked(event RequestEvent) {
	if t.maxEvents < 1 {
		return
	}
	if len(t.events) >= t.maxEvents {
		copy(t.events, t.events[1:])
		t.events[len(t.events)-1] = event
		return
	}
	t.events = append(t.events, event)
}

type modelRates struct {
	input      float64
	cacheRead  float64
	cacheWrite float64
	output     float64
}

// Rates are USD per million tokens for short-context traffic.
var shortContextRates = map[string]modelRates{
	"gpt-5.6-sol":    {input: 5, cacheRead: 0.5, cacheWrite: 6.25, output: 30},
	"gpt-5.6-terra":  {input: 2.5, cacheRead: 0.25, cacheWrite: 3.125, output: 15},
	"gpt-5.6-luna":   {input: 1, cacheRead: 0.1, cacheWrite: 1.25, output: 6},
	"claude-fable-5": {input: 10, cacheRead: 1, cacheWrite: 12.5, output: 50},
}

const openAILongContextThreshold int64 = 272_000

const (
	fableCacheWrite5mRate = 12.5
	fableCacheWrite1hRate = 20.0
)

// OpenAI prices the full request at this tier when total input is greater
// than 272K tokens.
var longContextRates = map[string]modelRates{
	"gpt-5.6-sol":   {input: 10, cacheRead: 1, cacheWrite: 12.5, output: 45},
	"gpt-5.6-terra": {input: 5, cacheRead: 0.5, cacheWrite: 6.25, output: 22.5},
	"gpt-5.6-luna":  {input: 2, cacheRead: 0.2, cacheWrite: 2.5, output: 9},
}

func normalizeRecord(record usage.Record) RequestEvent {
	operation := strings.ToLower(strings.TrimSpace(record.Operation))
	if operation == "" {
		operation = operationInference
	}

	provider := strings.ToLower(strings.TrimSpace(record.Provider))
	cacheRead := record.Detail.CacheReadTokens
	if cacheRead == 0 && !record.Detail.CacheTelemetryPresent && record.Detail.CachedTokens > 0 {
		cacheRead = record.Detail.CachedTokens
	}
	cacheWrite := record.Detail.CacheCreationTokens
	cacheTelemetryPresent := record.Detail.CacheTelemetryPresent || cacheRead != 0 || cacheWrite != 0
	cacheOutcome := CacheOutcomeUnknown
	if cacheTelemetryPresent {
		cacheOutcome = CacheOutcomeMiss
		if cacheRead > 0 {
			cacheOutcome = CacheOutcomeHit
		}
	}

	input := record.Detail.InputTokens
	if provider == "claude" {
		// Anthropic reports uncached input separately from cache reads/writes.
		input += cacheRead + cacheWrite
	}

	cost, catalogModel, costTier, costAvailable := estimatedCost(record, cacheRead, cacheWrite)
	timestamp := time.Now().UTC()
	return RequestEvent{
		Timestamp:                 timestamp,
		Provider:                  provider,
		Model:                     strings.TrimSpace(record.Model),
		Operation:                 operation,
		InputTokens:               input,
		OutputTokens:              record.Detail.OutputTokens,
		CacheReadTokens:           cacheRead,
		CacheWriteTokens:          cacheWrite,
		CacheTelemetryPresent:     cacheTelemetryPresent,
		CacheOutcome:              cacheOutcome,
		CacheMiss:                 cacheOutcome == CacheOutcomeMiss,
		Compaction:                operation == operationCompaction,
		Failed:                    record.Failed,
		LatencyMilliseconds:       record.Latency.Milliseconds(),
		EstimatedCostUSD:          cost,
		EstimatedCostAvailable:    costAvailable,
		EstimatedCostCatalogModel: catalogModel,
		EstimatedCostTier:         costTier,
	}
}

func estimatedCost(record usage.Record, cacheRead, cacheWrite int64) (float64, string, string, bool) {
	catalogModel, rates, tier, ok := resolveRates(record.Model, record.Alias, record.Detail.InputTokens)
	if !ok {
		return 0, "", "", false
	}
	if record.Failed && !hasPriceableUsage(record.Detail) && !record.Detail.CacheTelemetryPresent {
		return 0, catalogModel, tier, false
	}

	uncachedInput := record.Detail.InputTokens
	if strings.ToLower(strings.TrimSpace(record.Provider)) != "claude" {
		// OpenAI Responses input_tokens includes cached input tokens.
		uncachedInput -= cacheRead + cacheWrite
		if uncachedInput < 0 {
			uncachedInput = 0
		}
	}
	cacheWriteCost := float64(cacheWrite) * rates.cacheWrite
	if catalogModel == "claude-fable-5" {
		var cacheWriteCostOK bool
		cacheWriteCost, cacheWriteCostOK = fableCacheWriteCost(record.Detail, cacheWrite)
		if !cacheWriteCostOK {
			return 0, catalogModel, tier, false
		}
	}
	cost := float64(uncachedInput)*rates.input +
		float64(cacheRead)*rates.cacheRead +
		cacheWriteCost +
		float64(record.Detail.OutputTokens)*rates.output
	return cost / 1_000_000, catalogModel, tier, true
}

func hasPriceableUsage(detail usage.Detail) bool {
	return detail.InputTokens != 0 ||
		detail.OutputTokens != 0 ||
		detail.CacheReadTokens != 0 ||
		detail.CacheCreationTokens != 0 ||
		detail.CacheCreation5mTokens != 0 ||
		detail.CacheCreation1hTokens != 0
}

func fableCacheWriteCost(detail usage.Detail, totalCacheWrite int64) (float64, bool) {
	cacheWrite5m := detail.CacheCreation5mTokens
	cacheWrite1h := detail.CacheCreation1hTokens
	if totalCacheWrite < 0 || cacheWrite5m < 0 || cacheWrite1h < 0 {
		return 0, false
	}
	classified := cacheWrite5m + cacheWrite1h
	if classified < cacheWrite5m || classified > totalCacheWrite {
		// Contradictory provider telemetry cannot be priced accurately.
		return 0, false
	}
	// Anthropic's top-level cache-creation total can occasionally arrive
	// without the TTL breakdown. Price any unknown remainder at the higher
	// one-hour rate so the estimate cannot silently understate the bill.
	unknown := totalCacheWrite - classified
	return float64(cacheWrite5m)*fableCacheWrite5mRate +
		float64(cacheWrite1h+unknown)*fableCacheWrite1hRate, true
}

func resolveRates(model, alias string, inputTokens int64) (string, modelRates, string, bool) {
	for _, candidate := range []string{model, alias} {
		candidate = normalizeModelName(candidate)
		if rates, ok := shortContextRates[candidate]; ok {
			if inputTokens > openAILongContextThreshold {
				if longRates, longKnown := longContextRates[candidate]; longKnown {
					return candidate, longRates, "long", true
				}
			}
			return candidate, rates, "short", true
		}
	}
	return "", modelRates{}, "", false
}

func normalizeModelName(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		model = strings.TrimSpace(model[idx+1:])
	}
	if idx := strings.Index(model, "("); idx >= 0 {
		model = strings.TrimSpace(model[:idx])
	}
	return model
}

func logRequestEvent(event RequestEvent) {
	cost := "unavailable"
	if event.EstimatedCostAvailable {
		cost = fmt.Sprintf("%.8f", event.EstimatedCostUSD)
	}
	log.Infof(
		"request_event operation=%s provider=%q model=%q input_tokens=%d output_tokens=%d cache_read_tokens=%d cache_write_tokens=%d cache_telemetry_present=%t cache_outcome=%s cache_miss=%t estimated_cost_usd=%s estimated_cost_tier=%s cost_estimated=%t failed=%t latency_ms=%d",
		event.Operation,
		event.Provider,
		event.Model,
		event.InputTokens,
		event.OutputTokens,
		event.CacheReadTokens,
		event.CacheWriteTokens,
		event.CacheTelemetryPresent,
		event.CacheOutcome,
		event.CacheMiss,
		cost,
		event.EstimatedCostTier,
		event.EstimatedCostAvailable,
		event.Failed,
		event.LatencyMilliseconds,
	)
}

var defaultTracker = NewTracker(defaultRecentEventLimit)

// DefaultTracker returns the process-wide tracker registered with usage.
func DefaultTracker() *Tracker { return defaultTracker }

func init() {
	usage.RegisterNamedPlugin("request-observability", defaultTracker)
}
