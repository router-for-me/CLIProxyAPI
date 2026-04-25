package auth

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
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
	mu            sync.RWMutex
	strategy      schedulerStrategy
	providers     map[string]*providerScheduler
	authProviders map[string]string
	mixedCursorMu sync.RWMutex
	mixedCursors  map[mixedCursorKey]*atomic.Uint64
	largeCursors  sync.Map
}

type mixedProviderCandidate struct {
	shard        *modelScheduler
	priority     int
	weight       int
	segmentStart int
	segmentEnd   int
}

type mixedCursorKey struct {
	modelKey      string
	providerCount uint8
	providers     [4]string
}

// providerScheduler stores auth metadata and model shards for a single provider.
type providerScheduler struct {
	mu          sync.RWMutex
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
	mu               sync.RWMutex
	modelKey         string
	entries          map[string]*scheduledAuth
	priorityOrder    []int
	readyByPriority  map[int]*readyBucket
	blocked          cooldownQueue
	nextBlockedRetry time.Time
}

// scheduledAuth stores the runtime scheduling state for a single auth inside a model shard.
type scheduledAuth struct {
	meta        *scheduledAuthMeta
	auth        *Auth
	state       scheduledState
	nextRetryAt time.Time
}

// authFilter carries request-scoped auth constraints without allocating closures.
type authFilter struct {
	pinnedAuthID string
	tried        map[string]struct{}
	hasPinned    bool
	hasTried     bool
}

func newAuthFilter(pinnedAuthID string, tried map[string]struct{}) authFilter {
	return authFilter{
		pinnedAuthID: pinnedAuthID,
		tried:        tried,
		hasPinned:    pinnedAuthID != "",
		hasTried:     len(tried) > 0,
	}
}

func (f authFilter) empty() bool {
	return !f.hasPinned && !f.hasTried
}

func (f authFilter) matchesAuthID(authID string) bool {
	if f.hasPinned && authID != f.pinnedAuthID {
		return false
	}
	if !f.hasTried {
		return true
	}
	_, tried := f.tried[authID]
	return !tried
}

func (f authFilter) matches(entry *scheduledAuth) bool {
	if entry == nil || entry.auth == nil {
		return false
	}
	return f.matchesAuthID(entry.auth.ID)
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

type readyViewCursorState struct {
	cursor       int
	parentCursor int
	childCursors map[string]int
}

type readyBucketCursorState struct {
	all readyViewCursorState
	ws  readyViewCursorState
}

func snapshotReadyViewCursors(view readyView) readyViewCursorState {
	state := readyViewCursorState{
		cursor:       view.cursor,
		parentCursor: view.parentCursor,
	}
	if len(view.children) == 0 {
		return state
	}
	state.childCursors = make(map[string]int, len(view.children))
	for parent, child := range view.children {
		if child == nil {
			continue
		}
		state.childCursors[parent] = child.cursor
	}
	return state
}

func restoreReadyViewCursors(view *readyView, state readyViewCursorState) {
	if view == nil {
		return
	}
	if len(view.flat) > 0 {
		view.cursor = normalizeCursor(state.cursor, len(view.flat))
	}
	if len(view.parentOrder) == 0 || len(view.children) == 0 {
		return
	}
	view.parentCursor = normalizeCursor(state.parentCursor, len(view.parentOrder))
	if len(state.childCursors) == 0 {
		return
	}
	for parent, child := range view.children {
		if child == nil || len(child.items) == 0 {
			continue
		}
		cursor, ok := state.childCursors[parent]
		if !ok {
			continue
		}
		child.cursor = normalizeCursor(cursor, len(child.items))
	}
}

func normalizeCursor(cursor, size int) int {
	if size <= 0 || cursor <= 0 {
		return 0
	}
	cursor = cursor % size
	if cursor < 0 {
		cursor += size
	}
	return cursor
}

// newAuthScheduler constructs an empty scheduler configured for the supplied selector strategy.
func newAuthScheduler(selector Selector) *authScheduler {
	return &authScheduler{
		strategy:      selectorStrategy(selector),
		providers:     make(map[string]*providerScheduler),
		authProviders: make(map[string]string),
		mixedCursors:  make(map[mixedCursorKey]*atomic.Uint64),
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
	s.strategy = selectorStrategy(selector)
	s.mu.Unlock()
	s.clearMixedCursors()
}

// rebuild recreates the complete scheduler state from an auth snapshot.
func (s *authScheduler) rebuild(auths []*Auth) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.providers = make(map[string]*providerScheduler)
	s.authProviders = make(map[string]string)
	now := time.Now()
	for _, auth := range auths {
		s.upsertAuthLocked(auth, now)
	}
	s.mu.Unlock()
	s.clearMixedCursors()
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

	s.mu.RLock()
	defer s.mu.RUnlock()
	providerState := s.providers[providerKey]
	if providerState == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	shard := providerState.ensureModel(modelKey, time.Now())
	if shard == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	filter := newAuthFilter(pinnedAuthID, tried)
	if picked := shard.pickReady(preferWebsocket, s.strategy, filter); picked != nil {
		return picked, nil
	}
	return nil, shard.unavailableError(provider, model, filter)
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
	if len(normalized) == 1 {
		// When a single provider is eligible, reuse pickSingle so provider-specific preferences
		// (for example Codex websocket transport) are applied consistently.
		providerKey := normalized[0]
		picked, errPick := s.pickSingle(ctx, providerKey, model, opts, tried)
		if errPick != nil {
			return nil, "", errPick
		}
		if picked == nil {
			return nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		return picked, providerKey, nil
	}
	pinnedAuthID := pinnedAuthIDFromMetadata(opts.Metadata)
	modelKey := canonicalModelKey(model)

	s.mu.RLock()
	defer s.mu.RUnlock()
	if pinnedAuthID != "" {
		providerKey := s.authProviders[pinnedAuthID]
		if providerKey == "" || !containsProvider(normalized, providerKey) {
			return nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		providerState := s.providers[providerKey]
		if providerState == nil {
			return nil, "", &Error{Code: "auth_not_found", Message: "no auth available"}
		}
		shard := providerState.ensureModel(modelKey, time.Now())
		filter := newAuthFilter(pinnedAuthID, tried)
		if picked := shard.pickReady(false, s.strategy, filter); picked != nil {
			return picked, providerKey, nil
		}
		return nil, "", shard.unavailableError("mixed", model, filter)
	}

	filter := newAuthFilter("", tried)
	var smallCandidates [4]mixedProviderCandidate
	candidates := smallCandidates[:0]
	if len(normalized) <= len(smallCandidates) {
		candidates = smallCandidates[:len(normalized)]
	} else {
		candidates = make([]mixedProviderCandidate, len(normalized))
	}
	bestPriority := 0
	hasCandidate := false
	now := time.Now()
	for providerIndex, providerKey := range normalized {
		providerState := s.providers[providerKey]
		if providerState == nil {
			continue
		}
		shard := providerState.ensureModel(modelKey, now)
		candidates[providerIndex].shard = shard
		if shard == nil {
			continue
		}
		shard.promoteExpired(now)
		priorityReady, readyCount, okPriority := shard.highestReadyPriorityAndCount(false, filter)
		if !okPriority {
			continue
		}
		candidates[providerIndex].priority = priorityReady
		candidates[providerIndex].weight = readyCount
		if !hasCandidate || priorityReady > bestPriority {
			bestPriority = priorityReady
			hasCandidate = true
		}
	}
	if !hasCandidate {
		return nil, "", s.mixedUnavailableError(normalized, model, filter)
	}

	if s.strategy == schedulerStrategyFillFirst {
		for providerIndex, providerKey := range normalized {
			shard := candidates[providerIndex].shard
			if shard == nil {
				continue
			}
			picked := shard.pickReadyAtPriority(false, bestPriority, s.strategy, filter)
			if picked != nil {
				return picked, providerKey, nil
			}
		}
		return nil, "", s.mixedUnavailableError(normalized, model, filter)
	}

	totalWeight := 0
	for providerIndex := range normalized {
		candidates[providerIndex].segmentStart = totalWeight
		if candidates[providerIndex].shard != nil && candidates[providerIndex].priority == bestPriority {
			totalWeight += candidates[providerIndex].weight
		}
		candidates[providerIndex].segmentEnd = totalWeight
	}
	if totalWeight == 0 {
		return nil, "", s.mixedUnavailableError(normalized, model, filter)
	}

	cursor := s.ensureMixedCursor(normalized, modelKey)
	startSlot := int(cursor.Add(1)-1) % totalWeight
	startProviderIndex := -1
	for providerIndex := range normalized {
		if candidates[providerIndex].weight == 0 || candidates[providerIndex].priority != bestPriority {
			continue
		}
		if startSlot < candidates[providerIndex].segmentEnd {
			startProviderIndex = providerIndex
			break
		}
	}
	if startProviderIndex < 0 {
		return nil, "", s.mixedUnavailableError(normalized, model, filter)
	}

	localOffset := startSlot - candidates[startProviderIndex].segmentStart
	selectedShard := candidates[startProviderIndex].shard
	if selectedShard != nil {
		if picked := selectedShard.pickReadyAtPriorityOffset(false, bestPriority, localOffset, filter); picked != nil {
			return picked, normalized[startProviderIndex], nil
		}
	}

	for offset := 0; offset < len(normalized); offset++ {
		providerIndex := (startProviderIndex + offset) % len(normalized)
		if candidates[providerIndex].weight == 0 || candidates[providerIndex].priority != bestPriority {
			continue
		}
		providerKey := normalized[providerIndex]
		shard := candidates[providerIndex].shard
		if shard == nil {
			continue
		}
		picked := shard.pickReadyAtPriorityOffset(false, bestPriority, 0, filter)
		if picked == nil {
			continue
		}
		return picked, providerKey, nil
	}
	return nil, "", s.mixedUnavailableError(normalized, model, filter)
}

func (s *authScheduler) clearMixedCursors() {
	if s == nil {
		return
	}
	s.mixedCursorMu.Lock()
	s.mixedCursors = make(map[mixedCursorKey]*atomic.Uint64)
	s.mixedCursorMu.Unlock()
	s.largeCursors.Clear()
}

func (s *authScheduler) ensureMixedCursor(providers []string, modelKey string) *atomic.Uint64 {
	if s == nil {
		return nil
	}
	cursorKey, okCursorKey := makeMixedCursorKey(providers, modelKey)
	if okCursorKey {
		return s.ensureSmallMixedCursor(cursorKey)
	}
	return s.ensureLargeMixedCursor(strings.Join(providers, ",") + ":" + modelKey)
}

func (s *authScheduler) ensureSmallMixedCursor(cursorKey mixedCursorKey) *atomic.Uint64 {
	s.mixedCursorMu.RLock()
	if counter := s.mixedCursors[cursorKey]; counter != nil {
		s.mixedCursorMu.RUnlock()
		return counter
	}
	s.mixedCursorMu.RUnlock()

	counter := &atomic.Uint64{}
	s.mixedCursorMu.Lock()
	defer s.mixedCursorMu.Unlock()
	if existing := s.mixedCursors[cursorKey]; existing != nil {
		return existing
	}
	if s.mixedCursors == nil {
		s.mixedCursors = make(map[mixedCursorKey]*atomic.Uint64)
	}
	s.mixedCursors[cursorKey] = counter
	return counter
}

func (s *authScheduler) ensureLargeMixedCursor(cursorKey string) *atomic.Uint64 {
	if existing, ok := s.largeCursors.Load(cursorKey); ok {
		if counter, okCounter := existing.(*atomic.Uint64); okCounter && counter != nil {
			return counter
		}
	}
	counter := &atomic.Uint64{}
	actual, _ := s.largeCursors.LoadOrStore(cursorKey, counter)
	if actualCounter, ok := actual.(*atomic.Uint64); ok && actualCounter != nil {
		return actualCounter
	}
	return counter
}

func makeMixedCursorKey(providers []string, modelKey string) (mixedCursorKey, bool) {
	if len(providers) == 0 || len(providers) > len((mixedCursorKey{}).providers) {
		return mixedCursorKey{}, false
	}
	key := mixedCursorKey{
		modelKey:      modelKey,
		providerCount: uint8(len(providers)),
	}
	copy(key.providers[:], providers)
	return key, true
}

// mixedUnavailableError synthesizes the mixed-provider cooldown or unavailable error.
func (s *authScheduler) mixedUnavailableError(providers []string, model string, filter authFilter) error {
	now := time.Now()
	modelKey := canonicalModelKey(model)
	total := 0
	cooldownCount := 0
	earliest := time.Time{}
	for _, providerKey := range providers {
		providerState := s.providers[providerKey]
		if providerState == nil {
			continue
		}
		shard := providerState.ensureModel(modelKey, now)
		if shard == nil {
			continue
		}
		shard.promoteExpired(now)
		localTotal, localCooldownCount, localEarliest := shard.availabilitySummary(filter)
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

// normalizeProviderKeys lowercases, trims, and de-duplicates provider keys while preserving order.
func normalizeProviderKeys(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	normalized := true
	var seen map[string]struct{}
	for idx, provider := range providers {
		providerKey := strings.ToLower(strings.TrimSpace(provider))
		if providerKey == "" || providerKey != provider {
			normalized = false
			break
		}
		if len(providers) > 4 {
			if seen == nil {
				seen = make(map[string]struct{}, len(providers))
			}
			if _, exists := seen[providerKey]; exists {
				normalized = false
				break
			}
			seen[providerKey] = struct{}{}
		} else {
			for prev := 0; prev < idx; prev++ {
				if providers[prev] == providerKey {
					normalized = false
					break
				}
			}
			if !normalized {
				break
			}
		}
	}
	if normalized {
		return providers
	}
	seen = make(map[string]struct{}, len(providers))
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
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.auths == nil {
		p.auths = make(map[string]*scheduledAuthMeta)
	}
	if p.modelShards == nil {
		p.modelShards = make(map[string]*modelScheduler)
	}
	p.auths[meta.auth.ID] = meta
	for modelKey, shard := range p.modelShards {
		if shard == nil {
			continue
		}
		if !meta.supportsModel(modelKey) {
			shard.removeEntry(meta.auth.ID)
			continue
		}
		shard.upsertEntry(meta, now)
	}
}

// removeAuthLocked removes an auth from all model shards owned by the provider scheduler.
func (p *providerScheduler) removeAuthLocked(authID string) {
	if p == nil || authID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.auths, authID)
	for _, shard := range p.modelShards {
		if shard != nil {
			shard.removeEntry(authID)
		}
	}
}

// ensureModel returns the shard for modelKey, building it lazily from provider auths.
func (p *providerScheduler) ensureModel(modelKey string, now time.Time) *modelScheduler {
	if p == nil {
		return nil
	}
	modelKey = canonicalModelKey(modelKey)
	p.mu.RLock()
	shard, ok := p.modelShards[modelKey]
	p.mu.RUnlock()
	if ok && shard != nil {
		return shard
	}

	p.mu.Lock()
	if p.modelShards == nil {
		p.modelShards = make(map[string]*modelScheduler)
	}
	if shard, ok = p.modelShards[modelKey]; ok && shard != nil {
		p.mu.Unlock()
		return shard
	}
	shard = &modelScheduler{
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
	p.mu.Unlock()
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

func (m *modelScheduler) upsertEntry(meta *scheduledAuthMeta, now time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.upsertEntryLocked(meta, now)
	m.mu.Unlock()
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
	m.rebuildIndexesLocked()
}

func (m *modelScheduler) removeEntry(authID string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.removeEntryLocked(authID)
	m.mu.Unlock()
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

func (m *modelScheduler) promoteExpired(now time.Time) {
	if m == nil {
		return
	}
	m.mu.RLock()
	nextBlockedRetry := m.nextBlockedRetry
	m.mu.RUnlock()
	if nextBlockedRetry.IsZero() || nextBlockedRetry.After(now) {
		return
	}
	m.mu.Lock()
	m.promoteExpiredLocked(now)
	m.mu.Unlock()
}

// promoteExpiredLocked reevaluates blocked auths whose retry time has elapsed.
func (m *modelScheduler) promoteExpiredLocked(now time.Time) {
	if m == nil || len(m.blocked) == 0 {
		m.nextBlockedRetry = time.Time{}
		return
	}
	if m.nextBlockedRetry.IsZero() || m.nextBlockedRetry.After(now) {
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

func (m *modelScheduler) pickReady(preferWebsocket bool, strategy schedulerStrategy, filter authFilter) *Auth {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	picked := m.pickReadyLocked(preferWebsocket, strategy, filter)
	m.mu.Unlock()
	return picked
}

// pickReadyLocked selects the next ready auth from the highest available priority bucket.
func (m *modelScheduler) pickReadyLocked(preferWebsocket bool, strategy schedulerStrategy, filter authFilter) *Auth {
	if m == nil {
		return nil
	}
	m.promoteExpiredLocked(time.Now())
	priorityReady, okPriority := m.highestReadyPriorityLocked(preferWebsocket, filter)
	if !okPriority {
		return nil
	}
	return m.pickReadyAtPriorityLocked(preferWebsocket, priorityReady, strategy, filter)
}

func (m *modelScheduler) highestReadyPriority(preferWebsocket bool, filter authFilter) (int, bool) {
	if m == nil {
		return 0, false
	}
	m.mu.RLock()
	priorityReady, okPriority := m.highestReadyPriorityLocked(preferWebsocket, filter)
	m.mu.RUnlock()
	return priorityReady, okPriority
}

func (m *modelScheduler) highestReadyPriorityAndCount(preferWebsocket bool, filter authFilter) (int, int, bool) {
	if m == nil {
		return 0, 0, false
	}
	m.mu.RLock()
	priorityReady, okPriority := m.highestReadyPriorityLocked(preferWebsocket, filter)
	if !okPriority {
		m.mu.RUnlock()
		return 0, 0, false
	}
	readyCount := m.matchingReadyCountAtPriorityLocked(preferWebsocket, priorityReady, filter)
	m.mu.RUnlock()
	return priorityReady, readyCount, true
}

// highestReadyPriorityLocked returns the highest priority bucket that still has a matching ready auth.
// The caller must ensure expired entries are already promoted when needed.
func (m *modelScheduler) highestReadyPriorityLocked(preferWebsocket bool, filter authFilter) (int, bool) {
	if m == nil {
		return 0, false
	}
	if filter.empty() {
		if preferWebsocket {
			for _, priority := range m.priorityOrder {
				bucket := m.readyByPriority[priority]
				if bucket != nil && len(bucket.ws.flat) > 0 {
					return priority, true
				}
			}
		}
		for _, priority := range m.priorityOrder {
			bucket := m.readyByPriority[priority]
			if bucket != nil && len(bucket.all.flat) > 0 {
				return priority, true
			}
		}
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
			if bucket.ws.pickFirst(filter) != nil {
				return priority, true
			}
		}
	}
	for _, priority := range m.priorityOrder {
		bucket := m.readyByPriority[priority]
		if bucket == nil {
			continue
		}
		if bucket.all.pickFirst(filter) != nil {
			return priority, true
		}
	}
	return 0, false
}

func (m *modelScheduler) pickReadyAtPriority(preferWebsocket bool, priority int, strategy schedulerStrategy, filter authFilter) *Auth {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	picked := m.pickReadyAtPriorityLocked(preferWebsocket, priority, strategy, filter)
	m.mu.Unlock()
	return picked
}

// pickReadyAtPriorityLocked selects the next ready auth from a specific priority bucket.
// The caller must ensure expired entries are already promoted when needed.
func (m *modelScheduler) pickReadyAtPriorityLocked(preferWebsocket bool, priority int, strategy schedulerStrategy, filter authFilter) *Auth {
	if m == nil {
		return nil
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		return nil
	}
	view := &bucket.all
	if preferWebsocket {
		if filter.empty() {
			if len(bucket.ws.flat) > 0 {
				view = &bucket.ws
			}
		} else if bucket.ws.pickFirst(filter) != nil {
			view = &bucket.ws
		}
	}
	var picked *scheduledAuth
	if filter.empty() {
		if strategy == schedulerStrategyFillFirst {
			picked = view.pickFirstNoFilter()
		} else {
			picked = view.pickRoundRobinNoFilter()
		}
	} else if strategy == schedulerStrategyFillFirst {
		picked = view.pickFirst(filter)
	} else {
		picked = view.pickRoundRobin(filter)
	}
	if picked == nil || picked.auth == nil {
		return nil
	}
	return picked.auth
}

func (m *modelScheduler) readyCountAtPriority(preferWebsocket bool, priority int) int {
	if m == nil {
		return 0
	}
	m.mu.RLock()
	readyCount := m.readyCountAtPriorityLocked(preferWebsocket, priority)
	m.mu.RUnlock()
	return readyCount
}

func (m *modelScheduler) readyCountAtPriorityLocked(preferWebsocket bool, priority int) int {
	if m == nil {
		return 0
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		return 0
	}
	if preferWebsocket && len(bucket.ws.flat) > 0 {
		return len(bucket.ws.flat)
	}
	return len(bucket.all.flat)
}

func (m *modelScheduler) matchingReadyCountAtPriorityLocked(preferWebsocket bool, priority int, filter authFilter) int {
	if m == nil {
		return 0
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		return 0
	}
	view := &bucket.all
	if preferWebsocket && len(bucket.ws.flat) > 0 {
		view = &bucket.ws
	}
	if filter.empty() {
		return len(view.flat)
	}
	count := 0
	for _, entry := range view.flat {
		if filter.matchesAuthID(entry.auth.ID) {
			count++
		}
	}
	return count
}

func (m *modelScheduler) unavailableError(provider, model string, filter authFilter) error {
	if m == nil {
		return &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	m.mu.RLock()
	errUnavailable := m.unavailableErrorLocked(provider, model, filter)
	m.mu.RUnlock()
	return errUnavailable
}

func (m *modelScheduler) pickReadyAtPriorityOffset(preferWebsocket bool, priority, offset int, filter authFilter) *Auth {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	picked := m.pickReadyAtPriorityOffsetLocked(preferWebsocket, priority, offset, filter)
	m.mu.RUnlock()
	return picked
}

func (m *modelScheduler) pickReadyAtPriorityOffsetLocked(preferWebsocket bool, priority, offset int, filter authFilter) *Auth {
	if m == nil {
		return nil
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		return nil
	}
	view := &bucket.all
	if preferWebsocket {
		if filter.empty() {
			if len(bucket.ws.flat) > 0 {
				view = &bucket.ws
			}
		} else if bucket.ws.pickFirst(filter) != nil {
			view = &bucket.ws
		}
	}
	var picked *scheduledAuth
	if filter.empty() {
		picked = view.pickRoundRobinAtNoFilter(offset)
	} else {
		picked = view.pickRoundRobinAt(offset, filter)
	}
	if picked == nil || picked.auth == nil {
		return nil
	}
	return picked.auth
}

// unavailableErrorLocked returns the correct unavailable or cooldown error for the shard.
func (m *modelScheduler) unavailableErrorLocked(provider, model string, filter authFilter) error {
	now := time.Now()
	total, cooldownCount, earliest := m.availabilitySummaryLocked(filter)
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

func (m *modelScheduler) availabilitySummary(filter authFilter) (int, int, time.Time) {
	if m == nil {
		return 0, 0, time.Time{}
	}
	m.mu.RLock()
	total, cooldownCount, earliest := m.availabilitySummaryLocked(filter)
	m.mu.RUnlock()
	return total, cooldownCount, earliest
}

// availabilitySummaryLocked summarizes total candidates, cooldown count, and earliest retry time.
func (m *modelScheduler) availabilitySummaryLocked(filter authFilter) (int, int, time.Time) {
	if m == nil {
		return 0, 0, time.Time{}
	}
	total := 0
	cooldownCount := 0
	earliest := time.Time{}
	for _, entry := range m.entries {
		if !filter.matches(entry) {
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
	m.nextBlockedRetry = time.Time{}
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
	for _, entry := range m.blocked {
		if entry == nil || entry.nextRetryAt.IsZero() {
			continue
		}
		m.nextBlockedRetry = entry.nextRetryAt
		break
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

// pickFirst returns the first ready entry that satisfies predicate without advancing cursors.
func (v *readyView) pickFirst(filter authFilter) *scheduledAuth {
	for _, entry := range v.flat {
		if filter.matches(entry) {
			return entry
		}
	}
	return nil
}

func (v *readyView) pickFirstNoFilter() *scheduledAuth {
	if len(v.flat) == 0 {
		return nil
	}
	return v.flat[0]
}

// pickRoundRobin returns the next ready entry using flat or grouped round-robin traversal.
func (v *readyView) pickRoundRobin(filter authFilter) *scheduledAuth {
	if len(v.parentOrder) > 1 && len(v.children) > 0 {
		return v.pickGroupedRoundRobin(filter)
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
		if !filter.matches(entry) {
			continue
		}
		v.cursor = index + 1
		return entry
	}
	return nil
}

func (v *readyView) pickRoundRobinNoFilter() *scheduledAuth {
	if len(v.parentOrder) > 1 && len(v.children) > 0 {
		return v.pickGroupedRoundRobinNoFilter()
	}
	if len(v.flat) == 0 {
		return nil
	}
	index := v.cursor % len(v.flat)
	entry := v.flat[index]
	v.cursor = index + 1
	return entry
}

func (v *readyView) pickRoundRobinAt(offset int, filter authFilter) *scheduledAuth {
	if len(v.parentOrder) > 1 && len(v.children) > 0 {
		return v.pickGroupedRoundRobinAt(offset, filter)
	}
	if len(v.flat) == 0 {
		return nil
	}
	start := 0
	if len(v.flat) > 0 {
		start = offset % len(v.flat)
		if start < 0 {
			start += len(v.flat)
		}
	}
	for step := 0; step < len(v.flat); step++ {
		index := (start + step) % len(v.flat)
		entry := v.flat[index]
		if filter.matches(entry) {
			return entry
		}
	}
	return nil
}

func (v *readyView) pickRoundRobinAtNoFilter(offset int) *scheduledAuth {
	if len(v.parentOrder) > 1 && len(v.children) > 0 {
		return v.pickGroupedRoundRobinAtNoFilter(offset)
	}
	if len(v.flat) == 0 {
		return nil
	}
	index := offset % len(v.flat)
	if index < 0 {
		index += len(v.flat)
	}
	return v.flat[index]
}

// pickGroupedRoundRobin rotates across parents first and then within the selected parent.
func (v *readyView) pickGroupedRoundRobin(filter authFilter) *scheduledAuth {
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
			if !filter.matches(entry) {
				continue
			}
			child.cursor = itemIndex + 1
			v.parentCursor = parentIndex + 1
			return entry
		}
	}
	return nil
}

func (v *readyView) pickGroupedRoundRobinNoFilter() *scheduledAuth {
	if len(v.parentOrder) == 0 || len(v.children) == 0 {
		return nil
	}
	start := v.parentCursor % len(v.parentOrder)
	for offset := 0; offset < len(v.parentOrder); offset++ {
		parentIndex := (start + offset) % len(v.parentOrder)
		parent := v.parentOrder[parentIndex]
		child := v.children[parent]
		if child == nil || len(child.items) == 0 {
			continue
		}
		itemIndex := child.cursor % len(child.items)
		entry := child.items[itemIndex]
		child.cursor = itemIndex + 1
		v.parentCursor = parentIndex + 1
		return entry
	}
	return nil
}

func (v *readyView) pickGroupedRoundRobinAt(offset int, filter authFilter) *scheduledAuth {
	if len(v.parentOrder) == 0 || len(v.children) == 0 {
		return nil
	}
	start := offset % len(v.parentOrder)
	if start < 0 {
		start += len(v.parentOrder)
	}
	for parentStep := 0; parentStep < len(v.parentOrder); parentStep++ {
		parentIndex := (start + parentStep) % len(v.parentOrder)
		parent := v.parentOrder[parentIndex]
		child := v.children[parent]
		if child == nil || len(child.items) == 0 {
			continue
		}
		itemStart := offset % len(child.items)
		if itemStart < 0 {
			itemStart += len(child.items)
		}
		for itemStep := 0; itemStep < len(child.items); itemStep++ {
			itemIndex := (itemStart + itemStep) % len(child.items)
			entry := child.items[itemIndex]
			if filter.matches(entry) {
				return entry
			}
		}
	}
	return nil
}

func (v *readyView) pickGroupedRoundRobinAtNoFilter(offset int) *scheduledAuth {
	if len(v.parentOrder) == 0 || len(v.children) == 0 {
		return nil
	}
	start := offset % len(v.parentOrder)
	if start < 0 {
		start += len(v.parentOrder)
	}
	for parentStep := 0; parentStep < len(v.parentOrder); parentStep++ {
		parentIndex := (start + parentStep) % len(v.parentOrder)
		parent := v.parentOrder[parentIndex]
		child := v.children[parent]
		if child == nil || len(child.items) == 0 {
			continue
		}
		itemIndex := offset % len(child.items)
		if itemIndex < 0 {
			itemIndex += len(child.items)
		}
		return child.items[itemIndex]
	}
	return nil
}
