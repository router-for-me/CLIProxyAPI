package auth

import (
	"net/http"
	"testing"
	"time"
)

// TestApplyAuthFailureStateCapsCarriedOverRecoverAt pins the quota cooldown
// ratchet to quotaBackoffMax.
//
// A single upstream reset far in the future (Anthropic reports weekly resets
// days out) used to latch permanently: the 429 branch carried the previous
// NextRecoverAt forward whenever it was later than the freshly computed
// window, without ever running it through capQuotaCooldown. Because the
// credential is only retried after NextRetryAfter, it could never shorten its
// own window, so one weekly reset parked the seat for days even after the
// provider started accepting it again.
func TestApplyAuthFailureStateCapsCarriedOverRecoverAt(t *testing.T) {
	quotaErr := &Error{Code: "rate_limit", Message: "quota", HTTPStatus: http.StatusTooManyRequests}

	testCases := []struct {
		name       string
		retryAfter *time.Duration
	}{
		{name: "with upstream retry hint", retryAfter: durationPtr(30 * time.Second)},
		{name: "without upstream retry hint", retryAfter: nil},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now()
			weeklyReset := now.Add(5 * 24 * time.Hour)

			auth := &Auth{ID: "carried-over-recover-at"}
			auth.Quota.Exceeded = true
			auth.Quota.Reason = "quota"
			auth.Quota.NextRecoverAt = weeklyReset

			applyAuthFailureState(auth, quotaErr, tc.retryAfter, now, false)

			deadline := now.Add(quotaBackoffMax)
			if auth.Quota.NextRecoverAt.After(deadline) {
				t.Fatalf("NextRecoverAt %v exceeds the %v cap at %v; a far-future upstream reset latched permanently",
					auth.Quota.NextRecoverAt, quotaBackoffMax, deadline)
			}
			if auth.NextRetryAfter.After(deadline) {
				t.Fatalf("NextRetryAfter %v exceeds the %v cap at %v", auth.NextRetryAfter, quotaBackoffMax, deadline)
			}
			// The credential must still be parked; capping is not clearing.
			if !auth.Quota.NextRecoverAt.After(now) {
				t.Fatalf("expected the credential to stay parked, got NextRecoverAt %v", auth.Quota.NextRecoverAt)
			}
		})
	}
}

func durationPtr(d time.Duration) *time.Duration {
	return &d
}
