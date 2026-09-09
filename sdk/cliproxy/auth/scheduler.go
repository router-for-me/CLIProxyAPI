package auth

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// schedulerStrategy identifies which built-in routing semantics the scheduler should apply.
type schedulerStrategy int

const (
	schedulerStrategyCurrent            schedulerStrategy = -1
	schedulerStrategyCustom             schedulerStrategy = 0
	schedulerStrategyRoundRobin         schedulerStrategy = 1
	schedulerStrategyFillFirst          schedulerStrategy = 2
	schedulerStrategyWeightedRoundRobin schedulerStrategy = 3
)

// scheduledState describes how an auth currently participates in a model shard.
type scheduledState int

const (
	scheduledStateReady scheduledState = iota
	scheduledStateCooldown
	scheduledStateBlocked
	scheduledStateDisabled
)

// scheduledGenerationMeta records the latest generation and timestamp processed for an auth ID.
type scheduledGenerationMeta struct {
	epoch      uint64
	generation uint64
	updatedAt  time.Time
}

// authScheduler keeps the incremental provider/model scheduling state used by Manager.
type authScheduler struct {
	mu                  sync.Mutex
	strategy            schedulerStrategy
	providers           map[string]*providerScheduler
	authProviders       map[string]string
	authGenerations     map[string]scheduledGenerationMeta
	mixedCursors        map[string]int
	mixedWeightedStates map[string]*smoothWeightedState
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
	weight            int64
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

// readyView holds the selection order for flat round-robin traversal.
type readyView struct {
	flat          []*scheduledAuth
	lastPicked    string
	weightedState smoothWeightedState
}

// cooldownQueue is the blocked auth collection ordered by next retry time during rebuilds.
type cooldownQueue []*scheduledAuth

type readyViewCursorState struct {
	lastPicked    string
	weightedState smoothWeightedState
}

type readyBucketCursorState struct {
	all readyViewCursorState
	ws  readyViewCursorState
}

func snapshotReadyViewCursors(view readyView) readyViewCursorState {
	state := readyViewCursorState{lastPicked: view.lastPicked}
	if len(view.weightedState.current) > 0 {
		state.weightedState.current = make(map[string]int64, len(view.weightedState.current))
		for authID, current := range view.weightedState.current {
			state.weightedState.current[authID] = current
		}
	}
	if len(view.weightedState.weights) > 0 {
		state.weightedState.weights = make(map[string]int64, len(view.weightedState.weights))
		for authID, weight := range view.weightedState.weights {
			state.weightedState.weights[authID] = weight
		}
	}
	return state
}

func restoreReadyViewCursors(view *readyView, state readyViewCursorState) {
	if view == nil {
		return
	}
	view.lastPicked = state.lastPicked
	weights := scheduledWeightVector(view.flat)
	if len(state.weightedState.current) == 0 || weightsConfigChanged(state.weightedState.weights, weights) {
		return
	}
	current := make(map[string]int64, len(view.flat))
	for _, entry := range view.flat {
		if entry != nil && entry.auth != nil {
			if val, ok := state.weightedState.current[entry.auth.ID]; ok {
				current[entry.auth.ID] = val
			}
		}
	}
	view.weightedState.current = current
	view.weightedState.weights = weights
}

// newAuthScheduler constructs an empty scheduler configured for the supplied selector strategy.
func newAuthScheduler(selector Selector) *authScheduler {
	return &authScheduler{
		strategy:            selectorStrategy(selector),
		providers:           make(map[string]*providerScheduler),
		authProviders:       make(map[string]string),
		authGenerations:     make(map[string]scheduledGenerationMeta),
		mixedCursors:        make(map[string]int),
		mixedWeightedStates: make(map[string]*smoothWeightedState),
	}
}

// selectorStrategy maps a selector implementation to the scheduler semantics it should emulate.
func selectorStrategy(selector Selector) schedulerStrategy {
	switch selector.(type) {
	case *FillFirstSelector:
		return schedulerStrategyFillFirst
	case *WeightedRoundRobinSelector:
		return schedulerStrategyWeightedRoundRobin
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
	clear(s.mixedWeightedStates)
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
	if s.authGenerations == nil {
		s.authGenerations = make(map[string]scheduledGenerationMeta)
	}
	s.mixedCursors = make(map[string]int)
	s.mixedWeightedStates = make(map[string]*smoothWeightedState)
	now := time.Now()
	for _, auth := range auths {
		s.upsertAuthLocked(auth, now)
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

// RecordRemovalTombstone records a removal tombstone with the specified epoch and cleans up provider shards.
func (s *authScheduler) RecordRemovalTombstone(authID string, tombstoneEpoch uint64) {
	if s == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordRemovalTombstoneLocked(authID, tombstoneEpoch)
}

func (s *authScheduler) recordRemovalTombstoneLocked(authID string, tombstoneEpoch uint64) {
	if authID == "" {
		return
	}
	if s.authGenerations == nil {
		s.authGenerations = make(map[string]scheduledGenerationMeta)
	}
	now := time.Now()
	if existing, exists := s.authGenerations[authID]; exists && tombstoneEpoch < existing.epoch {
		return
	}
	s.authGenerations[authID] = scheduledGenerationMeta{
		epoch:      tombstoneEpoch,
		generation: 0,
		updatedAt:  now,
	}
	s.removeAuthFromProvidersLocked(authID)
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

// ResetAuthGeneration clears recorded generation/tombstone metadata for authID.
func (s *authScheduler) ResetAuthGeneration(authID string) {
	if s == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetAuthGenerationLocked(authID)
}

func (s *authScheduler) resetAuthGenerationLocked(authID string) {
	if s.authGenerations != nil {
		delete(s.authGenerations, authID)
	}
}

// pickSingle returns the next auth for a single provider/model request using scheduler state.
func (s *authScheduler) pickSingle(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, error) {
	return s.pickSingleWithStrategy(ctx, provider, model, opts, tried, schedulerStrategyCurrent)
}

func (s *authScheduler) pickSingleWithStrategy(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, tried map[string]struct{}, strategy schedulerStrategy) (*Auth, error) {
	if s == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	modelKey := canonicalModelKey(model)
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	preferWebsocket := cliproxyexecutor.DownstreamWebsocket(ctx) && providerPrefersWebsocketTransport(providerKey) && pinnedAuthID == ""

	s.mu.Lock()
	defer s.mu.Unlock()
	if strategy == schedulerStrategyCurrent {
		strategy = s.strategy
	}
	providerState := s.providers[providerKey]
	if providerState == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	shard := providerState.ensureModelLocked(modelKey, time.Now())
	if shard == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	predicate := scheduledAuthPredicate(eligibility, tried, pinnedAuthID, strategy == schedulerStrategyWeightedRoundRobin)
	if picked := shard.pickReadyLocked(preferWebsocket, strategy, predicate); picked != nil {
		return picked, nil
	}
	return nil, shard.unavailableErrorLocked(provider, model, predicate)
}

func providerPrefersWebsocketTransport(providerKey string) bool {
	switch strings.ToLower(strings.TrimSpace(providerKey)) {
	case "codex", "xai":
		return true
	default:
		return false
	}
}

// pickMixed returns the next auth and provider for a mixed-provider request.
func (s *authScheduler) pickMixed(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, string, error) {
	return s.pickMixedWithStrategy(ctx, providers, model, opts, tried, schedulerStrategyCurrent)
}

func (s *authScheduler) pickMixedWithStrategy(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, tried map[string]struct{}, strategy schedulerStrategy) (*Auth, string, error) {
	if s == nil {
		return nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	normalized := normalizeProviderKeys(providers)
	if len(normalized) == 0 {
		return nil, "", &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	if len(normalized) == 1 {
		// When a single provider is eligible, reuse pickSingle so provider-specific preferences
		// (for example Codex websocket transport) are applied consistently.
		providerKey := normalized[0]
		picked, errPick := s.pickSingleWithStrategy(ctx, providerKey, model, opts, tried, strategy)
		if errPick != nil {
			return nil, "", errPick
		}
		if picked == nil {
			return nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		return picked, providerKey, nil
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	modelKey := canonicalModelKey(model)

	s.mu.Lock()
	defer s.mu.Unlock()
	if strategy == schedulerStrategyCurrent {
		strategy = s.strategy
	}
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
		predicate := scheduledAuthPredicate(eligibility, tried, pinnedAuthID, strategy == schedulerStrategyWeightedRoundRobin)
		if picked := shard.pickReadyLocked(false, strategy, predicate); picked != nil {
			return picked, providerKey, nil
		}
		return nil, "", shard.unavailableErrorLocked("mixed", model, predicate)
	}

	predicate := scheduledAuthPredicate(eligibility, tried, "", strategy == schedulerStrategyWeightedRoundRobin)
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
		priorityReady, okPriority := shard.highestReadyPriorityLocked(false, predicate)
		if !okPriority {
			continue
		}
		if !hasCandidate || priorityReady > bestPriority {
			bestPriority = priorityReady
			hasCandidate = true
		}
	}
	if !hasCandidate {
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
	}

	if strategy == schedulerStrategyFillFirst {
		for providerIndex, providerKey := range normalized {
			shard := candidateShards[providerIndex]
			if shard == nil {
				continue
			}
			picked := shard.pickReadyAtPriorityLocked(false, bestPriority, strategy, predicate)
			if picked != nil {
				return picked, providerKey, nil
			}
		}
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
	}

	cursorKey := strings.Join(normalized, ",") + ":" + modelKey
	if strategy == schedulerStrategyWeightedRoundRobin {
		entries := make([]*scheduledAuth, 0)
		for _, shard := range candidateShards {
			if shard == nil {
				continue
			}
			bucket := shard.readyByPriority[bestPriority]
			if bucket != nil {
				entries = append(entries, bucket.all.flat...)
			}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i] == nil || entries[i].auth == nil {
				return false
			}
			if entries[j] == nil || entries[j].auth == nil {
				return true
			}
			return entries[i].auth.ID < entries[j].auth.ID
		})
		if s.mixedWeightedStates == nil {
			s.mixedWeightedStates = make(map[string]*smoothWeightedState)
		}
		state := s.mixedWeightedStates[cursorKey]
		if state == nil {
			state = &smoothWeightedState{}
			s.mixedWeightedStates[cursorKey] = state
		}
		state.prepare(scheduledWeightVectorMatching(entries, predicate))
		picked := pickSmoothWeightedScheduled(entries, state.current, predicate)
		if picked != nil && picked.meta != nil {
			return picked.auth, picked.meta.providerKey, nil
		}
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
	}

	weights := make([]int, len(normalized))
	segmentStarts := make([]int, len(normalized))
	segmentEnds := make([]int, len(normalized))
	totalWeight := 0
	for providerIndex, shard := range candidateShards {
		segmentStarts[providerIndex] = totalWeight
		if shard != nil {
			weights[providerIndex] = shard.readyCountAtPriorityLocked(false, bestPriority, predicate)
		}
		totalWeight += weights[providerIndex]
		segmentEnds[providerIndex] = totalWeight
	}
	if totalWeight == 0 {
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
	}

	startSlot := s.mixedCursors[cursorKey] % totalWeight
	startProviderIndex := -1
	for providerIndex := range normalized {
		if weights[providerIndex] == 0 {
			continue
		}
		if startSlot < segmentEnds[providerIndex] {
			startProviderIndex = providerIndex
			break
		}
	}
	if startProviderIndex < 0 {
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
	}

	slot := startSlot
	for offset := 0; offset < len(normalized); offset++ {
		providerIndex := (startProviderIndex + offset) % len(normalized)
		if weights[providerIndex] == 0 {
			continue
		}
		if providerIndex != startProviderIndex {
			slot = segmentStarts[providerIndex]
		}
		providerKey := normalized[providerIndex]
		shard := candidateShards[providerIndex]
		if shard == nil {
			continue
		}
		picked := shard.pickReadyAtPriorityLocked(false, bestPriority, schedulerStrategyRoundRobin, predicate)
		if picked == nil {
			continue
		}
		s.mixedCursors[cursorKey] = slot + 1
		return picked, providerKey, nil
	}
	return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
}

// mixedUnavailableErrorLocked synthesizes the mixed-provider cooldown or unavailable error.
func (s *authScheduler) mixedUnavailableErrorLocked(providers []string, model string, predicate func(*scheduledAuth) bool) error {
	now := time.Now()
	total := 0
	cooldownCount := 0
	earliest := time.Time{}

	var latestModelTime time.Time
	var latestModelAuthID string
	var latestModelErr error

	var latestAuthTime time.Time
	var latestAuthID string
	var latestAuthErr error

	for _, providerKey := range providers {
		providerState := s.providers[providerKey]
		if providerState == nil {
			continue
		}
		shard := providerState.ensureModelLocked(canonicalModelKey(model), now)
		if shard == nil {
			continue
		}
		localTotal, localCooldownCount, localEarliest := shard.availabilitySummaryLocked(predicate)
		total += localTotal
		cooldownCount += localCooldownCount
		if !localEarliest.IsZero() && (earliest.IsZero() || localEarliest.Before(earliest)) {
			earliest = localEarliest
		}
		candModelErr, candModelTime, candModelAuthID, candAuthErr, candAuthTime, candAuthAuthID := shard.candidateErrorsLocked(model, predicate)
		if candModelErr != nil {
			if latestModelErr == nil || candModelTime.After(latestModelTime) || (candModelTime.Equal(latestModelTime) && candModelAuthID > latestModelAuthID) {
				latestModelTime = candModelTime
				latestModelAuthID = candModelAuthID
				latestModelErr = candModelErr
			}
		}
		if candAuthErr != nil {
			if latestAuthErr == nil || candAuthTime.After(latestAuthTime) || (candAuthTime.Equal(latestAuthTime) && candAuthAuthID > latestAuthID) {
				latestAuthTime = candAuthTime
				latestAuthID = candAuthAuthID
				latestAuthErr = candAuthErr
			}
		}
	}

	var lastCandidateErr error
	if latestModelErr != nil {
		lastCandidateErr = latestModelErr
	} else {
		lastCandidateErr = latestAuthErr
	}

	if total == 0 {
		return WithCause(&Error{Code: "auth_not_found", Message: "no auth available"}, lastCandidateErr)
	}
	if cooldownCount == total && !earliest.IsZero() {
		resetIn := earliest.Sub(now)
		if resetIn < 0 {
			resetIn = 0
		}
		return newModelCooldownErrorWithCause(model, "", resetIn, lastCandidateErr)
	}
	return WithCause(&Error{Code: "auth_unavailable", Message: "no auth available"}, lastCandidateErr)
}

// scheduledAuthPredicate filters request-ineligible auths before scheduler state advances.
func scheduledAuthPredicate(eligibility authSelectionEligibility, tried map[string]struct{}, pinnedAuthID string, requirePositiveWeight bool) func(*scheduledAuth) bool {
	return func(entry *scheduledAuth) bool {
		if entry == nil || entry.auth == nil || !eligibility.allows(entry.auth) {
			return false
		}
		if requirePositiveWeight && (entry.meta == nil || entry.meta.weight <= 0) {
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

func (s *authScheduler) isStaleScheduledAuth(authID string, incomingEpoch, incomingGen uint64, incomingUpdatedAt time.Time) bool {
	if s.authGenerations == nil {
		s.authGenerations = make(map[string]scheduledGenerationMeta)
	}
	if existing, ok := s.authGenerations[authID]; ok {
		if existing.epoch > incomingEpoch {
			return true
		}
		if existing.epoch == incomingEpoch {
			if existing.generation > incomingGen {
				return true
			}
			if existing.generation == incomingGen && existing.updatedAt.After(incomingUpdatedAt) {
				return true
			}
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
	if authID == "" {
		return
	}

	if s.isStaleScheduledAuth(authID, auth.RegistrationEpoch, auth.Generation, auth.UpdatedAt) {
		return
	}
	s.authGenerations[authID] = scheduledGenerationMeta{
		epoch:      auth.RegistrationEpoch,
		generation: auth.Generation,
		updatedAt:  auth.UpdatedAt,
	}

	providerKey := executorKeyFromAuth(auth)
	if providerKey == "" || auth.Disabled || auth.Status == StatusDisabled {
		s.removeAuthFromProvidersLocked(authID)
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

func (s *authScheduler) removeAuthFromProvidersLocked(authID string) {
	if providerKey := s.authProviders[authID]; providerKey != "" {
		if providerState := s.providers[providerKey]; providerState != nil {
			providerState.removeAuthLocked(authID)
		}
		delete(s.authProviders, authID)
	}
}

// removeAuthLocked removes one auth from the scheduler while the scheduler mutex is held.
func (s *authScheduler) removeAuthLocked(authID string) {
	if authID == "" {
		return
	}
	if s.authGenerations == nil {
		s.authGenerations = make(map[string]scheduledGenerationMeta)
	}
	now := time.Now()
	epoch := uint64(1)
	if existing, ok := s.authGenerations[authID]; ok {
		epoch = existing.epoch + 1
	}
	s.authGenerations[authID] = scheduledGenerationMeta{
		epoch:      epoch,
		generation: 0,
		updatedAt:  now,
	}
	s.removeAuthFromProvidersLocked(authID)
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
	providerKey := executorKeyFromAuth(auth)
	var clonedAuth *Auth
	if auth != nil {
		clonedAuth = auth.Clone()
	}
	return &scheduledAuthMeta{
		auth:              clonedAuth,
		providerKey:       providerKey,
		priority:          authPriority(auth),
		weight:            authWeight(auth),
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
	previousWebsocketEnabled := false
	if entry.meta != nil {
		previousPriority = entry.meta.priority
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

	if ok && previousState == entry.state && previousNextRetryAt.Equal(entry.nextRetryAt) && previousPriority == meta.priority && previousWebsocketEnabled == meta.websocketEnabled {
		return
	}
	m.rebuildIndexesLocked()
}

// removeEntryLocked deletes one auth entry and rebuilds the shard indexes if needed.
func (m *modelScheduler) removeEntryLocked(authID string) {
	if m == nil || authID == "" {
		return
	}
	if _, ok := m.entries[authID]; !ok {
		return
	}
	delete(m.entries, authID)
	m.rebuildIndexesLocked()
}

// promoteExpiredLocked reevaluates blocked auths whose retry time has elapsed.
func (m *modelScheduler) promoteExpiredLocked(now time.Time) {
	if m == nil || len(m.blocked) == 0 {
		return
	}
	changed := false
	for _, entry := range m.blocked {
		if entry == nil || entry.auth == nil {
			continue
		}
		if entry.nextRetryAt.IsZero() || entry.nextRetryAt.After(now) {
			continue
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
		changed = true
	}
	if changed {
		m.rebuildIndexesLocked()
	}
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
	if preferWebsocket {
		// When downstream is websocket and Codex supports websocket transport, prefer websocket-enabled
		// credentials even if they are in a lower priority tier than HTTP-only credentials.
		for _, priority := range m.priorityOrder {
			bucket := m.readyByPriority[priority]
			if bucket == nil {
				continue
			}
			if bucket.ws.pickFirst(predicate) != nil {
				return priority, true
			}
		}
	}
	for _, priority := range m.priorityOrder {
		bucket := m.readyByPriority[priority]
		if bucket == nil {
			continue
		}
		if bucket.all.pickFirst(predicate) != nil {
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
	view := &bucket.all
	if preferWebsocket && bucket.ws.pickFirst(predicate) != nil {
		view = &bucket.ws
	}
	var picked *scheduledAuth
	switch strategy {
	case schedulerStrategyFillFirst:
		picked = view.pickFirst(predicate)
	case schedulerStrategyWeightedRoundRobin:
		picked = view.pickWeighted(predicate)
	default:
		picked = view.pickRoundRobin(predicate)
	}
	if picked == nil || picked.auth == nil {
		return nil
	}
	return picked.auth
}

func (m *modelScheduler) readyCountAtPriorityLocked(preferWebsocket bool, priority int, predicate func(*scheduledAuth) bool) int {
	if m == nil {
		return 0
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		return 0
	}
	view := &bucket.all
	if preferWebsocket && bucket.ws.pickFirst(predicate) != nil {
		view = &bucket.ws
	}
	count := 0
	for _, entry := range view.flat {
		if predicate == nil || predicate(entry) {
			count++
		}
	}
	return count
}

// unavailableErrorLocked returns the correct unavailable or cooldown error for the shard.
func (m *modelScheduler) unavailableErrorLocked(provider, model string, predicate func(*scheduledAuth) bool) error {
	now := time.Now()
	total, cooldownCount, earliest := m.availabilitySummaryLocked(predicate)
	lastCandidateErr, _, _ := m.latestCandidateErrorWithTimeLocked(model, predicate)
	if total == 0 {
		return WithCause(&Error{Code: "auth_not_found", Message: "no auth available"}, lastCandidateErr)
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
		return newModelCooldownErrorWithCause(model, providerForError, resetIn, lastCandidateErr)
	}
	return WithCause(&Error{Code: "auth_unavailable", Message: "no auth available"}, lastCandidateErr)
}

func (m *modelScheduler) latestCandidateErrorWithTimeLocked(model string, predicate func(*scheduledAuth) bool) (error, time.Time, string) {
	modelErr, modelTime, modelAuthID, authErr, authTime, authAuthID := m.candidateErrorsLocked(model, predicate)
	if modelErr != nil {
		return modelErr, modelTime, modelAuthID
	}
	return authErr, authTime, authAuthID
}

func (m *modelScheduler) candidateErrorsLocked(model string, predicate func(*scheduledAuth) bool) (modelErr error, modelTime time.Time, modelAuthID string, authErr error, authTime time.Time, authAuthID string) {
	if m == nil {
		return nil, time.Time{}, "", nil, time.Time{}, ""
	}
	modelKey := canonicalModelKey(model)
	for _, entry := range m.entries {
		if predicate != nil && !predicate(entry) {
			continue
		}
		if entry == nil || entry.auth == nil {
			continue
		}
		auth := entry.auth
		var curModelErr error
		var curModelTime time.Time
		if len(auth.ModelStates) > 0 {
			if state, ok := auth.ModelStates[model]; ok && state != nil {
				if state.LastError != nil {
					curModelErr = state.LastError
					curModelTime = state.UpdatedAt
				} else if strings.TrimSpace(state.StatusMessage) != "" {
					curModelErr = errors.New(state.StatusMessage)
					curModelTime = state.UpdatedAt
				}
			} else if state, ok := auth.ModelStates[modelKey]; ok && state != nil {
				if state.LastError != nil {
					curModelErr = state.LastError
					curModelTime = state.UpdatedAt
				} else if strings.TrimSpace(state.StatusMessage) != "" {
					curModelErr = errors.New(state.StatusMessage)
					curModelTime = state.UpdatedAt
				}
			}
		}

		if curModelErr != nil {
			if curModelTime.IsZero() {
				curModelTime = auth.UpdatedAt
			}
			if modelErr == nil || curModelTime.After(modelTime) || (curModelTime.Equal(modelTime) && auth.ID > modelAuthID) {
				modelTime = curModelTime
				modelAuthID = auth.ID
				modelErr = curModelErr
			}
		} else {
			var curAuthErr error
			var curAuthTime time.Time
			if auth.LastError != nil {
				curAuthErr = auth.LastError
				curAuthTime = auth.UpdatedAt
			} else if strings.TrimSpace(auth.StatusMessage) != "" {
				curAuthErr = errors.New(auth.StatusMessage)
				curAuthTime = auth.UpdatedAt
			}
			if curAuthErr != nil {
				if curAuthTime.IsZero() {
					curAuthTime = auth.UpdatedAt
				}
				if authErr == nil || curAuthTime.After(authTime) || (curAuthTime.Equal(authTime) && auth.ID > authAuthID) {
					authTime = curAuthTime
					authAuthID = auth.ID
					authErr = curAuthErr
				}
			}
		}
	}

	return modelErr, modelTime, modelAuthID, authErr, authTime, authAuthID
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
	cursorStates := make(map[int]readyBucketCursorState, len(m.readyByPriority))
	for priority, bucket := range m.readyByPriority {
		if bucket == nil {
			continue
		}
		cursorStates[priority] = readyBucketCursorState{
			all: snapshotReadyViewCursors(bucket.all),
			ws:  snapshotReadyViewCursors(bucket.ws),
		}
	}

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
		bucket := buildReadyBucket(entries)
		if cursorState, ok := cursorStates[priority]; ok && bucket != nil {
			restoreReadyViewCursors(&bucket.all, cursorState.all)
			restoreReadyViewCursors(&bucket.ws, cursorState.ws)
		}
		m.readyByPriority[priority] = bucket
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

// buildReadyView creates a flat view for rotation.
func buildReadyView(entries []*scheduledAuth) readyView {
	return readyView{flat: append([]*scheduledAuth(nil), entries...)}
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

// pickRoundRobin returns the next ready entry using flat round-robin traversal.
func (v *readyView) pickRoundRobin(predicate func(*scheduledAuth) bool) *scheduledAuth {
	if len(v.flat) == 0 {
		return nil
	}
	start := scheduledSuccessorIndex(v.flat, v.lastPicked)
	for offset := 0; offset < len(v.flat); offset++ {
		index := (start + offset) % len(v.flat)
		entry := v.flat[index]
		if entry == nil || entry.auth == nil {
			continue
		}
		if predicate != nil && !predicate(entry) {
			continue
		}
		v.lastPicked = entry.auth.ID
		return entry
	}
	return nil
}

// scheduledSuccessorIndex returns the index of the first scheduled candidate ordered after
// lastID, wrapping to the start of the ring. Candidates in readyView arrive sorted by auth ID.
func scheduledSuccessorIndex(entries []*scheduledAuth, lastID string) int {
	if lastID == "" {
		return 0
	}
	index := sort.Search(len(entries), func(i int) bool {
		return entries[i].auth.ID > lastID
	})
	if index >= len(entries) {
		return 0
	}
	return index
}

// pickWeighted returns the next ready entry using smooth weighted round-robin.
func (v *readyView) pickWeighted(predicate func(*scheduledAuth) bool) *scheduledAuth {
	if v == nil || len(v.flat) == 0 {
		return nil
	}
	v.weightedState.prepare(scheduledWeightVectorMatching(v.flat, predicate))
	return pickSmoothWeightedScheduled(v.flat, v.weightedState.current, predicate)
}

func scheduledWeightVector(entries []*scheduledAuth) map[string]int64 {
	return scheduledWeightVectorMatching(entries, nil)
}

func scheduledWeightVectorMatching(entries []*scheduledAuth, predicate func(*scheduledAuth) bool) map[string]int64 {
	weights := make(map[string]int64, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.auth == nil || entry.meta == nil || entry.meta.weight <= 0 {
			continue
		}
		if predicate != nil && !predicate(entry) {
			continue
		}
		weights[entry.auth.ID] = entry.meta.weight
	}
	return weights
}

func pickSmoothWeightedScheduled(entries []*scheduledAuth, current map[string]int64, predicate func(*scheduledAuth) bool) *scheduledAuth {
	var picked *scheduledAuth
	var pickedCurrent int64
	var totalWeight int64
	for _, entry := range entries {
		if entry == nil || entry.auth == nil || entry.meta == nil || entry.meta.weight <= 0 {
			continue
		}
		if predicate != nil && !predicate(entry) {
			continue
		}
		current[entry.auth.ID] = saturatingAddInt64(current[entry.auth.ID], entry.meta.weight)
		totalWeight = saturatingAddInt64(totalWeight, entry.meta.weight)
		if picked == nil || current[entry.auth.ID] > pickedCurrent {
			picked = entry
			pickedCurrent = current[entry.auth.ID]
		}
	}
	if picked == nil {
		return nil
	}
	current[picked.auth.ID] = saturatingAddInt64(current[picked.auth.ID], -totalWeight)
	return picked
}
