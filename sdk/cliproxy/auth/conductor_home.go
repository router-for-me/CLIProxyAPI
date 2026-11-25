package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/MoeForge/moego/regexp"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	homeAuthCountMetadataKey  = "__cliproxy_home_auth_count"
	homeRetryRoundMetadataKey = "request_retry_round"
	// ExcludedAuthIDsMetadataKey stores credential IDs already attempted in the
	// current request retry round.
	ExcludedAuthIDsMetadataKey = cliproxyexecutor.ExcludedAuthIDsMetadataKey
	// CloseAllExecutionSessionsID asks an executor to release all active execution sessions.
	// Executors that do not support this marker may ignore it.
	CloseAllExecutionSessionsID = "__all_execution_sessions__"
)

// HomeDispatchBundle is the immutable client and registry pair for one Home lifetime.
type HomeDispatchBundle struct {
	client     homeAuthDispatcher
	registry   *executionregistry.Registry
	generation uint64
}

// PublishHomeDispatch publishes the selectable Home lifetime as one atomic bundle.
func (m *Manager) PublishHomeDispatch(client homeAuthDispatcher, registry *executionregistry.Registry, generation uint64) *HomeDispatchBundle {
	if m == nil || client == nil || registry == nil {
		return nil
	}
	bundle := &HomeDispatchBundle{client: client, registry: registry, generation: generation}
	m.homeDispatchBundle.Store(bundle)
	return bundle
}

// ClearHomeDispatchBundle removes bundle only when it still belongs to the active lifetime.
func (m *Manager) ClearHomeDispatchBundle(bundle *HomeDispatchBundle) bool {
	if m == nil || bundle == nil {
		return false
	}
	return m.homeDispatchBundle.CompareAndSwap(bundle, nil)
}

// HomeDispatchBundle returns the active Home lifetime bundle.
func (m *Manager) HomeDispatchBundle() *HomeDispatchBundle {
	if m == nil {
		return nil
	}
	return m.homeDispatchBundle.Load()
}

// SetHomeExecutionRegistry preserves the legacy registry API for callers that also install the current dispatcher.
func (m *Manager) SetHomeExecutionRegistry(registry *executionregistry.Registry) {
	if m == nil {
		return
	}
	m.PublishHomeDispatch(currentHomeDispatcher(), registry, 0)
}

// ClearHomeExecutionRegistry removes a matching legacy registry bundle.
func (m *Manager) ClearHomeExecutionRegistry(registry *executionregistry.Registry) bool {
	bundle := m.HomeDispatchBundle()
	if bundle == nil || bundle.registry != registry {
		return false
	}
	return m.ClearHomeDispatchBundle(bundle)
}

// HomeExecutionRegistry returns the registry from the active Home lifetime bundle.
func (m *Manager) HomeExecutionRegistry() *executionregistry.Registry {
	bundle := m.HomeDispatchBundle()
	if bundle == nil {
		return nil
	}
	return bundle.registry
}

// HomeEnabled reports whether the home control plane integration is enabled in the runtime config.
func (m *Manager) HomeEnabled() bool {
	if m == nil {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	return cfg != nil && cfg.Home.Enabled
}

func (m *Manager) localExecutionAllowed() bool {
	return m != nil && !m.HomeEnabled()
}

func (m *Manager) localFallbackAuth(authID string) *Auth {
	if !m.localExecutionAllowed() {
		return nil
	}
	m.mu.RLock()
	auth := m.auths[strings.TrimSpace(authID)]
	m.mu.RUnlock()
	if auth == nil {
		return nil
	}
	return auth.Clone()
}

type homeErrorEnvelope struct {
	Error *homeErrorDetail `json:"error"`
}

type homeErrorDetail struct {
	Type         string `json:"type"`
	Message      string `json:"message"`
	Code         string `json:"code,omitempty"`
	Retryable    bool   `json:"retryable,omitempty"`
	RetryAfterMS int64  `json:"retry_after_ms,omitempty"`
	RequestRetry *int   `json:"request_retry,omitempty"`
}

type homeDispatchRetryAfterError struct {
	cause           *Error
	retryAfter      time.Duration
	requestRetry    int
	hasRequestRetry bool
}

// homeRetryRoundExhaustedError marks a terminal error produced after the
// current Home credential round has been exhausted. The wrapped error retains
// its status and retry-after metadata for the outer request retry policy.
type homeRetryRoundExhaustedError struct {
	cause         error
	retryAfter    time.Duration
	hasRetryAfter bool
	retryNow      bool
}

func (e *homeRetryRoundExhaustedError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *homeRetryRoundExhaustedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *homeRetryRoundExhaustedError) RetryAfter() *time.Duration {
	if e == nil || !e.hasRetryAfter {
		return nil
	}
	value := e.retryAfter
	return &value
}

func markHomeRetryRoundExhausted(err error, retryAfter *time.Duration, retryNow bool) error {
	if err == nil {
		return nil
	}
	upstreamAttempt := hasUpstreamExecutionAttempt(err)
	upstreamProvider := upstreamAttemptProviderFromError(err)
	err = unwrapUpstreamExecutionAttempt(err)
	marked := &homeRetryRoundExhaustedError{cause: err, retryNow: retryNow}
	if retryAfter != nil {
		marked.retryAfter = *retryAfter
		marked.hasRetryAfter = true
	}
	if upstreamAttempt {
		return markUpstreamExecutionAttemptWithProvider(marked, upstreamProvider)
	}
	return marked
}

func isHomeRetryRoundExhausted(err error) bool {
	if err == nil {
		return false
	}
	var marker *homeRetryRoundExhaustedError
	return errors.As(err, &marker) && marker != nil
}

type homeRetryRoundTiming struct {
	retryAfter time.Duration
	immediate  bool
	invalid    bool
}

func (t *homeRetryRoundTiming) Observe(err error) {
	if t == nil || err == nil || t.immediate || t.invalid {
		return
	}
	retryAfter := retryAfterFromError(err)
	if retryAfter == nil {
		return
	}
	if *retryAfter == 0 {
		t.retryAfter = 0
		t.immediate = true
		return
	}
	if *retryAfter < 0 {
		t.retryAfter = *retryAfter
		t.invalid = true
		return
	}
	if t.retryAfter <= 0 || *retryAfter < t.retryAfter {
		t.retryAfter = *retryAfter
	}
}

func (t *homeRetryRoundTiming) RetryAfter() *time.Duration {
	if t == nil || t.immediate || (!t.invalid && t.retryAfter <= 0) {
		return nil
	}
	value := t.retryAfter
	return &value
}

func (e *homeDispatchRetryAfterError) Error() string {
	if e == nil || e.cause == nil {
		return ""
	}
	return e.cause.Error()
}

func (e *homeDispatchRetryAfterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *homeDispatchRetryAfterError) StatusCode() int {
	if e == nil || e.cause == nil {
		return 0
	}
	return e.cause.HTTPStatus
}

func (e *homeDispatchRetryAfterError) RetryAfter() *time.Duration {
	if e == nil || e.retryAfter <= 0 {
		return nil
	}
	value := e.retryAfter
	return &value
}

func (e *homeDispatchRetryAfterError) RequestRetryLimit() (int, bool) {
	if e == nil || !e.hasRequestRetry {
		return 0, false
	}
	return e.requestRetry, true
}

const (
	homeUpstreamModelAttributeKey     = "home_upstream_model"
	homeForceMappingAttributeKey      = "home_force_mapping"
	homeOriginalAliasAttributeKey     = "home_original_alias"
	homeRequestRetryExceededErrorCode = "request_retry_exceeded"
)

func isHomeRequestRetryExceededError(err error) bool {
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(authErr.Code), homeRequestRetryExceededErrorCode)
}

func isSessionAffinityRetryError(err error) bool {
	if err == nil {
		return false
	}
	var authErr *Error
	return errors.As(err, &authErr) && authErr != nil && authErr.Code == "session_auth_retry"
}

func shouldReturnLastErrorOnPickFailure(homeMode bool, lastErr error, errPick error) bool {
	if lastErr == nil || isRequestStopError(errPick) || isSessionAffinityRetryError(errPick) {
		return false
	}
	if !homeMode {
		return true
	}
	if isSchedulerRejectError(errPick) || isHomeRequestRetryExceededError(errPick) {
		return true
	}
	var authErr *Error
	if !errors.As(errPick, &authErr) || authErr == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(authErr.Code)) {
	case "auth_not_found", "auth_unavailable":
		return true
	default:
		return false
	}
}

func isHomeNextRoundImmediatelyAvailable(err error) bool {
	var authErr *Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(authErr.Code), "auth_unavailable")
}

func pendingHomeRetryRoundDelay(err error, maxWait time.Duration, retryLimit *int, acceptRemoteRetryLimit bool) (time.Duration, bool) {
	if err == nil || isHomeRetryRoundExhausted(err) {
		return 0, false
	}
	var homeCooldown *homeDispatchRetryAfterError
	if !errors.As(err, &homeCooldown) || homeCooldown == nil {
		return 0, false
	}
	observeHomeCooldownRetryLimit(homeCooldown, retryLimit, acceptRemoteRetryLimit)
	retryAfter := homeCooldown.RetryAfter()
	if retryAfter == nil || *retryAfter <= 0 || maxWait <= 0 || *retryAfter > maxWait {
		return 0, false
	}
	return *retryAfter, true
}

func homeAuthAlreadyTried(tried map[string]struct{}, authID string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" || len(tried) == 0 {
		return false
	}
	_, ok := tried[authID]
	return ok
}

func repeatedHomeAuthError() *Error {
	return &Error{
		Code:       homeRequestRetryExceededErrorCode,
		Message:    "home returned a previously tried auth",
		HTTPStatus: http.StatusServiceUnavailable,
	}
}

type homeAuthDispatchResponse struct {
	SelectionID    string                 `json:"selection_id,omitempty"`
	Model          string                 `json:"model"`
	Provider       string                 `json:"provider"`
	ExecutorPlugin string                 `json:"executor_plugin,omitempty"`
	AuthIndex      string                 `json:"auth_index"`
	UserAPIKey     string                 `json:"user_api_key"`
	RequestRetry   *int                   `json:"request_retry,omitempty"`
	ForceMapping   bool                   `json:"force_mapping"`
	OriginalAlias  string                 `json:"original_alias"`
	ModelInfo      *homeDispatchModelInfo `json:"model_info,omitempty"`
	Auth           Auth                   `json:"auth"`
}

// HomePluginExecutorResolver exposes the live plugin executors that accept Home credentials.
type HomePluginExecutorResolver interface {
	HomeExecutorPlugins() map[string][]string
	ResolveHomePluginExecutor(pluginID, authProvider string) (ProviderExecutor, error)
}

// SetHomePluginExecutorResolver installs the plugin host for Home dispatch.
func (m *Manager) SetHomePluginExecutorResolver(resolver HomePluginExecutorResolver) {
	m.mu.Lock()
	m.homePluginExecutors = resolver
	m.mu.Unlock()
}

type homeDispatchModelInfo struct {
	ID                         string                       `json:"id"`
	Type                       string                       `json:"type,omitempty"`
	InputTokenLimit            int                          `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit           int                          `json:"outputTokenLimit,omitempty"`
	ContextLength              int                          `json:"context_length,omitempty"`
	MaxCompletionTokens        int                          `json:"max_completion_tokens,omitempty"`
	Thinking                   *registry.ThinkingSupport    `json:"thinking,omitempty"`
	NativeCapabilities         *registry.NativeCapabilities `json:"native_capabilities,omitempty"`
	SupportConfigurationUpdate *bool                        `json:"support_configuration_update,omitempty"`
	UserDefined                bool                         `json:"user_defined"`
}

func (m *homeDispatchModelInfo) registryModelInfo() *registry.ModelInfo {
	if m == nil || strings.TrimSpace(m.ID) == "" {
		return nil
	}
	return &registry.ModelInfo{
		ID:                         strings.TrimSpace(m.ID),
		Type:                       strings.TrimSpace(m.Type),
		InputTokenLimit:            m.InputTokenLimit,
		OutputTokenLimit:           m.OutputTokenLimit,
		ContextLength:              m.ContextLength,
		MaxCompletionTokens:        m.MaxCompletionTokens,
		Thinking:                   m.Thinking,
		NativeCapabilities:         m.NativeCapabilities,
		SupportConfigurationUpdate: m.SupportConfigurationUpdate != nil && *m.SupportConfigurationUpdate,
		UserDefined:                m.UserDefined,
	}
}

type homeDispatchSessionHierarchyDispatcher interface {
	RPopAuthWithSessionHierarchy(ctx context.Context, requestedModel string, sessionID string, parentSessionID string, headers http.Header, count int, credentialPolicy string, retryRound *int, excludedAuthIDs []string, pinnedAuthID string) ([]byte, error)
}

type homeAuthDispatcher interface {
	HeartbeatOK() bool
	RPopAuth(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int) ([]byte, error)
	AbortAmbiguousDispatch()
}

type homeDispatchConstraintsDispatcher interface {
	RPopAuthWithConstraints(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int, excludedAuthIDs []string, pinnedAuthID string) ([]byte, error)
}

type homeDispatchRetryRoundConstraintsDispatcher interface {
	RPopAuthWithRetryRoundConstraints(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int, retryRound int, excludedAuthIDs []string, pinnedAuthID string) ([]byte, error)
}

type homeCredentialPolicyDispatcher interface {
	RPopAuthWithPolicy(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int, credentialPolicy string) ([]byte, error)
}

type homeCredentialPolicyConstraintsDispatcher interface {
	RPopAuthWithPolicyAndConstraints(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int, credentialPolicy string, excludedAuthIDs []string, pinnedAuthID string) ([]byte, error)
}

type homeCredentialPolicyRetryRoundConstraintsDispatcher interface {
	RPopAuthWithPolicyAndRetryRoundConstraints(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int, credentialPolicy string, retryRound int, excludedAuthIDs []string, pinnedAuthID string) ([]byte, error)
}

var currentHomeDispatcher = func() homeAuthDispatcher {
	return home.Current()
}

func setHomeUserAPIKeyOnGinContext(ctx context.Context, apiKey string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || ctx == nil {
		return
	}
	coreusage.SetCapturedAPIKey(ctx, apiKey)
	ginCtx, ok := ctx.Value("gin").(interface{ Set(string, any) })
	if !ok || ginCtx == nil {
		return
	}
	ginCtx.Set("userApiKey", apiKey)
}

func homeDispatchHeaders(ctx context.Context, headers http.Header) http.Header {
	var out http.Header
	ensureCloned := func() {
		if out != nil {
			return
		}
		out = headers.Clone()
		if out == nil {
			out = http.Header{}
		}
	}
	if IsCodexClientRequest(ctx, headers) && !IsCodexClientUserAgent(headerValueFromHTTPHeader(headers, "User-Agent")) {
		ensureCloned()
		out.Set("User-Agent", "codex_cli_rs")
	}
	if apiKey, ok := homeQueryCredentialFromContext(ctx); ok {
		ensureCloned()
		if out.Get("Authorization") == "" && out.Get("X-Goog-Api-Key") == "" && out.Get("X-Api-Key") == "" {
			out.Set("X-Goog-Api-Key", apiKey)
		}
	}
	if out != nil {
		return out
	}
	return headers
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

type homeSessionSelectionKey struct {
	credentialID string
	routeModel   string
}

func (m *Manager) lockHomeWebsocketSession(ctx context.Context, opts cliproxyexecutor.Options) func() {
	if m == nil || !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return nil
	}
	sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	if sessionID == "" {
		return nil
	}
	lock, _ := m.homeSessionLocks.LoadOrStore(sessionID, &sync.Mutex{})
	mutex, ok := lock.(*sync.Mutex)
	if !ok || mutex == nil {
		return nil
	}
	mutex.Lock()
	return mutex.Unlock
}

func (m *Manager) retainedHomeSessionSelection(ctx context.Context, opts cliproxyexecutor.Options, model string, excludedAuthIDs map[string]struct{}) (*HomeDispatchSelection, bool, error) {
	if m == nil || !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return nil, false, nil
	}
	sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	credentialID := pinnedAuthIDFromMetadata(opts.Metadata)
	if sessionID == "" {
		return nil, false, nil
	}

	routeModel, validRouteModel := validCanonicalHomeConcurrencyModelKey(model)
	var retained *HomeDispatchSelection
	var ended []*HomeDispatchSelection
	fallbackAttempt := homeAuthCountFromMetadata(opts.Metadata) > 1 || homeRetryRoundFromMetadata(opts.Metadata) > 0
	m.mu.Lock()
	selections := m.homeSessionSelections[sessionID]
	for key, selection := range selections {
		if selection == nil {
			delete(selections, key)
			continue
		}
		matchesCredential := credentialID == "" || key.credentialID == credentialID
		matchesRoute := validRouteModel && key.routeModel == routeModel
		_, excluded := excludedAuthIDs[strings.TrimSpace(key.credentialID)]
		if !fallbackAttempt && !excluded && matchesCredential && selection.Active() && matchesRoute && retained == nil {
			retained = selection
			continue
		}
		delete(selections, key)
		ended = append(ended, selection)
	}
	if len(selections) == 0 {
		delete(m.homeSessionSelections, sessionID)
	}
	m.mu.Unlock()

	for _, selection := range ended {
		if errWait := m.endHomeSelectionBeforeRedispatch(ctx, selection, "target_changed"); errWait != nil {
			return nil, false, errWait
		}
	}
	return retained, retained != nil, nil
}

func (m *Manager) predictedHomeConcurrencyModel(auth *Auth, routeModel string) (string, bool) {
	requestedModel := rewriteModelForAuth(routeModel, auth)
	aliasResult := m.resolveExecutionAliasResultForRequested(auth, requestedModel)
	upstreamModel := executionAliasPoolModel(auth, requestedModel, aliasResult)
	if pool := m.resolveOpenAICompatUpstreamModelPool(auth, upstreamModel); len(pool) != 0 {
		if len(pool) != 1 {
			return "", false
		}
		upstreamModel = pool[0]
	} else {
		upstreamModel = m.applyAPIKeyModelAlias(auth, upstreamModel)
	}
	return validCanonicalHomeConcurrencyModelKey(upstreamModel)
}

func (m *Manager) endMismatchedHomeSessionSelections(ctx context.Context, sessionID, credentialID, model string, waitForAck bool) error {
	if m == nil || sessionID == "" {
		return nil
	}
	routeModel, validRouteModel := validCanonicalHomeConcurrencyModelKey(model)
	var ended []*HomeDispatchSelection
	m.mu.Lock()
	selections := m.homeSessionSelections[sessionID]
	for key, selection := range selections {
		if selection == nil {
			delete(selections, key)
			continue
		}
		matchesRoute := validRouteModel && key.routeModel == routeModel
		if key.credentialID == credentialID && matchesRoute {
			continue
		}
		delete(selections, key)
		ended = append(ended, selection)
	}
	if len(selections) == 0 {
		delete(m.homeSessionSelections, sessionID)
	}
	m.mu.Unlock()
	for _, selection := range ended {
		if !waitForAck {
			selection.End("target_changed")
			continue
		}
		if errWait := m.endHomeSelectionBeforeRedispatch(ctx, selection, "target_changed"); errWait != nil {
			return errWait
		}
	}
	return nil
}

func (m *Manager) endHomeSelectionBeforeRedispatch(ctx context.Context, selection *HomeDispatchSelection, reason string) error {
	if selection == nil {
		return nil
	}
	ticket := selection.EndWithRelease(reason)
	if ticket == nil {
		return nil
	}

	bound := internalconfig.CredentialConcurrencyConfig{}.WithDefaults().CPACancelBound
	if m != nil {
		if cfg, ok := m.runtimeConfig.Load().(*internalconfig.Config); ok && cfg != nil {
			bound = cfg.CredentialConcurrency.WithDefaults().CPACancelBound
		}
	}
	waitCtx := ctx
	if waitCtx == nil {
		waitCtx = context.Background()
	}
	waitCtx, cancelWait := context.WithTimeout(waitCtx, bound)
	defer cancelWait()
	if errWait := ticket.Wait(waitCtx); errWait != nil {
		return &Error{Code: "home_unavailable", Message: "Home did not acknowledge credential release: " + errWait.Error(), Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}
	return nil
}

func (m *Manager) retainHomeWebsocketSelection(ctx context.Context, opts cliproxyexecutor.Options, model string, selection *HomeDispatchSelection) bool {
	if m == nil || selection == nil || !selection.Retained() || !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return false
	}
	selectionAuth := selection.CloneAuth()
	if selectionAuth == nil {
		return false
	}
	sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	credentialID := strings.TrimSpace(selectionAuth.ID)
	routeModel, validRouteModel := validCanonicalHomeConcurrencyModelKey(model)
	if selection.accountedModel == "" {
		selection.accountedModel, _ = m.predictedHomeConcurrencyModel(selectionAuth, model)
	}
	if sessionID == "" || credentialID == "" || !validRouteModel || selection.accountedModel == "" {
		return false
	}
	_ = m.endMismatchedHomeSessionSelections(ctx, sessionID, credentialID, routeModel, false)
	m.rememberHomeRuntimeAuth(sessionID, selectionAuth)
	if !selection.Active() {
		return false
	}
	key := homeSessionSelectionKey{credentialID: credentialID, routeModel: routeModel}
	m.mu.Lock()
	if m.homeSessionSelections == nil {
		m.homeSessionSelections = make(map[string]map[homeSessionSelectionKey]*HomeDispatchSelection)
	}
	selections := m.homeSessionSelections[sessionID]
	if selections == nil {
		selections = make(map[homeSessionSelectionKey]*HomeDispatchSelection)
		m.homeSessionSelections[sessionID] = selections
	}
	previous := selections[key]
	selections[key] = selection
	m.mu.Unlock()
	if previous != nil && previous != selection {
		previous.End("target_replaced")
	}
	return true
}

func (m *Manager) clearHomeSessionLocks() {
	if m == nil {
		return
	}
	m.homeSessionLocks.Range(func(key, _ any) bool {
		m.homeSessionLocks.Delete(key)
		return true
	})
}

func (m *Manager) takeHomeSessionSelectionsLocked(sessionID string) []*HomeDispatchSelection {
	if m == nil {
		return nil
	}
	selections := m.homeSessionSelections[sessionID]
	delete(m.homeSessionSelections, sessionID)
	result := make([]*HomeDispatchSelection, 0, len(selections))
	for _, selection := range selections {
		result = append(result, selection)
	}
	return result
}

func (m *Manager) takeAllHomeSessionSelectionsLocked() []*HomeDispatchSelection {
	if m == nil {
		return nil
	}
	result := make([]*HomeDispatchSelection, 0)
	for sessionID, selections := range m.homeSessionSelections {
		delete(m.homeSessionSelections, sessionID)
		for _, selection := range selections {
			result = append(result, selection)
		}
	}
	return result
}

func (m *Manager) clearHomeRuntimeAuths() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.clearHomeRuntimeAuthsLocked()
	selections := m.takeAllHomeSessionSelectionsLocked()
	m.mu.Unlock()
	m.homeSessionAliases.clear()
	for _, selection := range selections {
		selection.End("home_disabled")
	}
}

func (m *Manager) clearHomeRuntimeAuthsLocked() {
	if m == nil {
		return
	}
	m.homeRuntimeAuths = make(map[string]map[string]*Auth)
	m.homeRuntimeAuthOwners = make(map[string]map[string]*HomeDispatchSelection)
}

func (m *Manager) clearHomeRuntimeAuthsForSessionLocked(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}
	delete(m.homeRuntimeAuths, sessionID)
	delete(m.homeRuntimeAuthOwners, sessionID)
}

func (m *Manager) bindHomeSelectionRuntimeAuth(ctx context.Context, opts cliproxyexecutor.Options, selection *HomeDispatchSelection) error {
	if m == nil || selection == nil || !cliproxyexecutor.DownstreamWebsocket(ctx) {
		return nil
	}
	selectionAuth := selection.CloneAuth()
	if selectionAuth == nil || !authWebsocketsEnabled(selectionAuth) {
		return nil
	}
	sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	authID := strings.TrimSpace(selectionAuth.ID)
	if sessionID == "" || authID == "" || !selection.runtimeAuthBound.CompareAndSwap(false, true) {
		return nil
	}
	m.rememberHomeSelectionRuntimeAuth(sessionID, selection)
	if errBind := selection.Bind(func() error {
		if !selection.retained.Load() {
			m.forgetHomeRuntimeAuth(sessionID, authID, selection)
		}
		return nil
	}); errBind != nil {
		selection.runtimeAuthBound.Store(false)
		m.forgetHomeRuntimeAuth(sessionID, authID, selection)
		return errBind
	}
	return nil
}

func (m *Manager) rememberHomeSelectionRuntimeAuth(sessionID string, selection *HomeDispatchSelection) {
	if m == nil || selection == nil {
		return
	}
	selectionAuth := selection.CloneAuth()
	if selectionAuth == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	authID := strings.TrimSpace(selectionAuth.ID)
	if sessionID == "" || authID == "" {
		return
	}
	m.mu.Lock()
	if m.homeRuntimeAuths == nil {
		m.homeRuntimeAuths = make(map[string]map[string]*Auth)
	}
	if m.homeRuntimeAuthOwners == nil {
		m.homeRuntimeAuthOwners = make(map[string]map[string]*HomeDispatchSelection)
	}
	if m.homeRuntimeAuths[sessionID] == nil {
		m.homeRuntimeAuths[sessionID] = make(map[string]*Auth)
	}
	if m.homeRuntimeAuthOwners[sessionID] == nil {
		m.homeRuntimeAuthOwners[sessionID] = make(map[string]*HomeDispatchSelection)
	}
	m.homeRuntimeAuths[sessionID][authID] = selectionAuth
	m.homeRuntimeAuthOwners[sessionID][authID] = selection
	m.mu.Unlock()
}

func (m *Manager) replaceHomeSelectionAuth(selection *HomeDispatchSelection, auth *Auth) {
	if m == nil || selection == nil || auth == nil {
		return
	}
	m.mu.Lock()
	selection.ReplaceAuth(auth)
	updated := selection.CloneAuth()
	if updated == nil {
		m.mu.Unlock()
		return
	}
	for sessionID, owners := range m.homeRuntimeAuthOwners {
		for authID, owner := range owners {
			if owner != selection || m.homeRuntimeAuths[sessionID] == nil {
				continue
			}
			m.homeRuntimeAuths[sessionID][authID] = updated.Clone()
		}
	}
	m.mu.Unlock()
}

func (m *Manager) forgetHomeRuntimeAuth(sessionID string, authID string, owner *HomeDispatchSelection) {
	sessionID = strings.TrimSpace(sessionID)
	authID = strings.TrimSpace(authID)
	if m == nil || sessionID == "" || authID == "" {
		return
	}
	m.mu.Lock()
	owners := m.homeRuntimeAuthOwners[sessionID]
	if owner != nil && owners[authID] != owner {
		m.mu.Unlock()
		return
	}
	sessionAuths := m.homeRuntimeAuths[sessionID]
	delete(sessionAuths, authID)
	delete(owners, authID)
	if len(sessionAuths) == 0 {
		delete(m.homeRuntimeAuths, sessionID)
	}
	if len(owners) == 0 {
		delete(m.homeRuntimeAuthOwners, sessionID)
	}
	m.mu.Unlock()
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
	logicalProvider := strings.ToLower(strings.TrimSpace(auth.Provider))
	executorKey := executorKeyFromAuth(auth)
	if logicalProvider == "" || executorKey == "" {
		return nil, nil, "", false
	}
	executor, ok := m.Executor(executorKey)
	if !ok && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["base_url"]) != "" {
		executor, ok = m.Executor("openai-compatibility")
	}
	if !ok {
		return nil, nil, "", false
	}
	return auth.Clone(), executor, logicalProvider, true
}

func (m *Manager) hasAlternativeHomeTargetForModel(ctx context.Context, model string, opts cliproxyexecutor.Options, excludedAuthID string) bool {
	if m == nil || !m.HomeEnabled() {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	probeOpts := opts
	probeOpts.Metadata = cloneRequestMetadata(opts.Metadata)
	delete(probeOpts.Metadata, cliproxyexecutor.PinnedAuthMetadataKey)
	delete(probeOpts.Metadata, cliproxyexecutor.ExecutionSessionMetadataKey)
	delete(probeOpts.Metadata, cliproxyexecutor.SelectedAuthCallbackMetadataKey)
	delete(probeOpts.Metadata, cliproxyexecutor.FinalizedResponseFormatCallbackMetadataKey)
	probeOpts = withHomeRetryRound(probeOpts, 0)
	probeOpts = withHomeAuthCount(probeOpts, 2)
	if excludedAuthID != "" {
		probeOpts = withHomeExcludedAuthIDs(probeOpts, map[string]struct{}{excludedAuthID: {}})
	}
	selection, errSelection := m.pickHomeDispatchSelection(ctx, model, probeOpts)
	if errSelection != nil || selection == nil {
		return false
	}
	selectionAuth := selection.CloneAuth()
	_ = m.endHomeSelectionBeforeRedispatch(ctx, selection, "replay_target_probe")
	if selectionAuth == nil {
		return false
	}
	candidateID := strings.TrimSpace(selectionAuth.ID)
	return candidateID != "" && candidateID != excludedAuthID
}

func (m *Manager) pickNextViaHome(ctx context.Context, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	selection, errSelection := m.pickHomeDispatchSelection(ctx, model, withHomeExcludedAuthIDs(opts, tried))
	if errSelection != nil {
		return nil, nil, "", errSelection
	}
	selectionAuth := selection.CloneAuth()
	if selectionAuth == nil || homeAuthAlreadyTried(tried, selectionAuth.ID) {
		selection.End("repeated_auth")
		return nil, nil, "", repeatedHomeAuthError()
	}
	auth := selection.CloneAuthForRoute(model)
	executor := selection.Executor
	provider := selection.Provider
	selection.End("legacy_selection_unbound")
	return auth, executor, provider, nil
}

func (m *Manager) pickHomeDispatchSelection(ctx context.Context, model string, opts cliproxyexecutor.Options) (*HomeDispatchSelection, error) {
	if m == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	requestedModel := strings.TrimSpace(model)
	if requestedModel == "" {
		requestedModel = requestedModelFromMetadata(opts.Metadata, model)
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	retryRound := homeRetryRoundFromMetadata(opts.Metadata)
	excludedAuthIDList := homeExcludedAuthIDsFromMetadata(opts.Metadata)
	excludedAuthIDs := make(map[string]struct{}, len(excludedAuthIDList))
	for _, authID := range excludedAuthIDList {
		excludedAuthIDs[authID] = struct{}{}
	}
	retained, retainedOK, errRetained := m.retainedHomeSessionSelection(ctx, opts, requestedModel, excludedAuthIDs)
	if errRetained != nil {
		return nil, errRetained
	}
	if retainedOK {
		return retained, nil
	}
	if errLoss := cliproxyexecutor.WebsocketInputLossError(ctx); errLoss != nil {
		return nil, errLoss
	}
	if sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata); sessionID != "" {
		if cliproxyexecutor.RequiredUpstreamWebsocket(ctx) && pinnedAuthID != "" {
			if _, excluded := excludedAuthIDs[pinnedAuthID]; !excluded {
				if _, ok := m.GetExecutionSessionAuthByID(sessionID, pinnedAuthID); ok {
					return nil, &cliproxyexecutor.UpstreamWebsocketTurnError{FullContextRequired: true}
				}
			}
		}
		if pinnedAuthID != "" {
			if errEnd := m.endMismatchedHomeSessionSelections(ctx, sessionID, pinnedAuthID, requestedModel, true); errEnd != nil {
				return nil, errEnd
			}
		}
	}

	bundle := m.HomeDispatchBundle()
	if bundle == nil || bundle.client == nil || bundle.registry == nil {
		return nil, &Error{Code: "home_unavailable", Message: "home dispatch bundle unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	client := bundle.client
	registry := bundle.registry
	if !client.HeartbeatOK() {
		return nil, &Error{Code: "home_unavailable", Message: "home control center unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	if pinnedAuthID != "" {
		if _, excluded := excludedAuthIDs[pinnedAuthID]; excluded {
			return nil, &Error{Code: "auth_not_found", Message: "pinned auth is unavailable in the current retry round", HTTPStatus: http.StatusServiceUnavailable}
		}
	}
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		return nil, &Error{Code: "home_unavailable", Message: "home execution registry unavailable", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}

	if opts.Metadata != nil {
		if opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] == nil && requestedModel != "" {
			opts.Metadata[cliproxyexecutor.SessionAffinityModelMetadataKey] = requestedModel
		}
	}
	sessionID, parentSessionID := m.homeDispatchSessionIDs(opts)
	if sessionID != "" && opts.Metadata != nil {
		opts.Metadata[cliproxyexecutor.CanonicalSessionIDMetadataKey] = sessionID
		if parentSessionID != "" {
			opts.Metadata[cliproxyexecutor.ParentSessionIDMetadataKey] = parentSessionID
		} else {
			delete(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
		}
	}
	dispatchHeaders := homeDispatchHeaders(ctx, opts.Headers)
	if opts.Metadata != nil {
		nodeKind, _ := opts.Metadata[cliproxyexecutor.NodeKindMetadataKey].(string)
		if isFork, _ := opts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool); isFork {
			nodeKind = "fork"
		}
		if nodeKind = strings.TrimSpace(nodeKind); nodeKind != "" {
			// Keep the caller headers unchanged when adding Home-only lineage metadata.
			dispatchHeaders = dispatchHeaders.Clone()
			if dispatchHeaders == nil {
				dispatchHeaders = make(http.Header)
			}
			dispatchHeaders.Set("X-Node-Kind", nodeKind)
		}
	}
	requestID := stringMetadataValue(opts.Metadata, cliproxyexecutor.RequestIDMetadataKey)
	if requestID == "" {
		requestID = logging.GetRequestID(ctx)
	}
	traceID := stringMetadataValue(opts.Metadata, cliproxyexecutor.TraceIDMetadataKey)
	if traceID == "" {
		traceID = coreusage.TraceIDFromContext(ctx)
	}
	ctx = home.WithSchedulerContext(ctx, home.SchedulerContext{
		Stream: opts.Stream, RequestID: requestID, TraceID: traceID,
		ExecutionSessionID: homeExecutionSessionIDFromMetadata(opts.Metadata),
		RequestedModel:     stringMetadataValue(opts.Metadata, cliproxyexecutor.RequestedModelMetadataKey),
	})
	m.mu.RLock()
	pluginExecutors := m.homePluginExecutors
	m.mu.RUnlock()
	var readyPlugins map[string][]string
	if pluginExecutors != nil {
		readyPlugins = pluginExecutors.HomeExecutorPlugins()
	}
	if readyPlugins == nil {
		readyPlugins = make(map[string][]string)
	}
	ctx = home.WithExecutorPlugins(ctx, readyPlugins)
	credentialPolicy := credentialPolicyFromContext(ctx)
	var raw []byte
	var errRPop error
	if hierarchyClient, okHierarchy := client.(homeDispatchSessionHierarchyDispatcher); okHierarchy {
		var retryRoundPtr *int
		if retryRound >= 0 {
			retryRoundPtr = &retryRound
		}
		raw, errRPop = hierarchyClient.RPopAuthWithSessionHierarchy(ctx, requestedModel, sessionID, parentSessionID, dispatchHeaders, homeAuthCountFromMetadata(opts.Metadata), credentialPolicy, retryRoundPtr, excludedAuthIDList, pinnedAuthID)
	} else if credentialPolicy == "" {
		if retryRoundClient, okRetryRound := client.(homeDispatchRetryRoundConstraintsDispatcher); okRetryRound {
			raw, errRPop = retryRoundClient.RPopAuthWithRetryRoundConstraints(ctx, requestedModel, sessionID, dispatchHeaders, homeAuthCountFromMetadata(opts.Metadata), retryRound, excludedAuthIDList, pinnedAuthID)
		} else if constrainedClient, okConstraints := client.(homeDispatchConstraintsDispatcher); okConstraints {
			raw, errRPop = constrainedClient.RPopAuthWithConstraints(ctx, requestedModel, sessionID, dispatchHeaders, homeAuthCountFromMetadata(opts.Metadata), excludedAuthIDList, pinnedAuthID)
		} else {
			raw, errRPop = client.RPopAuth(ctx, requestedModel, sessionID, dispatchHeaders, homeAuthCountFromMetadata(opts.Metadata))
		}
	} else if retryRoundPolicyClient, okRetryRound := client.(homeCredentialPolicyRetryRoundConstraintsDispatcher); okRetryRound {
		raw, errRPop = retryRoundPolicyClient.RPopAuthWithPolicyAndRetryRoundConstraints(ctx, requestedModel, sessionID, dispatchHeaders, homeAuthCountFromMetadata(opts.Metadata), credentialPolicy, retryRound, excludedAuthIDList, pinnedAuthID)
	} else if policyClient, okPolicy := client.(homeCredentialPolicyDispatcher); okPolicy {
		if constrainedClient, okConstraints := client.(homeCredentialPolicyConstraintsDispatcher); okConstraints {
			raw, errRPop = constrainedClient.RPopAuthWithPolicyAndConstraints(ctx, requestedModel, sessionID, dispatchHeaders, homeAuthCountFromMetadata(opts.Metadata), credentialPolicy, excludedAuthIDList, pinnedAuthID)
		} else {
			raw, errRPop = policyClient.RPopAuthWithPolicy(ctx, requestedModel, sessionID, dispatchHeaders, homeAuthCountFromMetadata(opts.Metadata), credentialPolicy)
		}
	} else {
		pending.End()
		return nil, &Error{Code: "home_unavailable", Message: "home dispatcher does not support credential policies", HTTPStatus: http.StatusServiceUnavailable}
	}
	if errRPop != nil {
		if home.IsAmbiguousDispatchError(errRPop) {
			client.AbortAmbiguousDispatch()
		}
		pending.End()
		if errors.Is(errRPop, home.ErrAuthNotFound) {
			return nil, &Error{Code: "auth_not_found", Message: errRPop.Error(), HTTPStatus: http.StatusServiceUnavailable}
		}
		return nil, &Error{Code: "home_unavailable", Message: errRPop.Error(), Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}

	envelope, errEnvelope := decodeHomeDispatchConcurrencyEnvelope(raw)
	if errEnvelope != nil {
		if envelope.Present {
			client.AbortAmbiguousDispatch()
		}
		pending.End()
		if envelope.Present {
			return nil, invalidHomeConcurrencyResponse("Home returned malformed concurrency tuple")
		}
		return nil, &Error{Code: "invalid_auth", Message: "home returned invalid auth payload", HTTPStatus: http.StatusBadGateway}
	}

	kind := "http"
	if cliproxyexecutor.DownstreamWebsocket(ctx) {
		kind = "websocket"
	} else if opts.Stream {
		kind = "stream"
	}
	baseScope := executionregistry.ScopeSpec{
		RequestID: logging.GetRequestID(ctx),
		Model:     requestedModel,
		Kind:      kind,
		StartedAt: time.Now(),
	}
	var scope *executionregistry.Scope
	if envelope.Present {
		var errInstall error
		scope, errInstall = installHomeConcurrencyScope(registry, pending, envelope.Tuple, baseScope)
		if errInstall != nil {
			client.AbortAmbiguousDispatch()
			pending.End()
			return nil, homeConcurrencyInstallError(errInstall)
		}
	}
	endScope := func() {
		if scope != nil {
			scope.End("local_validation_failed")
			return
		}
		pending.End()
	}
	if errHome := decodeHomeDispatchError(raw); errHome != nil {
		if envelope.Present {
			client.AbortAmbiguousDispatch()
			endScope()
			return nil, invalidHomeConcurrencyResponse("Home returned both accounted concurrency and an error")
		}
		pending.End()
		return nil, errHome
	}

	var dispatch homeAuthDispatchResponse
	if errUnmarshal := json.Unmarshal(raw, &dispatch); errUnmarshal != nil {
		endScope()
		return nil, &Error{Code: "invalid_auth", Message: "home returned invalid auth payload", HTTPStatus: http.StatusBadGateway}
	}
	auth := dispatch.Auth
	if strings.TrimSpace(auth.ID) == "" {
		// Backward compatibility: older Home instances returned the auth directly.
		if errUnmarshal := json.Unmarshal(raw, &auth); errUnmarshal != nil {
			endScope()
			return nil, &Error{Code: "invalid_auth", Message: "home returned invalid auth payload", HTTPStatus: http.StatusBadGateway}
		}
	}
	observedModel := canonicalHomeDispatchModel(dispatch.Model, requestedModel)
	if envelope.Present {
		observedConcurrencyModel, validModel := validCanonicalHomeConcurrencyModelKey(observedModel)
		if !validModel || envelope.Tuple.Model != observedConcurrencyModel {
			client.AbortAmbiguousDispatch()
			endScope()
			return nil, invalidHomeConcurrencyResponse("Home concurrency model does not match dispatched model")
		}
	}
	if !envelope.Present {
		baseScope.Model = observedModel
	}

	setHomeUserAPIKeyOnGinContext(ctx, dispatch.UserAPIKey)
	if upstreamModel := strings.TrimSpace(dispatch.Model); upstreamModel != "" {
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string, 3)
		}
		auth.Attributes[homeUpstreamModelAttributeKey] = upstreamModel
	}
	if originalAlias := strings.TrimSpace(dispatch.OriginalAlias); dispatch.ForceMapping && originalAlias != "" {
		if auth.Attributes == nil {
			auth.Attributes = make(map[string]string, 2)
		}
		auth.Attributes[homeForceMappingAttributeKey] = "true"
		auth.Attributes[homeOriginalAliasAttributeKey] = originalAlias
	}
	if strings.TrimSpace(auth.ID) == "" {
		endScope()
		return nil, &Error{Code: "invalid_auth", Message: "home returned auth without id", HTTPStatus: http.StatusBadGateway}
	}
	if pinnedAuthID != "" && strings.TrimSpace(auth.ID) != pinnedAuthID {
		endScope()
		return nil, &Error{Code: "auth_not_found", Message: "home returned an auth that does not match the pinned credential", HTTPStatus: http.StatusServiceUnavailable}
	}
	if errIdentity := verifyAccountedHomeConcurrencyIdentity(envelope.Tuple, &auth, dispatch.AuthIndex); errIdentity != nil {
		endScope()
		return nil, errIdentity
	}
	logicalProvider := strings.ToLower(strings.TrimSpace(auth.Provider))
	executorKey := executorKeyFromAuth(&auth)
	if logicalProvider == "" || executorKey == "" {
		endScope()
		return nil, &Error{Code: "invalid_auth", Message: "home returned auth without provider", HTTPStatus: http.StatusBadGateway}
	}

	homeAuthIndex := strings.TrimSpace(dispatch.AuthIndex)
	if homeAuthIndex != "" {
		auth.Index = homeAuthIndex
		auth.indexAssigned = true
	} else {
		auth.EnsureIndex()
	}

	authExecutor, okExecutor := m.Executor(executorKey)
	if !okExecutor && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["base_url"]) != "" {
		authExecutor, okExecutor = m.Executor("openai-compatibility")
	}
	executor := authExecutor
	executorPlugin := strings.TrimSpace(dispatch.ExecutorPlugin)
	if executorPlugin != "" {
		if isAuthDisabled(&auth) || strings.TrimSpace(dispatch.Model) == "" {
			endScope()
			return nil, &Error{Code: "invalid_auth", Message: "home plugin dispatch requires an enabled auth and an upstream model", HTTPStatus: http.StatusBadGateway}
		}
		var errPlugin error
		if pluginExecutors == nil {
			errPlugin = fmt.Errorf("plugin executor %q is unavailable: plugin host is not configured", executorPlugin)
		} else {
			executor, errPlugin = pluginExecutors.ResolveHomePluginExecutor(executorPlugin, logicalProvider)
			if errPlugin == nil && executor == nil {
				errPlugin = fmt.Errorf("plugin executor %q is unavailable", executorPlugin)
			}
		}
		if errPlugin != nil {
			endScope()
			return nil, &Error{Code: ErrorCodeHomeExecutorPluginUnavailable, Message: errPlugin.Error(), HTTPStatus: http.StatusServiceUnavailable}
		}
	} else if !okExecutor {
		endScope()
		return nil, &Error{Code: "executor_not_found", Message: "executor not registered", HTTPStatus: http.StatusBadGateway}
	}
	if scope == nil {
		var errInstall error
		scope, errInstall = installHomeConcurrencyScope(registry, pending, homeConcurrencyTuple{}, executionregistry.ScopeSpec{
			RequestID:    baseScope.RequestID,
			CredentialID: strings.TrimSpace(auth.ID),
			Model:        baseScope.Model,
			Kind:         baseScope.Kind,
			StartedAt:    baseScope.StartedAt,
		})
		if errInstall != nil {
			client.AbortAmbiguousDispatch()
			pending.End()
			return nil, homeConcurrencyInstallError(errInstall)
		}
	}

	selection, errSelection := newHomeDispatchSelection(auth.Clone(), executor, logicalProvider, scope)
	if errSelection != nil {
		endScope()
		return nil, &Error{Code: "home_unavailable", Message: "home execution registry unavailable", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}
	selection.ExecutorPlugin = executorPlugin
	selection.authExecutor = authExecutor
	selection.modelInfo = dispatch.ModelInfo.registryModelInfo()
	if dispatch.ModelInfo != nil {
		selection.configurationUpdateSupport = dispatch.ModelInfo.SupportConfigurationUpdate
	}
	if pinnedAuthID == "" && dispatch.RequestRetry != nil && *dispatch.RequestRetry >= 0 {
		selection.requestRetry = *dispatch.RequestRetry
		selection.hasRequestRetry = true
	}
	if envelope.Present {
		selection.accountedModel = envelope.Tuple.Model
	}
	if executionSessionID := homeExecutionSessionIDFromMetadata(opts.Metadata); executionSessionID != "" && cliproxyexecutor.DownstreamWebsocket(ctx) {
		if errEnd := m.endMismatchedHomeSessionSelections(ctx, executionSessionID, strings.TrimSpace(auth.ID), requestedModel, true); errEnd != nil {
			selection.End("target_change_release_failed")
			return nil, errEnd
		}
	}
	selection.SelectionID = strings.TrimSpace(dispatch.SelectionID)
	selection.CanonicalSessionID = sessionID
	selection.ParentSessionID = parentSessionID
	if lcpSessionID := sessionMetadataString(opts.Metadata, cliproxyexecutor.LCPAffinitySessionIDMetadataKey); lcpSessionID != "" {
		selection.lcpAffinitySessionID = lcpSessionID
		selection.sessionAffinityProvider = sessionMetadataString(opts.Metadata, cliproxyexecutor.SessionAffinityProviderMetadataKey)
		selection.sessionAffinityModel = sessionMetadataString(opts.Metadata, cliproxyexecutor.SessionAffinityModelMetadataKey)
		selection.lcpAccessGeneration, _ = opts.Metadata[cliproxyexecutor.LCPAccessGenerationMetadataKey].(uint64)
		selection.isLCPFork, _ = opts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool)
		selection.isLCPCompaction, _ = opts.Metadata[cliproxyexecutor.IsCompactionMetadataKey].(bool)
		selection.lcpNodeKind = sessionMetadataString(opts.Metadata, cliproxyexecutor.NodeKindMetadataKey)
	}
	return selection, nil
}

func homeRetryRoundFromMetadata(metadata map[string]any) int {
	if metadata == nil {
		return 0
	}
	switch value := metadata[homeRetryRoundMetadataKey].(type) {
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 && value == float64(int(value)) {
			return int(value)
		}
	}
	return 0
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

func (m *Manager) findAllAntigravityCreditsCandidateAuths(ctx context.Context, routeModel string, opts cliproxyexecutor.Options) ([]creditsCandidateEntry, error) {
	if m == nil || !m.localExecutionAllowed() {
		return nil, nil
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	selectionModel := authSelectionModelFromOptions(opts, routeModel)
	selectionModelKey := canonicalModelKey(selectionModel)
	routeModelKey := canonicalModelKey(routeModel)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	registryRef := registry.GetGlobalRegistry()
	now := time.Now()
	var candidates []creditsCandidateEntry
	m.mu.RLock()
	if affinity, ok := m.selector.(*SessionAffinitySelector); ok && affinity != nil && affinity.rebindOnNever {
		if boundAuthID, okBound := affinity.boundAuthForRequest([]string{"antigravity"}, selectionModel, opts); okBound {
			if pinnedAuthID != "" && pinnedAuthID != boundAuthID {
				m.mu.RUnlock()
				return nil, nil
			}
			pinnedAuthID = boundAuthID
		}
	}
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled || auth.RequiresSignIn ||
			hasUnauthorizedAuthFailure(auth) || auth.credentialRetryAfter.After(now) || auth.nonAuthCredentialRetryAfter.After(now) {
			continue
		}
		if pinnedAuthID != "" && auth.ID != pinnedAuthID {
			continue
		}
		if !eligibility.allows(auth) {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
			continue
		}
		if selectionModelKey != "" && !m.authSupportsRouteModel(registryRef, auth, selectionModel) {
			continue
		}
		if routeModelKey != "" && !sameCanonicalModelKey(routeModelKey, selectionModelKey) && !m.authSupportsRouteModel(registryRef, auth, routeModel) {
			continue
		}
		targetModel := m.selectionModelKeyForAuth(auth, routeModel)
		if !strings.Contains(strings.ToLower(strings.TrimSpace(targetModel)), "claude") {
			continue
		}
		providerKey := executorKeyFromAuth(auth)
		executor, ok := m.executorLocked(providerKey)
		if !ok {
			continue
		}
		candidates = append(candidates, creditsCandidateEntry{
			auth:     auth.Clone(),
			executor: executor,
			provider: providerKey,
		})
	}
	m.mu.RUnlock()

	var known []creditsCandidateEntry
	var unknown []creditsCandidateEntry
	for _, candidate := range candidates {
		hint, okHint, errHint := GetAntigravityCreditsHintForAuthRequired(ctx, candidate.auth)
		if errHint != nil {
			return nil, antigravityCreditsKVUnavailableError(errHint)
		}
		if okHint && hint.Known {
			if !hint.Available {
				if IsAntigravityCreditsHintFresh(hint, now) {
					continue
				}
				unknown = append(unknown, candidate)
				continue
			}
			known = append(known, candidate)
			continue
		}
		unknown = append(unknown, candidate)
	}
	sort.Slice(known, func(i, j int) bool {
		return known[i].auth.ID < known[j].auth.ID
	})
	sort.Slice(unknown, func(i, j int) bool {
		return unknown[i].auth.ID < unknown[j].auth.ID
	})
	return append(known, unknown...), nil
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

const antigravityCreditsShortCooldownThreshold = 5 * time.Minute

// IsTOSViolationError reports whether err represents a terminal Antigravity TOS_VIOLATION.
func IsTOSViolationError(err error) bool {
	return isTOSViolationError(err)
}

func isTOSViolationError(err error) bool {
	if err == nil {
		return false
	}
	if isTOSViolationResultError(resultErrorFromError(err)) {
		return true
	}
	status := statusCodeFromError(err)
	if status == http.StatusForbidden || status == http.StatusServiceUnavailable {
		return strings.Contains(strings.ToLower(extractErrorBody(err)), "tos_violation")
	}
	return false
}

// IsAntigravityValidationRequiredError reports whether err is a 403 VALIDATION_REQUIRED challenge.
func IsAntigravityValidationRequiredError(err error) bool {
	return isAntigravityValidationRequiredError(err)
}

func isAntigravityValidationRequiredError(err error) bool {
	if err == nil {
		return false
	}
	status := statusCodeFromError(err)
	if status != 0 && status != http.StatusForbidden {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(errorCodeFromError(err)), "validation_required") {
		return true
	}
	if status != http.StatusForbidden {
		return false
	}
	body := strings.TrimSpace(extractErrorBody(err))
	if ojson.Valid(body) {
		var matched, hasOtherReason bool
		for _, detailsPath := range []string{"error.details", "details", "response.error.details"} {
			ojson.Get(body, detailsPath).ForEach(func(_, detail ojson.Result) bool {
				reason := strings.TrimSpace(detail.Get("reason").String())
				if strings.EqualFold(reason, "VALIDATION_REQUIRED") {
					matched = true
					return false
				}
				if reason != "" {
					hasOtherReason = true
				}
				return true
			})
			if matched {
				return true
			}
		}
		for _, path := range []string{"error.code", "error.type", "error.status", "code", "type", "status", "response.error.code", "response.error.type", "response.error.status"} {
			if strings.EqualFold(strings.TrimSpace(ojson.Get(body, path).String()), "VALIDATION_REQUIRED") {
				return true
			}
		}
		if !hasOtherReason {
			for _, path := range []string{"error.message", "message", "response.error.message"} {
				if strings.Contains(strings.ToUpper(ojson.Get(body, path).String()), "VALIDATION_REQUIRED") {
					return true
				}
			}
		}
		return false
	}
	return strings.Contains(strings.ToUpper(body), "VALIDATION_REQUIRED")
}

func looksLikeHTMLErrorBody(lowerBody string) bool {
	trimmed := strings.TrimSpace(lowerBody)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "<") && (strings.Contains(trimmed, "<!doctype") || strings.Contains(trimmed, "<html") || strings.Contains(trimmed, "<head") || strings.Contains(trimmed, "<body") || strings.Contains(trimmed, "<title")) {
		return true
	}
	return strings.Contains(trimmed, "<!doctype html") || strings.Contains(trimmed, "<html")
}

var (
	reAntigravityTextRetryDuration = regexp.MustCompile(`(?i)\b(?:retry(?:\s+again)?|try\s+again|wait|resets?|reset)\s+(?:in|after)\s+((?:\d+(?:\.\d+)?\s*(?:milliseconds?|msecs?|ms|seconds?|secs?|sec|s|minutes?|mins?|min|m|hours?|hrs?|hr|h)\s*)+)`)
	reAntigravityDurationPart      = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(milliseconds?|msecs?|ms|seconds?|secs?|sec|s|minutes?|mins?|min|m|hours?|hrs?|hr|h)`)
	reAntigravityRateLimitToken    = regexp.MustCompile(`(?i)\b(?:rpm|tpm)\b`)
)

func parseAntigravityTextRetryHintDuration(lowerBody string) (time.Duration, bool) {
	match := reAntigravityTextRetryDuration.FindStringSubmatch(lowerBody)
	if len(match) < 2 {
		return 0, false
	}
	parts := reAntigravityDurationPart.FindAllStringSubmatch(match[1], -1)
	if len(parts) == 0 {
		return 0, false
	}
	var total time.Duration
	for _, part := range parts {
		if len(part) < 3 {
			continue
		}
		val, errParse := strconv.ParseFloat(part[1], 64)
		if errParse != nil || val < 0 {
			continue
		}
		var unit time.Duration
		switch strings.ToLower(part[2]) {
		case "ms", "msec", "msecs", "millisecond", "milliseconds":
			unit = time.Millisecond
		case "s", "sec", "secs", "second", "seconds":
			unit = time.Second
		case "m", "min", "mins", "minute", "minutes":
			unit = time.Minute
		case "h", "hr", "hrs", "hour", "hours":
			unit = time.Hour
		default:
			continue
		}
		total += time.Duration(val * float64(unit))
	}
	return total, true
}

func hasAntigravityShortRetryHint(retryAfter *time.Duration, lowerBody string) bool {
	if retryAfter != nil && *retryAfter < antigravityCreditsShortCooldownThreshold {
		return true
	}
	if d, ok := parseAntigravityTextRetryHintDuration(lowerBody); ok && d < antigravityCreditsShortCooldownThreshold {
		return true
	}
	return false
}

func hasAntigravityRateLimitPhrase(lowerBody string) bool {
	if reAntigravityRateLimitToken.MatchString(lowerBody) {
		return true
	}
	for _, marker := range []string{
		"per-minute", "per minute", "per-second", "per second",
		"requests per", "tokens per",
		"rate limit", "rate_limit", "rate-limit", "throttl",
	} {
		if strings.Contains(lowerBody, marker) {
			return true
		}
	}
	return false
}

func extractAntigravityJSONErrorMessageLower(body string) string {
	var parts []string
	for _, path := range []string{"error.message", "message", "response.error.message"} {
		if msg := strings.TrimSpace(ojson.Get(body, path).String()); msg != "" {
			parts = append(parts, strings.ToLower(msg))
		}
	}
	return strings.Join(parts, "\n")
}

func hasExplicitAntigravityQuotaPhrase(lowerText string) bool {
	if strings.Contains(lowerText, "resource has been exhausted") && strings.Contains(lowerText, "quota") {
		return true
	}
	for _, marker := range []string{
		"quota exhausted", "quota_exhausted",
		"quota exceeded", "quota_exceeded",
		"exhausted your quota", "exceeded your quota", "quota has been exhausted", "out of quota",
		"quota limit", "insufficient quota", "insufficient_quota",
		"insufficient_g1_credits_balance",
		"usage limit reached", "usage_limit_reached",
		"credit balance too low", "credit_balance_too_low",
	} {
		if strings.Contains(lowerText, marker) {
			return true
		}
	}
	return false
}

func isAntigravityRateLimitCode(code string) bool {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "rate_limit_exceeded", "rate_limited", "rate_limit", "too_many_requests", "throttled":
		return true
	default:
		return false
	}
}

func hasPositiveQuotaErrorMetadata(err error) bool {
	if isCredentialScopedError(err) {
		return true
	}
	type modelCooldownMarker interface {
		IsModelCooldown() bool
	}
	var mc modelCooldownMarker
	if errors.As(err, &mc) && mc != nil && mc.IsModelCooldown() {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(errorCodeFromError(err))) {
	case "quota_exhausted", "quota", "resource_exhausted", "insufficient_quota", "credit_balance_too_low", "quota_exceeded", "usage_limit_reached", "credential_quota", "model_cooldown":
		return true
	}
	return false
}

func isAntigravityCreditsQuota429(err error, retryAfter *time.Duration) bool {
	if isTransientRateLimitError(err) || isCloudflareChallengeError(err) {
		return false
	}
	body := strings.TrimSpace(extractErrorBody(err))
	lowerBody := strings.ToLower(body)
	if looksLikeHTMLErrorBody(lowerBody) {
		return false
	}
	if ojson.Valid(body) {
		var sawQuotaReason, sawRateLimitReason bool
		for _, detailsPath := range []string{"error.details", "details", "response.error.details"} {
			ojson.Get(body, detailsPath).ForEach(func(_, detail ojson.Result) bool {
				switch strings.ToUpper(strings.TrimSpace(detail.Get("reason").String())) {
				case "QUOTA_EXHAUSTED", "INSUFFICIENT_G1_CREDITS_BALANCE":
					sawQuotaReason = true
				case "RATE_LIMIT_EXCEEDED":
					sawRateLimitReason = true
				}
				return true
			})
		}
		if sawRateLimitReason {
			return retryAfter != nil && *retryAfter >= antigravityCreditsShortCooldownThreshold
		}
		if isAntigravityRateLimitCode(errorCodeFromError(err)) {
			return retryAfter != nil && *retryAfter >= antigravityCreditsShortCooldownThreshold
		}
		codePaths := []string{"error.code", "error.type", "code", "type", "response.error.code", "response.error.type"}
		for _, path := range codePaths {
			if isAntigravityRateLimitCode(ojson.Get(body, path).String()) {
				return retryAfter != nil && *retryAfter >= antigravityCreditsShortCooldownThreshold
			}
		}
		msgLower := extractAntigravityJSONErrorMessageLower(body)
		if hasAntigravityShortRetryHint(retryAfter, msgLower) || hasAntigravityRateLimitPhrase(msgLower) {
			return false
		}
		if sawQuotaReason {
			return true
		}
		for _, path := range codePaths {
			switch strings.ToLower(strings.TrimSpace(ojson.Get(body, path).String())) {
			case "quota_exhausted", "quota_exceeded", "usage_limit_reached", "insufficient_quota", "resource_exhausted", "credit_balance_too_low":
				return true
			}
		}
		if hasPositiveQuotaErrorMetadata(err) {
			return true
		}
		if strings.EqualFold(strings.TrimSpace(ojson.Get(body, "error.status").String()), "RESOURCE_EXHAUSTED") {
			if retryAfter != nil {
				return *retryAfter >= antigravityCreditsShortCooldownThreshold
			}
			return hasExplicitAntigravityQuotaPhrase(msgLower)
		}
		return hasExplicitAntigravityQuotaPhrase(msgLower)
	}
	if hasAntigravityShortRetryHint(retryAfter, lowerBody) || hasAntigravityRateLimitPhrase(lowerBody) {
		return false
	}
	if isAntigravityRateLimitCode(errorCodeFromError(err)) {
		return retryAfter != nil && *retryAfter >= antigravityCreditsShortCooldownThreshold
	}
	if hasPositiveQuotaErrorMetadata(err) {
		return true
	}
	return hasExplicitAntigravityQuotaPhrase(lowerBody)
}

func isAntigravityCreditsCapacity503(err error) bool {
	body := strings.TrimSpace(extractErrorBody(err))
	lowerBody := strings.ToLower(body)
	if looksLikeHTMLErrorBody(lowerBody) || strings.Contains(lowerBody, "tos_violation") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(errorCodeFromError(err))) {
	case "home_kv_unavailable", "home_unavailable", "home_fallback_unsupported", "thread_auth_unavailable", "manager_unavailable", "session_auth_retry":
		return false
	case "model_capacity_exhausted", "capacity_exhausted", "quota_exhausted", "resource_exhausted":
		return true
	}
	if ojson.Valid(body) {
		var matchedReason bool
		ojson.Get(body, "error.details").ForEach(func(_, detail ojson.Result) bool {
			switch strings.ToUpper(strings.TrimSpace(detail.Get("reason").String())) {
			case "MODEL_CAPACITY_EXHAUSTED", "CAPACITY_EXHAUSTED", "QUOTA_EXHAUSTED", "RESOURCE_EXHAUSTED":
				matchedReason = true
				return false
			}
			return true
		})
		if matchedReason {
			return true
		}
		for _, path := range []string{"error.status", "error.code", "error.type", "code", "type"} {
			switch strings.ToUpper(strings.TrimSpace(ojson.Get(body, path).String())) {
			case "MODEL_CAPACITY_EXHAUSTED", "CAPACITY_EXHAUSTED", "QUOTA_EXHAUSTED", "RESOURCE_EXHAUSTED":
				return true
			}
		}
	}
	return strings.Contains(lowerBody, "model_capacity_exhausted") ||
		strings.Contains(lowerBody, "no capacity available") ||
		strings.Contains(lowerBody, "capacity exhausted") ||
		strings.Contains(lowerBody, "capacity_exhausted") ||
		strings.Contains(lowerBody, "quota exhausted") ||
		strings.Contains(lowerBody, "quota_exhausted") ||
		strings.Contains(lowerBody, "resource_exhausted")
}

func shouldAttemptAntigravityCreditsFallback(m *Manager, lastErr error, providers []string) bool {
	if isRequestTerminatedError(lastErr) ||
		isCredentialTerminatedError(lastErr) ||
		isRequestStopError(lastErr) ||
		isSchedulerRejectError(lastErr) ||
		hasRoundEndedBySchedulerReject(lastErr) ||
		isRequestInvalidError(lastErr) ||
		IsTerminalAuthError(lastErr) ||
		isTransientRateLimitError(lastErr) ||
		isTOSViolationError(lastErr) {
		return false
	}
	status := statusCodeFromError(lastErr)
	log.WithFields(log.Fields{
		"lastErr":   errorString(lastErr),
		"status":    status,
		"providers": providers,
	}).Debug("shouldAttemptAntigravityCreditsFallback")
	if m == nil || lastErr == nil || m.HomeEnabled() {
		return false
	}
	cfg, _ := m.runtimeConfig.Load().(*internalconfig.Config)
	if cfg == nil || !cfg.QuotaExceeded.AntigravityCredits {
		return false
	}
	switch status {
	case http.StatusTooManyRequests:
		return isAntigravityCreditsQuota429(lastErr, retryAfterFromError(lastErr))
	case http.StatusServiceUnavailable:
		return isAntigravityCreditsCapacity503(lastErr)
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

func (m *Manager) antigravityCreditsStateModel(auth *Auth, routeModel string) string {
	stateModel := m.selectionModelKeyForAuth(auth, routeModel)
	if stateModel == "" {
		stateModel = canonicalModelKey(routeModel)
	}
	return stateModel
}

func (m *Manager) handleAntigravityCreditsPrepareError(creditsCtx context.Context, c creditsCandidateEntry, routeModel string, creditsOpts cliproxyexecutor.Options, errPrepare error) error {
	if errCtx := creditsCtx.Err(); errCtx != nil {
		return errCtx
	}
	if errCancel := claudeOAuthRequestCancellation(creditsCtx, c.auth, errPrepare); errCancel != nil {
		return errCancel
	}
	result := Result{
		AuthID:            c.auth.ID,
		Provider:          c.provider,
		Model:             m.antigravityCreditsStateModel(c.auth, routeModel),
		RouteModel:        routeModel,
		Success:           false,
		Error:             resultErrorFromError(errPrepare),
		Options:           creditsOpts,
		AccessToken:       authAccessTokenForRequest(creditsCtx, c.auth, creditsOpts.Headers),
		RefreshToken:      authRefreshToken(c.auth),
		CredentialVersion: c.auth.CredentialVersion,
		RegistrationEpoch: c.auth.RegistrationEpoch,
	}
	if ra := retryAfterFromError(errPrepare); ra != nil {
		result.RetryAfter = ra
	}
	if isCredentialScopedError(errPrepare) {
		result.CredentialScope = true
	}
	action, okAction := matchRequestScopedErrorAction(c.auth, errPrepare, m.requestScopedErrorConfig(creditsCtx))
	applyRequestScopedActionToResult(action, okAction, &result)
	m.MarkResult(creditsCtx, result)
	if isRequestTerminatedError(errPrepare) || isRequestStopError(errPrepare) || isTOSViolationError(errPrepare) || isTOSViolationResultError(result.Error) {
		return errPrepare
	}
	if isRequestScopedStop(action, okAction) {
		return wrapRequestStopError(errPrepare)
	}
	if !okAction && (isRequestInvalidError(errPrepare) || isAntigravityValidationRequiredError(errPrepare)) {
		return errPrepare
	}
	return nil
}

func (m *Manager) tryAntigravityCreditsExecute(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, bool, error) {
	updateInFlightScopeProvider(ctx, "antigravity")
	if m != nil && m.HomeEnabled() {
		return cliproxyexecutor.Response{}, false, &Error{Code: "home_fallback_unsupported", Message: "Home does not support Antigravity credits fallback", HTTPStatus: http.StatusServiceUnavailable}
	}
	if !m.localExecutionAllowed() {
		return cliproxyexecutor.Response{}, false, nil
	}
	routeModel := req.Model
	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(ctx, routeModel, opts)
	if errCandidates != nil {
		return cliproxyexecutor.Response{}, false, errCandidates
	}
	return m.tryAntigravityCreditsExecuteCandidates(ctx, req, opts, routeModel, candidates)
}

func (m *Manager) tryAntigravityCreditsExecuteCandidates(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel string, candidates []creditsCandidateEntry) (cliproxyexecutor.Response, bool, error) {
	maxRetryCredentials := m.MaxRetryCredentials()
	refusedAuths := requestRefusedAuthsFromContext(ctx)
	attempted := make(map[string]struct{})
	for _, c := range candidates {
		if errCtx := ctx.Err(); errCtx != nil {
			return cliproxyexecutor.Response{}, false, errCtx
		}
		if _, refused := refusedAuths[c.auth.ID]; refused {
			continue
		}
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			break
		}
		models, pooled, aliasResult, routing := m.executionModelCandidatesWithAlias(c.auth, routeModel)
		if len(models) == 0 {
			continue
		}
		attempted[c.auth.ID] = struct{}{}
		creditsCtx := WithAntigravityCredits(ctx)
		creditsCtx = m.WithAuthTransport(creditsCtx, c.auth)
		creditsCtx = m.withAntigravityModelState(creditsCtx, c.auth)
		creditsOpts := ensureRequestedModelMetadata(opts, routeModel)
		clearSchedulerSelectionID(creditsOpts.Metadata)
		creditsCtx = contextWithRequestedModelAlias(creditsCtx, creditsOpts, routeModel)
		creditsCtx = coreusage.WithSelectionID(creditsCtx, "")
		creditsCtx = m.newUpstreamAttemptContext(creditsCtx)
		preparedAuth, errPrepare := m.prepareRequestAuth(creditsCtx, c.executor, c.auth)
		if errPrepare != nil {
			if preparedAuth != nil {
				c.auth = preparedAuth
			}
			if errAbort := m.handleAntigravityCreditsPrepareError(creditsCtx, c, routeModel, creditsOpts, errPrepare); errAbort != nil {
				return cliproxyexecutor.Response{}, false, errAbort
			}
			continue
		}
		c.auth = preparedAuth
		publishSelectedAuthMetadata(creditsOpts.Metadata, c.auth)
		didRefreshOnUnauthorized := false
		for _, upstreamModel := range models {
			creditsCtx = m.newUpstreamAttemptContext(creditsCtx)
			resultModel := m.stateModelForExecution(c.auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			execOpts := creditsOpts
			payload := execOpts.OriginalRequest
			if len(payload) == 0 {
				payload = execReq.Payload
			}
			execOpts.Metadata = ensureCanonicalSessionMetadata(execOpts.Metadata, execOpts.Headers, payload)
			var errIntercept error
			execReq, execOpts, errIntercept = prepareRequestAfterAuth(creditsCtx, c.executor, c.auth, c.provider, execReq, execOpts, requestedModelAliasFromOptions(execOpts, routeModel))
			if errIntercept != nil {
				if isCredentialTerminatedError(errIntercept) {
					recordRequestRefusedAuth(ctx, c.auth.ID)
					break
				}
				return cliproxyexecutor.Response{}, false, errIntercept
			}
			var activeAuth atomic.Pointer[Auth]
			activeAuth.Store(c.auth)
			refreshExecCtx := func(base context.Context) context.Context {
				return m.withExecutionAuthRefresh(syncMetadataSessionToContext(base, execOpts.Metadata), executionAuthRefreshOptions{
					onAuthUpdated: func(updated *Auth) {
						if updated != nil {
							activeAuth.Store(updated.Clone())
						}
					},
				})
			}
			syncActiveAuth := func() {
				if currentAuth := activeAuth.Load(); currentAuth != nil && currentAuth != c.auth {
					c.auth = currentAuth
				}
			}
			creditsCtx = refreshExecCtx(creditsCtx)
			executionSeq := m.nextExecutionSeq()
			resp, errExec := c.executor.Execute(creditsCtx, c.auth, execReq, execOpts)
			syncActiveAuth()
			errExec = markUpstreamExecutionAttemptFromContext(creditsCtx, errExec)
			if errExec != nil {
				if errCtx := creditsCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, false, errCtx
				}
				refreshed, okRefresh := m.tryRefreshAfterUnauthorized(newUpstreamAttemptContext(creditsCtx), c.auth, errExec, didRefreshOnUnauthorized, execOpts.Headers)
				if errCtx := creditsCtx.Err(); errCtx != nil {
					return cliproxyexecutor.Response{}, false, errCtx
				}
				if okRefresh {
					c.auth = refreshed
					activeAuth.Store(c.auth)
					publishSelectedAuthMetadata(execOpts.Metadata, c.auth)
					didRefreshOnUnauthorized = true
					creditsCtx = refreshExecCtx(m.newUpstreamAttemptContext(creditsCtx))
					executionSeq = m.nextExecutionSeq()
					resp, errExec = c.executor.Execute(creditsCtx, c.auth, execReq, execOpts)
					syncActiveAuth()
					errExec = markUpstreamExecutionAttemptFromContext(creditsCtx, errExec)
					if errCtx := creditsCtx.Err(); errCtx != nil {
						return cliproxyexecutor.Response{}, false, errCtx
					}
				}
			}
			if errCancel := claudeOAuthRequestCancellation(creditsCtx, c.auth, errExec); errCancel != nil {
				return cliproxyexecutor.Response{}, false, errCancel
			}
			result := Result{AuthID: c.auth.ID, Provider: c.provider, Model: resultModel, RouteModel: routeModel, Success: errExec == nil, CompletedResponse: errExec == nil && !resp.Incomplete && isCompletedExecutionPayload(resp.Payload), Options: execOpts, AccessToken: authAccessTokenForRequest(creditsCtx, c.auth, execOpts.Headers), RefreshToken: authRefreshToken(c.auth), CredentialVersion: c.auth.CredentialVersion, RegistrationEpoch: c.auth.RegistrationEpoch, ExecutionSeq: executionSeq}
			if errExec != nil {
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				if isCredentialScopedError(errExec) {
					result.CredentialScope = true
				}
				action, okAction := matchRequestScopedErrorAction(c.auth, errExec, m.requestScopedErrorConfig(creditsCtx))
				applyRequestScopedActionToResult(action, okAction, &result)
				m.MarkResult(creditsCtx, result)
				if isRequestTerminatedError(errExec) || isRequestStopError(errExec) || isTOSViolationError(errExec) || isTOSViolationResultError(result.Error) {
					return cliproxyexecutor.Response{}, false, errExec
				}
				if isRequestScopedStop(action, okAction) {
					return cliproxyexecutor.Response{}, false, wrapRequestStopError(errExec)
				}
				if !okAction && (isRequestInvalidError(errExec) || isAntigravityValidationRequiredError(errExec)) {
					return cliproxyexecutor.Response{}, false, errExec
				}
				if result.CredentialScope {
					break
				}
				continue
			}
			m.MarkResult(creditsCtx, result)
			attemptAliasResult := resolveAttemptAliasResult(routing, c.auth, routeModel, upstreamModel, aliasResult)
			rewriteForceMappedResponse(&resp, attemptAliasResult)
			return resp, true, nil
		}
	}
	return cliproxyexecutor.Response{}, false, nil
}

func (m *Manager) tryAntigravityCreditsExecuteStream(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, bool, error) {
	updateInFlightScopeProvider(ctx, "antigravity")
	if m != nil && m.HomeEnabled() {
		return nil, false, &Error{Code: "home_fallback_unsupported", Message: "Home does not support Antigravity credits fallback", HTTPStatus: http.StatusServiceUnavailable}
	}
	if !m.localExecutionAllowed() {
		return nil, false, nil
	}
	routeModel := req.Model
	candidates, errCandidates := m.findAllAntigravityCreditsCandidateAuths(ctx, routeModel, opts)
	if errCandidates != nil {
		return nil, false, errCandidates
	}
	return m.tryAntigravityCreditsExecuteStreamCandidates(ctx, req, opts, routeModel, candidates)
}

func (m *Manager) tryAntigravityCreditsExecuteStreamCandidates(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, routeModel string, candidates []creditsCandidateEntry) (*cliproxyexecutor.StreamResult, bool, error) {
	maxRetryCredentials := m.MaxRetryCredentials()
	refusedAuths := requestRefusedAuthsFromContext(ctx)
	attempted := make(map[string]struct{})
	for _, c := range candidates {
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, false, errCtx
		}
		if _, refused := refusedAuths[c.auth.ID]; refused {
			continue
		}
		if maxRetryCredentials > 0 && len(attempted) >= maxRetryCredentials {
			break
		}
		models, pooled, aliasResult, routing := m.executionModelCandidatesWithAlias(c.auth, routeModel)
		if len(models) == 0 {
			continue
		}
		attempted[c.auth.ID] = struct{}{}
		creditsCtx := WithAntigravityCredits(ctx)
		creditsCtx = m.WithAuthTransport(creditsCtx, c.auth)
		creditsCtx = m.withAntigravityModelState(creditsCtx, c.auth)
		creditsOpts := ensureRequestedModelMetadata(opts, routeModel)
		clearSchedulerSelectionID(creditsOpts.Metadata)
		creditsCtx = contextWithRequestedModelAlias(creditsCtx, creditsOpts, routeModel)
		creditsCtx = coreusage.WithSelectionID(creditsCtx, "")
		creditsCtx = m.newUpstreamAttemptContext(creditsCtx)
		preparedAuth, errPrepare := m.prepareRequestAuth(creditsCtx, c.executor, c.auth)
		if errPrepare != nil {
			if preparedAuth != nil {
				c.auth = preparedAuth
			}
			if errAbort := m.handleAntigravityCreditsPrepareError(creditsCtx, c, routeModel, creditsOpts, errPrepare); errAbort != nil {
				return nil, false, errAbort
			}
			continue
		}
		c.auth = preparedAuth
		publishSelectedAuthMetadata(creditsOpts.Metadata, c.auth)
		creditsCtx = syncMetadataSessionToContext(creditsCtx, creditsOpts.Metadata)
		result, errStream := m.executeStreamWithModelPool(creditsCtx, c.executor, c.auth, c.provider, req, creditsOpts, routeModel, "", models, pooled, aliasResult, routing, true, false)
		if errStream != nil {
			if isCredentialTerminatedError(errStream) {
				recordRequestRefusedAuth(ctx, c.auth.ID)
				continue
			}
			if errCtx := creditsCtx.Err(); errCtx != nil {
				return nil, false, errCtx
			}
			if isRequestTerminatedError(errStream) || isRequestStopError(errStream) || isTOSViolationError(errStream) {
				return nil, false, errStream
			}
			action, okAction := matchRequestScopedErrorAction(c.auth, errStream, m.requestScopedErrorConfig(creditsCtx))
			if isRequestScopedStop(action, okAction) {
				return nil, false, wrapRequestStopError(errStream)
			}
			if !okAction && (isRequestInvalidError(errStream) || isAntigravityValidationRequiredError(errStream) || cliproxyexecutor.IsUpstreamWebsocketTurnError(errStream)) {
				return nil, false, errStream
			}
			continue
		}
		return result, true, nil
	}
	return nil, false, nil
}

func antigravityCreditsKVUnavailableError(cause error) error {
	if cause == nil {
		return &Error{Code: "home_kv_unavailable", Message: "home kv store unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	return &Error{Code: "home_kv_unavailable", Message: "home kv store unavailable: " + cause.Error(), HTTPStatus: http.StatusServiceUnavailable}
}
