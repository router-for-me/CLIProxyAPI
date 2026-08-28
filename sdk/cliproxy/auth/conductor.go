package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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
	// CredentialScope indicates that the failure affects the whole credential across models (e.g. Anthropic 5h/7d unified limits).
	CredentialScope bool
	// Error describes the failure when Success is false.
	Error *Error
	// Options carries execution request options (headers, metadata, etc.) for result tracking.
	Options cliproxyexecutor.Options
	// SkipQuotaObservation reports that this result must not replace the last
	// observed watermark. Count-tokens requests reuse the credential but are not
	// generation traffic; their response headers are not a generation snapshot.
	SkipQuotaObservation bool
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
	selectorMu                sync.Mutex
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
