package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// ProviderExecutor defines the contract required by Manager to execute provider calls.
type ProviderExecutor interface {
	// Identifier returns the provider key handled by this executor.
	Identifier() string
	// Execute handles non-streaming execution and returns the provider response payload.
	Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// ExecuteStream handles streaming execution and returns a StreamResult containing
	// upstream headers and a channel of provider chunks.
	ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	// Refresh attempts to refresh provider credentials and returns the updated auth state.
	Refresh(ctx context.Context, auth *Auth) (*Auth, error)
	// CountTokens returns the token count for the given request.
	CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
	// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
	// Callers must close the response body when non-nil.
	HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error)
}

// RequestAuthPreparer lets an executor update missing auth metadata immediately
// before a request. Manager serializes and persists returned updates.
type RequestAuthPreparer interface {
	ShouldPrepareRequestAuth(auth *Auth) bool
	PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error)
}

// ExecutionSessionCloser allows executors to release per-session runtime resources.
type ExecutionSessionCloser interface {
	CloseExecutionSession(sessionID string)
}

const (
	homeAuthCountMetadataKey = "__cliproxy_home_auth_count"
	// CloseAllExecutionSessionsID asks an executor to release all active execution sessions.
	// Executors that do not support this marker may ignore it.
	CloseAllExecutionSessionsID = "__all_execution_sessions__"
)

type requestAttemptTraceContextKey struct{}

type requestAttemptTrace struct {
	mu        sync.Mutex
	requestID string
	attempts  int
}

func ensureRequestAttemptTrace(ctx context.Context) (context.Context, *requestAttemptTrace) {
	if ctx == nil {
		ctx = context.Background()
	}
	if trace, ok := ctx.Value(requestAttemptTraceContextKey{}).(*requestAttemptTrace); ok && trace != nil {
		return ctx, trace
	}
	requestID := strings.TrimSpace(logging.GetRequestID(ctx))
	if requestID == "" {
		requestID = logging.GenerateRequestID()
		ctx = logging.WithRequestID(ctx, requestID)
	}
	trace := &requestAttemptTrace{requestID: requestID}
	return context.WithValue(ctx, requestAttemptTraceContextKey{}, trace), trace
}

func requestAttemptTraceFromContext(ctx context.Context) *requestAttemptTrace {
	if ctx == nil {
		return nil
	}
	trace, _ := ctx.Value(requestAttemptTraceContextKey{}).(*requestAttemptTrace)
	return trace
}

func (t *requestAttemptTrace) nextAttempt(retryReason string) coreusage.RequestAttempt {
	if t == nil {
		return coreusage.RequestAttempt{}
	}
	retryReason = strings.TrimSpace(retryReason)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.attempts++
	return coreusage.RequestAttempt{
		RequestID:   t.requestID,
		AttemptNo:   t.attempts,
		RetryReason: retryReason,
	}
}

func (t *requestAttemptTrace) attemptCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

func (t *requestAttemptTrace) requestIDValue() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.requestID
}

func retryReasonFromError(err error) string {
	if err == nil {
		return ""
	}
	if isTransientRoutingError(err) {
		return "transient_routing_error"
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		code := strings.TrimSpace(authErr.Code)
		if code != "" {
			return code
		}
		if authErr.Retryable {
			return "retryable_error"
		}
	}
	if code := strings.TrimSpace(errorCodeFromError(err)); code != "" {
		return code
	}
	if status := statusCodeFromError(err); status > 0 {
		return "status_" + strconv.Itoa(status)
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) && cooldownErr != nil {
		return "model_cooldown"
	}
	return "upstream_error"
}

func addRequestAttemptLogFields(ctx context.Context, fields log.Fields) {
	if len(fields) == 0 {
		return
	}
	attempt := coreusage.RequestAttemptFromContext(ctx)
	if attempt.RequestID != "" {
		fields["request_id"] = attempt.RequestID
	}
	if attempt.AttemptNo > 0 {
		fields["attempt_no"] = attempt.AttemptNo
	}
	if attempt.RetryReason != "" {
		fields["retry_reason"] = attempt.RetryReason
	}
}

// RefreshEvaluator allows runtime state to override refresh decisions.
type RefreshEvaluator interface {
	ShouldRefresh(now time.Time, auth *Auth) bool
}

const (
	refreshCheckInterval  = 5 * time.Second
	refreshMaxConcurrency = 16
	refreshPendingBackoff = time.Minute
	refreshFailureBackoff = 5 * time.Minute
	// refreshIneffectiveBackoff throttles refresh attempts when an executor returns
	// success but the auth still evaluates as needing refresh (e.g. token expiry
	// wasn't updated). Without this guard, the auto-refresh loop can tight-loop and
	// burn CPU at idle.
	refreshIneffectiveBackoff        = 30 * time.Second
	quotaBackoffBase                 = time.Second
	quotaBackoffMax                  = 30 * time.Minute
	quotaHardCooldownFailures        = health429OpenFailures
	quotaImmediateCooldownRetryAfter = 15 * time.Minute
	accountQuotaCooldown             = 24 * time.Hour
	halfOpenProbeStateLimit          = 4096
	transientNetworkRetryAttempts    = 3
	transientNetworkRetryMaxDelay    = 5 * time.Second
	emptyUpstreamResponseErrorCode   = "empty_upstream_response"
	slowRequestSoftThreshold         = 30 * time.Second
	slowRequestHardThreshold         = time.Minute
	slowRequestSoftPenalty           = 10
	slowRequestHardPenalty           = 25
	slowRequestMinHealthScore        = 10
)

var quotaCooldownDisabled atomic.Bool

// SetQuotaCooldownDisabled toggles quota cooldown scheduling globally.
func SetQuotaCooldownDisabled(disable bool) {
	quotaCooldownDisabled.Store(disable)
}

var deleteUnauthorizedAuthEnabled atomic.Bool

// SetDeleteUnauthorizedAuth toggles whether a 401 response should evict the auth
// from memory and delete it from the underlying store. When false (default), a
// 401 only marks the auth as unauthorized and cools it down (see MarkResult),
// but the auth record is preserved.
func SetDeleteUnauthorizedAuth(enable bool) {
	deleteUnauthorizedAuthEnabled.Store(enable)
}

func quotaCooldownDisabledForAuth(auth *Auth) bool {
	if auth != nil {
		if override, ok := auth.DisableCoolingOverride(); ok {
			return override
		}
	}
	return quotaCooldownDisabled.Load()
}

// Result captures execution outcome used to adjust auth state.
type Result struct {
	// AuthID references the auth that produced this result.
	AuthID string
	// Provider is copied for convenience when emitting hooks.
	Provider string
	// Model is the upstream model identifier used for the request.
	Model string
	// Success marks whether the execution succeeded.
	Success bool
	// RetryAfter carries a provider supplied retry hint (e.g. 429 retryDelay).
	RetryAfter *time.Duration
	// Duration records the upstream attempt duration for health-weight adjustment.
	Duration time.Duration
	// Error describes the failure when Success is false.
	Error *Error
	// Cause keeps the original executor error for typed infrastructure failures.
	Cause error
}

// Selector chooses an auth candidate for execution.
type Selector interface {
	Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error)
}

type loadAwareSelector interface {
	MarkDone(authID, model string)
}

// StoppableSelector is an optional interface for selectors that hold resources.
// Selectors that implement this interface will have Stop called during shutdown.
type StoppableSelector interface {
	Selector
	Stop()
}

// Hook captures lifecycle callbacks for observing auth changes.
type Hook interface {
	// OnAuthRegistered fires when a new auth is registered.
	OnAuthRegistered(ctx context.Context, auth *Auth)
	// OnAuthUpdated fires when an existing auth changes state.
	OnAuthUpdated(ctx context.Context, auth *Auth)
	// OnResult fires when execution result is recorded.
	OnResult(ctx context.Context, result Result)
}

// NoopHook provides optional hook defaults.
type NoopHook struct{}

// OnAuthRegistered implements Hook.
func (NoopHook) OnAuthRegistered(context.Context, *Auth) {}

// OnAuthUpdated implements Hook.
func (NoopHook) OnAuthUpdated(context.Context, *Auth) {}

// OnResult implements Hook.
func (NoopHook) OnResult(context.Context, Result) {}

// Manager orchestrates auth lifecycle, selection, execution, and persistence.
type Manager struct {
	store     Store
	executors map[string]ProviderExecutor
	selector  Selector
	hook      Hook
	mu        sync.RWMutex
	auths     map[string]*Auth
	scheduler *authScheduler
	// homeRuntimeAuths caches auths returned by Home so websocket sessions can
	// reuse an established upstream credential without dispatching every turn.
	homeRuntimeAuths map[string]map[string]*Auth
	// providerOffsets tracks per-model provider rotation state for multi-provider routing.
	providerOffsets map[string]int

	// Retry controls request retry behavior.
	requestRetry        atomic.Int32
	maxRetryCredentials atomic.Int32
	maxRetryInterval    atomic.Int64
	retryQueueDelay     atomic.Int64

	// oauthModelAlias stores global OAuth model alias mappings (alias -> upstream name) keyed by channel.
	oauthModelAlias atomic.Value

	// apiKeyModelAlias caches resolved model alias mappings for API-key auths.
	// Keyed by auth.ID, value is alias(lower) -> upstream model (including suffix).
	apiKeyModelAlias atomic.Value

	// modelPoolOffsets tracks per-auth alias pool rotation state.
	modelPoolOffsets map[string]int

	// runtimeConfig stores the latest application config for request-time decisions.
	// It is initialized in NewManager; never Load() before first Store().
	runtimeConfig atomic.Value

	// dynamicSelectors caches per-routing-group selector instances when routing
	// group strategy overrides are enabled.
	dynamicSelectorsMu sync.Mutex
	dynamicSelectors   map[string]Selector

	// Optional HTTP RoundTripper provider injected by host.
	rtProvider RoundTripperProvider

	// Auto refresh state
	refreshCancel context.CancelFunc
	refreshLoop   *authAutoRefreshLoop

	// halfOpenProbeNext tracks the earliest time another half-open probe may be
	// sent for one auth/model combination.
	halfOpenProbeMu          sync.Mutex
	halfOpenProbeNext        map[string]time.Time
	halfOpenProbeActiveUntil map[string]time.Time
	channelBreakers          map[string]HealthState

	codexModelLoadMu sync.Mutex
	codexModelLoads  map[string]int

	requestPrepareLocks sync.Map
}

// NewManager constructs a manager with optional custom selector and hook.
func NewManager(store Store, selector Selector, hook Hook) *Manager {
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	if hook == nil {
		hook = NoopHook{}
	}
	manager := &Manager{
		store:                    store,
		executors:                make(map[string]ProviderExecutor),
		selector:                 selector,
		hook:                     hook,
		auths:                    make(map[string]*Auth),
		homeRuntimeAuths:         make(map[string]map[string]*Auth),
		providerOffsets:          make(map[string]int),
		modelPoolOffsets:         make(map[string]int),
		dynamicSelectors:         make(map[string]Selector),
		halfOpenProbeNext:        make(map[string]time.Time),
		halfOpenProbeActiveUntil: make(map[string]time.Time),
		channelBreakers:          make(map[string]HealthState),
		codexModelLoads:          make(map[string]int),
	}
	// atomic.Value requires non-nil initial value.
	manager.runtimeConfig.Store(&internalconfig.Config{})
	manager.apiKeyModelAlias.Store(apiKeyModelAliasTable(nil))
	manager.scheduler = newAuthScheduler(selector)
	return manager
}

func isBuiltInSelector(selector Selector) bool {
	switch selector.(type) {
	case *RoundRobinSelector, *FillFirstSelector:
		return true
	default:
		return false
	}
}

func selectorUsesSpread(selector Selector) bool {
	switch s := selector.(type) {
	case *SpreadSelector:
		return true
	case *SessionAffinitySelector:
		return selectorUsesSpread(s.fallback)
	default:
		return false
	}
}

func (m *Manager) syncSchedulerFromSnapshot(auths []*Auth) {
	if m == nil || m.scheduler == nil {
		return
	}
	m.scheduler.rebuild(auths)
}

func (m *Manager) syncScheduler() {
	if m == nil || m.scheduler == nil {
		return
	}
	m.syncSchedulerFromSnapshot(m.snapshotAuths())
}

func (m *Manager) snapshotAuths() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Auth, 0, len(m.auths))
	for _, a := range m.auths {
		out = append(out, a.Clone())
	}
	return out
}

// RefreshSchedulerEntry re-upserts a single auth into the scheduler so that its
// supportedModelSet is rebuilt from the current global model registry state.
// This must be called after models have been registered for a newly added auth,
// because the initial scheduler.upsertAuth during Register/Update runs before
// registerModelsForAuth and therefore snapshots an empty model set.
func (m *Manager) RefreshSchedulerEntry(authID string) {
	if m == nil || m.scheduler == nil || authID == "" {
		return
	}
	m.mu.RLock()
	auth, ok := m.auths[authID]
	if !ok || auth == nil {
		m.mu.RUnlock()
		return
	}
	snapshot := auth.Clone()
	m.mu.RUnlock()
	m.scheduler.upsertAuth(snapshot)
}

// ReconcileRegistryModelStates aligns per-model runtime state with the current
// registry snapshot for one auth.
//
// Supported models are reset to a clean state because re-registration already
// cleared the registry-side cooldown/suspension snapshot. ModelStates for
// models that are no longer present in the registry are pruned entirely so
// renamed/removed models cannot keep auth-level status stale.
func (m *Manager) ReconcileRegistryModelStates(ctx context.Context, authID string) {
	if m == nil || authID == "" {
		return
	}

	supportedModels := registry.GetGlobalRegistry().GetModelsForClient(authID)
	supported := make(map[string]struct{}, len(supportedModels))
	for _, model := range supportedModels {
		if model == nil {
			continue
		}
		modelKey := canonicalModelKey(model.ID)
		if modelKey == "" {
			continue
		}
		supported[modelKey] = struct{}{}
	}

	var snapshot *Auth
	now := time.Now()

	m.mu.Lock()
	auth, ok := m.auths[authID]
	if ok && auth != nil && len(auth.ModelStates) > 0 {
		changed := false
		for modelKey, state := range auth.ModelStates {
			baseModel := canonicalModelKey(modelKey)
			if baseModel == "" {
				baseModel = strings.TrimSpace(modelKey)
			}
			if _, supportedModel := supported[baseModel]; !supportedModel {
				// Drop state for models that disappeared from the current registry
				// snapshot. Keeping them around leaks stale errors into auth-level
				// status, management output, and websocket fallback checks.
				delete(auth.ModelStates, modelKey)
				changed = true
				continue
			}
			if state == nil {
				continue
			}
			if isPersistedModelSupportState(state) {
				registry.GetGlobalRegistry().SuspendClientModel(authID, baseModel, "model_not_supported")
				continue
			}
			if modelStateIsClean(state) {
				continue
			}
			resetModelState(state, now)
			changed = true
		}
		if len(auth.ModelStates) == 0 {
			auth.ModelStates = nil
		}
		if changed {
			updateAggregatedAvailability(auth, now)
			if !hasModelError(auth, now) {
				auth.LastError = nil
				auth.StatusMessage = ""
				auth.Status = StatusActive
			}
			auth.UpdatedAt = now
			if errPersist := m.persist(ctx, auth); errPersist != nil {
				logEntryWithRequestID(ctx).WithField("auth_id", auth.ID).Warnf("failed to persist auth changes during model state reconciliation: %v", errPersist)
			}
			snapshot = auth.Clone()
		}
	}
	m.mu.Unlock()

	if m.scheduler != nil && snapshot != nil {
		m.scheduler.upsertAuth(snapshot)
	}
}

func (m *Manager) SetSelector(selector Selector) {
	if m == nil {
		return
	}
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	m.mu.Lock()
	m.selector = selector
	m.mu.Unlock()
	if m.scheduler != nil {
		m.scheduler.setSelector(selector)
		m.syncScheduler()
	}
}

// SetStore swaps the underlying persistence store.
func (m *Manager) SetStore(store Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
}

// SetRoundTripperProvider register a provider that returns a per-auth RoundTripper.
func (m *Manager) SetRoundTripperProvider(p RoundTripperProvider) {
	m.mu.Lock()
	m.rtProvider = p
	m.mu.Unlock()
}

// SetConfig updates the runtime config snapshot used by request-time helpers.
// Callers should provide the latest config on reload so per-credential alias mapping stays in sync.
func (m *Manager) SetConfig(cfg *internalconfig.Config) {
	if m == nil {
		return
	}
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.runtimeConfig.Store(cfg)
	if !cfg.Home.Enabled {
		m.clearHomeRuntimeAuths()
	}
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	if !m.hasRoutingStrategyOverrides() {
		m.stopDynamicSelectors()
	}
}

// HomeEnabled reports whether the home control plane integration is enabled in the runtime config.
func (m *Manager) HomeEnabled() bool {
	if m == nil {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return cfg != nil && cfg.Home.Enabled
}

func (m *Manager) lookupAPIKeyUpstreamModel(authID, requestedModel string) string {
	if m == nil {
		return ""
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return ""
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return ""
	}
	table, _ := m.apiKeyModelAlias.Load().(apiKeyModelAliasTable)
	if table == nil {
		return ""
	}
	byAlias := table[authID]
	if len(byAlias) == 0 {
		return ""
	}
	key := strings.ToLower(thinking.ParseSuffix(requestedModel).ModelName)
	if key == "" {
		key = strings.ToLower(requestedModel)
	}
	resolved := strings.TrimSpace(byAlias[key])
	if resolved == "" {
		return ""
	}
	return preserveRequestedModelSuffix(requestedModel, resolved)
}

func isAPIKeyAuth(auth *Auth) bool {
	if auth == nil {
		return false
	}
	kind, _ := auth.AccountInfo()
	return strings.EqualFold(strings.TrimSpace(kind), "api_key")
}

func isOpenAICompatAPIKeyAuth(auth *Auth) bool {
	if !isAPIKeyAuth(auth) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return true
	}
	if auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["compat_name"]) != ""
}

func openAICompatProviderKey(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if providerKey := strings.TrimSpace(auth.Attributes["provider_key"]); providerKey != "" {
			return strings.ToLower(providerKey)
		}
		if compatName := strings.TrimSpace(auth.Attributes["compat_name"]); compatName != "" {
			return strings.ToLower(compatName)
		}
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

func openAICompatModelPoolKey(auth *Auth, requestedModel string) string {
	base := strings.TrimSpace(thinking.ParseSuffix(requestedModel).ModelName)
	if base == "" {
		base = strings.TrimSpace(requestedModel)
	}
	return strings.ToLower(strings.TrimSpace(auth.ID)) + "|" + openAICompatProviderKey(auth) + "|" + strings.ToLower(base)
}

func apiKeyModelPoolKey(auth *Auth, requestedModel string) string {
	if auth == nil {
		return ""
	}
	base := strings.TrimSpace(thinking.ParseSuffix(requestedModel).ModelName)
	if base == "" {
		base = strings.TrimSpace(requestedModel)
	}
	return strings.ToLower(strings.TrimSpace(auth.ID)) + "|" + strings.ToLower(strings.TrimSpace(auth.Provider)) + "|" + strings.ToLower(base)
}

func oauthModelAliasPoolKey(auth *Auth, requestedModel string) string {
	if auth == nil {
		return ""
	}
	base := strings.TrimSpace(thinking.ParseSuffix(requestedModel).ModelName)
	if base == "" {
		base = strings.TrimSpace(requestedModel)
	}
	return strings.ToLower(strings.TrimSpace(auth.ID)) + "|" + modelAliasChannel(auth) + "|" + strings.ToLower(base)
}

func (m *Manager) nextModelPoolOffset(key string, size int) int {
	if m == nil || size <= 1 {
		return 0
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.modelPoolOffsets == nil {
		m.modelPoolOffsets = make(map[string]int)
	}
	offset := m.modelPoolOffsets[key]
	if offset >= 2_147_483_640 {
		offset = 0
	}
	m.modelPoolOffsets[key] = offset + 1
	if size <= 0 {
		return 0
	}
	return offset % size
}

func rotateStrings(values []string, offset int) []string {
	if len(values) <= 1 {
		return values
	}
	if offset <= 0 {
		out := make([]string, len(values))
		copy(out, values)
		return out
	}
	offset = offset % len(values)
	out := make([]string, 0, len(values))
	out = append(out, values[offset:]...)
	out = append(out, values[:offset]...)
	return out
}

func (m *Manager) resolveOpenAICompatUpstreamModelPool(auth *Auth, requestedModel string) []string {
	if m == nil || !isOpenAICompatAPIKeyAuth(auth) {
		return nil
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	providerKey := ""
	compatName := ""
	if auth.Attributes != nil {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider)
	if entry == nil {
		return nil
	}
	return resolveModelAliasPoolFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func preserveRequestedModelSuffix(requestedModel, resolved string) string {
	return preserveResolvedModelSuffix(resolved, thinking.ParseSuffix(requestedModel))
}

func (m *Manager) executionModelCandidates(auth *Auth, routeModel string) []string {
	if auth != nil && auth.Attributes != nil {
		if homeModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey]); homeModel != "" {
			return []string{homeModel}
		}
	}
	requestedModel := rewriteModelForAuth(routeModel, auth)
	if pool := m.resolveOAuthUpstreamModelPool(auth, requestedModel); len(pool) > 0 {
		if len(pool) == 1 {
			requestedModel = pool[0]
		} else {
			offset := m.nextModelPoolOffset(oauthModelAliasPoolKey(auth, requestedModel), len(pool))
			return rotateStrings(pool, offset)
		}
	} else {
		requestedModel = m.applyOAuthModelAlias(auth, requestedModel)
	}
	if pool := m.resolveAPIKeyUpstreamModelPool(auth, requestedModel); len(pool) > 0 {
		if len(pool) == 1 {
			return pool
		}
		offset := m.nextModelPoolOffset(apiKeyModelPoolKey(auth, requestedModel), len(pool))
		return rotateStrings(pool, offset)
	}
	if pool := m.resolveOpenAICompatUpstreamModelPool(auth, requestedModel); len(pool) > 0 {
		if len(pool) == 1 {
			return pool
		}
		offset := m.nextModelPoolOffset(openAICompatModelPoolKey(auth, requestedModel), len(pool))
		return rotateStrings(pool, offset)
	}
	resolved := m.applyAPIKeyModelAlias(auth, requestedModel)
	if strings.TrimSpace(resolved) == "" {
		resolved = requestedModel
	}
	return []string{resolved}
}

func (m *Manager) selectionModelForAuth(auth *Auth, routeModel string) string {
	requestedModel := rewriteModelForAuth(routeModel, auth)
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = strings.TrimSpace(routeModel)
	}
	resolvedModel := m.applyOAuthModelAlias(auth, requestedModel)
	if strings.TrimSpace(resolvedModel) == "" {
		resolvedModel = requestedModel
	}
	return resolvedModel
}

func (m *Manager) selectionModelKeyForAuth(auth *Auth, routeModel string) string {
	return canonicalModelKey(m.selectionModelForAuth(auth, routeModel))
}

func (m *Manager) stateModelForExecution(auth *Auth, routeModel, upstreamModel string, pooled bool) string {
	if auth != nil && auth.Attributes != nil {
		if homeModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey]); homeModel != "" {
			if resolved := strings.TrimSpace(upstreamModel); resolved != "" {
				return resolved
			}
			return homeModel
		}
	}
	stateModel := executionResultModel(routeModel, upstreamModel, pooled)
	selectionModel := m.selectionModelForAuth(auth, routeModel)
	if canonicalModelKey(selectionModel) == canonicalModelKey(upstreamModel) && strings.TrimSpace(selectionModel) != "" {
		return strings.TrimSpace(upstreamModel)
	}
	return stateModel
}

func executionResultModel(routeModel, upstreamModel string, pooled bool) string {
	if pooled {
		if resolved := strings.TrimSpace(upstreamModel); resolved != "" {
			return resolved
		}
	}
	if requested := strings.TrimSpace(routeModel); requested != "" {
		return requested
	}
	return strings.TrimSpace(upstreamModel)
}

func (m *Manager) filterExecutionModels(auth *Auth, routeModel string, candidates []string, pooled bool) []string {
	if len(candidates) == 0 {
		return nil
	}
	if isCodexAuth(auth) {
		return append([]string(nil), candidates...)
	}
	now := time.Now()
	out := make([]string, 0, len(candidates))
	for _, upstreamModel := range candidates {
		stateModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
		blocked, _, _ := isAuthBlockedForModel(auth, stateModel, now)
		probeActive := auth != nil && m.halfOpenProbeActive(auth.ID, stateModel, now)
		if blocked && !probeActive {
			continue
		}
		out = append(out, upstreamModel)
	}
	return out
}

type cooldownFallbackCandidate struct {
	auth     *Auth
	model    string
	next     time.Time
	priority int
	quota    bool
}

func (m *Manager) preparedExecutionModels(auth *Auth, routeModel string) ([]string, bool) {
	candidates := m.executionModelCandidates(auth, routeModel)
	pooled := len(candidates) > 1
	return m.filterExecutionModels(auth, routeModel, candidates, pooled), pooled
}

func (m *Manager) preparedExecutionModelsForRequest(auth *Auth, routeModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) ([]string, bool) {
	candidates := m.executionModelCandidates(auth, routeModel)
	pooled := len(candidates) > 1
	models := m.filterExecutionModels(auth, routeModel, candidates, pooled)
	models = filterMiniMaxM3RequiredExecutionModels(routeModel, req, opts, models)
	return models, pooled
}

func (m *Manager) prepareExecutionModels(auth *Auth, routeModel string) []string {
	models, _ := m.preparedExecutionModels(auth, routeModel)
	return models
}

func (m *Manager) availableAuthsForRouteModel(auths []*Auth, provider, routeModel string, now time.Time) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	spreadAcrossPriorities := selectorUsesSpread(m.selectorForAuths(auths))
	availableAll := make([]*Auth, 0, len(auths))
	availableByPriority := make(map[int][]*Auth)
	fallbackCandidates := make([]cooldownFallbackCandidate, 0, len(auths))
	cooldownCount := 0
	activeFallbackAvailable := false
	var earliest time.Time
	recordAvailable := func(candidate *Auth, checkModel string) {
		availableAll = append(availableAll, candidate)
		if spreadAcrossPriorities {
			return
		}
		priority := effectiveSelectionPriority(candidate, checkModel, now)
		availableByPriority[priority] = append(availableByPriority[priority], candidate)
	}
	recordTemporalBlock := func(candidate *Auth, checkModel string, next time.Time, quota bool) {
		cooldownCount++
		if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
			earliest = next
		}
		fallbackCandidates = append(fallbackCandidates, cooldownFallbackCandidate{
			auth:     candidate,
			model:    checkModel,
			next:     next,
			priority: effectiveSelectionPriority(candidate, checkModel, now),
			quota:    quota,
		})
	}
	for _, candidate := range auths {
		checkModel := m.selectionModelForAuth(candidate, routeModel)
		blocked, reason, next := isAuthBlockedForModel(candidate, checkModel, now)
		if !blocked {
			if m.halfOpenProbeActive(candidate.ID, checkModel, now) {
				activeFallbackAvailable = true
				recordAvailable(candidate, checkModel)
				continue
			}
			healthBlocked, healthNext := m.healthSelectionBlocked(candidate, checkModel, now)
			if healthBlocked {
				recordTemporalBlock(candidate, checkModel, healthNext, quotaCooldownForModel(candidate, checkModel))
				continue
			}
			recordAvailable(candidate, checkModel)
			continue
		}
		if (reason == blockReasonCooldown || reason == blockReasonOther) && !next.IsZero() {
			if m.halfOpenProbeActive(candidate.ID, checkModel, now) {
				activeFallbackAvailable = true
				recordAvailable(candidate, checkModel)
				continue
			}
			recordTemporalBlock(candidate, checkModel, next, reason == blockReasonCooldown)
		}
	}

	if activeFallbackAvailable && len(fallbackCandidates) > 0 {
		if fallback, _ := m.cooldownFallbackProbe(fallbackCandidates, now); fallback != nil {
			recordAvailable(fallback.auth, fallback.model)
		}
	}

	if spreadAcrossPriorities {
		if len(availableAll) == 0 {
			if cooldownCount == len(auths) && !earliest.IsZero() {
				if fallback, probeNext := m.cooldownFallbackProbe(fallbackCandidates, now); fallback != nil {
					return []*Auth{fallback.auth}, nil
				} else if !probeNext.IsZero() && probeNext.Before(earliest) {
					earliest = probeNext
				}
				providerForError := provider
				if providerForError == "mixed" {
					providerForError = ""
				}
				resetIn := earliest.Sub(now)
				if resetIn < 0 {
					resetIn = 0
				}
				return nil, newModelCooldownError(routeModel, providerForError, resetIn)
			}
			return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
		}
		if len(availableAll) > 1 {
			sort.Slice(availableAll, func(i, j int) bool { return availableAll[i].ID < availableAll[j].ID })
		}
		return availableAll, nil
	}

	if len(availableByPriority) == 0 {
		if cooldownCount == len(auths) && !earliest.IsZero() {
			if fallback, probeNext := m.cooldownFallbackProbe(fallbackCandidates, now); fallback != nil {
				return []*Auth{fallback.auth}, nil
			} else if !probeNext.IsZero() && probeNext.Before(earliest) {
				earliest = probeNext
			}
			providerForError := provider
			if providerForError == "mixed" {
				providerForError = ""
			}
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(routeModel, providerForError, resetIn)
		}
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}

	bestPriority := 0
	found := false
	for priority := range availableByPriority {
		if !found || priority > bestPriority {
			bestPriority = priority
			found = true
		}
	}

	available := availableByPriority[bestPriority]
	if len(available) > 1 {
		sort.Slice(available, func(i, j int) bool { return available[i].ID < available[j].ID })
	}
	return available, nil
}

func (m *Manager) cooldownFallbackProbe(candidates []cooldownFallbackCandidate, now time.Time) (*cooldownFallbackCandidate, time.Time) {
	if len(candidates) == 0 {
		return nil, time.Time{}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.next.IsZero() != right.next.IsZero() {
			return !left.next.IsZero()
		}
		if !left.next.Equal(right.next) {
			return left.next.Before(right.next)
		}
		if left.priority != right.priority {
			return left.priority > right.priority
		}
		leftID, rightID := "", ""
		if left.auth != nil {
			leftID = left.auth.ID
		}
		if right.auth != nil {
			rightID = right.auth.ID
		}
		return leftID < rightID
	})

	var probeNext time.Time
	for _, candidate := range candidates {
		if candidate.auth == nil {
			continue
		}
		interval, activeTTL := healthHalfOpenInterval, healthHalfOpenActiveTTL
		if candidate.quota {
			interval, activeTTL = quotaHalfOpenInterval, quotaHalfOpenActiveTTL
		}
		ok, next := m.reserveHalfOpenProbeWithWindow(candidate.auth.ID, candidate.model, now, interval, activeTTL)
		if ok {
			fallback := candidate
			return &fallback, time.Time{}
		}
		if !next.IsZero() && (probeNext.IsZero() || next.Before(probeNext)) {
			probeNext = next
		}
	}
	return nil, probeNext
}

func quotaCooldownForModel(auth *Auth, model string) bool {
	if auth == nil {
		return false
	}
	if model != "" && len(auth.ModelStates) > 0 {
		state, ok := auth.ModelStates[model]
		if (!ok || state == nil) && model != "" {
			baseModel := canonicalModelKey(model)
			if baseModel != "" && baseModel != model {
				state, ok = auth.ModelStates[baseModel]
			}
		}
		if ok && state != nil {
			return state.Quota.Exceeded
		}
	}
	return auth.Quota.Exceeded
}

func copyTriedMap(src map[string]struct{}) map[string]struct{} {
	if len(src) == 0 {
		return make(map[string]struct{})
	}
	out := make(map[string]struct{}, len(src))
	for key := range src {
		out[key] = struct{}{}
	}
	return out
}

func halfOpenProbeKey(authID, model string) string {
	return strings.TrimSpace(authID) + "\x00" + canonicalModelKey(model)
}

func (m *Manager) nextHalfOpenProbeAt(authID, model string) time.Time {
	if m == nil {
		return time.Time{}
	}
	key := halfOpenProbeKey(authID, model)
	if key == "\x00" {
		return time.Time{}
	}
	m.halfOpenProbeMu.Lock()
	defer m.halfOpenProbeMu.Unlock()
	nowTime := time.Now()
	m.pruneHalfOpenProbeStateLocked(nowTime)
	if activeUntil := m.halfOpenProbeActiveUntil[key]; !activeUntil.IsZero() && !activeUntil.After(nowTime) {
		delete(m.halfOpenProbeActiveUntil, key)
	}
	next := m.halfOpenProbeNext[key]
	if !next.IsZero() && !next.After(nowTime) {
		delete(m.halfOpenProbeNext, key)
		return time.Time{}
	}
	return next
}

func (m *Manager) reserveHalfOpenProbe(authID, model string, now time.Time) (bool, time.Time) {
	return m.reserveHalfOpenProbeWithWindow(authID, model, now, healthHalfOpenInterval, healthHalfOpenActiveTTL)
}

func (m *Manager) reserveHalfOpenProbeWithWindow(authID, model string, now time.Time, interval, activeTTL time.Duration) (bool, time.Time) {
	if m == nil {
		return true, time.Time{}
	}
	if interval <= 0 {
		interval = healthHalfOpenInterval
	}
	if activeTTL <= 0 {
		activeTTL = healthHalfOpenActiveTTL
	}
	key := halfOpenProbeKey(authID, model)
	if key == "\x00" {
		return true, time.Time{}
	}
	m.halfOpenProbeMu.Lock()
	defer m.halfOpenProbeMu.Unlock()
	m.pruneHalfOpenProbeStateLocked(now)
	if next := m.halfOpenProbeNext[key]; !next.IsZero() && next.After(now) {
		return false, next
	}
	next := now.Add(interval)
	m.halfOpenProbeNext[key] = next
	if m.halfOpenProbeActiveUntil == nil {
		m.halfOpenProbeActiveUntil = make(map[string]time.Time)
	}
	m.halfOpenProbeActiveUntil[key] = now.Add(activeTTL)
	return true, next
}

func (m *Manager) halfOpenProbeActive(authID, model string, now time.Time) bool {
	if m == nil {
		return false
	}
	key := halfOpenProbeKey(authID, model)
	if key == "\x00" {
		return false
	}
	m.halfOpenProbeMu.Lock()
	defer m.halfOpenProbeMu.Unlock()
	m.pruneHalfOpenProbeStateLocked(now)
	activeUntil := m.halfOpenProbeActiveUntil[key]
	if activeUntil.IsZero() {
		return false
	}
	if !activeUntil.After(now) {
		delete(m.halfOpenProbeActiveUntil, key)
		return false
	}
	return true
}

func (m *Manager) pruneHalfOpenProbeStateLocked(now time.Time) {
	if m == nil {
		return
	}
	if len(m.halfOpenProbeNext)+len(m.halfOpenProbeActiveUntil) <= halfOpenProbeStateLimit {
		return
	}
	for key, next := range m.halfOpenProbeNext {
		if next.IsZero() || !next.After(now) {
			delete(m.halfOpenProbeNext, key)
		}
	}
	for key, activeUntil := range m.halfOpenProbeActiveUntil {
		if activeUntil.IsZero() || !activeUntil.After(now) {
			delete(m.halfOpenProbeActiveUntil, key)
		}
	}
	for len(m.halfOpenProbeNext) > halfOpenProbeStateLimit {
		for key := range m.halfOpenProbeNext {
			delete(m.halfOpenProbeNext, key)
			break
		}
	}
	for len(m.halfOpenProbeActiveUntil) > halfOpenProbeStateLimit {
		for key := range m.halfOpenProbeActiveUntil {
			delete(m.halfOpenProbeActiveUntil, key)
			break
		}
	}
}

func healthRequiresHalfOpenProbe(auth *Auth, model string, now time.Time) bool {
	if isCodexAuth(auth) {
		return false
	}
	state := resolveHealthState(auth, model)
	switch state.BreakerState {
	case HealthBreakerHalfOpen:
		return true
	case HealthBreakerOpen:
		return !state.OpenUntil.IsZero() && !state.OpenUntil.After(now)
	default:
		return false
	}
}

func (m *Manager) healthSelectionBlocked(auth *Auth, model string, now time.Time) (bool, time.Time) {
	if isCodexAuth(auth) {
		return false, time.Time{}
	}
	state := resolveHealthState(auth, model)
	switch state.BreakerState {
	case HealthBreakerOpen:
		if !state.OpenUntil.IsZero() && state.OpenUntil.After(now) {
			return true, state.OpenUntil
		}
		fallthrough
	case HealthBreakerHalfOpen:
		if next := m.nextHalfOpenProbeAt(auth.ID, model); !next.IsZero() && next.After(now) {
			return true, next
		}
	}
	return false, time.Time{}
}

func selectionArgForSelector(selector Selector, routeModel string) string {
	if isBuiltInSelector(selector) {
		return ""
	}
	return routeModel
}

func authRoutingGroup(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		for _, key := range []string{"routing_group", "routing-group"} {
			if value := normalizeRoutingGroupKey(auth.Attributes[key]); value != "" {
				return value
			}
		}
		if value := normalizeRoutingGroupKey(auth.Attributes["compat_kind"]); value != "" {
			return value
		}
		if value := normalizeRoutingGroupKey(auth.Attributes["compat_name"]); value != "" {
			return value
		}
		if value := normalizeRoutingGroupKey(auth.Attributes["provider_key"]); value != "" {
			return value
		}
	}
	if value := normalizeRoutingGroupKey(auth.Prefix); value != "" {
		return value
	}
	return normalizeRoutingGroupKey(auth.Provider)
}

func commonRoutingGroup(auths []*Auth) string {
	group := ""
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		current := authRoutingGroup(auth)
		if current == "" {
			return ""
		}
		if group == "" {
			group = current
			continue
		}
		if group != current {
			return ""
		}
	}
	return group
}

func (m *Manager) routingGroupStrategies() map[string]string {
	if m == nil {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return nil
	}
	return NormalizeRoutingGroupStrategies(cfg.Routing.GroupStrategies)
}

func (m *Manager) routingProviderStrategies() map[string]string {
	if m == nil {
		return nil
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		return nil
	}
	return NormalizeRoutingProviderStrategies(cfg.Routing.ProviderStrategies)
}

func (m *Manager) hasRoutingGroupStrategies() bool {
	return len(m.routingGroupStrategies()) > 0
}

func (m *Manager) hasRoutingProviderStrategies() bool {
	return len(m.routingProviderStrategies()) > 0
}

func (m *Manager) hasRoutingStrategyOverrides() bool {
	return m.hasRoutingGroupStrategies() || m.hasRoutingProviderStrategies()
}

func commonProviderKey(auths []*Auth) string {
	providerKey := ""
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		current := normalizeRoutingGroupKey(auth.Provider)
		if current == "" {
			return ""
		}
		if providerKey == "" {
			providerKey = current
			continue
		}
		if providerKey != current {
			return ""
		}
	}
	return providerKey
}

func authProviderFamilyKey(auth *Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	if value := normalizeRoutingGroupKey(auth.Attributes["provider_family"]); value != "" {
		return value
	}
	for _, key := range []string{"provider_type", "provider-type"} {
		if value := normalizeRoutingGroupKey(auth.Attributes[key]); value != "" {
			return value
		}
	}
	if normalizeRoutingGroupKey(auth.Attributes["compat_name"]) != "" || normalizeRoutingGroupKey(auth.Attributes["provider_key"]) != "" {
		return "openai-compatibility"
	}
	return ""
}

func commonProviderFamilyKey(auths []*Auth) string {
	providerKey := ""
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		current := authProviderFamilyKey(auth)
		if current == "" {
			return ""
		}
		if providerKey == "" {
			providerKey = current
			continue
		}
		if providerKey != current {
			return ""
		}
	}
	return providerKey
}

func commonProviderStrategyKeys(auths []*Auth) []string {
	exact := commonProviderKey(auths)
	family := commonProviderFamilyKey(auths)
	keys := make([]string, 0, 2)
	if exact != "" {
		keys = append(keys, exact)
	}
	if family != "" && family != exact {
		keys = append(keys, family)
	}
	return keys
}

func (m *Manager) routingStrategyForAuths(auths []*Auth) (string, string, bool) {
	if overrides := m.routingGroupStrategies(); len(overrides) > 0 {
		group := commonRoutingGroup(auths)
		if group != "" {
			if strategy, ok := overrides[group]; ok {
				return "group:" + group, strategy, true
			}
		}
	}
	if overrides := m.routingProviderStrategies(); len(overrides) > 0 {
		for _, providerKey := range commonProviderStrategyKeys(auths) {
			if strategy, ok := overrides[providerKey]; ok {
				return "provider:" + providerKey, strategy, true
			}
		}
	}
	return "", "", false
}

func (m *Manager) selectorForStrategyGroup(group, strategy string) Selector {
	if m == nil {
		return SelectorForRoutingStrategy(strategy)
	}
	normalizedStrategy, ok := NormalizeRoutingStrategy(strategy)
	if !ok {
		return m.selector
	}
	group = normalizeRoutingGroupKey(group)
	if group == "" {
		return SelectorForRoutingStrategy(normalizedStrategy)
	}
	cacheKey := group + "\x00" + normalizedStrategy

	m.dynamicSelectorsMu.Lock()
	defer m.dynamicSelectorsMu.Unlock()
	if selector, ok := m.dynamicSelectors[cacheKey]; ok && selector != nil {
		return selector
	}

	var selector Selector = SelectorForRoutingStrategy(normalizedStrategy)
	if sessionSelector, ok := m.selector.(*SessionAffinitySelector); ok && sessionSelector != nil {
		ttl := time.Hour
		if sessionSelector.cache != nil && sessionSelector.cache.ttl > 0 {
			ttl = sessionSelector.cache.ttl
		}
		selector = NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
			Fallback: selector,
			TTL:      ttl,
		})
	}
	m.dynamicSelectors[cacheKey] = selector
	return selector
}

func (m *Manager) selectorForAuths(auths []*Auth) Selector {
	group, strategy, ok := m.routingStrategyForAuths(auths)
	if !ok {
		return m.selector
	}
	return m.selectorForStrategyGroup(group, strategy)
}

func (m *Manager) stopDynamicSelectors() {
	if m == nil {
		return
	}
	m.dynamicSelectorsMu.Lock()
	selectors := make([]Selector, 0, len(m.dynamicSelectors))
	for key, selector := range m.dynamicSelectors {
		if selector == nil {
			delete(m.dynamicSelectors, key)
			continue
		}
		selectors = append(selectors, selector)
	}
	m.dynamicSelectors = make(map[string]Selector)
	m.dynamicSelectorsMu.Unlock()

	for _, selector := range selectors {
		if stoppable, ok := selector.(StoppableSelector); ok {
			stoppable.Stop()
		}
	}
}

func (m *Manager) authSupportsRouteModel(registryRef *registry.ModelRegistry, auth *Auth, routeModel string) bool {
	if registryRef == nil || auth == nil {
		return true
	}
	routeKey := canonicalModelKey(routeModel)
	if routeKey == "" {
		return true
	}
	if registeredModels := registryRef.GetModelsForClient(auth.ID); len(registeredModels) == 0 {
		return !authRequiresRegisteredModels(auth)
	}
	if registryRef.ClientSupportsModel(auth.ID, routeKey) {
		return true
	}
	selectionKey := m.selectionModelKeyForAuth(auth, routeModel)
	return selectionKey != "" && selectionKey != routeKey && registryRef.ClientSupportsModel(auth.ID, selectionKey)
}

func authRequiresRegisteredModels(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if auth.Attributes != nil {
		if strings.EqualFold(strings.TrimSpace(auth.Attributes["auth_kind"]), "apikey") {
			return true
		}
	}
	accountKind, _ := auth.AccountInfo()
	return strings.EqualFold(accountKind, "api_key")
}

func discardStreamChunks(ch <-chan cliproxyexecutor.StreamChunk) {
	if ch == nil {
		return
	}
	go func() {
		for range ch {
		}
	}()
}

type streamBootstrapError struct {
	cause   error
	headers http.Header
}

func cloneHTTPHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	return headers.Clone()
}

func newStreamBootstrapError(err error, headers http.Header) error {
	if err == nil {
		return nil
	}
	return &streamBootstrapError{
		cause:   err,
		headers: cloneHTTPHeader(headers),
	}
}

func (e *streamBootstrapError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *streamBootstrapError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *streamBootstrapError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return cloneHTTPHeader(e.headers)
}

func streamErrorResult(headers http.Header, err error) *cliproxyexecutor.StreamResult {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Err: err}
	close(ch)
	return &cliproxyexecutor.StreamResult{
		Headers: cloneHTTPHeader(headers),
		Chunks:  ch,
	}
}

func readStreamBootstrap(ctx context.Context, ch <-chan cliproxyexecutor.StreamChunk) ([]cliproxyexecutor.StreamChunk, bool, error) {
	if ch == nil {
		return nil, true, nil
	}
	buffered := make([]cliproxyexecutor.StreamChunk, 0, 1)
	for {
		var (
			chunk cliproxyexecutor.StreamChunk
			ok    bool
		)
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case chunk, ok = <-ch:
			}
		} else {
			chunk, ok = <-ch
		}
		if !ok {
			return buffered, true, nil
		}
		if chunk.Err != nil {
			return nil, false, chunk.Err
		}
		buffered = append(buffered, chunk)
		if len(chunk.Payload) > 0 {
			return buffered, false, nil
		}
	}
}

func (m *Manager) wrapStreamResult(ctx context.Context, auth *Auth, provider, resultModel string, headers http.Header, buffered []cliproxyexecutor.StreamChunk, remaining <-chan cliproxyexecutor.StreamChunk, startedAt time.Time, releaseSlot func()) *cliproxyexecutor.StreamResult {
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		if releaseSlot != nil {
			defer releaseSlot()
		}
		var failed bool
		forward := true
		emit := func(chunk cliproxyexecutor.StreamChunk) bool {
			if chunk.Err != nil && !failed {
				failed = true
				rerr := resultErrorFromCause(chunk.Err)
				m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Duration: time.Since(startedAt), Error: rerr, Cause: chunk.Err})
				if shouldEvictUnauthorizedResult(rerr) {
					if errEvict := m.evictUnauthorizedAuth(ctx, auth, provider, resultModel); errEvict != nil {
						logEntryWithRequestID(ctx).Warnf("evict unauthorized auth %s failed: %v", auth.ID, errEvict)
					}
				}
			}
			if !forward {
				return false
			}
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				forward = false
				return false
			case out <- chunk:
				return true
			}
		}
		for _, chunk := range buffered {
			if ok := emit(chunk); !ok {
				discardStreamChunks(remaining)
				return
			}
		}
		for chunk := range remaining {
			if ok := emit(chunk); !ok {
				discardStreamChunks(remaining)
				return
			}
		}
		if !failed {
			m.MarkResult(ctx, Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: true, Duration: time.Since(startedAt)})
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}
}

func (m *Manager) executeStreamWithModelPool(ctx context.Context, executor ProviderExecutor, auth *Auth, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel string, execModels []string, pooled bool) (*cliproxyexecutor.StreamResult, error) {
	if executor == nil {
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	ctx = contextWithRequestedModelAlias(ctx, opts, routeModel)
	var lastErr error
	for idx, execModel := range execModels {
		resultModel := m.stateModelForExecution(auth, routeModel, execModel, pooled)
		execReq := req
		execReq.Model = execModel
		releaseSlot, errReserve := m.reserveCodexModelSlot(provider, resultModel)
		if errReserve != nil {
			m.markSelectorLoadDone(auth.ID, resultModel)
			return nil, errReserve
		}
		startedAt := time.Now()
		streamResult, errStream := executor.ExecuteStream(ctx, auth, execReq, opts)
		if errStream != nil {
			releaseSlot()
			if errCtx := ctx.Err(); errCtx != nil {
				return nil, errCtx
			}
			rerr := resultErrorFromCause(errStream)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Duration: time.Since(startedAt), Error: rerr, Cause: errStream}
			result.RetryAfter = retryAfterFromError(errStream)
			m.MarkResult(ctx, result)
			m.recordContentSafetyRequest(ctx, auth, provider, routeModel, execModel, opts, req.Payload, errStream)
			if shouldEvictUnauthorizedError(errStream) {
				return nil, errStream
			}
			if isRequestInvalidError(errStream) {
				if shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, errStream) {
					lastErr = errStream
					continue
				}
				return nil, errStream
			}
			lastErr = errStream
			continue
		}

		buffered, closed, bootstrapErr := readStreamBootstrap(ctx, streamResult.Chunks)
		if bootstrapErr != nil {
			if errCtx := ctx.Err(); errCtx != nil {
				discardStreamChunks(streamResult.Chunks)
				releaseSlot()
				return nil, errCtx
			}
			if isRequestInvalidError(bootstrapErr) {
				rerr := resultErrorFromCause(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Duration: time.Since(startedAt), Error: rerr, Cause: bootstrapErr}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				m.MarkResult(ctx, result)
				m.recordContentSafetyRequest(ctx, auth, provider, routeModel, execModel, opts, req.Payload, bootstrapErr)
				discardStreamChunks(streamResult.Chunks)
				releaseSlot()
				if shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, bootstrapErr) {
					lastErr = bootstrapErr
					continue
				}
				return nil, bootstrapErr
			}
			if shouldEvictUnauthorizedError(bootstrapErr) {
				rerr := resultErrorFromCause(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Duration: time.Since(startedAt), Error: rerr, Cause: bootstrapErr}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				m.MarkResult(ctx, result)
				discardStreamChunks(streamResult.Chunks)
				releaseSlot()
				return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
			}
			if idx < len(execModels)-1 {
				rerr := resultErrorFromCause(bootstrapErr)
				result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Duration: time.Since(startedAt), Error: rerr, Cause: bootstrapErr}
				result.RetryAfter = retryAfterFromError(bootstrapErr)
				m.MarkResult(ctx, result)
				discardStreamChunks(streamResult.Chunks)
				releaseSlot()
				lastErr = bootstrapErr
				continue
			}
			rerr := resultErrorFromCause(bootstrapErr)
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Duration: time.Since(startedAt), Error: rerr, Cause: bootstrapErr}
			result.RetryAfter = retryAfterFromError(bootstrapErr)
			m.MarkResult(ctx, result)
			discardStreamChunks(streamResult.Chunks)
			releaseSlot()
			return nil, newStreamBootstrapError(bootstrapErr, streamResult.Headers)
		}

		if closed && len(buffered) == 0 {
			emptyErr := &Error{Code: "empty_stream", Message: "upstream stream closed before first payload", Retryable: true}
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: false, Duration: time.Since(startedAt), Error: emptyErr}
			m.MarkResult(ctx, result)
			releaseSlot()
			if idx < len(execModels)-1 {
				lastErr = emptyErr
				continue
			}
			return nil, newStreamBootstrapError(emptyErr, streamResult.Headers)
		}

		remaining := streamResult.Chunks
		if closed {
			closedCh := make(chan cliproxyexecutor.StreamChunk)
			close(closedCh)
			remaining = closedCh
		}
		return m.wrapStreamResult(ctx, auth.Clone(), provider, resultModel, streamResult.Headers, buffered, remaining, startedAt, releaseSlot), nil
	}
	if lastErr == nil {
		lastErr = &Error{Code: "auth_not_found", Message: "no upstream model available"}
	}
	return nil, lastErr
}

func (m *Manager) rebuildAPIKeyModelAliasFromRuntimeConfig() {
	if m == nil {
		return
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rebuildAPIKeyModelAliasLocked(cfg)
}

func (m *Manager) rebuildAPIKeyModelAliasLocked(cfg *internalconfig.Config) {
	if m == nil {
		return
	}
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	out := make(apiKeyModelAliasTable)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if strings.TrimSpace(auth.ID) == "" {
			continue
		}
		if auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		kind, _ := auth.AccountInfo()
		if !strings.EqualFold(strings.TrimSpace(kind), "api_key") {
			continue
		}

		byAlias := make(map[string]string)
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		switch provider {
		case "gemini":
			if entry := resolveGeminiAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
			}
		case "claude":
			if entry := resolveClaudeAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
			}
		case "codex":
			if entry := resolveCodexAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
			}
		case "vertex":
			if entry := resolveVertexAPIKeyConfig(cfg, auth); entry != nil {
				compileAPIKeyModelAliasForModels(byAlias, entry.Models)
			}
		default:
			// OpenAI-compat uses config selection from auth.Attributes.
			providerKey := ""
			compatName := ""
			if auth.Attributes != nil {
				providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
				compatName = strings.TrimSpace(auth.Attributes["compat_name"])
			}
			if compatName != "" || strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
				if entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider); entry != nil {
					compileAPIKeyModelAliasForModels(byAlias, entry.Models)
				}
			}
		}

		if len(byAlias) > 0 {
			out[auth.ID] = byAlias
		}
	}

	m.apiKeyModelAlias.Store(out)
}

func compileAPIKeyModelAliasForModels[T interface {
	GetName() string
	GetAlias() string
}](out map[string]string, models []T) {
	if out == nil {
		return
	}
	for i := range models {
		alias := strings.TrimSpace(models[i].GetAlias())
		name := strings.TrimSpace(models[i].GetName())
		if alias == "" || name == "" {
			continue
		}
		aliasKey := strings.ToLower(thinking.ParseSuffix(alias).ModelName)
		if aliasKey == "" {
			aliasKey = strings.ToLower(alias)
		}
		// Config priority: first alias wins.
		if _, exists := out[aliasKey]; exists {
			continue
		}
		out[aliasKey] = name
		// Also allow direct lookup by upstream name (case-insensitive), so lookups on already-upstream
		// models remain a cheap no-op.
		nameKey := strings.ToLower(thinking.ParseSuffix(name).ModelName)
		if nameKey == "" {
			nameKey = strings.ToLower(name)
		}
		if nameKey != "" {
			if _, exists := out[nameKey]; !exists {
				out[nameKey] = name
			}
		}
		// Preserve config suffix priority by seeding a base-name lookup when name already has suffix.
		nameResult := thinking.ParseSuffix(name)
		if nameResult.HasSuffix {
			baseKey := strings.ToLower(strings.TrimSpace(nameResult.ModelName))
			if baseKey != "" {
				if _, exists := out[baseKey]; !exists {
					out[baseKey] = name
				}
			}
		}
	}
}

// SetRetryConfig updates retry attempts, credential retry limit and cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration, maxRetryCredentials int) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryCredentials < 0 {
		maxRetryCredentials = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryCredentials.Store(int32(maxRetryCredentials))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// SetRetryQueueDelay updates the delay inserted before fallback credential retries.
func (m *Manager) SetRetryQueueDelay(delay time.Duration) {
	if m == nil {
		return
	}
	if delay < 0 {
		delay = 0
	}
	m.retryQueueDelay.Store(delay.Nanoseconds())
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	provider := strings.TrimSpace(executor.Identifier())
	if provider == "" {
		return
	}

	var replaced ProviderExecutor
	m.mu.Lock()
	replaced = m.executors[provider]
	m.executors[provider] = executor
	m.mu.Unlock()

	if replaced == nil || replaced == executor {
		return
	}
	if closer, ok := replaced.(ExecutionSessionCloser); ok && closer != nil {
		closer.CloseExecutionSession(CloseAllExecutionSessionsID)
	}
}

// UnregisterExecutor removes the executor associated with the provider key.
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	delete(m.executors, provider)
	m.mu.Unlock()
}

// Register inserts a new auth entry into the manager.
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	auth.EnsureIndex()
	if err := m.persist(ctx, auth); err != nil {
		return nil, err
	}
	authClone := auth.Clone()
	m.mu.Lock()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone)
	}
	m.queueRefreshReschedule(auth.ID)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	m.mu.Lock()
	if existing, ok := m.auths[auth.ID]; ok && existing != nil {
		if !auth.indexAssigned && auth.Index == "" {
			auth.Index = existing.Index
			auth.indexAssigned = existing.indexAssigned
		}
		auth.Success = existing.Success
		auth.Failed = existing.Failed
		auth.recentRequests = existing.recentRequests
		if !existing.Disabled && existing.Status != StatusDisabled && !auth.Disabled && auth.Status != StatusDisabled {
			if len(auth.ModelStates) == 0 && len(existing.ModelStates) > 0 {
				auth.ModelStates = existing.ModelStates
			}
		}
	}
	auth.EnsureIndex()
	m.mu.Unlock()
	if err := m.persist(ctx, auth); err != nil {
		return nil, err
	}
	authClone := auth.Clone()
	m.mu.Lock()
	m.auths[auth.ID] = authClone
	m.mu.Unlock()
	m.rebuildAPIKeyModelAliasFromRuntimeConfig()
	if m.scheduler != nil {
		m.scheduler.upsertAuth(authClone)
	}
	m.queueRefreshReschedule(auth.ID)
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Delete removes an auth entry from runtime state and persistent storage when applicable.
func (m *Manager) Delete(ctx context.Context, authID string) error {
	return m.evictAuth(ctx, authID)
}

// Load resets manager state from the backing store.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	if m.store == nil {
		m.mu.Unlock()
		return nil
	}
	items, err := m.store.List(ctx)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	m.auths = make(map[string]*Auth, len(items))
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		auth.EnsureIndex()
		m.auths[auth.ID] = auth.Clone()
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}
	m.rebuildAPIKeyModelAliasLocked(cfg)
	m.mu.Unlock()
	m.syncScheduler()
	return nil
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) Execute(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx, trace := ensureRequestAttemptTrace(ctx)
	finalSuccess := false
	defer func() {
		coreusage.PublishRequestFinal(ctx, coreusage.RequestFinal{
			RequestID:    trace.requestIDValue(),
			FinalSuccess: finalSuccess,
			AttemptCount: trace.attemptCount(),
			CompletedAt:  time.Now(),
		})
	}()
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	_, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, errExec := m.executeMixedOnce(ctx, normalized, req, opts, maxRetryCredentials)
		if errExec == nil {
			finalSuccess = true
			return resp, nil
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, normalized, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if wait <= 0 {
			wait = m.retryQueueWait()
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		if hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, lastErr, normalized) {
			if resp, ok := m.tryAntigravityCreditsExecute(ctx, req, opts); ok {
				finalSuccess = true
				return resp, nil
			}
		}
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	ctx, trace := ensureRequestAttemptTrace(ctx)
	finalSuccess := false
	defer func() {
		coreusage.PublishRequestFinal(ctx, coreusage.RequestFinal{
			RequestID:    trace.requestIDValue(),
			FinalSuccess: finalSuccess,
			AttemptCount: trace.attemptCount(),
			CompletedAt:  time.Now(),
		})
	}()
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	_, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	for attempt := 0; ; attempt++ {
		resp, errExec := m.executeCountMixedOnce(ctx, normalized, req, opts, maxRetryCredentials)
		if errExec == nil {
			finalSuccess = true
			return resp, nil
		}
		lastErr = errExec
		wait, shouldRetry := m.shouldRetryAfterError(errExec, attempt, normalized, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if wait <= 0 {
			wait = m.retryQueueWait()
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return cliproxyexecutor.Response{}, errWait
		}
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model and round-robins the starting provider per model.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ctx, trace := ensureRequestAttemptTrace(ctx)
	finalSuccess := false
	defer func() {
		coreusage.PublishRequestFinal(ctx, coreusage.RequestFinal{
			RequestID:    trace.requestIDValue(),
			FinalSuccess: finalSuccess,
			AttemptCount: trace.attemptCount(),
			CompletedAt:  time.Now(),
		})
	}()
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	_, maxRetryCredentials, maxWait := m.retrySettings()

	var lastErr error
	for attempt := 0; ; attempt++ {
		result, errStream := m.executeStreamMixedOnce(ctx, normalized, req, opts, maxRetryCredentials)
		if errStream == nil {
			finalSuccess = true
			return result, nil
		}
		lastErr = errStream
		wait, shouldRetry := m.shouldRetryAfterError(errStream, attempt, normalized, req.Model, maxWait)
		if !shouldRetry {
			break
		}
		if wait <= 0 {
			wait = m.retryQueueWait()
		}
		if errWait := waitForCooldown(ctx, wait); errWait != nil {
			return nil, errWait
		}
	}
	if lastErr != nil {
		if hasAntigravityProvider(normalized) && shouldAttemptAntigravityCreditsFallback(m, lastErr, normalized) {
			if result, ok := m.tryAntigravityCreditsExecuteStream(ctx, req, opts); ok {
				finalSuccess = true
				return result, nil
			}
		}
		var bootstrapErr *streamBootstrapError
		if errors.As(lastErr, &bootstrapErr) && bootstrapErr != nil {
			return streamErrorResult(bootstrapErr.Headers(), bootstrapErr.cause), nil
		}
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

func (m *Manager) executeMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := req.Model
	opts = ensureRequestedModelMetadata(opts, routeModel)
	fallbackGuard := newGPTLargeToolHistoryFallbackGuard(providers, routeModel, opts)
	maxRetryCredentials = fallbackGuard.effectiveMaxRetryCredentials(maxRetryCredentials)
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	attempted := make(map[string]struct{})
	trace := requestAttemptTraceFromContext(ctx)
	nextRetryReason := ""
	var lastErr error
	for {
		if !homeMode && maxRetryCredentials > 0 && len(attempted) > maxRetryCredentials &&
			!shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, lastErr) {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeAuthCount(opts, homeAuthCount)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick != nil {
			m.logAuthSelectionFailureMetric(ctx, providers, routeModel, errPick)
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errPick
		}
		tried[auth.ID] = struct{}{}
		if fallbackGuard.shouldSkipAuth(auth) {
			continue
		}
		fallbackGuard.markAuth(auth)

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, req.Model)
		m.logAuthSelectionMetric(ctx, auth, provider, routeModel)
		publishSelectedAuthMetadata(opts.Metadata, auth.ID)

		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = contextWithSelectedAuthRoutingGroup(execCtx, auth)
		if trace != nil {
			execCtx = coreusage.WithRequestAttempt(execCtx, trace.nextAttempt(nextRetryReason))
			nextRetryReason = ""
		}

		models, pooled := m.preparedExecutionModelsForRequest(auth, routeModel, req, opts)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		var errPrepare error
		auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			result := Result{AuthID: auth.ID, Provider: provider, Model: routeModel, Success: false, Error: resultErrorFromCause(errPrepare)}
			m.MarkResult(execCtx, result)
			lastErr = errPrepare
			nextRetryReason = retryReasonFromError(errPrepare)
			continue
		}
		var authErr error
		countAttempt := false
		for _, upstreamModel := range models {
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			releaseSlot, errReserve := m.reserveCodexModelSlot(provider, resultModel)
			if errReserve != nil {
				m.markSelectorLoadDone(auth.ID, resultModel)
				return cliproxyexecutor.Response{}, errReserve
			}
			startedAt := time.Now()
			resp, errExec := executor.Execute(execCtx, auth, execReq, opts)
			duration := time.Since(startedAt)
			releaseSlot()
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: errExec == nil, Duration: duration}
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, errCtx
				}
				result.Error = resultErrorFromCause(errExec)
				result.Cause = errExec
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				m.MarkResult(execCtx, result)
				m.recordContentSafetyRequest(execCtx, auth, provider, routeModel, upstreamModel, opts, req.Payload, errExec)
				if shouldEvictUnauthorizedError(errExec) {
					if errEvict := m.evictUnauthorizedAuth(execCtx, auth, provider, resultModel); errEvict != nil {
						logEntryWithRequestID(execCtx).Warnf("evict unauthorized auth %s failed: %v", auth.ID, errEvict)
					}
					authErr = errExec
					countAttempt = false
					break
				}
				if isRequestInvalidError(errExec) {
					if shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, errExec) {
						authErr = errExec
						countAttempt = true
						continue
					}
					return cliproxyexecutor.Response{}, errExec
				}
				authErr = errExec
				countAttempt = true
				continue
			}
			m.MarkResult(execCtx, result)
			return resp, nil
		}
		if authErr != nil {
			routeFallback := shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, authErr)
			transientNetworkFallback := isTransientRoutingError(authErr)
			emptyUpstreamFallback := isRetryableEmptyUpstreamResponseError(authErr)
			if isRequestInvalidError(authErr) {
				if !routeFallback {
					return cliproxyexecutor.Response{}, authErr
				}
			}
			if countAttempt {
				attempted[auth.ID] = struct{}{}
			}
			lastErr = authErr
			nextRetryReason = retryReasonFromError(authErr)
			if homeMode {
				homeAuthCount++
			} else if !routeFallback && !transientNetworkFallback && !emptyUpstreamFallback {
				if errWait := m.waitForRetryQueue(ctx); errWait != nil {
					return cliproxyexecutor.Response{}, errWait
				}
			}
			continue
		}
	}
}

func (m *Manager) executeCountMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := req.Model
	opts = ensureRequestedModelMetadata(opts, routeModel)
	fallbackGuard := newGPTLargeToolHistoryFallbackGuard(providers, routeModel, opts)
	maxRetryCredentials = fallbackGuard.effectiveMaxRetryCredentials(maxRetryCredentials)
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	attempted := make(map[string]struct{})
	trace := requestAttemptTraceFromContext(ctx)
	nextRetryReason := ""
	var lastErr error
	for {
		if !homeMode && maxRetryCredentials > 0 && len(attempted) > maxRetryCredentials &&
			!shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, lastErr) {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeAuthCount(opts, homeAuthCount)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick != nil {
			m.logAuthSelectionFailureMetric(ctx, providers, routeModel, errPick)
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errPick
		}
		tried[auth.ID] = struct{}{}
		if fallbackGuard.shouldSkipAuth(auth) {
			continue
		}
		fallbackGuard.markAuth(auth)

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, req.Model)
		m.logAuthSelectionMetric(ctx, auth, provider, routeModel)
		publishSelectedAuthMetadata(opts.Metadata, auth.ID)

		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = contextWithSelectedAuthRoutingGroup(execCtx, auth)
		if trace != nil {
			execCtx = coreusage.WithRequestAttempt(execCtx, trace.nextAttempt(nextRetryReason))
			nextRetryReason = ""
		}

		models, pooled := m.preparedExecutionModelsForRequest(auth, routeModel, req, opts)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		var errPrepare error
		auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			result := Result{AuthID: auth.ID, Provider: provider, Model: routeModel, Success: false, Error: resultErrorFromCause(errPrepare)}
			m.MarkResult(execCtx, result)
			lastErr = errPrepare
			nextRetryReason = retryReasonFromError(errPrepare)
			continue
		}
		var authErr error
		countAttempt := false
		for _, upstreamModel := range models {
			resultModel := m.stateModelForExecution(auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			releaseSlot, errReserve := m.reserveCodexModelSlot(provider, resultModel)
			if errReserve != nil {
				m.markSelectorLoadDone(auth.ID, resultModel)
				return cliproxyexecutor.Response{}, errReserve
			}
			startedAt := time.Now()
			resp, errExec := executor.CountTokens(execCtx, auth, execReq, opts)
			duration := time.Since(startedAt)
			releaseSlot()
			result := Result{AuthID: auth.ID, Provider: provider, Model: resultModel, Success: errExec == nil, Duration: duration}
			if errExec != nil {
				if errCtx := execCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, errCtx
				}
				result.Error = resultErrorFromCause(errExec)
				result.Cause = errExec
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				m.MarkResult(execCtx, result)
				m.recordContentSafetyRequest(execCtx, auth, provider, routeModel, upstreamModel, opts, req.Payload, errExec)
				if shouldEvictUnauthorizedError(errExec) {
					if errEvict := m.evictUnauthorizedAuth(execCtx, auth, provider, resultModel); errEvict != nil {
						logEntryWithRequestID(execCtx).Warnf("evict unauthorized auth %s failed: %v", auth.ID, errEvict)
					}
					authErr = errExec
					countAttempt = false
					break
				}
				if isRequestInvalidError(errExec) {
					if shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, errExec) {
						authErr = errExec
						countAttempt = true
						continue
					}
					return cliproxyexecutor.Response{}, errExec
				}
				authErr = errExec
				countAttempt = true
				continue
			}
			m.MarkResult(execCtx, result)
			return resp, nil
		}
		if authErr != nil {
			routeFallback := shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, authErr)
			transientNetworkFallback := isTransientRoutingError(authErr)
			emptyUpstreamFallback := isRetryableEmptyUpstreamResponseError(authErr)
			if isRequestInvalidError(authErr) {
				if !routeFallback {
					return cliproxyexecutor.Response{}, authErr
				}
			}
			if countAttempt {
				attempted[auth.ID] = struct{}{}
			}
			lastErr = authErr
			nextRetryReason = retryReasonFromError(authErr)
			if homeMode {
				homeAuthCount++
			} else if !routeFallback && !transientNetworkFallback && !emptyUpstreamFallback {
				if errWait := m.waitForRetryQueue(ctx); errWait != nil {
					return cliproxyexecutor.Response{}, errWait
				}
			}
			continue
		}
	}
}

func (m *Manager) executeStreamMixedOnce(ctx context.Context, providers []string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, maxRetryCredentials int) (*cliproxyexecutor.StreamResult, error) {
	if len(providers) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	routeModel := req.Model
	opts = ensureRequestedModelMetadata(opts, routeModel)
	fallbackGuard := newGPTLargeToolHistoryFallbackGuard(providers, routeModel, opts)
	maxRetryCredentials = fallbackGuard.effectiveMaxRetryCredentials(maxRetryCredentials)
	homeMode := m.HomeEnabled()
	homeAuthCount := 1
	tried := make(map[string]struct{})
	attempted := make(map[string]struct{})
	trace := requestAttemptTraceFromContext(ctx)
	nextRetryReason := ""
	var lastErr error
	for {
		if !homeMode && maxRetryCredentials > 0 && len(attempted) > maxRetryCredentials &&
			!shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, lastErr) {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		pickOpts := opts
		if homeMode {
			pickOpts = withHomeAuthCount(opts, homeAuthCount)
		}
		auth, executor, provider, errPick := m.pickNextMixed(ctx, providers, routeModel, pickOpts, tried)
		if errPick != nil {
			m.logAuthSelectionFailureMetric(ctx, providers, routeModel, errPick)
			if shouldReturnLastErrorOnPickFailure(homeMode, lastErr, errPick) {
				return nil, lastErr
			}
			return nil, errPick
		}
		tried[auth.ID] = struct{}{}
		if fallbackGuard.shouldSkipAuth(auth) {
			continue
		}
		fallbackGuard.markAuth(auth)

		entry := logEntryWithRequestID(ctx)
		debugLogAuthSelection(entry, auth, provider, req.Model)
		m.logAuthSelectionMetric(ctx, auth, provider, routeModel)
		publishSelectedAuthMetadata(opts.Metadata, auth.ID)

		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		execCtx = contextWithRequestedModelAlias(execCtx, opts, routeModel)
		execCtx = contextWithSelectedAuthRoutingGroup(execCtx, auth)
		if trace != nil {
			execCtx = coreusage.WithRequestAttempt(execCtx, trace.nextAttempt(nextRetryReason))
			nextRetryReason = ""
		}
		models, pooled := m.preparedExecutionModelsForRequest(auth, routeModel, req, opts)
		if len(models) == 0 {
			continue
		}
		attempted[auth.ID] = struct{}{}
		var errPrepare error
		auth, errPrepare = m.prepareRequestAuth(execCtx, executor, auth)
		if errPrepare != nil {
			result := Result{AuthID: auth.ID, Provider: provider, Model: routeModel, Success: false, Error: resultErrorFromCause(errPrepare)}
			m.MarkResult(execCtx, result)
			lastErr = errPrepare
			nextRetryReason = retryReasonFromError(errPrepare)
			continue
		}
		streamResult, errStream := m.executeStreamWithModelPool(execCtx, executor, auth, provider, req, opts, routeModel, models, pooled)
		if errStream != nil {
			if errCtx := execCtx.Err(); errCtx != nil {
				return nil, errCtx
			}
			if shouldEvictUnauthorizedError(errStream) {
				if errEvict := m.evictUnauthorizedAuth(execCtx, auth, provider, routeModel); errEvict != nil {
					logEntryWithRequestID(execCtx).Warnf("evict unauthorized auth %s failed: %v", auth.ID, errEvict)
				}
				lastErr = errStream
				nextRetryReason = retryReasonFromError(errStream)
				if errWait := m.waitForRetryQueue(ctx); errWait != nil {
					return nil, errWait
				}
				continue
			}
			routeFallback := shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, errStream)
			transientNetworkFallback := isTransientRoutingError(errStream)
			emptyUpstreamFallback := isRetryableEmptyUpstreamResponseError(errStream)
			if isRequestInvalidError(errStream) {
				if !routeFallback {
					return nil, errStream
				}
			}
			attempted[auth.ID] = struct{}{}
			lastErr = errStream
			nextRetryReason = retryReasonFromError(errStream)
			if homeMode {
				homeAuthCount++
			} else if !routeFallback && !transientNetworkFallback && !emptyUpstreamFallback {
				if errWait := m.waitForRetryQueue(ctx); errWait != nil {
					return nil, errWait
				}
			}
			continue
		}
		return streamResult, nil
	}
}

func ensureRequestedModelMetadata(opts cliproxyexecutor.Options, requestedModel string) cliproxyexecutor.Options {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return opts
	}
	if hasRequestedModelMetadata(opts.Metadata) {
		return opts
	}
	if len(opts.Metadata) == 0 {
		opts.Metadata = map[string]any{cliproxyexecutor.RequestedModelMetadataKey: requestedModel}
		return opts
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[cliproxyexecutor.RequestedModelMetadataKey] = requestedModel
	opts.Metadata = meta
	return opts
}

func withHomeAuthCount(opts cliproxyexecutor.Options, count int) cliproxyexecutor.Options {
	if count <= 0 {
		count = 1
	}
	meta := make(map[string]any, len(opts.Metadata)+1)
	for k, v := range opts.Metadata {
		meta[k] = v
	}
	meta[homeAuthCountMetadataKey] = count
	opts.Metadata = meta
	return opts
}

func homeAuthCountFromMetadata(meta map[string]any) int {
	if len(meta) == 0 {
		return 1
	}
	switch value := meta[homeAuthCountMetadataKey].(type) {
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 {
			return int(value)
		}
	}
	return 1
}

func hasRequestedModelMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case []byte:
		return strings.TrimSpace(string(v)) != ""
	default:
		return false
	}
}

type requestAuthPrepareLock struct {
	mu sync.Mutex
}

func (m *Manager) prepareRequestAuth(ctx context.Context, executor ProviderExecutor, auth *Auth) (*Auth, error) {
	if m == nil || executor == nil || auth == nil {
		return auth, nil
	}
	preparer, ok := executor.(RequestAuthPreparer)
	if !ok || preparer == nil || !preparer.ShouldPrepareRequestAuth(auth) {
		return auth, nil
	}

	id := strings.TrimSpace(auth.ID)
	if id == "" {
		return preparer.PrepareRequestAuth(ctx, auth.Clone())
	}

	lockValue, _ := m.requestPrepareLocks.LoadOrStore(id, &requestAuthPrepareLock{})
	lock, ok := lockValue.(*requestAuthPrepareLock)
	if !ok || lock == nil {
		return preparer.PrepareRequestAuth(ctx, auth.Clone())
	}

	lock.mu.Lock()
	defer lock.mu.Unlock()

	target := auth.Clone()
	m.mu.RLock()
	if current := m.auths[id]; current != nil {
		target = current.Clone()
	}
	m.mu.RUnlock()

	if !preparer.ShouldPrepareRequestAuth(target) {
		return target, nil
	}

	updated, errPrepare := preparer.PrepareRequestAuth(ctx, target)
	if errPrepare != nil {
		return auth, errPrepare
	}
	if updated == nil {
		return target, nil
	}

	saved, errUpdate := m.Update(ctx, updated)
	if errUpdate != nil {
		return updated, errUpdate
	}
	if saved != nil {
		return saved, nil
	}
	return updated, nil
}

func contextWithRequestedModelAlias(ctx context.Context, opts cliproxyexecutor.Options, fallback string) context.Context {
	alias := requestedModelAliasFromOptions(opts, fallback)
	ctx = coreusage.WithRequestedModelAlias(ctx, alias)
	if effort := reasoningEffortFromOptions(opts); effort != "" {
		ctx = coreusage.WithReasoningEffort(ctx, effort)
	}
	ctx = coreusage.WithRequestShape(ctx, requestShapeFromOptions(opts))
	return ctx
}

func requestedModelAliasFromOptions(opts cliproxyexecutor.Options, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	if len(opts.Metadata) == 0 {
		return fallback
	}
	raw, ok := opts.Metadata[cliproxyexecutor.RequestedModelMetadataKey]
	if !ok || raw == nil {
		return fallback
	}
	switch value := raw.(type) {
	case string:
		if strings.TrimSpace(value) == "" {
			return fallback
		}
		return strings.TrimSpace(value)
	case []byte:
		if len(value) == 0 {
			return fallback
		}
		return strings.TrimSpace(string(value))
	default:
		return fallback
	}
}

func reasoningEffortFromOptions(opts cliproxyexecutor.Options) string {
	if len(opts.Metadata) == 0 {
		return ""
	}
	raw, ok := opts.Metadata[cliproxyexecutor.ReasoningEffortMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func requestShapeFromOptions(opts cliproxyexecutor.Options) coreusage.RequestShape {
	if len(opts.Metadata) == 0 {
		return coreusage.RequestShape{}
	}
	return coreusage.RequestShape{
		MessageCount: intMetadataValue(opts.Metadata[cliproxyexecutor.MessageCountMetadataKey]),
		ToolCount:    intMetadataValue(opts.Metadata[cliproxyexecutor.ToolCountMetadataKey]),
	}
}

func intMetadataValue(raw any) int {
	switch value := raw.(type) {
	case int:
		if value > 0 {
			return value
		}
	case int32:
		if value > 0 {
			return int(value)
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float32:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 {
			return int(value)
		}
	case string:
		parsed, errParse := strconv.Atoi(strings.TrimSpace(value))
		if errParse == nil && parsed > 0 {
			return parsed
		}
	case []byte:
		parsed, errParse := strconv.Atoi(strings.TrimSpace(string(value)))
		if errParse == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func pinnedAuthIDFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[cliproxyexecutor.PinnedAuthMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch val := raw.(type) {
	case string:
		return strings.TrimSpace(val)
	case []byte:
		return strings.TrimSpace(string(val))
	default:
		return ""
	}
}

func disallowFreeAuthFromMetadata(meta map[string]any) bool {
	if len(meta) == 0 {
		return false
	}
	raw, ok := meta[cliproxyexecutor.DisallowFreeAuthMetadataKey]
	if !ok || raw == nil {
		return false
	}
	switch val := raw.(type) {
	case bool:
		return val
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(val))
		return err == nil && parsed
	case []byte:
		parsed, err := strconv.ParseBool(strings.TrimSpace(string(val)))
		return err == nil && parsed
	default:
		return false
	}
}

func isFreeCodexAuth(auth *Auth) bool {
	if auth == nil || auth.Attributes == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["plan_type"]), "free")
}

func publishSelectedAuthMetadata(meta map[string]any, authID string) {
	if len(meta) == 0 {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	meta[cliproxyexecutor.SelectedAuthMetadataKey] = authID
	if callback, ok := meta[cliproxyexecutor.SelectedAuthCallbackMetadataKey].(func(string)); ok && callback != nil {
		callback(authID)
	}
}

func rewriteModelForAuth(model string, auth *Auth) string {
	if auth == nil || model == "" {
		return model
	}
	prefix := strings.TrimSpace(auth.Prefix)
	if prefix == "" {
		return model
	}
	needle := prefix + "/"
	if !strings.HasPrefix(model, needle) {
		return model
	}
	return strings.TrimPrefix(model, needle)
}

func (m *Manager) applyAPIKeyModelAlias(auth *Auth, requestedModel string) string {
	if m == nil || auth == nil {
		return requestedModel
	}

	kind, _ := auth.AccountInfo()
	if !strings.EqualFold(strings.TrimSpace(kind), "api_key") {
		return requestedModel
	}

	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return requestedModel
	}

	// Fast path: lookup per-auth mapping table (keyed by auth.ID).
	if resolved := m.lookupAPIKeyUpstreamModel(auth.ID, requestedModel); resolved != "" {
		return resolved
	}

	// Slow path: scan config for the matching credential entry and resolve alias.
	// This acts as a safety net if mappings are stale or auth.ID is missing.
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	upstreamModel := ""
	switch provider {
	case "gemini":
		upstreamModel = resolveUpstreamModelForGeminiAPIKey(cfg, auth, requestedModel)
	case "claude":
		upstreamModel = resolveUpstreamModelForClaudeAPIKey(cfg, auth, requestedModel)
	case "codex":
		upstreamModel = resolveUpstreamModelForCodexAPIKey(cfg, auth, requestedModel)
	case "vertex":
		upstreamModel = resolveUpstreamModelForVertexAPIKey(cfg, auth, requestedModel)
	default:
		upstreamModel = resolveUpstreamModelForOpenAICompatAPIKey(cfg, auth, requestedModel)
	}

	// Return upstream model if found, otherwise return requested model.
	if upstreamModel != "" {
		return upstreamModel
	}
	return requestedModel
}

// APIKeyConfigEntry is a generic interface for API key configurations.
type APIKeyConfigEntry interface {
	GetAPIKey() string
	GetBaseURL() string
}

func resolveAPIKeyConfig[T APIKeyConfigEntry](entries []T, auth *Auth) *T {
	if auth == nil || len(entries) == 0 {
		return nil
	}
	attrKey, attrBase := "", ""
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range entries {
		entry := &entries[i]
		cfgKey := strings.TrimSpace((*entry).GetAPIKey())
		cfgBase := strings.TrimSpace((*entry).GetBaseURL())
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range entries {
			entry := &entries[i]
			if strings.EqualFold(strings.TrimSpace((*entry).GetAPIKey()), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func resolveGeminiAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.GeminiKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.GeminiKey, auth)
}

func resolveClaudeAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.ClaudeKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.ClaudeKey, auth)
}

func resolveCodexAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.CodexKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.CodexKey, auth)
}

func resolveVertexAPIKeyConfig(cfg *internalconfig.Config, auth *Auth) *internalconfig.VertexCompatKey {
	if cfg == nil {
		return nil
	}
	return resolveAPIKeyConfig(cfg.VertexCompatAPIKey, auth)
}

func resolveUpstreamModelForGeminiAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveGeminiAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelPoolForGeminiAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) []string {
	entry := resolveGeminiAPIKeyConfig(cfg, auth)
	if entry == nil {
		return nil
	}
	return resolveModelAliasPoolFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForClaudeAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveClaudeAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelPoolForClaudeAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) []string {
	entry := resolveClaudeAPIKeyConfig(cfg, auth)
	if entry == nil {
		return nil
	}
	return resolveModelAliasPoolFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForCodexAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveCodexAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelPoolForCodexAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) []string {
	entry := resolveCodexAPIKeyConfig(cfg, auth)
	if entry == nil {
		return nil
	}
	return resolveModelAliasPoolFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForVertexAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	entry := resolveVertexAPIKeyConfig(cfg, auth)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelPoolForVertexAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) []string {
	entry := resolveVertexAPIKeyConfig(cfg, auth)
	if entry == nil {
		return nil
	}
	return resolveModelAliasPoolFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelForOpenAICompatAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) string {
	providerKey := ""
	compatName := ""
	if auth != nil && len(auth.Attributes) > 0 {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if compatName == "" && !strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return ""
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider)
	if entry == nil {
		return ""
	}
	return resolveModelAliasFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func resolveUpstreamModelPoolForOpenAICompatAPIKey(cfg *internalconfig.Config, auth *Auth, requestedModel string) []string {
	providerKey := ""
	compatName := ""
	if auth != nil && len(auth.Attributes) > 0 {
		providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
		compatName = strings.TrimSpace(auth.Attributes["compat_name"])
	}
	if compatName == "" && !strings.EqualFold(strings.TrimSpace(auth.Provider), "openai-compatibility") {
		return nil
	}
	entry := resolveOpenAICompatConfig(cfg, providerKey, compatName, auth.Provider)
	if entry == nil {
		return nil
	}
	return resolveModelAliasPoolFromConfigModels(requestedModel, asModelAliasEntries(entry.Models))
}

func (m *Manager) resolveAPIKeyUpstreamModelPool(auth *Auth, requestedModel string) []string {
	if m == nil || auth == nil {
		return nil
	}
	kind, _ := auth.AccountInfo()
	if !strings.EqualFold(strings.TrimSpace(kind), "api_key") {
		return nil
	}
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel == "" {
		return nil
	}

	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return resolveUpstreamModelPoolForClaudeAPIKey(cfg, auth, requestedModel)
	case "codex":
		return resolveUpstreamModelPoolForCodexAPIKey(cfg, auth, requestedModel)
	case "gemini":
		return resolveUpstreamModelPoolForGeminiAPIKey(cfg, auth, requestedModel)
	case "vertex":
		return resolveUpstreamModelPoolForVertexAPIKey(cfg, auth, requestedModel)
	default:
		return resolveUpstreamModelPoolForOpenAICompatAPIKey(cfg, auth, requestedModel)
	}
}

type apiKeyModelAliasTable map[string]map[string]string

func resolveOpenAICompatConfig(cfg *internalconfig.Config, providerKey, compatName, authProvider string) *internalconfig.OpenAICompatibility {
	if cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if v := strings.TrimSpace(compatName); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(providerKey); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(authProvider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func asModelAliasEntries[T interface {
	GetName() string
	GetAlias() string
}](models []T) []modelAliasEntry {
	if len(models) == 0 {
		return nil
	}
	out := make([]modelAliasEntry, 0, len(models))
	for i := range models {
		out = append(out, models[i])
	}
	return out
}

func (m *Manager) normalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	result := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.TrimSpace(strings.ToLower(provider))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

func isCodexProviderName(provider string) bool {
	return strings.EqualFold(strings.TrimSpace(provider), "codex")
}

func isCodexAuth(auth *Auth) bool {
	return auth != nil && isCodexProviderName(auth.Provider)
}

func isCodexAPIKeyAuth(auth *Auth) bool {
	return isCodexAuth(auth) && isAPIKeyAuth(auth)
}

func (m *Manager) retrySettings() (int, int, time.Duration) {
	if m == nil {
		return 0, 0, 0
	}
	return int(m.requestRetry.Load()), int(m.maxRetryCredentials.Load()), time.Duration(m.maxRetryInterval.Load())
}

func (m *Manager) retryQueueWait() time.Duration {
	if m == nil {
		return 0
	}
	base := time.Duration(m.retryQueueDelay.Load())
	if base <= 0 {
		return 0
	}
	jitterLimit := int64(base)
	if jitterLimit <= 1 {
		return base
	}
	return base + time.Duration(time.Now().UnixNano()%jitterLimit)
}

func (m *Manager) waitForRetryQueue(ctx context.Context) error {
	return waitForCooldown(ctx, m.retryQueueWait())
}

func codexModelLoadKey(provider, model string) string {
	if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
		return ""
	}
	modelKey := canonicalModelKey(model)
	if modelKey == "" {
		return ""
	}
	return "codex:" + modelKey
}

func (m *Manager) codexModelConcurrencyLimit(model string) int {
	if m == nil {
		return 1
	}
	modelKey := canonicalModelKey(model)
	if modelKey == "" {
		return 1
	}
	now := time.Now()
	limit := 0
	registryRef := registry.GetGlobalRegistry()
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || auth.Disabled {
			continue
		}
		if !m.authSupportsRouteModel(registryRef, auth, modelKey) {
			continue
		}
		checkModel := m.selectionModelForAuth(auth, modelKey)
		blocked, _, _ := isAuthBlockedForModel(auth, checkModel, now)
		if blocked {
			continue
		}
		limit++
	}
	if limit < 1 {
		return 1
	}
	return limit
}

func (m *Manager) reserveCodexModelSlot(provider, model string) (func(), error) {
	key := codexModelLoadKey(provider, model)
	if key == "" || m == nil {
		return func() {}, nil
	}
	m.codexModelLoadMu.Lock()
	if m.codexModelLoads == nil {
		m.codexModelLoads = make(map[string]int)
	}
	// Track Codex model pressure without rejecting requests. Hard model-level
	// 429s are too disruptive for long-running streaming workloads.
	m.codexModelLoads[key]++
	m.codexModelLoadMu.Unlock()

	return func() {
		m.codexModelLoadMu.Lock()
		defer m.codexModelLoadMu.Unlock()
		current := m.codexModelLoads[key]
		if current <= 1 {
			delete(m.codexModelLoads, key)
			return
		}
		m.codexModelLoads[key] = current - 1
	}, nil
}

func (m *Manager) closestCooldownWait(providers []string, model string, attempt int) (time.Duration, bool) {
	if m == nil || len(providers) == 0 {
		return 0, false
	}
	now := time.Now()
	defaultRetry := int(m.requestRetry.Load())
	if defaultRetry < 0 {
		defaultRetry = 0
	}
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var (
		found   bool
		minWait time.Duration
	)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		effectiveRetry := defaultRetry
		if override, ok := auth.RequestRetryOverride(); ok {
			effectiveRetry = override
		}
		if effectiveRetry < 0 {
			effectiveRetry = 0
		}
		if attempt >= effectiveRetry {
			continue
		}
		checkModel := model
		if strings.TrimSpace(model) != "" {
			checkModel = m.selectionModelForAuth(auth, model)
		}
		blocked, reason, next := isAuthBlockedForModel(auth, checkModel, now)
		if !blocked || next.IsZero() || reason == blockReasonDisabled {
			continue
		}
		wait := next.Sub(now)
		if wait < 0 {
			continue
		}
		if !found || wait < minWait {
			minWait = wait
			found = true
		}
	}
	return minWait, found
}

func (m *Manager) retryAllowed(attempt int, providers []string) bool {
	if m == nil || attempt < 0 || len(providers) == 0 {
		return false
	}
	defaultRetry := int(m.requestRetry.Load())
	if defaultRetry < 0 {
		defaultRetry = 0
	}
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.TrimSpace(strings.ToLower(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	if len(providerSet) == 0 {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		effectiveRetry := defaultRetry
		if override, ok := auth.RequestRetryOverride(); ok {
			effectiveRetry = override
		}
		if effectiveRetry < 0 {
			effectiveRetry = 0
		}
		if attempt < effectiveRetry {
			return true
		}
	}
	return false
}

func (m *Manager) shouldRetryAfterError(err error, attempt int, providers []string, model string, maxWait time.Duration) (time.Duration, bool) {
	if err == nil {
		return 0, false
	}
	status := statusCodeFromError(err)
	if status == http.StatusOK {
		return 0, false
	}
	if isRequestInvalidError(err) {
		return 0, false
	}
	if isTransientRoutingError(err) {
		return transientNetworkRetryDelay(attempt, maxWait)
	}
	if isRetryableEmptyUpstreamResponseError(err) {
		if !m.retryAllowed(attempt, providers) {
			return 0, false
		}
		return transientNetworkRetryDelay(attempt, maxWait)
	}
	if maxWait <= 0 {
		return 0, false
	}
	if status == 0 && isRetryableAuthError(err) {
		if !m.retryAllowed(attempt, providers) {
			return 0, false
		}
		return 0, true
	}
	wait, found := m.closestCooldownWait(providers, model, attempt)
	if found {
		if wait > maxWait {
			return 0, false
		}
		return wait, true
	}
	if status != http.StatusTooManyRequests {
		return 0, false
	}
	if !m.retryAllowed(attempt, providers) {
		return 0, false
	}
	retryAfter := retryAfterFromError(err)
	if retryAfter == nil || *retryAfter <= 0 || *retryAfter > maxWait {
		return 0, false
	}
	return *retryAfter, true
}

func transientNetworkRetryDelay(attempt int, maxWait time.Duration) (time.Duration, bool) {
	if attempt < 0 || attempt >= transientNetworkRetryAttempts {
		return 0, false
	}
	wait := time.Duration(attempt+1) * time.Second
	if wait > transientNetworkRetryMaxDelay {
		wait = transientNetworkRetryMaxDelay
	}
	if maxWait > 0 && wait > maxWait {
		return 0, false
	}
	return wait, true
}

func isRetryableAuthError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	return authErr.Retryable
}

func waitForCooldown(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isTransientUpstreamStatus(statusCode int) bool {
	switch statusCode {
	case 408, 500, 502, 503, 504, 520, 521, 522, 523, 524:
		return true
	default:
		return false
	}
}

// MarkResult records an execution result and notifies hooks.
func (m *Manager) MarkResult(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}
	m.markSelectorLoadDone(result.AuthID, result.Model)

	shouldResumeModel := false
	shouldSuspendModel := false
	shouldUnregisterAuth := false
	suspendReason := ""
	clearModelQuota := false
	setModelQuota := false
	var modelQuotaRecoverAt time.Time
	registryModel := ""
	var authSnapshot *Auth
	var schedulerSnapshots []*Auth

	m.mu.Lock()
	if auth, ok := m.auths[result.AuthID]; ok && auth != nil {
		now := time.Now()
		requestedModelAlias := coreusage.RequestedModelAliasFromContext(ctx)
		aliasAvailabilityModel := openAICompatAvailabilityAliasForResult(auth, requestedModelAlias, result)
		managedModel := strings.TrimSpace(result.Model)
		if aliasAvailabilityModel != "" {
			managedModel = aliasAvailabilityModel
		}
		registryModel = managedModel
		if result.Success && aliasAvailabilityModel == "" {
			registryModel = strings.TrimSpace(result.Model)
		}
		codexAPIKeyHealthOnly := isCodexAPIKeyAuth(auth)
		codexBypassCooling := isCodexAuth(auth) && !codexAPIKeyHealthOnly
		slowPenalty := 0
		if result.Success && m.slowRequestPenaltyEnabledLocked(auth) {
			slowPenalty = slowRequestHealthPenalty(result.Duration)
		}
		auth.recordRecentRequest(now, result.Success)
		if result.Success {
			auth.Success++
		} else {
			auth.Failed++
		}

		if shouldDisableAuthForProxyFailure(auth, result) {
			disableAuthForProxyFailure(auth, result, now)
			shouldUnregisterAuth = true
			cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
			if cfg == nil {
				cfg = &internalconfig.Config{}
			}
			m.rebuildAPIKeyModelAliasLocked(cfg)
			logEntryWithRequestID(ctx).WithFields(log.Fields{
				"auth_id":  auth.ID,
				"provider": auth.Provider,
				"model":    result.Model,
			}).Warn("disabled auth because SOCKS5 proxy dialing failed")
		} else if shouldDisableAuthForBalanceExhausted(result) {
			disableAuthForBalanceExhausted(auth, result, now)
			shouldUnregisterAuth = true
			cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
			if cfg == nil {
				cfg = &internalconfig.Config{}
			}
			m.rebuildAPIKeyModelAliasLocked(cfg)
			logEntryWithRequestID(ctx).WithFields(log.Fields{
				"auth_id":  auth.ID,
				"provider": auth.Provider,
				"model":    result.Model,
			}).Warn("disabled auth because upstream reported insufficient balance")
		} else if result.Success {
			if result.Model != "" {
				state := ensureModelState(auth, result.Model)
				resetModelState(state, now)
				applyHealthSuccess(&state.Health, now)
				applySlowRequestHealthPenalty(&state.Health, now, slowPenalty)
				updateAggregatedAvailability(auth, now)
				if aliasAvailabilityModel != "" && aliasAvailabilityModel != strings.TrimSpace(result.Model) {
					aliasState := ensureModelState(auth, aliasAvailabilityModel)
					resetModelState(aliasState, now)
					applyHealthSuccess(&aliasState.Health, now)
					aliasState.UpdatedAt = now
					clearAggregatedAvailability(auth)
				}
				if !hasModelError(auth, now) {
					auth.LastError = nil
					auth.StatusMessage = ""
					auth.Status = StatusActive
				}
				auth.UpdatedAt = now
				shouldResumeModel = true
				clearModelQuota = true
			} else {
				clearAuthStateOnSuccess(auth, now)
				applyHealthSuccess(&auth.Health, now)
				applySlowRequestHealthPenalty(&auth.Health, now, slowPenalty)
			}
		} else {
			if codexBypassCooling {
				if result.Model != "" {
					state := ensureModelState(auth, result.Model)
					resetModelState(state, now)
					state.Health = HealthState{}
					updateAggregatedAvailability(auth, now)
					auth.Health = HealthState{}
					auth.LastError = nil
					auth.StatusMessage = ""
					if auth.Status != StatusDisabled {
						auth.Status = StatusActive
					}
					auth.UpdatedAt = now
					shouldResumeModel = true
					clearModelQuota = true
				} else {
					clearAuthStateOnSuccess(auth, now)
				}
			} else if codexAPIKeyHealthOnly {
				applyCodexAPIKeyFailureState(auth, result, now)
			} else if managedModel != "" {
				if !isRequestScopedNotFoundResultError(result.Error) &&
					!isRequestScopedFeatureUnsupportedResultError(result.Error) &&
					!isRequestScopedContentSafetyResultError(result.Error) &&
					!isRequestScopedContextLimitResultError(result.Error) &&
					!isTransientRoutingResultError(result.Error) {
					disableCooling := quotaCooldownDisabledForAuth(auth)
					state := ensureModelState(auth, managedModel)
					state.Unavailable = true
					state.Status = StatusError
					state.UpdatedAt = now
					statusCode := statusCodeFromResult(result.Error)
					accountQuotaFailure := isAccountQuotaExhaustedResultError(result.Error)
					applyHealthFailure(&state.Health, now, statusCode)
					if result.Error != nil {
						state.LastError = cloneError(result.Error)
						state.StatusMessage = result.Error.Message
						auth.LastError = cloneError(result.Error)
						auth.StatusMessage = result.Error.Message
					}
					if isModelSupportResultError(result.Error) {
						state.Status = StatusDisabled
						next := now.Add(12 * time.Hour)
						state.NextRetryAfter = next
						suspendReason = "model_not_supported"
						shouldSuspendModel = true
					} else if accountQuotaFailure {
						applyHealthFailure(&auth.Health, now, statusCode)
						next := applyAccountQuotaFailureState(auth, state, result.Error, result.RetryAfter, now)
						suspendReason = "billing_cycle_quota"
						shouldSuspendModel = true
						setModelQuota = true
						modelQuotaRecoverAt = next
					} else {
						switch statusCode {
						case 401:
							if disableCooling {
								state.NextRetryAfter = time.Time{}
							} else {
								next := now.Add(30 * time.Minute)
								state.NextRetryAfter = next
								suspendReason = "unauthorized"
								shouldSuspendModel = true
							}
						case 402, 403:
							if disableCooling {
								state.NextRetryAfter = time.Time{}
							} else {
								next := now.Add(30 * time.Minute)
								state.NextRetryAfter = next
								suspendReason = "payment_required"
								shouldSuspendModel = true
							}
						case 404:
							if disableCooling {
								state.NextRetryAfter = time.Time{}
							} else {
								next := now.Add(12 * time.Hour)
								state.NextRetryAfter = next
								suspendReason = "not_found"
								shouldSuspendModel = true
							}
						case 429:
							var next time.Time
							backoffLevel := state.Quota.BackoffLevel
							hardCooldown := !disableCooling && shouldHardCooldownQuota(state.Health, result.RetryAfter)
							if hardCooldown {
								if result.RetryAfter != nil {
									next = now.Add(*result.RetryAfter)
								} else {
									cooldown, nextLevel := nextQuotaCooldown(backoffLevel, disableCooling)
									if cooldown > 0 {
										next = now.Add(cooldown)
									}
									backoffLevel = nextLevel
								}
								next = laterTime(next, state.Health.OpenUntil)
							}
							state.NextRetryAfter = next
							state.Quota = QuotaState{
								Exceeded:      true,
								Reason:        "quota",
								NextRecoverAt: next,
								BackoffLevel:  backoffLevel,
							}
							if hardCooldown {
								suspendReason = "quota"
								shouldSuspendModel = true
								setModelQuota = true
								modelQuotaRecoverAt = next
							}
						default:
							if isTransientUpstreamStatus(statusCode) {
								if disableCooling {
									state.NextRetryAfter = time.Time{}
								} else if next := transientHardCooldownUntil(state.Health); !next.IsZero() {
									state.NextRetryAfter = next
								} else {
									state.NextRetryAfter = time.Time{}
								}
							} else {
								state.NextRetryAfter = time.Time{}
							}
						}
					}

					auth.Status = StatusError
					auth.UpdatedAt = now
					if !accountQuotaFailure {
						updateAggregatedAvailability(auth, now)
						if aliasAvailabilityModel != "" {
							clearAggregatedAvailability(auth)
						}
					}
				}
			} else {
				applyAuthFailureState(auth, result.Error, result.RetryAfter, now)
			}
		}
		schedulerSnapshots = append(schedulerSnapshots, m.applyChannelBreakerResultLocked(auth, result, requestedModelAlias, now)...)
		if slowPenalty > 0 {
			schedulerSnapshots = append(schedulerSnapshots, m.applySlowRequestGroupPenaltyLocked(auth, result, now, slowPenalty)...)
		}

		if errPersist := m.persist(ctx, auth); errPersist != nil {
			logEntryWithRequestID(ctx).WithField("auth_id", auth.ID).Warnf("failed to persist auth result state: %v", errPersist)
		}
		authSnapshot = auth.Clone()
		schedulerSnapshots = append(schedulerSnapshots, authSnapshot)
	}
	m.mu.Unlock()
	if m.scheduler != nil {
		seenSnapshots := make(map[string]struct{}, len(schedulerSnapshots))
		for _, snapshot := range schedulerSnapshots {
			if snapshot == nil || snapshot.ID == "" {
				continue
			}
			if _, seen := seenSnapshots[snapshot.ID]; seen {
				continue
			}
			seenSnapshots[snapshot.ID] = struct{}{}
			m.scheduler.upsertAuth(snapshot)
		}
	}

	if shouldUnregisterAuth {
		registry.GetGlobalRegistry().UnregisterClient(result.AuthID)
	}
	if registryModel == "" {
		registryModel = strings.TrimSpace(result.Model)
	}
	if registryModel == "" {
		registryModel = coreusage.RequestedModelAliasFromContext(ctx)
	}
	if clearModelQuota && registryModel != "" {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(result.AuthID, registryModel)
	}
	if setModelQuota && registryModel != "" {
		registry.GetGlobalRegistry().SetModelQuotaExceeded(result.AuthID, registryModel, modelQuotaRecoverAt)
	}
	if shouldResumeModel && registryModel != "" {
		registry.GetGlobalRegistry().ResumeClientModel(result.AuthID, registryModel)
	} else if shouldSuspendModel && registryModel != "" {
		registry.GetGlobalRegistry().SuspendClientModel(result.AuthID, registryModel, suspendReason)
	}

	if authSnapshot != nil {
		m.logAuthResultMetric(ctx, authSnapshot, result)
	}
	m.hook.OnResult(ctx, result)
}

func applyCodexAPIKeyFailureState(auth *Auth, result Result, now time.Time) {
	if auth == nil {
		return
	}
	if isCodexAPIKeyRequestScopedResultError(result.Error) {
		return
	}
	statusCode := statusCodeFromResult(result.Error)
	shouldLowerHealth := shouldCountCodexAPIKeyHealthFailure(result)
	var resultErr *Error
	if result.Error != nil {
		resultErr = cloneError(result.Error)
	}
	if result.Model != "" {
		state := ensureModelState(auth, result.Model)
		if state == nil {
			return
		}
		state.Unavailable = false
		state.NextRetryAfter = time.Time{}
		state.Quota = QuotaState{}
		state.Status = StatusError
		state.UpdatedAt = now
		if resultErr != nil {
			state.LastError = cloneError(resultErr)
			state.StatusMessage = resultErr.Message
			auth.LastError = cloneError(resultErr)
			auth.StatusMessage = resultErr.Message
		}
		if shouldLowerHealth {
			applyCodexAPIKeyHealthFailure(&state.Health, now, statusCode)
		}
		updateAggregatedAvailability(auth, now)
	} else {
		auth.Unavailable = false
		auth.NextRetryAfter = time.Time{}
		auth.Quota = QuotaState{}
		if shouldLowerHealth {
			applyCodexAPIKeyHealthFailure(&auth.Health, now, statusCode)
		}
	}
	if auth.Status != StatusDisabled {
		auth.Status = StatusError
	}
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
		auth.StatusMessage = resultErr.Message
	}
	auth.UpdatedAt = now
}

func shouldCountCodexAPIKeyHealthFailure(result Result) bool {
	if result.Success || result.Error == nil {
		return false
	}
	if isCodexAPIKeyRequestScopedResultError(result.Error) {
		return false
	}
	statusCode := statusCodeFromResult(result.Error)
	if statusCode == 0 {
		return true
	}
	if isTransientNetworkResultError(result.Error) ||
		isModelSupportResultError(result.Error) ||
		isAccountQuotaExhaustedResultError(result.Error) {
		return true
	}
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusNotFound, http.StatusTooManyRequests:
		return true
	default:
		return isTransientUpstreamStatus(statusCode) ||
			isRetryableAvailabilityErrorMessage(result.Error.Code+" "+result.Error.Message)
	}
}

func isCodexAPIKeyRequestScopedResultError(err *Error) bool {
	return isRequestScopedNotFoundResultError(err) ||
		isRequestScopedFeatureUnsupportedResultError(err) ||
		isRequestScopedContentSafetyResultError(err) ||
		isRequestScopedContextLimitResultError(err)
}

func applyCodexAPIKeyHealthFailure(health *HealthState, now time.Time, statusCode int) {
	if health == nil {
		return
	}
	applyHealthFailure(health, now, statusCode)
	health.BreakerState = HealthBreakerClosed
	health.OpenUntil = time.Time{}
	health.HalfOpenSuccesses = 0
}

func openAICompatAvailabilityAliasForResult(auth *Auth, requestedModelAlias string, result Result) string {
	if authProviderFamilyKey(auth) != "openai-compatibility" {
		return ""
	}
	requestedModelAlias = strings.TrimSpace(requestedModelAlias)
	if requestedModelAlias == "" {
		return ""
	}
	if canonicalModelKey(requestedModelAlias) == canonicalModelKey(result.Model) {
		return ""
	}
	if result.Success {
		if auth == nil || len(auth.ModelStates) == 0 {
			return ""
		}
		if state := auth.ModelStates[requestedModelAlias]; state != nil {
			return requestedModelAlias
		}
		aliasKey := canonicalModelKey(requestedModelAlias)
		if aliasKey != "" && aliasKey != requestedModelAlias {
			if state := auth.ModelStates[aliasKey]; state != nil {
				return requestedModelAlias
			}
		}
		return ""
	}
	if result.Error == nil {
		return ""
	}
	if isRequestScopedNotFoundResultError(result.Error) ||
		isRequestScopedFeatureUnsupportedResultError(result.Error) ||
		isRequestScopedContentSafetyResultError(result.Error) ||
		isRequestScopedContextLimitResultError(result.Error) ||
		isTransientRoutingResultError(result.Error) ||
		isModelSupportResultError(result.Error) ||
		isAccountQuotaExhaustedResultError(result.Error) ||
		isBalanceExhaustedResultError(result.Error) {
		return ""
	}
	statusCode := statusCodeFromResult(result.Error)
	if statusCode == http.StatusTooManyRequests || isTransientUpstreamStatus(statusCode) {
		return requestedModelAlias
	}
	if statusCode == 0 && isTransientNetworkResultError(result.Error) {
		return requestedModelAlias
	}
	if isRetryableAvailabilityErrorMessage(result.Error.Code + " " + result.Error.Message) {
		return requestedModelAlias
	}
	return ""
}

func channelBreakerModelKeyForResult(auth *Auth, result Result, requestedModelAlias string) string {
	modelKey := strings.TrimSpace(result.Model)
	if aliasModel := openAICompatAvailabilityAliasForResult(auth, requestedModelAlias, result); aliasModel != "" {
		return aliasModel
	}
	return modelKey
}

func (m *Manager) applyChannelBreakerResultLocked(auth *Auth, result Result, requestedModelAlias string, now time.Time) []*Auth {
	if m == nil || auth == nil || quotaCooldownDisabledForAuth(auth) {
		return nil
	}
	if isCodexAPIKeyAuth(auth) {
		return m.applyCodexAPIKeyChannelHealthResultLocked(auth, result, now)
	}
	aliasScoped := openAICompatAvailabilityAliasForResult(auth, requestedModelAlias, result) != ""
	breakerModel := channelBreakerModelKeyForResult(auth, result, requestedModelAlias)
	key := channelBreakerKey(auth, breakerModel)
	if key == "" {
		return nil
	}
	m.pruneChannelBreakersLocked(now)
	if result.Success {
		m.recordChannelBreakerSuccessLocked(key, now)
		return nil
	}
	if !shouldCountChannelBreakerFailure(result) {
		return nil
	}

	statusCode := statusCodeFromResult(result.Error)
	health := m.channelBreakers[key]
	applyHealthFailure(&health, now, statusCode)
	if health.ConsecutiveFailures >= channelBreakerOpenFailures {
		cooldown := healthOpenCooldown(statusCode, health.ConsecutiveFailures)
		if result.RetryAfter != nil && *result.RetryAfter > cooldown {
			cooldown = *result.RetryAfter
		}
		if cooldown > quotaBackoffMax {
			cooldown = quotaBackoffMax
		}
		if cooldown <= 0 {
			cooldown = healthOpenCooldown(0, health.ConsecutiveFailures)
		}
		health.BreakerState = HealthBreakerOpen
		health.OpenUntil = now.Add(cooldown)
	}
	if health.BreakerState == HealthBreakerClosed && health.ConsecutiveFailures == 0 {
		delete(m.channelBreakers, key)
		return nil
	}
	if m.channelBreakers == nil {
		m.channelBreakers = make(map[string]HealthState)
	}
	m.channelBreakers[key] = health
	if health.BreakerState != HealthBreakerOpen || health.OpenUntil.IsZero() || !health.OpenUntil.After(now) {
		return nil
	}
	return m.applyChannelBreakerCooldownLocked(auth, result, breakerModel, aliasScoped, health, now)
}

func (m *Manager) recordChannelBreakerSuccessLocked(key string, now time.Time) {
	if m == nil || key == "" || len(m.channelBreakers) == 0 {
		return
	}
	health, ok := m.channelBreakers[key]
	if !ok {
		return
	}
	applyHealthSuccess(&health, now)
	if health.BreakerState == HealthBreakerClosed {
		delete(m.channelBreakers, key)
		return
	}
	m.channelBreakers[key] = health
}

func (m *Manager) applyChannelBreakerCooldownLocked(auth *Auth, result Result, breakerModel string, aliasScoped bool, health HealthState, now time.Time) []*Auth {
	baseKey := channelBreakerBaseKey(auth)
	if m == nil || baseKey == "" || strings.TrimSpace(breakerModel) == "" {
		return nil
	}
	statusCode := statusCodeFromResult(result.Error)
	message := channelBreakerStatusMessage
	if result.Error != nil && strings.TrimSpace(result.Error.Message) != "" {
		message = channelBreakerStatusMessage + ": " + result.Error.Message
	}
	snapshots := make([]*Auth, 0)
	for _, peer := range m.auths {
		if peer == nil || peer.Disabled || peer.Status == StatusDisabled {
			continue
		}
		if channelBreakerBaseKey(peer) != baseKey {
			continue
		}
		state := ensureModelState(peer, breakerModel)
		if state == nil || state.Status == StatusDisabled {
			continue
		}
		state.Unavailable = true
		state.Status = StatusError
		state.StatusMessage = message
		state.LastError = &Error{
			Code:       channelBreakerErrorCode,
			Message:    message,
			Retryable:  true,
			HTTPStatus: statusCode,
		}
		state.NextRetryAfter = laterTime(state.NextRetryAfter, health.OpenUntil)
		state.Health = health
		state.UpdatedAt = now
		if peer.Status != StatusDisabled {
			peer.Status = StatusError
		}
		peer.StatusMessage = message
		peer.LastError = cloneError(state.LastError)
		peer.UpdatedAt = now
		updateAggregatedAvailability(peer, now)
		if aliasScoped {
			clearAggregatedAvailability(peer)
		}
		snapshots = append(snapshots, peer.Clone())
	}
	return snapshots
}

func shouldCountChannelBreakerFailure(result Result) bool {
	if result.Success || result.Error == nil {
		return false
	}
	if isRequestScopedNotFoundResultError(result.Error) ||
		isRequestScopedFeatureUnsupportedResultError(result.Error) ||
		isRequestScopedContentSafetyResultError(result.Error) ||
		isRequestScopedContextLimitResultError(result.Error) {
		return false
	}
	if isTransientRoutingResultError(result.Error) {
		return false
	}
	if isModelSupportResultError(result.Error) || isBalanceExhaustedResultError(result.Error) {
		return false
	}
	statusCode := statusCodeFromResult(result.Error)
	if statusCode == 0 {
		return true
	}
	if statusCode == http.StatusTooManyRequests || isTransientUpstreamStatus(statusCode) {
		return true
	}
	if statusCode == http.StatusUnauthorized || statusCode == http.StatusNotFound {
		return false
	}
	return isRetryableAvailabilityErrorMessage(result.Error.Code + " " + result.Error.Message)
}

func (m *Manager) applyCodexAPIKeyChannelHealthResultLocked(auth *Auth, result Result, now time.Time) []*Auth {
	key := codexAPIKeyChannelKey(auth, result.Model)
	if key == "" {
		return nil
	}
	m.pruneChannelBreakersLocked(now)
	if result.Success {
		m.recordChannelBreakerSuccessLocked(key, now)
		return nil
	}
	if !shouldCountCodexAPIKeyHealthFailure(result) {
		return nil
	}

	statusCode := statusCodeFromResult(result.Error)
	health := m.channelBreakers[key]
	applyCodexAPIKeyHealthFailure(&health, now, statusCode)
	if health.BreakerState == HealthBreakerClosed && health.ConsecutiveFailures == 0 {
		delete(m.channelBreakers, key)
		return nil
	}
	if m.channelBreakers == nil {
		m.channelBreakers = make(map[string]HealthState)
	}
	m.channelBreakers[key] = health
	if health.ConsecutiveFailures < channelBreakerOpenFailures {
		return nil
	}
	return m.applyCodexAPIKeyChannelHealthPenaltyLocked(auth, result, health, now)
}

func (m *Manager) applyCodexAPIKeyChannelHealthPenaltyLocked(auth *Auth, result Result, health HealthState, now time.Time) []*Auth {
	baseKey := codexAPIKeyChannelBaseKey(auth)
	if m == nil || baseKey == "" || result.Model == "" {
		return nil
	}
	snapshots := make([]*Auth, 0)
	for _, peer := range m.auths {
		if peer == nil || peer.Disabled || peer.Status == StatusDisabled {
			continue
		}
		if codexAPIKeyChannelBaseKey(peer) != baseKey {
			continue
		}
		state := ensureModelState(peer, result.Model)
		if state == nil || state.Status == StatusDisabled {
			continue
		}
		if !shouldApplyCodexAPIKeyChannelHealth(state.Health, health, now) {
			continue
		}
		state.Health = health
		state.UpdatedAt = now
		snapshots = append(snapshots, peer.Clone())
	}
	return snapshots
}

func shouldApplyCodexAPIKeyChannelHealth(current, candidate HealthState, now time.Time) bool {
	if !healthStateKnown(candidate) {
		return false
	}
	if !healthStateKnown(current) {
		return true
	}
	currentScore := recoveredHealthScore(current, now)
	candidateScore := recoveredHealthScore(candidate, now)
	if candidateScore < currentScore {
		return true
	}
	return candidateScore == currentScore && candidate.ConsecutiveFailures > current.ConsecutiveFailures
}

func slowRequestHealthPenalty(duration time.Duration) int {
	if duration >= slowRequestHardThreshold {
		return slowRequestHardPenalty
	}
	if duration >= slowRequestSoftThreshold {
		return slowRequestSoftPenalty
	}
	return 0
}

func applySlowRequestHealthPenalty(health *HealthState, now time.Time, penalty int) bool {
	if health == nil || penalty <= 0 {
		return false
	}
	score := recoveredHealthScore(*health, now)
	score -= penalty
	if score < slowRequestMinHealthScore {
		score = slowRequestMinHealthScore
	}
	if score > healthScoreDefault {
		score = healthScoreDefault
	}
	health.Observed = true
	health.Score = score
	health.LastUpdatedAt = now
	health.LastStatusCode = http.StatusOK
	if health.BreakerState == "" {
		health.BreakerState = HealthBreakerClosed
	}
	return true
}

func (m *Manager) slowRequestPenaltyEnabledLocked(auth *Auth) bool {
	if m == nil || auth == nil {
		return false
	}
	return selectorUsesSpread(m.selectorForAuths([]*Auth{auth}))
}

func slowRequestPenaltyBaseKey(auth *Auth) string {
	if isCodexAPIKeyAuth(auth) {
		return codexAPIKeyChannelBaseKey(auth)
	}
	return channelBreakerBaseKey(auth)
}

func (m *Manager) applySlowRequestGroupPenaltyLocked(auth *Auth, result Result, now time.Time, penalty int) []*Auth {
	if m == nil || auth == nil || penalty <= 0 {
		return nil
	}
	baseKey := slowRequestPenaltyBaseKey(auth)
	if baseKey == "" {
		return nil
	}
	snapshots := make([]*Auth, 0)
	for _, peer := range m.auths {
		if peer == nil || peer.ID == auth.ID || peer.Disabled || peer.Status == StatusDisabled {
			continue
		}
		if slowRequestPenaltyBaseKey(peer) != baseKey {
			continue
		}
		if !m.slowRequestPenaltyEnabledLocked(peer) {
			continue
		}
		changed := false
		if result.Model != "" {
			state := ensureModelState(peer, result.Model)
			if state == nil || state.Status == StatusDisabled {
				continue
			}
			changed = applySlowRequestHealthPenalty(&state.Health, now, penalty)
			if changed {
				state.UpdatedAt = now
			}
		} else {
			changed = applySlowRequestHealthPenalty(&peer.Health, now, penalty)
		}
		if !changed {
			continue
		}
		peer.UpdatedAt = now
		snapshots = append(snapshots, peer.Clone())
	}
	return snapshots
}

func codexAPIKeyChannelBaseKey(auth *Auth) string {
	if !isCodexAPIKeyAuth(auth) || auth.Attributes == nil {
		return ""
	}
	baseURL := normalizeChannelBreakerURL(auth.Attributes["base_url"])
	proxyURL := normalizeChannelBreakerURL(auth.ProxyURL)
	prefix := strings.ToLower(strings.TrimSpace(auth.Prefix))
	routingGroup := normalizeRoutingGroupKey(auth.Attributes["routing_group"])
	return strings.Join([]string{
		"codex-api-key",
		baseURL,
		proxyURL,
		prefix,
		routingGroup,
	}, "\x00")
}

func codexAPIKeyChannelKey(auth *Auth, model string) string {
	baseKey := codexAPIKeyChannelBaseKey(auth)
	modelKey := canonicalModelKey(model)
	if modelKey == "" {
		modelKey = strings.ToLower(strings.TrimSpace(model))
	}
	if baseKey == "" || modelKey == "" {
		return ""
	}
	return baseKey + "\x00model=" + modelKey
}

func channelBreakerBaseKey(auth *Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	if authProviderFamilyKey(auth) != "openai-compatibility" &&
		strings.TrimSpace(auth.Attributes["provider_key"]) == "" &&
		strings.TrimSpace(auth.Attributes["compat_name"]) == "" {
		return ""
	}
	providerKey := normalizeRoutingGroupKey(auth.Attributes["provider_key"])
	if providerKey == "" {
		providerKey = normalizeRoutingGroupKey(auth.Provider)
	}
	compatName := normalizeRoutingGroupKey(auth.Attributes["compat_name"])
	baseURL := normalizeChannelBreakerURL(auth.Attributes["base_url"])
	proxyURL := normalizeChannelBreakerURL(auth.ProxyURL)
	prefix := strings.ToLower(strings.TrimSpace(auth.Prefix))
	routingGroup := normalizeRoutingGroupKey(auth.Attributes["routing_group"])
	return strings.Join([]string{
		"openai-compatibility",
		providerKey,
		compatName,
		baseURL,
		proxyURL,
		prefix,
		routingGroup,
	}, "\x00")
}

func channelBreakerKey(auth *Auth, model string) string {
	baseKey := channelBreakerBaseKey(auth)
	modelKey := canonicalModelKey(model)
	if modelKey == "" {
		modelKey = strings.ToLower(strings.TrimSpace(model))
	}
	if baseKey == "" || modelKey == "" {
		return ""
	}
	return baseKey + "\x00model=" + modelKey
}

func normalizeChannelBreakerURL(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	for strings.HasSuffix(raw, "/") {
		raw = strings.TrimSuffix(raw, "/")
	}
	return raw
}

func (m *Manager) pruneChannelBreakersLocked(now time.Time) {
	if m == nil || len(m.channelBreakers) <= channelBreakerStateLimit {
		return
	}
	for key, health := range m.channelBreakers {
		if health.BreakerState == HealthBreakerOpen && !health.OpenUntil.IsZero() && health.OpenUntil.After(now) {
			continue
		}
		delete(m.channelBreakers, key)
	}
	for len(m.channelBreakers) > channelBreakerStateLimit {
		for key := range m.channelBreakers {
			delete(m.channelBreakers, key)
			break
		}
	}
}

func (m *Manager) markSelectorLoadDone(authID, model string) {
	if m == nil || strings.TrimSpace(authID) == "" || strings.TrimSpace(model) == "" {
		return
	}
	if selector, ok := m.selector.(loadAwareSelector); ok {
		selector.MarkDone(authID, model)
	}

	m.dynamicSelectorsMu.Lock()
	selectors := make([]Selector, 0, len(m.dynamicSelectors))
	for _, selector := range m.dynamicSelectors {
		if selector != nil {
			selectors = append(selectors, selector)
		}
	}
	m.dynamicSelectorsMu.Unlock()

	for _, selector := range selectors {
		if loadAware, ok := selector.(loadAwareSelector); ok {
			loadAware.MarkDone(authID, model)
		}
	}
}

func ensureModelState(auth *Auth, model string) *ModelState {
	if auth == nil || model == "" {
		return nil
	}
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	if state, ok := auth.ModelStates[model]; ok && state != nil {
		return state
	}
	state := &ModelState{Status: StatusActive}
	auth.ModelStates[model] = state
	return state
}

func resetModelState(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	state.Unavailable = false
	state.Status = StatusActive
	state.StatusMessage = ""
	state.NextRetryAfter = time.Time{}
	state.LastError = nil
	state.Quota = QuotaState{}
	state.UpdatedAt = now
}

func modelStateIsClean(state *ModelState) bool {
	if state == nil {
		return true
	}
	if state.Status != StatusActive {
		return false
	}
	if state.Unavailable || state.StatusMessage != "" || !state.NextRetryAfter.IsZero() || state.LastError != nil {
		return false
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		return false
	}
	return true
}

func updateAggregatedAvailability(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	if len(auth.ModelStates) == 0 {
		clearAggregatedAvailability(auth)
		return
	}
	allUnavailable := true
	earliestRetry := time.Time{}
	quotaExceeded := false
	quotaRecover := time.Time{}
	maxBackoffLevel := 0
	hasState := false
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		hasState = true
		stateUnavailable := false
		if state.Status == StatusDisabled {
			stateUnavailable = true
		} else if state.Unavailable {
			if state.NextRetryAfter.IsZero() {
				stateUnavailable = false
			} else if state.NextRetryAfter.After(now) {
				stateUnavailable = true
				if earliestRetry.IsZero() || state.NextRetryAfter.Before(earliestRetry) {
					earliestRetry = state.NextRetryAfter
				}
			} else {
				state.Unavailable = false
				state.NextRetryAfter = time.Time{}
			}
		}
		if !stateUnavailable {
			allUnavailable = false
		}
		if state.Quota.Exceeded {
			quotaExceeded = true
			if quotaRecover.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.Before(quotaRecover)) {
				quotaRecover = state.Quota.NextRecoverAt
			}
			if state.Quota.BackoffLevel > maxBackoffLevel {
				maxBackoffLevel = state.Quota.BackoffLevel
			}
		}
	}
	if !hasState {
		clearAggregatedAvailability(auth)
		return
	}
	auth.Unavailable = allUnavailable
	if allUnavailable {
		auth.NextRetryAfter = earliestRetry
	} else {
		auth.NextRetryAfter = time.Time{}
	}
	if quotaExceeded {
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		auth.Quota.NextRecoverAt = quotaRecover
		auth.Quota.BackoffLevel = maxBackoffLevel
	} else {
		auth.Quota.Exceeded = false
		auth.Quota.Reason = ""
		auth.Quota.NextRecoverAt = time.Time{}
		auth.Quota.BackoffLevel = 0
	}
}

func clearAggregatedAvailability(auth *Auth) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.NextRetryAfter = time.Time{}
	auth.Quota = QuotaState{}
}

func hasModelError(auth *Auth, now time.Time) bool {
	if auth == nil || len(auth.ModelStates) == 0 {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.LastError != nil {
			return true
		}
		if state.Status == StatusError {
			if state.Unavailable && (state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now)) {
				return true
			}
		}
	}
	return false
}

func clearAuthStateOnSuccess(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.Status = StatusActive
	auth.StatusMessage = ""
	auth.Quota.Exceeded = false
	auth.Quota.Reason = ""
	auth.Quota.NextRecoverAt = time.Time{}
	auth.Quota.BackoffLevel = 0
	auth.LastError = nil
	auth.NextRetryAfter = time.Time{}
	auth.UpdatedAt = now
}

func applyHealthSuccess(health *HealthState, now time.Time) {
	if health == nil {
		return
	}
	score := recoveredHealthScore(*health, now)
	health.Observed = true
	health.SuccessCount++
	health.LastSuccessAt = now
	health.LastUpdatedAt = now
	health.LastStatusCode = http.StatusOK
	switch health.BreakerState {
	case HealthBreakerOpen:
		if !health.OpenUntil.IsZero() && health.OpenUntil.After(now) {
			return
		}
		health.BreakerState = HealthBreakerHalfOpen
		health.HalfOpenSuccesses = 1
		health.ConsecutiveFailures = 0
		health.OpenUntil = time.Time{}
		if score < healthBreakerThreshold {
			score = healthBreakerThreshold
		}
		health.Score = score
		return
	case HealthBreakerHalfOpen:
		health.HalfOpenSuccesses++
		health.ConsecutiveFailures = 0
		if health.HalfOpenSuccesses >= healthHalfOpenSuccesses {
			score += healthScoreStepSuccess
			if score > healthScoreDefault {
				score = healthScoreDefault
			}
			health.Score = score
			health.BreakerState = HealthBreakerClosed
			health.OpenUntil = time.Time{}
			health.HalfOpenSuccesses = 0
			return
		}
		if score < healthBreakerThreshold {
			score = healthBreakerThreshold
		}
		health.Score = score
		return
	default:
		score += healthScoreStepSuccess
		if score > healthScoreDefault {
			score = healthScoreDefault
		}
		health.Score = score
		health.ConsecutiveFailures = 0
		health.BreakerState = HealthBreakerClosed
		health.OpenUntil = time.Time{}
		health.HalfOpenSuccesses = 0
	}
}

func applyHealthFailure(health *HealthState, now time.Time, statusCode int) {
	if health == nil {
		return
	}
	score := recoveredHealthScore(*health, now)
	nextConsecutive := health.ConsecutiveFailures + 1
	score -= healthFailurePenalty(statusCode, nextConsecutive)
	if score < 0 {
		score = 0
	}
	health.Observed = true
	health.Score = score
	health.ConsecutiveFailures = nextConsecutive
	health.FailureCount++
	health.LastFailureAt = now
	health.LastUpdatedAt = now
	health.LastStatusCode = statusCode
	health.HalfOpenSuccesses = 0
	if shouldOpenHealthCircuit(*health, statusCode) {
		health.BreakerState = HealthBreakerOpen
		health.OpenUntil = now.Add(healthOpenCooldown(statusCode, nextConsecutive))
	} else if health.BreakerState == HealthBreakerHalfOpen {
		health.BreakerState = HealthBreakerOpen
		health.OpenUntil = now.Add(healthOpenCooldown(statusCode, nextConsecutive))
	} else if health.BreakerState == HealthBreakerOpen && health.OpenUntil.Before(now) {
		health.OpenUntil = now.Add(healthOpenCooldown(statusCode, nextConsecutive))
	} else {
		health.BreakerState = HealthBreakerClosed
		health.OpenUntil = time.Time{}
	}
}

func healthFailurePenalty(statusCode, consecutiveFailures int) int {
	penalty := 10
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound:
		penalty = 35
	case http.StatusTooManyRequests:
		penalty = 20
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 521, 522, 523, 524:
		penalty = 20
	default:
		if statusCode >= 500 {
			penalty = 20
		}
	}
	if consecutiveFailures > 1 {
		penalty += minInt(20, (consecutiveFailures-1)*5)
	}
	return penalty
}

func shouldOpenHealthCircuit(health HealthState, statusCode int) bool {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound:
		return true
	case http.StatusTooManyRequests:
		return health.ConsecutiveFailures >= health429OpenFailures
	}
	if health.ConsecutiveFailures >= 3 {
		return true
	}
	return health.ConsecutiveFailures >= 2 && health.Score <= healthBreakerThreshold
}

func shouldHardCooldownQuota(health HealthState, retryAfter *time.Duration) bool {
	if retryAfter != nil && *retryAfter >= quotaImmediateCooldownRetryAfter {
		return true
	}
	if health.BreakerState == HealthBreakerOpen {
		return true
	}
	return health.ConsecutiveFailures >= quotaHardCooldownFailures
}

func transientHardCooldownUntil(health HealthState) time.Time {
	if health.BreakerState != HealthBreakerOpen {
		return time.Time{}
	}
	return health.OpenUntil
}

func laterTime(a, b time.Time) time.Time {
	if a.IsZero() {
		return b
	}
	if b.IsZero() || a.After(b) {
		return a
	}
	return b
}

func healthOpenCooldown(statusCode, consecutiveFailures int) time.Duration {
	switch statusCode {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound:
		return 10 * time.Minute
	case http.StatusTooManyRequests:
		return time.Duration(minInt(3, consecutiveFailures)) * 15 * time.Second
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 521, 522, 523, 524:
		return time.Duration(minInt(4, consecutiveFailures)) * 30 * time.Second
	default:
		return time.Duration(minInt(3, consecutiveFailures)) * 30 * time.Second
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func cloneError(err *Error) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:       err.Code,
		Message:    err.Message,
		Retryable:  err.Retryable,
		HTTPStatus: err.HTTPStatus,
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func statusCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		return sc.StatusCode()
	}
	return 0
}

func errorCodeFromError(err error) string {
	if err == nil {
		return ""
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		return strings.TrimSpace(authErr.Code)
	}
	type errorCoder interface {
		ErrorCode() string
	}
	var ec errorCoder
	if errors.As(err, &ec) && ec != nil {
		return strings.TrimSpace(ec.ErrorCode())
	}
	return ""
}

func resultErrorFromCause(err error) *Error {
	if err == nil {
		return nil
	}
	resultErr := &Error{
		Code:       errorCodeFromError(err),
		Message:    err.Error(),
		HTTPStatus: statusCodeFromError(err),
	}
	return resultErr
}

func isUnauthorizedError(err error) bool {
	if err == nil {
		return false
	}
	if statusCodeFromError(err) == http.StatusUnauthorized {
		return true
	}
	raw := strings.ToLower(err.Error())
	return strings.Contains(raw, "status 401") || strings.Contains(raw, "401 unauthorized")
}

func shouldEvictUnauthorizedError(err error) bool {
	return isUnauthorizedError(err) && !isModelSupportError(err)
}

func shouldEvictUnauthorizedResult(err *Error) bool {
	return isUnauthorizedResult(err) && !isModelSupportResultError(err)
}

func hasUnauthorizedAuthFailure(auth *Auth) bool {
	if auth == nil || auth.LastError == nil {
		return false
	}
	return auth.LastError.StatusCode() == http.StatusUnauthorized || strings.EqualFold(auth.LastError.Code, "unauthorized")
}

func refreshErrorFromError(err error) *Error {
	if err == nil {
		return nil
	}
	statusCode := statusCodeFromError(err)
	if statusCode == 0 && isUnauthorizedError(err) {
		statusCode = http.StatusUnauthorized
	}
	authErr := &Error{Message: err.Error(), HTTPStatus: statusCode}
	if statusCode == http.StatusUnauthorized {
		authErr.Code = "unauthorized"
		authErr.Retryable = false
	}
	return authErr
}

func retryAfterFromError(err error) *time.Duration {
	if err == nil {
		return nil
	}
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	rap, ok := err.(retryAfterProvider)
	if !ok || rap == nil {
		return nil
	}
	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		return nil
	}
	value := *retryAfter
	return &value
}

func statusCodeFromResult(err *Error) int {
	if err == nil {
		return 0
	}
	return err.StatusCode()
}

func isUnauthorizedResult(err *Error) bool {
	return statusCodeFromResult(err) == http.StatusUnauthorized
}

func isModelSupportErrorMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	patterns := [...]string{
		"model_not_supported",
		"requested model is not supported",
		"requested model is unsupported",
		"requested model is unavailable",
		"requested model does not exist",
		"requested model is not available",
		"model is not supported",
		"model not supported",
		"model does not exist",
		"model not found",
		"unsupported model",
		"model unavailable",
		"not available for your plan",
		"not available for your account",
		"not available for this account",
		"not enabled for your account",
		"not enabled for this account",
		"does not have access to model",
		"model has been disabled",
		"模型不存在",
		"模型未开通",
		"模型不可用",
		"没有该模型权限",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func isModelSupportError(err error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromError(err)
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
	default:
		return false
	}
	return isModelSupportErrorMessage(err.Error())
}

func isModelSupportResultError(err *Error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromResult(err)
	switch status {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity:
	default:
		return false
	}
	return isModelSupportErrorMessage(err.Message)
}

func isPersistedModelSupportState(state *ModelState) bool {
	if state == nil || state.Status != StatusDisabled {
		return false
	}
	if state.LastError != nil && isModelSupportResultError(state.LastError) {
		return true
	}
	return isModelSupportErrorMessage(state.StatusMessage)
}

func isAccountQuotaExhaustedResultError(err *Error) bool {
	if err == nil {
		return false
	}
	switch statusCodeFromResult(err) {
	case http.StatusPaymentRequired, http.StatusForbidden, http.StatusTooManyRequests:
	default:
		return false
	}
	return isAccountQuotaExhaustedMessage(err.Message)
}

func isAccountQuotaExhaustedMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	patterns := [...]string{
		"usage limit",
		"billing cycle",
		"quota will be refreshed",
		"refreshed in the next cycle",
		"quota-upgrade",
		"monthly quota",
		"用量上限",
		"账期",
		"帳期",
		"下个周期",
		"下一周期",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func shouldDisableAuthForBalanceExhausted(result Result) bool {
	return !result.Success && isBalanceExhaustedResultError(result.Error)
}

func isBalanceExhaustedResultError(err *Error) bool {
	if err == nil {
		return false
	}
	if statusCodeFromResult(err) != http.StatusPaymentRequired {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(err.Code + " " + err.Message))
	if lower == "" {
		return false
	}
	patterns := [...]string{
		"insufficient balance",
		"insufficient_balance",
		"balance insufficient",
		"balance_insufficient",
		"balance is insufficient",
		"account balance insufficient",
		"not enough balance",
		"balance not enough",
		"balance_not_enough",
		"insufficient credit",
		"insufficient credits",
		"credit balance",
		"credits exhausted",
		"no credit",
		"recharge",
		"top up",
		"top-up",
		"充值",
		"余额不足",
		"餘額不足",
		"余额不够",
		"餘額不夠",
		"余额耗尽",
		"餘額耗盡",
		"余额已用完",
		"餘額已用完",
		"账户余额",
		"帳戶餘額",
		"欠费",
		"欠費",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func accountQuotaRetryAfter(retryAfter *time.Duration) time.Duration {
	if retryAfter != nil && *retryAfter > 0 {
		return *retryAfter
	}
	return accountQuotaCooldown
}

func applyAccountQuotaFailureState(auth *Auth, state *ModelState, resultErr *Error, retryAfter *time.Duration, now time.Time) time.Time {
	next := now.Add(accountQuotaRetryAfter(retryAfter))
	statusMessage := "billing cycle quota exhausted"
	quota := QuotaState{
		Exceeded:      true,
		Reason:        "billing_cycle_quota",
		NextRecoverAt: next,
	}

	auth.Unavailable = true
	auth.Status = StatusError
	auth.StatusMessage = statusMessage
	auth.NextRetryAfter = next
	auth.Quota = quota
	auth.UpdatedAt = now
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
	}
	if state != nil {
		state.Unavailable = true
		state.Status = StatusError
		state.StatusMessage = statusMessage
		state.NextRetryAfter = next
		state.Quota = quota
		state.UpdatedAt = now
		if resultErr != nil {
			state.LastError = cloneError(resultErr)
		}
	}
	return next
}

func isRetryableAvailabilityErrorMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	if isAccountQuotaExhaustedMessage(lower) {
		return true
	}
	patterns := [...]string{
		"payment required",
		"insufficient balance",
		"balance insufficient",
		"account balance insufficient",
		"insufficient_quota",
		"quota exhausted",
		"quota_exhausted",
		"rate limit",
		"rate_limit",
		"too many requests",
		"resource exhausted",
		"no available key",
		"no available api key",
		"no available channel",
		"channel unavailable",
		"upstream unavailable",
		"provider unavailable",
		"no healthy upstream",
		"无可用key",
		"无可用 key",
		"无可用渠道",
		"渠道不可用",
		"上游不可用",
		"额度已用尽",
		"额度不足",
		"余额不足",
		"账户余额不足",
		"帐户余额不足",
		"频率限制",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func isRequestScopedFeatureUnsupportedMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	patterns := [...]string{
		"request_feature_unsupported:",
		"minimax anthropic compatibility does not support output_config.format",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return false
}

func isRequestScopedNotFoundMessage(message string) bool {
	if message == "" {
		return false
	}
	lower := strings.ToLower(message)
	return strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false")
}

func isRequestScopedNotFoundResultError(err *Error) bool {
	if err == nil || statusCodeFromResult(err) != http.StatusNotFound {
		return false
	}
	return isRequestScopedNotFoundMessage(err.Message)
}

func isRequestScopedFeatureUnsupportedResultError(err *Error) bool {
	if err == nil || statusCodeFromResult(err) != http.StatusBadRequest {
		return false
	}
	return isRequestScopedFeatureUnsupportedMessage(err.Message)
}

func isRequestScopedContentSafetyMessage(message string) bool {
	return isRequestScopedContentSafetySignal("", message)
}

func isRequestScopedContentSafetySignal(code, message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return isMiniMaxNewSensitiveSignal(code, message)
	}
	return (strings.Contains(lower, "request was rejected") &&
		(strings.Contains(lower, "high risk") || strings.Contains(lower, "high-risk"))) ||
		(strings.Contains(lower, "content") && strings.Contains(lower, "blocked")) ||
		isMiniMaxNewSensitiveSignal(code, message)
}

func isMiniMaxNewSensitiveMessage(message string) bool {
	return isMiniMaxNewSensitiveSignal("", message)
}

func isMiniMaxInputNewSensitiveSignal(code, message string) bool {
	normalizedCode := strings.Trim(strings.ToLower(strings.TrimSpace(code)), `"'(),:;[]{}<>`)
	if normalizedCode == "1026" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	if strings.Contains(lower, "input new_sensitive") {
		return true
	}
	return strings.Contains(lower, "new_sensitive") && strings.Contains(lower, "1026")
}

func isMiniMaxOutputNewSensitiveSignal(code, message string) bool {
	normalizedCode := strings.Trim(strings.ToLower(strings.TrimSpace(code)), `"'(),:;[]{}<>`)
	if normalizedCode == "1027" {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	if strings.Contains(lower, "output new_sensitive") {
		return true
	}
	return strings.Contains(lower, "new_sensitive") && strings.Contains(lower, "1027")
}

func isMiniMaxNewSensitiveSignal(code, message string) bool {
	return isMiniMaxInputNewSensitiveSignal(code, message) ||
		isMiniMaxOutputNewSensitiveSignal(code, message)
}

func isMiniMaxUnknown1000Message(message string) bool {
	return isMiniMaxUnknown1000Signal("", message)
}

func isMiniMaxUnknown1000Signal(code, message string) bool {
	normalizedCode := strings.Trim(strings.ToLower(strings.TrimSpace(code)), `"'(),:;[]{}<>`)
	lower := strings.ToLower(strings.TrimSpace(message))
	if !strings.Contains(lower, "unknown error") {
		return false
	}
	return normalizedCode == "1000" || strings.Contains(lower, "1000")
}

func hasHTTPStatusInMessage(message string, statuses ...int) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	for _, status := range statuses {
		code := strconv.Itoa(status)
		if strings.Contains(lower, "status_code="+code) ||
			strings.Contains(lower, "status_code: "+code) ||
			strings.Contains(lower, "status code="+code) ||
			strings.Contains(lower, "status code: "+code) ||
			strings.Contains(lower, "status="+code) ||
			strings.Contains(lower, "status: "+code) {
			return true
		}
	}
	return false
}

func isRequestScopedContentSafetyStatus(status int, code, message string) bool {
	if isMiniMaxNewSensitiveSignal(code, message) {
		switch status {
		case http.StatusBadRequest, http.StatusInternalServerError, http.StatusUnavailableForLegalReasons:
			return true
		case 0:
			return !hasHTTPStatusInMessage(message, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusTooManyRequests) ||
				hasHTTPStatusInMessage(message, http.StatusBadRequest, http.StatusInternalServerError, http.StatusUnavailableForLegalReasons)
		default:
			return false
		}
	}
	switch status {
	case http.StatusBadRequest, http.StatusUnavailableForLegalReasons:
		return true
	case 0:
		return hasHTTPStatusInMessage(message, http.StatusBadRequest, http.StatusUnavailableForLegalReasons)
	default:
		return false
	}
}

func isRequestScopedContentSafetyResultError(err *Error) bool {
	if err == nil {
		return false
	}
	return isRequestScopedContentSafetyStatus(statusCodeFromResult(err), err.Code, err.Message) &&
		isRequestScopedContentSafetySignal(err.Code, err.Message)
}

func isRequestScopedContentSafetyError(err error) bool {
	if err == nil {
		return false
	}
	code := errorCodeFromError(err)
	message := err.Error()
	return isRequestScopedContentSafetyStatus(statusCodeFromError(err), code, message) &&
		isRequestScopedContentSafetySignal(code, message)
}

func isRequestScopedContextLimitMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "context window exceeds limit") ||
		strings.Contains(lower, "context window exceeded") ||
		strings.Contains(lower, "context length exceeded") ||
		strings.Contains(lower, "context length exceeds") ||
		strings.Contains(lower, "context_length_exceeded") ||
		(strings.Contains(lower, "maximum context") && strings.Contains(lower, "exceed")) ||
		(strings.Contains(lower, "context") && strings.Contains(lower, "too long"))
}

func isRequestScopedContextLimitStatus(status int, message string) bool {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return true
	case 0:
		return hasHTTPStatusInMessage(message, http.StatusBadRequest, http.StatusUnprocessableEntity)
	default:
		return false
	}
}

func isRequestScopedContextLimitResultError(err *Error) bool {
	if err == nil {
		return false
	}
	return isRequestScopedContextLimitStatus(statusCodeFromResult(err), err.Message) &&
		isRequestScopedContextLimitMessage(err.Message)
}

func isRequestScopedContextLimitError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return isRequestScopedContextLimitStatus(statusCodeFromError(err), message) &&
		isRequestScopedContextLimitMessage(message)
}

func isTransientNetworkMessage(message string) bool {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	patterns := []string{
		"connection reset by peer",
		"broken pipe",
		"unexpected eof",
		"read: eof",
		"write: eof",
		"server closed idle connection",
		"use of closed network connection",
		"i/o timeout",
		"io timeout",
		"tls handshake timeout",
		"timeout awaiting response headers",
		"client timeout exceeded",
		"context deadline exceeded",
		"connection refused",
		"connection aborted",
	}
	for _, pattern := range patterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	return lower == "eof" || strings.HasSuffix(lower, ": eof")
}

func isTransientNetworkStatus(status int, message string) bool {
	if status == 0 {
		return !hasHTTPStatusInMessage(message, http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity) ||
			hasHTTPStatusInMessage(message, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout)
	}
	return status == http.StatusRequestTimeout || isTransientUpstreamStatus(status)
}

func isTransientNetworkResultError(err *Error) bool {
	if err == nil {
		return false
	}
	message := strings.TrimSpace(err.Code + " " + err.Message)
	return isTransientNetworkMessage(message) && isTransientNetworkStatus(statusCodeFromResult(err), message)
}

func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return isTransientNetworkMessage(message) && isTransientNetworkStatus(statusCodeFromError(err), message)
}

func isMiniMaxTransientUpstreamStatus(status int, code, message string) bool {
	if !isMiniMaxUnknown1000Signal(code, message) {
		return false
	}
	if status == 0 {
		return !hasHTTPStatusInMessage(message, http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity)
	}
	return status == http.StatusRequestTimeout || isTransientUpstreamStatus(status)
}

func isMiniMaxTransientUpstreamResultError(err *Error) bool {
	if err == nil {
		return false
	}
	return isMiniMaxTransientUpstreamStatus(statusCodeFromResult(err), err.Code, err.Message)
}

func isMiniMaxTransientUpstreamError(err error) bool {
	if err == nil {
		return false
	}
	return isMiniMaxTransientUpstreamStatus(statusCodeFromError(err), errorCodeFromError(err), err.Error())
}

func isTransientRoutingResultError(err *Error) bool {
	return isTransientNetworkResultError(err) || isMiniMaxTransientUpstreamResultError(err)
}

func isTransientRoutingError(err error) bool {
	return isTransientNetworkError(err) || isMiniMaxTransientUpstreamError(err)
}

func isRetryableEmptyUpstreamResponseError(err error) bool {
	if err == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(errorCodeFromError(err)), emptyUpstreamResponseErrorCode) {
		return false
	}
	status := statusCodeFromError(err)
	if status == 0 {
		return true
	}
	return status == http.StatusRequestTimeout || isTransientUpstreamStatus(status)
}

func isRequestScopedRouteFallbackError(err error) bool {
	if isRequestScopedContentSafetyError(err) {
		return true
	}
	return isRequestScopedContextLimitError(err)
}

func shouldFallbackRequestScopedContentSafetyError(routeModel string, err error) bool {
	if !isRequestScopedFallbackModel(routeModel) {
		return false
	}
	return isRequestScopedContentSafetyError(err) && isRequestScopedRouteFallbackError(err)
}

func shouldFallbackRequestScopedContentSafetyErrorForRequest(routeModel string, opts cliproxyexecutor.Options, err error) bool {
	if !isRequestScopedContentSafetyError(err) {
		return false
	}
	return shouldFallbackRequestScopedRouteErrorForRequest(routeModel, opts, err)
}

func shouldFallbackRequestScopedRouteErrorForRequest(routeModel string, opts cliproxyexecutor.Options, err error) bool {
	if !isRequestScopedRouteFallbackError(err) {
		return false
	}
	if isMiniMaxNewSensitiveSignal(errorCodeFromError(err), err.Error()) &&
		!isMiniMaxInputNewSensitiveSignal(errorCodeFromError(err), err.Error()) {
		return false
	}
	if isRequestScopedFallbackModel(routeModel) {
		return true
	}
	return isRequestScopedFallbackModel(requestedModelAliasFromOptions(opts, routeModel))
}

func isRequestScopedFallbackModel(model string) bool {
	return isClaudeSonnet46FallbackModel(model) || isGLM47FallbackModel(model)
}

func isClaudeSonnet46FallbackModel(model string) bool {
	return isSpecificFallbackModel(model, "claude-sonnet-4-6")
}

func isGLM47FallbackModel(model string) bool {
	return isSpecificFallbackModel(model, "glm-4.7")
}

func isSpecificFallbackModel(model string, target string) bool {
	model = strings.TrimSpace(model)
	target = strings.TrimSpace(target)
	if model == "" || target == "" {
		return false
	}
	base := strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
	if base == "" {
		base = model
	}
	return strings.EqualFold(base, target)
}

// isRequestInvalidError returns true if the error represents a client request
// error that should not be retried. Specifically, it treats 400 responses with
// "invalid_request_error", request-scoped content safety/context-window rejections,
// request-scoped 404 item misses caused by `store=false`, and all 422 responses
// as request-shape failures for the generic retry loop. Model-support errors are
// excluded so routing can fall through to another auth or upstream.
func isRequestInvalidError(err error) bool {
	if err == nil {
		return false
	}
	if isModelSupportError(err) {
		return false
	}
	if isRequestScopedFeatureUnsupportedMessage(err.Error()) {
		return false
	}
	if isRequestScopedContentSafetyError(err) {
		return true
	}
	if isRequestScopedContextLimitError(err) {
		return true
	}
	status := statusCodeFromError(err)
	switch status {
	case http.StatusBadRequest:
		msg := err.Error()
		return (strings.Contains(msg, "invalid_request_error") && !isRetryableAvailabilityErrorMessage(msg)) ||
			strings.Contains(msg, "INVALID_ARGUMENT") ||
			strings.Contains(msg, "FAILED_PRECONDITION")
	case http.StatusUnavailableForLegalReasons:
		return false
	case http.StatusNotFound:
		return isRequestScopedNotFoundMessage(err.Error())
	case http.StatusUnprocessableEntity:
		return true
	case http.StatusInternalServerError:
		msg := err.Error()
		return strings.Contains(msg, "\"status\":\"UNKNOWN\"") ||
			strings.Contains(msg, "\"status\": \"UNKNOWN\"")
	default:
		return false
	}
}

func shouldDisableAuthForProxyFailure(auth *Auth, result Result) bool {
	if auth == nil || result.Success {
		return false
	}
	if strings.TrimSpace(auth.ProxyURL) == "" || !proxyutil.IsSOCKS5ProxyURL(auth.ProxyURL) {
		return false
	}
	return proxyutil.IsProxyDialError(result.Cause)
}

func disableAuthForProxyFailure(auth *Auth, result Result, now time.Time) {
	if auth == nil {
		return
	}
	auth.Disabled = true
	auth.Unavailable = true
	auth.Status = StatusDisabled
	auth.StatusMessage = "disabled due to SOCKS5 proxy failure"
	auth.NextRetryAfter = time.Time{}
	auth.Quota = QuotaState{}
	auth.UpdatedAt = now
	if result.Error != nil {
		auth.LastError = cloneError(result.Error)
	} else if result.Cause != nil {
		auth.LastError = &Error{Code: "proxy_dial_failed", Message: result.Cause.Error(), Retryable: true}
	}
	if result.Model != "" {
		state := ensureModelState(auth, result.Model)
		if state != nil {
			state.Status = StatusDisabled
			state.StatusMessage = auth.StatusMessage
			state.Unavailable = true
			state.NextRetryAfter = time.Time{}
			state.UpdatedAt = now
			if result.Error != nil {
				state.LastError = cloneError(result.Error)
			} else if result.Cause != nil {
				state.LastError = &Error{Code: "proxy_dial_failed", Message: result.Cause.Error(), Retryable: true}
			}
		}
	}
}

func disableAuthForBalanceExhausted(auth *Auth, result Result, now time.Time) {
	if auth == nil {
		return
	}
	auth.Disabled = true
	auth.Unavailable = true
	auth.Status = StatusDisabled
	auth.StatusMessage = "disabled due to insufficient balance"
	auth.NextRetryAfter = time.Time{}
	auth.Quota = QuotaState{}
	auth.UpdatedAt = now
	if result.Error != nil {
		auth.LastError = cloneError(result.Error)
	}
	if result.Model != "" {
		state := ensureModelState(auth, result.Model)
		if state != nil {
			state.Status = StatusDisabled
			state.StatusMessage = auth.StatusMessage
			state.Unavailable = true
			state.NextRetryAfter = time.Time{}
			state.Quota = QuotaState{}
			state.UpdatedAt = now
			if result.Error != nil {
				state.LastError = cloneError(result.Error)
			}
		}
	}
}

func applyAuthFailureState(auth *Auth, resultErr *Error, retryAfter *time.Duration, now time.Time) {
	if auth == nil {
		return
	}
	if isCodexAuth(auth) {
		clearAuthStateOnSuccess(auth, now)
		return
	}
	if isRequestScopedNotFoundResultError(resultErr) ||
		isRequestScopedFeatureUnsupportedResultError(resultErr) ||
		isRequestScopedContentSafetyResultError(resultErr) ||
		isRequestScopedContextLimitResultError(resultErr) ||
		isTransientRoutingResultError(resultErr) {
		return
	}
	applyHealthFailure(&auth.Health, now, statusCodeFromResult(resultErr))
	disableCooling := quotaCooldownDisabledForAuth(auth)
	auth.Unavailable = true
	auth.Status = StatusError
	auth.UpdatedAt = now
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
		if resultErr.Message != "" {
			auth.StatusMessage = resultErr.Message
		}
	}
	statusCode := statusCodeFromResult(resultErr)
	if isAccountQuotaExhaustedResultError(resultErr) {
		applyAccountQuotaFailureState(auth, nil, resultErr, retryAfter, now)
		return
	}
	switch statusCode {
	case 401:
		auth.StatusMessage = "unauthorized"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		}
	case 402, 403:
		auth.StatusMessage = "payment_required"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(30 * time.Minute)
		}
	case 404:
		auth.StatusMessage = "not_found"
		if disableCooling {
			auth.NextRetryAfter = time.Time{}
		} else {
			auth.NextRetryAfter = now.Add(12 * time.Hour)
		}
	case 429:
		auth.StatusMessage = "quota exhausted"
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		var next time.Time
		if !disableCooling && shouldHardCooldownQuota(auth.Health, retryAfter) {
			if retryAfter != nil {
				next = now.Add(*retryAfter)
			} else {
				cooldown, nextLevel := nextQuotaCooldown(auth.Quota.BackoffLevel, disableCooling)
				if cooldown > 0 {
					next = now.Add(cooldown)
				}
				auth.Quota.BackoffLevel = nextLevel
			}
			next = laterTime(next, auth.Health.OpenUntil)
		}
		auth.Quota.NextRecoverAt = next
		auth.NextRetryAfter = next
	default:
		if isTransientUpstreamStatus(statusCode) {
			auth.StatusMessage = "transient upstream error"
			if disableCooling {
				auth.NextRetryAfter = time.Time{}
			} else if next := transientHardCooldownUntil(auth.Health); !next.IsZero() {
				auth.NextRetryAfter = next
			} else {
				auth.NextRetryAfter = time.Time{}
			}
			return
		}
		if auth.StatusMessage == "" {
			auth.StatusMessage = "request failed"
		}
	}
}

func (m *Manager) evictAuth(ctx context.Context, authID string) error {
	authID = strings.TrimSpace(authID)
	if m == nil || authID == "" {
		return nil
	}

	var authSnapshot *Auth
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil {
		cfg = &internalconfig.Config{}
	}

	m.mu.Lock()
	if existing := m.auths[authID]; existing != nil {
		authSnapshot = existing.Clone()
		delete(m.auths, authID)
		m.rebuildAPIKeyModelAliasLocked(cfg)
	}
	m.mu.Unlock()

	if authSnapshot == nil {
		return nil
	}
	if m.scheduler != nil {
		m.scheduler.removeAuth(authID)
	}
	registry.GetGlobalRegistry().UnregisterClient(authID)

	if m.store == nil {
		return nil
	}
	if shouldSkipPersist(ctx) {
		return nil
	}
	if authSnapshot.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(authSnapshot.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	if authSnapshot.Metadata == nil {
		return nil
	}
	if err := m.store.Delete(ctx, authID); err != nil {
		return err
	}
	return nil
}

func (m *Manager) evictUnauthorizedAuth(ctx context.Context, auth *Auth, provider, model string) error {
	if auth == nil {
		return nil
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = strings.TrimSpace(auth.Provider)
	}
	model = strings.TrimSpace(model)

	entry := logEntryWithRequestID(ctx)
	if !deleteUnauthorizedAuthEnabled.Load() {
		if model != "" {
			entry.Infof("skip evicting unauthorized auth provider=%s auth=%s model=%s (delete-unauthorized-auth=false)", provider, auth.ID, model)
		} else {
			entry.Infof("skip evicting unauthorized auth provider=%s auth=%s (delete-unauthorized-auth=false)", provider, auth.ID)
		}
		return nil
	}
	if model != "" {
		entry.Infof("evicting unauthorized auth provider=%s auth=%s model=%s due to 401", provider, auth.ID, model)
	} else {
		entry.Infof("evicting unauthorized auth provider=%s auth=%s due to 401", provider, auth.ID)
	}

	return m.evictAuth(ctx, auth.ID)
}

// nextQuotaCooldown returns the next cooldown duration and updated backoff level for repeated quota errors.
func nextQuotaCooldown(prevLevel int, disableCooling bool) (time.Duration, int) {
	if prevLevel < 0 {
		prevLevel = 0
	}
	if disableCooling {
		return 0, prevLevel
	}
	cooldown := quotaBackoffBase * time.Duration(1<<prevLevel)
	if cooldown < quotaBackoffBase {
		cooldown = quotaBackoffBase
	}
	if cooldown >= quotaBackoffMax {
		return quotaBackoffMax, prevLevel
	}
	return cooldown, prevLevel + 1
}

// List returns all auth entries currently known by the manager.
func (m *Manager) List() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		list = append(list, auth.Clone())
	}
	return list
}

// ResolveConfiguredProviders infers provider keys for a route model directly from
// the current auth set and runtime config. It is a safety net for moments when
// the shared model registry temporarily lacks a model registration even though
// the active config still contains matching credentials.
func (m *Manager) ResolveConfiguredProviders(routeModel string) []string {
	if m == nil {
		return nil
	}
	routeModel = strings.TrimSpace(routeModel)
	if routeModel == "" {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]string, 0, len(m.auths))
	seen := make(map[string]struct{}, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		if providerKey == "" {
			continue
		}
		if _, exists := seen[providerKey]; exists {
			continue
		}
		if _, hasExecutor := m.executors[providerKey]; !hasExecutor {
			continue
		}
		if !m.authMatchesConfiguredRouteModel(auth, routeModel) {
			continue
		}
		seen[providerKey] = struct{}{}
		out = append(out, providerKey)
	}
	return out
}

func (m *Manager) authMatchesConfiguredRouteModel(auth *Auth, routeModel string) bool {
	if m == nil || auth == nil {
		return false
	}

	requestedModel := rewriteModelForAuth(routeModel, auth)
	if strings.TrimSpace(requestedModel) == "" {
		requestedModel = strings.TrimSpace(routeModel)
	}
	if requestedModel == "" {
		return false
	}

	if pool := m.resolveOAuthUpstreamModelPool(auth, requestedModel); len(pool) > 0 {
		return true
	}
	if pool := m.resolveAPIKeyUpstreamModelPool(auth, requestedModel); len(pool) > 0 {
		return true
	}
	if pool := m.resolveOpenAICompatUpstreamModelPool(auth, requestedModel); len(pool) > 0 {
		return true
	}
	if auth.Attributes != nil {
		if homeModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey]); homeModel != "" &&
			canonicalModelKey(homeModel) == canonicalModelKey(requestedModel) {
			return true
		}
	}
	if authSupportsDirectProviderRouteModel(auth, requestedModel) {
		return true
	}
	return false
}

func authSupportsDirectProviderRouteModel(auth *Auth, routeModel string) bool {
	if auth == nil || authRequiresRegisteredModels(auth) {
		return false
	}
	modelKey := canonicalModelKey(routeModel)
	if modelKey == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "claude":
		return strings.HasPrefix(modelKey, "claude-")
	default:
		return false
	}
}

// GetByID retrieves an auth entry by its ID.

func (m *Manager) GetByID(id string) (*Auth, bool) {
	if id == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth, ok := m.auths[id]
	if !ok {
		return nil, false
	}
	return auth.Clone(), true
}

// GetExecutionSessionAuthByID retrieves a Home runtime auth scoped to an execution session.
func (m *Manager) GetExecutionSessionAuthByID(sessionID string, authID string) (*Auth, bool) {
	sessionID = strings.TrimSpace(sessionID)
	authID = strings.TrimSpace(authID)
	if m == nil || sessionID == "" || authID == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	sessionAuths := m.homeRuntimeAuths[sessionID]
	auth := sessionAuths[authID]
	if auth == nil {
		return nil, false
	}
	return auth.Clone(), true
}

// Executor returns the registered provider executor for a provider key.
func (m *Manager) Executor(provider string) (ProviderExecutor, bool) {
	if m == nil {
		return nil, false
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, false
	}

	m.mu.RLock()
	executor, okExecutor := m.executors[provider]
	if !okExecutor {
		lowerProvider := strings.ToLower(provider)
		if lowerProvider != provider {
			executor, okExecutor = m.executors[lowerProvider]
		}
	}
	m.mu.RUnlock()

	if !okExecutor || executor == nil {
		return nil, false
	}
	return executor, true
}

// CloseExecutionSession asks all registered executors to release the supplied execution session.
func (m *Manager) CloseExecutionSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}

	m.mu.Lock()
	if sessionID == CloseAllExecutionSessionsID {
		m.clearHomeRuntimeAuthsLocked()
	} else {
		m.clearHomeRuntimeAuthsForSessionLocked(sessionID)
	}
	executors := make([]ProviderExecutor, 0, len(m.executors))
	for _, exec := range m.executors {
		executors = append(executors, exec)
	}
	m.mu.Unlock()

	for i := range executors {
		if closer, ok := executors[i].(ExecutionSessionCloser); ok && closer != nil {
			closer.CloseExecutionSession(sessionID)
		}
	}
}

func (m *Manager) useSchedulerFastPath() bool {
	if m == nil || m.scheduler == nil {
		return false
	}
	if m.hasRoutingStrategyOverrides() {
		return false
	}
	return isBuiltInSelector(m.selector)
}

func shouldRetrySchedulerPick(err error) bool {
	if err == nil {
		return false
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) {
		return true
	}
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	return authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable"
}

func (m *Manager) routeAwareSelectionRequired(auth *Auth, routeModel string) bool {
	if auth == nil || strings.TrimSpace(routeModel) == "" {
		return false
	}
	return m.selectionModelKeyForAuth(auth, routeModel) != canonicalModelKey(routeModel)
}

func (m *Manager) pickNextLegacy(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	if m.HomeEnabled() {
		auth, exec, _, err := m.pickNextViaHome(ctx, model, opts, tried)
		return auth, exec, err
	}

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	localTried := copyTriedMap(tried)
	disallowFreeAuth := disallowFreeAuthFromMetadata(opts.Metadata)

	m.mu.RLock()
	executor, okExecutor := m.executors[provider]
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	// Always use base model name (without thinking suffix) for auth matching.
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate.Provider != provider || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if disallowFreeAuth && isFreeCodexAuth(candidate) {
			continue
		}
		if _, used := localTried[candidate.ID]; used {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	for {
		available, errAvailable := m.availableAuthsForRouteModel(candidates, provider, model, time.Now())
		if errAvailable != nil {
			m.mu.RUnlock()
			return nil, nil, errAvailable
		}
		selector := m.selectorForAuths(available)
		selected, errPick := selector.Pick(ctx, provider, selectionArgForSelector(selector, model), opts, available)
		if errPick != nil {
			m.mu.RUnlock()
			return nil, nil, errPick
		}
		if selected == nil {
			m.mu.RUnlock()
			return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
		}
		checkModel := m.selectionModelForAuth(selected, model)
		probeNow := time.Now()
		if healthRequiresHalfOpenProbe(selected, checkModel, probeNow) && !m.halfOpenProbeActive(selected.ID, checkModel, probeNow) {
			if okReserve, _ := m.reserveHalfOpenProbe(selected.ID, checkModel, probeNow); !okReserve {
				localTried[selected.ID] = struct{}{}
				candidates = candidates[:0]
				for _, candidate := range m.auths {
					if candidate.Provider != provider || candidate.Disabled {
						continue
					}
					if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
						continue
					}
					if disallowFreeAuth && isFreeCodexAuth(candidate) {
						continue
					}
					if _, used := localTried[candidate.ID]; used {
						continue
					}
					if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
						continue
					}
					candidates = append(candidates, candidate)
				}
				if len(candidates) == 0 {
					m.mu.RUnlock()
					return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
				}
				continue
			}
		}
		authCopy := selected.Clone()
		m.mu.RUnlock()
		if !selected.indexAssigned {
			m.mu.Lock()
			if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
				current.EnsureIndex()
				authCopy = current.Clone()
			}
			m.mu.Unlock()
		}
		return authCopy, executor, nil
	}
}

func (m *Manager) pickNext(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	if m.HomeEnabled() {
		auth, exec, _, err := m.pickNextViaHome(ctx, model, opts, tried)
		return auth, exec, err
	}

	if !m.useSchedulerFastPath() {
		return m.pickNextLegacy(ctx, provider, model, opts, tried)
	}
	if strings.TrimSpace(model) != "" {
		m.mu.RLock()
		for _, candidate := range m.auths {
			if candidate == nil || candidate.Provider != provider || candidate.Disabled {
				continue
			}
			if _, used := tried[candidate.ID]; used {
				continue
			}
			if m.routeAwareSelectionRequired(candidate, model) {
				m.mu.RUnlock()
				return m.pickNextLegacy(ctx, provider, model, opts, tried)
			}
		}
		m.mu.RUnlock()
	}
	executor, okExecutor := m.Executor(provider)
	if !okExecutor {
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	disallowFreeAuth := disallowFreeAuthFromMetadata(opts.Metadata)
	for {
		selected, errPick := m.scheduler.pickSingle(ctx, provider, model, opts, tried)
		if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
			m.syncScheduler()
			selected, errPick = m.scheduler.pickSingle(ctx, provider, model, opts, tried)
			if errPick != nil {
				if fallbackAuth, fallbackExecutor, errFallback := m.pickNextLegacy(ctx, provider, model, opts, tried); errFallback == nil {
					return fallbackAuth, fallbackExecutor, nil
				}
			}
		}
		if errPick != nil {
			return nil, nil, errPick
		}
		if selected == nil {
			return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
		}
		if disallowFreeAuth && isFreeCodexAuth(selected) {
			if tried == nil {
				tried = make(map[string]struct{})
			}
			tried[selected.ID] = struct{}{}
			continue
		}
		authCopy := selected.Clone()
		if !selected.indexAssigned {
			m.mu.Lock()
			if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
				current.EnsureIndex()
				authCopy = current.Clone()
			}
			m.mu.Unlock()
		}
		return authCopy, executor, nil
	}
}

func (m *Manager) pickNextMixedLegacy(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m.HomeEnabled() {
		return m.pickNextViaHome(ctx, model, opts, tried)
	}

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	localTried := copyTriedMap(tried)
	disallowFreeAuth := disallowFreeAuthFromMetadata(opts.Metadata)

	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.TrimSpace(strings.ToLower(provider))
		if p == "" {
			continue
		}
		providerSet[p] = struct{}{}
	}
	if len(providerSet) == 0 {
		return nil, nil, "", &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}

	m.mu.RLock()
	candidates := make([]*Auth, 0, len(m.auths))
	modelKey := strings.TrimSpace(model)
	// Always use base model name (without thinking suffix) for auth matching.
	if modelKey != "" {
		parsed := thinking.ParseSuffix(modelKey)
		if parsed.ModelName != "" {
			modelKey = strings.TrimSpace(parsed.ModelName)
		}
	}
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate == nil || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if disallowFreeAuth && isFreeCodexAuth(candidate) {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(candidate.Provider))
		if providerKey == "" {
			continue
		}
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if _, used := localTried[candidate.ID]; used {
			continue
		}
		if _, ok := m.executors[providerKey]; !ok {
			continue
		}
		if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		m.mu.RUnlock()
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	for {
		available, errAvailable := m.availableAuthsForRouteModel(candidates, "mixed", model, time.Now())
		if errAvailable != nil {
			m.mu.RUnlock()
			return nil, nil, "", errAvailable
		}
		selector := m.selectorForAuths(available)
		selected, errPick := selector.Pick(ctx, "mixed", selectionArgForSelector(selector, model), opts, available)
		if errPick != nil {
			m.mu.RUnlock()
			return nil, nil, "", errPick
		}
		if selected == nil {
			m.mu.RUnlock()
			return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
		}
		checkModel := m.selectionModelForAuth(selected, model)
		probeNow := time.Now()
		if healthRequiresHalfOpenProbe(selected, checkModel, probeNow) && !m.halfOpenProbeActive(selected.ID, checkModel, probeNow) {
			if okReserve, _ := m.reserveHalfOpenProbe(selected.ID, checkModel, probeNow); !okReserve {
				localTried[selected.ID] = struct{}{}
				candidates = candidates[:0]
				for _, candidate := range m.auths {
					if candidate == nil || candidate.Disabled {
						continue
					}
					if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
						continue
					}
					if disallowFreeAuth && isFreeCodexAuth(candidate) {
						continue
					}
					providerKey := strings.TrimSpace(strings.ToLower(candidate.Provider))
					if providerKey == "" {
						continue
					}
					if _, ok := providerSet[providerKey]; !ok {
						continue
					}
					if _, used := localTried[candidate.ID]; used {
						continue
					}
					if _, ok := m.executors[providerKey]; !ok {
						continue
					}
					if modelKey != "" && !m.authSupportsRouteModel(registryRef, candidate, model) {
						continue
					}
					candidates = append(candidates, candidate)
				}
				if len(candidates) == 0 {
					m.mu.RUnlock()
					return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
				}
				continue
			}
		}
		providerKey := strings.TrimSpace(strings.ToLower(selected.Provider))
		executor, okExecutor := m.executors[providerKey]
		if !okExecutor {
			m.mu.RUnlock()
			return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
		}
		authCopy := selected.Clone()
		m.mu.RUnlock()
		if !selected.indexAssigned {
			m.mu.Lock()
			if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
				current.EnsureIndex()
				authCopy = current.Clone()
			}
			m.mu.Unlock()
		}
		return authCopy, executor, providerKey, nil
	}
}

func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m.HomeEnabled() {
		return m.pickNextViaHome(ctx, model, opts, tried)
	}

	if !m.useSchedulerFastPath() {
		return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
	}

	eligibleProviders := make([]string, 0, len(providers))
	seenProviders := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := strings.TrimSpace(strings.ToLower(provider))
		if providerKey == "" {
			continue
		}
		if _, seen := seenProviders[providerKey]; seen {
			continue
		}
		if _, okExecutor := m.Executor(providerKey); !okExecutor {
			continue
		}
		seenProviders[providerKey] = struct{}{}
		eligibleProviders = append(eligibleProviders, providerKey)
	}
	if len(eligibleProviders) == 0 {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if strings.TrimSpace(model) != "" {
		providerSet := make(map[string]struct{}, len(eligibleProviders))
		for _, providerKey := range eligibleProviders {
			providerSet[providerKey] = struct{}{}
		}
		m.mu.RLock()
		for _, candidate := range m.auths {
			if candidate == nil || candidate.Disabled {
				continue
			}
			if _, ok := providerSet[strings.TrimSpace(strings.ToLower(candidate.Provider))]; !ok {
				continue
			}
			if _, used := tried[candidate.ID]; used {
				continue
			}
			if m.routeAwareSelectionRequired(candidate, model) {
				m.mu.RUnlock()
				return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
			}
		}
		m.mu.RUnlock()
	}

	disallowFreeAuth := disallowFreeAuthFromMetadata(opts.Metadata)
	for {
		selected, providerKey, errPick := m.scheduler.pickMixed(ctx, eligibleProviders, model, opts, tried)
		if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
			m.syncScheduler()
			selected, providerKey, errPick = m.scheduler.pickMixed(ctx, eligibleProviders, model, opts, tried)
			if errPick != nil {
				if fallbackAuth, fallbackExecutor, fallbackProvider, errFallback := m.pickNextMixedLegacy(ctx, providers, model, opts, tried); errFallback == nil {
					return fallbackAuth, fallbackExecutor, fallbackProvider, nil
				}
			}
		}
		if errPick != nil {
			return nil, nil, "", errPick
		}
		if selected == nil {
			return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
		}
		if disallowFreeAuth && isFreeCodexAuth(selected) {
			if tried == nil {
				tried = make(map[string]struct{})
			}
			tried[selected.ID] = struct{}{}
			continue
		}
		executor, okExecutor := m.Executor(providerKey)
		if !okExecutor {
			return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered"}
		}
		authCopy := selected.Clone()
		if !selected.indexAssigned {
			m.mu.Lock()
			if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
				current.EnsureIndex()
				authCopy = current.Clone()
			}
			m.mu.Unlock()
		}
		return authCopy, executor, providerKey, nil
	}
}

type homeErrorEnvelope struct {
	Error *homeErrorDetail `json:"error"`
}

type homeErrorDetail struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Code    string `json:"code,omitempty"`
}

const (
	homeUpstreamModelAttributeKey     = "home_upstream_model"
	homeRequestRetryExceededErrorCode = "request_retry_exceeded"
)

func isHomeRequestRetryExceededError(err error) bool {
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(authErr.Code), homeRequestRetryExceededErrorCode)
}

func shouldReturnLastErrorOnPickFailure(homeMode bool, lastErr error, errPick error) bool {
	if lastErr == nil {
		return false
	}
	if !homeMode {
		return true
	}
	return isHomeRequestRetryExceededError(errPick)
}

type homeAuthDispatchResponse struct {
	Model      string `json:"model"`
	Provider   string `json:"provider"`
	AuthIndex  string `json:"auth_index"`
	UserAPIKey string `json:"user_api_key"`
	Auth       Auth   `json:"auth"`
}

func setHomeUserAPIKeyOnGinContext(ctx context.Context, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || ctx == nil {
		return
	}
	ginCtx, ok := ctx.Value("gin").(interface{ Set(string, any) })
	if !ok || ginCtx == nil {
		return
	}
	ginCtx.Set("userApiKey", apiKey)
}

func homeDispatchHeaders(ctx context.Context, headers http.Header) http.Header {
	apiKey, ok := homeQueryCredentialFromContext(ctx)
	if !ok {
		return headers
	}
	out := headers.Clone()
	if out == nil {
		out = http.Header{}
	}
	if out.Get("Authorization") != "" || out.Get("X-Goog-Api-Key") != "" || out.Get("X-Api-Key") != "" {
		return out
	}
	out.Set("X-Goog-Api-Key", apiKey)
	return out
}

func homeQueryCredentialFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	if queryCtx, ok := ctx.Value("gin").(interface{ Query(string) string }); ok && queryCtx != nil {
		if apiKey := strings.TrimSpace(queryCtx.Query("key")); apiKey != "" {
			return apiKey, true
		}
		if apiKey := strings.TrimSpace(queryCtx.Query("auth_token")); apiKey != "" {
			return apiKey, true
		}
	}
	ginCtx, ok := ctx.Value("gin").(interface{ Get(string) (any, bool) })
	if !ok || ginCtx == nil {
		return "", false
	}
	rawMetadata, ok := ginCtx.Get("accessMetadata")
	if !ok {
		return "", false
	}
	source := accessMetadataSource(rawMetadata)
	if source != "query-key" && source != "query-auth-token" {
		return "", false
	}
	rawAPIKey, ok := ginCtx.Get("userApiKey")
	if !ok {
		return "", false
	}
	apiKey := contextStringValue(rawAPIKey)
	if apiKey == "" {
		return "", false
	}
	return apiKey, true
}

func accessMetadataSource(raw any) string {
	switch v := raw.(type) {
	case map[string]string:
		return strings.TrimSpace(v["source"])
	case map[string]any:
		return contextStringValue(v["source"])
	default:
		return ""
	}
}

func contextStringValue(raw any) string {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func homeExecutionSessionIDFromMetadata(meta map[string]any) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[cliproxyexecutor.ExecutionSessionMetadataKey]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func (m *Manager) clearHomeRuntimeAuths() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.clearHomeRuntimeAuthsLocked()
	m.mu.Unlock()
}

func (m *Manager) clearHomeRuntimeAuthsLocked() {
	if m == nil {
		return
	}
	m.homeRuntimeAuths = make(map[string]map[string]*Auth)
}

func (m *Manager) clearHomeRuntimeAuthsForSessionLocked(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}
	delete(m.homeRuntimeAuths, sessionID)
}

func (m *Manager) rememberHomeRuntimeAuth(sessionID string, auth *Auth) {
	sessionID = strings.TrimSpace(sessionID)
	authID := ""
	if auth != nil {
		authID = strings.TrimSpace(auth.ID)
	}
	if m == nil || auth == nil || sessionID == "" || authID == "" || !authWebsocketsEnabled(auth) {
		return
	}
	m.mu.Lock()
	if m.homeRuntimeAuths == nil {
		m.homeRuntimeAuths = make(map[string]map[string]*Auth)
	}
	sessionAuths := m.homeRuntimeAuths[sessionID]
	if sessionAuths == nil {
		sessionAuths = make(map[string]*Auth)
		m.homeRuntimeAuths[sessionID] = sessionAuths
	}
	sessionAuths[authID] = auth.Clone()
	m.mu.Unlock()
}

func (m *Manager) homeRuntimeAuthByID(sessionID string, authID string) (*Auth, ProviderExecutor, string, bool) {
	sessionID = strings.TrimSpace(sessionID)
	authID = strings.TrimSpace(authID)
	if m == nil || sessionID == "" || authID == "" {
		return nil, nil, "", false
	}
	m.mu.RLock()
	sessionAuths := m.homeRuntimeAuths[sessionID]
	auth := sessionAuths[authID]
	m.mu.RUnlock()
	if auth == nil || !authWebsocketsEnabled(auth) {
		return nil, nil, "", false
	}
	providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
	if providerKey == "" {
		return nil, nil, "", false
	}
	executor, ok := m.Executor(providerKey)
	if !ok && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["base_url"]) != "" {
		executor, ok = m.Executor("openai-compatibility")
		if ok {
			providerKey = "openai-compatibility"
		}
	}
	if !ok {
		return nil, nil, "", false
	}
	return auth.Clone(), executor, providerKey, true
}

func (m *Manager) pickNextViaHome(ctx context.Context, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	executionSessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	count := homeAuthCountFromMetadata(opts.Metadata)
	if cliproxyexecutor.DownstreamWebsocket(ctx) && executionSessionID != "" && count <= 1 {
		if pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata); pinnedAuthID != "" {
			_, alreadyTried := tried[pinnedAuthID]
			if !alreadyTried {
				if auth, executor, providerKey, ok := m.homeRuntimeAuthByID(executionSessionID, pinnedAuthID); ok {
					return auth, executor, providerKey, nil
				}
			}
		}
	}

	client := home.Current()
	if client == nil || !client.HeartbeatOK() {
		return nil, nil, "", &Error{Code: "home_unavailable", Message: "home control center unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}

	requestedModel := requestedModelFromMetadata(opts.Metadata, model)
	sessionID := ExtractSessionID(opts.Headers, opts.OriginalRequest, opts.Metadata)
	dispatchHeaders := homeDispatchHeaders(ctx, opts.Headers)

	raw, err := client.RPopAuth(ctx, requestedModel, sessionID, dispatchHeaders, count)
	if err != nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: err.Error(), HTTPStatus: http.StatusServiceUnavailable}
	}

	var env homeErrorEnvelope
	if errUnmarshal := json.Unmarshal(raw, &env); errUnmarshal == nil && env.Error != nil {
		code := strings.TrimSpace(env.Error.Type)
		if code == "" {
			code = strings.TrimSpace(env.Error.Code)
		}
		msg := strings.TrimSpace(env.Error.Message)
		if msg == "" {
			msg = "home returned error"
		}
		status := http.StatusBadGateway
		switch strings.ToLower(code) {
		case "model_not_found":
			status = http.StatusNotFound
		case "authentication_error", "unauthorized":
			status = http.StatusUnauthorized
		}
		return nil, nil, "", &Error{Code: code, Message: msg, HTTPStatus: status}
	}

	var dispatch homeAuthDispatchResponse
	if errUnmarshal := json.Unmarshal(raw, &dispatch); errUnmarshal != nil {
		return nil, nil, "", &Error{Code: "invalid_auth", Message: "home returned invalid auth payload", HTTPStatus: http.StatusBadGateway}
	}
	setHomeUserAPIKeyOnGinContext(ctx, dispatch.UserAPIKey)
	auth := dispatch.Auth
	if strings.TrimSpace(auth.ID) == "" {
		// Backward compatibility: older home instances returned the auth directly.
		if errUnmarshal := json.Unmarshal(raw, &auth); errUnmarshal != nil {
			return nil, nil, "", &Error{Code: "invalid_auth", Message: "home returned invalid auth payload", HTTPStatus: http.StatusBadGateway}
		}
	}
	if upstreamModel := strings.TrimSpace(dispatch.Model); upstreamModel != "" {
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string, 1)
		}
		auth.Attributes[homeUpstreamModelAttributeKey] = upstreamModel
	}
	if strings.TrimSpace(auth.ID) == "" {
		return nil, nil, "", &Error{Code: "invalid_auth", Message: "home returned auth without id", HTTPStatus: http.StatusBadGateway}
	}
	providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
	if providerKey == "" {
		return nil, nil, "", &Error{Code: "invalid_auth", Message: "home returned auth without provider", HTTPStatus: http.StatusBadGateway}
	}

	homeAuthIndex := strings.TrimSpace(dispatch.AuthIndex)
	if homeAuthIndex != "" {
		auth.Index = homeAuthIndex
		auth.indexAssigned = true
	} else {
		auth.EnsureIndex()
	}

	executor, ok := m.Executor(providerKey)
	if !ok && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["base_url"]) != "" {
		executor, ok = m.Executor("openai-compatibility")
		if ok {
			providerKey = "openai-compatibility"
		}
	}
	if !ok {
		return nil, nil, "", &Error{Code: "executor_not_found", Message: "executor not registered", HTTPStatus: http.StatusBadGateway}
	}

	authCopy := auth.Clone()
	if cliproxyexecutor.DownstreamWebsocket(ctx) && executionSessionID != "" && authWebsocketsEnabled(authCopy) {
		m.rememberHomeRuntimeAuth(executionSessionID, authCopy)
	}
	return authCopy, executor, providerKey, nil
}

func requestedModelFromMetadata(metadata map[string]any, fallback string) string {
	if metadata != nil {
		if v, ok := metadata[cliproxyexecutor.RequestedModelMetadataKey]; ok {
			switch typed := v.(type) {
			case string:
				if trimmed := strings.TrimSpace(typed); trimmed != "" {
					return trimmed
				}
			case []byte:
				if trimmed := strings.TrimSpace(string(typed)); trimmed != "" {
					return trimmed
				}
			}
		}
	}
	fallback = strings.TrimSpace(fallback)
	if fallback == "" {
		return "unknown"
	}
	return fallback
}

func (m *Manager) findAllAntigravityCreditsCandidateAuths(routeModel string, opts cliproxyexecutor.Options) []creditsCandidateEntry {
	if m == nil {
		return nil
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	m.mu.RLock()
	defer m.mu.RUnlock()
	var known []creditsCandidateEntry
	var unknown []creditsCandidateEntry
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if pinnedAuthID != "" && auth.ID != pinnedAuthID {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
			continue
		}
		if !strings.Contains(strings.ToLower(strings.TrimSpace(routeModel)), "claude") {
			continue
		}
		providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
		executor, ok := m.executors[providerKey]
		if !ok {
			continue
		}

		hint, okHint := GetAntigravityCreditsHint(auth.ID)
		if okHint && hint.Known {
			if !hint.Available {
				continue
			}
			known = append(known, creditsCandidateEntry{
				auth:     auth.Clone(),
				executor: executor,
				provider: providerKey,
			})
			continue
		}
		unknown = append(unknown, creditsCandidateEntry{
			auth:     auth.Clone(),
			executor: executor,
			provider: providerKey,
		})
	}
	sort.Slice(known, func(i, j int) bool {
		return known[i].auth.ID < known[j].auth.ID
	})
	sort.Slice(unknown, func(i, j int) bool {
		return unknown[i].auth.ID < unknown[j].auth.ID
	})
	return append(known, unknown...)
}

type creditsCandidateEntry struct {
	auth     *Auth
	executor ProviderExecutor
	provider string
}

func hasAntigravityProvider(providers []string) bool {
	for _, p := range providers {
		if strings.EqualFold(strings.TrimSpace(p), "antigravity") {
			return true
		}
	}
	return false
}

func shouldAttemptAntigravityCreditsFallback(m *Manager, lastErr error, providers []string) bool {
	status := statusCodeFromError(lastErr)
	log.WithFields(log.Fields{
		"lastErr":   errorString(lastErr),
		"status":    status,
		"providers": providers,
	}).Debug("shouldAttemptAntigravityCreditsFallback")
	if m == nil || lastErr == nil {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || !cfg.QuotaExceeded.AntigravityCredits {
		return false
	}
	switch status {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	case 0:
		var authErr *Error
		if errors.As(lastErr, &authErr) && authErr != nil {
			return authErr.Code == "auth_not_found" || authErr.Code == "auth_unavailable" || authErr.Code == "model_cooldown"
		}
		var cooldownErr *modelCooldownError
		if errors.As(lastErr, &cooldownErr) {
			return true
		}
		return false
	default:
		return false
	}
}

func (m *Manager) tryAntigravityCreditsExecute(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, bool) {
	routeModel := req.Model
	candidates := m.findAllAntigravityCreditsCandidateAuths(routeModel, opts)
	for _, c := range candidates {
		if ctx.Err() != nil {
			return cliproxyexecutor.Response{}, false
		}
		creditsCtx := WithAntigravityCredits(ctx)
		if rt := m.roundTripperFor(c.auth); rt != nil {
			creditsCtx = context.WithValue(creditsCtx, roundTripperContextKey{}, rt)
			creditsCtx = context.WithValue(creditsCtx, "cliproxy.roundtripper", rt)
		}
		creditsOpts := ensureRequestedModelMetadata(opts, routeModel)
		creditsCtx = contextWithRequestedModelAlias(creditsCtx, creditsOpts, routeModel)
		preparedAuth, errPrepare := m.prepareRequestAuth(creditsCtx, c.executor, c.auth)
		if errPrepare != nil {
			continue
		}
		c.auth = preparedAuth
		publishSelectedAuthMetadata(creditsOpts.Metadata, c.auth.ID)
		models := m.executionModelCandidates(c.auth, routeModel)
		if len(models) == 0 {
			continue
		}
		for _, upstreamModel := range models {
			resultModel := m.stateModelForExecution(c.auth, routeModel, upstreamModel, len(models) > 1)
			execReq := req
			execReq.Model = upstreamModel
			resp, errExec := c.executor.Execute(creditsCtx, c.auth, execReq, creditsOpts)
			result := Result{AuthID: c.auth.ID, Provider: c.provider, Model: resultModel, Success: errExec == nil}
			if errExec != nil {
				result.Error = resultErrorFromCause(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				m.MarkResult(creditsCtx, result)
				continue
			}
			m.MarkResult(creditsCtx, result)
			return resp, true
		}
	}
	return cliproxyexecutor.Response{}, false
}

func (m *Manager) tryAntigravityCreditsExecuteStream(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, bool) {
	routeModel := req.Model
	candidates := m.findAllAntigravityCreditsCandidateAuths(routeModel, opts)
	for _, c := range candidates {
		if ctx.Err() != nil {
			return nil, false
		}
		creditsCtx := WithAntigravityCredits(ctx)
		if rt := m.roundTripperFor(c.auth); rt != nil {
			creditsCtx = context.WithValue(creditsCtx, roundTripperContextKey{}, rt)
			creditsCtx = context.WithValue(creditsCtx, "cliproxy.roundtripper", rt)
		}
		creditsOpts := ensureRequestedModelMetadata(opts, routeModel)
		preparedAuth, errPrepare := m.prepareRequestAuth(creditsCtx, c.executor, c.auth)
		if errPrepare != nil {
			continue
		}
		c.auth = preparedAuth
		publishSelectedAuthMetadata(creditsOpts.Metadata, c.auth.ID)
		models := m.executionModelCandidates(c.auth, routeModel)
		if len(models) == 0 {
			continue
		}
		result, errStream := m.executeStreamWithModelPool(creditsCtx, c.executor, c.auth, c.provider, req, creditsOpts, routeModel, models, len(models) > 1)
		if errStream != nil {
			continue
		}
		return result, true
	}
	return nil, false
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil {
		return nil
	}
	if shouldSkipPersist(ctx) {
		return nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	// Skip persistence when metadata is absent (e.g., runtime-only auths).
	if auth.Metadata == nil {
		return nil
	}
	_, err := m.store.Save(ctx, auth)
	return err
}

// StartAutoRefresh launches a background loop that evaluates auth freshness
// every few seconds and triggers refresh operations when required.
// Only one loop is kept alive; starting a new one cancels the previous run.
func (m *Manager) StartAutoRefresh(parent context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = refreshCheckInterval
	}

	m.mu.Lock()
	cancelPrev := m.refreshCancel
	m.refreshCancel = nil
	m.refreshLoop = nil
	m.mu.Unlock()
	if cancelPrev != nil {
		cancelPrev()
	}

	ctx, cancelCtx := context.WithCancel(parent)
	workers := refreshMaxConcurrency
	if cfg, ok := m.runtimeConfig.Load().(*internalconfig.Config); ok && cfg != nil && cfg.AuthAutoRefreshWorkers > 0 {
		workers = cfg.AuthAutoRefreshWorkers
	}
	loop := newAuthAutoRefreshLoop(m, interval, workers)

	m.mu.Lock()
	m.refreshCancel = cancelCtx
	m.refreshLoop = loop
	m.mu.Unlock()

	loop.rebuild(time.Now())
	go loop.run(ctx)
}

// StopAutoRefresh cancels the background refresh loop, if running.
// It also stops the selector if it implements StoppableSelector.
func (m *Manager) StopAutoRefresh() {
	m.mu.Lock()
	cancel := m.refreshCancel
	m.refreshCancel = nil
	m.refreshLoop = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Stop selector if it implements StoppableSelector (e.g., SessionAffinitySelector)
	if stoppable, ok := m.selector.(StoppableSelector); ok {
		stoppable.Stop()
	}
	m.stopDynamicSelectors()
}

func (m *Manager) queueRefreshReschedule(authID string) {
	if m == nil || authID == "" {
		return
	}
	m.mu.RLock()
	loop := m.refreshLoop
	m.mu.RUnlock()
	if loop == nil {
		return
	}
	loop.queueReschedule(authID)
}

func (m *Manager) shouldRefresh(a *Auth, now time.Time) bool {
	if a == nil {
		return false
	}
	if hasUnauthorizedAuthFailure(a) {
		return false
	}
	if !a.NextRefreshAfter.IsZero() && now.Before(a.NextRefreshAfter) {
		return false
	}
	if evaluator, ok := a.Runtime.(RefreshEvaluator); ok && evaluator != nil {
		return evaluator.ShouldRefresh(now, a)
	}

	lastRefresh := a.LastRefreshedAt
	if lastRefresh.IsZero() {
		if ts, ok := authLastRefreshTimestamp(a); ok {
			lastRefresh = ts
		}
	}

	expiry, hasExpiry := a.ExpirationTime()

	if interval := authPreferredInterval(a); interval > 0 {
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) {
				return true
			}
			if expiry.Sub(now) <= interval {
				return true
			}
		}
		if lastRefresh.IsZero() {
			return true
		}
		return now.Sub(lastRefresh) >= interval
	}

	provider := strings.ToLower(a.Provider)
	lead := ProviderRefreshLead(provider, a.Runtime)
	if lead == nil {
		return false
	}
	if *lead <= 0 {
		if hasExpiry && !expiry.IsZero() {
			return now.After(expiry)
		}
		return false
	}
	if hasExpiry && !expiry.IsZero() {
		return time.Until(expiry) <= *lead
	}
	if !lastRefresh.IsZero() {
		return now.Sub(lastRefresh) >= *lead
	}
	return true
}

func authPreferredInterval(a *Auth) time.Duration {
	if a == nil {
		return 0
	}
	if d := durationFromMetadata(a.Metadata, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	if d := durationFromAttributes(a.Attributes, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	return 0
}

func durationFromMetadata(meta map[string]any, keys ...string) time.Duration {
	if len(meta) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if dur := parseDurationValue(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func durationFromAttributes(attrs map[string]string, keys ...string) time.Duration {
	if len(attrs) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := attrs[key]; ok {
			if dur := parseDurationString(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

func parseDurationValue(val any) time.Duration {
	switch v := val.(type) {
	case time.Duration:
		if v <= 0 {
			return 0
		}
		return v
	case int:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int32:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case int64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint32:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case uint64:
		if v == 0 {
			return 0
		}
		return time.Duration(v) * time.Second
	case float32:
		if v <= 0 {
			return 0
		}
		return time.Duration(float64(v) * float64(time.Second))
	case float64:
		if v <= 0 {
			return 0
		}
		return time.Duration(v * float64(time.Second))
	case json.Number:
		if i, err := v.Int64(); err == nil {
			if i <= 0 {
				return 0
			}
			return time.Duration(i) * time.Second
		}
		if f, err := v.Float64(); err == nil && f > 0 {
			return time.Duration(f * float64(time.Second))
		}
	case string:
		return parseDurationString(v)
	}
	return 0
}

func parseDurationString(raw string) time.Duration {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	if dur, err := time.ParseDuration(s); err == nil && dur > 0 {
		return dur
	}
	if secs, err := strconv.ParseFloat(s, 64); err == nil && secs > 0 {
		return time.Duration(secs * float64(time.Second))
	}
	return 0
}

func authLastRefreshTimestamp(a *Auth) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if a.Metadata != nil {
		if ts, ok := lookupMetadataTime(a.Metadata, "last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"); ok {
			return ts, true
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"} {
			if val := strings.TrimSpace(a.Attributes[key]); val != "" {
				if ts, ok := parseTimeValue(val); ok {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

func lookupMetadataTime(meta map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func (m *Manager) markRefreshPending(id string, now time.Time) bool {
	m.mu.Lock()
	auth, ok := m.auths[id]
	if !ok || auth == nil {
		m.mu.Unlock()
		return false
	}
	if !auth.NextRefreshAfter.IsZero() && now.Before(auth.NextRefreshAfter) {
		m.mu.Unlock()
		return false
	}
	auth.NextRefreshAfter = now.Add(refreshPendingBackoff)
	m.auths[id] = auth
	m.mu.Unlock()

	m.queueRefreshReschedule(id)
	return true
}

func (m *Manager) refreshAuth(ctx context.Context, id string) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	auth := m.auths[id]
	var exec ProviderExecutor
	var cloned *Auth
	if auth != nil {
		exec = m.executors[auth.Provider]
		cloned = auth.Clone()
	}
	m.mu.RUnlock()
	if auth == nil || exec == nil {
		return
	}
	updated, err := exec.Refresh(ctx, cloned)
	if err != nil && errors.Is(err, context.Canceled) {
		log.Debugf("refresh canceled for %s, %s", auth.Provider, auth.ID)
		return
	}
	log.Debugf("refreshed %s, %s, %v", auth.Provider, auth.ID, err)
	now := time.Now()
	if err != nil {
		unauthorized := isUnauthorizedError(err)
		shouldReschedule := false
		m.mu.Lock()
		if current := m.auths[id]; current != nil {
			current.LastError = refreshErrorFromError(err)
			if unauthorized {
				current.NextRefreshAfter = time.Time{}
				current.Unavailable = true
				current.Status = StatusError
				current.StatusMessage = "unauthorized"
			} else {
				current.NextRefreshAfter = now.Add(refreshFailureBackoff)
			}
			m.auths[id] = current
			shouldReschedule = true
			if m.scheduler != nil {
				m.scheduler.upsertAuth(current.Clone())
			}
		}
		m.mu.Unlock()
		if shouldReschedule {
			m.queueRefreshReschedule(id)
		}
		return
	}
	if updated == nil {
		updated = cloned
	}
	// Preserve runtime created by the executor during Refresh.
	// If executor didn't set one, fall back to the previous runtime.
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	updated.LastRefreshedAt = now
	updated.NextRefreshAfter = time.Time{}
	updated.LastError = nil
	updated.UpdatedAt = now
	if m.shouldRefresh(updated, now) {
		updated.NextRefreshAfter = now.Add(refreshIneffectiveBackoff)
	}
	if _, errUpdate := m.Update(ctx, updated); errUpdate != nil {
		log.Warnf("failed to persist refreshed auth %s, %s: %v", auth.Provider, auth.ID, errUpdate)
	}
}

func (m *Manager) executorFor(provider string) ProviderExecutor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executors[provider]
}

// roundTripperContextKey is an unexported context key type to avoid collisions.
type roundTripperContextKey struct{}

// roundTripperFor retrieves an HTTP RoundTripper for the given auth if a provider is registered.
func (m *Manager) roundTripperFor(auth *Auth) http.RoundTripper {
	m.mu.RLock()
	p := m.rtProvider
	m.mu.RUnlock()
	if p == nil || auth == nil {
		return nil
	}
	return p.RoundTripperFor(auth)
}

// RoundTripperProvider defines a minimal provider of per-auth HTTP transports.
type RoundTripperProvider interface {
	RoundTripperFor(auth *Auth) http.RoundTripper
}

// RequestPreparer is an optional interface that provider executors can implement
// to mutate outbound HTTP requests with provider credentials.
type RequestPreparer interface {
	PrepareRequest(req *http.Request, auth *Auth) error
}

func executorKeyFromAuth(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		providerKey := strings.TrimSpace(auth.Attributes["provider_key"])
		compatName := strings.TrimSpace(auth.Attributes["compat_name"])
		if compatName != "" {
			if providerKey == "" {
				providerKey = compatName
			}
			return strings.ToLower(providerKey)
		}
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// logEntryWithRequestID returns a logrus entry with request_id field if available in context.
func logEntryWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	if reqID := logging.GetRequestID(ctx); reqID != "" {
		return log.WithField("request_id", reqID)
	}
	return log.NewEntry(log.StandardLogger())
}

func authMetricIndex(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if index := strings.TrimSpace(auth.Index); index != "" {
		return index
	}
	return auth.EnsureIndex()
}

func selectorMetricStrategy(selector Selector) string {
	switch s := selector.(type) {
	case *RoundRobinSelector:
		return RoutingStrategyRoundRobin
	case *FillFirstSelector:
		return RoutingStrategyFillFirst
	case *SequentialFillSelector:
		return RoutingStrategySequentialFill
	case *SpreadSelector:
		return RoutingStrategySpread
	case *SessionAffinitySelector:
		fallback := selectorMetricStrategy(s.fallback)
		if fallback == "" {
			return "session-affinity"
		}
		return "session-affinity+" + fallback
	default:
		return "custom"
	}
}

func (m *Manager) authMetricRouting(auth *Auth) (string, string) {
	if m == nil {
		return "default", ""
	}
	if group, strategy, ok := m.routingStrategyForAuths([]*Auth{auth}); ok {
		return group, strategy
	}
	return "default", selectorMetricStrategy(m.selector)
}

func (m *Manager) authMetricFields(auth *Auth, provider, model string) log.Fields {
	fields := log.Fields{
		"provider": provider,
		"model":    canonicalModelKey(model),
	}
	if auth == nil {
		return fields
	}
	fields["auth_index"] = authMetricIndex(auth)
	if group := authRoutingGroup(auth); group != "" {
		fields["routing_group"] = group
	}
	scope, strategy := m.authMetricRouting(auth)
	if scope != "" {
		fields["routing_scope"] = scope
	}
	if strategy != "" {
		fields["routing_strategy"] = strategy
	}
	return fields
}

func (m *Manager) logAuthSelectionMetric(ctx context.Context, auth *Auth, provider, model string) {
	if auth == nil {
		return
	}
	fields := m.authMetricFields(auth, provider, model)
	fields["event"] = "auth_selection"
	addRequestAttemptLogFields(ctx, fields)
	logEntryWithRequestID(ctx).WithFields(fields).Info("auth_selection")
}

func (m *Manager) logAuthSelectionFailureMetric(ctx context.Context, providers []string, model string, err error) {
	if err == nil {
		return
	}
	fields := log.Fields{
		"event":     "auth_selection_failed",
		"providers": strings.Join(normalizeProviderKeys(providers), ","),
		"model":     canonicalModelKey(model),
	}
	addRequestAttemptLogFields(ctx, fields)
	if status := statusCodeFromError(err); status > 0 {
		fields["status"] = status
	}
	var authErr *Error
	if errors.As(err, &authErr) && authErr != nil {
		if authErr.Code != "" {
			fields["error_code"] = authErr.Code
		}
		if authErr.Retryable {
			fields["retryable"] = true
		}
	}
	var cooldownErr *modelCooldownError
	if errors.As(err, &cooldownErr) && cooldownErr != nil {
		fields["error_code"] = "model_cooldown"
		fields["reset_ms"] = cooldownErr.resetIn.Milliseconds()
	}
	logEntryWithRequestID(ctx).WithFields(fields).Warn("auth_selection_failed")
}

func (m *Manager) logAuthResultMetric(ctx context.Context, auth *Auth, result Result) {
	fields := m.authMetricFields(auth, result.Provider, result.Model)
	fields["event"] = "auth_result"
	fields["success"] = result.Success
	addRequestAttemptLogFields(ctx, fields)
	if result.Duration > 0 {
		fields["duration_ms"] = result.Duration.Milliseconds()
		if penalty := slowRequestHealthPenalty(result.Duration); result.Success && penalty > 0 {
			fields["slow_penalty"] = penalty
		}
	}
	status := statusCodeFromResult(result.Error)
	if result.Success && status == 0 {
		status = http.StatusOK
	}
	if status > 0 {
		fields["status"] = status
	}
	if result.Error != nil {
		if result.Error.Code != "" {
			fields["error_code"] = result.Error.Code
		}
		if result.Error.Retryable {
			fields["retryable"] = true
		}
	}
	if result.RetryAfter != nil {
		fields["retry_after_ms"] = result.RetryAfter.Milliseconds()
	}
	logEntryWithRequestID(ctx).WithFields(fields).Info("auth_result")
}

func debugLogAuthSelection(entry *log.Entry, auth *Auth, provider string, model string) {
	if !log.IsLevelEnabled(log.DebugLevel) {
		return
	}
	if entry == nil || auth == nil {
		return
	}
	accountType, accountInfo := auth.AccountInfo()
	proxyInfo := auth.ProxyInfo()
	suffix := ""
	if proxyInfo != "" {
		suffix = " " + proxyInfo
	}
	switch accountType {
	case "api_key":
		entry.Debugf("Use API key %s for model %s%s", util.HideAPIKey(accountInfo), model, suffix)
	case "oauth":
		ident := formatOauthIdentity(auth, provider, accountInfo)
		entry.Debugf("Use OAuth %s for model %s%s", ident, model, suffix)
	}
}

func formatOauthIdentity(auth *Auth, provider string, accountInfo string) string {
	if auth == nil {
		return ""
	}
	// Prefer the auth's provider when available.
	providerName := strings.TrimSpace(auth.Provider)
	if providerName == "" {
		providerName = strings.TrimSpace(provider)
	}
	// Only log the basename to avoid leaking host paths.
	// FileName may be unset for some auth backends; fall back to ID.
	authFile := strings.TrimSpace(auth.FileName)
	if authFile == "" {
		authFile = strings.TrimSpace(auth.ID)
	}
	if authFile != "" {
		authFile = filepath.Base(authFile)
	}
	parts := make([]string, 0, 3)
	if providerName != "" {
		parts = append(parts, "provider="+providerName)
	}
	if authFile != "" {
		parts = append(parts, "auth_file="+authFile)
	}
	if len(parts) == 0 {
		return accountInfo
	}
	return strings.Join(parts, " ")
}

// InjectCredentials delegates per-provider HTTP request preparation when supported.
// If the registered executor for the auth provider implements RequestPreparer,
// it will be invoked to modify the request (e.g., add headers).
func (m *Manager) InjectCredentials(req *http.Request, authID string) error {
	if req == nil || authID == "" {
		return nil
	}
	m.mu.RLock()
	a := m.auths[authID]
	var exec ProviderExecutor
	if a != nil {
		exec = m.executors[executorKeyFromAuth(a)]
	}
	m.mu.RUnlock()
	if a == nil || exec == nil {
		return nil
	}
	if p, ok := exec.(RequestPreparer); ok && p != nil {
		return p.PrepareRequest(req, a)
	}
	return nil
}

// PrepareHttpRequest injects provider credentials into the supplied HTTP request.
func (m *Manager) PrepareHttpRequest(ctx context.Context, auth *Auth, req *http.Request) error {
	if m == nil {
		return &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	if ctx != nil {
		*req = *req.WithContext(ctx)
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	preparer, ok := exec.(RequestPreparer)
	if !ok || preparer == nil {
		return &Error{Code: "not_supported", Message: "executor does not support http request preparation"}
	}
	return preparer.PrepareRequest(req, auth)
}

// NewHttpRequest constructs a new HTTP request and injects provider credentials into it.
func (m *Manager) NewHttpRequest(ctx context.Context, auth *Auth, method, targetURL string, body []byte, headers http.Header) (*http.Request, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	method = strings.TrimSpace(method)
	if method == "" {
		method = http.MethodGet
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, targetURL, reader)
	if err != nil {
		return nil, err
	}
	if headers != nil {
		httpReq.Header = headers.Clone()
	}
	if errPrepare := m.PrepareHttpRequest(ctx, auth, httpReq); errPrepare != nil {
		return nil, errPrepare
	}
	return httpReq, nil
}

// HttpRequest injects provider credentials into the supplied HTTP request and executes it.
func (m *Manager) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	if m == nil {
		return nil, &Error{Code: "provider_not_found", Message: "manager is nil"}
	}
	if auth == nil {
		return nil, &Error{Code: "auth_not_found", Message: "auth is nil"}
	}
	if req == nil {
		return nil, &Error{Code: "invalid_request", Message: "http request is nil"}
	}
	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" {
		return nil, &Error{Code: "provider_not_found", Message: "auth provider is empty"}
	}
	exec := m.executorFor(providerKey)
	if exec == nil {
		return nil, &Error{Code: "provider_not_found", Message: "executor not registered for provider: " + providerKey}
	}
	return exec.HttpRequest(ctx, auth, req)
}
