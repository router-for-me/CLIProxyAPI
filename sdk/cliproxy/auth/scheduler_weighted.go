package auth

import "math/rand/v2"

// pickWeighted samples one ready auth proportionally to its per-entry weight
// column (scheduler keeps the weight on scheduledAuthMeta.weight, populated
// from auth.Attributes[AttributeWeight] via authWeight() — the renderer/upstream
// sync pipeline normalizes the UpstreamProviderAPIKey.Weight *int from the
// PG-backed control plane onto that attribute at the planner boundary).
//
// O(n) linear scan; n is small per priority tier (typically 2-10 entries),
// matching the fill-first selector's pattern. Binary-search variant deferred
// unless profiling shows it matters.
//
// Semantics (round-2, docs/plans/2026-09-17-omniroute-round-2-design.md):
//   - Each candidate contributes its meta.weight (>=1 post-planner) to the
//     total sum.
//   - One rand.Int64N(total) draw selects a slot; the cumulative scan returns
//     the entry whose [low, high) range contains the slot.
//   - Empty input returns nil.
//   - Defensive fallback: if every candidate's effective weight is <= 0
//     (impossible after the planner's normalizeEntryWeight clamps nil/0/-
//     to 1, but the helper must not panic on a stray nil meta), the helper
//     returns the first non-nil entry. This is a safety net — callers should
//     keep entries normalized before reaching the helper.
//
// Distinct from the round-1 smooth-WRR weighted selector
// (schedulerStrategyWeightedRoundRobin → readyView.pickWeighted → pickSmooth-
// WeightedScheduled): the round-1 selector uses smoothed current-value state
// across cycles, the round-2 selector draws one rand.Int64N per pick over the
// prefix sum.
func pickWeighted(entries []*scheduledAuth) *scheduledAuth {
	if len(entries) == 0 {
		return nil
	}
	var total int64
	for _, entry := range entries {
		if entry == nil || entry.auth == nil {
			continue
		}
		if entry.meta == nil {
			continue
		}
		w := entry.meta.weight
		if w > 0 {
			total += w
		}
	}
	if total <= 0 {
		// Defensive fallback: deterministic first non-nil entry.
		for _, entry := range entries {
			if entry != nil && entry.auth != nil {
				return entry
			}
		}
		return nil
	}
	target := rand.Int64N(total)
	cum := int64(0)
	for _, entry := range entries {
		if entry == nil || entry.auth == nil || entry.meta == nil {
			continue
		}
		w := entry.meta.weight
		if w <= 0 {
			continue
		}
		cum += w
		if target < cum {
			return entry
		}
	}
	// Defense in depth: floating-point rounding shouldn't escape the loop
	// above, but if it ever does, return the last eligible entry.
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if entry != nil && entry.auth != nil {
			return entry
		}
	}
	return nil
}

// pickWeightedFromView is the readyView-scoped wrapper used by the
// scheduler's pickReadyAtPriorityLocked switch. It mirrors
// pickFillFirst (readyView.pickFillFirst in scheduler_fillfirst.go):
// candidates are filtered through the predicate first, then handed to the
// pure helper above. Returns nil when no candidate matches the predicate.
func (v *readyView) pickWeightedFromView(predicate func(*scheduledAuth) bool) *scheduledAuth {
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
	return pickWeighted(candidates)
}
