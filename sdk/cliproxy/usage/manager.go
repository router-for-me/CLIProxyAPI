package usage

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
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
	// RequestID uniquely identifies this specific model execution instance (UUID v4).
	RequestID string
	// TraceID identifies the parent inbound HTTP request when available (8-character hex).
	TraceID string
	// SelectionID identifies the scheduler selection decision that produced this usage record when available.
	SelectionID string
	// HomeSelection carries deferred accounting for reused Home dispatches.
	HomeSelection *HomeSelection
	Provider      string
	// BaseURL stores the configured upstream base URL when available.
	BaseURL string
	// ExecutorType stores the concrete executor type that handled the request.
	ExecutorType string
	Model        string
	Alias        string
	APIKey       string
	// IsNativeKey reports whether the client API key was authenticated by CPA's native config provider.
	IsNativeKey bool
	// AccessProvider identifies the client request authentication provider (e.g. "config-inline" or plugin ID).
	AccessProvider  string
	SessionID       string
	ParentSessionID string
	AuthID          string
	AuthIndex       string
	// AccessTokenSHA256 identifies the OAuth token version without exposing the token.
	AccessTokenSHA256 string
	AuthType          string
	Source            string
	// ReasoningEffort stores the translated upstream thinking level for request event logs.
	ReasoningEffort string
	// ServiceTier stores the client-requested service tier.
	ServiceTier string
	// EffectiveServiceTier stores the tier in the final outbound request after
	// protocol translation and payload defaults or overrides.
	EffectiveServiceTier string
	// RequestServiceTier is a deprecated input-only alias retained for existing
	// plugin callers. It is normalized into ServiceTier and never emitted.
	RequestServiceTier string
	// ResponseServiceTier stores the final tier reported by the upstream response.
	ResponseServiceTier string
	// ResponseModel stores the model name reported by the upstream response, empty when unknown.
	ResponseModel string
	// Generate reports whether the client requested actual generation.
	// nil or true means generation is enabled; only an explicit false disables generation.
	// Use GenerateFlag to set the value and GenerateEnabled to read it with the default.
	Generate *bool
	// Stream reports whether the request was executed in streaming mode.
	Stream      bool
	RequestedAt time.Time
	Latency     time.Duration
	TTFT        time.Duration
	Failed      bool
	Fail        Failure
	Detail      Detail
	// ResponseHeaders stores a snapshot of upstream response headers for usage sinks.
	ResponseHeaders http.Header
}

// FailureStreamTruncatedCode marks a locally synthesized failure for an
// accepted upstream stream that ended without its terminal frame.
const FailureStreamTruncatedCode = "stream_truncated"

// FailureRequestScopedCode marks failures belonging to the request rather than
// the selected credential, including local capability validation failures.
const FailureRequestScopedCode = "request_scoped"

// FailureForceCooldownCode marks failures matched by a request-scoped error rule
// configured with a cooldown action (stop-and-cooldown or continue-and-cooldown).
const FailureForceCooldownCode = "force_cooldown"

// Failure holds HTTP failure metadata for an upstream request attempt.
type Failure struct {
	StatusCode int
	Body       string
	// Code carries structured failure classification (e.g. FailureStreamTruncatedCode)
	// across usage sinks so downstream schedulers do not treat locally
	// synthesized statuses as received upstream refusals.
	Code string
}

// Detail holds the token and structured usage breakdown captured from an upstream response.
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

	CacheCreation5mTokens    *int64
	CacheCreation30mTokens   *int64
	CacheCreation1hTokens    *int64
	CacheCreation12hTokens   *int64
	TextInputTokens          *int64
	ImageInputTokens         *int64
	AudioInputTokens         *int64
	VideoInputTokens         *int64
	FileInputTokens          *int64
	CachedTextTokens         *int64
	CachedImageTokens        *int64
	CachedAudioTokens        *int64
	TextOutputTokens         *int64
	ImageOutputTokens        *int64
	AudioOutputTokens        *int64
	AcceptedPredictionTokens *int64
	RejectedPredictionTokens *int64
	CacheDetails             *CacheDetails
	WebSearchRequests        *int64
	ImageCount               *int64
	RequestedImageDetails    *ImageDetails
	ImageDetails             *ImageDetails
	MediaCounts              *MediaCounts
	ToolUsageDetails         *ToolUsageDetails
	Iterations               *[]IterationUsage
	CostDetails              *CostDetails
	Speed                    string
	InferenceGeo             string
	UpstreamServiceTier      string
}

type requestedModelAliasContextKey struct{}
type reasoningEffortContextKey struct{}
type serviceTierContextKey struct{}
type generateContextKey struct{}
type streamContextKey struct{}
type executionRequestIDContextKey struct{}
type executionTraceIDContextKey struct{}
type selectionIDContextKey struct{}
type apiKeyContextKey struct{}
type accessProviderContextKey struct{}
type isNativeKeyContextKey struct{}
type capturedIdentityContextKey struct{}

type capturedIdentityHolder struct {
	mu             sync.RWMutex
	apiKey         string
	accessProvider string
	isNativeKey    bool
	hasNativeKey   bool
}

// WithExecutionRequestID attaches a specific execution instance request ID to the context.
func WithExecutionRequestID(ctx context.Context, requestID string) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, executionRequestIDContextKey{}, strings.TrimSpace(requestID))
}

// ExecutionRequestIDFromContext retrieves the execution instance request ID from the context.
func ExecutionRequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(executionRequestIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithTraceID attaches the parent inbound HTTP request ID to the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return context.WithValue(ctx, executionTraceIDContextKey{}, strings.TrimSpace(traceID))
}

// TraceIDFromContext retrieves the parent inbound HTTP request ID from the context.
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if v, ok := ctx.Value(executionTraceIDContextKey{}).(string); ok {
		return v
	}
	return ""
}

// WithSelectionID attaches the scheduler selection ID for the selected credential to the context.
func WithSelectionID(ctx context.Context, selectionID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, selectionIDContextKey{}, strings.TrimSpace(selectionID))
}

// SelectionIDFromContext retrieves the scheduler selection ID from the context.
func SelectionIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	raw := ctx.Value(selectionIDContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
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

// WithStream stores whether the request was executed in streaming mode for usage sinks.
func WithStream(ctx context.Context, stream bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, streamContextKey{}, stream)
}

// StreamFromContext returns whether the request was executed in streaming mode.
// Missing values default to false.
func StreamFromContext(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	raw := ctx.Value(streamContextKey{})
	switch value := raw.(type) {
	case bool:
		return value
	default:
		return false
	}
}

// WithAccessProvider stores the client authentication provider identifier for usage sinks.
func WithAccessProvider(ctx context.Context, provider string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, accessProviderContextKey{}, provider)
	if holder, ok := ctx.Value(capturedIdentityContextKey{}).(*capturedIdentityHolder); ok && holder != nil {
		holder.mu.RLock()
		cloned := &capturedIdentityHolder{
			apiKey:         holder.apiKey,
			accessProvider: provider,
			isNativeKey:    holder.isNativeKey,
			hasNativeKey:   holder.hasNativeKey,
		}
		holder.mu.RUnlock()
		ctx = context.WithValue(ctx, capturedIdentityContextKey{}, cloned)
	}
	return ctx
}

// WithAPIKey stores the client API key for usage sinks.
func WithAPIKey(ctx context.Context, apiKey string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, apiKeyContextKey{}, apiKey)
	if holder, ok := ctx.Value(capturedIdentityContextKey{}).(*capturedIdentityHolder); ok && holder != nil {
		holder.mu.RLock()
		cloned := &capturedIdentityHolder{
			apiKey:         apiKey,
			accessProvider: holder.accessProvider,
			isNativeKey:    holder.isNativeKey,
			hasNativeKey:   holder.hasNativeKey,
		}
		holder.mu.RUnlock()
		ctx = context.WithValue(ctx, capturedIdentityContextKey{}, cloned)
	}
	return ctx
}

// WithCapturedIdentity stores an authoritative snapshot of the request's usage identity
// (including empty values) so later asynchronous calls do not read a reused gin.Context.
func WithCapturedIdentity(ctx context.Context, apiKey, accessProvider string, isNativeKey ...bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	holder := &capturedIdentityHolder{
		apiKey:         strings.TrimSpace(apiKey),
		accessProvider: strings.TrimSpace(accessProvider),
	}
	if len(isNativeKey) > 0 {
		holder.isNativeKey = isNativeKey[0]
		holder.hasNativeKey = true
	}
	return context.WithValue(ctx, capturedIdentityContextKey{}, holder)
}

// CaptureIdentity snapshots the current request's usage identity into ctx while the
// HTTP handler is active so post-handler goroutines never read a reused gin.Context.
func CaptureIdentity(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	if HasCapturedIdentity(ctx) {
		return ctx
	}
	apiKey := apiKeyFromGinContext(ctx)
	if apiKey == "" {
		apiKey = APIKeyFromContext(ctx)
	}
	accessProvider := AccessProviderFromContext(ctx)
	if isNative, ok := IsNativeKeyFromContext(ctx); ok {
		return WithCapturedIdentity(ctx, apiKey, accessProvider, isNative)
	}
	if accessProvider != "" {
		return WithCapturedIdentity(ctx, apiKey, accessProvider, IsNativeAccessProvider(accessProvider))
	}
	return WithCapturedIdentity(ctx, apiKey, accessProvider)
}

// HasCapturedIdentity reports whether ctx holds a captured usage identity snapshot.
func HasCapturedIdentity(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	holder, ok := ctx.Value(capturedIdentityContextKey{}).(*capturedIdentityHolder)
	return ok && holder != nil
}

// SetCapturedAPIKey updates the captured API key in ctx when an identity snapshot holder exists.
func SetCapturedAPIKey(ctx context.Context, apiKey string) {
	if ctx == nil {
		return
	}
	if holder, ok := ctx.Value(capturedIdentityContextKey{}).(*capturedIdentityHolder); ok && holder != nil {
		holder.mu.Lock()
		holder.apiKey = strings.TrimSpace(apiKey)
		holder.mu.Unlock()
	}
}

func contextValueGetter(ctx context.Context) interface{ Get(string) (any, bool) } {
	if ctx == nil {
		return nil
	}
	raw := ctx.Value("gin")
	if raw == nil {
		return nil
	}
	rv := reflect.ValueOf(raw)
	if rv.Kind() == reflect.Ptr && rv.IsNil() {
		return nil
	}
	getter, ok := raw.(interface{ Get(string) (any, bool) })
	if !ok {
		return nil
	}
	return getter
}

func apiKeyFromGinContext(ctx context.Context) string {
	if ginCtx := contextValueGetter(ctx); ginCtx != nil {
		if v, exists := ginCtx.Get("userApiKey"); exists {
			switch value := v.(type) {
			case string:
				return strings.TrimSpace(value)
			case fmt.Stringer:
				return strings.TrimSpace(value.String())
			default:
				return strings.TrimSpace(fmt.Sprintf("%v", value))
			}
		}
	}
	return ""
}

// APIKeyFromContext returns the client API key stored in ctx or gin.Context.
func APIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if holder, ok := ctx.Value(capturedIdentityContextKey{}).(*capturedIdentityHolder); ok && holder != nil {
		holder.mu.RLock()
		key := holder.apiKey
		holder.mu.RUnlock()
		return key
	}
	raw := ctx.Value(apiKeyContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	}
	return apiKeyFromGinContext(ctx)
}

// AccessProviderFromContext returns the client authentication provider stored in ctx.
func AccessProviderFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if holder, ok := ctx.Value(capturedIdentityContextKey{}).(*capturedIdentityHolder); ok && holder != nil {
		holder.mu.RLock()
		provider := holder.accessProvider
		holder.mu.RUnlock()
		return provider
	}
	raw := ctx.Value(accessProviderContextKey{})
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	}
	if ginCtx := contextValueGetter(ctx); ginCtx != nil {
		if v, exists := ginCtx.Get("accessProvider"); exists {
			switch value := v.(type) {
			case string:
				return strings.TrimSpace(value)
			case []byte:
				return strings.TrimSpace(string(value))
			}
		}
	}
	return ""
}

// WithIsNativeKey stores whether the client API key is native to CPA for usage sinks.
func WithIsNativeKey(ctx context.Context, isNative bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, isNativeKeyContextKey{}, isNative)
	if holder, ok := ctx.Value(capturedIdentityContextKey{}).(*capturedIdentityHolder); ok && holder != nil {
		holder.mu.RLock()
		cloned := &capturedIdentityHolder{
			apiKey:         holder.apiKey,
			accessProvider: holder.accessProvider,
			isNativeKey:    isNative,
			hasNativeKey:   true,
		}
		holder.mu.RUnlock()
		ctx = context.WithValue(ctx, capturedIdentityContextKey{}, cloned)
	}
	return ctx
}

// IsNativeKeyFromContext returns whether the client API key is native to CPA if explicitly set in ctx.
func IsNativeKeyFromContext(ctx context.Context) (bool, bool) {
	if ctx == nil {
		return false, false
	}
	if holder, ok := ctx.Value(capturedIdentityContextKey{}).(*capturedIdentityHolder); ok && holder != nil {
		holder.mu.RLock()
		isNative, hasNative := holder.isNativeKey, holder.hasNativeKey
		holder.mu.RUnlock()
		if hasNative {
			return isNative, true
		}
	}
	raw := ctx.Value(isNativeKeyContextKey{})
	if value, ok := raw.(bool); ok {
		return value, true
	}
	return false, false
}

// IsNativeAccessProvider reports whether provider represents CPA's built-in inline key provider.
func IsNativeAccessProvider(provider string) bool {
	p := strings.TrimSpace(provider)
	return strings.EqualFold(p, "config-inline") || strings.EqualFold(p, "config-api-key")
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

// DefaultQueueCapacity is used when NewManager receives a nonpositive capacity
// and by the zero-value Manager. Capacity counts queued records, excluding the
// single record currently being dispatched.
const DefaultQueueCapacity = 512

// DeliveryStats is a consistent, lifetime snapshot. Accepted, Delivered, Failed,
// Unhandled and rejection counters count records; PluginPanics counts callbacks.
// Accepted = Delivered + Failed + Unhandled + Queued + InFlight.
// Delivered means every callback in the dispatch snapshot returned normally. It
// does not acknowledge durable storage. Stopped means dispatch has finished;
// rejections, Failed and Unhandled still prevent complete delivery coverage.
type DeliveryStats struct {
	Accepted       uint64
	RejectedFull   uint64
	RejectedClosed uint64
	Delivered      uint64
	Failed         uint64
	Unhandled      uint64
	PluginPanics   uint64
	Capacity       int
	Queued         int
	InFlight       int
	Closed         bool
	Stopped        bool
}

// Manager maintains a bounded queue and delivers records serially to plugins.
// A manager has one lifetime; Start cannot reopen it after Stop or Shutdown.
type Manager struct {
	mu      sync.Mutex
	cond    *sync.Cond
	queue   []queueItem
	head    int
	queued  int
	started bool
	closed  bool
	done    chan struct{}
	stats   DeliveryStats

	pluginsMu sync.RWMutex
	plugins   []Plugin
	named     map[string]int
}

// NewManager constructs a manager with a fixed queue capacity. Nonpositive
// capacities use DefaultQueueCapacity. A full queue rejects new records.
func NewManager(buffer int) *Manager {
	if buffer <= 0 {
		buffer = DefaultQueueCapacity
	}
	m := &Manager{queue: make([]queueItem, buffer)}
	m.initLocked()
	return m
}

// initLocked also supports the zero-value Manager. The caller holds m.mu or
// owns a manager that has not been published to other goroutines.
func (m *Manager) initLocked() {
	if m.cond == nil {
		m.cond = sync.NewCond(&m.mu)
		m.done = make(chan struct{})
		if m.queue == nil {
			m.queue = make([]queueItem, DefaultQueueCapacity)
		}
	}
}

// Start launches the dispatcher once. As before, ctx does not cancel delivery;
// call Stop or Shutdown to close admission. Publication also starts dispatch.
func (m *Manager) Start(_ context.Context) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initLocked()
	if !m.started && !m.closed {
		m.started = true
		go m.run()
	}
}

// Stop closes admission and begins draining accepted records without waiting.
// Use Shutdown to wait for dispatch to finish with a caller-provided context.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initLocked()
	if m.closed {
		return
	}
	m.closed = true
	if !m.started {
		m.stats.Stopped = true
		close(m.done)
	}
	m.cond.Broadcast()
}

// Shutdown closes admission and waits for all accepted records to finish
// dispatch. It is safe to call concurrently or after an earlier timeout.
// A nil context uses context.Background and has no deadline.
// A context error means dispatch may still be active; records are not discarded.
// Plugins are called synchronously and cannot be forcibly interrupted. A plugin
// that never returns can retain the single worker, but Shutdown creates no
// waiting goroutine. Do not call Shutdown synchronously from a usage callback.
// A nil error means dispatch ended, not that delivery coverage is complete;
// inspect Stats for rejections, plugin failures and records without plugins.
func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.Stop()
	m.mu.Lock()
	done := m.done
	m.mu.Unlock()
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Stats returns counters and queue state from the same instant.
func (m *Manager) Stats() DeliveryStats {
	if m == nil {
		return DeliveryStats{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initLocked()
	stats := m.stats
	stats.Capacity = len(m.queue)
	stats.Queued = m.queued
	stats.Closed = m.closed
	return stats
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

// Publish attempts to enqueue a usage record. It preserves the legacy void API;
// use TryPublish for admission feedback and Stats for all rejection counts.
func (m *Manager) Publish(ctx context.Context, record Record) {
	m.TryPublish(ctx, record)
}

// TryPublish enqueues without waiting for a plugin or free queue space. False
// means the manager is nil, closed or full. A cancelled request context does not
// reject usage: the original context is still passed to each plugin as before.
func (m *Manager) TryPublish(ctx context.Context, record Record) bool {
	if m == nil {
		return false
	}
	if strings.TrimSpace(record.RequestID) == "" {
		if reqID := ExecutionRequestIDFromContext(ctx); reqID != "" {
			record.RequestID = reqID
		} else {
			record.RequestID = uuid.NewString()
		}
	}
	if strings.TrimSpace(record.TraceID) == "" {
		if trID := TraceIDFromContext(ctx); trID != "" {
			record.TraceID = trID
		} else if trID := internallogging.GetRequestID(ctx); trID != "" {
			record.TraceID = trID
		}
	}
	if record.HomeSelection == nil {
		record.HomeSelection = HomeSelectionFromContext(ctx)
	}
	if strings.TrimSpace(record.SelectionID) == "" {
		if selID := SelectionIDFromContext(ctx); selID != "" {
			record.SelectionID = selID
		}
	}
	if strings.TrimSpace(record.APIKey) == "" {
		if key := APIKeyFromContext(ctx); key != "" {
			record.APIKey = key
		}
	}
	if strings.TrimSpace(record.AccessProvider) == "" {
		if provider := AccessProviderFromContext(ctx); provider != "" {
			record.AccessProvider = provider
		}
	}
	if !record.IsNativeKey {
		if nativeExplicit, ok := IsNativeKeyFromContext(ctx); ok {
			record.IsNativeKey = nativeExplicit
		} else if record.AccessProvider != "" {
			record.IsNativeKey = IsNativeAccessProvider(record.AccessProvider)
		}
	}
	ctx = WithCapturedIdentity(ctx, record.APIKey, record.AccessProvider, record.IsNativeKey)
	// ensure worker is running even if Start was not called explicitly
	m.Start(context.Background())
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		m.stats.RejectedClosed++
		return false
	}
	if m.queued == len(m.queue) {
		m.stats.RejectedFull++
		return false
	}
	index := (m.head + m.queued) % len(m.queue)
	m.queue[index] = queueItem{ctx: ctx, record: record}
	m.queued++
	m.stats.Accepted++
	m.cond.Signal()
	return true
}

func (m *Manager) run() {
	for {
		m.mu.Lock()
		for !m.closed && m.queued == 0 {
			m.cond.Wait()
		}
		if m.queued == 0 && m.closed {
			m.stats.Stopped = true
			close(m.done)
			m.mu.Unlock()
			return
		}
		item := m.queue[m.head]
		m.queue[m.head] = queueItem{}
		m.head = (m.head + 1) % len(m.queue)
		m.queued--
		m.stats.InFlight = 1
		m.mu.Unlock()
		plugins, panics := m.dispatch(item)
		item = queueItem{}
		m.mu.Lock()
		m.stats.InFlight = 0
		m.stats.PluginPanics += panics
		switch {
		case panics != 0:
			m.stats.Failed++
		case plugins == 0:
			m.stats.Unhandled++
		default:
			m.stats.Delivered++
		}
		m.mu.Unlock()
	}
}

func (m *Manager) dispatch(item queueItem) (invoked, panics uint64) {
	m.pluginsMu.RLock()
	plugins := make([]Plugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.pluginsMu.RUnlock()
	for _, plugin := range plugins {
		if plugin == nil {
			continue
		}
		invoked++
		if !safeInvoke(plugin, item.ctx, item.record) {
			panics++
		}
	}
	return invoked, panics
}

func safeInvoke(plugin Plugin, ctx context.Context, record Record) (delivered bool) {
	defer func() {
		if recover() != nil {
			// Panic values can contain private request data. Count the failure
			// without including the panic value or record in logs.
			log.Error("usage: plugin panic recovered")
		}
	}()
	plugin.HandleUsage(ctx, record)
	return true
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

// StopDefault closes admission and begins draining without waiting.
func StopDefault() { DefaultManager().Stop() }

// TryPublishRecord attempts admission to the default manager.
func TryPublishRecord(ctx context.Context, record Record) bool {
	return DefaultManager().TryPublish(ctx, record)
}

// ShutdownDefault closes admission and joins the default dispatcher.
func ShutdownDefault(ctx context.Context) error { return DefaultManager().Shutdown(ctx) }
