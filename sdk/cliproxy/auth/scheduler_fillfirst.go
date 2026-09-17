package auth

import (
	"strconv"
	"strings"
)

// maxParallelUnbounded is the cap used when no per-auth max_parallel_requests
// attribute is set; matches the "no cap / unlimited" convention in
// internal/policy/enforce.go (Zero means "no cap / unlimited").
const maxParallelUnbounded = int(^uint(0) >> 1)

// maxParallelForAuth returns the per-auth max_parallel_requests cap parsed
// from auth.Attributes["max_parallel"], or 0 when unset / unparseable. The
// scheduler's maxParallelLookup wires this as the production cap resolver so
// fill-first respects operator-set per-auth concurrency caps; tests can pass
// a scripted lookup instead.
func maxParallelForAuth(auth *Auth) int {
	if auth == nil || len(auth.Attributes) == 0 {
		return 0
	}
	raw := strings.TrimSpace(auth.Attributes["max_parallel"])
	if raw == "" {
		return 0
	}
	parsed, errParse := strconv.Atoi(raw)
	if errParse != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

// pickFillFirst is the readyView-scoped variant of the global fill-first
// selector: from the entries in the bucket, return the auth whose current
// in-flight count is highest while still below its max_parallel_requests cap.
// See pickFillFirst for full semantics. This wrapper is what the scheduler's
// pickReadyAtPriorityLocked switch calls; it preserves the predicate gate
// (auths failing the predicate are skipped before the in-flight scan).
func (v *readyView) pickFillFirst(predicate func(*scheduledAuth) bool, inFlightCount func(string) int, maxParallel func(string) int) *scheduledAuth {
	if v == nil || len(v.flat) == 0 {
		return nil
	}
	candidates := make([]*scheduledAuth, 0, len(v.flat))
	for _, entry := range v.flat {
		if predicate != nil && !predicate(entry) {
			continue
		}
		candidates = append(candidates, entry)
	}
	return pickFillFirst(candidates, inFlightCount, maxParallel)
}

// pickFillFirst returns the ready auth whose current in-flight count is
// highest while still below its max_parallel_requests cap. Distinct from the
// pool-level PoolStrategyFillFirst (deterministic-by-priority on pool rows)
// and the round-1 FillFirstSelector (deterministic first-available by ID):
// the round-2 global fill-first keeps one auth busy to the brim before
// touching the next, which minimizes distinct upstream connections and warms
// any per-key caches.
//
// Tie-break: when two auths share the same in-flight count (and both are
// below cap), the auth with the smaller auth.ID wins. This keeps the pick
// deterministic across replicas and matches the stable-by-ID ordering used
// elsewhere in the scheduler (sort by entries[i].auth.ID).
//
// maxParallel is a lookup the caller can use to scope fill-first by
// per-auth concurrency caps. A return value of 0 (or any value <= 0) means
// "no cap" — pickFillFirst treats the auth as uncapped and only the
// in-flight count matters. The default scheduler wires a lookup that reads
// auth.Attributes["max_parallel"]; tests can pass a scripted lookup.
func pickFillFirst(entries []*scheduledAuth, inFlight func(string) int, maxParallel func(string) int) *scheduledAuth {
	if len(entries) == 0 {
		return nil
	}
	var best *scheduledAuth
	bestInFlight := -1
	for _, entry := range entries {
		if entry == nil || entry.auth == nil {
			continue
		}
		cur := 0
		if inFlight != nil {
			cur = inFlight(entry.auth.ID)
		}
		if cur < 0 {
			cur = 0
		}
		capVal := maxParallelUnbounded
		if maxParallel != nil {
			if v := maxParallel(entry.auth.ID); v > 0 {
				capVal = v
			}
		}
		if cur >= capVal {
			continue
		}
		switch {
		case best == nil || cur > bestInFlight:
			best = entry
			bestInFlight = cur
		case cur == bestInFlight && best != nil && entry.auth.ID < best.auth.ID:
			best = entry
		}
	}
	return best
}
