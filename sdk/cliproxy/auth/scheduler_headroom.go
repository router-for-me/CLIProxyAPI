package auth

// pickHeadroom returns the ready auth whose HeadroomLookup value is highest.
// If every candidate has headroom <= 0 the helper falls back to least-used
// ordering (smallest in-flight count) and returns exhausted=true so callers
// can surface a routing-decision marker (e.g. the X-NixLLM-Decision header).
//
// Tie-break: when two auths share the same headroom value, the auth with the
// smaller auth.ID wins. This keeps the pick deterministic across replicas and
// matches the stable-by-ID ordering used elsewhere in the scheduler.
//
// Best-effort ordering, not enforcement: the real quota limits are enforced
// by policy.WindowFor's 429s (see internal/policy/). The headroom selector
// only biases the order in which the scheduler tries auths — it does not
// override hard limits.
//
// Distinct from pickFillFirst (which biases by in-flight + cap) and
// pickWeighted (which samples by per-entry weight). The headroom pick is the
// round-2 global selector wired through schedulerStrategyHeadroom.
func pickHeadroom(entries []*scheduledAuth, lookup HeadroomLookup, inFlight func(string) int) (*scheduledAuth, bool) {
	if len(entries) == 0 {
		return nil, false
	}
	var best *scheduledAuth
	bestH := -1.0
	var anyPositive bool
	for _, entry := range entries {
		if entry == nil || entry.auth == nil {
			continue
		}
		h := 100.0
		if lookup != nil {
			h = lookup.Headroom(entry.auth.ID)
		}
		if h > 0 {
			anyPositive = true
		}
		switch {
		case best == nil || h > bestH:
			best = entry
			bestH = h
		case h == bestH && best != nil && entry.auth.ID < best.auth.ID:
			best = entry
		}
	}
	if !anyPositive {
		// Fallback: every candidate is at or below 0% headroom. Route to
		// the least-in-flight auth so the request still goes somewhere.
		// Ties break by smaller auth.ID for determinism.
		var luBest *scheduledAuth
		luBestInFlight := 1<<31 - 1
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
			switch {
			case luBest == nil || cur < luBestInFlight:
				luBest = entry
				luBestInFlight = cur
			case cur == luBestInFlight && luBest != nil && entry.auth.ID < luBest.auth.ID:
				luBest = entry
			}
		}
		return luBest, true
	}
	return best, false
}

// Note: there is intentionally no readyView.pickHeadroomFromView wrapper,
// even though pickFillFirst / pickWeightedFromView expose one. The headroom
// branch is hand-rolled inline in pickSingleWithStrategy /
// pickMixedWithStrategy so the call site can co-set
// authScheduler.lastHeadroomExhausted alongside the returned pick without
// invasive plumbing through the shared readyView API.
