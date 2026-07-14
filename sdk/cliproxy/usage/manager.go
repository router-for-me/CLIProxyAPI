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
	APIKey       string
	AuthID       string
	AuthIndex    string
	AuthType     string
	Source       string
	// ReasoningEffort stores the translated upstream thinking level for request event logs.
	ReasoningEffort string
	// ServiceTier stores the client-requested service tier.
	ServiceTier string
	// RequestServiceTier is a deprecated input-only alias retained for existing
	// plugin callers. It is normalized into ServiceTier and never emitted.
	RequestServiceTier string
	// ResponseServiceTier stores the final tier reported by the upstream response.
	ResponseServiceTier string
	RequestedAt         time.Time
	Latency             time.Duration
	TTFT                time.Duration
	Failed              bool
	Fail                Failure
	Detail              Detail
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
	ResponseServiceTier string
}

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}
type serviceTierContextKey struct{}

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
