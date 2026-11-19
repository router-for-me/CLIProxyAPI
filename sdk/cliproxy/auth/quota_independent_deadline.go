package auth

import "time"

// Independent rate restrictions retain their own fallback backoff, without
// borrowing an open subscription deadline from the visible composed quota.
func independentRateLimitDeadline(quota QuotaState, retry *time.Duration, now time.Time) time.Time {
	if retry != nil {
		duration := *retry
		if duration < minQuotaCooldownFloor {
			duration = minQuotaCooldownFloor
		}
		return now.Add(duration).Round(0)
	}
	quota.NextRecoverAt = time.Time{}
	next, _ := quotaCooldownAfterFailure(quota, now)
	return next
}
