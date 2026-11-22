package cliproxy

import "sync/atomic"

// proxxHomeRetryJitterOverride, when set, replaces the random jitter used for
// Home subscriber retry delays. It is nil in production; tests pin it so the
// retry delay after a lost subscription is deterministic.
var proxxHomeRetryJitterOverride atomic.Pointer[float64]

// proxxHomeRetryJitter returns the pinned jitter when an override is set, and
// random otherwise.
func proxxHomeRetryJitter(random float64) float64 {
	if pinned := proxxHomeRetryJitterOverride.Load(); pinned != nil {
		return *pinned
	}
	return random
}
