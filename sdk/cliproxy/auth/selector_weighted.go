package auth

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"sort"
	"strconv"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// weightedAccountingPoolKey carries the manager-validated pre-pin pool. It affects
// accounting only; the selector still receives pin-filtered selection candidates.
type weightedAccountingPoolKey struct{}

// weightedTransportProviderKey preserves single-provider transport preference on
// the mixed entrypoint without changing its weighted accounting namespace.
type weightedTransportProviderKey struct{}

// weightedReservation separates speculative credit from committed, clamped credit.
// Releasing one request must never undo accounting committed by another request.
type weightedReservation struct {
	selector        *WeightedRoundRobinSelector
	state           *weightedPoolState
	generation      uint64
	member          int
	selectedID      string
	selectedEpoch   uint64
	candidateIDs    []string
	candidateEpochs []uint64
	total           int64
	bound           bool
	finished        bool
}

// pickWithAffinityReservation normalizes weighted candidates and defers accounting
// until the local binding completes. Other fallback selectors keep their behavior.
func (s *SessionAffinitySelector) pickWithAffinityReservation(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth, bound *Auth, reservation **weightedReservation) (*Auth, error) {
	weighted, ok := s.fallback.(*WeightedRoundRobinSelector)
	if !ok {
		if bound != nil {
			return bound, nil
		}
		return s.fallback.Pick(ctx, provider, model, opts, auths)
	}
	picked, pending, errPick := weighted.reserveForRequest(ctx, provider, model, opts, auths, bound)
	*reservation = pending
	return picked, errPick
}

// reserveForRequest shares candidate normalization and pinned accounting between
// bare weighted routing and session affinity, which owns deferred confirmation.
func (s *WeightedRoundRobinSelector) reserveForRequest(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth, bound *Auth) (*Auth, *weightedReservation, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return nil, nil, errContext
	}
	if accountingAuths, ok := ctx.Value(weightedAccountingPoolKey{}).([]*Auth); ok {
		pinnedID := pinnedAuthIDFromMetadata(opts.Metadata)
		if bound == nil {
			for _, auth := range auths {
				if auth != nil && auth.ID == pinnedID {
					bound = auth
					break
				}
			}
		}
		if bound == nil || bound.ID != pinnedID || authWeight(bound) <= 0 {
			return nil, nil, &Error{Code: "auth_unavailable", Message: "pinned auth is not available for weighted accounting"}
		}
		auths = positiveWeightAuths(accountingAuths)
	}
	transportProvider := provider
	if singleProvider, ok := ctx.Value(weightedTransportProviderKey{}).(string); ok {
		transportProvider = singleProvider
	}
	if providerPrefersWebsocketTransport(transportProvider) {
		// Reuse the transport filter for every provider preferred by the scheduler.
		transportProvider = "codex"
	}
	var available []*Auth
	authID := ""
	transportClass := ""
	if bound == nil {
		var errAvailable error
		available, errAvailable = getSelectorAvailableAuths(ctx, positiveWeightAuths(auths), provider, model, time.Now())
		if errAvailable != nil {
			return nil, nil, errAvailable
		}
		available = preferCodexWebsocketAuths(ctx, transportProvider, available)
		if cliproxyexecutor.DownstreamWebsocket(ctx) && transportProvider == "codex" && len(available) > 0 {
			if authWebsocketsEnabled(available[0]) {
				transportClass = "ws"
			} else {
				transportClass = "http"
			}
		}
	} else {
		authID = bound.ID
		// A binding may bypass priority or transport preference, but its traffic
		// must only charge peers that compete with it, never another priority tier.
		priority := authPriority(bound)
		for _, auth := range auths {
			if authPriority(auth) == priority {
				available = append(available, auth)
			}
		}
		preferred := preferCodexWebsocketAuths(ctx, transportProvider, available)
		if len(preferred) < len(available) && !authWebsocketsEnabled(bound) {
			// Keep an HTTP-bound session outside the preferred WebSocket pool.
			nonWebsocket := available[:0]
			for _, auth := range available {
				if !authWebsocketsEnabled(auth) {
					nonWebsocket = append(nonWebsocket, auth)
				}
			}
			available = nonWebsocket
			transportClass = "http"
		} else {
			available = preferred
			if cliproxyexecutor.DownstreamWebsocket(ctx) && transportProvider == "codex" && len(available) > 0 {
				if authWebsocketsEnabled(available[0]) {
					transportClass = "ws"
				} else {
					transportClass = "http"
				}
			}
		}
	}
	priority := inferredWeightedPoolPriority(available)
	key := provider + ":" + canonicalModelLookupKey(weightedSelectorStateModel(ctx, model))
	picked, pending := s.reserveScoped(key, priority, transportClass, available, authID)
	if picked == nil {
		return nil, nil, &Error{Code: "auth_unavailable", Message: "no auth available with positive weight"}
	}
	return picked, pending, nil
}

// weightedPoolMember stores each ID once, with parallel accounting values in a
// single allocation. Members are sorted by ID; reservations retain only an index.
type weightedPoolMember struct {
	id       string
	weight   int64
	current  int64
	pending  int64
	epoch    uint64
	regEpoch uint64
	regGen   uint64
	inFlight uint32
}

type weightedAuthVersion struct {
	regEpoch uint64
	regGen   uint64
}

func extractWeightedAuthVersions(available []*Auth, ids []string) []weightedAuthVersion {
	versions := make([]weightedAuthVersion, len(ids))
	if len(available) == len(ids) {
		sortedMatch := true
		for i, auth := range available {
			if auth == nil || auth.ID != ids[i] {
				sortedMatch = false
				break
			}
			versions[i] = weightedAuthVersion{regEpoch: auth.RegistrationEpoch, regGen: auth.Generation}
		}
		if sortedMatch {
			return versions
		}
		clear(versions)
	}
	for _, auth := range available {
		if auth == nil {
			continue
		}
		idx := sort.SearchStrings(ids, auth.ID)
		if idx < len(ids) && ids[idx] == auth.ID {
			v := weightedAuthVersion{regEpoch: auth.RegistrationEpoch, regGen: auth.Generation}
			if v.regEpoch > versions[idx].regEpoch || (v.regEpoch == versions[idx].regEpoch && v.regGen > versions[idx].regGen) {
				versions[idx] = v
			}
		}
	}
	return versions
}

func memberOlderOrEqual(member weightedPoolMember, maxEpoch, maxGen uint64) bool {
	if member.regEpoch != maxEpoch {
		return member.regEpoch < maxEpoch
	}
	return member.regGen <= maxGen
}

// weightedPoolState is separate from the scheduler's transient-subset accumulator:
// each selector pool has fixed membership and shares its weights across reservations.
type weightedPoolState struct {
	route           string
	members         []weightedPoolMember
	inFlight        uint64
	generation      uint64
	next            *weightedPoolState // Exact-identity collision chain for the digest key.
	dynamic         bool               // Shared bucket-scoped accumulator for pools > maxExactWeightedPoolMembers.
	peak            int                // Largest candidate count observed on a dynamic pool.
	nextMemberEpoch uint64             // Monotonic member identity counter isolating removed/re-added IDs.
	lastActiveIDs   []string           // Most recent active candidate IDs for post-drain compaction.
}

// maxExactWeightedPoolMembers is the candidate-set size above which exact-membership
// pools switch to a shared bucket-scoped dynamic accumulator so transient retry
// exclusions and cooldowns cannot collapse smooth WRR onto the lowest Auth.IDs.
const maxExactWeightedPoolMembers = maxSmoothWeightedStateEntries

func inferredWeightedPoolPriority(available []*Auth) int {
	for _, auth := range available {
		if auth != nil {
			return authPriority(auth)
		}
	}
	return 0
}

func dynamicWeightedPoolKey(route string, priority int, transportClass string) string {
	if priority == 0 && transportClass == "" {
		return route
	}
	return route + "\x00p=" + strconv.Itoa(priority) + "\x00t=" + transportClass
}

func sortedWeightedIDs(weights map[string]int64) []string {
	ids := make([]string, 0, len(weights))
	for id := range weights {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// weightedPoolStateKey isolates competing sets without retaining concatenated IDs.
// Weights stay out of the key so configuration changes invalidate reservations.
func weightedPoolStateKey(route string, weights map[string]int64) [32]byte {
	return weightedPoolDigest(route, sortedWeightedIDs(weights))
}

func weightedPoolDigest(route string, ids []string) [32]byte {
	size := 8 + len(route)
	for _, id := range ids {
		size += 8 + len(id)
	}
	identity := make([]byte, 0, size)
	identity = binary.LittleEndian.AppendUint64(identity, uint64(len(route)))
	identity = append(identity, route...)
	for _, id := range ids {
		identity = binary.LittleEndian.AppendUint64(identity, uint64(len(id)))
		identity = append(identity, id...)
	}
	return sha256.Sum256(identity)
}

// matches verifies the full identity even on a digest hit. Hash collisions must
// never combine the quota or configured weights of unrelated candidate pools.
func (s *weightedPoolState) matches(route string, ids []string) bool {
	if s.route != route || len(s.members) != len(ids) {
		return false
	}
	for i, id := range ids {
		if s.members[i].id != id {
			return false
		}
	}
	return true
}

func (s *weightedPoolState) prepareWithVersions(weights []int64, versions []weightedAuthVersion) {
	weightChanged := false
	epochReplaced := false
	for i, member := range s.members {
		if member.weight != weights[i] {
			weightChanged = true
			break
		}
		if member.regEpoch > 0 && versions[i].regEpoch > member.regEpoch {
			epochReplaced = true
		}
	}
	if weightChanged {
		s.generation++
		s.inFlight = 0
		for i := range s.members {
			s.members[i].weight = weights[i]
			s.members[i].current = 0
			s.members[i].pending = 0
			s.members[i].regEpoch = versions[i].regEpoch
			s.members[i].regGen = versions[i].regGen
		}
		return
	}
	if epochReplaced {
		s.generation++
		s.inFlight = 0
		var totalWeight int64
		for i := range s.members {
			s.members[i].pending = 0
			if s.members[i].regEpoch > 0 && versions[i].regEpoch > s.members[i].regEpoch {
				s.members[i].current = 0
			}
			if versions[i].regEpoch > s.members[i].regEpoch ||
				(versions[i].regEpoch == s.members[i].regEpoch && versions[i].regGen > s.members[i].regGen) {
				s.members[i].regEpoch = versions[i].regEpoch
				s.members[i].regGen = versions[i].regGen
			}
			totalWeight = saturatingAddInt64(totalWeight, s.members[i].weight)
		}
		if totalWeight > 0 {
			s.boundCredits(totalWeight)
		}
		return
	}
	for i := range s.members {
		if versions[i].regEpoch > s.members[i].regEpoch ||
			(versions[i].regEpoch == s.members[i].regEpoch && versions[i].regGen > s.members[i].regGen) {
			s.members[i].regEpoch = versions[i].regEpoch
			s.members[i].regGen = versions[i].regGen
		}
	}
}

// reserve selects and reserves credit atomically. A non-empty authID preserves a
// sticky binding; otherwise committed and pending credits both guide selection.
func (s *WeightedRoundRobinSelector) reserve(route string, available []*Auth, authID string) (*Auth, *weightedReservation) {
	return s.reserveScoped(route, inferredWeightedPoolPriority(available), "", available, authID)
}

func (s *WeightedRoundRobinSelector) reserveScoped(route string, priority int, transportClass string, available []*Auth, authID string) (*Auth, *weightedReservation) {
	weightVector := authWeightVector(available)
	ids := sortedWeightedIDs(weightVector)
	weights := make([]int64, len(ids))
	for i, id := range ids {
		weights[i] = weightVector[id]
	}
	return s.reservePoolScoped(route, priority, transportClass, available, weights, ids, authID)
}

// reservePool consumes a normalized pool. Scheduler ready views already supply
// ID-sorted candidates and cached weights, avoiding sorting and cloning auths.
// ids must be sorted, with one positive weight per ID in the parallel slice.
func (s *WeightedRoundRobinSelector) reservePool(route string, available []*Auth, weights []int64, ids []string, authID string) (*Auth, *weightedReservation) {
	return s.reservePoolScoped(route, inferredWeightedPoolPriority(available), "", available, weights, ids, authID)
}

func (s *WeightedRoundRobinSelector) reservePoolScoped(route string, priority int, transportClass string, available []*Auth, weights []int64, ids []string, authID string) (*Auth, *weightedReservation) {
	dKey := dynamicWeightedPoolKey(route, priority, transportClass)
	versions := extractWeightedAuthVersions(available, ids)
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(ids) > maxExactWeightedPoolMembers || s.largeStates[dKey] != nil {
		return s.reserveDynamicPoolLocked(dKey, route, available, weights, ids, versions, authID)
	}
	key := weightedPoolDigest(route, ids)
	if s.states == nil {
		s.states = make(map[[32]byte]*weightedPoolState)
	}
	state := s.states[key]
	for state != nil && !state.matches(route, ids) {
		state = state.next
	}
	if state == nil {
		limit := s.poolKeyLimit()
		if s.stateCount+len(s.largeStates) >= limit {
			if s.stateCount > 0 {
				s.states = make(map[[32]byte]*weightedPoolState)
				s.stateCount = 0
			} else {
				s.evictOneLargePoolLocked()
			}
		}
		state = &weightedPoolState{route: route, members: make([]weightedPoolMember, len(ids)), next: s.states[key]}
		for i, id := range ids {
			state.members[i] = weightedPoolMember{
				id:       id,
				weight:   weights[i],
				regEpoch: versions[i].regEpoch,
				regGen:   versions[i].regGen,
			}
		}
		s.states[key] = state
		s.stateCount++
	} else {
		state.prepareWithVersions(weights, versions)
	}
	var picked *Auth
	var pickedCurrent, total int64
	pickedMember := -1
	for _, auth := range available {
		if auth == nil {
			continue
		}
		index := sort.SearchStrings(ids, auth.ID)
		if index == len(ids) || ids[index] != auth.ID || weights[index] <= 0 {
			continue
		}
		total = saturatingAddInt64(total, weights[index])
		if authID != "" && auth.ID != authID {
			continue
		}
		member := &state.members[index]
		current := saturatingAddInt64(member.current, member.pending)
		current = saturatingAddInt64(current, member.weight)
		if picked == nil || current > pickedCurrent {
			picked, pickedCurrent, pickedMember = auth, current, index
		}
	}
	if picked == nil {
		return nil, nil
	}
	// Bound traffic beyond the compensation window still keeps its credential,
	// but must not create speculative credit that can starve concurrent picks.
	if authID != "" && !state.boundChargeFits(pickedMember, total) {
		return picked, nil
	}
	state.adjustPending(pickedMember, total, false)
	state.inFlight++
	return picked, &weightedReservation{
		selector: s, state: state, generation: state.generation,
		member: pickedMember, total: total, bound: authID != "",
	}
}

func (s *WeightedRoundRobinSelector) poolKeyLimit() int {
	if s.maxKeys > 0 {
		return s.maxKeys
	}
	return 4096
}

func (s *WeightedRoundRobinSelector) removeLargeOrderKeyLocked(target string) {
	for i, key := range s.largeOrder {
		if key == target {
			s.largeOrder = append(s.largeOrder[:i], s.largeOrder[i+1:]...)
			return
		}
	}
}

func (s *WeightedRoundRobinSelector) evictOneLargePoolLocked() {
	if len(s.largeOrder) == 0 {
		for k := range s.largeStates {
			delete(s.largeStates, k)
			return
		}
		return
	}
	for i, key := range s.largeOrder {
		st := s.largeStates[key]
		if st == nil || st.inFlight == 0 {
			delete(s.largeStates, key)
			s.largeOrder = append(s.largeOrder[:i], s.largeOrder[i+1:]...)
			return
		}
	}
	oldest := s.largeOrder[0]
	s.largeOrder = s.largeOrder[1:]
	delete(s.largeStates, oldest)
}

func (s *WeightedRoundRobinSelector) reserveDynamicPoolLocked(dKey, route string, available []*Auth, weights []int64, ids []string, versions []weightedAuthVersion, authID string) (*Auth, *weightedReservation) {
	if s.largeStates == nil {
		s.largeStates = make(map[string]*weightedPoolState)
	}
	state := s.largeStates[dKey]
	if state == nil {
		limit := s.poolKeyLimit()
		for len(s.largeStates)+s.stateCount >= limit {
			if s.stateCount > 0 {
				s.states = make(map[[32]byte]*weightedPoolState)
				s.stateCount = 0
				if len(s.largeStates) < limit {
					break
				}
			}
			if len(s.largeStates) == 0 {
				break
			}
			s.evictOneLargePoolLocked()
		}
		state = &weightedPoolState{route: route, dynamic: true}
		s.largeStates[dKey] = state
		s.largeOrder = append(s.largeOrder, dKey)
	}
	state.syncDynamicMembers(ids, weights, versions)

	var picked *Auth
	var pickedEpoch uint64
	var pickedCurrent, total int64
	for _, auth := range available {
		if auth == nil {
			continue
		}
		idIndex := sort.SearchStrings(ids, auth.ID)
		if idIndex == len(ids) || ids[idIndex] != auth.ID || weights[idIndex] <= 0 {
			continue
		}
		memberIndex, okMember := state.findMember(auth.ID)
		if !okMember {
			continue
		}
		weight := weights[idIndex]
		total = saturatingAddInt64(total, weight)
		if authID != "" && auth.ID != authID {
			continue
		}
		member := &state.members[memberIndex]
		current := saturatingAddInt64(member.current, member.pending)
		current = saturatingAddInt64(current, weight)
		if picked == nil || current > pickedCurrent {
			picked, pickedEpoch, pickedCurrent = auth, member.epoch, current
		}
	}
	if picked == nil {
		return nil, nil
	}
	candidateIDs := append([]string(nil), ids...)
	state.lastActiveIDs = candidateIDs
	candidateEpochs := make([]uint64, len(ids))
	for i, id := range ids {
		if memberIndex, okMember := state.findMember(id); okMember {
			candidateEpochs[i] = state.members[memberIndex].epoch
		}
	}
	fullTotalWeight := max(total, state.totalMemberWeight())
	if authID != "" && !state.dynamicBoundChargeFits(candidateIDs, candidateEpochs, picked.ID, pickedEpoch, total, fullTotalWeight) {
		return picked, nil
	}
	state.adjustDynamicPending(candidateIDs, candidateEpochs, picked.ID, pickedEpoch, total, false)
	state.inFlight++
	return picked, &weightedReservation{
		selector:        s,
		state:           state,
		generation:      state.generation,
		selectedID:      picked.ID,
		selectedEpoch:   pickedEpoch,
		candidateIDs:    candidateIDs,
		candidateEpochs: candidateEpochs,
		total:           total,
		bound:           authID != "",
	}
}

func (s *weightedPoolState) findMember(id string) (int, bool) {
	index := sort.Search(len(s.members), func(i int) bool { return s.members[i].id >= id })
	if index < len(s.members) && s.members[index].id == id {
		return index, true
	}
	return index, false
}

func (s *weightedPoolState) findMemberWithEpoch(id string, epoch uint64) (int, bool) {
	index, ok := s.findMember(id)
	if !ok || s.members[index].epoch != epoch {
		return index, false
	}
	return index, true
}

func (s *weightedPoolState) totalMemberWeight() int64 {
	var total int64
	for _, member := range s.members {
		total = saturatingAddInt64(total, member.weight)
	}
	return total
}

func (s *weightedPoolState) syncDynamicMembers(ids []string, weights []int64, versions []weightedAuthVersion) {
	if len(ids) > s.peak {
		s.peak = len(ids)
	}
	changed := false
	for i, id := range ids {
		if index, ok := s.findMember(id); ok && s.members[index].weight != weights[i] {
			changed = true
			break
		}
	}
	if changed {
		s.generation++
		s.inFlight = 0
		for i := range s.members {
			s.members[i].current = 0
			s.members[i].pending = 0
			s.members[i].inFlight = 0
		}
	}
	for i, id := range ids {
		ver := versions[i]
		index, ok := s.findMember(id)
		if ok {
			if s.members[index].regEpoch > 0 && ver.regEpoch > s.members[index].regEpoch {
				s.nextMemberEpoch++
				s.members[index] = weightedPoolMember{
					id:       id,
					weight:   weights[i],
					epoch:    s.nextMemberEpoch,
					regEpoch: ver.regEpoch,
					regGen:   ver.regGen,
				}
				continue
			}
			s.members[index].weight = weights[i]
			if ver.regEpoch > s.members[index].regEpoch ||
				(ver.regEpoch == s.members[index].regEpoch && ver.regGen > s.members[index].regGen) {
				s.members[index].regEpoch = ver.regEpoch
				s.members[index].regGen = ver.regGen
			}
			continue
		}
		s.nextMemberEpoch++
		s.members = append(s.members, weightedPoolMember{})
		copy(s.members[index+1:], s.members[index:])
		s.members[index] = weightedPoolMember{
			id:       id,
			weight:   weights[i],
			epoch:    s.nextMemberEpoch,
			regEpoch: ver.regEpoch,
			regGen:   ver.regGen,
		}
	}
	s.pruneDynamicMembers(ids)
}

func (s *weightedPoolState) pruneDynamicMembers(presentIDs []string) {
	pruneBound := max(maxSmoothWeightedStateEntries, 2*s.peak)
	if len(s.members) <= pruneBound {
		return
	}
	present := make(map[string]struct{}, len(presentIDs))
	for _, id := range presentIDs {
		present[id] = struct{}{}
	}
	keepCount := 0
	for _, member := range s.members {
		if member.inFlight > 0 {
			keepCount++
			continue
		}
		if _, ok := present[member.id]; ok {
			keepCount++
		}
	}
	if keepCount == len(s.members) {
		return
	}
	kept := make([]weightedPoolMember, 0, keepCount)
	for _, member := range s.members {
		if member.inFlight > 0 {
			kept = append(kept, member)
			continue
		}
		if _, ok := present[member.id]; ok {
			kept = append(kept, member)
		}
	}
	s.members = kept
}

func (s *weightedPoolState) adjustDynamicPending(ids []string, epochs []uint64, selectedID string, selectedEpoch uint64, subsetTotal int64, release bool) {
	for i, id := range ids {
		index, ok := s.findMemberWithEpoch(id, epochs[i])
		if !ok {
			continue
		}
		member := &s.members[index]
		delta := member.weight
		if id == selectedID && epochs[i] == selectedEpoch {
			delta -= subsetTotal
		}
		if release {
			delta = -delta
			if member.inFlight > 0 {
				member.inFlight--
			}
		} else {
			member.inFlight++
		}
		member.pending = saturatingAddInt64(member.pending, delta)
	}
}

func (s *weightedPoolState) dynamicBoundChargeFits(ids []string, epochs []uint64, selectedID string, selectedEpoch uint64, subsetTotal, fullTotalWeight int64) bool {
	limit := int64(math.MaxInt64)
	if fullTotalWeight <= math.MaxInt64/maxSmoothWeightedCreditRounds {
		limit = fullTotalWeight * maxSmoothWeightedCreditRounds
	}
	for i, id := range ids {
		index, ok := s.findMemberWithEpoch(id, epochs[i])
		if !ok {
			continue
		}
		member := &s.members[index]
		delta := member.weight
		if id == selectedID && epochs[i] == selectedEpoch {
			delta -= subsetTotal
		}
		current := saturatingAddInt64(member.current, member.pending)
		next := saturatingAddInt64(current, delta)
		if next < -limit || next > limit {
			return false
		}
	}
	return true
}

func (s *weightedPoolState) chargeDynamic(ids []string, epochs []uint64, selectedID string, selectedEpoch uint64, subsetTotal int64, bound bool) {
	fullTotalWeight := max(subsetTotal, s.totalMemberWeight())
	if !bound || s.dynamicBoundChargeFits(ids, epochs, selectedID, selectedEpoch, subsetTotal, fullTotalWeight) {
		for i, id := range ids {
			if index, ok := s.findMemberWithEpoch(id, epochs[i]); ok {
				member := &s.members[index]
				member.current = saturatingAddInt64(member.current, member.weight)
			}
		}
		if index, ok := s.findMemberWithEpoch(selectedID, selectedEpoch); ok {
			s.members[index].current = saturatingAddInt64(s.members[index].current, -subsetTotal)
		}
	}
	if s.inFlight == 0 {
		s.boundCredits(fullTotalWeight)
	}
}

// ForgetAuth removes permanently deregistered credentials from weighted pool state.
// Transient exclusions (retry tried sets and cooldowns) must never call ForgetAuth.
func (s *WeightedRoundRobinSelector) ForgetAuth(authID string) {
	s.ForgetAuthUpTo(authID, ^uint64(0), ^uint64(0))
}

// ForgetAuthUpTo removes weighted pool entries for authID whose observed
// (RegistrationEpoch, Generation) is no newer than (maxEpoch, maxGen), so a delayed
// removal or disablement cannot evict a newly re-registered or re-enabled credential.
func (s *WeightedRoundRobinSelector) ForgetAuthUpTo(authID string, maxEpoch, maxGen uint64) {
	if s == nil || authID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for dKey, state := range s.largeStates {
		if index, ok := state.findMember(authID); ok && memberOlderOrEqual(state.members[index], maxEpoch, maxGen) {
			last := len(state.members) - 1
			copy(state.members[index:], state.members[index+1:])
			state.members[last] = weightedPoolMember{}
			state.members = state.members[:last]
			if state.peak > len(state.members) {
				state.peak = len(state.members)
			}
			if len(state.members) > 0 && cap(state.members) > 2*max(maxSmoothWeightedStateEntries, len(state.members)) {
				compacted := make([]weightedPoolMember, len(state.members))
				copy(compacted, state.members)
				state.members = compacted
			}
			state.lastActiveIDs = nil
		}
		if len(state.members) == 0 || (state.inFlight == 0 && state.peak <= maxExactWeightedPoolMembers && len(state.members) <= maxExactWeightedPoolMembers) {
			delete(s.largeStates, dKey)
			s.removeLargeOrderKeyLocked(dKey)
		}
	}
	for key, head := range s.states {
		var previous *weightedPoolState
		for state := head; state != nil; {
			next := state.next
			if index, ok := state.findMember(authID); ok && memberOlderOrEqual(state.members[index], maxEpoch, maxGen) {
				if previous == nil {
					if next == nil {
						delete(s.states, key)
					} else {
						s.states[key] = next
					}
				} else {
					previous.next = next
				}
				s.stateCount--
			} else {
				previous = state
			}
			state = next
		}
	}
}

// finish confirms or releases a reservation exactly once under the selector lock.
func (r *weightedReservation) finish(committed bool) {
	if r == nil {
		return
	}
	r.selector.mu.Lock()
	defer r.selector.mu.Unlock()
	if r.finished {
		return
	}
	r.finished = true
	if r.state.generation != r.generation {
		return
	}
	if r.state.dynamic {
		r.state.adjustDynamicPending(r.candidateIDs, r.candidateEpochs, r.selectedID, r.selectedEpoch, r.total, true)
		r.state.inFlight--
		if committed {
			r.state.chargeDynamic(r.candidateIDs, r.candidateEpochs, r.selectedID, r.selectedEpoch, r.total, r.bound)
		} else if r.state.inFlight == 0 {
			r.state.boundCredits(max(r.total, r.state.totalMemberWeight()))
		}
		if r.state.inFlight == 0 {
			r.state.pruneDynamicMembers(r.state.lastActiveIDs)
		}
		return
	}
	r.state.adjustPending(r.member, r.total, true)
	r.state.inFlight--
	if committed {
		r.state.charge(r.member, r.bound)
	} else if r.state.inFlight == 0 {
		// A final rollback can remove the offset to already committed credit.
		r.state.boundCredits(r.total)
	}
}

// adjustPending must be called with the selector mutex held. Pending deltas are
// not clamped, so releasing them cannot lose credit at the compensation bound.
func (s *weightedPoolState) adjustPending(selected int, total int64, release bool) {
	for i := range s.members {
		member := &s.members[i]
		delta := member.weight
		if i == selected {
			delta -= total
		}
		if release {
			delta = -delta
		}
		member.pending = saturatingAddInt64(member.pending, delta)
	}
}

// maxSmoothWeightedCreditRounds bounds compensation after a long sticky session.
const maxSmoothWeightedCreditRounds int64 = 32

// boundChargeFits admits the whole sticky delta or none. Both reservation and
// commit include outstanding deltas: committed credit alone can transiently
// exceed the cap while successful reservations settle in a different order.
func (s *weightedPoolState) boundChargeFits(selected int, totalWeight int64) bool {
	limit := int64(math.MaxInt64)
	if totalWeight <= math.MaxInt64/maxSmoothWeightedCreditRounds {
		limit = totalWeight * maxSmoothWeightedCreditRounds
	}
	for i, member := range s.members {
		delta := member.weight
		if i == selected {
			delta -= totalWeight
		}
		current := saturatingAddInt64(member.current, member.pending)
		next := saturatingAddInt64(current, delta)
		if next < -limit || next > limit {
			return false
		}
	}
	return true
}

func (s *weightedPoolState) charge(selected int, bound bool) {
	var totalWeight int64
	for _, member := range s.members {
		totalWeight = saturatingAddInt64(totalWeight, member.weight)
	}
	if !bound || s.boundChargeFits(selected, totalWeight) {
		// At the cap, forced traffic cannot erase a small peer's earnings.
		// Pending repayments still count until they actually roll back.
		for i := range s.members {
			member := &s.members[i]
			member.current = saturatingAddInt64(member.current, member.weight)
		}
		member := &s.members[selected]
		member.current = saturatingAddInt64(member.current, -totalWeight)
	}
	if s.inFlight == 0 {
		s.boundCredits(totalWeight)
	}
}

// boundCredits runs only once the pool settles. Net pending deltas can be zero
// even with live reservations, so the in-flight count, not len(pending), decides
// when clipping cannot irreversibly discard another reservation's offset.
func (s *weightedPoolState) boundCredits(totalWeight int64) {
	limit := int64(math.MaxInt64)
	if totalWeight <= math.MaxInt64/maxSmoothWeightedCreditRounds {
		limit = totalWeight * maxSmoothWeightedCreditRounds
	}
	var minCredit, maxCredit int64 = math.MaxInt64, math.MinInt64
	for _, member := range s.members {
		minCredit = min(minCredit, member.current)
		maxCredit = max(maxCredit, member.current)
	}
	// A common offset keeps leading credits distinct at the compensation bound.
	// Independent clamps can repeatedly tie the leaders and starve the later
	// candidate in deterministic selection order.
	minOffset := saturatingAddInt64(maxCredit, -limit)
	maxOffset := saturatingAddInt64(minCredit, limit)
	var offset int64
	if minOffset > 0 || minOffset > maxOffset {
		offset = minOffset
	} else if maxOffset < 0 {
		offset = maxOffset
	}
	// If the full spread cannot fit, cap only the trailing debt after rebasing
	// the leader to the upper bound. Do not flatten leading candidates' credits.
	for i := range s.members {
		member := &s.members[i]
		member.current = max(-limit, saturatingAddInt64(member.current, -offset))
	}
}
