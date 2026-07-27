package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

const (
	homeAuthCountMetadataKey = "__cliproxy_home_auth_count"
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

func shouldReturnLastErrorOnPickFailure(homeMode bool, lastErr error, errPick error) bool {
	if lastErr == nil {
		return false
	}
	if !homeMode {
		return true
	}
	return isHomeRequestRetryExceededError(errPick)
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
	Model         string `json:"model"`
	Provider      string `json:"provider"`
	AuthIndex     string `json:"auth_index"`
	UserAPIKey    string `json:"user_api_key"`
	ForceMapping  bool   `json:"force_mapping"`
	OriginalAlias string `json:"original_alias"`
	Auth          Auth   `json:"auth"`
}

type homeAuthDispatcher interface {
	HeartbeatOK() bool
	RPopAuth(ctx context.Context, requestedModel string, sessionID string, headers http.Header, count int) ([]byte, error)
	AbortAmbiguousDispatch()
}

var currentHomeDispatcher = func() homeAuthDispatcher {
	return home.Current()
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

func (m *Manager) retainedHomeSessionSelection(ctx context.Context, opts cliproxyexecutor.Options, model string) (*HomeDispatchSelection, bool, error) {
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
	fallbackAttempt := homeAuthCountFromMetadata(opts.Metadata) > 1
	m.mu.Lock()
	selections := m.homeSessionSelections[sessionID]
	for key, selection := range selections {
		if selection == nil {
			delete(selections, key)
			continue
		}
		matchesCredential := credentialID == "" || key.credentialID == credentialID
		matchesRoute := validRouteModel && key.routeModel == routeModel
		if !fallbackAttempt && matchesCredential && selection.Active() && matchesRoute && retained == nil {
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
	if m == nil || selection == nil || !selection.Retained() || !cliproxyexecutor.DownstreamWebsocket(ctx) || selection.Auth == nil {
		return false
	}
	sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	credentialID := strings.TrimSpace(selection.Auth.ID)
	routeModel, validRouteModel := validCanonicalHomeConcurrencyModelKey(model)
	if selection.accountedModel == "" {
		selection.accountedModel, _ = m.predictedHomeConcurrencyModel(selection.Auth, model)
	}
	if sessionID == "" || credentialID == "" || !validRouteModel || selection.accountedModel == "" {
		return false
	}
	_ = m.endMismatchedHomeSessionSelections(ctx, sessionID, credentialID, routeModel, false)
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
	m.rememberHomeRuntimeAuth(sessionID, selection.Auth)
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
	if m == nil || selection == nil || !cliproxyexecutor.DownstreamWebsocket(ctx) || selection.Auth == nil || !authWebsocketsEnabled(selection.Auth) {
		return nil
	}
	sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata)
	authID := strings.TrimSpace(selection.Auth.ID)
	if sessionID == "" || authID == "" || !selection.runtimeAuthBound.CompareAndSwap(false, true) {
		return nil
	}
	m.rememberHomeSelectionRuntimeAuth(sessionID, selection)
	if errBind := selection.Bind(func() error {
		m.forgetHomeRuntimeAuth(sessionID, authID, selection)
		return nil
	}); errBind != nil {
		selection.runtimeAuthBound.Store(false)
		m.forgetHomeRuntimeAuth(sessionID, authID, selection)
		return errBind
	}
	return nil
}

func (m *Manager) rememberHomeSelectionRuntimeAuth(sessionID string, selection *HomeDispatchSelection) {
	if m == nil || selection == nil || selection.Auth == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	authID := strings.TrimSpace(selection.Auth.ID)
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
	m.homeRuntimeAuths[sessionID][authID] = selection.Auth.Clone()
	m.homeRuntimeAuthOwners[sessionID][authID] = selection
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

func (m *Manager) pickNextViaHome(ctx context.Context, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	selection, errSelection := m.pickHomeDispatchSelection(ctx, model, opts)
	if errSelection != nil {
		return nil, nil, "", errSelection
	}
	if selection.Auth == nil || homeAuthAlreadyTried(tried, selection.Auth.ID) {
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
	retained, retainedOK, errRetained := m.retainedHomeSessionSelection(ctx, opts, requestedModel)
	if errRetained != nil {
		return nil, errRetained
	}
	if retainedOK {
		return retained, nil
	}
	if sessionID := homeExecutionSessionIDFromMetadata(opts.Metadata); sessionID != "" {
		if credentialID := pinnedAuthIDFromMetadata(opts.Metadata); credentialID != "" {
			if errEnd := m.endMismatchedHomeSessionSelections(ctx, sessionID, credentialID, requestedModel, true); errEnd != nil {
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
	pending, errBegin := registry.BeginDispatch()
	if errBegin != nil {
		return nil, &Error{Code: "home_unavailable", Message: "home execution registry unavailable", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}

	sessionID := ExtractSessionID(opts.Headers, opts.OriginalRequest, opts.Metadata)
	dispatchHeaders := homeDispatchHeaders(ctx, opts.Headers)
	raw, errRPop := client.RPopAuth(ctx, requestedModel, sessionID, dispatchHeaders, homeAuthCountFromMetadata(opts.Metadata))
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

	executor, okExecutor := m.Executor(executorKey)
	if !okExecutor && auth.Attributes != nil && strings.TrimSpace(auth.Attributes["base_url"]) != "" {
		executor, okExecutor = m.Executor("openai-compatibility")
	}
	if !okExecutor {
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
	if envelope.Present {
		selection.accountedModel = envelope.Tuple.Model
	}
	if executionSessionID := homeExecutionSessionIDFromMetadata(opts.Metadata); executionSessionID != "" && cliproxyexecutor.DownstreamWebsocket(ctx) {
		if errEnd := m.endMismatchedHomeSessionSelections(ctx, executionSessionID, strings.TrimSpace(auth.ID), requestedModel, true); errEnd != nil {
			selection.End("target_change_release_failed")
			return nil, errEnd
		}
	}
	return selection, nil
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
	var candidates []creditsCandidateEntry
	m.mu.RLock()
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
		providerKey := executorKeyFromAuth(auth)
		executor, ok := m.executors[providerKey]
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
		hint, okHint, errHint := GetAntigravityCreditsHintRequired(ctx, candidate.auth.ID)
		if errHint != nil {
			return nil, antigravityCreditsKVUnavailableError(errHint)
		}
		if okHint && hint.Known {
			if !hint.Available {
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

func shouldAttemptAntigravityCreditsFallback(m *Manager, lastErr error, providers []string) bool {
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

func (m *Manager) tryAntigravityCreditsExecute(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, bool, error) {
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
	for _, c := range candidates {
		if ctx.Err() != nil {
			return cliproxyexecutor.Response{}, false, nil
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
		publishSelectedAuthMetadata(creditsOpts.Metadata, c.auth)
		models, pooled, aliasResult := m.executionModelCandidatesWithAlias(c.auth, routeModel)
		if len(models) == 0 {
			continue
		}
		for _, upstreamModel := range models {
			resultModel := m.stateModelForExecution(c.auth, routeModel, upstreamModel, pooled)
			execReq := req
			execReq.Model = upstreamModel
			resp, errExec := c.executor.Execute(creditsCtx, c.auth, execReq, creditsOpts)
			result := Result{AuthID: c.auth.ID, Provider: c.provider, Model: resultModel, Success: errExec == nil}
			if errExec != nil {
				result.Error = resultErrorFromError(errExec)
				if ra := retryAfterFromError(errExec); ra != nil {
					result.RetryAfter = ra
				}
				m.MarkResult(creditsCtx, result)
				continue
			}
			m.MarkResult(creditsCtx, result)
			rewriteForceMappedResponse(&resp, aliasResult)
			return resp, true, nil
		}
	}
	return cliproxyexecutor.Response{}, false, nil
}

func (m *Manager) tryAntigravityCreditsExecuteStream(ctx context.Context, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, bool, error) {
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
	for _, c := range candidates {
		if ctx.Err() != nil {
			return nil, false, nil
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
		publishSelectedAuthMetadata(creditsOpts.Metadata, c.auth)
		models, pooled, aliasResult := m.executionModelCandidatesWithAlias(c.auth, routeModel)
		if len(models) == 0 {
			continue
		}
		result, errStream := m.executeStreamWithModelPool(creditsCtx, c.executor, c.auth, c.provider, req, creditsOpts, routeModel, "", models, pooled, aliasResult, true, false)
		if errStream != nil {
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
