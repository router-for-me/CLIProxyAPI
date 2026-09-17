package auth

// HeadroomLookup returns the lowest remaining quota percent across active
// usage windows for an auth. Semantics:
//
//   - 100.0 means no quota is configured (effectively unlimited).
//   - 0.0 means at or above the configured limit on every window.
//   - Negative values indicate over-limit (async writers can lag real usage
//     so the windows can dip below zero briefly).
//
// The round-2 headroom global selector (schedulerStrategyHeadroom in
// scheduler.go) asks this interface once per candidate, picks the highest
// remaining-quota auth, and falls back to least-used when every candidate
// is at or below zero. Production wiring is a static stub today (see
// (*authScheduler).headroomLookup in scheduler.go); the events/quota
// workstream (Task 7) replaces it with a usage_windows-backed reader.
type HeadroomLookup interface {
	Headroom(authID string) float64
}

// StaticHeadroomLookup returns scripted values for tests and for the
// production stub before the usage_windows-backed implementation lands.
// Unknown auth IDs default to 100.0 (unlimited) — that matches the
// "no quota configured" semantic the headroom selector relies on to leave
// untouched auths alone.
type StaticHeadroomLookup map[string]float64

// Headroom returns the scripted headroom percentage for the given auth ID,
// or 100.0 when the auth has no scripted value.
func (s StaticHeadroomLookup) Headroom(authID string) float64 {
	if v, ok := s[authID]; ok {
		return v
	}
	return 100.0
}
