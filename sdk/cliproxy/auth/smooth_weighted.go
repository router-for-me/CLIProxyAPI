package auth

// smoothWeightedCandidate is the minimal view the shared smooth weighted
// round-robin algorithm needs from a pick candidate, so both the selector-side
// ([]*Auth) and scheduler-side ([]*scheduledAuth) call sites can share one
// implementation without one knowing about the other's types. The comparable
// constraint lets the algorithm detect "no pick yet" via the zero value.
type smoothWeightedCandidate interface {
	comparable
	// CandidateID is the stable identity used to key the current map.
	CandidateID() string
	// CandidateWeight is the configured pick weight; <= 0 excludes the candidate.
	CandidateWeight() int64
}

// pickSmoothWeighted advances one smooth weighted round-robin step over the
// candidates and returns the picked candidate, or nil when no candidate has a
// positive weight (or passes filter).
//
// current carries the accumulated per-id "credit" between calls and is mutated
// in place; filter may be nil. Inactive ids (present in current but absent from
// the surviving candidate set) are removed BEFORE accumulating — the strictly
// safer order, since a re-appearing candidate cannot inherit a stale credit it
// did not earn in this cycle.
//
// Both former copies (pickSmoothWeightedAuth in selector.go and
// pickSmoothWeightedScheduled in scheduler.go) are this algorithm; the parity
// test in smooth_weighted_parity_test.go pins their equivalence.
func pickSmoothWeighted[T smoothWeightedCandidate](candidates []T, current map[string]int64, filter func(T) bool) T {
	var picked T
	if current == nil {
		return picked
	}
	active := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.CandidateWeight() <= 0 {
			continue
		}
		if filter != nil && !filter(candidate) {
			continue
		}
		active[candidate.CandidateID()] = struct{}{}
	}
	// Clean before accumulate: drop credits of candidates that no longer
	// participate so a returning candidate starts from a clean slate.
	for id := range current {
		if _, ok := active[id]; !ok {
			delete(current, id)
		}
	}

	var pickedCurrent int64
	var totalWeight int64
	for _, candidate := range candidates {
		weight := candidate.CandidateWeight()
		if weight <= 0 {
			continue
		}
		if filter != nil && !filter(candidate) {
			continue
		}
		id := candidate.CandidateID()
		current[id] = saturatingAddInt64(current[id], weight)
		totalWeight = saturatingAddInt64(totalWeight, weight)
		var zero T
		if picked == zero || current[id] > pickedCurrent {
			picked = candidate
			pickedCurrent = current[id]
		}
	}
	var zero T
	if picked == zero {
		return picked
	}
	pickedID := picked.CandidateID()
	current[pickedID] = saturatingAddInt64(current[pickedID], -totalWeight)
	return picked
}

// pickSmoothWeightedAuth is the selector-side entry point: smooth WRR over
// []*Auth keyed by auth ID with the weight read from the auth's attributes.
// Kept as a thin wrapper so the legacy selector call sites read naturally.
func pickSmoothWeightedAuth(auths []*Auth, current map[string]int64) *Auth {
	wrapper := make([]authSmoothWeighted, 0, len(auths))
	for _, auth := range auths {
		wrapper = append(wrapper, authSmoothWeighted{auth: auth})
	}
	picked := pickSmoothWeighted(wrapper, current, nil)
	if picked.auth == nil {
		return nil
	}
	return picked.auth
}

// authSmoothWeighted adapts *Auth to smoothWeightedCandidate.
type authSmoothWeighted struct {
	auth *Auth
}

func (a authSmoothWeighted) CandidateID() string {
	if a.auth == nil {
		return ""
	}
	return a.auth.ID
}

func (a authSmoothWeighted) CandidateWeight() int64 {
	return authWeight(a.auth)
}

// scheduledSmoothWeighted adapts *scheduledAuth to smoothWeightedCandidate.
type scheduledSmoothWeighted struct {
	entry *scheduledAuth
}

func (s scheduledSmoothWeighted) CandidateID() string {
	if s.entry == nil || s.entry.auth == nil {
		return ""
	}
	return s.entry.auth.ID
}

func (s scheduledSmoothWeighted) CandidateWeight() int64 {
	if s.entry == nil || s.entry.meta == nil {
		return 0
	}
	return s.entry.meta.weight
}
