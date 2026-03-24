package auth

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// schedulerStrategy identifies which built-in routing semantics the scheduler should apply.
type schedulerStrategy int

const (
	schedulerStrategyCustom schedulerStrategy = iota
	schedulerStrategyRoundRobin
	schedulerStrategyFillFirst
)

// scheduledState describes how an auth currently participates in a model shard.
type scheduledState int

const (
	scheduledStateReady scheduledState = iota
	scheduledStateCooldown
	scheduledStateBlocked
	scheduledStateDisabled
)

// authScheduler keeps the incremental provider/model scheduling state used by Manager.
type authScheduler struct {
	mu            sync.Mutex
	strategy      schedulerStrategy
	providers     map[string]*providerScheduler
	authProviders map[string]string
	mixedCursors  map[string]int
}

// providerScheduler stores auth metadata and model shards for a single provider.
type providerScheduler struct {
	providerKey string
	auths       map[string]*scheduledAuthMeta
	modelShards map[string]*modelScheduler
}

// scheduledAuthMeta stores the immutable scheduling fields derived from an auth snapshot.
type scheduledAuthMeta struct {
	auth              *Auth
	providerKey       string
	priority          int
	virtualParent     string
	websocketEnabled  bool
	supportedModelSet map[string]struct{}
}

// modelScheduler tracks ready and blocked auths for one provider/model combination.
type modelScheduler struct {
	modelKey        string
	entries         map[string]*scheduledAuth
	priorityOrder   []int
	readyByPriority map[int]*readyBucket
	blocked         cooldownQueue
}

// scheduledAuth stores the runtime scheduling state for a single auth inside a model shard.
type scheduledAuth struct {
	meta        *scheduledAuthMeta
	auth        *Auth
	state       scheduledState
	nextRetryAt time.Time
}

// readyBucket keeps the ready views for one priority level.
type readyBucket struct {
	all readyView
	ws  readyView
}

// readyView holds the selection order for flat or grouped round-robin traversal.
type readyView struct {
	flat         []*scheduledAuth
	cursor       int
	parentOrder  []string
	parentCursor int
	children     map[string]*childBucket
}

// childBucket keeps the per-parent rotation state for grouped Gemini virtual auths.
type childBucket struct {
	items  []*scheduledAuth
	cursor int
}

// cooldownQueue is the blocked auth collection ordered by next retry time during rebuilds.
type cooldownQueue []*scheduledAuth

// newAuthScheduler constructs an empty scheduler configured for the supplied selector strategy.
func newAuthScheduler(selector Selector) *authScheduler {
	return &authScheduler{
		strategy:      selectorStrategy(selector),
		providers:     make(map[string]*providerScheduler),
		authProviders: make(map[string]string),
		mixedCursors:  make(map[string]int),
	}
}

// selectorStrategy maps a selector implementation to the scheduler semantics it should emulate.
func selectorStrategy(selector Selector) schedulerStrategy {
	switch selector.(type) {
	case *FillFirstSelector:
		return schedulerStrategyFillFirst
	case nil, *RoundRobinSelector:
		return schedulerStrategyRoundRobin
	default:
		return schedulerStrategyCustom
	}
}

// setSelector updates the active built-in strategy and resets mixed-provider cursors.
func (s *authScheduler) setSelector(selector Selector) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.strategy = selectorStrategy(selector)
	clear(s.mixedCursors)
}

// rebuild recreates the complete scheduler state from an auth snapshot.
func (s *authScheduler) rebuild(auths []*Auth) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.providers = make(map[string]*providerScheduler)
	s.authProviders = make(map[string]string)
	s.mixedCursors = make(map[string]int)
	now := time.Now()
	for _, auth := range auths {
		s.upsertAuthLocked(auth, now)
	}
	for _, providerState := range s.providers {
		if providerState == nil {
			continue
		}
		for _, shard := range providerState.modelShards {
			if shard != nil {
				shard.rebuildIndexesLocked()
			}
		}
	}
}

// upsertAuth incrementally synchronizes one auth into the scheduler.
func (s *authScheduler) upsertAuth(auth *Auth) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertAuthLocked(auth, time.Now())
}

// applyModelStateUpdate updates one auth/model runtime state without rebuilding every shard.
// It returns true when the update was applied incrementally, allowing callers to skip the
// slower full-auth upsert path.
func (s *authScheduler) applyModelStateUpdate(authID, provider, model string, state *ModelState) bool {
	if s == nil {
		return false
	}
	authID = strings.TrimSpace(authID)
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	modelKey := canonicalModelKey(model)
	if authID == "" || providerKey == "" || modelKey == "" {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	providerState := s.providers[providerKey]
	if providerState == nil {
		return false
	}
	shard := providerState.modelShards[modelKey]
	if shard == nil {
		return false
	}
	return shard.applyModelStateLocked(authID, modelKey, state, time.Now())
}

// removeAuth deletes one auth from every scheduler shard that references it.
func (s *authScheduler) removeAuth(authID string) {
	if s == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeAuthLocked(authID)
}

// pickSingle returns the next auth for a single provider/model request using scheduler state.
func (s *authScheduler) pickSingle(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, error) {
	if s == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	modelKey := canonicalModelKey(model)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	preferWebsocket := cliproxyexecutor.DownstreamWebsocket(ctx) && providerKey == "codex" && pinnedAuthID == ""

	s.mu.Lock()
	defer s.mu.Unlock()
	providerState := s.providers[providerKey]
	if providerState == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	shard := providerState.ensureModelLocked(modelKey, time.Now())
	if shard == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	predicate := func(entry *scheduledAuth) bool {
		if entry == nil || entry.auth == nil {
			return false
		}
		if pinnedAuthID != "" && entry.auth.ID != pinnedAuthID {
			return false
		}
		if len(tried) > 0 {
			if _, ok := tried[entry.auth.ID]; ok {
				return false
			}
		}
		return true
	}
	if picked := shard.pickReadyLocked(preferWebsocket, s.strategy, predicate); picked != nil {
		return picked.Clone(), nil
	}
	return nil, shard.unavailableErrorLocked(provider, model, predicate)
}

// pickMixed returns the next auth and provider for a mixed-provider request.
func (s *authScheduler) pickMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, string, error) {
	if s == nil {
		return nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	normalized := normalizeProviderKeys(providers)
	if len(normalized) == 0 {
		return nil, "", &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	modelKey := canonicalModelKey(model)
	preferWebsocketForProvider := func(providerKey string) bool {
		return pinnedAuthID == "" && cliproxyexecutor.DownstreamWebsocket(ctx) && providerKey == "codex"
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if pinnedAuthID != "" {
		providerKey := s.authProviders[pinnedAuthID]
		if providerKey == "" || !containsProvider(normalized, providerKey) {
			return nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		providerState := s.providers[providerKey]
		if providerState == nil {
			return nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		shard := providerState.ensureModelLocked(modelKey, time.Now())
		predicate := func(entry *scheduledAuth) bool {
			if entry == nil || entry.auth == nil || entry.auth.ID != pinnedAuthID {
				return false
			}
			if len(tried) == 0 {
				return true
			}
			_, ok := tried[pinnedAuthID]
			return !ok
		}
		if picked := shard.pickReadyLocked(false, s.strategy, predicate); picked != nil {
			return picked.Clone(), providerKey, nil
		}
		return nil, "", shard.unavailableErrorLocked("mixed", model, predicate)
	}

	predicate := triedPredicate(tried)
	candidateShards := make([]*modelScheduler, len(normalized))
	bestPriority := 0
	hasCandidate := false
	now := time.Now()
	for providerIndex, providerKey := range normalized {
		providerState := s.providers[providerKey]
		if providerState == nil {
			continue
		}
		shard := providerState.ensureModelLocked(modelKey, now)
		candidateShards[providerIndex] = shard
		if shard == nil {
			continue
		}
		priorityReady, okPriority := shard.highestReadyPriorityLocked(preferWebsocketForProvider(providerKey), predicate)
		if !okPriority {
			continue
		}
		if !hasCandidate || priorityReady > bestPriority {
			bestPriority = priorityReady
			hasCandidate = true
		}
	}
	if !hasCandidate {
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, tried)
	}

	if s.strategy == schedulerStrategyFillFirst {
		for providerIndex, providerKey := range normalized {
			shard := candidateShards[providerIndex]
			if shard == nil {
				continue
			}
			picked := shard.pickReadyAtPriorityLocked(preferWebsocketForProvider(providerKey), bestPriority, s.strategy, predicate)
			if picked != nil {
				return picked.Clone(), providerKey, nil
			}
		}
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, tried)
	}

	cursorKey := strings.Join(normalized, ",") + ":" + modelKey
	start := 0
	if len(normalized) > 0 {
		start = s.mixedCursors[cursorKey] % len(normalized)
	}
	for offset := 0; offset < len(normalized); offset++ {
		providerIndex := (start + offset) % len(normalized)
		providerKey := normalized[providerIndex]
		shard := candidateShards[providerIndex]
		if shard == nil {
			continue
		}
		picked := shard.pickReadyAtPriorityLocked(preferWebsocketForProvider(providerKey), bestPriority, schedulerStrategyRoundRobin, predicate)
		if picked == nil {
			continue
		}
		s.mixedCursors[cursorKey] = providerIndex + 1
		return picked.Clone(), providerKey, nil
	}
	return nil, "", s.mixedUnavailableErrorLocked(normalized, model, tried)
}

// mixedUnavailableErrorLocked synthesizes the mixed-provider cooldown or unavailable error.
func (s *authScheduler) mixedUnavailableErrorLocked(providers []string, model string, tried map[string]struct{}) error {
	now := time.Now()
	total := 0
	cooldownCount := 0
	earliest := time.Time{}
	for _, providerKey := range providers {
		providerState := s.providers[providerKey]
		if providerState == nil {
			continue
		}
		shard := providerState.ensureModelLocked(canonicalModelKey(model), now)
		if shard == nil {
			continue
		}
		localTotal, localCooldownCount, localEarliest := shard.availabilitySummaryLocked(triedPredicate(tried))
		total += localTotal
		cooldownCount += localCooldownCount
		if !localEarliest.IsZero() && (earliest.IsZero() || localEarliest.Before(earliest)) {
			earliest = localEarliest
		}
	}
	if total == 0 {
		return &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if cooldownCount == total && !earliest.IsZero() {
		resetIn := earliest.Sub(now)
		if resetIn < 0 {
			resetIn = 0
		}
		return newModelCooldownError(model, "", resetIn)
	}
	return &Error{Code: "auth_unavailable", Message: "no auth available"}
}

// triedPredicate builds a filter that excludes auths already attempted for the current request.
func triedPredicate(tried map[string]struct{}) func(*scheduledAuth) bool {
	if len(tried) == 0 {
		return func(entry *scheduledAuth) bool { return entry != nil && entry.auth != nil }
	}
	return func(entry *scheduledAuth) bool {
		if entry == nil || entry.auth == nil {
			return false
		}
		_, ok := tried[entry.auth.ID]
		return !ok
	}
}

// normalizeProviderKeys lowercases, trims, and de-duplicates provider keys while preserving order.
func normalizeProviderKeys(providers []string) []string {
	seen := make(map[string]struct{}, len(providers))
	out := make([]string, 0, len(providers))
	for _, provider := range providers {
		providerKey := strings.ToLower(strings.TrimSpace(provider))
		if providerKey == "" {
			continue
		}
		if _, ok := seen[providerKey]; ok {
			continue
		}
		seen[providerKey] = struct{}{}
		out = append(out, providerKey)
	}
	return out
}

// containsProvider reports whether provider is present in the normalized provider list.
func containsProvider(providers []string, provider string) bool {
	for _, candidate := range providers {
		if candidate == provider {
			return true
		}
	}
	return false
}

// upsertAuthLocked updates one auth in-place while the scheduler mutex is held.
func (s *authScheduler) upsertAuthLocked(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
	if authID == "" || providerKey == "" || auth.Disabled {
		s.removeAuthLocked(authID)
		return
	}
	if previousProvider := s.authProviders[authID]; previousProvider != "" && previousProvider != providerKey {
		if previousState := s.providers[previousProvider]; previousState != nil {
			previousState.removeAuthLocked(authID)
		}
	}
	meta := buildScheduledAuthMeta(auth)
	s.authProviders[authID] = providerKey
	s.ensureProviderLocked(providerKey).upsertAuthLocked(meta, now)
}

// removeAuthLocked removes one auth from the scheduler while the scheduler mutex is held.
func (s *authScheduler) removeAuthLocked(authID string) {
	if authID == "" {
		return
	}
	if providerKey := s.authProviders[authID]; providerKey != "" {
		if providerState := s.providers[providerKey]; providerState != nil {
			providerState.removeAuthLocked(authID)
		}
		delete(s.authProviders, authID)
	}
}

// ensureProviderLocked returns the provider scheduler for providerKey, creating it when needed.
func (s *authScheduler) ensureProviderLocked(providerKey string) *providerScheduler {
	if s.providers == nil {
		s.providers = make(map[string]*providerScheduler)
	}
	providerState := s.providers[providerKey]
	if providerState == nil {
		providerState = &providerScheduler{
			providerKey: providerKey,
			auths:       make(map[string]*scheduledAuthMeta),
			modelShards: make(map[string]*modelScheduler),
		}
		s.providers[providerKey] = providerState
	}
	return providerState
}

// buildScheduledAuthMeta extracts the scheduling metadata needed for shard bookkeeping.
func buildScheduledAuthMeta(auth *Auth) *scheduledAuthMeta {
	providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
	virtualParent := ""
	if auth.Attributes != nil {
		virtualParent = strings.TrimSpace(auth.Attributes["gemini_virtual_parent"])
	}
	return &scheduledAuthMeta{
		auth:              auth,
		providerKey:       providerKey,
		priority:          authPriority(auth),
		virtualParent:     virtualParent,
		websocketEnabled:  authWebsocketsEnabled(auth),
		supportedModelSet: supportedModelSetForAuth(auth.ID),
	}
}

// supportedModelSetForAuth snapshots the registry models currently registered for an auth.
func supportedModelSetForAuth(authID string) map[string]struct{} {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return nil
	}
	models := registry.GetGlobalRegistry().GetModelsForClient(authID)
	if len(models) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model == nil {
			continue
		}
		modelKey := canonicalModelKey(model.ID)
		if modelKey == "" {
			continue
		}
		set[modelKey] = struct{}{}
	}
	return set
}

// upsertAuthLocked updates every existing model shard that can reference the auth metadata.
func (p *providerScheduler) upsertAuthLocked(meta *scheduledAuthMeta, now time.Time) {
	if p == nil || meta == nil || meta.auth == nil {
		return
	}
	p.auths[meta.auth.ID] = meta
	for modelKey, shard := range p.modelShards {
		if shard == nil {
			continue
		}
		if !meta.supportsModel(modelKey) {
			shard.removeEntryLocked(meta.auth.ID)
			continue
		}
		shard.upsertEntryLocked(meta, now)
	}
}

// removeAuthLocked removes an auth from all model shards owned by the provider scheduler.
func (p *providerScheduler) removeAuthLocked(authID string) {
	if p == nil || authID == "" {
		return
	}
	delete(p.auths, authID)
	for _, shard := range p.modelShards {
		if shard != nil {
			shard.removeEntryLocked(authID)
		}
	}
}

// ensureModelLocked returns the shard for modelKey, building it lazily from provider auths.
func (p *providerScheduler) ensureModelLocked(modelKey string, now time.Time) *modelScheduler {
	if p == nil {
		return nil
	}
	modelKey = canonicalModelKey(modelKey)
	if shard, ok := p.modelShards[modelKey]; ok && shard != nil {
		shard.promoteExpiredLocked(now)
		return shard
	}
	shard := &modelScheduler{
		modelKey:        modelKey,
		entries:         make(map[string]*scheduledAuth),
		readyByPriority: make(map[int]*readyBucket),
	}
	for _, meta := range p.auths {
		if meta == nil || !meta.supportsModel(modelKey) {
			continue
		}
		shard.upsertEntryLocked(meta, now)
	}
	shard.resetReadyCursorsLocked()
	p.modelShards[modelKey] = shard
	return shard
}

// supportsModel reports whether the auth metadata currently supports modelKey.
func (m *scheduledAuthMeta) supportsModel(modelKey string) bool {
	modelKey = canonicalModelKey(modelKey)
	if modelKey == "" {
		return true
	}
	if len(m.supportedModelSet) == 0 {
		return false
	}
	_, ok := m.supportedModelSet[modelKey]
	return ok
}

// upsertEntryLocked updates or inserts one auth entry and rebuilds indexes when ordering changes.
func (m *modelScheduler) upsertEntryLocked(meta *scheduledAuthMeta, now time.Time) {
	if m == nil || meta == nil || meta.auth == nil {
		return
	}
	entry, ok := m.entries[meta.auth.ID]
	if !ok || entry == nil {
		entry = &scheduledAuth{}
		m.entries[meta.auth.ID] = entry
	}
	previousState := entry.state
	previousNextRetryAt := entry.nextRetryAt
	previousPriority := 0
	previousParent := ""
	previousWebsocketEnabled := false
	if entry.meta != nil {
		previousPriority = entry.meta.priority
		previousParent = entry.meta.virtualParent
		previousWebsocketEnabled = entry.meta.websocketEnabled
	}

	entry.meta = meta
	entry.auth = meta.auth
	entry.nextRetryAt = time.Time{}
	blocked, reason, next := isAuthBlockedForModel(meta.auth, m.modelKey, now)
	switch {
	case !blocked:
		entry.state = scheduledStateReady
	case reason == blockReasonCooldown:
		entry.state = scheduledStateCooldown
		entry.nextRetryAt = next
	case reason == blockReasonDisabled:
		entry.state = scheduledStateDisabled
	default:
		entry.state = scheduledStateBlocked
		entry.nextRetryAt = next
	}

	if ok && previousState == entry.state && previousNextRetryAt.Equal(entry.nextRetryAt) && previousPriority == meta.priority && previousParent == meta.virtualParent && previousWebsocketEnabled == meta.websocketEnabled {
		return
	}
	m.transitionEntryWithPreviousMetaLocked(entry, previousState, previousNextRetryAt, previousPriority, previousParent, previousWebsocketEnabled)
}

func (m *modelScheduler) applyModelStateLocked(authID, modelKey string, state *ModelState, now time.Time) bool {
	if m == nil || authID == "" {
		return false
	}
	entry := m.entries[authID]
	if entry == nil || entry.meta == nil {
		return false
	}
	if entry.auth == nil {
		if entry.meta.auth == nil {
			return false
		}
		entry.auth = entry.meta.auth.Clone()
	}
	if authAggregateStateWouldChange(entry.auth, modelKey, state, now) {
		return false
	}
	applyModelStateSnapshot(entry.auth, modelKey, state)
	if entry.meta.auth != nil {
		applyModelStateSnapshot(entry.meta.auth, modelKey, state)
	}

	previousState := entry.state
	previousNextRetryAt := entry.nextRetryAt
	entry.state, entry.nextRetryAt = scheduledStateFromModelState(state, now)

	if previousState == entry.state && previousNextRetryAt.Equal(entry.nextRetryAt) {
		return true
	}

	m.transitionEntryLocked(entry, previousState, previousNextRetryAt)
	return true
}

type projectedAggregateState struct {
	status         Status
	disabled       bool
	unavailable    bool
	nextRetryAfter time.Time
	quota          QuotaState
}

func authAggregateStateWouldChange(auth *Auth, modelKey string, state *ModelState, now time.Time) bool {
	if auth == nil {
		return false
	}
	projected := projectAggregatedAuthState(auth, modelKey, state, now)
	return projected.status != auth.Status ||
		projected.disabled != auth.Disabled ||
		projected.unavailable != auth.Unavailable ||
		!projected.nextRetryAfter.Equal(auth.NextRetryAfter) ||
		projected.quota != auth.Quota
}

func projectAggregatedAuthState(auth *Auth, modelKey string, override *ModelState, now time.Time) projectedAggregateState {
	projected := projectedAggregateState{}
	if auth == nil {
		return projected
	}
	projected.status = auth.Status
	projected.disabled = auth.Disabled
	projected.unavailable = auth.Unavailable
	projected.nextRetryAfter = auth.NextRetryAfter
	projected.quota = auth.Quota
	if auth.Disabled || auth.Status == StatusDisabled {
		projected.status = StatusDisabled
		projected.unavailable = true
		projected.nextRetryAfter = time.Time{}
		projected.quota = QuotaState{}
		return projected
	}

	var (
		hasState       bool
		hasError       bool
		allUnavailable = true
		earliestRetry  time.Time
		quotaExceeded  bool
		quotaRecover   time.Time
		maxBackoff     int
		maxStrike      int
		overrideSeen   bool
	)

	for currentModelKey, currentState := range auth.ModelStates {
		effectiveState := currentState
		if currentModelKey == modelKey {
			effectiveState = override
			overrideSeen = true
		}
		if !accumulateProjectedAggregateState(
			effectiveState,
			now,
			&hasState,
			&hasError,
			&allUnavailable,
			&earliestRetry,
			&quotaExceeded,
			&quotaRecover,
			&maxBackoff,
			&maxStrike,
		) {
			continue
		}
	}
	if !overrideSeen {
		accumulateProjectedAggregateState(
			override,
			now,
			&hasState,
			&hasError,
			&allUnavailable,
			&earliestRetry,
			&quotaExceeded,
			&quotaRecover,
			&maxBackoff,
			&maxStrike,
		)
	}
	if !hasState {
		return projected
	}

	projected.unavailable = allUnavailable
	if allUnavailable {
		projected.nextRetryAfter = earliestRetry
	} else {
		projected.nextRetryAfter = time.Time{}
	}
	if quotaExceeded {
		projected.quota = QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: quotaRecover,
			BackoffLevel:  maxBackoff,
			StrikeCount:   maxStrike,
		}
	} else {
		projected.quota = QuotaState{}
	}
	if hasError {
		projected.status = StatusError
	} else {
		projected.status = StatusActive
	}
	return projected
}

func accumulateProjectedAggregateState(
	state *ModelState,
	now time.Time,
	hasState *bool,
	hasError *bool,
	allUnavailable *bool,
	earliestRetry *time.Time,
	quotaExceeded *bool,
	quotaRecover *time.Time,
	maxBackoff *int,
	maxStrike *int,
) bool {
	if state == nil {
		return false
	}

	*hasState = true

	stateUnavailable := false
	if state.Status == StatusDisabled {
		stateUnavailable = true
	} else if state.Unavailable {
		if !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now) {
			stateUnavailable = true
			if earliestRetry.IsZero() || state.NextRetryAfter.Before(*earliestRetry) {
				*earliestRetry = state.NextRetryAfter
			}
		}
	}
	if !stateUnavailable {
		*allUnavailable = false
	}

	if state.Quota.Exceeded {
		*quotaExceeded = true
		if quotaRecover.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.Before(*quotaRecover)) {
			*quotaRecover = state.Quota.NextRecoverAt
		}
		if state.Quota.BackoffLevel > *maxBackoff {
			*maxBackoff = state.Quota.BackoffLevel
		}
		if state.Quota.StrikeCount > *maxStrike {
			*maxStrike = state.Quota.StrikeCount
		}
	}

	if state.LastError != nil {
		*hasError = true
		return true
	}
	if state.Status == StatusError && state.Unavailable && (state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now)) {
		*hasError = true
	}
	return true
}

func syncAggregatedAuthStateFromModelStates(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	if auth.Disabled || auth.Status == StatusDisabled {
		auth.Status = StatusDisabled
		auth.Unavailable = true
		auth.NextRetryAfter = time.Time{}
		auth.Quota = QuotaState{}
		return
	}
	if len(auth.ModelStates) == 0 {
		return
	}
	updateAggregatedAvailability(auth, now)
	if hasModelError(auth, now) {
		auth.Status = StatusError
		return
	}
	auth.Status = StatusActive
	auth.StatusMessage = ""
	auth.LastError = nil
}

// removeEntryLocked deletes one auth entry and rebuilds the shard indexes if needed.
func (m *modelScheduler) removeEntryLocked(authID string) {
	if m == nil || authID == "" {
		return
	}
	entry, ok := m.entries[authID]
	if !ok || entry == nil {
		return
	}
	delete(m.entries, authID)
	previousPriority := 0
	previousParent := ""
	previousWebsocketEnabled := false
	if entry.meta != nil {
		previousPriority = entry.meta.priority
		previousParent = entry.meta.virtualParent
		previousWebsocketEnabled = entry.meta.websocketEnabled
	}
	m.removeEntryFromIndexesLocked(entry, entry.state, previousPriority, previousParent, previousWebsocketEnabled)
}

// promoteExpiredLocked reevaluates blocked auths whose retry time has elapsed.
func (m *modelScheduler) promoteExpiredLocked(now time.Time) {
	if m == nil || len(m.blocked) == 0 {
		return
	}
	expiredCount := 0
	for expiredCount < len(m.blocked) {
		entry := m.blocked[expiredCount]
		if entry == nil || entry.auth == nil {
			expiredCount++
			continue
		}
		if entry.nextRetryAt.IsZero() || entry.nextRetryAt.After(now) {
			break
		}
		expiredCount++
	}
	if expiredCount == 0 {
		return
	}

	expired := append([]*scheduledAuth(nil), m.blocked[:expiredCount]...)
	copy(m.blocked, m.blocked[expiredCount:])
	for index := len(m.blocked) - expiredCount; index < len(m.blocked); index++ {
		m.blocked[index] = nil
	}
	m.blocked = m.blocked[:len(m.blocked)-expiredCount]

	for _, entry := range expired {
		m.promoteExpiredBlockedEntryLocked(entry, now)
	}
}

func (m *modelScheduler) promoteExpiredBlockedEntryLocked(entry *scheduledAuth, now time.Time) {
	if m == nil || entry == nil || entry.meta == nil || entry.auth == nil {
		return
	}
	blocked, reason, next := isAuthBlockedForModel(entry.auth, m.modelKey, now)
	switch {
	case !blocked:
		entry.state = scheduledStateReady
		entry.nextRetryAt = time.Time{}
	case reason == blockReasonCooldown:
		entry.state = scheduledStateCooldown
		entry.nextRetryAt = next
	case reason == blockReasonDisabled:
		entry.state = scheduledStateDisabled
		entry.nextRetryAt = time.Time{}
	default:
		entry.state = scheduledStateBlocked
		entry.nextRetryAt = next
	}
	m.addEntryToIndexesLocked(entry)
}

func applyModelStateSnapshot(auth *Auth, modelKey string, state *ModelState) {
	if auth == nil || strings.TrimSpace(modelKey) == "" {
		return
	}
	if state == nil {
		if auth.ModelStates == nil {
			return
		}
		delete(auth.ModelStates, modelKey)
		if len(auth.ModelStates) == 0 {
			auth.ModelStates = nil
		}
		return
	}
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	auth.ModelStates[modelKey] = state.Clone()
}

func scheduledStateFromModelState(state *ModelState, now time.Time) (scheduledState, time.Time) {
	if state == nil {
		return scheduledStateReady, time.Time{}
	}
	if state.Status == StatusDisabled {
		return scheduledStateDisabled, time.Time{}
	}
	if !state.Unavailable {
		return scheduledStateReady, time.Time{}
	}
	if state.NextRetryAfter.IsZero() {
		return scheduledStateReady, time.Time{}
	}
	if !state.NextRetryAfter.After(now) {
		return scheduledStateReady, time.Time{}
	}
	next := state.NextRetryAfter
	if !state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.After(now) {
		next = state.Quota.NextRecoverAt
	}
	if next.Before(now) {
		next = now
	}
	if state.Quota.Exceeded {
		return scheduledStateCooldown, next
	}
	return scheduledStateBlocked, next
}

func (m *modelScheduler) transitionEntryLocked(entry *scheduledAuth, previousState scheduledState, previousNextRetryAt time.Time) {
	previousPriority := 0
	previousParent := ""
	previousWebsocketEnabled := false
	if entry != nil && entry.meta != nil {
		previousPriority = entry.meta.priority
		previousParent = entry.meta.virtualParent
		previousWebsocketEnabled = entry.meta.websocketEnabled
	}
	m.transitionEntryWithPreviousMetaLocked(entry, previousState, previousNextRetryAt, previousPriority, previousParent, previousWebsocketEnabled)
}

func (m *modelScheduler) transitionEntryWithPreviousMetaLocked(entry *scheduledAuth, previousState scheduledState, previousNextRetryAt time.Time, previousPriority int, previousParent string, previousWebsocketEnabled bool) {
	if m == nil || entry == nil || entry.meta == nil || entry.auth == nil {
		return
	}
	m.removeEntryFromIndexesLocked(entry, previousState, previousPriority, previousParent, previousWebsocketEnabled)
	m.addEntryToIndexesLocked(entry)
}

func (m *modelScheduler) addEntryToIndexesLocked(entry *scheduledAuth) {
	if m == nil || entry == nil || entry.meta == nil || entry.auth == nil {
		return
	}
	switch entry.state {
	case scheduledStateReady:
		m.addReadyEntryLocked(entry.meta.priority, entry)
	case scheduledStateCooldown, scheduledStateBlocked:
		m.addBlockedEntryLocked(entry)
	}
}

func (m *modelScheduler) removeEntryFromIndexesLocked(entry *scheduledAuth, state scheduledState, priority int, parent string, websocketEnabled bool) {
	if m == nil || entry == nil || entry.auth == nil {
		return
	}
	switch state {
	case scheduledStateReady:
		m.removeReadyEntryLocked(priority, readyEntryWithMeta(entry, priority, parent, websocketEnabled))
	case scheduledStateCooldown, scheduledStateBlocked:
		m.removeBlockedEntryLocked(entry.auth.ID)
	}
}

func readyEntryWithMeta(entry *scheduledAuth, priority int, parent string, websocketEnabled bool) *scheduledAuth {
	if entry == nil || entry.auth == nil {
		return nil
	}
	return &scheduledAuth{
		auth: entry.auth,
		meta: &scheduledAuthMeta{
			priority:         priority,
			virtualParent:    parent,
			websocketEnabled: websocketEnabled,
		},
	}
}

func (m *modelScheduler) addReadyEntryLocked(priority int, entry *scheduledAuth) {
	if m == nil || entry == nil || entry.auth == nil {
		return
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		bucket = &readyBucket{}
		m.readyByPriority[priority] = bucket
		m.insertPriorityLocked(priority)
	}
	if bucketUsesGroupedView(bucket) || entryUsesGroupedReadyView(entry) {
		entries := insertScheduledAuthSorted(bucket.all.flat, entry)
		*bucket = *buildReadyBucket(entries)
		return
	}
	insertReadyViewEntry(&bucket.all, entry)
	if entry.meta.websocketEnabled {
		insertReadyViewEntry(&bucket.ws, entry)
	}
}

func (m *modelScheduler) removeReadyEntryLocked(priority int, entry *scheduledAuth) {
	if m == nil || entry == nil || entry.auth == nil {
		return
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		return
	}
	if bucketUsesGroupedView(bucket) || entryUsesGroupedReadyView(entry) {
		entries := removeScheduledAuthByID(bucket.all.flat, entry.auth.ID)
		if len(entries) == 0 {
			delete(m.readyByPriority, priority)
			m.removePriorityLocked(priority)
			return
		}
		*bucket = *buildReadyBucket(entries)
		return
	}
	removeReadyViewEntry(&bucket.all, entry.auth.ID)
	if entry.meta.websocketEnabled {
		removeReadyViewEntry(&bucket.ws, entry.auth.ID)
	}
	if len(bucket.all.flat) == 0 {
		delete(m.readyByPriority, priority)
		m.removePriorityLocked(priority)
		return
	}
	if readyEntriesShouldUseGroupedView(bucket.all.flat) {
		*bucket = *buildReadyBucket(bucket.all.flat)
	}
}

func (m *modelScheduler) addBlockedEntryLocked(entry *scheduledAuth) {
	if m == nil || entry == nil || entry.auth == nil {
		return
	}
	insertAt := sort.Search(len(m.blocked), func(i int) bool {
		return blockedEntryLess(entry, m.blocked[i])
	})
	m.blocked = append(m.blocked, nil)
	copy(m.blocked[insertAt+1:], m.blocked[insertAt:])
	m.blocked[insertAt] = entry
}

func (m *modelScheduler) removeBlockedEntryLocked(authID string) {
	if m == nil || authID == "" || len(m.blocked) == 0 {
		return
	}
	for index, entry := range m.blocked {
		if entry == nil || entry.auth == nil || entry.auth.ID != authID {
			continue
		}
		copy(m.blocked[index:], m.blocked[index+1:])
		m.blocked[len(m.blocked)-1] = nil
		m.blocked = m.blocked[:len(m.blocked)-1]
		return
	}
}

func (m *modelScheduler) insertPriorityLocked(priority int) {
	for _, existing := range m.priorityOrder {
		if existing == priority {
			return
		}
	}
	insertAt := sort.Search(len(m.priorityOrder), func(i int) bool {
		return m.priorityOrder[i] <= priority
	})
	m.priorityOrder = append(m.priorityOrder, 0)
	copy(m.priorityOrder[insertAt+1:], m.priorityOrder[insertAt:])
	m.priorityOrder[insertAt] = priority
}

func (m *modelScheduler) removePriorityLocked(priority int) {
	for index, existing := range m.priorityOrder {
		if existing != priority {
			continue
		}
		copy(m.priorityOrder[index:], m.priorityOrder[index+1:])
		m.priorityOrder = m.priorityOrder[:len(m.priorityOrder)-1]
		return
	}
}

func bucketUsesGroupedView(bucket *readyBucket) bool {
	if bucket == nil {
		return false
	}
	return len(bucket.all.parentOrder) > 0 || len(bucket.ws.parentOrder) > 0
}

func entryUsesGroupedReadyView(entry *scheduledAuth) bool {
	return entry != nil && entry.meta != nil && entry.meta.virtualParent != ""
}

func insertReadyViewEntry(view *readyView, entry *scheduledAuth) {
	if view == nil || entry == nil || entry.auth == nil {
		return
	}
	insertAt := sort.Search(len(view.flat), func(i int) bool {
		return view.flat[i].auth.ID >= entry.auth.ID
	})
	view.flat = append(view.flat, nil)
	copy(view.flat[insertAt+1:], view.flat[insertAt:])
	view.flat[insertAt] = entry
	if len(view.flat) == 1 {
		view.cursor = 0
		return
	}
	if insertAt <= view.cursor {
		view.cursor++
	}
	view.cursor %= len(view.flat)
}

func removeReadyViewEntry(view *readyView, authID string) {
	if view == nil || authID == "" || len(view.flat) == 0 {
		return
	}
	index := sort.Search(len(view.flat), func(i int) bool {
		return view.flat[i].auth.ID >= authID
	})
	if index >= len(view.flat) || view.flat[index] == nil || view.flat[index].auth == nil || view.flat[index].auth.ID != authID {
		return
	}
	copy(view.flat[index:], view.flat[index+1:])
	view.flat[len(view.flat)-1] = nil
	view.flat = view.flat[:len(view.flat)-1]
	if len(view.flat) == 0 {
		view.cursor = 0
		return
	}
	if index < view.cursor && view.cursor > 0 {
		view.cursor--
	}
	view.cursor %= len(view.flat)
}

func insertScheduledAuthSorted(entries []*scheduledAuth, entry *scheduledAuth) []*scheduledAuth {
	insertAt := sort.Search(len(entries), func(i int) bool {
		return entries[i].auth.ID >= entry.auth.ID
	})
	entries = append(entries, nil)
	copy(entries[insertAt+1:], entries[insertAt:])
	entries[insertAt] = entry
	return entries
}

func removeScheduledAuthByID(entries []*scheduledAuth, authID string) []*scheduledAuth {
	index := sort.Search(len(entries), func(i int) bool {
		return entries[i].auth.ID >= authID
	})
	if index >= len(entries) || entries[index] == nil || entries[index].auth == nil || entries[index].auth.ID != authID {
		return entries
	}
	copy(entries[index:], entries[index+1:])
	entries[len(entries)-1] = nil
	return entries[:len(entries)-1]
}

func blockedEntryLess(left, right *scheduledAuth) bool {
	if left == nil || left.auth == nil {
		return false
	}
	if right == nil || right.auth == nil {
		return true
	}
	if left.nextRetryAt.Equal(right.nextRetryAt) {
		return left.auth.ID < right.auth.ID
	}
	if left.nextRetryAt.IsZero() {
		return false
	}
	if right.nextRetryAt.IsZero() {
		return true
	}
	return left.nextRetryAt.Before(right.nextRetryAt)
}

// pickReadyLocked selects the next ready auth from the highest available priority bucket.
func (m *modelScheduler) pickReadyLocked(preferWebsocket bool, strategy schedulerStrategy, predicate func(*scheduledAuth) bool) *Auth {
	if m == nil {
		return nil
	}
	m.promoteExpiredLocked(time.Now())
	priorityReady, okPriority := m.highestReadyPriorityLocked(preferWebsocket, predicate)
	if !okPriority {
		return nil
	}
	return m.pickReadyAtPriorityLocked(preferWebsocket, priorityReady, strategy, predicate)
}

// highestReadyPriorityLocked returns the highest priority bucket that still has a matching ready auth.
// The caller must ensure expired entries are already promoted when needed.
func (m *modelScheduler) highestReadyPriorityLocked(preferWebsocket bool, predicate func(*scheduledAuth) bool) (int, bool) {
	if m == nil {
		return 0, false
	}
	for _, priority := range m.priorityOrder {
		bucket := m.readyByPriority[priority]
		if bucket == nil {
			continue
		}
		if bucketHasMatchingReadyEntry(bucket, preferWebsocket, predicate) {
			return priority, true
		}
	}
	return 0, false
}

// pickReadyAtPriorityLocked selects the next ready auth from a specific priority bucket.
// The caller must ensure expired entries are already promoted when needed.
func (m *modelScheduler) pickReadyAtPriorityLocked(preferWebsocket bool, priority int, strategy schedulerStrategy, predicate func(*scheduledAuth) bool) *Auth {
	if m == nil {
		return nil
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		return nil
	}
	picked := pickReadyFromBucket(bucket, preferWebsocket, strategy, predicate)
	if picked == nil || picked.auth == nil {
		return nil
	}
	return picked.auth
}

func bucketHasMatchingReadyEntry(bucket *readyBucket, preferWebsocket bool, predicate func(*scheduledAuth) bool) bool {
	if bucket == nil {
		return false
	}
	if preferWebsocket && len(bucket.ws.flat) > 0 && bucket.ws.pickFirst(predicate) != nil {
		return true
	}
	return bucket.all.pickFirst(predicate) != nil
}

func pickReadyFromBucket(bucket *readyBucket, preferWebsocket bool, strategy schedulerStrategy, predicate func(*scheduledAuth) bool) *scheduledAuth {
	if bucket == nil {
		return nil
	}
	if preferWebsocket && len(bucket.ws.flat) > 0 {
		if picked := pickReadyFromView(&bucket.ws, strategy, predicate); picked != nil {
			return picked
		}
	}
	return pickReadyFromView(&bucket.all, strategy, predicate)
}

func pickReadyFromView(view *readyView, strategy schedulerStrategy, predicate func(*scheduledAuth) bool) *scheduledAuth {
	if view == nil {
		return nil
	}
	if strategy == schedulerStrategyFillFirst {
		return view.pickFirst(predicate)
	}
	return view.pickRoundRobin(predicate)
}

// unavailableErrorLocked returns the correct unavailable or cooldown error for the shard.
func (m *modelScheduler) unavailableErrorLocked(provider, model string, predicate func(*scheduledAuth) bool) error {
	now := time.Now()
	total, cooldownCount, earliest := m.availabilitySummaryLocked(predicate)
	if total == 0 {
		return &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	if cooldownCount == total && !earliest.IsZero() {
		providerForError := provider
		if providerForError == "mixed" {
			providerForError = ""
		}
		resetIn := earliest.Sub(now)
		if resetIn < 0 {
			resetIn = 0
		}
		return newModelCooldownError(model, providerForError, resetIn)
	}
	return &Error{Code: "auth_unavailable", Message: "no auth available"}
}

// availabilitySummaryLocked summarizes total candidates, cooldown count, and earliest retry time.
func (m *modelScheduler) availabilitySummaryLocked(predicate func(*scheduledAuth) bool) (int, int, time.Time) {
	if m == nil {
		return 0, 0, time.Time{}
	}
	total := 0
	cooldownCount := 0
	earliest := time.Time{}
	for _, entry := range m.entries {
		if predicate != nil && !predicate(entry) {
			continue
		}
		total++
		if entry == nil || entry.auth == nil {
			continue
		}
		if entry.state != scheduledStateCooldown {
			continue
		}
		cooldownCount++
		if !entry.nextRetryAt.IsZero() && (earliest.IsZero() || entry.nextRetryAt.Before(earliest)) {
			earliest = entry.nextRetryAt
		}
	}
	return total, cooldownCount, earliest
}

// rebuildIndexesLocked reconstructs ready and blocked views from the current entry map.
func (m *modelScheduler) rebuildIndexesLocked() {
	m.readyByPriority = make(map[int]*readyBucket)
	m.priorityOrder = m.priorityOrder[:0]
	m.blocked = m.blocked[:0]
	priorityBuckets := make(map[int][]*scheduledAuth)
	for _, entry := range m.entries {
		if entry == nil || entry.auth == nil {
			continue
		}
		switch entry.state {
		case scheduledStateReady:
			priority := entry.meta.priority
			priorityBuckets[priority] = append(priorityBuckets[priority], entry)
		case scheduledStateCooldown, scheduledStateBlocked:
			m.blocked = append(m.blocked, entry)
		}
	}
	for priority, entries := range priorityBuckets {
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].auth.ID < entries[j].auth.ID
		})
		m.readyByPriority[priority] = buildReadyBucket(entries)
		m.priorityOrder = append(m.priorityOrder, priority)
	}
	sort.Slice(m.priorityOrder, func(i, j int) bool {
		return m.priorityOrder[i] > m.priorityOrder[j]
	})
	sort.Slice(m.blocked, func(i, j int) bool {
		left := m.blocked[i]
		right := m.blocked[j]
		if left == nil || right == nil {
			return left != nil
		}
		if left.nextRetryAt.Equal(right.nextRetryAt) {
			return left.auth.ID < right.auth.ID
		}
		if left.nextRetryAt.IsZero() {
			return false
		}
		if right.nextRetryAt.IsZero() {
			return true
		}
		return left.nextRetryAt.Before(right.nextRetryAt)
	})
}

func (m *modelScheduler) resetReadyCursorsLocked() {
	if m == nil {
		return
	}
	for _, bucket := range m.readyByPriority {
		resetReadyViewCursors(&bucket.all)
		resetReadyViewCursors(&bucket.ws)
	}
}

func resetReadyViewCursors(view *readyView) {
	if view == nil {
		return
	}
	view.cursor = 0
	view.parentCursor = 0
	for _, child := range view.children {
		if child != nil {
			child.cursor = 0
		}
	}
}

// buildReadyBucket prepares the general and websocket-only ready views for one priority bucket.
func buildReadyBucket(entries []*scheduledAuth) *readyBucket {
	bucket := &readyBucket{}
	bucket.all = buildReadyView(entries)
	wsEntries := make([]*scheduledAuth, 0, len(entries))
	for _, entry := range entries {
		if entry != nil && entry.meta != nil && entry.meta.websocketEnabled {
			wsEntries = append(wsEntries, entry)
		}
	}
	bucket.ws = buildReadyView(wsEntries)
	return bucket
}

// buildReadyView creates either a flat view or a grouped parent/child view for rotation.
func buildReadyView(entries []*scheduledAuth) readyView {
	view := readyView{flat: append([]*scheduledAuth(nil), entries...)}
	if len(entries) == 0 {
		return view
	}
	groups := make(map[string][]*scheduledAuth)
	for _, entry := range entries {
		if entry == nil || entry.meta == nil || entry.meta.virtualParent == "" {
			return view
		}
		groups[entry.meta.virtualParent] = append(groups[entry.meta.virtualParent], entry)
	}
	if len(groups) <= 1 {
		return view
	}
	view.children = make(map[string]*childBucket, len(groups))
	view.parentOrder = make([]string, 0, len(groups))
	for parent := range groups {
		view.parentOrder = append(view.parentOrder, parent)
	}
	sort.Strings(view.parentOrder)
	for _, parent := range view.parentOrder {
		view.children[parent] = &childBucket{items: append([]*scheduledAuth(nil), groups[parent]...)}
	}
	return view
}

func readyEntriesShouldUseGroupedView(entries []*scheduledAuth) bool {
	if len(entries) <= 1 {
		return false
	}
	groups := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.meta == nil || entry.meta.virtualParent == "" {
			return false
		}
		groups[entry.meta.virtualParent] = struct{}{}
		if len(groups) > 1 {
			return true
		}
	}
	return false
}

// pickFirst returns the first ready entry that satisfies predicate without advancing cursors.
func (v *readyView) pickFirst(predicate func(*scheduledAuth) bool) *scheduledAuth {
	for _, entry := range v.flat {
		if predicate == nil || predicate(entry) {
			return entry
		}
	}
	return nil
}

// pickRoundRobin returns the next ready entry using flat or grouped round-robin traversal.
func (v *readyView) pickRoundRobin(predicate func(*scheduledAuth) bool) *scheduledAuth {
	if len(v.parentOrder) > 1 && len(v.children) > 0 {
		return v.pickGroupedRoundRobin(predicate)
	}
	if len(v.flat) == 0 {
		return nil
	}
	start := 0
	if len(v.flat) > 0 {
		start = v.cursor % len(v.flat)
	}
	for offset := 0; offset < len(v.flat); offset++ {
		index := (start + offset) % len(v.flat)
		entry := v.flat[index]
		if predicate != nil && !predicate(entry) {
			continue
		}
		v.cursor = index + 1
		return entry
	}
	return nil
}

// pickGroupedRoundRobin rotates across parents first and then within the selected parent.
func (v *readyView) pickGroupedRoundRobin(predicate func(*scheduledAuth) bool) *scheduledAuth {
	start := 0
	if len(v.parentOrder) > 0 {
		start = v.parentCursor % len(v.parentOrder)
	}
	for offset := 0; offset < len(v.parentOrder); offset++ {
		parentIndex := (start + offset) % len(v.parentOrder)
		parent := v.parentOrder[parentIndex]
		child := v.children[parent]
		if child == nil || len(child.items) == 0 {
			continue
		}
		itemStart := child.cursor % len(child.items)
		for itemOffset := 0; itemOffset < len(child.items); itemOffset++ {
			itemIndex := (itemStart + itemOffset) % len(child.items)
			entry := child.items[itemIndex]
			if predicate != nil && !predicate(entry) {
				continue
			}
			child.cursor = itemIndex + 1
			v.parentCursor = parentIndex + 1
			return entry
		}
	}
	return nil
}
