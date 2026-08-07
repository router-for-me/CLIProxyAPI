// Package autorouter implements the Auto Router feature: it scores an incoming
// LLM request across several dimensions and maps it to one of four complexity
// tiers (SIMPLE / MEDIUM / COMPLEX / REASONING), each of which can target a
// different concrete upstream model.
package autorouter

// Tier is the complexity classification produced by the scorer.
type Tier string

// The four complexity tiers, in ascending difficulty order.
const (
	TierSimple    Tier = "simple"
	TierMedium    Tier = "medium"
	TierComplex   Tier = "complex"
	TierReasoning Tier = "reasoning"
)

// TierOrder lists every tier in ascending difficulty. Used for fallback ordering
// (a request whose chosen tier has no available upstream falls back to the next
// lower tier).
var TierOrder = []Tier{TierSimple, TierMedium, TierComplex, TierReasoning}

// Score thresholds that decide the tier from the weighted 0-1 total.
const (
	// SimpleMax is the upper bound (exclusive) of the SIMPLE tier.
	SimpleMax = 0.15
	// MediumMax is the upper bound (exclusive) of the MEDIUM tier.
	MediumMax = 0.35
	// ComplexMax is the upper bound (exclusive) of the COMPLEX tier. Scores
	// above this fall into REASONING.
	ComplexMax = 0.60
	// ReasonerMarkerThreshold is the number of independent reasoning markers
	// that forces the REASONING tier regardless of the weighted score.
	ReasonerMarkerThreshold = 2
)

// ScoreField names every dimension the scorer computes. Each contributes a
// 0-1 sub-score to the weighted total.
type ScoreField string

const (
	FieldTokens         ScoreField = "token_count"
	FieldCode           ScoreField = "code_presence"
	FieldReasoningMark  ScoreField = "reasoning_markers"
	FieldTechnicalTerms ScoreField = "technical_terms"
	FieldSimpleIndic    ScoreField = "simple_indicators"
	FieldMultiStep      ScoreField = "multi_step_patterns"
	FieldQuestion       ScoreField = "question_complexity"
)

// DefaultWeights are the fixed per-dimension weights used to combine the seven
// sub-scores into the weighted total. They are baked into the code (KISS); only
// a router's tier-to-model mapping is editable per router. The weights need not
// sum to 1 — the scorer normalizes by the positive-weight sum so the final total
// always lies in [0,1]. Higher-weight dimensions (reasoning, code, multi-step)
// push requests toward harder tiers.
//
// SimpleIndic is special: it is a PENALTY subtracted from the weighted total (a
// request littered with greetings/trivial phrasing is easier, not harder), so
// its contribution is applied with a negative sign below.
var DefaultWeights = map[ScoreField]float64{
	FieldTokens:         0.05,
	FieldCode:           0.25,
	FieldReasoningMark:  0.30,
	FieldTechnicalTerms: 0.08,
	FieldSimpleIndic:    0.12,
	FieldMultiStep:      0.33,
	FieldQuestion:       0.20,
}

// RequestScore is the result of scoring one request.
type RequestScore struct {
	// Fields holds the 0-1 sub-score for each dimension (keyed by ScoreField).
	Fields map[ScoreField]float64 `json:"fields"`
	// Total is the weighted 0-1 total across all dimensions.
	Total float64 `json:"total"`
	// ReasoningMarkers is the raw count of independent reasoning markers found.
	ReasoningMarkers int `json:"reasoning_markers"`
	// Tier is the final classification.
	Tier Tier `json:"tier"`
}

// ProviderPriority assigns a priority weight to a single provider within a
// tier's routing. Higher numbers are preferred (served first). Only providers
// also listed in TierMapping.Providers are considered.
type ProviderPriority struct {
	Provider string `json:"provider"`
	Priority int    `json:"priority"`
}

// TierTarget is one candidate upstream model within a tier's multi-target set.
// Weight drives the "weighted" target strategy: higher numbers are picked more
// often (proportionally). A missing or non-positive weight defaults to 1 (equal
// share), so an operator leaving weights blank gets a uniform distribution.
//
// A target may carry its own per-model routing (providers/strategy/priorities),
// mirroring a Model Route exactly like Models Group. When it configures none,
// resolution falls back to the tier mapping's routing (or the model's default
// providers + the global routing strategy).
type TierTarget struct {
	// Model is the concrete upstream model id for this candidate.
	Model string `json:"model" yaml:"model"`
	// Weight is the relative selection weight for the "weighted" strategy.
	Weight int `json:"weight,omitempty" yaml:"weight,omitempty"`
	// Providers pins this target model to a subset of upstream provider keys.
	// Empty inherits the tier's Providers (or the model's default providers).
	Providers []string `json:"providers,omitempty" yaml:"providers,omitempty"`
	// Strategy optionally overrides the routing strategy ("priority"/"failover")
	// for this target. Empty inherits the tier's Strategy (or the global
	// routing strategy).
	Strategy string `json:"strategy,omitempty" yaml:"strategy,omitempty"`
	// Priorities optionally assigns a priority weight per pinned provider for
	// this target (higher = primary). Only providers also pinned for the target
	// are honored.
	Priorities []ProviderPriority `json:"priorities,omitempty" yaml:"priorities,omitempty"`
}

// TierMapping maps a single complexity tier to one or more concrete upstream
// targets plus per-model routing, mirroring a Model Route (providers + strategy
// + priorities) exactly like Models Group.
//
// A tier targets a single model via Model (the legacy form) or multiple models
// via Targets. When Targets is non-empty it is authoritative and Model (if also
// set) is kept for display/back-compat only. When both Model and Targets are
// empty the tier is unresolved and resolution falls back to a lower tier.
//
// The routing fields on the mapping act as the tier's default routing: a target
// that carries its own per-target routing wins, otherwise the tier's routing
// applies (or the target model's default providers + global strategy).
type TierMapping struct {
	// Tier is the complexity tier this mapping applies to.
	Tier Tier `json:"tier" yaml:"tier"`
	// Model is the concrete upstream model id to send requests classified into
	// this tier (e.g. "claude-sonnet-4-5"). Ignored for selection when Targets
	// is non-empty. Empty means the tier has no single-model target.
	Model string `json:"model,omitempty" yaml:"model,omitempty"`
	// Targets optionally lists multiple candidate upstream models for this tier,
	// selected per TargetStrategy. Empty falls back to the single Model.
	Targets []TierTarget `json:"targets,omitempty" yaml:"targets,omitempty"`
	// TargetStrategy picks among Targets: "" inherits the default, "weighted"
	// (the default for multiple targets) selects randomly weighted by each
	// target's Weight, and "priority" deterministically selects the highest
	// weight (ties broken by list order). Ignored for a single target.
	TargetStrategy string `json:"target_strategy,omitempty" yaml:"target_strategy,omitempty"`
	// Providers pins the tier's target model(s) to a subset of upstream provider
	// keys. Empty inherits the model's default providers.
	Providers []string `json:"providers,omitempty" yaml:"providers,omitempty"`
	// Strategy optionally overrides the routing strategy ("priority"/"failover").
	// Empty inherits the global routing strategy. Acts as the tier's default
	// routing for targets that do not configure their own.
	Strategy string `json:"strategy,omitempty" yaml:"strategy,omitempty"`
	// Priorities optionally assigns a priority weight per pinned provider
	// (higher = primary). Only providers also in Providers are honored.
	Priorities []ProviderPriority `json:"priorities,omitempty" yaml:"priorities,omitempty"`
}

// Config is the operator-facing definition of a single Auto Router. It also
// acts as a "global model": it has a name, a requestable model id, display
// metadata, and pricing, exactly like a regular model, and can be instantiated
// multiple times with distinct names/model ids.
type Config struct {
	// ID is the persisted store id (uuid).
	ID string `json:"id,omitempty"`
	// Name is the human-friendly router name (unique), e.g. "smart-router".
	Name string `json:"name"`
	// ModelID is the client-facing model id requesters call, e.g.
	// "router:smart-router". Unique across routers.
	ModelID string `json:"model_id"`
	// Description is free-form operator notes.
	Description string `json:"description,omitempty"`
	// DisplayName is the label shown in listings, mirroring a model display name.
	DisplayName string `json:"display_name,omitempty"`
	// Enabled gates whether the router resolves requests at runtime. Disabled
	// routers pass requests through unchanged.
	Enabled bool `json:"enabled"`
	// Mappings is the per-tier target configuration. It should contain at most
	// one entry per Tier.
	Mappings []TierMapping `json:"mappings,omitempty"`
	// VisionBridgeModel is an optional per-router model used to analyze images
	// when a tier's resolved target model does not support vision. Empty = the
	// vision bridge is disabled for this router.
	VisionBridgeModel string `json:"vision_bridge_model,omitempty"`
	// Pricing is optional model-style pricing metadata (USD per 1M tokens).
	Pricing *Pricing `json:"pricing,omitempty"`
}

// Pricing mirrors a model's pricing metadata for display on the router entity.
// Units are USD per 1M tokens. The cached fields mirror the general model
// pricing convention: CachedInputPer1M is the cache-creation (write) price, and
// CachedReadPer1M is the cache-read ("prompt caching") discount price billed
// for tokens served from an exact prompt-cache match.
type Pricing struct {
	InputPer1M       float64 `json:"input_per_1m_usd,omitempty"`
	OutputPer1M      float64 `json:"output_per_1m_usd,omitempty"`
	CachedInputPer1M float64 `json:"cached_input_per_1m_usd,omitempty"`
	CachedReadPer1M  float64 `json:"cached_read_per_1m_usd,omitempty"`
	ReasoningPer1M   float64 `json:"reasoning_per_1m_usd,omitempty"`
}

// mappingFor returns the TierMapping for tier, or nil when none is configured.
func (c *Config) mappingFor(tier Tier) *TierMapping {
	for i := range c.Mappings {
		if c.Mappings[i].Tier == tier {
			m := c.Mappings[i]
			return &m
		}
	}
	return nil
}
