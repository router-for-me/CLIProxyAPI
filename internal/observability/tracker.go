// Package observability normalizes provider usage into request events and an
// in-memory process-lifetime snapshot for the management API and TUI.
package observability

import (
	"context"
	"crypto/sha256"
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
	defaultRecentEventLimit  = 200
	operationInference       = "inference"
	operationCompaction      = "compaction"
	operationCompactionReset = "compaction_reset"
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
	ReasoningEffort           string       `json:"effort,omitempty"`
	Operation                 string       `json:"operation"`
	InputTokens               int64        `json:"input_tokens"`
	OutputTokens              int64        `json:"output_tokens"`
	CacheReadTokens           int64        `json:"cache_read_tokens"`
	CacheWriteTokens          int64        `json:"cache_write_tokens"`
	CacheWriteEstimated       bool         `json:"cache_write_estimated"`
	UncachedInputTokens       int64        `json:"uncached_input_tokens"`
	CacheReadPercent          float64      `json:"cache_read_percent"`
	CacheLowReuse             bool         `json:"cache_low_reuse"`
	CacheTelemetryPresent     bool         `json:"cache_telemetry_present"`
	CacheOutcome              CacheOutcome `json:"cache_outcome"`
	CacheMiss                 bool         `json:"cache_miss"`
	Compaction                bool         `json:"compaction"`
	Failed                    bool         `json:"failed"`
	LatencyMilliseconds       int64        `json:"latency_ms"`
	EstimatedCostUSD          float64      `json:"estimated_cost_usd"`
	EstimatedInputCostUSD     float64      `json:"estimated_input_cost_usd"`
	EstimatedOutputCostUSD    float64      `json:"estimated_output_cost_usd"`
	EstimatedCacheCostUSD     float64      `json:"estimated_cache_cost_usd"`
	EstimatedCostAvailable    bool         `json:"estimated_cost_available"`
	EstimatedCostCatalogModel string       `json:"estimated_cost_catalog_model,omitempty"`
	EstimatedCostTier         string       `json:"estimated_cost_tier,omitempty"`
	ResetReason               string       `json:"reset_reason,omitempty"`
	LaneID                    string       `json:"lane_id,omitempty"`
	AgentID                   string       `json:"agent_id,omitempty"`
	PreviousEnvelopeID        string       `json:"previous_envelope_id,omitempty"`
	EnvelopeID                string       `json:"envelope_id,omitempty"`
}

// Snapshot contains process-lifetime totals plus a bounded recent event list.
// Costs are estimates based on the built-in context-tiered catalog.
type Snapshot struct {
	GeneratedAt           time.Time      `json:"generated_at"`
	StartedAt             time.Time      `json:"started_at"`
	BootID                string         `json:"boot_id"`
	ProcessID             int            `json:"process_id"`
	Requests              uint64         `json:"requests"`
	FailedRequests        uint64         `json:"failed_requests"`
	InputTokens           int64          `json:"input_tokens"`
	OutputTokens          int64          `json:"output_tokens"`
	CacheHits             uint64         `json:"cache_hits"`
	CacheMisses           uint64         `json:"cache_misses"`
	CacheUnknown          uint64         `json:"cache_unknown"`
	CacheLowReuseRequests uint64         `json:"cache_low_reuse_requests"`
	Compactions           uint64         `json:"compactions"`
	CompactionAttempts    uint64         `json:"compaction_attempts"`
	CompactionResets      uint64         `json:"compaction_resets"`
	EstimatedCostUSD      float64        `json:"estimated_cost_usd"`
	CostEstimated         bool           `json:"cost_estimated"`
	PricedRequests        uint64         `json:"priced_requests"`
	UnpricedRequests      uint64         `json:"unpriced_requests"`
	EarliestSequence      uint64         `json:"earliest_sequence"`
	LatestSequence        uint64         `json:"latest_sequence"`
	NextAfter             uint64         `json:"next_after"`
	EventGap              bool           `json:"event_gap"`
	GapFromSequence       uint64         `json:"gap_from_sequence,omitempty"`
	GapToSequence         uint64         `json:"gap_to_sequence,omitempty"`
	CursorReset           bool           `json:"cursor_reset,omitempty"`
	RecentEvents          []RequestEvent `json:"recent_events"`
}

// CompactionResetDiagnostic describes a proxy-side compaction lane reset.
// Identity material remains private so callers cannot accidentally place raw
// session, agent, or envelope identifiers in management responses or logs.
type CompactionResetDiagnostic struct {
	timestamp          time.Time
	provider           string
	model              string
	reason             string
	laneID             string
	agentID            string
	previousEnvelopeID string
	envelopeID         string
}

// NewCompactionResetDiagnostic constructs a reset diagnostic while replacing
// all opaque identity material with stable, short SHA-256 fingerprints.
func NewCompactionResetDiagnostic(provider, model, reason, laneMaterial, agentID, previousEnvelope, envelope string) CompactionResetDiagnostic {
	return CompactionResetDiagnostic{
		timestamp:          time.Now().UTC(),
		provider:           strings.ToLower(strings.TrimSpace(provider)),
		model:              strings.TrimSpace(model),
		reason:             normalizeDiagnosticReason(reason),
		laneID:             diagnosticFingerprint(laneMaterial),
		agentID:            diagnosticFingerprint(agentID),
		previousEnvelopeID: diagnosticFingerprint(previousEnvelope),
		envelopeID:         diagnosticFingerprint(envelope),
	}
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
	if event.CacheLowReuse {
		t.snapshot.CacheLowReuseRequests++
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

// RecordCompactionReset appends a non-request diagnostic event. It advances
// the event cursor and reset total without changing request, cache, token,
// pricing, or successful-compaction counters.
func (t *Tracker) RecordCompactionReset(diagnostic CompactionResetDiagnostic) {
	if t == nil {
		return
	}
	timestamp := diagnostic.timestamp
	if timestamp.IsZero() {
		timestamp = time.Now().UTC()
	}
	event := RequestEvent{
		Timestamp:          timestamp,
		Provider:           diagnostic.provider,
		Model:              diagnostic.model,
		Operation:          operationCompactionReset,
		CacheOutcome:       CacheOutcomeUnknown,
		ResetReason:        diagnostic.reason,
		LaneID:             diagnostic.laneID,
		AgentID:            diagnostic.agentID,
		PreviousEnvelopeID: diagnostic.previousEnvelopeID,
		EnvelopeID:         diagnostic.envelopeID,
	}

	t.mu.Lock()
	t.next++
	event.Sequence = t.next
	t.snapshot.CompactionResets++
	t.appendEventLocked(event)
	t.mu.Unlock()

	logCompactionResetEvent(event)
}

// RecordCompactionReset appends a diagnostic to the process-wide tracker.
func RecordCompactionReset(diagnostic CompactionResetDiagnostic) {
	DefaultTracker().RecordCompactionReset(diagnostic)
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
	reportedCacheWrite := record.Detail.CacheCreationTokens
	cacheTelemetryPresent := record.Detail.CacheTelemetryPresent ||
		cacheRead != 0 || reportedCacheWrite != 0 || record.Detail.CacheCreationEstimateAvailable
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
		input += cacheRead + reportedCacheWrite
	}
	cacheWrite := reportedCacheWrite
	cacheWriteEstimated := false
	if reportedCacheWrite == 0 && record.Detail.CacheCreationEstimateAvailable {
		cacheWrite = record.Detail.EstimatedCacheCreationTokens
		if cacheWrite < 0 {
			cacheWrite = 0
		}
		cacheWriteEstimated = true
	} else if provider == "codex" && reportedCacheWrite == 0 && cacheTelemetryPresent {
		// Codex Responses reports discounted cache reads but no explicit write
		// bucket. Keep a defensive fallback for usage producers that predate the
		// explicit estimate fields.
		cacheWrite = input - cacheRead
		if cacheWrite < 0 {
			cacheWrite = 0
		}
		cacheWriteEstimated = true
	}
	coverageCacheRead := cacheRead
	if coverageCacheRead < 0 {
		coverageCacheRead = 0
	}
	if coverageCacheRead > input && input >= 0 {
		coverageCacheRead = input
	}
	uncachedInput := input - coverageCacheRead
	if uncachedInput < 0 {
		uncachedInput = 0
	}
	cacheReadPercent := 0.0
	cacheLowReuse := false
	if cacheTelemetryPresent && input > 0 {
		cacheReadPercent = float64(coverageCacheRead) * 100 / float64(input)
		// A low-reuse request is one where the majority of normalized input
		// was not served from cache. This includes strict zero-read misses but
		// does not change the existing hit/miss/unknown classification.
		cacheLowReuse = uncachedInput > coverageCacheRead
	}

	costs, catalogModel, costTier, costAvailable := estimatedCostComponents(record, cacheRead, reportedCacheWrite)
	timestamp := time.Now().UTC()
	return RequestEvent{
		Timestamp:                 timestamp,
		Provider:                  provider,
		Model:                     strings.TrimSpace(record.Model),
		ReasoningEffort:           strings.ToLower(strings.TrimSpace(record.ReasoningEffort)),
		Operation:                 operation,
		InputTokens:               input,
		OutputTokens:              record.Detail.OutputTokens,
		CacheReadTokens:           cacheRead,
		CacheWriteTokens:          cacheWrite,
		CacheWriteEstimated:       cacheWriteEstimated,
		UncachedInputTokens:       uncachedInput,
		CacheReadPercent:          cacheReadPercent,
		CacheLowReuse:             cacheLowReuse,
		CacheTelemetryPresent:     cacheTelemetryPresent,
		CacheOutcome:              cacheOutcome,
		CacheMiss:                 cacheOutcome == CacheOutcomeMiss,
		Compaction:                operation == operationCompaction,
		Failed:                    record.Failed,
		LatencyMilliseconds:       record.Latency.Milliseconds(),
		EstimatedCostUSD:          costs.total(),
		EstimatedInputCostUSD:     costs.input,
		EstimatedOutputCostUSD:    costs.output,
		EstimatedCacheCostUSD:     costs.cache,
		EstimatedCostAvailable:    costAvailable,
		EstimatedCostCatalogModel: catalogModel,
		EstimatedCostTier:         costTier,
	}
}

func estimatedCost(record usage.Record, cacheRead, cacheWrite int64) (float64, string, string, bool) {
	costs, catalogModel, tier, ok := estimatedCostComponents(record, cacheRead, cacheWrite)
	return costs.total(), catalogModel, tier, ok
}

type costComponents struct {
	input  float64
	output float64
	cache  float64
}

func (c costComponents) total() float64 {
	return c.input + c.output + c.cache
}

func estimatedCostComponents(record usage.Record, cacheRead, cacheWrite int64) (costComponents, string, string, bool) {
	catalogModel, rates, tier, ok := resolveRates(record.Model, record.Alias, record.Detail.InputTokens)
	if !ok {
		return costComponents{}, "", "", false
	}
	if record.Failed && !hasPriceableUsage(record.Detail) && !record.Detail.CacheTelemetryPresent {
		return costComponents{}, catalogModel, tier, false
	}

	uncachedInput := record.Detail.InputTokens
	isClaude := strings.EqualFold(strings.TrimSpace(record.Provider), "claude")
	if !isClaude {
		// OpenAI Responses input_tokens includes provider-confirmed cache reads
		// and writes. A display-only estimate is never passed into this function,
		// so only confirmed writes receive the catalog's write rate.
		uncachedInput -= cacheRead + cacheWrite
		if uncachedInput < 0 {
			uncachedInput = 0
		}
	}
	cacheWriteCost := float64(cacheWrite) * rates.cacheWrite
	if isClaude && catalogModel == "claude-fable-5" {
		var cacheWriteCostOK bool
		cacheWriteCost, cacheWriteCostOK = fableCacheWriteCost(record.Detail, cacheWrite)
		if !cacheWriteCostOK {
			return costComponents{}, catalogModel, tier, false
		}
	}
	costs := costComponents{
		input:  float64(uncachedInput) * rates.input / 1_000_000,
		output: float64(record.Detail.OutputTokens) * rates.output / 1_000_000,
		cache:  (float64(cacheRead)*rates.cacheRead + cacheWriteCost) / 1_000_000,
	}
	return costs, catalogModel, tier, true
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
	inputCost := "unavailable"
	outputCost := "unavailable"
	cacheCost := "unavailable"
	if event.EstimatedCostAvailable {
		cost = fmt.Sprintf("%.8f", event.EstimatedCostUSD)
		inputCost = fmt.Sprintf("%.8f", event.EstimatedInputCostUSD)
		outputCost = fmt.Sprintf("%.8f", event.EstimatedOutputCostUSD)
		cacheCost = fmt.Sprintf("%.8f", event.EstimatedCacheCostUSD)
	}
	log.Infof(
		"request_event operation=%s provider=%q model=%q effort=%q input_tokens=%d output_tokens=%d cache_read_tokens=%d cache_write_tokens=%d cache_write_estimated=%t uncached_input_tokens=%d cache_read_percent=%.2f cache_low_reuse=%t cache_telemetry_present=%t cache_outcome=%s cache_miss=%t estimated_input_cost_usd=%s estimated_output_cost_usd=%s estimated_cache_cost_usd=%s estimated_cost_usd=%s estimated_cost_tier=%s cost_estimated=%t failed=%t latency_ms=%d",
		event.Operation,
		event.Provider,
		event.Model,
		event.ReasoningEffort,
		event.InputTokens,
		event.OutputTokens,
		event.CacheReadTokens,
		event.CacheWriteTokens,
		event.CacheWriteEstimated,
		event.UncachedInputTokens,
		event.CacheReadPercent,
		event.CacheLowReuse,
		event.CacheTelemetryPresent,
		event.CacheOutcome,
		event.CacheMiss,
		inputCost,
		outputCost,
		cacheCost,
		cost,
		event.EstimatedCostTier,
		event.EstimatedCostAvailable,
		event.Failed,
		event.LatencyMilliseconds,
	)
}

func logCompactionResetEvent(event RequestEvent) {
	log.Warnf(
		"compaction_event operation=%s provider=%q model=%q reason=%q lane_id=%q agent_id=%q previous_envelope_id=%q envelope_id=%q",
		event.Operation,
		event.Provider,
		event.Model,
		event.ResetReason,
		event.LaneID,
		event.AgentID,
		event.PreviousEnvelopeID,
		event.EnvelopeID,
	)
}

func diagnosticFingerprint(material string) string {
	material = strings.TrimSpace(material)
	if material == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(material))
	return fmt.Sprintf("%x", digest[:8])
}

func normalizeDiagnosticReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return "unknown"
	}
	var normalized strings.Builder
	normalized.Grow(min(len(reason), 64))
	for _, r := range reason {
		if normalized.Len() >= 64 {
			break
		}
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			normalized.WriteRune(r)
		case r == ' ':
			normalized.WriteByte('_')
		}
	}
	if normalized.Len() == 0 {
		return "unknown"
	}
	return normalized.String()
}

var defaultTracker = NewTracker(defaultRecentEventLimit)

// DefaultTracker returns the process-wide tracker registered with usage.
func DefaultTracker() *Tracker { return defaultTracker }

func init() {
	usage.RegisterNamedPlugin("request-observability", defaultTracker)
}
