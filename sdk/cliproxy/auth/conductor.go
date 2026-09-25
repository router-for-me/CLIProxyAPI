package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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
	// Error describes the failure when Success is false.
	Error *Error
}

// Selector chooses an auth candidate for execution.
type Selector interface {
	Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error)
}

type PluginScheduler interface {
	PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error)
}

type pluginSchedulerState interface {
	HasScheduler() bool
}

// StoppableSelector is an optional interface for selectors that hold resources.
// Selectors that implement this interface will have Stop called during shutdown.
type StoppableSelector interface {
	Selector
	Stop()
}

// SessionAffinityView is an optional interface implemented by selectors that
// pin requests to a credential by session (currently SessionAffinitySelector).
// Management endpoints use it to enumerate live bindings and revoke a stuck
// session without restarting the process. Selectors that don't do session
// pinning (plain RoundRobinSelector / FillFirstSelector when affinity is
// disabled) simply don't satisfy it — Manager.SessionAffinityEnabled then
// reports false and the dashboard shows the "affinity disabled" state.
type SessionAffinityView interface {
	Selector
	Snapshot() []SessionAffinityBinding
	InvalidateSession(sessionID string)
	InvalidateAuth(authID string) int
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
	store                     Store
	cooldownStore             CooldownStateStore
	pendingCooldownStateStore CooldownStateStore
	executors                 map[string]ProviderExecutor
	selector                  Selector
	hook                      Hook
	mu                        sync.RWMutex
	configCooldownMu          sync.Mutex
	auths                     map[string]*Auth
	scheduler                 *authScheduler
	// pluginScheduler runs outside m.mu before falling back to native selection.
	pluginScheduler PluginScheduler
	// homeRuntimeAuths retains legacy session auth lookups for non-execution callers.
	homeRuntimeAuths map[string]map[string]*Auth
	// homeRuntimeAuthOwners prevents a stale selection from clearing a replacement auth.
	homeRuntimeAuthOwners map[string]map[string]*HomeDispatchSelection
	// homeSessionSelections owns retained Home selections for websocket sessions.
	homeSessionSelections map[string]map[homeSessionSelectionKey]*HomeDispatchSelection
	homeSessionLocks      sync.Map
	homeSessionAliases    homeSessionAliasCache
	// providerOffsets tracks per-model provider rotation state for multi-provider routing.
	providerOffsets             map[string]int
	homeDispatchBundle          atomic.Pointer[HomeDispatchBundle]
	homeInFlightPublisherConfig atomic.Pointer[HomeInFlightPublisherConfig]

	// Retry controls request retry behavior.
	requestRetry        atomic.Int32
	maxRetryCredentials atomic.Int32
	maxRetryInterval    atomic.Int64

	// oauthModelAlias stores global OAuth model alias mappings (alias -> upstream name) keyed by channel.
	oauthModelAlias atomic.Value

	// apiKeyModelRouting atomically publishes per-auth aliases and configured capabilities.
	apiKeyModelRouting atomic.Value

	// modelPoolOffsets tracks per-auth alias pool rotation state.
	modelPoolOffsets map[string]int

	// modelPoolCooldowns is the pool-wide per-model cooldown aggregate
	// (Phase 2 F2): when enough of a provider's credentials are cooling for
	// the same canonical model, selection for that model fails fast instead
	// of rotating through every cooling credential. Populated in NewManager.
	modelPoolCooldowns *modelPoolCooldowns

	// pendingAffinityMigrations carries recently removed auths' session
	// bindings so a re-render that changes an auth's identity (same logical
	// credential, new ID) can rebind them. Entries expire after the window
	// below; the map stays tiny and in-memory only.
	pendingAffinityMigrations map[string]pendingAffinityMigration

	// runtimeConfig stores the latest application config for request-time decisions.
	// It is initialized in NewManager; never Load() before first Store().
	runtimeConfig atomic.Value

	// Optional HTTP RoundTripper provider injected by host.
	rtProvider RoundTripperProvider

	// Auto refresh state
	refreshCancel context.CancelFunc
	refreshLoop   *authAutoRefreshLoop

	requestPrepareLocks sync.Map
	// refreshLocks serializes credential refresh per auth ID so concurrent
	// 401 recoveries and auto-refresh workers do not race the same refresh_token.
	refreshLocks sync.Map
	// refreshSink is an optional callback invoked after every OAuth/auth
	// credential refresh completes (success or failure), so the management
	// layer can persist the outcome to the upstream_sync_log table. nil when
	// PG is not configured (no-op). Read atomically via refreshSink.Load;
	// set via SetRefreshSink so it survives runtime config reloads.
	refreshSink atomic.Pointer[RefreshSink]

	// capacityWaitFn overrides the execute-loop capacity wait (per-entry
	// concurrency caps). nil in production: waitForCapacity uses the real
	// bounded sleep honoring ctx. Tests install a seam to stay deterministic
	// without real 50ms/1200ms sleeps. It runs in the execution loop only,
	// never under the scheduler lock.
	capacityWaitFn func(context.Context, time.Duration) bool

	// autoDisableSink is an optional callback fired when the conductor's
	// classification path matches an upstream error against the auth's
	// configured auto_disable_codes. The management layer attaches it so a
	// later task can persist auto_disabled=true in PG + re-render; nil when
	// PG is not configured (no-op). Read atomically via autoDisableSink.Load;
	// set via SetAutoDisableSink so it survives runtime config reloads.
	autoDisableSink atomic.Pointer[AutoDisableSink]
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
		store:                 store,
		executors:             make(map[string]ProviderExecutor),
		selector:              selector,
		hook:                  hook,
		auths:                 make(map[string]*Auth),
		homeRuntimeAuths:      make(map[string]map[string]*Auth),
		homeRuntimeAuthOwners: make(map[string]map[string]*HomeDispatchSelection),
		homeSessionSelections: make(map[string]map[homeSessionSelectionKey]*HomeDispatchSelection),
		providerOffsets:       make(map[string]int),
		modelPoolOffsets:      make(map[string]int),
		modelPoolCooldowns:    newModelPoolCooldowns(),

		pendingAffinityMigrations: make(map[string]pendingAffinityMigration),
	}
	// atomic.Value requires non-nil initial value.
	manager.runtimeConfig.Store(&internalconfig.Config{})
	manager.apiKeyModelRouting.Store(&apiKeyModelRoutingSnapshot{config: &internalconfig.Config{}})
	defaultInFlightConfig, errInFlightConfig := HomeInFlightPublisherConfigFromConfig(internalconfig.DefaultCredentialInFlightConfig())
	if errInFlightConfig == nil {
		manager.ApplyHomeInFlightPublisherConfig(defaultInFlightConfig)
	}
	manager.scheduler = newAuthScheduler(selector)
	return manager
}

// SetRefreshSink wires an optional callback invoked after every OAuth/auth
// credential refresh completes (success or failure). Pass nil to detach an
// existing sink (e.g. when PG is reconfigured off at runtime). The sink is
// stored atomically so a runtime config reload can swap it without locking.
// Implementations must be safe for concurrent use and must never block the
// refresh pipeline on slow persistence (fire-and-forget internally).
func (m *Manager) SetRefreshSink(sink RefreshSink) {
	if m == nil {
		return
	}
	if sink == nil {
		m.refreshSink.Store(nil)
		return
	}
	m.refreshSink.Store(&sink)
}

// recordRefreshOutcome fires the configured RefreshSink (if any) with the
// outcome of a credential refresh. No-op when no sink is attached. It never
// panics and never blocks the caller: a panicking sink is recovered so a buggy
// persistence adapter cannot stall refreshes.
func (m *Manager) recordRefreshOutcome(o RefreshOutcome) {
	if m == nil {
		return
	}
	sinkPtr := m.refreshSink.Load()
	if sinkPtr == nil || *sinkPtr == nil {
		return
	}
	if o.OccurredAt.IsZero() {
		o.OccurredAt = time.Now()
	}
	// Fire the sink in its own goroutine with a recovered panic so a slow or
	// buggy persistence adapter cannot block or crash the refresh pipeline.
	go func(sink RefreshSink, outcome RefreshOutcome) {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("panic", r).Debug("auth: refresh sink panic recovered")
			}
		}()
		sink(context.Background(), outcome)
	}(*sinkPtr, o)
}

// AutoDisableEvent captures a classified upstream failure that matched the
// auth's configured auto_disable_codes. Provider + EntryID identify the
// upstream_providers row to disable, Code is the matched configured code
// (the string compare hit, or the numeric code matched via the HTTP status),
// and Message is a bounded fragment of the upstream error for diagnostics.
type AutoDisableEvent struct {
	Provider string `json:"provider"`
	EntryID  int64  `json:"entry_id"`
	Code     string `json:"code"`
	Message  string `json:"message,omitempty"`
}

// AutoDisableSink is invoked by the conductor when a classified upstream error
// matches an auth's auto_disable_codes. It is the bridge that lets the
// management layer persist auto_disabled=true + re-render without the auth
// package (which sits in sdk/cliproxy) importing internal/store. Implementations
// must be safe to call from arbitrary goroutines.
type AutoDisableSink func(ctx context.Context, ev AutoDisableEvent)

// SetAutoDisableSink wires an optional callback invoked when the conductor's
// classification path matches an upstream error against an auth's configured
// auto_disable_codes. Pass nil to detach an existing sink (e.g. when PG is
// reconfigured off at runtime). The sink is stored atomically so a runtime
// config reload can swap it without locking. Implementations must be safe for
// concurrent use and must never block the classification pipeline
// (fire-and-forget internally).
func (m *Manager) SetAutoDisableSink(sink AutoDisableSink) {
	if m == nil {
		return
	}
	if sink == nil {
		m.autoDisableSink.Store(nil)
		return
	}
	m.autoDisableSink.Store(&sink)
}

// recordAutoDisable fires the configured AutoDisableSink (if any) with a
// classified auto-disable event. No-op when no sink is attached. It never
// panics and never blocks the caller: a panicking sink is recovered so a buggy
// persistence adapter cannot stall request classification.
func (m *Manager) recordAutoDisable(ev AutoDisableEvent) {
	if m == nil {
		return
	}
	sinkPtr := m.autoDisableSink.Load()
	if sinkPtr == nil || *sinkPtr == nil {
		return
	}
	// Fire the sink in its own goroutine with a recovered panic so a slow or
	// buggy persistence adapter cannot block or crash the classification path.
	go func(sink AutoDisableSink, event AutoDisableEvent) {
		defer func() {
			if r := recover(); r != nil {
				log.WithField("panic", r).Debug("auth: auto-disable sink panic recovered")
			}
		}()
		sink(context.Background(), event)
	}(*sinkPtr, ev)
}

// autoDisableMessageBound caps the diagnostic message carried in an
// AutoDisableEvent so sink payloads stay bounded regardless of upstream noise.
const autoDisableMessageBound = 512

// authAutoDisableCodeMatch returns the matched configured code ("" = no match)
// for a classified error against the auth's auto_disable_codes attribute. A
// configured code matches exactly when it equals err.Code; failing that, a
// configured code that is a pure number matches the error's HTTP status as a
// decimal string (e.g. "401" ↔ 401) whether err.Code was absent or unrelated.
func authAutoDisableCodeMatch(auth *Auth, resultErr *Error) string {
	if auth == nil || resultErr == nil {
		return ""
	}
	raw := authAttribute(auth, AttributeAutoDisableCodes)
	if raw == "" {
		return ""
	}
	var codes []string
	if err := json.Unmarshal([]byte(raw), &codes); err != nil || len(codes) == 0 {
		return ""
	}
	if resultErr.Code != "" {
		for _, code := range codes {
			if code == resultErr.Code {
				return code
			}
		}
	}
	if resultErr.HTTPStatus <= 0 {
		return ""
	}
	statusString := strconv.Itoa(resultErr.HTTPStatus)
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if _, err := strconv.ParseUint(code, 10, 64); err == nil && code == statusString {
			return code
		}
	}
	return ""
}

// entryIDFromProviderKey parses the numeric entry child-row ID out of the
// synthesizer-built entry_provider_key ("<providerKey>:key-<id>"). The provider
// key part may itself contain colons (e.g. "claude:42" or an openai-compat
// scheme), so the split anchors on the LAST ":key-" occurrence, mirroring how
// the synthesizer concatenates providerKey + ":key-" + entryID. Non-numeric or
// absent suffixes resolve to 0 (unattributable).
func entryIDFromProviderKey(auth *Auth) int64 {
	if auth == nil {
		return 0
	}
	raw := authAttribute(auth, AttributeEntryProviderKey)
	if raw == "" {
		return 0
	}
	idx := strings.LastIndex(raw, ":key-")
	if idx < 0 {
		return 0
	}
	id, err := strconv.ParseInt(raw[idx+len(":key-"):], 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}
