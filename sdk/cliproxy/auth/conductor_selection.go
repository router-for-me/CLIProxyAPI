package auth

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func (m *Manager) SetPluginScheduler(scheduler PluginScheduler) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.pluginScheduler = scheduler
	m.mu.Unlock()
}

func (m *Manager) hasPluginScheduler() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	scheduler := m.pluginScheduler
	m.mu.RUnlock()
	if scheduler == nil {
		return false
	}
	if state, ok := scheduler.(pluginSchedulerState); ok {
		return state.HasScheduler()
	}
	return true
}

func isBuiltInSelector(selector Selector) bool {
	switch selector.(type) {
	case *RoundRobinSelector, *WeightedRoundRobinSelector, *FillFirstSelector, *P2CSelector, *LeastUsedSelector, *WeightedByEntrySelector, *HeadroomByUsageSelector:
		return true
	default:
		return false
	}
}

type requiredAuthKindContextKey struct{}
type credentialPolicyContextKey struct{}

type authSelectionEligibility struct {
	requiredKind     string
	credentialPolicy string
	disallowFreeAuth bool
}

func withRequiredAuthKind(ctx context.Context, requiredKind string) context.Context {
	return context.WithValue(ctx, requiredAuthKindContextKey{}, requiredKind)
}

func withCredentialPolicy(ctx context.Context, policy string) context.Context {
	return context.WithValue(ctx, credentialPolicyContextKey{}, policy)
}

func credentialPolicyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	policy, _ := ctx.Value(credentialPolicyContextKey{}).(string)
	return policy
}

func authSelectionEligibilityForRequest(ctx context.Context, opts cliproxyexecutor.Options) authSelectionEligibility {
	eligibility := authSelectionEligibility{disallowFreeAuth: disallowFreeAuthFromMetadata(opts.Metadata)}
	if ctx != nil {
		eligibility.requiredKind, _ = ctx.Value(requiredAuthKindContextKey{}).(string)
		eligibility.credentialPolicy, _ = ctx.Value(credentialPolicyContextKey{}).(string)
	}
	return eligibility
}

func (e authSelectionEligibility) allows(auth *Auth) bool {
	if auth == nil {
		return false
	}
	if e.requiredKind != "" && auth.AuthKind() != e.requiredKind {
		return false
	}
	if e.credentialPolicy != "" && !credentialPolicyAllows(e.credentialPolicy, auth) {
		return false
	}
	return !e.disallowFreeAuth || !isFreeCodexAuth(auth)
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

// RefreshSchedulerAll rebuilds scheduler entries for every known auth.
func (m *Manager) RefreshSchedulerAll() {
	if m == nil {
		return
	}
	m.mu.RLock()
	ids := make([]string, 0, len(m.auths))
	for id := range m.auths {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		m.RefreshSchedulerEntry(id)
	}
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
	outgoing := m.selector
	m.selector = selector
	m.mu.Unlock()
	// A swap replaces a resource-holding selector the manager owned (routing
	// config changes build a fresh SessionAffinitySelector per update). Stop
	// the outgoing one so its cache goroutine and map do not leak; a shared
	// instance being re-assigned survives.
	if outgoingAffinity, ok := outgoing.(*SessionAffinitySelector); ok && outgoingAffinity != selector {
		if incomingAffinity, okIncoming := selector.(*SessionAffinitySelector); okIncoming {
			if moved := incomingAffinity.cache.Adopt(outgoingAffinity.cache); moved > 0 {
				log.Infof("session-affinity: handed off %d binding(s) across selector swap", moved)
			}
		} else {
			log.Infof("session-affinity: dropping %d binding(s) — selector swapped to %T", len(outgoingAffinity.Snapshot()), selector)
		}
		outgoingAffinity.Stop()
	}
	if m.scheduler != nil {
		m.scheduler.setSelector(selector)
		m.syncScheduler()
	}
}

// Selector returns the current credential selector.
func (m *Manager) Selector() Selector {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.selector
}

// SessionAffinityEnabled reports whether the active selector pins requests to
// credentials by session (i.e. it implements SessionAffinityView). Returns
// false when routing.session-affinity is disabled and the selector is a plain
// round-robin / fill-first. Management surfaces use this to render the
// "affinity disabled" state instead of an empty binding list.
func (m *Manager) SessionAffinityEnabled() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	_, ok := m.selector.(SessionAffinityView)
	m.mu.RUnlock()
	return ok
}

// SessionAffinitySnapshot returns the live session→auth bindings, or nil when
// session affinity is not in use. The returned slice is a stable copy; callers
// may iterate it without holding the manager lock.
func (m *Manager) SessionAffinitySnapshot() []SessionAffinityBinding {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	view, ok := m.selector.(SessionAffinityView)
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return view.Snapshot()
}

// InvalidateSessionAffinityBinding drops every binding keyed by the given
// session ID. No-op (returns false) when session affinity is disabled, so
// callers can distinguish "feature off" from "session not found". Used by the
// management revoke endpoint to manually unpin a session whose requests keep
// erroring against a stuck/auth-unavailable credential.
func (m *Manager) InvalidateSessionAffinityBinding(sessionID string) bool {
	if m == nil || sessionID == "" {
		return false
	}
	m.mu.RLock()
	view, ok := m.selector.(SessionAffinityView)
	m.mu.RUnlock()
	if !ok {
		return false
	}
	view.InvalidateSession(sessionID)
	return true
}

// SetStore swaps the underlying persistence store.
func (m *Manager) SetStore(store Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
}

// SetCooldownStateStore swaps the independent runtime cooldown state store.
func (m *Manager) SetCooldownStateStore(store CooldownStateStore) {
	if m == nil {
		return
	}
	m.configCooldownMu.Lock()
	defer m.configCooldownMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooldownStore = store
}

// SetRoundTripperProvider register a provider that returns a per-auth RoundTripper.
func (m *Manager) SetRoundTripperProvider(p RoundTripperProvider) {
	m.mu.Lock()
	m.rtProvider = p
	m.mu.Unlock()
}

func (m *Manager) availableAuthsForRouteModel(auths []*Auth, provider, routeModel string, now time.Time) ([]*Auth, error) {
	return m.availableAuthsForRouteModelWithPriorityMode(auths, provider, routeModel, now, false)
}

func (m *Manager) availableAuthsForRouteModelAcrossPriorities(auths []*Auth, provider, routeModel string, now time.Time) ([]*Auth, error) {
	return m.availableAuthsForRouteModelWithPriorityMode(auths, provider, routeModel, now, true)
}

func (m *Manager) availableAuthsForRouteModelWithPriorityMode(auths []*Auth, provider, routeModel string, now time.Time, allPriorities bool) ([]*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	availableByPriority := make(map[int][]*Auth)
	cooldownCount := 0
	var earliest time.Time
	for _, candidate := range auths {
		checkModel := m.selectionModelForAuth(candidate, routeModel)
		blocked, reason, next := isAuthBlockedForModel(candidate, checkModel, now)
		if !blocked {
			priority := authPriority(candidate)
			availableByPriority[priority] = append(availableByPriority[priority], candidate)
			continue
		}
		if reason == blockReasonCooldown {
			cooldownCount++
			if !next.IsZero() && (earliest.IsZero() || next.Before(earliest)) {
				earliest = next
			}
		}
	}

	if len(availableByPriority) == 0 {
		if cooldownCount == len(auths) && !earliest.IsZero() {
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

	return availableAuthsFromPriorityBuckets(availableByPriority, allPriorities), nil
}

// availableAuthsForSelector reports the candidates handed to priority-scoped consumers such as
// the plugin scheduler, plus the candidates handed to the configured selector. Both are equal
// unless session affinity is active, in which case the selector additionally receives lower
// priority tiers so an established binding can be validated instead of being preempted by a
// recovered higher-priority credential.
func (m *Manager) availableAuthsForSelector(selector Selector, auths []*Auth, provider, routeModel string, now time.Time) (priorityAuths, selectorAuths []*Auth, err error) {
	if _, sessionAffinity := selector.(*SessionAffinitySelector); !sessionAffinity {
		priorityAuths, err = m.availableAuthsForRouteModel(auths, provider, routeModel, now)
		if err != nil {
			return nil, nil, err
		}
		priorityAuths = cloneAuthSlice(priorityAuths)
		return priorityAuths, priorityAuths, nil
	}

	// One availability pass and one clone pass serve both lists: the highest priority tier is a
	// subset of the across-priority candidates, so it is narrowed from the same cloned auths.
	selectorAuths, err = m.availableAuthsForRouteModelAcrossPriorities(auths, provider, routeModel, now)
	if err != nil {
		return nil, nil, err
	}
	selectorAuths = cloneAuthSlice(selectorAuths)
	return highestPriorityAuths(selectorAuths), selectorAuths, nil
}

func selectionArgForSelector(selector Selector, routeModel string) string {
	if isBuiltInSelector(selector) {
		return ""
	}
	return routeModel
}

func restoreModelCooldownErrorModel(err error, requestedModel string) error {
	if err == nil || requestedModel == "" {
		return err
	}
	var cooldownErr *modelCooldownError
	if !errors.As(err, &cooldownErr) || cooldownErr == nil || cooldownErr.model != "" {
		return err
	}
	return newModelCooldownError(requestedModel, cooldownErr.provider, cooldownErr.resetIn)
}

func schedulerAttributeSensitive(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	normalized := strings.NewReplacer("-", "_", ".", "_", " ", "_").Replace(key)
	compact := strings.NewReplacer("_", "", "-", "", ".", "", " ", "").Replace(key)
	for _, fragment := range []string{
		"api_key",
		"apikey",
		"token",
		"secret",
		"cookie",
		"credential",
		"password",
		"storage",
		"authorization",
		"auth_header",
		"proxy_url",
	} {
		if strings.Contains(key, fragment) || strings.Contains(normalized, fragment) || strings.Contains(compact, fragment) {
			return true
		}
	}
	return false
}

func schedulerSafeAttributes(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]string, len(src))
	for key, value := range src {
		if schedulerAttributeSensitive(key) {
			continue
		}
		out[key] = value
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneSchedulerAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func cloneAuthSlice(auths []*Auth) []*Auth {
	if len(auths) == 0 {
		return nil
	}
	out := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		out = append(out, auth.Clone())
	}
	return out
}

func schedulerAuthCandidates(auths []*Auth) []pluginapi.SchedulerAuthCandidate {
	if len(auths) == 0 {
		return nil
	}
	out := make([]pluginapi.SchedulerAuthCandidate, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		out = append(out, pluginapi.SchedulerAuthCandidate{
			ID:         auth.ID,
			Provider:   strings.ToLower(strings.TrimSpace(auth.Provider)),
			Priority:   authPriority(auth),
			Status:     string(auth.Status),
			Attributes: schedulerSafeAttributes(auth.Attributes),
		})
	}
	return out
}

func schedulerProviders(provider string, providers []string) []string {
	out := make([]string, 0, len(providers)+1)
	seen := make(map[string]struct{}, len(providers)+1)
	addProvider := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || value == "mixed" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	addProvider(provider)
	for _, value := range providers {
		addProvider(value)
	}
	return out
}

func schedulerOptions(opts cliproxyexecutor.Options) pluginapi.SchedulerOptions {
	return pluginapi.SchedulerOptions{
		Headers:  cloneHTTPHeader(opts.Headers),
		Metadata: cloneSchedulerAnyMap(opts.Metadata),
	}
}

func pickSchedulerAuthByID(candidates []*Auth, authID string) *Auth {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	for _, candidate := range candidates {
		if candidate != nil && candidate.ID == authID {
			return candidate
		}
	}
	return nil
}

func builtinSchedulerStrategy(delegate string) (schedulerStrategy, bool) {
	switch strings.TrimSpace(delegate) {
	case pluginapi.SchedulerBuiltinRoundRobin:
		return schedulerStrategyRoundRobin, true
	case pluginapi.SchedulerBuiltinFillFirst:
		return schedulerStrategyFillFirst, true
	default:
		return schedulerStrategyCustom, false
	}
}

func (m *Manager) pickViaBuiltinScheduler(ctx context.Context, strategy schedulerStrategy, provider string, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, bool, error) {
	if m == nil || m.scheduler == nil {
		return nil, false, nil
	}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	var selected *Auth
	var errPick error
	if providerKey == "mixed" {
		selected, _, errPick = m.scheduler.pickMixedWithStrategy(ctx, providers, model, opts, tried, strategy)
		if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
			m.syncScheduler()
			selected, _, errPick = m.scheduler.pickMixedWithStrategy(ctx, providers, model, opts, tried, strategy)
		}
	} else {
		selected, errPick = m.scheduler.pickSingleWithStrategy(ctx, providerKey, model, opts, tried, strategy)
		if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
			m.syncScheduler()
			selected, errPick = m.scheduler.pickSingleWithStrategy(ctx, providerKey, model, opts, tried, strategy)
		}
	}
	if errPick != nil {
		return nil, true, errPick
	}
	if selected == nil {
		return nil, true, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	return selected, true, nil
}

func (m *Manager) pickViaPluginScheduler(ctx context.Context, scheduler PluginScheduler, provider string, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}, candidates []*Auth) (*Auth, bool, error) {
	if scheduler == nil || len(candidates) == 0 {
		return nil, false, nil
	}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	requestProvider := providerKey
	if providerKey == "mixed" {
		requestProvider = ""
	}
	req := pluginapi.SchedulerPickRequest{
		Provider:   requestProvider,
		Providers:  schedulerProviders(providerKey, providers),
		Model:      model,
		Stream:     opts.Stream,
		Options:    schedulerOptions(opts),
		Candidates: schedulerAuthCandidates(candidates),
	}
	resp, handled, errPick := scheduler.PickAuth(ctx, req)
	if errPick != nil {
		return nil, true, errPick
	}
	if !handled || !resp.Handled {
		return nil, false, nil
	}
	if selected := pickSchedulerAuthByID(candidates, resp.AuthID); selected != nil {
		return selected, true, nil
	}

	strategy, okStrategy := builtinSchedulerStrategy(resp.DelegateBuiltin)
	if !okStrategy {
		return nil, false, nil
	}
	return m.pickViaBuiltinScheduler(ctx, strategy, providerKey, providers, model, opts, tried)
}

func (m *Manager) authSupportsRouteModel(registryRef *registry.ModelRegistry, auth *Auth, routeModel string) bool {
	if registryRef == nil || auth == nil {
		return true
	}
	routeKey := canonicalModelKey(routeModel)
	if routeKey == "" {
		return true
	}
	if registryRef.ClientSupportsModel(auth.ID, routeKey) {
		return true
	}
	selectionKey := m.selectionModelKeyForAuth(auth, routeModel)
	if selectionKey != "" && selectionKey != routeKey && registryRef.ClientSupportsModel(auth.ID, selectionKey) {
		return true
	}
	// The registry registers only the request-facing alias name when an
	// OAuth model alias is configured with fork=false (the default): the
	// upstream name is dropped to avoid exposing it to clients. A request
	// may still arrive naming the upstream model directly — e.g. because a
	// per-API-key model_route pins the upstream name. Such an auth DOES hold
	// valid material for the model; rejecting it here (before the runtime
	// alias resolution in executionModelCandidatesWithAlias runs) produces a
	// spurious 503 "auth_unavailable: no auth available". Accept the auth if
	// any of its alias entries maps FROM the routeModel (treated as upstream
	// name) TO a registered alias name, or FROM the routeModel (treated as
	// alias) TO a registered upstream name.
	return m.authSupportsRouteModelViaAlias(registryRef, auth, routeKey)
}

// authSupportsRouteModelViaAlias is the bidirectional tail of
// authSupportsRouteModel: it accepts an auth whose OAuth model-alias table
// links the route model to a model ID that IS registered for the auth,
// covering the fork=false case where the route model names the dropped
// (non-registered) side of an alias pair.
func (m *Manager) authSupportsRouteModelViaAlias(registryRef *registry.ModelRegistry, auth *Auth, routeKey string) bool {
	if registryRef == nil || auth == nil || routeKey == "" {
		return false
	}
	for _, alias := range OAuthModelAliasesFromAttributes(authAttributes(auth)) {
		name := strings.TrimSpace(alias.Name)
		aliasName := strings.TrimSpace(alias.Alias)
		if name == "" || aliasName == "" {
			continue
		}
		// routeModel is the upstream name; alias name is request-facing and
		// registered (fork=false drops the upstream id).
		if strings.EqualFold(canonicalModelKey(name), routeKey) &&
			registryRef.ClientSupportsModel(auth.ID, canonicalModelKey(aliasName)) {
			return true
		}
		// routeModel is the request-facing alias; upstream name is registered
		// (covers the inverse fork=false configuration where the alias id is
		// dropped but the upstream id is kept).
		if strings.EqualFold(canonicalModelKey(aliasName), routeKey) &&
			registryRef.ClientSupportsModel(auth.ID, canonicalModelKey(name)) {
			return true
		}
	}
	// Fall back to the global alias table when the auth has no per-auth
	// attributes (legacy / centrally configured aliases).
	channel := modelAliasChannel(auth)
	if channel == "" {
		return false
	}
	raw := m.oauthModelAlias.Load()
	table, _ := raw.(*oauthModelAliasTable)
	if table == nil || table.reverse == nil {
		return false
	}
	rev := table.reverse[channel]
	if rev == nil {
		return false
	}
	for aliasLower, entry := range rev {
		upstream := canonicalModelKey(entry.upstreamModel)
		// routeModel == upstream name; the registered alias id may match.
		if upstream != "" && upstream == routeKey &&
			registryRef.ClientSupportsModel(auth.ID, canonicalModelKey(aliasLower)) {
			return true
		}
		// routeModel == alias name; the registered upstream id may match.
		if canonicalModelKey(aliasLower) == routeKey && upstream != "" &&
			registryRef.ClientSupportsModel(auth.ID, upstream) {
			return true
		}
	}
	return false
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

// LiveProviderKeysForModel returns provider keys that currently register the
// requested model. The registry normally contains the authoritative provider
// list, but adding each matching auth's routing key repairs a short transition
// window where an auth was re-rendered with a per-row provider_key before its
// registry client was re-registered. This also keeps bare OAuth/legacy keys
// distinct from compound built-in API-key row keys.
func (m *Manager) LiveProviderKeysForModel(model string) []string {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}

	providers := registry.GetGlobalRegistry().GetModelProviders(model)
	seen := make(map[string]struct{}, len(providers))
	out := make([]string, 0, len(providers))
	add := func(provider string) {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider == "" {
			return
		}
		if _, exists := seen[provider]; exists {
			return
		}
		seen[provider] = struct{}{}
		out = append(out, provider)
	}
	for _, provider := range providers {
		add(provider)
	}
	if m == nil {
		return out
	}

	m.mu.RLock()
	auths := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		auths = append(auths, auth.Clone())
	}
	m.mu.RUnlock()
	registryRef := registry.GetGlobalRegistry()
	for _, auth := range auths {
		if auth == nil || !registryRef.ClientSupportsModel(auth.ID, model) {
			continue
		}
		add(routingKeyFromAuth(auth))
		// For OpenAI-compat entries, also surface the entry-level key so the
		// picker can show each named entry (or each persisted "key-<id>" row)
		// as its own live row. Built-in channels never set this attribute.
		if auth.Attributes != nil {
			if entryKey := strings.ToLower(strings.TrimSpace(auth.Attributes[AttributeEntryProviderKey])); entryKey != "" {
				add(entryKey)
			}
		}
	}
	return out
}

// AvailableProviders returns the set of provider keys that currently have at least one
// registered auth record that is not disabled. It is a best-effort snapshot for routing
// decisions and does not account for per-model cooldowns or transient runtime availability.
// Disabled auths (Disabled flag or StatusDisabled) are excluded so routing does not target
// providers that auth selection would refuse to use, which would otherwise cause execution
// failures instead of falling back to lower-priority routers.
func (m *Manager) AvailableProviders() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]struct{}, len(m.auths))
	out := make([]string, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if provider == "" {
			continue
		}
		if _, ok := seen[provider]; ok {
			continue
		}
		seen[provider] = struct{}{}
		out = append(out, provider)
	}
	sort.Strings(out)
	return out
}

// HasProviderAuth reports whether at least one non-disabled auth record is registered for
// the provider. Disabled auths (Disabled flag or StatusDisabled) are excluded to match the
// behavior of auth selection, which refuses to pick disabled credentials.
func (m *Manager) HasProviderAuth(provider string) bool {
	if m == nil {
		return false
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
			continue
		}
		if strings.ToLower(strings.TrimSpace(auth.Provider)) == provider {
			return true
		}
	}
	return false
}

func (m *Manager) retrySettings() (int, int, time.Duration) {
	if m == nil {
		return 0, 0, 0
	}
	return int(m.requestRetry.Load()), int(m.maxRetryCredentials.Load()), time.Duration(m.maxRetryInterval.Load())
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
		if !authMatchesAnyProvider(auth, providerSet) {
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
		if !authMatchesAnyProvider(auth, providerSet) {
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
	var homeBusy *HomeConcurrencyBusyError
	if errors.As(err, &homeBusy) && homeBusy != nil {
		return 0, false
	}
	if maxWait <= 0 {
		return 0, false
	}
	status := statusCodeFromError(err)
	if status == http.StatusOK {
		return 0, false
	}
	if isRequestInvalidError(err) {
		return 0, false
	}
	// Bounded cooldown wait (G5): the routing.cooldown_wait budget is applied
	// per-wait — each shouldRetryAfterError call applies it independently (the
	// function is stateless), so the worst-case total server-side wait across
	// re-dispatches is max_attempts x max_wait_ms. It only ever tightens the
	// pre-existing max-retry-interval ceiling, never loosens it.
	if budget := cooldownWaitBudgetMS.Load(); budget > 0 {
		budgetWait := time.Duration(budget) * time.Millisecond
		if budgetWait < maxWait {
			maxWait = budgetWait
		}
	}
	wait, found := m.closestCooldownWait(providers, model, attempt)
	if found {
		if attemptCap := int(cooldownWaitMaxAttempts.Load()); attemptCap > 0 && attempt >= attemptCap {
			return 0, false
		}
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

// cooldownWaitJitterCap bounds the random jitter added to cooldown waits so a
// long wait is never extended by more than this amount.
const cooldownWaitJitterCap = 2 * time.Second

// jitteredCooldownWait adds a small random delay to a cooldown wait so
// concurrent requests waiting on the same recovery deadline do not wake in
// lockstep and stampede the first credential that recovers. The jitter never
// pushes the total wait past maxWait, which callers have already enforced as
// the retry ceiling; maxWait <= 0 means no ceiling.
func jitteredCooldownWait(wait, maxWait time.Duration) time.Duration {
	if wait <= 0 {
		return wait
	}
	jitterRange := wait / 4
	if jitterRange > cooldownWaitJitterCap {
		jitterRange = cooldownWaitJitterCap
	}
	if maxWait > 0 && jitterRange > maxWait-wait {
		jitterRange = maxWait - wait
	}
	if jitterRange <= 0 {
		return wait
	}
	return wait + rand.N(jitterRange)
}

// waitForCooldown blocks until the closest upstream cooldown expires (bounded
// by maxWait) or ctx is canceled. A request held here produces no output, so
// every nonzero wait is logged: "stopped without reason" reports were usually
// requests silently waiting in cooldown with no trace.
func waitForCooldown(ctx context.Context, wait, maxWait time.Duration, providers []string, model string) error {
	if wait <= 0 {
		return nil
	}
	log.WithFields(log.Fields{
		"providers":   strings.Join(providers, ","),
		"model":       model,
		"wait_ms":     wait.Milliseconds(),
		"max_wait_ms": maxWait.Milliseconds(),
	}).Info("all upstreams cooling down; delaying retry")
	timer := time.NewTimer(jitteredCooldownWait(wait, maxWait))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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

// executorForRoutingKey resolves an exact provider executor before translating
// a built-in compound row key to its bare channel. Exact lookup is required
// for plugin and OpenAI-compatible identifiers that may contain colons (for
// example, "claude:123" or "foo:bar").
func (m *Manager) executorForRoutingKey(provider string) (ProviderExecutor, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil, false
	}
	if executor, okExecutor := m.Executor(provider); okExecutor {
		return executor, true
	}
	executorKey := executorKeyFromRoutingKey(provider)
	if executorKey != provider {
		if executor, okExecutor := m.Executor(executorKey); okExecutor {
			return executor, true
		}
	}
	// OpenAI-compatible entry routing keys are deliberately opaque because a
	// provider name may contain a colon. Resolve them through the auth metadata
	// instead of guessing where the provider/entry boundary is.
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executorForEntryRoutingKeyLocked(provider)
}

// executorForRoutingKeyLocked is the lock-held variant of
// executorForRoutingKey. Callers must hold m.mu for reading or writing.
func (m *Manager) executorForRoutingKeyLocked(provider string) (ProviderExecutor, bool) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return nil, false
	}
	lookup := func(key string) (ProviderExecutor, bool) {
		executor, okExecutor := m.executors[key]
		return executor, okExecutor && executor != nil
	}
	if executor, okExecutor := lookup(provider); okExecutor {
		return executor, true
	}
	executorKey := executorKeyFromRoutingKey(provider)
	if executorKey != provider {
		if executor, okExecutor := lookup(executorKey); okExecutor {
			return executor, true
		}
	}
	return m.executorForEntryRoutingKeyLocked(provider)
}

// executorForEntryRoutingKeyLocked resolves an OpenAI-compatible entry routing
// key through the matching auth's provider metadata. The entry key is opaque:
// provider names may contain colons, while persisted entries use a
// ":key-<id>" suffix, so parsing the string alone would be ambiguous.
func (m *Manager) executorForEntryRoutingKeyLocked(provider string) (ProviderExecutor, bool) {
	if m == nil {
		return nil, false
	}
	for _, auth := range m.auths {
		if auth == nil || auth.Attributes == nil {
			continue
		}
		entryKey := strings.ToLower(strings.TrimSpace(auth.Attributes[AttributeEntryProviderKey]))
		if entryKey == "" || entryKey != provider {
			continue
		}
		executorKey := executorKeyFromAuth(auth)
		if executorKey == "" {
			continue
		}
		if executor, okExecutor := m.executors[executorKey]; okExecutor && executor != nil {
			return executor, true
		}
	}
	return nil, false
}

// PoolStrategyForProviderKeys returns the shared in-pool routing strategy of
// the auths matching any of the given provider keys. Returns "" when no
// matching auth carries a pool strategy or when matching pools disagree —
// mixed pools keep the global routing strategy. Matching reuses the
// auth-selection semantics (authMatchesAnyProvider / provider_key +
// entry_provider_key), so a pool key matches both the row key and its
// per-entry keys.
func (m *Manager) PoolStrategyForProviderKeys(keys ...string) string {
	if m == nil || len(keys) == 0 {
		return ""
	}
	providerSet := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}
	if len(providerSet) == 0 {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var (
		found    bool
		strategy string
	)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		if !authMatchesAnyProvider(auth, providerSet) {
			continue
		}
		poolStrategy := poolStrategyFromAuth(auth)
		if poolStrategy == "" {
			continue
		}
		if found && !strings.EqualFold(strategy, poolStrategy) {
			return ""
		}
		strategy = poolStrategy
		found = true
	}
	if !found {
		return ""
	}
	return strings.ToLower(strategy)
}

func (m *Manager) isCompoundRoutingKey(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return false
	}
	// A registered exact executor identifies a literal provider key, not a
	// built-in row key. This disambiguates plugin IDs such as "claude:123".
	if _, okExecutor := m.Executor(provider); okExecutor {
		return false
	}
	if executorKeyFromRoutingKey(provider) != provider {
		return true
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, auth := range m.auths {
		if auth == nil || auth.Attributes == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(auth.Attributes[AttributeEntryProviderKey]), provider) {
			return true
		}
	}
	return false
}

// CloseExecutionSession asks all registered executors to release the supplied execution session.
func (m *Manager) CloseExecutionSession(sessionID string) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}

	m.mu.Lock()
	var selections []*HomeDispatchSelection
	if sessionID == CloseAllExecutionSessionsID {
		m.clearHomeRuntimeAuthsLocked()
		selections = m.takeAllHomeSessionSelectionsLocked()
		m.clearHomeSessionLocks()
	} else {
		m.clearHomeRuntimeAuthsForSessionLocked(sessionID)
		selections = m.takeHomeSessionSelectionsLocked(sessionID)
		m.homeSessionLocks.Delete(sessionID)
	}
	executors := make([]ProviderExecutor, 0, len(m.executors))
	for _, exec := range m.executors {
		executors = append(executors, exec)
	}
	m.mu.Unlock()

	for _, selection := range selections {
		selection.End("session_closed")
	}
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
	eligibility := authSelectionEligibilityForRequest(ctx, opts)

	m.mu.RLock()
	selector := m.selector
	pluginScheduler := m.pluginScheduler
	executor, okExecutor := m.executorForRoutingKeyLocked(provider)
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
		if candidate == nil || !authMatchesProvider(candidate, provider) || candidate.Disabled {
			continue
		}
		if pinnedAuthID != "" && candidate.ID != pinnedAuthID {
			continue
		}
		if !eligibility.allows(candidate) {
			continue
		}
		if _, used := tried[candidate.ID]; used {
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
	available, selectorAuths, errAvailable := m.availableAuthsForSelector(selector, candidates, provider, model, time.Now())
	if errAvailable != nil {
		m.mu.RUnlock()
		return nil, nil, errAvailable
	}
	m.mu.RUnlock()

	selected, handled, errPick := m.pickViaPluginScheduler(ctx, pluginScheduler, provider, []string{provider}, model, opts, tried, available)
	if errPick != nil {
		return nil, nil, errPick
	}
	if !handled {
		selectorCtx := withWeightedSelectorStateModel(ctx, selector, model)
		selected, errPick = selector.Pick(selectorCtx, provider, selectionArgForSelector(selector, model), opts, selectorAuths)
		if errPick != nil {
			if isBuiltInSelector(selector) {
				errPick = restoreModelCooldownErrorModel(errPick, model)
			}
			return nil, nil, errPick
		}
	}
	if selected == nil {
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
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

// SelectAuth selects one credential through the configured scheduling strategy.
// It does not execute or alter the selected credential's result state.
func (m *Manager) SelectAuth(ctx context.Context, provider, model string, opts cliproxyexecutor.Options) (*Auth, error) {
	if m != nil && m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	selected, _, errPick := m.pickNextLegacy(ctx, provider, model, opts, nil)
	if errPick != nil {
		return nil, errPick
	}
	if m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	return selected, nil
}

// SelectAuthByKind selects one credential of the required kind through the
// configured scheduling strategy. Credentials of other kinds are skipped.
func (m *Manager) SelectAuthByKind(ctx context.Context, provider, model, requiredKind string, opts cliproxyexecutor.Options) (*Auth, error) {
	if m != nil && m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	requiredKind = normalizeAuthKind(requiredKind)
	if requiredKind == "" {
		return nil, &Error{Code: "invalid_auth_kind", Message: "required auth kind is invalid", HTTPStatus: http.StatusBadRequest}
	}

	selectionCtx := withRequiredAuthKind(ctx, requiredKind)
	selected, _, errPick := m.pickNextLegacy(selectionCtx, provider, model, opts, nil)
	if errPick != nil {
		return nil, errPick
	}
	if selected == nil {
		return nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	if m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	return selected, nil
}

// SelectAuthWithCredentialPolicy selects one local credential allowed by a fixed policy.
func (m *Manager) SelectAuthWithCredentialPolicy(ctx context.Context, provider, model, policy string, opts cliproxyexecutor.Options) (*Auth, error) {
	if m != nil && m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	policy = normalizeCredentialPolicy(policy)
	if policy == "" {
		return nil, &Error{Code: "invalid_credential_policy", Message: "credential policy is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	selectionCtx := withCredentialPolicy(ctx, policy)
	selected, _, errPick := m.pickNextLegacy(selectionCtx, provider, model, opts, nil)
	if errPick != nil {
		return nil, errPick
	}
	if selected == nil || !credentialPolicyAllows(policy, selected) {
		return nil, &Error{Code: "auth_not_found", Message: "selector returned no eligible auth"}
	}
	if m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "legacy auth selection is unavailable while Home is enabled", HTTPStatus: http.StatusServiceUnavailable}
	}
	return selected, nil
}

// SelectHomeAuthWithCredentialPolicy selects a policy-constrained Home dispatch while retaining its execution scope.
func (m *Manager) SelectHomeAuthWithCredentialPolicy(ctx context.Context, provider, model, policy string, opts cliproxyexecutor.Options) (*HomeDispatchSelection, error) {
	policy = normalizeCredentialPolicy(policy)
	if policy == "" {
		return nil, &Error{Code: "invalid_credential_policy", Message: "credential policy is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if m == nil || !m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "home control center unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	selectionCtx := withCredentialPolicy(ctx, policy)
	homeAuthCount := homeAuthCountFromMetadata(opts.Metadata)
	tried := make(map[string]struct{})
	for {
		selectionOpts := withHomeAuthCount(opts, homeAuthCount)
		selection, errSelection := m.pickHomeDispatchSelection(selectionCtx, model, selectionOpts)
		if errSelection != nil {
			return nil, errSelection
		}
		providerMatches := strings.TrimSpace(provider) == "" || strings.EqualFold(strings.TrimSpace(selection.Provider), strings.TrimSpace(provider))
		policyMatches := credentialPolicyAllows(policy, selection.Auth)
		if providerMatches && policyMatches {
			return selection, nil
		}

		authID := ""
		if selection.Auth != nil {
			authID = strings.TrimSpace(selection.Auth.ID)
		}
		reason := "credential_policy_mismatch"
		if !providerMatches {
			reason = "provider_mismatch"
		}
		if errEnd := m.endHomeSelectionBeforeRedispatch(selectionCtx, selection, reason); errEnd != nil {
			return nil, errEnd
		}
		if authID == "" {
			return nil, &Error{Code: "auth_not_found", Message: "selected auth has no ID"}
		}
		if _, alreadyTried := tried[authID]; alreadyTried {
			return nil, &Error{Code: "auth_not_found", Message: "selector repeatedly returned an ineligible auth"}
		}
		tried[authID] = struct{}{}
		homeAuthCount++
	}
}

// SelectHomeAuthByKind selects a Home dispatch while retaining its execution scope.
func (m *Manager) SelectHomeAuthByKind(ctx context.Context, provider string, model string, requiredKind string, opts cliproxyexecutor.Options) (*HomeDispatchSelection, error) {
	requiredKind = normalizeAuthKind(requiredKind)
	if requiredKind == "" {
		return nil, &Error{Code: "invalid_auth_kind", Message: "required auth kind is invalid", HTTPStatus: http.StatusBadRequest}
	}
	if m == nil || !m.HomeEnabled() {
		return nil, &Error{Code: "home_unavailable", Message: "home control center unavailable", HTTPStatus: http.StatusServiceUnavailable}
	}

	homeAuthCount := homeAuthCountFromMetadata(opts.Metadata)
	tried := make(map[string]struct{})
	for {
		selectionOpts := withHomeAuthCount(opts, homeAuthCount)
		selection, errSelection := m.pickHomeDispatchSelection(ctx, model, selectionOpts)
		if errSelection != nil {
			return nil, errSelection
		}
		providerMatches := strings.TrimSpace(provider) == "" || strings.EqualFold(strings.TrimSpace(selection.Provider), strings.TrimSpace(provider))
		selectionAuth := selection.CloneAuth()
		kindMatches := selectionAuth != nil && selectionAuth.AuthKind() == requiredKind
		if providerMatches && kindMatches {
			return selection, nil
		}

		authID := ""
		if selectionAuth != nil {
			authID = strings.TrimSpace(selectionAuth.ID)
		}
		reason := "auth_kind_mismatch"
		if !providerMatches {
			reason = "provider_mismatch"
		}
		if errEnd := m.endHomeSelectionBeforeRedispatch(ctx, selection, reason); errEnd != nil {
			return nil, errEnd
		}
		if authID == "" {
			return nil, &Error{Code: "auth_not_found", Message: "selected auth has no ID"}
		}
		if _, alreadyTried := tried[authID]; alreadyTried {
			return nil, &Error{Code: "auth_not_found", Message: "selector repeatedly returned an ineligible auth"}
		}
		tried[authID] = struct{}{}
		homeAuthCount++
	}
}

func (m *Manager) pickNext(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	if m.HomeEnabled() {
		auth, exec, _, err := m.pickNextViaHome(ctx, model, opts, tried)
		return auth, exec, err
	}

	if m.hasPluginScheduler() || !m.useSchedulerFastPath() {
		return m.pickNextLegacy(ctx, provider, model, opts, tried)
	}
	if m.isCompoundRoutingKey(provider) {
		return m.pickNextLegacy(ctx, provider, model, opts, tried)
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	if strings.TrimSpace(model) != "" {
		m.mu.RLock()
		for _, candidate := range m.auths {
			if candidate == nil || !authMatchesProvider(candidate, provider) || candidate.Disabled {
				continue
			}
			if !eligibility.allows(candidate) {
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
	executor, okExecutor := m.executorForRoutingKey(provider)
	if !okExecutor {
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}
	selected, errPick := m.scheduler.pickSingle(ctx, provider, model, opts, tried)
	if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
		m.syncScheduler()
		selected, errPick = m.scheduler.pickSingle(ctx, provider, model, opts, tried)
	}
	if errPick != nil {
		return nil, nil, errPick
	}
	if selected == nil {
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
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

func (m *Manager) pickNextMixedLegacy(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m.HomeEnabled() {
		return m.pickNextViaHome(ctx, model, opts, tried)
	}

	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)

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
	selector := m.selector
	pluginScheduler := m.pluginScheduler
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
		if !eligibility.allows(candidate) {
			continue
		}
		if executorKeyFromAuth(candidate) == "" {
			continue
		}
		if !authMatchesAnyProvider(candidate, providerSet) {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		if _, ok := m.executors[executorKeyFromAuth(candidate)]; !ok {
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
	available, selectorAuths, errAvailable := m.availableAuthsForSelector(selector, candidates, "mixed", model, time.Now())
	if errAvailable != nil {
		m.mu.RUnlock()
		return nil, nil, "", errAvailable
	}
	m.mu.RUnlock()

	selected, handled, errPick := m.pickViaPluginScheduler(ctx, pluginScheduler, "mixed", providers, model, opts, tried, available)
	if errPick != nil {
		return nil, nil, "", errPick
	}
	if !handled {
		selectorCtx := withWeightedSelectorStateModel(ctx, selector, model)
		selected, errPick = selector.Pick(selectorCtx, "mixed", selectionArgForSelector(selector, model), opts, selectorAuths)
		if errPick != nil {
			if isBuiltInSelector(selector) {
				errPick = restoreModelCooldownErrorModel(errPick, model)
			}
			return nil, nil, "", errPick
		}
	}
	if selected == nil {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}
	providerKey := executorKeyFromAuth(selected)
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

// pickNextMixedCompoundRoute tries the route's compound keys in order, rotating
// the starting index per model when the per-model strategy is round-robin
// ("failover"), so successive picks spread across the pinned rows. When the
// strategy is fill-first ("priority") the rotation is pinned to index 0 and the
// first route entry keeps serving, matching the non-compound scheduler path.
func (m *Manager) pickNextMixedCompoundRoute(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}, strategy schedulerStrategy) (*Auth, ProviderExecutor, string, error) {
	startIndex := 0
	if strategy == schedulerStrategyRoundRobin {
		startIndex = m.nextCompoundRouteOffset(model, len(providers))
	}
	var lastRetryableErr error
	for offset := 0; offset < len(providers); offset++ {
		provider := providers[(startIndex+offset)%len(providers)]
		providerKey := strings.ToLower(strings.TrimSpace(provider))
		if providerKey == "" {
			continue
		}
		auth, selectedExecutor, errPick := m.pickNextLegacy(ctx, providerKey, model, opts, tried)
		if errPick != nil {
			var authErr *Error
			if shouldRetrySchedulerPick(errPick) || (errors.As(errPick, &authErr) && authErr != nil && authErr.Code == "executor_not_found") {
				lastRetryableErr = errPick
				continue
			}
			return nil, nil, "", errPick
		}
		if auth == nil {
			continue
		}
		if selectedExecutor == nil {
			continue
		}
		return auth, selectedExecutor, executorKeyFromAuth(auth), nil
	}
	if lastRetryableErr != nil {
		return nil, nil, "", lastRetryableErr
	}
	return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
}

// nextCompoundRouteOffset returns the rotating start index for a compound-key
// route under the failover (round-robin) strategy. The offset advances once per
// successful pick so the next request starts at the following route entry.
func (m *Manager) nextCompoundRouteOffset(model string, size int) int {
	if m == nil || size <= 1 {
		return 0
	}
	key := "compound-route:" + canonicalModelKey(model)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.providerOffsets == nil {
		m.providerOffsets = make(map[string]int)
	}
	offset := m.providerOffsets[key]
	if offset >= 2_147_483_640 {
		offset = 0
	}
	m.providerOffsets[key] = offset + 1
	return offset % size
}

func (m *Manager) pickNextMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, ProviderExecutor, string, error) {
	if m.HomeEnabled() {
		return m.pickNextViaHome(ctx, model, opts, tried)
	}

	if m.hasPluginScheduler() || !m.useSchedulerFastPath() {
		return m.pickNextMixedLegacy(ctx, providers, model, opts, tried)
	}
	compoundRoute := false
	for _, provider := range providers {
		if m.isCompoundRoutingKey(provider) {
			compoundRoute = true
			break
		}
	}
	if compoundRoute {
		// The scheduler indexes auths by bare executor channel, so it cannot
		// distinguish two compound rows on the same channel. For an explicit
		// per-model priority/failover route, try the already priority-ordered
		// route keys one at a time; pickNextLegacy retains exact row matching.
		routeStrategy := routeStrategyFromMetadata(opts.Metadata)
		if routeStrategy == schedulerStrategyFillFirst || routeStrategy == schedulerStrategyRoundRobin {
			return m.pickNextMixedCompoundRoute(ctx, providers, model, opts, tried, routeStrategy)
		}
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
		if _, okExecutor := m.executorForRoutingKey(providerKey); !okExecutor {
			continue
		}
		seenProviders[providerKey] = struct{}{}
		eligibleProviders = append(eligibleProviders, providerKey)
	}
	if len(eligibleProviders) == 0 {
		return nil, nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
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
			if !authMatchesAnyProvider(candidate, providerSet) {
				continue
			}
			if !eligibility.allows(candidate) {
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
	// Per-model routing strategy (priority/failover) overrides the global
	// scheduler strategy for this request only. The override is honored solely
	// on the builtin scheduler fast path; plugin-scheduler deployments keep
	// their global strategy (documented). schedulerStrategyCurrent = inherit.
	routeStrategy := routeStrategyFromMetadata(opts.Metadata)
	for {
		selected, providerKey, errPick := m.scheduler.pickMixedWithStrategy(ctx, eligibleProviders, model, opts, tried, routeStrategy)
		if errPick != nil && model != "" && shouldRetrySchedulerPick(errPick) {
			m.syncScheduler()
			selected, providerKey, errPick = m.scheduler.pickMixedWithStrategy(ctx, eligibleProviders, model, opts, tried, routeStrategy)
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
		executor, okExecutor := m.executorForRoutingKey(providerKey)
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
