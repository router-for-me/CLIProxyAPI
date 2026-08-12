package usage

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	// APIKey contains the raw authenticated client key for compatibility.
	// It is sensitive; usage sinks should prefer ClientKeyID for persistence.
	APIKey string
	// ClientKeyID is the stable identity used to aggregate client usage.
	ClientKeyID string
	// ClientKeyAlias is the display-only name configured for the client key.
	ClientKeyAlias string
	AuthID         string
	AuthIndex      string
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
	// Generate reports whether the client requested actual generation.
	// nil or true means generation is enabled; only an explicit false disables generation.
	// Use GenerateFlag to set the value and GenerateEnabled to read it with the default.
	Generate    *bool
	RequestedAt time.Time
	Latency     time.Duration
	TTFT        time.Duration
	// ExternalRequest marks the final outcome of one client request. Executor
	// records leave this false and represent individual upstream attempts.
	ExternalRequest bool
	// UpstreamAttempt marks a real credential/model/provider attempt. Supplemental
	// usage records (for example an image tool model reported in one response)
	// carry token detail but do not increment attempt counters.
	UpstreamAttempt bool
	// Supplemental marks token-only usage attached to an upstream attempt. It is
	// kept separate so external plugins that only set OutcomeKnown still default
	// to a real upstream attempt.
	Supplemental bool
	// OutcomeKnown prevents legacy sinks from replacing an explicit outcome
	// with a stale response status kept in the request context.
	OutcomeKnown bool
	Failed       bool
	Fail         Failure
	Detail       Detail
	// ResponseHeaders stores a snapshot of upstream response headers for usage sinks.
	ResponseHeaders http.Header
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

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}
type serviceTierContextKey struct{}
type generateContextKey struct{}
type managerContextKey struct{}

type requestStateContextKey struct{}

type requestState struct {
	mu        sync.Mutex
	startedAt time.Time
	prototype Record
	hasRecord bool
	finalized bool
}

// BeginRequest starts a request-level usage scope. Nested calls reuse the
// existing scope so provider fallbacks cannot emit duplicate final outcomes.
func BeginRequest(ctx context.Context, seed Record) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if state, _ := ctx.Value(requestStateContextKey{}).(*requestState); state != nil {
		return ctx
	}
	startedAt := seed.RequestedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
		seed.RequestedAt = startedAt
	}
	state := &requestState{startedAt: startedAt, prototype: seed}
	return context.WithValue(ctx, requestStateContextKey{}, state)
}

func requestStateFromContext(ctx context.Context) *requestState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(requestStateContextKey{}).(*requestState)
	return state
}

func observeRequestRecord(ctx context.Context, record Record) {
	state := requestStateFromContext(ctx)
	if state == nil || record.ExternalRequest || (record.OutcomeKnown && record.Supplemental) {
		return
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if record.ClientKeyID == "" {
		record.ClientKeyID = state.prototype.ClientKeyID
	}
	if record.ClientKeyAlias == "" {
		record.ClientKeyAlias = state.prototype.ClientKeyAlias
	}
	// Prefer the last successful attempt as the request metadata prototype;
	// otherwise retain the most recent failure for diagnostics.
	if !state.hasRecord || !record.Failed || state.prototype.Failed {
		state.prototype = record
	}
	state.hasRecord = true
}

// FinalizeRequest emits one idempotent external-request outcome. Token detail
// is intentionally omitted because token usage belongs to upstream attempts
// and copying it here would double-charge the request.
func FinalizeRequest(ctx context.Context, failed bool, failure Failure) {
	state := requestStateFromContext(ctx)
	if state == nil {
		return
	}
	state.mu.Lock()
	if state.finalized {
		state.mu.Unlock()
		return
	}
	state.finalized = true
	prototype := state.prototype
	startedAt := state.startedAt
	state.mu.Unlock()

	now := time.Now()
	latency := time.Duration(0)
	if !startedAt.IsZero() {
		latency = now.Sub(startedAt)
		if latency < 0 {
			latency = 0
		}
	}
	prototype.ExternalRequest = true
	prototype.OutcomeKnown = true
	prototype.RequestedAt = startedAt
	prototype.Latency = latency
	prototype.TTFT = 0
	prototype.Detail = Detail{}
	prototype.Failed = failed
	prototype.Fail = failure
	PublishRecord(ctx, prototype)
}

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

// WithGenerate stores whether the client requested actual generation for usage sinks.
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

// ClientKeyMetadataFromContext reads the access provider's stable client-key
// metadata without depending on Gin or the access package. It is also useful
// when a request fails before an upstream credential is selected.
func ClientKeyMetadataFromContext(ctx context.Context) (string, string) {
	if ctx == nil {
		return "", ""
	}
	holder, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) })
	if !ok || holder == nil {
		return "", ""
	}
	raw, exists := holder.Get("accessMetadata")
	if !exists {
		return "", ""
	}
	get := func(key string) string {
		switch metadata := raw.(type) {
		case map[string]string:
			return strings.TrimSpace(metadata[key])
		case map[string]any:
			value, _ := metadata[key].(string)
			return strings.TrimSpace(value)
		default:
			return ""
		}
	}
	return get("client_key_id"), get("client_key_alias")
}

// WithManager scopes built-in usage delivery to the service handling a
// request. Process-wide plugins continue to receive the same record through
// the default manager.
func WithManager(ctx context.Context, manager *Manager) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if manager == nil {
		return ctx
	}
	return context.WithValue(ctx, managerContextKey{}, manager)
}

// ManagerFromContext returns the service-scoped usage manager, when present.
func ManagerFromContext(ctx context.Context) *Manager {
	if ctx == nil {
		return nil
	}
	manager, _ := ctx.Value(managerContextKey{}).(*Manager)
	return manager
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
	done     chan struct{}

	mu              sync.Mutex
	cond            *sync.Cond
	queue           []queueItem
	maxQueue        int
	closed          bool
	dropped         atomic.Uint64
	publishInFlight int

	pluginsMu     sync.RWMutex
	plugins       []Plugin
	named         map[string]int
	critical      []Plugin
	criticalNamed map[string]int
}

// NewManager constructs a manager with a buffered queue.
func NewManager(buffer int) *Manager {
	if buffer <= 0 {
		buffer = 1
	}
	m := &Manager{
		done:     make(chan struct{}),
		maxQueue: buffer,
		queue:    make([]queueItem, 0, buffer),
	}
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
	// Ensure a worker exists so Wait can observe completion even when Stop is
	// called before the first Publish or explicit Start.
	m.Start(context.Background())
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

// Wait blocks until the dispatcher has drained its queue and exited. A caller
// must call Stop separately to initiate shutdown.
func (m *Manager) Wait(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// StopAndWait initiates shutdown and waits for all queued usage records to be
// delivered, or until ctx is canceled.
func (m *Manager) StopAndWait(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.Stop()
	return m.Wait(ctx)
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

// RegisterCriticalNamed registers a synchronous plugin that is invoked before
// bounded asynchronous delivery. Critical plugins should do small, non-blocking
// work; they are intended for accounting that must not be dropped under load.
func (m *Manager) RegisterCriticalNamed(name string, plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	m.pluginsMu.Lock()
	defer m.pluginsMu.Unlock()
	if m.criticalNamed == nil {
		m.criticalNamed = make(map[string]int)
	}
	if index, exists := m.criticalNamed[name]; exists && index >= 0 && index < len(m.critical) {
		m.critical[index] = plugin
		return
	}
	m.criticalNamed[name] = len(m.critical)
	m.critical = append(m.critical, plugin)
}

// DroppedRecords returns the number of asynchronous records rejected because
// the bounded queue was full. Critical plugins are invoked before this count.
func (m *Manager) DroppedRecords() uint64 {
	if m == nil {
		return 0
	}
	return m.dropped.Load()
}

// Publish enqueues a usage record for processing. If no plugin is registered
// the record will be discarded downstream.
func (m *Manager) Publish(ctx context.Context, record Record) {
	if m == nil {
		return
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.publishInFlight++
	m.mu.Unlock()

	item := queueItem{ctx: ctx, record: record}
	m.dispatchCritical(item)
	if !m.hasAsyncPlugins() {
		m.finishPublish()
		return
	}

	// Ensure a worker is running even if Start was not called explicitly.
	m.Start(context.Background())
	m.mu.Lock()
	if len(m.queue) >= m.maxQueue {
		m.finishPublishLocked()
		m.mu.Unlock()
		m.cond.Broadcast()
		dropped := m.dropped.Add(1)
		if dropped == 1 || dropped&(dropped-1) == 0 {
			log.Warnf("usage: asynchronous queue full; dropped records=%d", dropped)
		}
		return
	}
	m.queue = append(m.queue, item)
	m.finishPublishLocked()
	m.mu.Unlock()
	m.cond.Broadcast()
}

func (m *Manager) finishPublish() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.finishPublishLocked()
	m.mu.Unlock()
	m.cond.Broadcast()
}

func (m *Manager) finishPublishLocked() {
	if m.publishInFlight > 0 {
		m.publishInFlight--
	}
}

func (m *Manager) run(ctx context.Context) {
	defer close(m.done)
	for {
		m.mu.Lock()
		for len(m.queue) == 0 && (!m.closed || m.publishInFlight > 0) {
			m.cond.Wait()
		}
		if len(m.queue) == 0 && m.closed && m.publishInFlight == 0 {
			m.mu.Unlock()
			return
		}
		item := m.queue[0]
		m.queue[0] = queueItem{}
		m.queue = m.queue[1:]
		if len(m.queue) == 0 {
			m.queue = nil
		}
		m.mu.Unlock()
		m.dispatch(item)
	}
}

func (m *Manager) hasAsyncPlugins() bool {
	m.pluginsMu.RLock()
	hasPlugins := len(m.plugins) > 0
	m.pluginsMu.RUnlock()
	return hasPlugins
}

func (m *Manager) dispatchCritical(item queueItem) {
	m.pluginsMu.RLock()
	plugins := append([]Plugin(nil), m.critical...)
	m.pluginsMu.RUnlock()
	for _, plugin := range plugins {
		if plugin != nil {
			safeInvoke(plugin, item.ctx, item.record)
		}
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

// RegisterCriticalNamedPlugin registers a synchronous accounting plugin on the default manager.
func RegisterCriticalNamedPlugin(name string, plugin Plugin) {
	DefaultManager().RegisterCriticalNamed(name, plugin)
}

// PublishRecord publishes to the service-scoped manager first, then to the
// process-wide default manager used by legacy and plugin sinks.
func PublishRecord(ctx context.Context, record Record) {
	observeRequestRecord(ctx, record)
	defaultUsageManager := DefaultManager()
	if scoped := ManagerFromContext(ctx); scoped != nil && scoped != defaultUsageManager {
		scoped.Publish(ctx, record)
	}
	defaultUsageManager.Publish(ctx, record)
}

// StartDefault starts the default manager's dispatcher.
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault stops the default manager's dispatcher.
func StopDefault() { DefaultManager().Stop() }

// StopDefaultAndWait stops the default manager and waits for queued records to drain.
func StopDefaultAndWait(ctx context.Context) error { return DefaultManager().StopAndWait(ctx) }
