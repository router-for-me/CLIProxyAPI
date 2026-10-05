package usage

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// DefaultServiceTier is retained for direct SDK and non-OpenAI usage callers.
const DefaultServiceTier = "default"

// AutoServiceTier is the OpenAI request semantics when service_tier is omitted.
// OpenAI HTTP handlers set it explicitly, without changing other providers'
// historical direct-SDK default.
const AutoServiceTier = "auto"

// Record contains the usage statistics captured for a single provider request.
type Record struct {
	Provider string
	// ExecutorType stores the concrete executor type that handled the request.
	ExecutorType string
	Model        string
	Alias        string
	// RequestID is the per-request correlation identifier (sourced from the
	// logging request-id context). Persisted on both usage_events and
	// usage_errors so failures can be correlated back to the originating log
	// entries. Empty when no request id was assigned upstream.
	RequestID string
	APIKey    string
	AuthID    string
	AuthIndex string
	// EntryProviderKey is the synthesizer's per-entry routing identity of the
	// upstream auth that served the request
	// (auth.Attributes[AttributeEntryProviderKey], "<provider-key>:key-<entryID>").
	// It lets the Postgres flusher attribute spend to the exact upstream
	// provider API-key entry (provider budget tracking). Empty when the auth
	// has no per-entry identity (legacy YAML entries, oauth channels).
	EntryProviderKey string
	// AccessTokenSHA256 identifies the OAuth token version without exposing the token.
	AccessTokenSHA256 string
	AuthType          string
	Source            string
	// ReasoningEffort stores the translated upstream thinking level for request event logs.
	ReasoningEffort string
	// ServiceTier stores the client-requested service tier.
	ServiceTier string
	// RequestServiceTier is a deprecated input-only alias retained for existing
	// plugin callers. It is normalized into ServiceTier and never emitted.
	RequestServiceTier string
	// ResponseServiceTier stores the final tier reported by the upstream response.
	ResponseServiceTier string
	// FinishReason records the upstream stop_reason / finish_reason from the
	// provider response. Empty when no reason was reported.
	FinishReason string
	// Tier stores the Auto Router complexity tier (simple/medium/complex/reasoning)
	// that routed this request; empty for non-routed requests.
	Tier string
	// RouterID stores the Auto Router id (e.g. "router:smart") that owned the tier
	// decision; empty for non-routed requests. Kept separate from Alias because the
	// alias is the client-requested model string and is not a reliable aggregation
	// key across renames.
	RouterID string
	// AutoRouterScoredTier is the tier derived from the configured scorer profile
	// (thresholds + weights) before any literal keyword override or mapping
	// fallback. Empty for non-routed requests.
	AutoRouterScoredTier string
	// AutoRouterMappingTier is the tier whose mapping the resolver actually used
	// to pick a target model. It may be lower than the scored/effective tier when
	// the requested tier has no mapping and the resolver falls back. Empty for
	// non-routed requests.
	AutoRouterMappingTier string
	// AutoRouterDecisionCause explains why the effective tier differs from the
	// scored tier (literal_keyword_match) or matches it (complexity_scorer).
	AutoRouterDecisionCause string
	// AutoRouterProfileVersion is the version of the profile used to score the
	// request. Zero when the default built-in profile was used.
	AutoRouterProfileVersion int64
	// AutoRouterProfileHash identifies the exact profile configuration used.
	AutoRouterProfileHash string
	// AutoRouterDecisionJSON is the marshaled explainability snapshot persisted as
	// a JSONB blob. Empty bytes mean "no snapshot" (non-routed requests).
	AutoRouterDecisionJSON []byte
	// Generate reports whether the client requested actual generation.
	// nil or true means generation is enabled; only an explicit false disables generation.
	// Use GenerateFlag to set the value and GenerateEnabled to read it with the default.
	Generate    *bool
	RequestedAt time.Time
	Latency     time.Duration
	TTFT        time.Duration
	Failed      bool
	Fail        Failure
	Detail      Detail
	// ResponseHeaders stores a snapshot of upstream response headers for usage sinks.
	ResponseHeaders http.Header
	// RouteModel stores the model name exactly as the client requested it
	// (before any alias/upstream resolution), captured for usage_errors rows
	// so misrouting (e.g. a model pinned to the wrong provider/endpoint) can
	// be diagnosed by comparing RouteModel against Model (the resolved
	// upstream id sent to the provider).
	RouteModel string
	// ServedModel stores the model identifier the upstream response itself
	// reported serving, captured from the raw response body before any
	// alias/ForceMapping rewrite. Empty when the upstream did not report one.
	// It will be compared against Model at flush time so a silent upstream
	// substitution (the provider serving a different model than requested) is
	// recorded rather than hidden. Invariant: the value pairs only with the
	// primary record's Model — secondary records emitted via
	// PublishAdditionalModel always leave it empty, since their model was not
	// the one captured.
	ServedModel string
	// Endpoint stores the upstream URL the executor actually hit. Persisted
	// on usage_errors so a 4xx/5xx can be traced to the concrete provider
	// path (e.g. openai-compat "/chat/completions" vs an Anthropic-style
	// endpoint), which is otherwise impossible to reconstruct for failed
	// attempts.
	Endpoint string
	// ClientIP is the client TCP address as resolved by gin (honors reverse
	// proxy headers like X-Forwarded-For / X-Real-IP). Persisted on both
	// usage_events and usage_errors so failed requests can be attributed to a
	// source IP.
	ClientIP string
	// ForwardedFor is the raw X-Forwarded-For header captured at the edge,
	// kept separately so multi-hop proxy chains remain auditable even when
	// gin collapses the chained addresses into a single ClientIP.
	ForwardedFor string
	// NetworkRTTMs records the measured network round-trip time to the
	// upstream provider (TCP connect + TLS handshake) in milliseconds.
	// Zero means unmeasured (pre-execution failure or non-HTTP executor).
	NetworkRTTMs int64
	// request, when the provider measures it. Zero means unmeasured.
	EnergyJoules float64
	// ProviderMetadata carries provider-specific billing/attribution data
	// (e.g. Neuralwatt cost, cache savings, grid/carbon attribution) keyed by
	// provider. Persisted to usage_events.provider_metadata.
	ProviderMetadata map[string]any
}

// Failure holds HTTP failure metadata for an upstream request attempt.
type Failure struct {
	StatusCode int
	Body       string
}

// Detail holds the token usage breakdown.
type Detail struct {
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	TokenBreakdown      TokenBreakdown
	ResponseServiceTier string
}

// SubstitutionReport describes whether the upstream served a model different
// from the one NixLLM asked for.
type SubstitutionReport struct {
	// Served is the trimmed model the upstream reported serving.
	Served string
	// Requested is the trimmed model NixLLM resolved and sent upstream.
	Requested string
	// Substituted is true only when both values are known and differ.
	Substituted bool
}

// DetectSubstitution compares the model the upstream reported serving against
// the model NixLLM sent. The comparison is case-insensitive and ignores
// surrounding whitespace, because providers vary the casing of the same id.
// Whitespace inside an id is not normalized away, so an internal difference is
// conservatively reported as a substitution. An empty value on either side
// means "unknown" and is never reported as a substitution — a missing field
// must not raise a false alarm.
func DetectSubstitution(served, requested string) SubstitutionReport {
	trimmedServed := strings.TrimSpace(served)
	trimmedRequested := strings.TrimSpace(requested)
	return SubstitutionReport{
		Served:      trimmedServed,
		Requested:   trimmedRequested,
		Substituted: trimmedServed != "" && trimmedRequested != "" && !strings.EqualFold(trimmedServed, trimmedRequested),
	}
}

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}
type serviceTierContextKey struct{}
type generateContextKey struct{}

// routerTierContextValue carries the Auto Router tier decision stored in ctx.
// It is a named struct (rather than a positional tuple) so the fields read
// unambiguously in both setters and getters.
type routerTierContextValue struct {
	tier     string
	routerID string
}
type routerTierContextKey struct{}

type autoRouterDecisionContextValue struct {
	scoredTier     string
	mappingTier    string
	cause          string
	profileVersion int64
	profileHash    string
	snapshot       []byte
}
type autoRouterDecisionContextKey struct{}

// WithRequestedModelAlias stores the client-requested model name for usage sinks.
func WithRequestedModelAlias(ctx context.Context, alias string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return ctx
	}
	return context.WithValue(ctx, requestedModelAliasContextKey{}, alias)
}

// RequestedModelAliasFromContext returns the client-requested model name stored in ctx.
func RequestedModelAliasFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(requestedModelAliasContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithReasoningEffort stores the client-requested reasoning effort for usage sinks.
func WithReasoningEffort(ctx context.Context, effort string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return ctx
	}
	return context.WithValue(ctx, reasoningEffortContextKey{}, effort)
}

// ReasoningEffortFromContext returns the client-requested reasoning effort stored in ctx.
func ReasoningEffortFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(reasoningEffortContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

// WithServiceTier stores the client-requested service tier for usage sinks.
func WithServiceTier(ctx context.Context, tier string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	tier = strings.TrimSpace(tier)
	if tier == "" {
		tier = DefaultServiceTier
	}
	return context.WithValue(ctx, serviceTierContextKey{}, tier)
}

// ServiceTierFromContext returns the client-requested service tier stored in ctx.
func ServiceTierFromContext(ctx context.Context) string {
	if ctx == nil {
		return DefaultServiceTier
	}
	raw := ctx.Value(serviceTierContextKey{})
	switch value := raw.(type) {
	case string:
		tier := strings.TrimSpace(value)
		if tier == "" {
			return DefaultServiceTier
		}
		return tier
	case []byte:
		tier := strings.TrimSpace(string(value))
		if tier == "" {
			return DefaultServiceTier
		}
		return tier
	default:
		return DefaultServiceTier
	}
}

// WithRouterTier stores the Auto Router tier and router id that routed this request
// for usage sinks. Both values are trimmed; if both are empty, ctx is returned
// unchanged. A nil ctx is tolerated and replaced with context.Background().
func WithRouterTier(ctx context.Context, tier, routerID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	tier = strings.TrimSpace(tier)
	routerID = strings.TrimSpace(routerID)
	if tier == "" && routerID == "" {
		return ctx
	}
	return context.WithValue(ctx, routerTierContextKey{}, routerTierContextValue{tier: tier, routerID: routerID})
}

// RouterTierFromContext returns the Auto Router tier stored in ctx.
// Returns the empty string when ctx is nil or no value is present.
func RouterTierFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(routerTierContextKey{})
	if value, ok := raw.(routerTierContextValue); ok {
		return value.tier
	}
	return ""
}

// RouterIDFromContext returns the Auto Router id stored in ctx.
// Returns the empty string when ctx is nil or no value is present.
func RouterIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(routerTierContextKey{})
	if value, ok := raw.(routerTierContextValue); ok {
		return value.routerID
	}
	return ""
}

// WithAutoRouterDecision stores an immutable Auto Router explainability
// snapshot for usage sinks. The snapshot is copied before it enters the async
// usage queue so callers may safely reuse their buffer.
func WithAutoRouterDecision(ctx context.Context, scoredTier, mappingTier, cause string, profileVersion int64, profileHash string, snapshot []byte) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(scoredTier) == "" && strings.TrimSpace(mappingTier) == "" && len(snapshot) == 0 {
		return ctx
	}
	return context.WithValue(ctx, autoRouterDecisionContextKey{}, autoRouterDecisionContextValue{
		scoredTier: strings.TrimSpace(scoredTier), mappingTier: strings.TrimSpace(mappingTier), cause: strings.TrimSpace(cause),
		profileVersion: profileVersion, profileHash: strings.TrimSpace(profileHash), snapshot: append([]byte(nil), snapshot...),
	})
}

// AutoRouterDecisionFromContext returns a copied decision context value.
func AutoRouterDecisionFromContext(ctx context.Context) (scoredTier, mappingTier, cause string, profileVersion int64, profileHash string, snapshot []byte) {
	if ctx == nil {
		return "", "", "", 0, "", nil
	}
	value, ok := ctx.Value(autoRouterDecisionContextKey{}).(autoRouterDecisionContextValue)
	if !ok {
		return "", "", "", 0, "", nil
	}
	return value.scoredTier, value.mappingTier, value.cause, value.profileVersion, value.profileHash, append([]byte(nil), value.snapshot...)
}

// Missing context values default to true; only an explicit false disables generation.
func WithGenerate(ctx context.Context, generate bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, generateContextKey{}, generate)
}

// GenerateFromContext returns whether the client requested actual generation.
// Missing values default to true.
func GenerateFromContext(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	raw := ctx.Value(generateContextKey{})
	switch value := raw.(type) {
	case bool:
		return value
	default:
		return true
	}
}

// GenerateFlag returns a pointer suitable for Record.Generate.
func GenerateFlag(generate bool) *bool {
	return &generate
}

// GenerateEnabled reports whether generation is enabled for the record field.
// A nil value defaults to true so legacy callers that omit Generate keep the historical behavior.
func GenerateEnabled(generate *bool) bool {
	if generate == nil {
		return true
	}
	return *generate
}

// Plugin consumes usage records emitted by the proxy runtime.
type Plugin interface {
	HandleUsage(ctx context.Context, record Record)
}

type queueItem struct {
	ctx    context.Context
	record Record
}

// Manager maintains a queue of usage records and delivers them to registered plugins.
type Manager struct {
	once     sync.Once
	stopOnce sync.Once
	cancel   context.CancelFunc

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []queueItem
	closed bool

	pluginsMu sync.RWMutex
	plugins   []Plugin
	named     map[string]int
}

// NewManager constructs a manager with a buffered queue.
func NewManager(buffer int) *Manager {
	m := &Manager{}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// Start launches the background dispatcher. Calling Start multiple times is safe.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.once.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		var workerCtx context.Context
		workerCtx, m.cancel = context.WithCancel(ctx)
		go m.run(workerCtx)
	})
}

// Stop stops the dispatcher and drains the queue.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		m.cond.Broadcast()
	})
}

// Register appends a plugin to the delivery list.
func (m *Manager) Register(plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	m.pluginsMu.Lock()
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// RegisterNamed registers or replaces a plugin by name.
func (m *Manager) RegisterNamed(name string, plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}

	m.pluginsMu.Lock()
	if m.named == nil {
		m.named = make(map[string]int)
	}
	if index, exists := m.named[name]; exists && index >= 0 && index < len(m.plugins) {
		m.plugins[index] = plugin
		m.pluginsMu.Unlock()
		return
	}
	m.named[name] = len(m.plugins)
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// Publish enqueues a usage record for processing. If no plugin is registered
// the record will be discarded downstream.
func (m *Manager) Publish(ctx context.Context, record Record) {
	if m == nil {
		return
	}
	// ensure worker is running even if Start was not called explicitly
	m.Start(context.Background())
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.queue = append(m.queue, queueItem{ctx: ctx, record: record})
	m.mu.Unlock()
	m.cond.Signal()
}

func (m *Manager) run(ctx context.Context) {
	for {
		m.mu.Lock()
		for !m.closed && len(m.queue) == 0 {
			m.cond.Wait()
		}
		if len(m.queue) == 0 && m.closed {
			m.mu.Unlock()
			return
		}
		item := m.queue[0]
		m.queue = m.queue[1:]
		m.mu.Unlock()
		m.dispatch(item)
	}
}

func (m *Manager) dispatch(item queueItem) {
	m.pluginsMu.RLock()
	plugins := make([]Plugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.pluginsMu.RUnlock()
	if len(plugins) == 0 {
		return
	}
	for _, plugin := range plugins {
		if plugin == nil {
			continue
		}
		safeInvoke(plugin, item.ctx, item.record)
	}
}

func safeInvoke(plugin Plugin, ctx context.Context, record Record) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("usage: plugin panic recovered: %v", r)
		}
	}()
	plugin.HandleUsage(ctx, record)
}

var defaultManager = NewManager(512)

// DefaultManager returns the global usage manager instance.
func DefaultManager() *Manager { return defaultManager }

// RegisterPlugin registers a plugin on the default manager.
func RegisterPlugin(plugin Plugin) { DefaultManager().Register(plugin) }

// RegisterNamedPlugin registers or replaces a named plugin on the default manager.
func RegisterNamedPlugin(name string, plugin Plugin) { DefaultManager().RegisterNamed(name, plugin) }

// PublishRecord publishes a record using the default manager.
func PublishRecord(ctx context.Context, record Record) { DefaultManager().Publish(ctx, record) }

// StartDefault starts the default manager's dispatcher.
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault stops the default manager's dispatcher.
func StopDefault() { DefaultManager().Stop() }
