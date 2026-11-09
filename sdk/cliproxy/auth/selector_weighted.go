package auth

import (
	"context"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// weightedAccountingPoolKey carries the manager-validated pre-pin pool. It affects
// accounting only; the selector still receives pin-filtered selection candidates.
type weightedAccountingPoolKey struct{}

// weightedReservation separates speculative credit from committed, clamped credit.
// Releasing one request must never undo accounting committed by another request.
type weightedReservation struct {
	selector   *WeightedRoundRobinSelector
	state      *smoothWeightedState
	generation uint64
	weights    map[string]int64
	authID     string
	total      int64
	bound      bool
	finished   bool
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
			return nil, &Error{Code: "auth_unavailable", Message: "pinned auth is not available for weighted accounting"}
		}
		auths = positiveWeightAuths(accountingAuths)
	}
	var available []*Auth
	authID := ""
	if bound == nil {
		var errAvailable error
		available, errAvailable = getSelectorAvailableAuths(ctx, positiveWeightAuths(auths), provider, model, time.Now())
		if errAvailable != nil {
			return nil, errAvailable
		}
		available = preferCodexWebsocketAuths(ctx, provider, available)
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
		preferred := preferCodexWebsocketAuths(ctx, provider, available)
		if len(preferred) < len(available) && !authWebsocketsEnabled(bound) {
			// Keep an HTTP-bound session outside the preferred WebSocket pool.
			nonWebsocket := available[:0]
			for _, auth := range available {
				if !authWebsocketsEnabled(auth) {
					nonWebsocket = append(nonWebsocket, auth)
				}
			}
			available = nonWebsocket
		} else {
			available = preferred
		}
	}
	key := provider + ":" + canonicalModelLookupKey(weightedSelectorStateModel(ctx, model))
	picked, pending := weighted.reserve(key, available, authID)
	if picked == nil {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available with positive weight"}
	}
	*reservation = pending
	return picked, nil
}

// weightedPoolStateKey isolates credits for actual competing sets. Reusing a
// provider/model accumulator across different sets grants credit for requests
// those sets cannot serve, which can permanently starve their smaller weights.
// Length-prefixed, sorted IDs avoid collisions and keep input order irrelevant.
// Weights stay out of the key so prepare can invalidate pending reservations on
// configuration changes. Returning to a prior set resumes that set's credits.
func weightedPoolStateKey(route string, weights map[string]int64) string {
	ids := make([]string, 0, len(weights))
	for id := range weights {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var key strings.Builder
	key.WriteString(strconv.Itoa(len(route)))
	key.WriteByte(':')
	key.WriteString(route)
	for _, id := range ids {
		key.WriteString(strconv.Itoa(len(id)))
		key.WriteByte(':')
		key.WriteString(id)
	}
	return key.String()
}

// reserve selects and reserves credit atomically. A non-empty authID preserves a
// sticky binding; otherwise committed and pending credits both guide selection.
func (s *WeightedRoundRobinSelector) reserve(key string, available []*Auth, authID string) (*Auth, *weightedReservation) {
	weights := authWeightVector(available)
	key = weightedPoolStateKey(key, weights)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[string]*smoothWeightedState)
	}
	limit := s.maxKeys
	if limit <= 0 {
		limit = 4096
	}
	if _, ok := s.states[key]; !ok && len(s.states) >= limit {
		s.states = make(map[string]*smoothWeightedState)
	}
	state := s.states[key]
	if state == nil {
		state = &smoothWeightedState{}
		s.states[key] = state
	}
	state.prepare(weights)
	var picked *Auth
	var pickedCurrent, total int64
	for _, auth := range available {
		if auth == nil || weights[auth.ID] <= 0 {
			continue
		}
		total = saturatingAddInt64(total, weights[auth.ID])
		if authID != "" && auth.ID != authID {
			continue
		}
		current := saturatingAddInt64(state.current[auth.ID], state.pending[auth.ID])
		current = saturatingAddInt64(current, weights[auth.ID])
		if picked == nil || current > pickedCurrent {
			picked, pickedCurrent = auth, current
		}
	}
	if picked == nil {
		return nil, nil
	}
	// Bound traffic beyond the compensation window still keeps its credential,
	// but must not create speculative credit that can starve concurrent picks.
	if authID != "" && !state.boundChargeFits(weights, picked.ID, total) {
		return picked, nil
	}
	state.adjustPending(weights, picked.ID, total, false)
	state.inFlight++
	return picked, &weightedReservation{
		selector: s, state: state, generation: state.generation,
		weights: weights, authID: picked.ID, total: total, bound: authID != "",
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
	r.state.adjustPending(r.weights, r.authID, r.total, true)
	r.state.inFlight--
	if committed {
		r.state.charge(r.weights, r.authID, r.bound)
	} else if r.state.inFlight == 0 {
		// A final rollback can remove the offset to already committed credit.
		r.state.boundCredits(r.weights, r.total)
	}
}

// adjustPending must be called with the selector mutex held. Pending deltas are
// not clamped, so releasing them cannot lose credit at the compensation bound.
func (s *smoothWeightedState) adjustPending(weights map[string]int64, authID string, total int64, release bool) {
	for id, weight := range weights {
		delta := weight
		if id == authID {
			delta -= total
		}
		if release {
			delta = -delta
		}
		s.pending[id] = saturatingAddInt64(s.pending[id], delta)
		if s.pending[id] == 0 {
			delete(s.pending, id)
		}
	}
}

// maxSmoothWeightedCreditRounds bounds compensation after a long sticky session.
const maxSmoothWeightedCreditRounds int64 = 32

// boundChargeFits admits the whole sticky delta or none. Both reservation and
// commit include outstanding deltas: committed credit alone can transiently
// exceed the cap while successful reservations settle in a different order.
func (s *smoothWeightedState) boundChargeFits(weights map[string]int64, authID string, totalWeight int64) bool {
	limit := int64(math.MaxInt64)
	if totalWeight <= math.MaxInt64/maxSmoothWeightedCreditRounds {
		limit = totalWeight * maxSmoothWeightedCreditRounds
	}
	for id, weight := range weights {
		delta := weight
		if id == authID {
			delta -= totalWeight
		}
		current := saturatingAddInt64(s.current[id], s.pending[id])
		next := saturatingAddInt64(current, delta)
		if next < -limit || next > limit {
			return false
		}
	}
	return true
}

func (s *smoothWeightedState) charge(weights map[string]int64, authID string, bound bool) {
	var totalWeight int64
	for _, weight := range weights {
		totalWeight = saturatingAddInt64(totalWeight, weight)
	}
	if !bound || s.boundChargeFits(weights, authID, totalWeight) {
		// At the cap, forced traffic cannot erase a small peer's earnings.
		// Pending repayments still count until they actually roll back.
		for id, weight := range weights {
			s.current[id] = saturatingAddInt64(s.current[id], weight)
		}
		s.current[authID] = saturatingAddInt64(s.current[authID], -totalWeight)
	}
	if s.inFlight == 0 {
		s.boundCredits(weights, totalWeight)
	}
}

// boundCredits runs only once the pool settles. Net pending deltas can be zero
// even with live reservations, so the in-flight count, not len(pending), decides
// when clipping cannot irreversibly discard another reservation's offset.
func (s *smoothWeightedState) boundCredits(weights map[string]int64, totalWeight int64) {
	limit := int64(math.MaxInt64)
	if totalWeight <= math.MaxInt64/maxSmoothWeightedCreditRounds {
		limit = totalWeight * maxSmoothWeightedCreditRounds
	}
	var minCredit, maxCredit int64 = math.MaxInt64, math.MinInt64
	for id := range weights {
		minCredit = min(minCredit, s.current[id])
		maxCredit = max(maxCredit, s.current[id])
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
	// Temporarily excluded credentials retain their credit for later selections.
	for id := range weights {
		s.current[id] = max(-limit, saturatingAddInt64(s.current[id], -offset))
	}
}
