package auth

import (
	"context"
	"math/rand/v2"
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
	schedulerStrategyP2C                schedulerStrategy = 4
	schedulerStrategyLeastUsed          schedulerStrategy = 5
	// schedulerStrategyWeighted samples one ready auth proportionally to its
	// per-entry weight column (normalized to >=1 at the planner boundary in
	// internal/configsnapshot.normalizeEntryWeight). Distinct from
	// schedulerStrategyWeightedRoundRobin (smooth-WRR state above): this
	// selector uses a single rand.Intn draw over the prefix-sum of weights.
	// See scheduler_weighted.go for the helper and docs/plans/2026-09-17-
	// omniroute-round-2-design.md for the design.
	schedulerStrategyWeighted schedulerStrategy = 6
	// schedulerStrategyHeadroom samples the ready auth whose remaining-quota
	// percent is highest (HeadroomLookup interface). Distinct from the WRR
	// strategies above: this selector falls back to least-used ordering when
	// every candidate is at or below 0% headroom and sets
	// lastHeadroomExhausted so the X-NixLLM-Decision header (Task 6) can
	// carry a marker. See scheduler_headroom.go for the helper and
	// docs/plans/2026-09-17-omniroute-round-2-design.md for the design.
	schedulerStrategyHeadroom schedulerStrategy = 7
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
	mu                  sync.Mutex
	strategy            schedulerStrategy
	providers           map[string]*providerScheduler
	authProviders       map[string]string
	mixedCursors        map[string]int
	mixedWeightedStates map[string]*smoothWeightedState
	// inFlight tracks per-auth in-flight dispatch counts (G4). It lives
	// outside scheduledAuthMeta because metas are recreated on every upsert
	// and rebuild (MarkResult re-upserts per result), which would zero a
	// meta-resident counter mid-flight.
	inFlight map[string]int
	// headroomLookup resolves the remaining-quota percent for an auth. The
	// round-2 headroom selector (schedulerStrategyHeadroom) routes through
	// this lookup. Tests inject a scripted StaticHeadroomLookup; production
	// wires a stub that returns 100.0 until the usage_windows-backed reader
	// lands in the events/quota workstream (Task 7).
	headroomLookup HeadroomLookup
	// lastHeadroomExhausted is set when the most recent headroom pick had to
	// fall back to least-used ordering because every candidate was at or
	// below 0% headroom. The X-NixLLM-Decision header (Task 6) reads this
	// flag to emit the headroom=exhausted,fallback=least-used marker.
	// Cleared at the start of every pick cycle (see pickSingleWithStrategy
	// and pickMixedWithStrategy).
	lastHeadroomExhausted bool
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
	cursor        int
	weightedState smoothWeightedState
}

// cooldownQueue is the blocked auth collection ordered by next retry time during rebuilds.
type cooldownQueue []*scheduledAuth

type readyViewCursorState struct {
	cursor        int
	weightedState smoothWeightedState
}

type readyBucketCursorState struct {
	all readyViewCursorState
	ws  readyViewCursorState
}

func snapshotReadyViewCursors(view readyView) readyViewCursorState {
	state := readyViewCursorState{cursor: view.cursor}
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
	if len(view.flat) > 0 {
		view.cursor = normalizeCursor(state.cursor, len(view.flat))
	}
	weights := scheduledWeightVector(view.flat)
	if len(state.weightedState.current) == 0 || !weightVectorsEqual(state.weightedState.weights, weights) {
		return
	}
	view.weightedState.current = state.weightedState.current
	view.weightedState.weights = weights
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
		strategy:            selectorStrategy(selector),
		providers:           make(map[string]*providerScheduler),
		authProviders:       make(map[string]string),
		mixedCursors:        make(map[string]int),
		mixedWeightedStates: make(map[string]*smoothWeightedState),
		inFlight:            make(map[string]int),
		headroomLookup:      defaultHeadroomLookup(),
	}
}

// defaultHeadroomLookup returns the production headroom resolver. Today this
// is a StaticHeadroomLookup{} which answers 100.0 (unlimited) for every
// auth — effectively a no-op for the round-2 headroom selector until the
// events/quota workstream (Task 7) replaces it with a usage_windows-backed
// reader. Tests inject scripted lookups directly via scheduler.headroomLookup.
func defaultHeadroomLookup() HeadroomLookup {
	return StaticHeadroomLookup{}
}

// adjustInFlight applies a delta to an auth's in-flight counter. No-op for
// unknown auths or empty IDs. The counter feeds the power-of-two-choices and
// least-used pick strategies (G4) and is otherwise informational.
func (s *authScheduler) adjustInFlight(authID string, delta int) {
	if s == nil {
		return
	}
	authID = strings.TrimSpace(authID)
	if authID == "" || delta == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.authKnownLocked(authID) {
		return
	}
	if s.inFlight == nil {
		s.inFlight = make(map[string]int)
	}
	next := s.inFlight[authID] + delta
	if next <= 0 {
		delete(s.inFlight, authID)
		return
	}
	s.inFlight[authID] = next
}

func (s *authScheduler) inFlightSnapshot() map[string]int {
	if s == nil || len(s.inFlight) == 0 {
		return nil
	}
	snapshot := make(map[string]int, len(s.inFlight))
	for authID, count := range s.inFlight {
		snapshot[authID] = count
	}
	return snapshot
}

// inFlightForAuth returns the auth's current in-flight count (0 when unknown).
func (s *authScheduler) inFlightForAuth(authID string) int {
	if s == nil {
		return 0
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inFlight[authID]
}

// authKnownLocked reports whether the auth is currently registered in the
// scheduler under any provider. Callers must hold s.mu.
func (s *authScheduler) authKnownLocked(authID string) bool {
	if providerKey, ok := s.authProviders[authID]; ok && providerKey != "" {
		return true
	}
	return false
}

// selectorStrategy maps a selector implementation to the scheduler semantics it should emulate.
func selectorStrategy(selector Selector) schedulerStrategy {
	switch selector.(type) {
	case *FillFirstSelector:
		return schedulerStrategyFillFirst
	case *WeightedRoundRobinSelector:
		return schedulerStrategyWeightedRoundRobin
	case *WeightedByEntrySelector:
		return schedulerStrategyWeighted
	case *HeadroomByUsageSelector:
		return schedulerStrategyHeadroom
	case *P2CSelector:
		return schedulerStrategyP2C
	case *LeastUsedSelector:
		return schedulerStrategyLeastUsed
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
	s.mixedCursors = make(map[string]int)
	s.mixedWeightedStates = make(map[string]*smoothWeightedState)
	now := time.Now()
	for _, auth := range auths {
		s.upsertAuthLocked(auth, now)
	}
	s.pruneInFlightLocked()
}

// pruneInFlightLocked drops in-flight entries for auths no longer registered.
// Counts for live auths are preserved: the counter tracks dispatches in
// progress, which a rebuild does not interrupt. Callers must hold s.mu.
func (s *authScheduler) pruneInFlightLocked() {
	for authID := range s.inFlight {
		if !s.authKnownLocked(authID) {
			delete(s.inFlight, authID)
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
	return s.pickSingleWithStrategy(ctx, provider, model, opts, tried, schedulerStrategyCurrent)
}

func (s *authScheduler) pickReadyLookup(snapshot map[string]int) func(string) int {
	if snapshot == nil {
		return func(string) int { return 0 }
	}
	return func(authID string) int { return snapshot[authID] }
}

// maxParallelLookup returns a function that resolves a per-auth
// max_parallel_requests cap by auth.ID, defaulting to 0 (unbounded). The
// scheduler holds a snapshot of auth providers at call time so the lookup is
// allocation-free for the common case (no caps set). Used by the round-2
// global fill-first selector to keep filling one auth until it hits its cap.
func (s *authScheduler) maxParallelLookup() func(string) int {
	if s == nil || len(s.providers) == 0 {
		return func(string) int { return 0 }
	}
	caps := make(map[string]int, len(s.providers))
	for _, providerState := range s.providers {
		if providerState == nil || len(providerState.auths) == 0 {
			continue
		}
		for authID, meta := range providerState.auths {
			if meta == nil || meta.auth == nil {
				continue
			}
			if v := maxParallelForAuth(meta.auth); v > 0 {
				caps[authID] = v
			}
		}
	}
	if len(caps) == 0 {
		return func(string) int { return 0 }
	}
	return func(authID string) int { return caps[authID] }
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
	// Clear the headroom-exhausted marker at the start of every pick cycle
	// so the X-NixLLM-Decision header (Task 6) only carries the marker when
	// the current pick actually fell back to least-used ordering.
	s.lastHeadroomExhausted = false
	providerState := s.providers[providerKey]
	if providerState == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	shard := providerState.ensureModelLocked(modelKey, time.Now())
	if shard == nil {
		return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}
	predicate := scheduledAuthPredicate(eligibility, tried, pinnedAuthID, strategy == schedulerStrategyWeightedRoundRobin)
	if strategy == schedulerStrategyHeadroom {
		// Round-2 headroom picks live at the authScheduler level so the
		// lastHeadroomExhausted flag can be set here (the per-shard pick path
		// is modelScheduler-owned and cannot write back to the authScheduler's
		// flag without invasive return-value plumbing). The scan below mirrors
		// the headroom branch in pickMixedWithStrategy: collect every
		// predicate-passing entry in the highest priority bucket, hand them
		// to pickHeadroom, and translate the exhausted boolean into the flag.
		shard.promoteExpiredLocked(time.Now())
		priorityReady, okPriority := shard.highestReadyPriorityLocked(preferWebsocket, predicate)
		if !okPriority {
			return nil, shard.unavailableErrorLocked(provider, model, predicate)
		}
		bucket := shard.readyByPriority[priorityReady]
		if bucket == nil {
			return nil, shard.unavailableErrorLocked(provider, model, predicate)
		}
		entries := make([]*scheduledAuth, 0, len(bucket.all.flat))
		for _, entry := range bucket.all.flat {
			if entry == nil || entry.auth == nil {
				continue
			}
			if predicate != nil && !predicate(entry) {
				continue
			}
			entries = append(entries, entry)
		}
		picked, exhausted := pickHeadroom(entries, s.headroomLookup, s.pickReadyLookup(s.inFlightSnapshot()))
		s.lastHeadroomExhausted = exhausted
		if picked != nil {
			return picked.auth, nil
		}
		return nil, shard.unavailableErrorLocked(provider, model, predicate)
	}
	if picked := shard.pickReadyLocked(preferWebsocket, strategy, predicate, s.pickReadyLookup(s.inFlightSnapshot()), s.maxParallelLookup()); picked != nil {
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
	// Clear the headroom-exhausted marker at the start of every pick cycle
	// so the X-NixLLM-Decision header (Task 6) only carries the marker when
	// the current pick actually fell back to least-used ordering.
	s.lastHeadroomExhausted = false
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
		if picked := shard.pickReadyLocked(false, strategy, predicate, s.pickReadyLookup(s.inFlightSnapshot()), s.maxParallelLookup()); picked != nil {
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
		// Round-2 fill-first: scan every candidate shard at the best
		// priority, then pick the auth with the highest in-flight count
		// below its per-auth max_parallel_requests cap. Distinct from the
		// round-1 deterministic per-shard pick (which used
		// pickReadyAtPriorityLocked directly).
		entries := make([]*scheduledAuth, 0)
		for _, shard := range candidateShards {
			if shard == nil {
				continue
			}
			bucket := shard.readyByPriority[bestPriority]
			if bucket == nil {
				continue
			}
			for _, entry := range bucket.all.flat {
				if entry == nil || entry.auth == nil {
					continue
				}
				if predicate != nil && !predicate(entry) {
					continue
				}
				entries = append(entries, entry)
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
		picked := pickFillFirst(entries, s.pickReadyLookup(s.inFlightSnapshot()), s.maxParallelLookup())
		if picked != nil && picked.meta != nil {
			return picked.auth, picked.meta.providerKey, nil
		}
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
	}

	if strategy == schedulerStrategyWeighted {
		// Round-2 weighted: scan every candidate shard at the best priority,
		// then sample one ready auth proportionally to its per-entry weight
		// column (normalized to >=1 at the planner boundary). Distinct from
		// the round-1 schedulerStrategyWeightedRoundRobin branch below, which
		// uses smoothed weighted state across cycles.
		entries := make([]*scheduledAuth, 0)
		for _, shard := range candidateShards {
			if shard == nil {
				continue
			}
			bucket := shard.readyByPriority[bestPriority]
			if bucket == nil {
				continue
			}
			for _, entry := range bucket.all.flat {
				if entry == nil || entry.auth == nil {
					continue
				}
				if predicate != nil && !predicate(entry) {
					continue
				}
				entries = append(entries, entry)
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
		picked := pickWeighted(entries)
		if picked != nil && picked.meta != nil {
			return picked.auth, picked.meta.providerKey, nil
		}
		return nil, "", s.mixedUnavailableErrorLocked(normalized, model, predicate)
	}

	if strategy == schedulerStrategyHeadroom {
		// Round-2 headroom: scan every candidate shard at the best priority,
		// then pick the auth whose HeadroomLookup value is highest. When every
		// candidate is at or below 0% headroom the helper falls back to
		// least-used ordering and sets s.lastHeadroomExhausted so the
		// X-NixLLM-Decision header (Task 6) can carry the marker. The
		// flag is cleared at the top of pickMixedWithStrategy.
		entries := make([]*scheduledAuth, 0)
		for _, shard := range candidateShards {
			if shard == nil {
				continue
			}
			bucket := shard.readyByPriority[bestPriority]
			if bucket == nil {
				continue
			}
			for _, entry := range bucket.all.flat {
				if entry == nil || entry.auth == nil {
					continue
				}
				if predicate != nil && !predicate(entry) {
					continue
				}
				entries = append(entries, entry)
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
		picked, exhausted := pickHeadroom(entries, s.headroomLookup, s.pickReadyLookup(s.inFlightSnapshot()))
		s.lastHeadroomExhausted = exhausted
		if picked != nil && picked.meta != nil {
			return picked.auth, picked.meta.providerKey, nil
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

	if strategy == schedulerStrategyP2C || strategy == schedulerStrategyLeastUsed {
		entries := make([]*scheduledAuth, 0)
		for _, shard := range candidateShards {
			if shard == nil {
				continue
			}
			bucket := shard.readyByPriority[bestPriority]
			if bucket == nil {
				continue
			}
			entries = append(entries, bucket.all.flat...)
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
		view := readyView{flat: entries}
		var picked *scheduledAuth
		switch strategy {
		case schedulerStrategyP2C:
			picked = view.pickPowerOfTwo(predicate, s.pickReadyLookup(s.inFlightSnapshot()))
		case schedulerStrategyLeastUsed:
			picked = view.pickLeastUsed(predicate, s.pickReadyLookup(s.inFlightSnapshot()))
		}
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
		picked := shard.pickReadyAtPriorityLocked(false, bestPriority, schedulerStrategyRoundRobin, predicate, s.pickReadyLookup(s.inFlightSnapshot()), s.maxParallelLookup())
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

// upsertAuthLocked updates one auth in-place while the scheduler mutex is held.
func (s *authScheduler) upsertAuthLocked(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	authID := strings.TrimSpace(auth.ID)
	providerKey := executorKeyFromAuth(auth)
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
	delete(s.inFlight, authID)
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
	return &scheduledAuthMeta{
		auth:              auth,
		providerKey:       providerKey,
		priority:          authPriority(auth),
		weight:            authWeight(auth),
		websocketEnabled:  authWebsocketsEnabled(auth),
		supportedModelSet: supportedModelSetForAuth(auth),
	}
}

// supportedModelSetForAuth snapshots the registry models currently registered for an auth.
//
// The snapshot also expands each registered model ID to include the other
// side of any OAuth model alias configured on the auth. This is necessary
// because, when an alias has fork=false, registration drops one name from
// the visible model set (see applyOpenAIModelAliasEntries), yet requests
// may still arrive naming either side — e.g. a per-API-key model_route can
// pin the upstream name while only the alias id is registered. Without this
// expansion the scheduler's supportsModel gate would reject the auth for
// the dropped-name route and surface a spurious 503
// "auth_unavailable: no auth available".
func supportedModelSetForAuth(auth *Auth) map[string]struct{} {
	if auth == nil {
		return nil
	}
	authID := strings.TrimSpace(auth.ID)
	if authID == "" {
		return nil
	}
	models := registry.GetGlobalRegistry().GetModelsForClient(authID)
	if len(models) == 0 && len(auth.Attributes) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(models)+len(auth.Attributes))
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
	// Add the counterpart side of each OAuth alias configured on the auth so
	// requests naming the dropped (non-registered) side still route.
	for _, alias := range OAuthModelAliasesFromAttributes(auth.Attributes) {
		name := canonicalModelKey(alias.Name)
		aliasName := canonicalModelKey(alias.Alias)
		if name != "" {
			set[name] = struct{}{}
		}
		if aliasName != "" {
			set[aliasName] = struct{}{}
		}
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
func (m *modelScheduler) pickReadyLocked(preferWebsocket bool, strategy schedulerStrategy, predicate func(*scheduledAuth) bool, inFlightCount func(string) int, maxParallel func(string) int) *Auth {
	if m == nil {
		return nil
	}
	m.promoteExpiredLocked(time.Now())
	priorityReady, okPriority := m.highestReadyPriorityLocked(preferWebsocket, predicate)
	if !okPriority {
		return nil
	}
	return m.pickReadyAtPriorityLocked(preferWebsocket, priorityReady, strategy, predicate, inFlightCount, maxParallel)
}

// highestReadyPriorityLocked returns the highest priority bucket that still has a matching ready auth.
// The caller must ensure expired entries are already promoted when needed.
func (m *modelScheduler) highestReadyPriorityLocked(preferWebsocket bool, predicate func(*scheduledAuth) bool) (int, bool) {
	if m == nil {
		return 0, false
	}
	if preferWebsocket {
		// When downstream is websocket and Codex supports websocket transport, prefer websocket-enabled
		// credentials even if they are in a lower priority tier than HTTP-only credentials
		// (documented for operators in config.example.yaml under the routing section).
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
func (m *modelScheduler) pickReadyAtPriorityLocked(preferWebsocket bool, priority int, strategy schedulerStrategy, predicate func(*scheduledAuth) bool, inFlightCount func(string) int, maxParallel func(string) int) *Auth {
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
		picked = view.pickFillFirst(predicate, inFlightCount, maxParallel)
	case schedulerStrategyWeightedRoundRobin:
		picked = view.pickWeighted(predicate)
	case schedulerStrategyWeighted:
		picked = view.pickWeightedFromView(predicate)
	case schedulerStrategyP2C:
		picked = view.pickPowerOfTwo(predicate, inFlightCount)
	case schedulerStrategyLeastUsed:
		picked = view.pickLeastUsed(predicate, inFlightCount)
	default:
		picked = view.pickRoundRobin(predicate)
	}
	if picked == nil || picked.auth == nil {
		return nil
	}
	return picked.auth
}

// pickPowerOfTwo samples two distinct matching entries and returns the one
// with fewer in-flight requests — classic power-of-two-choices. When only one
// candidate matches it returns it; with none it returns nil. An in-flight tie
// keeps the earlier flat-view entry (stable, index-based; weights are a
// WRR-only concept and are deliberately ignored here).
func (v *readyView) pickPowerOfTwo(predicate func(*scheduledAuth) bool, inFlightCount func(string) int) *scheduledAuth {
	if v == nil || len(v.flat) == 0 {
		return nil
	}
	candidates := make([]*scheduledAuth, 0, len(v.flat))
	for _, entry := range v.flat {
		if predicate == nil || predicate(entry) {
			candidates = append(candidates, entry)
		}
	}
	switch len(candidates) {
	case 0:
		return nil
	case 1:
		return candidates[0]
	}
	first := rand.IntN(len(candidates))
	second := rand.IntN(len(candidates) - 1)
	if second >= first {
		second++
	}
	a, b := candidates[first], candidates[second]
	aIndex, bIndex := first, second
	if bIndex < aIndex {
		a, b = b, a
		aIndex, bIndex = bIndex, aIndex
	}
	return resolvePowerOfTwoPair(a, aIndex, b, bIndex, inFlightCount)
}

// resolvePowerOfTwoPair returns the entry with fewer in-flight requests,
// preferring the earlier flat-view index on a tie. Split out from
// pickPowerOfTwo so the comparison rule is testable deterministically.
func resolvePowerOfTwoPair(a *scheduledAuth, aIndex int, b *scheduledAuth, bIndex int, inFlightCount func(string) int) *scheduledAuth {
	var aCount, bCount int
	if inFlightCount != nil {
		aCount = inFlightCount(a.auth.ID)
		bCount = inFlightCount(b.auth.ID)
	}
	switch {
	case aCount < bCount:
		return a
	case bCount < aCount:
		return b
	}
	if aIndex <= bIndex {
		return a
	}
	return b
}

// pickLeastUsed returns the matching entry with the minimum in-flight count.
// Ties rotate through the view cursor (round-robin among equals) so repeated
// picks spread instead of pinning on the first. Like p2c it ignores entry
// weights — weights are a WRR-only concept.
func (v *readyView) pickLeastUsed(predicate func(*scheduledAuth) bool, inFlightCount func(string) int) *scheduledAuth {
	if v == nil || len(v.flat) == 0 {
		return nil
	}
	var best *scheduledAuth
	bestCount := -1
	tiedIndexes := make([]int, 0, len(v.flat))
	for i, entry := range v.flat {
		if predicate != nil && !predicate(entry) {
			continue
		}
		count := 0
		if inFlightCount != nil {
			count = inFlightCount(entry.auth.ID)
		}
		switch {
		case bestCount < 0 || count < bestCount:
			best = entry
			bestCount = count
			tiedIndexes = tiedIndexes[:0]
			tiedIndexes = append(tiedIndexes, i)
		case count == bestCount:
			tiedIndexes = append(tiedIndexes, i)
		}
	}
	if best == nil {
		return nil
	}
	if len(tiedIndexes) <= 1 {
		return best
	}
	pickIndex := tiedIndexes[v.cursor%len(tiedIndexes)]
	v.cursor++
	if v.cursor >= len(tiedIndexes) {
		v.cursor = 0
	}
	return v.flat[pickIndex]
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

// pickWeighted returns the next ready entry using smooth weighted round-robin.
func (v *readyView) pickWeighted(predicate func(*scheduledAuth) bool) *scheduledAuth {
	if v == nil || len(v.flat) == 0 {
		return nil
	}
	v.weightedState.prepare(scheduledWeightVectorMatching(v.flat, predicate))
	return pickSmoothWeightedScheduled(v.flat, v.weightedState.current, predicate)
}

// pickSmoothWeightedScheduled is the scheduler-side entry point over
// []*scheduledAuth (see smooth_weighted.go for the shared algorithm).
func pickSmoothWeightedScheduled(entries []*scheduledAuth, current map[string]int64, predicate func(*scheduledAuth) bool) *scheduledAuth {
	wrapper := make([]scheduledSmoothWeighted, 0, len(entries))
	for _, entry := range entries {
		wrapper = append(wrapper, scheduledSmoothWeighted{entry: entry})
	}
	filter := func(candidate scheduledSmoothWeighted) bool {
		return predicate == nil || predicate(candidate.entry)
	}
	picked := pickSmoothWeighted(wrapper, current, filter)
	if picked.entry == nil {
		return nil
	}
	return picked.entry
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
