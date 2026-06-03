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

const defaultQueueSize = 512

// Record contains the usage statistics captured for a single provider request.
type Record struct {
	Provider  string
	Model     string
	Alias     string
	APIKey    string
	AuthID    string
	AuthIndex string
	AuthType  string
	Source    string
	// RequestID links all upstream attempts for one client request.
	RequestID string
	// AttemptNo is the 1-based upstream attempt number within the request.
	AttemptNo int
	// RetryReason explains why this attempt followed a previous failure.
	RetryReason string
	// FinalSuccess is filled when the request's final outcome is known.
	FinalSuccess *bool
	// ReasoningEffort stores the client-requested thinking level for request event logs.
	ReasoningEffort string
	RequestedAt     time.Time
	Latency         time.Duration
	Failed          bool
	// ProviderStatusCode stores the upstream HTTP status for failed requests.
	ProviderStatusCode int
	// ErrorCode stores a short provider error code only; raw messages and bodies are never stored here.
	ErrorCode string
	Fail      Failure
	Detail    Detail
	// ResponseHeaders stores a snapshot of upstream response headers for usage sinks.
	ResponseHeaders http.Header
}

// Failure holds HTTP failure metadata for an upstream request attempt.
type Failure struct {
	StatusCode int
	ErrorCode  string
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
}

// RequestAttempt stores request-scoped retry metadata for usage sinks.
type RequestAttempt struct {
	RequestID   string
	AttemptNo   int
	RetryReason string
}

// RequestFinal stores the final outcome for one client request.
type RequestFinal struct {
	RequestID    string
	FinalSuccess bool
	AttemptCount int
	CompletedAt  time.Time
}

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}
type requestAttemptContextKey struct{}

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

// WithRequestAttempt stores request-scoped retry attempt metadata.
func WithRequestAttempt(ctx context.Context, attempt RequestAttempt) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	attempt.RequestID = strings.TrimSpace(attempt.RequestID)
	attempt.RetryReason = strings.TrimSpace(attempt.RetryReason)
	if attempt.RequestID == "" && attempt.AttemptNo <= 0 && attempt.RetryReason == "" {
		return ctx
	}
	return context.WithValue(ctx, requestAttemptContextKey{}, attempt)
}

// RequestAttemptFromContext returns request-scoped retry attempt metadata.
func RequestAttemptFromContext(ctx context.Context) RequestAttempt {
	if ctx == nil {
		return RequestAttempt{}
	}
	raw := ctx.Value(requestAttemptContextKey{})
	switch value := raw.(type) {
	case RequestAttempt:
		value.RequestID = strings.TrimSpace(value.RequestID)
		value.RetryReason = strings.TrimSpace(value.RetryReason)
		return value
	case *RequestAttempt:
		if value == nil {
			return RequestAttempt{}
		}
		out := *value
		out.RequestID = strings.TrimSpace(out.RequestID)
		out.RetryReason = strings.TrimSpace(out.RetryReason)
		return out
	default:
		return RequestAttempt{}
	}
}

// Plugin consumes usage records emitted by the proxy runtime.
type Plugin interface {
	HandleUsage(ctx context.Context, record Record)
}

// RequestFinalizer is implemented by plugins that can update request outcomes.
type RequestFinalizer interface {
	HandleRequestFinal(ctx context.Context, final RequestFinal)
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
	maxLen int
	drops  atomic.Uint64

	pluginsMu sync.RWMutex
	plugins   []Plugin
}

// NewManager constructs a manager with a buffered queue.
func NewManager(buffer int) *Manager {
	if buffer <= 0 {
		buffer = defaultQueueSize
	}
	m := &Manager{maxLen: buffer}
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
	if m.maxLen > 0 && len(m.queue) >= m.maxLen {
		copy(m.queue, m.queue[1:])
		m.queue[len(m.queue)-1] = queueItem{ctx: ctx, record: record}
		m.drops.Add(1)
		m.mu.Unlock()
		m.cond.Signal()
		return
	}
	m.queue = append(m.queue, queueItem{ctx: ctx, record: record})
	m.mu.Unlock()
	m.cond.Signal()
}

// Dropped returns the number of usage records dropped because the queue was full.
func (m *Manager) Dropped() uint64 {
	if m == nil {
		return 0
	}
	return m.drops.Load()
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
		m.queue[0] = queueItem{}
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

// PublishRequestFinal notifies interested plugins about a completed request.
func (m *Manager) PublishRequestFinal(ctx context.Context, final RequestFinal) {
	if m == nil {
		return
	}
	final.RequestID = strings.TrimSpace(final.RequestID)
	if final.RequestID == "" {
		return
	}
	if final.CompletedAt.IsZero() {
		final.CompletedAt = time.Now()
	}

	m.pluginsMu.RLock()
	plugins := make([]Plugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.pluginsMu.RUnlock()
	for _, plugin := range plugins {
		finalizer, ok := plugin.(RequestFinalizer)
		if !ok || finalizer == nil {
			continue
		}
		safeInvokeFinalizer(finalizer, ctx, final)
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

func safeInvokeFinalizer(plugin RequestFinalizer, ctx context.Context, final RequestFinal) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("usage: finalizer plugin panic recovered: %v", r)
		}
	}()
	plugin.HandleRequestFinal(ctx, final)
}

var defaultManager = NewManager(512)

// DefaultManager returns the global usage manager instance.
func DefaultManager() *Manager { return defaultManager }

// RegisterPlugin registers a plugin on the default manager.
func RegisterPlugin(plugin Plugin) { DefaultManager().Register(plugin) }

// PublishRecord publishes a record using the default manager.
func PublishRecord(ctx context.Context, record Record) { DefaultManager().Publish(ctx, record) }

// PublishRequestFinal notifies plugins that one client request has completed.
func PublishRequestFinal(ctx context.Context, final RequestFinal) {
	DefaultManager().PublishRequestFinal(ctx, final)
}

// StartDefault starts the default manager's dispatcher.
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault stops the default manager's dispatcher.
func StopDefault() { DefaultManager().Stop() }
