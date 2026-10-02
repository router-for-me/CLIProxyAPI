package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// UsageWindowType distinguishes the granularity at which a budget window rolls.
const (
	WindowTypeHourly  = "hourly"
	WindowTypeWeekly  = "weekly"
	WindowTypeMonthly = "monthly"
)

// UsageEvent is the persisted representation of a request's usage record. The
// fields parallel the in-memory coreusage.Record but include the resolved
// api_key_id and computed cost_usd that the hot path does not know at publish
// time.
type UsageEvent struct {
	ID              int64  `json:"id,omitempty"`
	RequestID       string `json:"request_id,omitempty"`
	APIKeyID        string `json:"api_key_id,omitempty"`
	APIKeyPrincipal string `json:"api_key_principal,omitempty"`
	UserID          string `json:"user_id,omitempty"`
	Provider        string `json:"provider"`
	ExecutorType    string `json:"executor_type,omitempty"`
	Model           string `json:"model"`
	// ServedModel is the model the upstream response reported serving. It
	// differs from Model when the provider silently substituted a different
	// model; empty when the upstream did not report one.
	ServedModel         string `json:"served_model,omitempty"`
	Alias               string `json:"alias,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	ClientIP            string `json:"client_ip,omitempty"`
	ForwardedFor        string `json:"forwarded_for,omitempty"`
	AuthType            string `json:"auth_type,omitempty"`
	Source              string `json:"source,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	ServiceTier         string `json:"service_tier,omitempty"`
	ResponseServiceTier string `json:"response_service_tier,omitempty"`
	// Tier stores the Auto Router complexity tier (e.g. simple/medium/complex/
	// reasoning) chosen for the request. Empty for non-routed requests.
	Tier string `json:"tier,omitempty"`
	// RouterID stores the Auto Router id (e.g. "router:smart") that owned the
	// tier decision. Empty for non-routed requests.
	RouterID string `json:"router_id,omitempty"`
	// ScoredTier is the tier the scorer derived from the configured thresholds
	// and weights before any keyword override or mapping fallback. Empty for
	// non-routed requests.
	ScoredTier string `json:"scored_tier,omitempty"`
	// EffectiveTier mirrors Tier but is persisted as its own column for
	// fast filtering alongside ScoredTier. Empty for non-routed requests.
	EffectiveTier string `json:"effective_tier,omitempty"`
	// MappingTier is the tier whose mapping the resolver actually used to
	// pick a target model. May be lower than the effective tier when the
	// resolver fell back. Empty for non-routed requests.
	MappingTier string `json:"mapping_tier,omitempty"`
	// DecisionCause explains why the effective tier differs from the scored
	// tier (literal_keyword_match) or matches it (complexity_scorer).
	DecisionCause string `json:"decision_cause,omitempty"`
	// ProfileVersion identifies the Auto Router profile version used to score
	// the request. Zero when the default built-in profile was used.
	ProfileVersion int64 `json:"profile_version,omitempty"`
	// ProfileHash identifies the exact profile configuration used.
	ProfileHash string `json:"profile_hash,omitempty"`
	// AutoRouterDecision is the marshaled explainability snapshot. Empty
	// bytes mean "no snapshot" (non-routed requests).
	AutoRouterDecision  []byte  `json:"auto_router_decision,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	// DiscountPct is the resolved model-group discount percentage (0-100)
	// applied to CostUSD when the requesting key's policy attaches to a model
	// group carrying a discount. The stored CostUSD is the post-discount value;
	// OriginalCostUSD (below) is the pre-discount figure stamped alongside.
	// 0 = no discount applied. Persisted column, stamped at write time.
	DiscountPct float64 `json:"discount_pct,omitempty"`
	// OriginalCostUSD is the pre-discount cost (before the model-group
	// discount was multiplied in). Stamped at flush time alongside DiscountPct
	// so the read-side never re-derives it (which would drift if the pricing
	// row changed between flush and read). Equals CostUSD when no discount was
	// applied. Persisted column.
	OriginalCostUSD float64 `json:"original_cost_usd,omitempty"`
	// EnergyJoules is the upstream-reported energy consumption for the
	// request, when measured. nil for providers that do not report it (and
	// for unmeasured responses) — persisted as SQL NULL rather than a
	// misleading 0.
	EnergyJoules *float64 `json:"energy_joules,omitempty"`
	// ProviderMetadata carries provider-specific billing/attribution data as
	// a JSON object keyed by provider name (e.g.
	// {"neuralwatt": {"request_cost_usd": 0.0034}}). Persisted as a NOT NULL
	// jsonb column defaulting to '{}' so readers can rely on a stable shape.
	ProviderMetadata map[string]any `json:"provider_metadata,omitempty"`
	LatencyMs        int64          `json:"latency_ms,omitempty"`
	TTFTMs           int64          `json:"ttft_ms,omitempty"`
	Failed           bool           `json:"failed"`
	FailStatusCode   int            `json:"fail_status_code,omitempty"`
	Generate         bool           `json:"generate,omitempty"`
	RequestedAt      time.Time      `json:"requested_at"`
}

// Pricing captures per-model unit prices in USD per 1,000,000 tokens. A zero
// value (or missing row) is treated as "no cost" — usage still records the
// token counts but contributes 0 USD toward budget enforcement.
type Pricing struct {
	ID               string  `json:"id"`
	InputPer1M       float64 `json:"input_per_1m_usd"`
	OutputPer1M      float64 `json:"output_per_1m_usd"`
	CachedInputPer1M float64 `json:"cached_input_per_1m_usd"`
	CachedReadPer1M  float64 `json:"cached_read_per_1m_usd"`
	ReasoningPer1M   float64 `json:"reasoning_per_1m_usd"`
}

// UsageFilter specifies the slice of usage data to aggregate.
type UsageFilter struct {
	APIKeyID  string
	Principal string
	Provider  string
	Model     string
	// RouterID narrows to events routed by a specific Auto Router id. Exact
	// match against usage_events.router_id; empty means no constraint.
	RouterID string
	UserID   string
	// RequestID narrows to a single per-request correlation identifier. Exact
	// match against usage_events.request_id / usage_errors.request_id; empty
	// means no constraint.
	RequestID string
	From      time.Time
	To        time.Time
	// GroupBy selects the aggregation dimension: "api_key_id" | "model" |
	// "provider" | "user_id" | "day" | "hour" | "" (no grouping, totals only).
	GroupBy string
	Limit   int
}

// UsageAggregate mirrors a single aggregated row returned by SelectAggregate.
type UsageAggregate struct {
	Bucket          string  `json:"bucket,omitempty"`
	APIKeyID        string  `json:"api_key_id,omitempty"`
	APIKeyPrincipal string  `json:"api_key_principal,omitempty"`
	Model           string  `json:"model,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	RequestCount    int64   `json:"request_count"`
	FailedCount     int64   `json:"failed_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
}

// AutoRouterTierStat is one aggregated row per complexity tier for a router.
type AutoRouterTierStat struct {
	Tier         string  `json:"tier"`
	RequestCount int64   `json:"request_count"`
	TotalTokens  int64   `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// AutoRouterModelStat is one aggregated row per target model for a router.
type AutoRouterModelStat struct {
	Model         string  `json:"model"`
	RequestCount  int64   `json:"request_count"`
	TotalTokens   int64   `json:"total_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	AvgCostPerReq float64 `json:"avg_cost_per_request"`
}

// AutoRouterScoreBucket is one histogram cell of the weighted score total:
// bucket i covers [0.05*i, 0.05*(i+1)) for i in 0..19.
type AutoRouterScoreBucket struct {
	Bucket int   `json:"bucket"`
	Count  int64 `json:"count"`
}

// AutoRouterChainCount counts occurrences of one fallback chain (serialized
// tier list) across a router's decision events.
type AutoRouterChainCount struct {
	Chain string `json:"chain"`
	Count int64  `json:"count"`
}

// AutoRouterDecisionStats is the decision-distribution rollup over one
// router's stored decision snapshots.
type AutoRouterDecisionStats struct {
	EventCount        int64                   `json:"event_count"`
	ScoreHistogram    []AutoRouterScoreBucket `json:"score_histogram"`
	DimensionAverages map[string]float64      `json:"dimension_averages"`
	CauseCounts       map[string]int64        `json:"cause_counts"`
	FallbackChains    []AutoRouterChainCount  `json:"fallback_chains"`
	MismatchCount     int64                   `json:"mismatch_count"`
}

// AutoRouterJevStats is the rollup of the Jev AI classifier's contribution to
// one router's routing decisions. Every field is derived from the persisted
// `auto_router_decision->'jev'` block plus the effective tier, so it reflects
// what the classifier actually did — not what it was configured to do (the
// configured knobs travel separately in the management response's router
// block).
//
// Only the two JSONB keys the aggregation needs (choice, confidence) are read;
// the persisted probability distribution is never selected, which keeps the
// scanned rows small.
type AutoRouterJevStats struct {
	// Consulted counts routed events that carry a classifier block, i.e. the
	// gate was reached and produced a verdict.
	Consulted int64 `json:"consulted"`
	// Verdict outcome counts; these sum to Consulted.
	Accepted      int64 `json:"accepted"`
	LowConfidence int64 `json:"low_confidence"`
	Errors        int64 `json:"errors"`
	BreakerOpen   int64 `json:"breaker_open"`
	// CacheHits counts verdicts served from the process-local cache. A cache
	// hit carries the original call's latency and tokens, so it must not be
	// used to compute per-call cost.
	CacheHits int64 `json:"cache_hits"`
	// Confidence statistics over the consulted events that carry a confidence.
	// DecidedCount is that denominator; a breaker-open verdict has no
	// confidence and is excluded rather than counted as 0.
	AvgConfidence float64 `json:"avg_confidence"`
	P95Confidence float64 `json:"p95_confidence"`
	DecidedCount  int64   `json:"decided_count"`
	// Overrouted counts events where the classifier's choice sat above the
	// heuristic's scored tier; Underrouted the reverse. Overrouting costs
	// money, underrouting costs capability, so they are reported separately.
	//
	// The heuristic tier is read from the scored_tier column, which is the
	// scorer's tier *before* keyword-rule overrides. A keyword rule can
	// therefore make the heuristic's own final tier differ from scored_tier,
	// so these are the classifier-versus-scorer disagreement counts, not
	// classifier-versus-final-routing counts.
	Overrouted  int64 `json:"overrouted"`
	Underrouted int64 `json:"underrouted"`
	// OverrideCount counts accepted verdicts whose choice differed from the
	// heuristic tier — i.e. requests the classifier actually re-routed rather
	// than merely agreeing with.
	OverrideCount int64 `json:"override_count"`
	// ChoiceCounts is the classifier's tier choice distribution over every
	// verdict that named a tier, seeded with all four canonical tiers so a
	// consumer renders 0 rather than a gap. It includes verdicts that were
	// rejected for low confidence — what the classifier wanted, whether or not
	// it was used.
	ChoiceCounts map[string]int64 `json:"choice_counts"`
	// AppliedChoiceCounts is the same distribution restricted to accepted
	// verdicts, i.e. the tiers that were actually routed. The two together show
	// how much of the classifier's opinion the floor discards.
	AppliedChoiceCounts map[string]int64 `json:"applied_choice_counts"`
	// ChoiceVsScored maps "choice|scored_tier" to a count: the diagonal is
	// agreement, above is over-routing, below is under-routing. Only observed
	// pairs are present; consumers seed missing cells themselves.
	ChoiceVsScored map[string]int64 `json:"choice_vs_scored"`
	// OverroutedByTier attributes over-routing to the heuristic tier the
	// request was scored at, so an operator can see which tier the classifier
	// escalates away from.
	OverroutedByTier map[string]int64 `json:"overrouted_by_tier"`
	// ConfidenceHistogram is 20 buckets of 0.05, mirroring the score histogram.
	ConfidenceHistogram []AutoRouterScoreBucket `json:"confidence_histogram"`
	// Classifier call cost. These are averaged over every consulted event that
	// reports the field, which includes cache hits — a hit re-serves the
	// original call's latency and token count rather than performing a fresh
	// one, so a warm cache makes both figures an upper bound on the real
	// per-call cost. InputTokens is the raw sum behind AvgInputTokens.
	AvgLatencyMs   float64 `json:"avg_latency_ms"`
	AvgInputTokens float64 `json:"avg_input_tokens"`
	InputTokens    int64   `json:"input_tokens"`
}

// AutoRouterTierPerformance is one tier × target-model performance row.
type AutoRouterTierPerformance struct {
	Tier         string  `json:"tier"`
	Model        string  `json:"model"`
	RequestCount int64   `json:"request_count"`
	P50LatencyMs float64 `json:"p50_latency_ms"`
	P95LatencyMs float64 `json:"p95_latency_ms"`
	AvgTTFTMs    float64 `json:"avg_ttft_ms"`
	CostUSD      float64 `json:"cost_usd"`
	ErrorCount   int64   `json:"error_count"`
	ErrorRate    float64 `json:"error_rate"`
}

// AutoRouterDecisionFilter narrows the auto-router decisions listing. Empty
// fields mean "no constraint". RouterID is required by the management
// endpoint; the rest are optional and stack.
type AutoRouterDecisionFilter struct {
	APIKeyID      string
	ScoredTier    string
	EffectiveTier string
	MappingTier   string
	DecisionCause string
	TargetModel   string
	ProfileHash   string
	From          time.Time
	To            time.Time
}

// AutoRouterDecisionRow is one explainability record returned by
// ListAutoRouterDecisions. It carries the persisted snapshot fields plus the
// minimal request metadata needed to reconcile against usage_events.
type AutoRouterDecisionRow struct {
	ID                 int64           `json:"id"`
	RequestedAt        time.Time       `json:"requested_at"`
	RequestID          string          `json:"request_id,omitempty"`
	APIKeyID           string          `json:"api_key_id,omitempty"`
	Model              string          `json:"model"`
	Alias              string          `json:"alias,omitempty"`
	ScoredTier         string          `json:"scored_tier,omitempty"`
	EffectiveTier      string          `json:"effective_tier,omitempty"`
	MappingTier        string          `json:"mapping_tier,omitempty"`
	DecisionCause      string          `json:"decision_cause,omitempty"`
	ProfileVersion     int64           `json:"profile_version,omitempty"`
	ProfileHash        string          `json:"profile_hash,omitempty"`
	AutoRouterDecision json.RawMessage `json:"auto_router_decision,omitempty"`
	InputTokens        int64           `json:"input_tokens"`
	OutputTokens       int64           `json:"output_tokens"`
	TotalTokens        int64           `json:"total_tokens"`
	CostUSD            float64         `json:"cost_usd"`
	// Event metrics surfaced by the replay endpoint; zero on list responses
	// that did not select them.
	LatencyMs      int64 `json:"latency_ms,omitempty"`
	TTFTMs         int64 `json:"ttft_ms,omitempty"`
	Failed         bool  `json:"failed,omitempty"`
	FailStatusCode int   `json:"fail_status_code,omitempty"`
}

// AutoRouterDecisionEvent is one raw decision event selected for simulation:
// the stored snapshot plus the tier/cause fields the recompute compares
// against. Ordered newest-first by the store; the caller applies the cap.
type AutoRouterDecisionEvent struct {
	RequestID          string
	ScoredTier         string
	EffectiveTier      string
	MappingTier        string
	DecisionCause      string
	ProfileVersion     int64
	ProfileHash        string
	AutoRouterDecision []byte
}

// SelectAutoRouterDecisionEvents loads up to limit stored decision snapshots
// for a router within the optional window, newest first, plus the total count
// of matching events (so the caller can flag truncation when total > limit).
func (s *UsageStore) SelectAutoRouterDecisionEvents(ctx context.Context, routerID string, filter UsageFilter, limit int) ([]AutoRouterDecisionEvent, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	if strings.TrimSpace(routerID) == "" {
		return nil, 0, fmt.Errorf("postgres store: router_id is required for decision events")
	}
	if limit <= 0 {
		limit = 10000
	}
	var b strings.Builder
	args := []any{routerID}
	b.WriteString(`SELECT COALESCE(e.request_id, ''), COALESCE(e.scored_tier, ''), COALESCE(e.effective_tier, ''),
		COALESCE(e.mapping_tier, ''), COALESCE(e.decision_cause, ''), COALESCE(e.profile_version, 0),
		COALESCE(e.profile_hash, ''), e.auto_router_decision
		FROM ` + s.eventsTable + ` e
		WHERE e.router_id = $1 AND e.router_id IS NOT NULL AND e.router_id <> ''
		AND e.auto_router_decision IS NOT NULL
		AND jsonb_typeof(e.auto_router_decision) = 'object'`)
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(` AND e.api_key_id = $`)
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(` AND e.requested_at >= $`)
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(` AND e.requested_at < $`)
		b.WriteString(itoa(len(args)))
	}
	b.WriteString(` ORDER BY e.requested_at DESC, e.id DESC LIMIT $`)
	args = append(args, limit)
	b.WriteString(itoa(len(args)))

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: select auto-router decision events: %w", err)
	}
	defer rows.Close()
	out := make([]AutoRouterDecisionEvent, 0, limit)
	for rows.Next() {
		var ev AutoRouterDecisionEvent
		if err := rows.Scan(&ev.RequestID, &ev.ScoredTier, &ev.EffectiveTier, &ev.MappingTier,
			&ev.DecisionCause, &ev.ProfileVersion, &ev.ProfileHash, &ev.AutoRouterDecision); err != nil {
			return nil, 0, err
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	// Total matching events (same predicate, no LIMIT) for truncation flags.
	var countB strings.Builder
	cargs := []any{routerID}
	countB.WriteString(`SELECT COUNT(*) FROM ` + s.eventsTable + ` e
		WHERE e.router_id = $1 AND e.router_id IS NOT NULL AND e.router_id <> ''
		AND e.auto_router_decision IS NOT NULL
		AND jsonb_typeof(e.auto_router_decision) = 'object'`)
	if filter.APIKeyID != "" {
		cargs = append(cargs, filter.APIKeyID)
		countB.WriteString(` AND e.api_key_id = $`)
		countB.WriteString(itoa(len(cargs)))
	}
	if !filter.From.IsZero() {
		cargs = append(cargs, filter.From)
		countB.WriteString(` AND e.requested_at >= $`)
		countB.WriteString(itoa(len(cargs)))
	}
	if !filter.To.IsZero() {
		cargs = append(cargs, filter.To)
		countB.WriteString(` AND e.requested_at < $`)
		countB.WriteString(itoa(len(cargs)))
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, countB.String(), cargs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count auto-router decision events: %w", err)
	}
	return out, total, nil
}

// GetAutoRouterDecisionByRequest loads the newest decision event matching
// router + request_id with its event metrics (latency/ttft/failure), for the
// replay endpoint. Returns (nil, nil) when no row matches.
func (s *UsageStore) GetAutoRouterDecisionByRequest(ctx context.Context, routerID, requestID string) (*AutoRouterDecisionRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	routerID = strings.TrimSpace(routerID)
	requestID = strings.TrimSpace(requestID)
	if routerID == "" || requestID == "" {
		return nil, fmt.Errorf("postgres store: router_id and request_id are required for decision replay")
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, requested_at, COALESCE(request_id, ''), COALESCE(api_key_id, ''),
		model, COALESCE(alias, ''), COALESCE(scored_tier, ''), COALESCE(effective_tier, ''),
		COALESCE(mapping_tier, ''), COALESCE(decision_cause, ''), COALESCE(profile_version, 0),
		COALESCE(profile_hash, ''), auto_router_decision,
		input_tokens, output_tokens, total_tokens, cost_usd,
		COALESCE(latency_ms, 0), COALESCE(ttft_ms, 0), failed, COALESCE(fail_status_code, 0)
		FROM `+s.eventsTable+`
		WHERE router_id = $1 AND router_id IS NOT NULL AND router_id <> ''
		AND request_id = $2 AND auto_router_decision IS NOT NULL
		ORDER BY requested_at DESC, id DESC LIMIT 1`, routerID, requestID)
	var (
		r        AutoRouterDecisionRow
		snapshot []byte
		at       time.Time
	)
	if err := row.Scan(&r.ID, &at, &r.RequestID, &r.APIKeyID, &r.Model, &r.Alias,
		&r.ScoredTier, &r.EffectiveTier, &r.MappingTier, &r.DecisionCause,
		&r.ProfileVersion, &r.ProfileHash, &snapshot,
		&r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.CostUSD,
		&r.LatencyMs, &r.TTFTMs, &r.Failed, &r.FailStatusCode); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("postgres store: get auto router decision by request: %w", err)
	}
	r.RequestedAt = at.UTC()
	if len(snapshot) > 0 {
		r.AutoRouterDecision = append(json.RawMessage(nil), snapshot...)
	}
	return &r, nil
}

// UsageWindow mirrors a row in usage_windows and is used both for budget
// enforcement reads and management API exposure.
type UsageWindow struct {
	APIKeyID     string    `json:"api_key_id"`
	WindowType   string    `json:"window_type"`
	WindowStart  time.Time `json:"window_start"`
	WindowEnd    time.Time `json:"window_end"`
	RequestCount int64     `json:"request_count"`
	TotalTokens  int64     `json:"total_tokens"`
	CostUSD      float64   `json:"cost_usd"`
}

// UsageStore wraps the database handle with usage-persistence operations. It
// is safe for concurrent use: the parent *sql.DB manages pooling.
type UsageStore struct {
	db                 *sql.DB
	apiKeysTable       string
	litellmKeysTable   string
	eventsTable        string
	errorsTable        string
	windowsTable       string
	rollupTable        string
	pricingTable       string
	internalUsersTable string
	modelsCatalogTable string
	requestBodiesTable string
	// sealer encrypts api_key_principal at rest. nil when no passphrase was
	// configured (writes stay plaintext; reads tolerate plaintext rows).
	sealer *Sealer
	// cache short-TTL read-through store for aggregate usage reads. nil disables
	// caching. Never nil for stores built via NewUsageStore.
	cache *usageCache
}

// NewUsageStore builds a UsageStore from a PostgresStore connection. Returns
// nil when the parent store is nil so feature-detection is a single nil check.
// The sealer is derived from the parent's UsageEncryptionKey: when empty,
// encryption is disabled and the column is stored in plaintext.
func NewUsageStore(parent *PostgresStore) *UsageStore {
	if parent == nil {
		return nil
	}
	sealer, err := NewSealer(parent.cfg.UsageEncryptionKey)
	if err != nil {
		log.Printf("postgres store: usage encryption disabled due to key error: %v", err)
		sealer = nil
	}
	return &UsageStore{
		db:                 parent.DB(),
		apiKeysTable:       parent.APIKeysTable(),
		litellmKeysTable:   parent.LiteLLMKeysTable(),
		eventsTable:        parent.UsageEventsTable(),
		errorsTable:        parent.UsageErrorsTable(),
		windowsTable:       parent.UsageWindowsTable(),
		rollupTable:        parent.RollupTable(),
		pricingTable:       parent.ModelPricingTable(),
		internalUsersTable: parent.InternalUsersTable(),
		modelsCatalogTable: parent.ModelsTable(),
		requestBodiesTable: parent.RequestBodiesTable(),
		sealer:             sealer,
		cache:              newUsageCache(),
	}
}

// SetSealer overrides the in-memory sealer. Used by tests that construct a
// UsageStore directly without going through NewPostgresStore. Production
// callers should rely on NewUsageStore instead.
func (s *UsageStore) SetSealer(sealer *Sealer) {
	if s == nil {
		return
	}
	s.sealer = sealer
}

// Sealer exposes the configured sealer (may be nil when encryption is
// disabled). Callers that need to seal/open fields outside the hot insert
// path (e.g. deriving a filter value) should reuse this.
func (s *UsageStore) Sealer() *Sealer {
	if s == nil {
		return nil
	}
	return s.sealer
}

// PricingTable returns the fully-qualified name of the model_pricing
// table backing this store. Used by callers that need to JOIN against it
// from outside the UsageStore API (e.g. the models catalog summary view).
func (s *UsageStore) PricingTable() string {
	if s == nil {
		return ""
	}
	return s.pricingTable
}

const usageEventColumnList = `
	request_id, api_key_id, api_key_principal, user_id, provider, executor_type, model, served_model,
	alias, endpoint, client_ip, forwarded_for, auth_type, source, reasoning_effort, service_tier,
	response_service_tier, tier, router_id, scored_tier, effective_tier, mapping_tier, decision_cause,
	profile_version, profile_hash, auto_router_decision, input_tokens, output_tokens, reasoning_tokens,
	cached_tokens, cache_creation_tokens, total_tokens, cost_usd, discount_pct, original_cost_usd, latency_ms,
	ttft_ms, failed, fail_status_code, generate, requested_at, energy_joules, provider_metadata
`
const usageEventColumnCount = 43

// InsertEvent records a single usage event. The api_key_principal field is
// sealed at rest via the configured Sealer before being bound. When the
// sealer is nil (no passphrase configured), the value is stored as-is.
func (s *UsageStore) InsertEvent(ctx context.Context, e UsageEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if e.RequestedAt.IsZero() {
		e.RequestedAt = time.Now().UTC()
	}
	principal, err := s.sealer.Seal(e.APIKeyPrincipal)
	if err != nil {
		// Best-effort: drop the principal rather than failing the request,
		// but surface the error so operators can detect misconfiguration.
		log.WithError(err).Warn("postgres store: seal api_key_principal failed; persisting empty")
		principal = ""
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (%s) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24, $25,
			$26, $27, $28, $29, $30, $31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43)
	`, s.eventsTable, usageEventColumnList),
		e.RequestID, nullableString(e.APIKeyID), nullableString(principal),
		nullableString(e.UserID),
		e.Provider, e.ExecutorType, e.Model, e.ServedModel, e.Alias, e.Endpoint,
		nullableString(e.ClientIP), nullableString(e.ForwardedFor),
		e.AuthType,
		e.Source, e.ReasoningEffort, e.ServiceTier, e.ResponseServiceTier,
		nullableString(e.Tier), nullableString(e.RouterID),
		nullableString(e.ScoredTier), nullableString(e.EffectiveTier), nullableString(e.MappingTier), nullableString(e.DecisionCause),
		nullableInt64(e.ProfileVersion), nullableString(e.ProfileHash), nullableJSONB(e.AutoRouterDecision),
		e.InputTokens, e.OutputTokens, e.ReasoningTokens, e.CachedTokens,
		e.CacheCreationTokens, e.TotalTokens, e.CostUSD, e.DiscountPct, e.OriginalCostUSD, e.LatencyMs, e.TTFTMs,
		e.Failed, e.FailStatusCode, e.Generate, e.RequestedAt,
		nullableFloat64Ptr(e.EnergyJoules), providerMetadataJSONB(e.ProviderMetadata),
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert usage event: %w", err)
	}
	return nil
}

// BatchInsertEvents records up to len(events) usage events in a single
// multi-value INSERT statement. Callers should bound the slice length to a
// sane batch size (the flusher defaults to 100).
func (s *UsageStore) BatchInsertEvents(ctx context.Context, events []UsageEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if len(events) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("INSERT INTO ")
	b.WriteString(s.eventsTable)
	b.WriteString(" (")
	b.WriteString(usageEventColumnList)
	b.WriteString(") VALUES ")
	args := make([]any, 0, len(events)*usageEventColumnCount)
	for i, ev := range events {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('(')
		for j := 1; j <= usageEventColumnCount; j++ {
			if j > 1 {
				b.WriteByte(',')
			}
			b.WriteByte('$')
			b.WriteString(itoa(i*usageEventColumnCount + j))
		}
		b.WriteByte(')')
		if ev.RequestedAt.IsZero() {
			ev.RequestedAt = time.Now().UTC()
		}
		// Seal the principal once per event; failures fall back to an
		// empty value so a malformed row never aborts the whole batch.
		principal, err := s.sealer.Seal(ev.APIKeyPrincipal)
		if err != nil {
			log.WithError(err).Warn("postgres store: seal api_key_principal failed in batch; persisting empty")
			principal = ""
		}
		args = append(args, ev.RequestID, nullableString(ev.APIKeyID), nullableString(principal),
			nullableString(ev.UserID),
			ev.Provider, ev.ExecutorType, ev.Model, ev.ServedModel, ev.Alias, ev.Endpoint,
			nullableString(ev.ClientIP), nullableString(ev.ForwardedFor),
			ev.AuthType,
			ev.Source, ev.ReasoningEffort, ev.ServiceTier, ev.ResponseServiceTier,
			nullableString(ev.Tier), nullableString(ev.RouterID),
			nullableString(ev.ScoredTier), nullableString(ev.EffectiveTier), nullableString(ev.MappingTier), nullableString(ev.DecisionCause),
			nullableInt64(ev.ProfileVersion), nullableString(ev.ProfileHash), nullableJSONB(ev.AutoRouterDecision),
			ev.InputTokens, ev.OutputTokens, ev.ReasoningTokens, ev.CachedTokens,
			ev.CacheCreationTokens, ev.TotalTokens, ev.CostUSD, ev.DiscountPct, ev.OriginalCostUSD, ev.LatencyMs, ev.TTFTMs,
			ev.Failed, ev.FailStatusCode, ev.Generate, ev.RequestedAt,
			nullableFloat64Ptr(ev.EnergyJoules), providerMetadataJSONB(ev.ProviderMetadata))
	}
	if _, err := s.db.ExecContext(ctx, b.String(), args...); err != nil {
		return fmt.Errorf("postgres store: batch insert usage events: %w", err)
	}
	return nil
}

// ImportLiteLLMSpendLogs inserts historical spend-log events from an external
// LiteLLM into usage_events, deduplicating by request_id so re-running a
// migration is idempotent. Events without a request_id are skipped (they have
// no stable dedup key and would otherwise duplicate on every re-sync). Returns
// the number of rows actually inserted; rows already present are counted as
// zero. Batches are bounded to 100 rows to keep the multi-value INSERT small.
func (s *UsageStore) ImportLiteLLMSpendLogs(ctx context.Context, events []UsageEvent) (int, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	const batchSize = 100
	imported := 0
	remaining := make([]UsageEvent, 0, len(events))
	for _, ev := range events {
		if strings.TrimSpace(ev.RequestID) != "" {
			remaining = append(remaining, ev)
		}
	}
	for start := 0; start < len(remaining); start += batchSize {
		end := start + batchSize
		if end > len(remaining) {
			end = len(remaining)
		}
		chunk := remaining[start:end]
		var b strings.Builder
		b.WriteString("INSERT INTO ")
		b.WriteString(s.eventsTable)
		b.WriteString(" (")
		b.WriteString(usageEventColumnList)
		b.WriteString(") VALUES ")
		args := make([]any, 0, len(chunk)*usageEventColumnCount)
		for i, ev := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteByte('(')
			for j := 1; j <= usageEventColumnCount; j++ {
				if j > 1 {
					b.WriteByte(',')
				}
				b.WriteByte('$')
				b.WriteString(itoa(i*usageEventColumnCount + j))
			}
			b.WriteByte(')')
			if ev.RequestedAt.IsZero() {
				ev.RequestedAt = time.Now().UTC()
			}
			principal, err := s.sealer.Seal(ev.APIKeyPrincipal)
			if err != nil {
				log.WithError(err).Warn("postgres store: seal api_key_principal failed in spend-log import; persisting empty")
				principal = ""
			}
			args = append(args, ev.RequestID, nullableString(ev.APIKeyID), nullableString(principal),
				nullableString(ev.UserID),
				ev.Provider, ev.ExecutorType, ev.Model, ev.ServedModel, ev.Alias, ev.Endpoint,
				nullableString(ev.ClientIP), nullableString(ev.ForwardedFor),
				ev.AuthType,
				ev.Source, ev.ReasoningEffort, ev.ServiceTier, ev.ResponseServiceTier,
				nullableString(ev.Tier), nullableString(ev.RouterID),
				nullableString(ev.ScoredTier), nullableString(ev.EffectiveTier), nullableString(ev.MappingTier), nullableString(ev.DecisionCause),
				nullableInt64(ev.ProfileVersion), nullableString(ev.ProfileHash), nullableJSONB(ev.AutoRouterDecision),
				ev.InputTokens, ev.OutputTokens, ev.ReasoningTokens, ev.CachedTokens,
				ev.CacheCreationTokens, ev.TotalTokens, ev.CostUSD, ev.DiscountPct, ev.OriginalCostUSD, ev.LatencyMs, ev.TTFTMs,
				ev.Failed, ev.FailStatusCode, ev.Generate, ev.RequestedAt,
				nullableFloat64Ptr(ev.EnergyJoules), providerMetadataJSONB(ev.ProviderMetadata))
		}
		b.WriteString(" ON CONFLICT (request_id) WHERE request_id IS NOT NULL AND request_id <> '' DO NOTHING")
		res, err := s.db.ExecContext(ctx, b.String(), args...)
		if err != nil {
			return imported, fmt.Errorf("postgres store: import litellm spend logs: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return imported, fmt.Errorf("postgres store: spend log import rows affected: %w", err)
		}
		imported += int(n)
	}
	return imported, nil
}

// itoa writes a small non-negative integer as a string without pulling in
// strconv at the call site (kept tight for the hot batch-insert builder).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}

// UpsertWindow increments the budget window counter atomically. The row is
// created on first use; subsequent calls add delta values. Window boundaries
// must be pre-computed by the caller (the policy service does this via the
// Clock helper).
// ModelSpendForKey returns the total lifetime cost_usd a single API key has
// accrued for one model, summed on the fly from usage_events. Used by the
// policy service to enforce per-model max-budget caps on group-attached keys
// (mirrors the per-user GetModelSpend aggregation pattern). A missing model
// row returns 0, not an error. Failed requests still carry cost_usd when the
// upstream billed us, so they count toward the cap.
func (s *UsageStore) ModelSpendForKey(ctx context.Context, apiKeyID, model string) (float64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	if apiKeyID == "" || model == "" {
		return 0, fmt.Errorf("postgres store: ModelSpendForKey requires api_key_id and model")
	}
	var total float64
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(cost_usd), 0)
		FROM %s
		WHERE api_key_id = $1 AND model = $2
	`, s.eventsTable), apiKeyID, model).Scan(&total); err != nil {
		return 0, fmt.Errorf("postgres store: select per-model spend for key: %w", err)
	}
	return total, nil
}

func (s *UsageStore) UpsertWindow(ctx context.Context, apiKeyID, windowType string, windowStart, windowEnd time.Time, deltaReq, deltaTokens int64, deltaCost float64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if apiKeyID == "" || windowType == "" {
		return fmt.Errorf("postgres store: upsert window requires api_key_id and window_type")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (api_key_id, window_type, window_start, window_end,
			request_count, total_tokens, cost_usd)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (api_key_id, window_type, window_start) DO UPDATE SET
			request_count = %s.request_count + EXCLUDED.request_count,
			total_tokens  = %s.total_tokens + EXCLUDED.total_tokens,
			cost_usd      = %s.cost_usd + EXCLUDED.cost_usd
	`, s.windowsTable, s.windowsTable, s.windowsTable, s.windowsTable),
		apiKeyID, windowType, windowStart, windowEnd, deltaReq, deltaTokens, deltaCost)
	if err != nil {
		return fmt.Errorf("postgres store: upsert usage window: %w", err)
	}
	return nil
}

// GetWindow returns the budget window for a key covering the supplied
// reference time. The caller picks windowStart/windowEnd; this method just
// reflects the persisted counter values (zero row → zero counters).
func (s *UsageStore) GetWindow(ctx context.Context, apiKeyID, windowType string, windowStart time.Time) (UsageWindow, error) {
	if s == nil || s.db == nil {
		return UsageWindow{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	var w UsageWindow
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT api_key_id, window_type, window_start, window_end,
			COALESCE(request_count, 0), COALESCE(total_tokens, 0),
			COALESCE(cost_usd, 0)
		FROM %s
		WHERE api_key_id = $1 AND window_type = $2 AND window_start = $3
	`, s.windowsTable), apiKeyID, windowType, windowStart)
	w.WindowType = windowType
	err := row.Scan(&w.APIKeyID, &w.WindowType, &w.WindowStart, &w.WindowEnd,
		&w.RequestCount, &w.TotalTokens, &w.CostUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageWindow{
			APIKeyID:    apiKeyID,
			WindowType:  windowType,
			WindowStart: windowStart,
		}, nil
	}
	if err != nil {
		return UsageWindow{}, fmt.Errorf("postgres store: get usage window: %w", err)
	}
	return w, nil
}

// ListWindows returns all budget windows for an API key (any type) ordered by
// window_start desc, capped at limit (default 100 when limit<=0).
func (s *UsageStore) ListWindows(ctx context.Context, apiKeyID string, limit int) ([]UsageWindow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT api_key_id, window_type, window_start, window_end,
			request_count, total_tokens, cost_usd
		FROM %s
		WHERE api_key_id = $1
		ORDER BY window_start DESC
		LIMIT $2
	`, s.windowsTable), apiKeyID, limit)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list usage windows: %w", err)
	}
	defer rows.Close()
	out := make([]UsageWindow, 0, limit)
	for rows.Next() {
		var w UsageWindow
		if err = rows.Scan(&w.APIKeyID, &w.WindowType, &w.WindowStart, &w.WindowEnd,
			&w.RequestCount, &w.TotalTokens, &w.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan usage window: %w", err)
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// SelectAggregate runs a grouped sum query over usage_events. The grouping
// dimension is derived from filter.GroupBy. The result rows always expose
// (bucket, key_alias, request_count, failed_count, token sums, cost): the
// alias column is null when GROUP BY is not api_key_id (one bucket may
// contain many keys). The raw api_key_principal is sealed at rest and never
// projected; operators should resolve the human label via api_keys.key_alias.
func (s *UsageStore) SelectAggregate(ctx context.Context, filter UsageFilter) ([]UsageAggregate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	// Request-scoped reads are unique one-row lookups: bypass the cache.
	// RequestID is how the LiteLLM compat handlers drill into a single event.
	if s.cache == nil || UsageFilterIncludeRequestID(filter) {
		return s.selectAggregateMiss(ctx, filter)
	}
	return s.cache.getAggregate(ctx, filter, func() ([]UsageAggregate, error) {
		return s.selectAggregateMiss(ctx, filter)
	})
}

// selectAggregateMiss is the uncached SQL+scan path shared by SelectAggregate.
// It is the loader for the aggregate cache.
func (s *UsageStore) selectAggregateMiss(ctx context.Context, filter UsageFilter) ([]UsageAggregate, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	// Fast path: when the aggregate can be answered exactly from the daily
	// rollup table (a day-aligned group-by-user query with no non-rollup
	// dimension filters), read it from usage_stat_day instead of scanning
	// every usage_events row in the window. This is the query behind LiteLLM's
	// /litellm/spend/users, which many users poll.
	//
	// NOTE on freshness: a day-aligned result reflects the last rollup fold, so
	// "today" is at most ~24h stale (folded on the daily cadence + startup
	// backfill). The 15s aggregate cache hides DB load but does not change this.
	// This is an accepted tradeoff — see docs/operations/postgres-sizing.md.
	if rows, used, err := s.tryRollupAggregateUser(ctx, filter); err != nil {
		return nil, err
	} else if used {
		return rows, nil
	}
	groupExpr, groupCol, err := aggregateGroupClause(filter.GroupBy)
	if err != nil {
		return nil, err
	}
	// Build the SELECT clause. We always project two leading columns for
	// Scan: bucket (TEXT) and principal (TEXT, nullable) — the latter is
	// populated from the api_keys.key_alias column via a LEFT JOIN so the
	// sealed api_key_principal never leaves the database. Both columns must
	// appear in GROUP BY when grouping is active.
	var b strings.Builder
	b.WriteString("SELECT ")
	if groupExpr == "" {
		b.WriteString("'total' AS bucket, NULL::text AS principal")
	} else {
		b.WriteString(groupExpr)
		b.WriteString(" AS bucket, MAX(COALESCE(NULLIF(k.key_alias, ''), k.name)) AS principal")
	}
	b.WriteString(`
		, COUNT(*) AS request_count,
		0 AS failed_count,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
		COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(cost_usd), 0) AS cost_usd
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e LEFT JOIN ")
	b.WriteString(s.apiKeysTable)
	b.WriteString(" k ON k.id = e.api_key_id WHERE 1=1")
	args := []any{}
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(" AND e.api_key_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.UserID != "" {
		args = append(args, filter.UserID)
		b.WriteString(" AND e.user_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Provider != "" {
		args = append(args, filter.Provider)
		b.WriteString(" AND e.provider = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Model != "" {
		args = append(args, filter.Model)
		b.WriteString(" AND e.model = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.RouterID != "" {
		args = append(args, filter.RouterID)
		b.WriteString(" AND e.router_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.RequestID != "" {
		args = append(args, filter.RequestID)
		b.WriteString(" AND e.request_id = $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(" AND e.requested_at >= $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(" AND e.requested_at < $")
		b.WriteString(itoa(len(args)))
	}
	if groupCol != "" {
		b.WriteString(" GROUP BY ")
		b.WriteString(groupCol)
		b.WriteString(" ORDER BY bucket")
	}
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		b.WriteString(" LIMIT $")
		b.WriteString(itoa(len(args)))
	}

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select usage aggregate: %w", err)
	}
	defer rows.Close()
	out := make([]UsageAggregate, 0, 16)
	for rows.Next() {
		var (
			a         UsageAggregate
			principal sql.NullString
		)
		if err = rows.Scan(&a.Bucket, &principal, &a.RequestCount, &a.FailedCount,
			&a.InputTokens, &a.OutputTokens, &a.ReasoningTokens, &a.CachedTokens,
			&a.TotalTokens, &a.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan usage aggregate: %w", err)
		}
		if principal.Valid {
			a.APIKeyPrincipal = principal.String
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// rollupDayExpr reports whether t lands exactly on a UTC calendar-day boundary
// (00:00:00 UTC). Midnights truncate to themselves; any other instant does not.
func rollupDayExpr(t time.Time) bool {
	return t.UTC().Truncate(24*time.Hour) == t.UTC()
}

// rollupGroupByUserSQL is the rollup-backed GROUP BY user_id aggregate over a
// day-aligned window. It reads usage_stat_day (one row per (day,user,key,model,
// provider,source) bucket) and rolls each per-user row up into a UsageAggregate.
//
// Locator columns match the on-the-fly path so callers see identical shapes:
// bucket carries the user id, principal is NULL (a user spans many api_keys, so
// the api_keys LEFT JOIN is not applicable here). The rollup does not track
// reasoning_tokens or cached_tokens, so those project as 0; and failed_count,
// like the on-the-fly scan, deliberately projects 0 (the rollup's fail_count
// column holds real counts, but matching the existing on-the-fly behavior keeps
// the two paths consistent). So on the group-by-user fast path
// reasoning_tokens, cached_tokens, and failed_count are all 0.
//
// stat_day is a DATE pinned to UTC, so the window is inclusive on both ends
// (>= From_date AND <= To_date). Parameters are bound as explicit YYYY-MM-DD
// date strings cast to ::date — never as time.Time — so the comparison is
// timezone-independent and matches the UTC-pinned stat_day regardless of the
// session TIMEZONE.
const rollupGroupByUserSQL = `
SELECT r.user_id AS bucket,
       NULL::text AS principal,
       SUM(r.request_count) AS request_count,
       0                    AS failed_count,
       SUM(r.input_tokens)   AS input_tokens,
       SUM(r.output_tokens)  AS output_tokens,
       0                     AS reasoning_tokens,
       0                     AS cached_tokens,
       SUM(r.tot_tokens)     AS total_tokens,
       SUM(r.cost_usd)       AS cost_usd
FROM %s r
WHERE 1=1%s%s
  AND r.user_id <> ''
GROUP BY r.user_id
ORDER BY bucket%s`

// tryRollupAggregateUser answers a group-by-user aggregate from the daily rollup
// when the filter's window is day-aligned and carries no non-rollup dimension
// filters. It returns usedRollup=false (falling back to the usage_events scan)
// whenever the fast path cannot faithfully reproduce the query's result.
//
// Results from this fast path reflect the last rollup fold (whole-day-aligned),
// so "today" may be up to ~24h stale and is cached for the 15s aggregate-cache
// TTL. Accepted tradeoff — see docs/operations/postgres-sizing.md.
func (s *UsageStore) tryRollupAggregateUser(ctx context.Context, filter UsageFilter) ([]UsageAggregate, bool, error) {
	if s.rollupTable == "" {
		return nil, false, nil
	}
	// Only the group-by-user aggregation is fast-pathed; all other GroupBy
	// dimensions (api_key_id, model, provider, day, hour, "") keep the
	// on-the-fly usage_events path unchanged.
	if strings.TrimSpace(strings.ToLower(filter.GroupBy)) != "user_id" {
		return nil, false, nil
	}
	// The rollup buckets by all six dimensions, so any non-rollup dimension
	// filter needs a filtered scan — fall back. router_id is not tracked by the
	// rollup either, so a router-filtered query must scan usage_events directly.
	if filter.UserID != "" || filter.APIKeyID != "" || filter.Model != "" ||
		filter.Provider != "" || filter.RequestID != "" || filter.RouterID != "" {
		return nil, false, nil
	}
	// Day alignment: both bounds, when set, must be UTC calendar-day midnights.
	// A partial day (non-midnight From or exclusive To) cannot be answered from
	// whole-day rollup buckets, so it falls back for partial-day accuracy.
	if !filter.From.IsZero() && !rollupDayExpr(filter.From) {
		return nil, false, nil
	}
	if !filter.To.IsZero() && !rollupDayExpr(filter.To) {
		return nil, false, nil
	}

	from := ""
	if !filter.From.IsZero() {
		from = "\n  AND r.stat_day >= '" + filter.From.UTC().Format("2006-01-02") + "'"
	}
	to := ""
	if !filter.To.IsZero() {
		to = "\n  AND r.stat_day <= '" + filter.To.UTC().Format("2006-01-02") + "'"
	}
	limit := ""
	args := []any{}
	if filter.Limit > 0 {
		limit = " LIMIT $1"
		args = append(args, filter.Limit)
	}
	query := fmt.Sprintf(rollupGroupByUserSQL, s.rollupTable, from, to, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("postgres store: select rollup aggregate by user: %w", err)
	}
	defer rows.Close()
	out := make([]UsageAggregate, 0, 16)
	for rows.Next() {
		var (
			a         UsageAggregate
			principal sql.NullString
		)
		if err = rows.Scan(&a.Bucket, &principal, &a.RequestCount, &a.FailedCount,
			&a.InputTokens, &a.OutputTokens, &a.ReasoningTokens, &a.CachedTokens,
			&a.TotalTokens, &a.CostUSD); err != nil {
			return nil, false, fmt.Errorf("postgres store: scan rollup aggregate by user: %w", err)
		}
		if principal.Valid {
			a.APIKeyPrincipal = principal.String
		}
		out = append(out, a)
	}
	if err = rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres store: iterate rollup aggregate by user: %w", err)
	}
	return out, true, nil
}

// Returns:
//   - groupExpr: the SELECT projection that becomes the bucket (already
//     contains any function call like date_trunc).
//   - groupCol: the GROUP BY clause body (a single column reference or
//     function expression). Empty string means "no grouping, totals only".
//
// The split is needed because PostgreSQL requires GROUP BY to reference the
// underlying column/function rather than the SELECT alias when the alias
// points at a function expression in some versions.
func aggregateGroupClause(groupBy string) (groupExpr, groupCol string, err error) {
	switch strings.ToLower(strings.TrimSpace(groupBy)) {
	case "", "total":
		return "", "", nil
	case "api_key_id", "apikey", "key":
		return "e.api_key_id", "e.api_key_id", nil
	case "model":
		return "e.model", "e.model", nil
	case "provider":
		return "e.provider", "e.provider", nil
	case "user_id", "user":
		return "e.user_id", "e.user_id", nil
	case "day":
		return "date_trunc('day', e.requested_at)", "date_trunc('day', e.requested_at)", nil
	case "hour":
		return "date_trunc('hour', e.requested_at)", "date_trunc('hour', e.requested_at)", nil
	default:
		return "", "", fmt.Errorf("unsupported group_by: %q", groupBy)
	}
}

// GetPricing returns the pricing row for model id. Missing rows return a
// zero-value Pricing (treated as no-cost) without error.
func (s *UsageStore) GetPricing(ctx context.Context, id string) (Pricing, error) {
	if s == nil || s.db == nil {
		return Pricing{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	var p Pricing
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, input_per_1m_usd, output_per_1m_usd,
			cached_input_per_1m_usd, cached_read_per_1m_usd, reasoning_per_1m_usd
		FROM %s WHERE id = $1
	`, s.pricingTable), id)
	p.ID = id
	err := row.Scan(&p.ID, &p.InputPer1M, &p.OutputPer1M, &p.CachedInputPer1M, &p.CachedReadPer1M, &p.ReasoningPer1M)
	if errors.Is(err, sql.ErrNoRows) {
		return Pricing{ID: id}, nil
	}
	if err != nil {
		return Pricing{}, fmt.Errorf("postgres store: get pricing: %w", err)
	}
	return p, nil
}

// HasRates reports whether the pricing row carries at least one non-zero
// rate. GetPricing returns an all-zero Pricing for both a missing row and an
// explicitly all-zero row, so this is the discriminator callers use to decide
// whether to fall back to an alias (see ResolvePricing).
func (p Pricing) HasRates() bool {
	return p.InputPer1M > 0 ||
		p.OutputPer1M > 0 ||
		p.CachedInputPer1M > 0 ||
		p.CachedReadPer1M > 0 ||
		p.ReasoningPer1M > 0
}

// ResolvePricing looks up the pricing row for model, falling back to alias
// when the model's row has no usable rate. Pricing rows are keyed by the
// client-facing/alias model id (the models_catalog id that /v1/models lists),
// but usage records carry the resolved upstream model as Model (e.g.
// "glm-5.2-flex") and the client-requested name as Alias (e.g. "glm-5.2").
// Looking up the resolved model alone misses when the row was authored under
// the alias, silently yielding cost=0. The alias fallback closes that gap.
//
// Semantics:
//   - model's row has a rate → returned as-is.
//   - model's row is all-zero (missing or free) and alias differs → alias's
//     row is tried; if it has a rate it wins, otherwise the original zero row
//     is returned (unchanged "missing pricing ⇒ 0" behavior).
//
// Safe to call on a nil store (returns zero Pricing, no error).
func (s *UsageStore) ResolvePricing(ctx context.Context, model, alias string) (Pricing, error) {
	if s == nil || s.db == nil {
		return Pricing{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	p, err := s.GetPricing(ctx, model)
	if err != nil {
		return Pricing{}, err
	}
	if p.HasRates() || alias == "" || alias == model {
		return p, nil
	}
	if pa, errAlias := s.GetPricing(ctx, alias); errAlias == nil && pa.HasRates() {
		return pa, nil
	}
	return p, nil
}

// UpsertPricing inserts or replaces the pricing row for the given model id.
// Used by the model_pricing management endpoint.
func (s *UsageStore) UpsertPricing(ctx context.Context, p Pricing) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if p.ID == "" {
		return fmt.Errorf("postgres store: upsert pricing requires id")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (id, input_per_1m_usd, output_per_1m_usd,
			cached_input_per_1m_usd, cached_read_per_1m_usd, reasoning_per_1m_usd, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, NOW())
		ON CONFLICT (id) DO UPDATE SET
			input_per_1m_usd = EXCLUDED.input_per_1m_usd,
			output_per_1m_usd = EXCLUDED.output_per_1m_usd,
			cached_input_per_1m_usd = EXCLUDED.cached_input_per_1m_usd,
			cached_read_per_1m_usd = EXCLUDED.cached_read_per_1m_usd,
			reasoning_per_1m_usd = EXCLUDED.reasoning_per_1m_usd,
			updated_at = NOW()
	`, s.pricingTable), p.ID, p.InputPer1M, p.OutputPer1M, p.CachedInputPer1M, p.CachedReadPer1M, p.ReasoningPer1M)
	if err != nil {
		return fmt.Errorf("postgres store: upsert pricing: %w", err)
	}
	return nil
}

// ComputeCost derives the dollar cost of a single usage event given the
// applicable Pricing. Token counts are expressed per-1M units, matching the
// pricing table columns.
//
// Semantics (provider parsers normalize to these before persistence — see
// internal/runtime/executor/helps/usage_helpers.go):
//   - input:           billable non-cached prompt tokens. For OpenAI/Gemini
//     the provider folds cached tokens into prompt_tokens; the parser
//     subtracts them so input is the freshly-evaluated portion only.
//   - output:          completion tokens generated by the upstream.
//   - reasoning:       reasoning/thinking tokens (Claude extended thinking, OpenAI o1).
//   - cached:          cache-creation (write) tokens — providers bill these as
//     an input-token surcharge for storing the prompt.
//   - cacheRead:       cache-read tokens — providers bill these at a discount
//     because the prompt was served from cache rather than
//     re-evaluated. Anthropic distinguishes the two; OpenAI's
//     "cached_tokens" is conventionally cache-read.
func ComputeCost(p Pricing, input, output, reasoning, cached, cacheRead int64) float64 {
	perM := func(value float64, tokens int64) float64 {
		if tokens <= 0 || value <= 0 {
			return 0
		}
		return float64(tokens) * value / 1_000_000
	}
	return perM(p.InputPer1M, input) +
		perM(p.OutputPer1M, output) +
		perM(p.ReasoningPer1M, reasoning) +
		perM(p.CachedInputPer1M, cached) +
		perM(p.CachedReadPer1M, cacheRead)
}

// CostBreakdown is the per-segment dollar attribution emitted alongside a
// UsageEventRow. Each segment is the cost of one token kind evaluated against
// the applicable Pricing row, so the sum of all segments equals the event's
// total cost_usd (modulo floating-point rounding). A nil/missing Pricing row
// leaves every segment at 0 — the same behavior as ComputeCost.
type CostBreakdown struct {
	Input         float64 `json:"input"`
	Output        float64 `json:"output"`
	Reasoning     float64 `json:"reasoning"`
	CachedRead    float64 `json:"cached_read"`
	CacheCreation float64 `json:"cache_creation"`
}

// SegmentCosts returns each segment's dollar cost independently so the
// dashboard can render a per-segment breakdown bar. The mapping mirrors
// ComputeCost's billing model:
//   - input_tokens        × InputPer1M
//   - output_tokens       × OutputPer1M
//   - reasoning_tokens    × ReasoningPer1M
//   - cache_creation_tokens × CachedInputPer1M (cache write surcharge)
//   - cached_tokens       × CachedReadPer1M    (cache read discount)
//
// Provider parsers normalize token counts before persistence so the segment
// sum reconciles with both total_tokens (Σ segments == total_tokens) and
// cost_usd (Σ segments == ComputeCost). See internal/runtime/executor/helps/
// usage_helpers.go.
func SegmentCosts(p Pricing, input, output, reasoning, cachedRead, cacheCreation int64) CostBreakdown {
	perM := func(value float64, tokens int64) float64 {
		if tokens <= 0 || value <= 0 {
			return 0
		}
		return float64(tokens) * value / 1_000_000
	}
	return CostBreakdown{
		Input:         perM(p.InputPer1M, input),
		Output:        perM(p.OutputPer1M, output),
		Reasoning:     perM(p.ReasoningPer1M, reasoning),
		CachedRead:    perM(p.CachedReadPer1M, cachedRead),
		CacheCreation: perM(p.CachedInputPer1M, cacheCreation),
	}
}

// Sum returns the total dollar cost of all segments. Equal to ComputeCost for
// the same inputs (up to floating-point rounding).
func (b CostBreakdown) Sum() float64 {
	return b.Input + b.Output + b.Reasoning + b.CachedRead + b.CacheCreation
}

// ListPricing returns all model pricing rows (used for cache warming + management UI).
func (s *UsageStore) ListPricing(ctx context.Context) ([]Pricing, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, input_per_1m_usd, output_per_1m_usd,
			cached_input_per_1m_usd, cached_read_per_1m_usd, reasoning_per_1m_usd
		FROM %s ORDER BY id
	`, s.pricingTable))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list pricing: %w", err)
	}
	defer rows.Close()
	out := make([]Pricing, 0, 32)
	for rows.Next() {
		var p Pricing
		if err = rows.Scan(&p.ID, &p.InputPer1M, &p.OutputPer1M, &p.CachedInputPer1M, &p.CachedReadPer1M, &p.ReasoningPer1M); err != nil {
			return nil, fmt.Errorf("postgres store: scan pricing: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeleteEventsBefore purges usage_events older than the cutoff timestamp. Use
// for retention cleanup (optional,configured by PGSTORE_USAGE_RETENTION_DAYS).
func (s *UsageStore) DeleteEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE requested_at < $1`, s.eventsTable,
	), cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres store: delete old usage events: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres store: usage delete rows affected: %w", err)
	}
	return n, nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func nullableJSONB(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// providerMetadataJSONB marshals provider metadata for the NOT NULL jsonb
// column; an absent map binds the column default shape ('{}') instead of nil.
func providerMetadataJSONB(md map[string]any) any {
	if len(md) == 0 {
		return []byte("{}")
	}
	raw, err := json.Marshal(md)
	if err != nil {
		log.WithError(err).Warn("postgres store: marshal provider metadata failed; persisting empty object")
		return []byte("{}")
	}
	return raw
}

// UsageTimeSeriesPoint is one bucket in a time-series query.
type UsageTimeSeriesPoint struct {
	Bucket          string  `json:"bucket"`    // RFC3339 timestamp string (UTC)
	BucketTimestamp int64   `json:"bucket_ts"` // epoch seconds, handy for client JS new Date()
	RequestCount    int64   `json:"request_count"`
	FailedCount     int64   `json:"failed_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	CachedTokens    int64   `json:"cached_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
}

// TopEntry is one row in a "top N models / keys / providers" leaderboard.
type TopEntry struct {
	Key             string  `json:"key"` // model id / principal / provider / api_key_id
	RequestCount    int64   `json:"request_count"`
	FailedCount     int64   `json:"failed_count"`
	InputTokens     int64   `json:"input_tokens"`
	OutputTokens    int64   `json:"output_tokens"`
	ReasoningTokens int64   `json:"reasoning_tokens"`
	TotalTokens     int64   `json:"total_tokens"`
	CostUSD         float64 `json:"cost_usd"`
}

// SelectTimeSeries returns one bucket per interval across the requested
// window. The `interval` parameter controls bucket granularity:
//
//	"minute", "hour", "day" (default hour).
//
// Filters are applied identically to SelectAggregate. The result is always
// sorted ascending by bucket_start; buckets with zero activity are
// omitted (callers may pad client-side if they want a smooth axis).
func (s *UsageStore) SelectTimeSeries(ctx context.Context, filter UsageFilter, interval string) ([]UsageTimeSeriesPoint, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	intervalExpr, err := intervalExpr(interval)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(intervalExpr)
	b.WriteString(" AS bucket, EXTRACT(EPOCH FROM ")
	b.WriteString(intervalExpr)
	b.WriteString(`)::bigint AS bucket_ts,
		COUNT(*) AS request_count,
		0 AS failed_count,
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e")
	args := buildWhereClause(&b, filter)
	b.WriteString(" GROUP BY bucket, bucket_ts ORDER BY bucket ASC")
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		b.WriteString(" LIMIT $")
		b.WriteString(itoa(len(args)))
	}
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select usage timeseries: %w", err)
	}
	defer rows.Close()
	out := make([]UsageTimeSeriesPoint, 0, 32)
	for rows.Next() {
		var p UsageTimeSeriesPoint
		var bucket time.Time
		if err = rows.Scan(&bucket, &p.BucketTimestamp, &p.RequestCount, &p.FailedCount,
			&p.InputTokens, &p.OutputTokens, &p.ReasoningTokens, &p.CachedTokens,
			&p.TotalTokens, &p.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan timeseries row: %w", err)
		}
		p.Bucket = bucket.UTC().Format(time.RFC3339)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SelectTop returns the top-N rows ordered by a chosen metric across the
// supplied dimension. dimension must be one of: "model", "provider",
// "api_key_id", "api_key_principal" (the last resolves to key_alias). metric
// must be one of: "request_count", "total_tokens", "cost_usd" (default
// request_count).
func (s *UsageStore) SelectTop(ctx context.Context, filter UsageFilter, dimension, metric string, limit int) ([]TopEntry, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	dimCol, joinExtra, err := dimensionColumn(dimension, s.apiKeysTable, s.internalUsersTable)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 500 {
		limit = 500
	}
	metricExpr, err := metricExpr(metric)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("SELECT COALESCE(")
	b.WriteString(dimCol)
	b.WriteString(`, '') AS key,
		COUNT(*) AS request_count,
		0 AS failed_count,
		COALESCE(SUM(input_tokens), 0) AS input_tokens,
		COALESCE(SUM(output_tokens), 0) AS output_tokens,
		COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(cost_usd), 0) AS cost_usd
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e")
	if joinExtra != "" {
		b.WriteString(joinExtra)
	}
	args := buildWhereClause(&b, filter)
	b.WriteString(" GROUP BY key ORDER BY ")
	b.WriteString(metricExpr)
	b.WriteString(" DESC, key ASC")
	args = append(args, limit)
	b.WriteString(" LIMIT $")
	b.WriteString(itoa(len(args)))
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select usage top: %w", err)
	}
	defer rows.Close()
	out := make([]TopEntry, 0, limit)
	for rows.Next() {
		var t TopEntry
		if err = rows.Scan(&t.Key, &t.RequestCount, &t.FailedCount,
			&t.InputTokens, &t.OutputTokens, &t.ReasoningTokens,
			&t.TotalTokens, &t.CostUSD); err != nil {
			return nil, fmt.Errorf("postgres store: scan top row: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SelectTotals returns roll-up counts across the same filtered window. Useful
// for KPI cards (total requests / tokens / cost / failure rate).
func (s *UsageStore) SelectTotals(ctx context.Context, filter UsageFilter) (UsageAggregate, error) {
	if s == nil || s.db == nil {
		return UsageAggregate{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	// Request-scoped reads are unique one-row lookups: bypass the cache.
	if s.cache == nil || UsageFilterIncludeRequestID(filter) {
		return s.selectTotalsMiss(ctx, filter)
	}
	return s.cache.getTotals(ctx, filter, func() (UsageAggregate, error) {
		return s.selectTotalsMiss(ctx, filter)
	})
}

// selectTotalsMiss is the uncached SQL+scan path shared by SelectTotals. It is
// the loader for the totals cache.
func (s *UsageStore) selectTotalsMiss(ctx context.Context, filter UsageFilter) (UsageAggregate, error) {
	var b strings.Builder
	b.WriteString(`SELECT 'total', NULL::text,
		COUNT(*),
		0,
		COALESCE(SUM(input_tokens), 0), COALESCE(SUM(output_tokens), 0),
		COALESCE(SUM(reasoning_tokens), 0), COALESCE(SUM(cached_tokens), 0),
		COALESCE(SUM(total_tokens), 0), COALESCE(SUM(cost_usd), 0)
	FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e")
	args := buildWhereClause(&b, filter)
	row := s.db.QueryRowContext(ctx, b.String(), args...)
	var (
		a         UsageAggregate
		principal sql.NullString
	)
	if err := row.Scan(&a.Bucket, &principal, &a.RequestCount, &a.FailedCount,
		&a.InputTokens, &a.OutputTokens, &a.ReasoningTokens, &a.CachedTokens,
		&a.TotalTokens, &a.CostUSD); err != nil {
		return UsageAggregate{}, fmt.Errorf("postgres store: select usage totals: %w", err)
	}
	if principal.Valid {
		a.APIKeyPrincipal = principal.String
	}
	return a, nil
}

// UsageEventRow is the dashboard-friendly projection of a single
// usage_events row. It mirrors UsageEvent but replaces the sealed
// api_key_principal with the non-secret KeyAlias (resolved via a LEFT JOIN
// on api_keys). The raw principal is never returned to API callers.
type UsageEventRow struct {
	ID        int64  `json:"id"`
	RequestID string `json:"request_id,omitempty"`
	APIKeyID  string `json:"api_key_id,omitempty"`
	KeyAlias  string `json:"key_alias,omitempty"`
	Provider  string `json:"provider"`
	// OfficialProvider is the official_provider label resolved from the models
	// catalog for this row's (provider key, model); populated best-effort by
	// the management layer's FillOfficialProvider. Empty when no catalog row
	// matches (file-only deployments, unknown model) — callers should fall back
	// to Provider. Never persisted in usage_events itself.
	OfficialProvider string `json:"official_provider,omitempty"`
	ExecutorType     string `json:"executor_type,omitempty"`
	Model            string `json:"model"`
	Alias            string `json:"alias,omitempty"`
	// RouteModel is the model name exactly as the client requested it (before
	// alias/upstream resolution), persisted at flush time. Lets the dashboard
	// diagnose misrouting by comparing it against the resolved Model.
	RouteModel string `json:"route_model,omitempty"`
	// ServedModel is the model the upstream response reported serving. It
	// differs from Model when the provider silently substituted a different
	// model; empty when the upstream did not report one.
	ServedModel         string `json:"served_model,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	ClientIP            string `json:"client_ip,omitempty"`
	ForwardedFor        string `json:"forwarded_for,omitempty"`
	AuthType            string `json:"auth_type,omitempty"`
	Source              string `json:"source,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
	ServiceTier         string `json:"service_tier,omitempty"`
	ResponseServiceTier string `json:"response_service_tier,omitempty"`
	UserID              string `json:"user_id,omitempty"`
	// Tier stores the Auto Router complexity tier chosen for the request.
	// Empty for non-routed requests.
	Tier string `json:"tier,omitempty"`
	// RouterID stores the Auto Router id that owned the tier decision. Empty
	// for non-routed requests.
	RouterID string `json:"router_id,omitempty"`
	// ScoredTier is the tier the scorer derived before any keyword override or
	// mapping fallback. Empty for non-routed requests.
	ScoredTier string `json:"scored_tier,omitempty"`
	// EffectiveTier mirrors Tier but is persisted as its own column.
	EffectiveTier string `json:"effective_tier,omitempty"`
	// MappingTier is the tier whose mapping the resolver actually used.
	MappingTier string `json:"mapping_tier,omitempty"`
	// DecisionCause explains why the effective tier differs from the scored
	// tier (literal_keyword_match) or matches it (complexity_scorer).
	DecisionCause string `json:"decision_cause,omitempty"`
	// ProfileVersion identifies the Auto Router profile version used to score
	// the request. Zero when the default built-in profile was used.
	ProfileVersion int64 `json:"profile_version,omitempty"`
	// ProfileHash identifies the exact profile configuration used.
	ProfileHash         string  `json:"profile_hash,omitempty"`
	InputTokens         int64   `json:"input_tokens"`
	OutputTokens        int64   `json:"output_tokens"`
	ReasoningTokens     int64   `json:"reasoning_tokens"`
	CachedTokens        int64   `json:"cached_tokens"`
	CacheCreationTokens int64   `json:"cache_creation_tokens"`
	TotalTokens         int64   `json:"total_tokens"`
	CostUSD             float64 `json:"cost_usd"`
	// DiscountPct is the resolved model-group discount percentage (0-100) that
	// was applied to CostUSD at flush time. 0 = no discount. Populated from the
	// persisted usage_events.discount_pct column.
	DiscountPct float64 `json:"discount_pct,omitempty"`
	// OriginalCostUSD is the pre-discount cost (before the model-group
	// discount was multiplied in), persisted at flush time alongside
	// DiscountPct so the dashboard shows an authoritative "was $X" figure
	// (never re-derived on read, so immune to pricing drift). Equals CostUSD
	// when no discount was applied.
	OriginalCostUSD float64 `json:"original_cost_usd,omitempty"`
	// CostBreakdown is populated only when the caller asks for it
	// (include=cost_breakdown on the events endpoints). It attributes
	// cost_usd to each token kind using the model's pricing row. Stays nil
	// otherwise so existing API consumers see no payload change.
	CostBreakdown *CostBreakdown `json:"cost_breakdown,omitempty"`
	// AppliedPricing is the resolved pricing row (unit rates) that produced
	// CostBreakdown. Populated alongside CostBreakdown so the dashboard can
	// render a tokens × rate → cost derivation. Present-but-all-zero when no
	// pricing row matched (i.e. "no pricing configured"), mirroring
	// CostBreakdown's missing-row semantics. Stays nil when CostBreakdown is
	// not requested so the list endpoint's payload is unchanged.
	AppliedPricing *Pricing `json:"applied_pricing,omitempty"`
	LatencyMs      int64    `json:"latency_ms,omitempty"`
	TTFTMs         int64    `json:"ttft_ms,omitempty"`
	// EnergyJoules is the upstream-reported energy consumption, nil when not
	// measured (persisted as SQL NULL rather than a misleading 0).
	EnergyJoules   *float64  `json:"energy_joules,omitempty"`
	Failed         bool      `json:"failed"`
	FailStatusCode int       `json:"fail_status_code,omitempty"`
	Generate       bool      `json:"generate,omitempty"`
	RequestedAt    time.Time `json:"requested_at"`
}

// eventRowSelectColumns lists the columns projected by SelectEvents and
// GetEvent, in the order scanEventRow expects. KeyAlias is resolved via the
// api_keys LEFT JOIN. OfficialProvider is resolved via LEFT JOINs on
// models_catalog — see eventJoin for the resolution precedence. Rows that
// have no matching catalog row at all get an empty value here and are left
// to the best-effort registry resolver in the management layer.
// api_key_id is COALESCE'd to ” because it is nullable on usage_events
// (the flusher records only api_key_principal when no api_keys row can be
// resolved), and it is scanned into a plain string in scanEventRow — a raw
// NULL would fail the scan.
const eventRowSelectColumns = `
	e.id, e.request_id, COALESCE(e.api_key_id, ''),
	COALESCE(NULLIF(k.key_alias, ''), NULLIF(lk.key_alias, ''), lk.name, k.name, '') AS key_alias,
	e.provider, e.executor_type, e.model, e.alias, e.endpoint,
	e.route_model, e.served_model,
	e.client_ip, e.forwarded_for,
	e.auth_type,
	e.source, e.reasoning_effort, e.service_tier, e.response_service_tier,
	e.user_id, e.tier, e.router_id, e.scored_tier, e.effective_tier, e.mapping_tier, e.decision_cause,
	e.profile_version, e.profile_hash,
	e.input_tokens, e.output_tokens, e.reasoning_tokens,
	e.cached_tokens, e.cache_creation_tokens, e.total_tokens, e.cost_usd,
	e.discount_pct,
	e.original_cost_usd,
	e.latency_ms, e.ttft_ms, e.energy_joules, e.failed, e.fail_status_code, e.generate,
	e.requested_at,
	COALESCE(mcAlias.official_provider, mcModel.official_provider, mcCompat.official_provider, '') AS official_provider
`

// eventJoin builds the FROM clause shared by SelectEvents and GetEvent. It
// joins api_keys (for the non-secret KeyAlias) and models_catalog three times
// to resolve official_provider deterministically from the persisted catalog,
// independent of the in-memory registry connection state:
//
//  1. mcAlias — (catalog id = COALESCE(event alias, event model),
//     catalog provider ILIKE event provider). Most built-in providers (claude,
//     gemini, antigravity, ...) persist catalog rows keyed by the alias / model
//     id with provider = OwnedBy == the internal provider key, so this exact
//     match is the common case. Using COALESCE(alias, model) handles "thinking"
//     model variants (e.model = "claude-opus-4-6-thinking") whose catalog row
//     is keyed by the alias "claude-opus-4-6".
//  2. mcModel — falls back to matching the raw event model id directly (for
//     rows where the alias differs but only the model id has a catalog row).
//  3. mcCompat — strips the "openai-compatible-" prefix from the internal key
//     and matches ILIKE against the catalog provider column. OpenAI-compatible
//     catalog rows are keyed by the compat name (e.g. "SemutSSH", "opencode"),
//     never the internal "openai-compatible-*" key, so this third arm resolves
//     events for OpenAI-compatible upstreams — including ones that have since
//     been deleted (the catalog row survives key deletion, so we still surface
//     the operator-edited official_provider rather than the internal key).
//
// Provider matching is LOWER()/LOWER() so capitalisation differences between
// the internal key / compat name and the catalog row never break resolution.
// All three arms seek the (id, provider) primary key, so the join stays an
// index lookup with no row amplification.
func (s *UsageStore) eventJoin() string {
	catalog := s.modelsCatalogTable
	// Join the Manage-LiteLLM keys table (litellm_api_keys) as a fallback alias
	// source: spend-log events imported by the LiteLLM sync carry the remote
	// key hash, which may only resolve in litellm_api_keys when the runtime
	// api_keys table has not been migrated yet. The key_alias column COALESCEs
	// runtime over litellm so the runtime label wins when both exist.
	return s.eventsTable + " e LEFT JOIN " + s.apiKeysTable + " k ON k.id = e.api_key_id" +
		" LEFT JOIN " + s.litellmKeysTable + " lk ON lk.id = e.api_key_id" +
		" LEFT JOIN " + catalog + " mcAlias ON mcAlias.id = COALESCE(NULLIF(e.alias, ''), e.model) AND LOWER(mcAlias.provider) = LOWER(e.provider)" +
		" LEFT JOIN " + catalog + " mcModel ON mcModel.id = e.model AND LOWER(mcModel.provider) = LOWER(e.provider)" +
		" LEFT JOIN " + catalog + " mcCompat ON mcCompat.id = COALESCE(NULLIF(e.alias, ''), e.model) AND LOWER(mcCompat.provider) = LOWER(REPLACE(e.provider, 'openai-compatible-', ''))"
}

// scanEventRow scans one row from the column order defined above. Shared by
// SelectEvents and GetEvent so the two stay in sync. client_ip and
// forwarded_for are nullable (added via idempotent ALTER; pre-existing rows
// carry NULL), so they are scanned into sql.NullString and then resolved to
// plain strings — mirroring how route_model is handled on the errors path.
func scanEventRow(scanner interface {
	Scan(dest ...any) error
}) (UsageEventRow, error) {
	var r UsageEventRow
	var clientIP, forwardedFor, routeModel, servedModel sql.NullString
	var userID, tier, routerID, scoredTier, effectiveTier, mappingTier, decisionCause, profileHash sql.NullString
	var profileVersion sql.NullInt64
	var energyJoules sql.NullFloat64
	if err := scanner.Scan(
		&r.ID, &r.RequestID, &r.APIKeyID, &r.KeyAlias,
		&r.Provider, &r.ExecutorType, &r.Model, &r.Alias, &r.Endpoint,
		&routeModel, &servedModel,
		&clientIP, &forwardedFor,
		&r.AuthType,
		&r.Source, &r.ReasoningEffort, &r.ServiceTier, &r.ResponseServiceTier,
		&userID, &tier, &routerID, &scoredTier, &effectiveTier, &mappingTier, &decisionCause,
		&profileVersion, &profileHash,
		&r.InputTokens, &r.OutputTokens, &r.ReasoningTokens, &r.CachedTokens,
		&r.CacheCreationTokens, &r.TotalTokens, &r.CostUSD, &r.DiscountPct, &r.OriginalCostUSD, &r.LatencyMs, &r.TTFTMs,
		&energyJoules,
		&r.Failed, &r.FailStatusCode, &r.Generate, &r.RequestedAt,
		&r.OfficialProvider,
	); err != nil {
		return UsageEventRow{}, err
	}
	if routeModel.Valid {
		r.RouteModel = routeModel.String
	}
	if servedModel.Valid {
		r.ServedModel = servedModel.String
	}
	if clientIP.Valid {
		r.ClientIP = clientIP.String
	}
	if forwardedFor.Valid {
		r.ForwardedFor = forwardedFor.String
	}
	if userID.Valid {
		r.UserID = userID.String
	}
	if tier.Valid {
		r.Tier = tier.String
	}
	if routerID.Valid {
		r.RouterID = routerID.String
	}
	if scoredTier.Valid {
		r.ScoredTier = scoredTier.String
	}
	if effectiveTier.Valid {
		r.EffectiveTier = effectiveTier.String
	}
	if mappingTier.Valid {
		r.MappingTier = mappingTier.String
	}
	if decisionCause.Valid {
		r.DecisionCause = decisionCause.String
	}
	if profileVersion.Valid {
		r.ProfileVersion = profileVersion.Int64
	}
	if profileHash.Valid {
		r.ProfileHash = profileHash.String
	}
	if energyJoules.Valid {
		v := energyJoules.Float64
		r.EnergyJoules = &v
	}
	return r, nil
}

// SelectEvents returns a page of raw usage events, newest first, with the
// non-secret KeyAlias projected from the JOINed api_keys row. page is
// 1-indexed; pageSize is clamped to [1, 200]. The returned total reflects the
// same filter so the caller can render a pager.
func (s *UsageStore) SelectEvents(ctx context.Context, filter UsageFilter, page, pageSize int) ([]UsageEventRow, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	// Count first so the pager math is correct even when the page is empty.
	var b strings.Builder
	b.WriteString("SELECT COUNT(*) FROM ")
	b.WriteString(s.eventJoin())
	countArgs := buildWhereClause(&b, filter)
	var total int64
	if err := s.db.QueryRowContext(ctx, b.String(), countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count usage events: %w", err)
	}

	b.Reset()
	b.WriteString("SELECT ")
	b.WriteString(eventRowSelectColumns)
	b.WriteString(" FROM ")
	b.WriteString(s.eventJoin())
	args := buildWhereClause(&b, filter)
	b.WriteString(" ORDER BY e.requested_at DESC, e.id DESC")
	args = append(args, pageSize, (page-1)*pageSize)
	b.WriteString(" LIMIT $")
	b.WriteString(itoa(len(args) - 1))
	b.WriteString(" OFFSET $")
	b.WriteString(itoa(len(args)))

	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: select usage events: %w", err)
	}
	defer rows.Close()
	out := make([]UsageEventRow, 0, pageSize)
	for rows.Next() {
		r, err := scanEventRow(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan usage event row: %w", err)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// GetEvent returns a single usage event by its primary key. The principal
// column is replaced by KeyAlias (resolved via the api_keys LEFT JOIN).
func (s *UsageStore) GetEvent(ctx context.Context, id int64) (UsageEventRow, error) {
	if s == nil || s.db == nil {
		return UsageEventRow{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	var b strings.Builder
	b.WriteString("SELECT ")
	b.WriteString(eventRowSelectColumns)
	b.WriteString(" FROM ")
	b.WriteString(s.eventJoin())
	b.WriteString(" WHERE e.id = $1")
	row := s.db.QueryRowContext(ctx, b.String(), id)
	r, err := scanEventRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return UsageEventRow{}, ErrUsageEventNotFound
		}
		return UsageEventRow{}, fmt.Errorf("postgres store: get usage event: %w", err)
	}
	return r, nil
}

// ErrUsageEventNotFound is returned by GetEvent when no row matches the id.
var ErrUsageEventNotFound = errors.New("postgres store: usage event not found")

// FillCostBreakdown populates the CostBreakdown field on every row by looking
// up the model's pricing row and applying SegmentCosts. Missing pricing rows
// leave the breakdown at a zero-value *CostBreakdown so the caller can
// distinguish "no price set" (breakdown present, all-zero) from "breakdown
// not requested" (nil pointer). The pricing lookup is deduplicated per model
// id, so a page of 25 events for the same model costs a single GetPricing
// round-trip. Safe to call on a nil store — returns nil immediately.
//
// Pricing is resolved via ResolvePricing(r.Model, r.Alias) so events whose row
// is keyed by the client-facing alias (not the resolved upstream model) still
// get a non-zero breakdown. The cache key is the (model, alias) pair so two
// events sharing a resolved model but differing in alias can't reuse a wrong
// alias's resolved row.
func (s *UsageStore) FillCostBreakdown(ctx context.Context, rows []UsageEventRow) error {
	if s == nil || s.db == nil || len(rows) == 0 {
		return nil
	}
	cache := make(map[string]*Pricing, len(rows))
	for i := range rows {
		r := &rows[i]
		key := r.Model + "\x00" + r.Alias
		p, ok := cache[key]
		if !ok {
			resolved, err := s.ResolvePricing(ctx, r.Model, r.Alias)
			if err != nil {
				return fmt.Errorf("postgres store: fill cost breakdown: %w", err)
			}
			p = &resolved
			cache[key] = p
		}
		b := SegmentCosts(*p, r.InputTokens, r.OutputTokens, r.ReasoningTokens, r.CachedTokens, r.CacheCreationTokens)
		r.CostBreakdown = &b
		// AppliedPricing mirrors CostBreakdown's present-but-all-zero semantics
		// for a missing pricing row so the dashboard can show "no pricing
		// configured" rather than conflating it with "free".
		// Note: OriginalCostUSD is read straight from the persisted
		// usage_events.original_cost_usd column (stamped at flush time) — no
		// re-derivation here, so it is immune to the pricing row drifting
		// between flush and read.
		r.AppliedPricing = p
	}
	return nil
}

// FilterOptions is the materialized set of distinct filter values for a usage
// window. The dashboard renders these as dropdown options so operators never
// have to type free-text filter values (which would be impossible against the
// sealed api_key_principal column anyway).
type FilterOptions struct {
	APIKeys   []APIKeyOption       `json:"api_keys"`
	Providers []string             `json:"providers"`
	Models    []string             `json:"models"`
	Users     []InternalUserOption `json:"users"`
}

// APIKeyOption pairs an api_key_id with its non-secret alias/name so the
// dashboard's API Key dropdown can show the human label while binding the
// id back into the filter.
type APIKeyOption struct {
	ID    string `json:"id"`
	Alias string `json:"alias"`
}

// InternalUserOption pairs an internal user id with its non-secret
// alias/email so the dashboard's Internal User dropdown can show the human
// label while binding the id back into the filter.
type InternalUserOption struct {
	ID    string `json:"id"`
	Alias string `json:"alias"`
}

// SelectFilterOptions returns the distinct api_keys / providers / models
// observed in usage_events for the supplied filter window. The api_keys list
// is sourced from a JOIN so it surfaces only keys that actually have usage in
// the window (rather than the full catalog).
func (s *UsageStore) SelectFilterOptions(ctx context.Context, filter UsageFilter) (FilterOptions, error) {
	if s == nil || s.db == nil {
		return FilterOptions{}, fmt.Errorf("postgres store: usage store not initialized")
	}
	out := FilterOptions{}

	// Distinct API keys contributing events in the window, with alias/name.
	var b strings.Builder
	b.WriteString(`SELECT DISTINCT e.api_key_id, COALESCE(NULLIF(k.key_alias, ''), k.name, '') AS alias
		FROM `)
	b.WriteString(s.eventJoin())
	args := buildWhereClause(&b, filter)
	b.WriteString(" AND e.api_key_id IS NOT NULL AND e.api_key_id <> '' ORDER BY alias ASC")
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return FilterOptions{}, fmt.Errorf("postgres store: select usage filter api_keys: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var opt APIKeyOption
		if err = rows.Scan(&opt.ID, &opt.Alias); err != nil {
			return FilterOptions{}, fmt.Errorf("postgres store: scan usage filter api_keys: %w", err)
		}
		out.APIKeys = append(out.APIKeys, opt)
	}
	if err = rows.Err(); err != nil {
		return FilterOptions{}, err
	}

	// Distinct providers.
	b.Reset()
	b.WriteString("SELECT DISTINCT e.provider FROM ")
	b.WriteString(s.eventJoin())
	args = buildWhereClause(&b, filter)
	b.WriteString(" AND e.provider IS NOT NULL AND e.provider <> '' ORDER BY e.provider ASC")
	rows2, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return FilterOptions{}, fmt.Errorf("postgres store: select usage filter providers: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var p string
		if err = rows2.Scan(&p); err != nil {
			return FilterOptions{}, fmt.Errorf("postgres store: scan usage filter providers: %w", err)
		}
		out.Providers = append(out.Providers, p)
	}
	if err = rows2.Err(); err != nil {
		return FilterOptions{}, err
	}

	// Distinct models.
	b.Reset()
	b.WriteString("SELECT DISTINCT e.model FROM ")
	b.WriteString(s.eventJoin())
	args = buildWhereClause(&b, filter)
	b.WriteString(" AND e.model IS NOT NULL AND e.model <> '' ORDER BY e.model ASC")
	rows3, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return FilterOptions{}, fmt.Errorf("postgres store: select usage filter models: %w", err)
	}
	defer rows3.Close()
	for rows3.Next() {
		var m string
		if err = rows3.Scan(&m); err != nil {
			return FilterOptions{}, fmt.Errorf("postgres store: scan usage filter models: %w", err)
		}
		out.Models = append(out.Models, m)
	}
	if err = rows3.Err(); err != nil {
		return FilterOptions{}, err
	}

	// Distinct internal users contributing events in the window, with alias.
	b.Reset()
	b.WriteString(`SELECT DISTINCT e.user_id,
		COALESCE(NULLIF(u.user_alias, ''), u.user_email, e.user_id) AS alias
		FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(" e LEFT JOIN ")
	b.WriteString(s.internalUsersTable)
	b.WriteString(" u ON u.id = e.user_id")
	args = buildWhereClause(&b, filter)
	b.WriteString(" AND e.user_id IS NOT NULL AND e.user_id <> '' ORDER BY alias ASC")
	rows4, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return FilterOptions{}, fmt.Errorf("postgres store: select usage filter users: %w", err)
	}
	defer rows4.Close()
	for rows4.Next() {
		var opt InternalUserOption
		if err = rows4.Scan(&opt.ID, &opt.Alias); err != nil {
			return FilterOptions{}, fmt.Errorf("postgres store: scan usage filter users: %w", err)
		}
		out.Users = append(out.Users, opt)
	}
	if err = rows4.Err(); err != nil {
		return FilterOptions{}, err
	}
	return out, nil
}

// buildWhereClause appends a parameterized WHERE clause to the supplied
// builder based on the parts of filter the caller populated. Returns the
// args slice (with new parameters appended) so callers can chain LIMIT. This
// is shared by the usage_events read queries and by the usage_errors queries
// (pg_usage_errors.go), which operate on a table mirroring only the columns
// common to both. Only filter fields backed by a column present in BOTH
// tables (usage_events and usage_errors) may be applied here; columns unique
// to usage_events (e.g. router_id) must be filtered by the caller's own query
// instead of being widened into this shared clause.
//
// ListAutoRouterDecisions returns a page of decision-snapshot rows for one
// router. It is the explainability companion of SelectAutoRouterTierStats /
// SelectAutoRouterModelStats: every event returned carries the persisted
// auto_router_decision JSONB plus the persisted tier fields, so an operator
// can reconstruct what the scorer saw without needing the live profile. The
// full-text prompt is intentionally NOT projected.
//
// routerID is required; filter fields are AND-ed and empty means "no
// constraint". Pagination mirrors SelectEvents: a COUNT(*) runs first so the
// pager math is correct even when the page is empty.
func (s *UsageStore) ListAutoRouterDecisions(ctx context.Context, routerID string, filter AutoRouterDecisionFilter, page, pageSize int) ([]AutoRouterDecisionRow, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: usage store not initialized")
	}
	routerID = strings.TrimSpace(routerID)
	if routerID == "" {
		return nil, 0, fmt.Errorf("postgres store: list auto router decisions: router_id is required")
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 25
	}
	if pageSize > 200 {
		pageSize = 200
	}
	where, args := buildAutoRouterDecisionWhere(routerID, filter)
	var total int64
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+s.eventsTable+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count auto router decisions: %w", err)
	}
	args = append(args, pageSize, (page-1)*pageSize)
	limitClause := fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	query := `SELECT id, requested_at, COALESCE(request_id, ''), COALESCE(api_key_id, ''),
		model, COALESCE(alias, ''), COALESCE(scored_tier, ''), COALESCE(effective_tier, ''),
		COALESCE(mapping_tier, ''), COALESCE(decision_cause, ''), COALESCE(profile_version, 0),
		COALESCE(profile_hash, ''), auto_router_decision,
		input_tokens, output_tokens, total_tokens, cost_usd
		FROM ` + s.eventsTable + where + " ORDER BY requested_at DESC, id DESC" + limitClause
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: select auto router decisions: %w", err)
	}
	defer rows.Close()
	out := make([]AutoRouterDecisionRow, 0, pageSize)
	for rows.Next() {
		var (
			r        AutoRouterDecisionRow
			snapshot []byte
			at       time.Time
		)
		if err := rows.Scan(&r.ID, &at, &r.RequestID, &r.APIKeyID, &r.Model, &r.Alias,
			&r.ScoredTier, &r.EffectiveTier, &r.MappingTier, &r.DecisionCause,
			&r.ProfileVersion, &r.ProfileHash, &snapshot,
			&r.InputTokens, &r.OutputTokens, &r.TotalTokens, &r.CostUSD); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan auto router decision: %w", err)
		}
		r.RequestedAt = at.UTC()
		if len(snapshot) > 0 {
			r.AutoRouterDecision = append(json.RawMessage(nil), snapshot...)
		}
		out = append(out, r)
	}
	return out, total, rows.Err()
}

// buildAutoRouterDecisionWhere assembles a router-scoped WHERE clause that
// AND-s every non-empty AutoRouterDecisionFilter field. The router id is
// always required and is the first positional parameter.
func buildAutoRouterDecisionWhere(routerID string, filter AutoRouterDecisionFilter) (string, []any) {
	var b strings.Builder
	b.WriteString(" WHERE router_id = $1 AND router_id IS NOT NULL AND router_id <> ''")
	args := []any{routerID}
	add := func(column, value string) {
		if value == "" {
			return
		}
		args = append(args, value)
		b.WriteString(" AND ")
		b.WriteString(column)
		b.WriteString(" = $")
		b.WriteString(itoa(len(args)))
	}
	add("api_key_id", filter.APIKeyID)
	add("scored_tier", filter.ScoredTier)
	add("effective_tier", filter.EffectiveTier)
	add("mapping_tier", filter.MappingTier)
	add("decision_cause", filter.DecisionCause)
	add("model", filter.TargetModel)
	add("profile_hash", filter.ProfileHash)
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(" AND requested_at >= $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(" AND requested_at < $")
		b.WriteString(itoa(len(args)))
	}
	return b.String(), args
}

// The Principal field is intentionally NOT applied: api_key_principal is
// sealed at rest and cannot be matched against a plaintext WHERE value.
// Operators should filter via APIKeyID (resolved in the dashboard from the
// alias-driven dropdown).
func buildWhereClause(b *strings.Builder, filter UsageFilter) []any {
	b.WriteString(" WHERE 1=1")
	args := []any{}
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(" AND e.api_key_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.UserID != "" {
		args = append(args, filter.UserID)
		b.WriteString(" AND e.user_id = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Provider != "" {
		args = append(args, filter.Provider)
		b.WriteString(" AND e.provider = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.Model != "" {
		args = append(args, filter.Model)
		b.WriteString(" AND e.model = $")
		b.WriteString(itoa(len(args)))
	}
	if filter.RequestID != "" {
		args = append(args, filter.RequestID)
		b.WriteString(" AND e.request_id = $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(" AND e.requested_at >= $")
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(" AND e.requested_at < $")
		b.WriteString(itoa(len(args)))
	}
	return args
}

// intervalExpr maps a granularity name to a PG date_trunc expression. The
// returned expression references the e alias used by all usage_events queries
// in this file.
func intervalExpr(interval string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "", "hour":
		return "date_trunc('hour', e.requested_at)", nil
	case "minute":
		return "date_trunc('minute', e.requested_at)", nil
	case "day":
		return "date_trunc('day', e.requested_at)", nil
	default:
		return "", fmt.Errorf("unsupported interval: %q (use minute|hour|day)", interval)
	}
}

// dimensionColumn returns the column reference for a top-N query dimension.
// The returned joinExtra (when non-empty) is extra LEFT JOIN clauses the
// caller must append after the `usage_events e` table. apiKeysTable and
// internalUsersTable are the fully-qualified table names the caller has
// resolved; either may be empty when its respective dimension is not used.
func dimensionColumn(dimension, apiKeysTable, internalUsersTable string) (column string, joinExtra string, err error) {
	switch strings.ToLower(strings.TrimSpace(dimension)) {
	case "model":
		return "e.model", "", nil
	case "provider":
		return "e.provider", "", nil
	case "api_key_id", "key", "apikey":
		return "e.api_key_id", "", nil
	case "principal", "api_key_principal":
		// Resolve to the human label (key_alias, falling back to name) via
		// the api_keys join. The raw api_key_principal column is sealed and
		// never projected to API callers.
		return "COALESCE(NULLIF(k.key_alias, ''), k.name)",
			" LEFT JOIN " + apiKeysTable + " k ON k.id = e.api_key_id", nil
	case "user_id", "user":
		// Resolve the internal user's alias (falling back to email/id) via the
		// internal_users join. The user_id column on usage_events is plaintext
		// and safe to GROUP BY directly.
		return "COALESCE(NULLIF(u.user_alias, ''), u.user_email, e.user_id)",
			" LEFT JOIN " + internalUsersTable + " u ON u.id = e.user_id", nil
	default:
		return "", "", fmt.Errorf("unsupported dimension: %q", dimension)
	}
}

// metricExpr returns the SQL aggregate expression to order a top-N query by.
// We return the underlying SUM(...) expression (rather than the SELECT alias)
// so PostgreSQL does not mis-resolve ORDER BY against the un-grouped base
// column (which triggers SQLSTATE 42803 when GROUP BY is active).
func metricExpr(metric string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(metric)) {
	case "", "request_count", "requests":
		return "COUNT(*)", nil
	case "total_tokens", "tokens":
		return "COALESCE(SUM(total_tokens), 0)", nil
	case "cost_usd", "cost":
		return "COALESCE(SUM(cost_usd), 0)", nil
	default:
		return "", fmt.Errorf("unsupported metric: %q", metric)
	}
}

// addUsageScopeArgs appends the api_key_id, user_id and time-range WHERE
// clauses shared by the auto-router aggregation queries; router_id is handled
// by each caller. Appends to args and grows b via the builder.
func addUsageScopeArgs(b *strings.Builder, args *[]any, filter UsageFilter) {
	if filter.APIKeyID != "" {
		*args = append(*args, filter.APIKeyID)
		b.WriteString(` AND e.api_key_id = $`)
		b.WriteString(itoa(len(*args)))
	}
	if filter.UserID != "" {
		*args = append(*args, filter.UserID)
		b.WriteString(` AND e.user_id = $`)
		b.WriteString(itoa(len(*args)))
	}
	if !filter.From.IsZero() {
		*args = append(*args, filter.From)
		b.WriteString(` AND e.requested_at >= $`)
		b.WriteString(itoa(len(*args)))
	}
	if !filter.To.IsZero() {
		*args = append(*args, filter.To)
		b.WriteString(` AND e.requested_at < $`)
		b.WriteString(itoa(len(*args)))
	}
}

// SelectAutoRouterTierStats returns per-tier request/token/cost totals for a
// router, optionally scoped by api_key_id and time range. It always returns
// the 4 canonical tiers; tiers with no requests are zero-valued. Events with a
// NULL tier (non-routed or pre-feature rows) are excluded.
func (s *UsageStore) SelectAutoRouterTierStats(ctx context.Context, filter UsageFilter) ([]AutoRouterTierStat, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if strings.TrimSpace(filter.RouterID) == "" {
		return nil, fmt.Errorf("postgres store: router_id is required for tier stats")
	}
	out := []AutoRouterTierStat{
		{Tier: "simple"}, {Tier: "medium"}, {Tier: "complex"}, {Tier: "reasoning"},
	}
	var b strings.Builder
	b.WriteString(`SELECT e.tier,
		COUNT(*), COALESCE(SUM(e.total_tokens), 0), COALESCE(SUM(e.cost_usd), 0)
		FROM `)
	b.WriteString(s.eventsTable)
	b.WriteString(` e WHERE e.tier IS NOT NULL AND e.router_id = $1`)
	args := []any{filter.RouterID}
	addUsageScopeArgs(&b, &args, filter)
	b.WriteString(` GROUP BY e.tier ORDER BY e.tier`)
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select auto-router tier stats: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tier string
		var st AutoRouterTierStat
		if err := rows.Scan(&tier, &st.RequestCount, &st.TotalTokens, &st.CostUSD); err != nil {
			return nil, err
		}
		for i := range out {
			if out[i].Tier == tier {
				st.Tier = tier
				out[i] = st
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SelectAutoRouterModelStats returns per-target-model request/token/cost totals
// for a router, optionally scoped by api_key_id and time range, ordered by cost
// descending.
func (s *UsageStore) SelectAutoRouterModelStats(ctx context.Context, filter UsageFilter) ([]AutoRouterModelStat, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if strings.TrimSpace(filter.RouterID) == "" {
		return nil, fmt.Errorf("postgres store: router_id is required for model stats")
	}
	var b strings.Builder
	b.WriteString(`SELECT e.model,
		COUNT(*), COALESCE(SUM(e.total_tokens), 0), COALESCE(SUM(e.cost_usd), 0)
		FROM `)
	b.WriteString(s.eventsTable)
	// Mirror SelectAutoRouterTierStats: restrict to events that were actually
	// routed (tier IS NOT NULL), so a non-routed event that happens to carry a
	// router_id (e.g. a future attribution change) cannot leak into the
	// per-target-model rollup.
	b.WriteString(` e WHERE e.tier IS NOT NULL AND e.router_id = $1`)
	args := []any{filter.RouterID}
	addUsageScopeArgs(&b, &args, filter)
	b.WriteString(` GROUP BY e.model ORDER BY COALESCE(SUM(e.cost_usd), 0) DESC`)
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		b.WriteString(` LIMIT $`)
		b.WriteString(itoa(len(args)))
	}
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select auto-router model stats: %w", err)
	}
	defer rows.Close()
	out := make([]AutoRouterModelStat, 0, 8)
	for rows.Next() {
		var st AutoRouterModelStat
		if err := rows.Scan(&st.Model, &st.RequestCount, &st.TotalTokens, &st.CostUSD); err != nil {
			return nil, err
		}
		if st.RequestCount > 0 {
			st.AvgCostPerReq = st.CostUSD / float64(st.RequestCount)
		}
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// autoRouterSnapshotWhere returns the common router-scoped snapshot predicate
// plus the shared scope clauses for the decision-aggregation queries.
func autoRouterSnapshotWhere(eventsTable string, filter UsageFilter, extraCondition string) (string, []any) {
	var b strings.Builder
	args := []any{filter.RouterID}
	b.WriteString(` FROM ` + eventsTable + ` e
		WHERE e.router_id = $1 AND e.router_id IS NOT NULL AND e.router_id <> ''
		AND e.auto_router_decision IS NOT NULL
		AND jsonb_typeof(e.auto_router_decision) = 'object'`)
	if extraCondition != "" {
		b.WriteString(` AND `)
		b.WriteString(extraCondition)
	}
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(` AND e.api_key_id = $`)
		b.WriteString(itoa(len(args)))
	}
	if filter.UserID != "" {
		args = append(args, filter.UserID)
		b.WriteString(` AND e.user_id = $`)
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(` AND e.requested_at >= $`)
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(` AND e.requested_at < $`)
		b.WriteString(itoa(len(args)))
	}
	return b.String(), args
}

// SelectAutoRouterDecisionStats aggregates the stored decision snapshots of one
// router into a distribution rollup: score-total histogram (20 buckets of
// 0.05), per-dimension averages, decision-cause counts, fallback-chain
// frequencies, and the effective-vs-mapping tier mismatch count. The window is
// enforced by the caller (handler defaults/caps it).
func (s *UsageStore) SelectAutoRouterDecisionStats(ctx context.Context, filter UsageFilter) (*AutoRouterDecisionStats, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if strings.TrimSpace(filter.RouterID) == "" {
		return nil, fmt.Errorf("postgres store: router_id is required for decision stats")
	}
	out := &AutoRouterDecisionStats{
		ScoreHistogram: make([]AutoRouterScoreBucket, 20),
		DimensionAverages: map[string]float64{
			"token_count":         0,
			"code_presence":       0,
			"reasoning_markers":   0,
			"technical_terms":     0,
			"simple_indicators":   0,
			"multi_step_patterns": 0,
			"question_complexity": 0,
		},
		// Every cause the router can emit is seeded so a consumer can render a
		// zero ("this never happened in range") instead of an absent key. The
		// three jev_* causes stay 0 when the classifier is off.
		CauseCounts: map[string]int64{
			"literal_keyword_match":  0,
			"complexity_scorer":      0,
			"jev_classifier":         0,
			"jev_low_confidence":     0,
			"jev_fallback_heuristic": 0,
		},
		FallbackChains: []AutoRouterChainCount{},
	}

	// Single-row aggregate: count, dimension averages, cause counts, mismatch.
	base, args := autoRouterSnapshotWhere(s.eventsTable, filter, "")
	var b strings.Builder
	b.WriteString(`SELECT
		COUNT(*),
		COALESCE(AVG((e.auto_router_decision->'score_fields'->>'token_count')::float8), 0),
		COALESCE(AVG((e.auto_router_decision->'score_fields'->>'code_presence')::float8), 0),
		COALESCE(AVG((e.auto_router_decision->'score_fields'->>'reasoning_markers')::float8), 0),
		COALESCE(AVG((e.auto_router_decision->'score_fields'->>'technical_terms')::float8), 0),
		COALESCE(AVG((e.auto_router_decision->'score_fields'->>'simple_indicators')::float8), 0),
		COALESCE(AVG((e.auto_router_decision->'score_fields'->>'multi_step_patterns')::float8), 0),
		COALESCE(AVG((e.auto_router_decision->'score_fields'->>'question_complexity')::float8), 0),
		COALESCE(SUM(CASE WHEN e.decision_cause = 'literal_keyword_match' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN e.decision_cause = 'complexity_scorer' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN e.decision_cause = 'jev_classifier' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN e.decision_cause = 'jev_low_confidence' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN e.decision_cause = 'jev_fallback_heuristic' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN e.effective_tier IS DISTINCT FROM e.mapping_tier THEN 1 ELSE 0 END), 0)`)
	b.WriteString(base)
	var (
		count        int64
		avgTokens    float64
		avgCode      float64
		avgReason    float64
		avgTech      float64
		avgSimple    float64
		avgMulti     float64
		avgQuestion  float64
		causeKw      int64
		causeScore   int64
		causeJevPick int64
		causeJevLow  int64
		causeJevFall int64
		mismatch     int64
	)
	if err := s.db.QueryRowContext(ctx, b.String(), args...).Scan(
		&count, &avgTokens, &avgCode, &avgReason, &avgTech, &avgSimple, &avgMulti, &avgQuestion,
		&causeKw, &causeScore, &causeJevPick, &causeJevLow, &causeJevFall, &mismatch,
	); err != nil {
		return nil, fmt.Errorf("postgres store: auto-router decision stats aggregate: %w", err)
	}
	out.EventCount = count
	out.DimensionAverages["token_count"] = avgTokens
	out.DimensionAverages["code_presence"] = avgCode
	out.DimensionAverages["reasoning_markers"] = avgReason
	out.DimensionAverages["technical_terms"] = avgTech
	out.DimensionAverages["simple_indicators"] = avgSimple
	out.DimensionAverages["multi_step_patterns"] = avgMulti
	out.DimensionAverages["question_complexity"] = avgQuestion
	out.CauseCounts["literal_keyword_match"] = causeKw
	out.CauseCounts["complexity_scorer"] = causeScore
	out.CauseCounts["jev_classifier"] = causeJevPick
	out.CauseCounts["jev_low_confidence"] = causeJevLow
	out.CauseCounts["jev_fallback_heuristic"] = causeJevFall
	out.MismatchCount = mismatch

	// Histogram: width_bucket over score_total. width_bucket returns 1..20 for
	// [0,1); score_total == 1.0 lands in bucket 21 (right-open) and is folded
	// into the last bucket so the histogram sums to the event count.
	hb := strings.Builder{}
	hb.WriteString(`SELECT width_bucket((e.auto_router_decision->>'score_total')::float8, 0, 1, 20), COUNT(*)
		FROM ` + s.eventsTable + ` e
		WHERE e.router_id = $1 AND e.router_id IS NOT NULL AND e.router_id <> ''
		AND e.auto_router_decision IS NOT NULL
		AND jsonb_typeof(e.auto_router_decision) = 'object'
		AND e.auto_router_decision ? 'score_total'`)
	hargs := []any{filter.RouterID}
	appendScopeTo(&hb, &hargs, filter)
	hb.WriteString(` GROUP BY 1 ORDER BY 1`)
	histRows, err := s.db.QueryContext(ctx, hb.String(), hargs...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: auto-router decision stats histogram: %w", err)
	}
	defer histRows.Close()
	for histRows.Next() {
		var bucket int
		var n int64
		if err := histRows.Scan(&bucket, &n); err != nil {
			return nil, err
		}
		if bucket >= 1 && bucket <= 20 {
			out.ScoreHistogram[bucket-1].Count = n
		} else if bucket == 21 {
			out.ScoreHistogram[19].Count += n
		}
	}
	if err := histRows.Err(); err != nil {
		return nil, err
	}
	for i := range out.ScoreHistogram {
		out.ScoreHistogram[i].Bucket = i
	}

	// Fallback chains: serialized tier list text → count, most frequent first.
	cargs := []any{filter.RouterID}
	cb := strings.Builder{}
	cb.WriteString(`SELECT (e.auto_router_decision->>'fallback_chain'), COUNT(*)
		FROM ` + s.eventsTable + ` e
		WHERE e.router_id = $1 AND e.router_id IS NOT NULL AND e.router_id <> ''
		AND e.auto_router_decision IS NOT NULL
		AND jsonb_typeof(e.auto_router_decision) = 'object'
		AND e.auto_router_decision ? 'fallback_chain'`)
	appendScopeTo(&cb, &cargs, filter)
	cb.WriteString(` GROUP BY 1 ORDER BY COUNT(*) DESC LIMIT 25`)
	chainRows, err := s.db.QueryContext(ctx, cb.String(), cargs...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: auto-router decision stats chains: %w", err)
	}
	defer chainRows.Close()
	for chainRows.Next() {
		var c AutoRouterChainCount
		if err := chainRows.Scan(&c.Chain, &c.Count); err != nil {
			return nil, err
		}
		out.FallbackChains = append(out.FallbackChains, c)
	}
	if err := chainRows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// jevTierRankSQL renders expr as a 0..3 tier rank; a value outside the four
// canonical tiers yields NULL, which propagates so any comparison against it is
// NULL and the row drops out of the CASE. Inlined rather than defined as a SQL
// function because this codebase migrates with plain DDL, not CREATE FUNCTION.
func jevTierRankSQL(expr string) string {
	return `(CASE ` + expr + ` WHEN 'simple' THEN 0 WHEN 'medium' THEN 1 WHEN 'complex' THEN 2 WHEN 'reasoning' THEN 3 END)`
}

// jevPresentSQL is the predicate selecting events the classifier was consulted
// on. `? 'jev'` is a key-existence test, so this is true even for a
// breaker-open verdict, which carries no choice or confidence.
const jevPresentSQL = `AND e.auto_router_decision ? 'jev'`

// jevAppliedSQL selects the verdicts whose tier was actually routed: only an
// accepted verdict changes anything. A rejected-low-confidence verdict still
// records what the classifier *wanted*, which is worth showing, but counting it
// as a route would report headroom that was never spent.
const jevAppliedSQL = `e.auto_router_decision->'jev'->>'verdict' = 'accepted'`

// jevAnsweredSQL selects the verdicts that carry a meaningful confidence. A
// breaker-open or error verdict never assigns one, yet the persisted JSON
// still says confidence:0 (the field has no omitempty), so a plain
// "confidence IS NOT NULL" test would average in a 0 for every call that never
// got an answer — reporting an unsure classifier where there was none.
const jevAnsweredSQL = `e.auto_router_decision->'jev'->>'verdict' IN ('accepted', 'rejected_low_confidence')`

// jevIntSQL casts a JSONB text value to int, yielding NULL rather than raising
// when the key is absent (e.g. a breaker-open verdict has no input_tokens).
func jevIntSQL(path string) string {
	return `NULLIF(` + path + `, '')::int`
}

// SelectAutoRouterJevStats rolls up the Jev AI classifier's contribution to one
// router's decisions over the same window and scope as the other analysis
// aggregations. Rows without a classifier block are excluded throughout, so
// every field describes consulted requests only.
func (s *UsageStore) SelectAutoRouterJevStats(ctx context.Context, filter UsageFilter) (*AutoRouterJevStats, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if strings.TrimSpace(filter.RouterID) == "" {
		return nil, fmt.Errorf("postgres store: router_id is required for jev stats")
	}
	out := &AutoRouterJevStats{
		ChoiceCounts: map[string]int64{
			"simple": 0, "medium": 0, "complex": 0, "reasoning": 0,
		},
		AppliedChoiceCounts: map[string]int64{
			"simple": 0, "medium": 0, "complex": 0, "reasoning": 0,
		},
		ChoiceVsScored:      map[string]int64{},
		OverroutedByTier:    map[string]int64{},
		ConfidenceHistogram: make([]AutoRouterScoreBucket, 20),
	}

	const j = `e.auto_router_decision->'jev'`
	choice := j + `->>'choice'`

	// Single-row aggregate: verdict split, confidence stats, over/under-routing.
	base, args := autoRouterSnapshotWhere(s.eventsTable, filter, "")
	var b strings.Builder
	b.WriteString(`SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN ` + j + `->>'verdict' = 'accepted' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ` + j + `->>'verdict' = 'rejected_low_confidence' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ` + j + `->>'verdict' = 'error' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ` + j + `->>'verdict' = 'breaker_open' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ` + j + `->>'cache' = 'hit' THEN 1 ELSE 0 END), 0),
		COALESCE(AVG(NULLIF(` + j + `->>'confidence', '')::float8) FILTER (WHERE ` + jevAnsweredSQL + `), 0),
		COALESCE(PERCENTILE_CONT(0.95) WITHIN GROUP (ORDER BY NULLIF(` + j + `->>'confidence', '')::float8) FILTER (WHERE ` + jevAnsweredSQL + `), 0),
		COUNT(*) FILTER (WHERE ` + jevAnsweredSQL + `),
		COALESCE(SUM(CASE WHEN ` + jevAppliedSQL + ` AND ` + jevTierRankSQL(choice) + ` > ` + jevTierRankSQL(`e.scored_tier`) + ` THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ` + jevAppliedSQL + ` AND ` + jevTierRankSQL(choice) + ` < ` + jevTierRankSQL(`e.scored_tier`) + ` THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN ` + jevAppliedSQL + ` AND ` + jevTierRankSQL(choice) + ` IS DISTINCT FROM ` + jevTierRankSQL(`e.scored_tier`) + ` THEN 1 ELSE 0 END), 0),
		COALESCE(AVG(NULLIF(` + j + `->>'latency_ms', '')::float8), 0),
		COALESCE(AVG(` + jevIntSQL(j+`->>'input_tokens'`) + `), 0),
		COALESCE(SUM(` + jevIntSQL(j+`->>'input_tokens'`) + `), 0)`)
	b.WriteString(base)
	b.WriteString(` ` + jevPresentSQL)

	var (
		consulted, accepted, lowConf, errs, breakerOpen, cacheHits int64
		avgConf, p95Conf                                           float64
		decidedCount                                               int64
		over, under, overridden                                    int64
		avgLatency, avgTokens                                      float64
		tokenSum                                                   int64
	)
	if err := s.db.QueryRowContext(ctx, b.String(), args...).Scan(
		&consulted, &accepted, &lowConf, &errs, &breakerOpen, &cacheHits,
		&avgConf, &p95Conf, &decidedCount,
		&over, &under, &overridden,
		&avgLatency, &avgTokens, &tokenSum,
	); err != nil {
		return nil, fmt.Errorf("postgres store: auto-router jev stats aggregate: %w", err)
	}
	out.Consulted = consulted
	out.Accepted = accepted
	out.LowConfidence = lowConf
	out.Errors = errs
	out.BreakerOpen = breakerOpen
	out.CacheHits = cacheHits
	out.AvgConfidence = avgConf
	out.P95Confidence = p95Conf
	out.DecidedCount = decidedCount
	out.Overrouted = over
	out.Underrouted = under
	out.OverrideCount = overridden
	out.AvgLatencyMs = avgLatency
	out.AvgInputTokens = avgTokens
	out.InputTokens = tokenSum

	// Choice distribution, seeded with the canonical tiers and split by whether
	// the verdict was actually applied. base already opens with FROM, so the
	// select list is prepended to it rather than restated.
	cb := strings.Builder{}
	cb.WriteString(`SELECT (` + choice + `) AS c, ` + jevAppliedSQL + ` AS applied, COUNT(*)`)
	cb.WriteString(base)
	cb.WriteString(` ` + jevPresentSQL + ` AND ` + choice + ` IS NOT NULL GROUP BY 1, 2`)
	cargs := append([]any(nil), args...)
	choiceRows, err := s.db.QueryContext(ctx, cb.String(), cargs...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: auto-router jev choice counts: %w", err)
	}
	defer choiceRows.Close()
	for choiceRows.Next() {
		var c string
		var applied bool
		var n int64
		if err := choiceRows.Scan(&c, &applied, &n); err != nil {
			return nil, err
		}
		out.ChoiceCounts[c] += n
		if applied {
			out.AppliedChoiceCounts[c] += n
		}
	}
	if err := choiceRows.Err(); err != nil {
		return nil, err
	}

	// Choice × scored tier, and over-routing attributed to the scored tier.
	// Only applied verdicts can over-route, so the same predicate gates both.
	vb := strings.Builder{}
	vb.WriteString(`SELECT (` + choice + `), COALESCE(e.scored_tier, ''), COUNT(*), ` +
		`COALESCE(SUM(CASE WHEN ` + jevTierRankSQL(choice) + ` > ` + jevTierRankSQL(`e.scored_tier`) + ` THEN 1 ELSE 0 END), 0)`)
	vb.WriteString(base)
	vb.WriteString(` ` + jevPresentSQL + ` AND ` + jevAppliedSQL + ` AND ` + choice + ` IS NOT NULL GROUP BY 1, 2`)
	vargs := append([]any(nil), args...)
	verdictRows, err := s.db.QueryContext(ctx, vb.String(), vargs...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: auto-router jev verdict tiers: %w", err)
	}
	defer verdictRows.Close()
	for verdictRows.Next() {
		var c, scored string
		var n, overN int64
		if err := verdictRows.Scan(&c, &scored, &n, &overN); err != nil {
			return nil, err
		}
		out.ChoiceVsScored[c+"|"+scored] = n
		if overN > 0 && scored != "" {
			out.OverroutedByTier[scored] += overN
		}
	}
	if err := verdictRows.Err(); err != nil {
		return nil, err
	}

	// Confidence histogram: 20 buckets of 0.05. width_bucket returns 1..20 for
	// [0,1) and 21 for a confidence of exactly 1.0, folded back into the last
	// bucket so the histogram sums to DecidedCount.
	hb := strings.Builder{}
	hb.WriteString(`SELECT width_bucket(NULLIF(` + j + `->>'confidence', '')::float8, 0, 1, 20), COUNT(*)`)
	hb.WriteString(base)
	hb.WriteString(` ` + jevPresentSQL + ` AND ` + jevAnsweredSQL + ` GROUP BY 1 ORDER BY 1`)
	hargs := append([]any(nil), args...)
	histRows, err := s.db.QueryContext(ctx, hb.String(), hargs...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: auto-router jev confidence histogram: %w", err)
	}
	defer histRows.Close()
	for histRows.Next() {
		var bucket int
		var n int64
		if err := histRows.Scan(&bucket, &n); err != nil {
			return nil, err
		}
		if bucket >= 1 && bucket <= 20 {
			out.ConfidenceHistogram[bucket-1].Count = n
		} else if bucket == 21 {
			out.ConfidenceHistogram[19].Count += n
		}
	}
	if err := histRows.Err(); err != nil {
		return nil, err
	}
	for i := range out.ConfidenceHistogram {
		out.ConfidenceHistogram[i].Bucket = i
	}
	return out, nil
}

// appendScopeTo appends the shared api_key_id / user_id / time-range clauses
// (same placeholders as addUsageScopeArgs) onto an arbitrary builder/args pair.
func appendScopeTo(b *strings.Builder, args *[]any, filter UsageFilter) {
	if filter.APIKeyID != "" {
		*args = append(*args, filter.APIKeyID)
		b.WriteString(` AND e.api_key_id = $`)
		b.WriteString(itoa(len(*args)))
	}
	if filter.UserID != "" {
		*args = append(*args, filter.UserID)
		b.WriteString(` AND e.user_id = $`)
		b.WriteString(itoa(len(*args)))
	}
	if !filter.From.IsZero() {
		*args = append(*args, filter.From)
		b.WriteString(` AND e.requested_at >= $`)
		b.WriteString(itoa(len(*args)))
	}
	if !filter.To.IsZero() {
		*args = append(*args, filter.To)
		b.WriteString(` AND e.requested_at < $`)
		b.WriteString(itoa(len(*args)))
	}
}

// SelectAutoRouterTierPerformance returns per tier × target-model performance
// metrics for a router: request count, p50/p95 latency (percentile_cont),
// average time-to-first-token, total cost, and error rate. Mirrors the tier
// stats restriction to actually-routed events (tier IS NOT NULL).
func (s *UsageStore) SelectAutoRouterTierPerformance(ctx context.Context, filter UsageFilter) ([]AutoRouterTierPerformance, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	if strings.TrimSpace(filter.RouterID) == "" {
		return nil, fmt.Errorf("postgres store: router_id is required for tier performance")
	}
	var b strings.Builder
	args := []any{filter.RouterID}
	b.WriteString(`SELECT e.tier, e.model,
		COUNT(*),
		COALESCE(percentile_cont(0.5) WITHIN GROUP (ORDER BY e.latency_ms), 0),
		COALESCE(percentile_cont(0.95) WITHIN GROUP (ORDER BY e.latency_ms), 0),
		COALESCE(AVG(e.ttft_ms), 0),
		COALESCE(SUM(e.cost_usd), 0),
		COALESCE(SUM(CASE WHEN e.failed THEN 1 ELSE 0 END), 0)
		FROM ` + s.eventsTable + ` e
		WHERE e.tier IS NOT NULL AND e.router_id = $1 AND e.router_id <> ''`)
	if filter.APIKeyID != "" {
		args = append(args, filter.APIKeyID)
		b.WriteString(` AND e.api_key_id = $`)
		b.WriteString(itoa(len(args)))
	}
	if filter.UserID != "" {
		args = append(args, filter.UserID)
		b.WriteString(` AND e.user_id = $`)
		b.WriteString(itoa(len(args)))
	}
	if !filter.From.IsZero() {
		args = append(args, filter.From)
		b.WriteString(` AND e.requested_at >= $`)
		b.WriteString(itoa(len(args)))
	}
	if !filter.To.IsZero() {
		args = append(args, filter.To)
		b.WriteString(` AND e.requested_at < $`)
		b.WriteString(itoa(len(args)))
	}
	b.WriteString(` GROUP BY e.tier, e.model ORDER BY e.tier, COALESCE(SUM(e.cost_usd), 0) DESC`)
	if filter.Limit > 0 {
		args = append(args, filter.Limit)
		b.WriteString(` LIMIT $`)
		b.WriteString(itoa(len(args)))
	}
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: select auto-router tier performance: %w", err)
	}
	defer rows.Close()
	out := make([]AutoRouterTierPerformance, 0, 16)
	for rows.Next() {
		var p AutoRouterTierPerformance
		if err := rows.Scan(&p.Tier, &p.Model, &p.RequestCount, &p.P50LatencyMs, &p.P95LatencyMs, &p.AvgTTFTMs, &p.CostUSD, &p.ErrorCount); err != nil {
			return nil, err
		}
		if p.RequestCount > 0 {
			p.ErrorRate = float64(p.ErrorCount) / float64(p.RequestCount)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SubstitutionRow aggregates usage_events rows where the upstream reported
// serving a different model than the one requested, over the given window.
type SubstitutionRow struct {
	Provider    string
	Model       string
	ServedModel string
	Count       int64
}

// ListSubstitutions returns substitution aggregates for events requested at or
// after since. Rows with an empty or equal served_model are excluded; the
// comparison is trimmed and case-insensitive, mirroring DetectSubstitution
// (TrimSpace + EqualFold) in sdk/cliproxy/usage. The IS NOT NULL guard covers
// legacy rows that predate the served_model column (added via ALTER TABLE, so
// pre-existing rows are NULL), and the empty-string guard covers events whose
// upstream reported no served model (the flusher persists ""). No COALESCE is
// needed on read: the WHERE clause already excludes every NULL served_model
// row. Results are ordered by descending count so the loudest substitution
// surfaces first, capped at 50 triples per call: a pathological upstream that
// varies served_model per request cannot fan out the alert sweep unbounded,
// and dropping the quietest triples beyond the cap is acceptable for a
// lookback alert.
func (s *UsageStore) ListSubstitutions(ctx context.Context, since time.Time) ([]SubstitutionRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: usage store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT provider, model, served_model, COUNT(*) AS substitution_count
		FROM %s
		WHERE requested_at >= $1
		  AND served_model IS NOT NULL
		  AND TRIM(served_model) <> ''
		  AND LOWER(TRIM(served_model)) <> LOWER(TRIM(model))
		GROUP BY provider, model, served_model
		ORDER BY substitution_count DESC
		LIMIT 50
	`, s.eventsTable), since)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list substitutions: %w", err)
	}
	defer rows.Close()
	var out []SubstitutionRow
	for rows.Next() {
		var row SubstitutionRow
		if errScan := rows.Scan(&row.Provider, &row.Model, &row.ServedModel, &row.Count); errScan != nil {
			return nil, fmt.Errorf("postgres store: scan substitution row: %w", errScan)
		}
		out = append(out, row)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("postgres store: iterate substitutions: %w", errRows)
	}
	return out, nil
}
