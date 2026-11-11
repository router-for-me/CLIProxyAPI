package auth

// pickWeightedPoolLocked consumes the scheduler's ID-sorted, immutable ready
// snapshots without cloning the whole pool. Legacy and fast-path selections use
// the same selector-owned credits, including pending reservations.
// The scheduler mutex must be held while reading entries and selecting an auth.
func (s *authScheduler) pickWeightedPoolLocked(route string, priority int, transportClass string, entries []*scheduledAuth, predicate func(*scheduledAuth) bool, pinnedAuthID string, nonWebsocketOnly bool) (*Auth, error) {
	if s.weightedSelector == nil {
		return nil, &Error{Code: "auth_unavailable", Message: "weighted selector is not configured"}
	}
	auths := make([]*Auth, 0, len(entries))
	ids := make([]string, 0, len(entries))
	weights := make([]int64, 0, len(entries))
	for _, entry := range entries {
		if entry == nil || entry.auth == nil || entry.meta == nil || entry.meta.weight <= 0 {
			continue
		}
		if nonWebsocketOnly && entry.meta.websocketEnabled {
			continue
		}
		// The pool identity must cover every positive-weight credential of the
		// structural pool (transport-filtered). Keying on the predicate-filtered
		// subset would mint a fresh pool state (resetting all accumulated credits)
		// whenever a retry exclusion or cooldown shrinks the candidate set,
		// collapsing large pools onto the first Auth.ID-sorted members. Selection
		// still happens only among `auths`.
		ids = append(ids, entry.auth.ID)
		weights = append(weights, entry.meta.weight)
		if predicate != nil && !predicate(entry) {
			continue
		}
		auths = append(auths, entry.auth)
	}
	picked, reservation := s.weightedSelector.reservePoolScoped(route, priority, transportClass, auths, weights, ids, pinnedAuthID)
	if picked == nil {
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available with positive weight"}
	}
	reservation.finish(true)
	return picked, nil
}
