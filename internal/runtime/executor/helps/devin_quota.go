package helps

import (
	"strings"
	"time"
)

// DevinQuotaPeriod identifies an account-wide Devin usage quota window.
type DevinQuotaPeriod int

const (
	// DevinQuotaPeriodNone means the error is not an account-wide
	// daily/weekly usage quota exhaustion.
	DevinQuotaPeriodNone DevinQuotaPeriod = iota
	// DevinQuotaPeriodDaily marks daily usage quota exhaustion.
	DevinQuotaPeriodDaily
	// DevinQuotaPeriodWeekly marks weekly usage quota exhaustion.
	DevinQuotaPeriodWeekly
)

// devinQuotaResetMaxDelay bounds how far in the future an observed quota
// reset timestamp may lie before it is treated as unusable. A weekly window
// plus slack covers a just-rolled-over observation; anything further out is
// implausible for a daily/weekly quota and must not pin the credential.
const devinQuotaResetMaxDelay = 8 * 24 * time.Hour

// DevinUsageQuotaExhaustionPeriod reports whether the upstream error
// message explicitly says the account-wide daily or weekly usage quota has
// been exhausted (issue #6690). Matching is deliberately narrow: the period
// word, "quota", and "exhausted" must all be present, so monthly ACU
// messages, generic rate limits, and transient capacity errors never match
// and stay on the short per-model backoff path. If the message somehow
// names both periods, weekly wins because it is the longer window.
func DevinUsageQuotaExhaustionPeriod(message string) DevinQuotaPeriod {
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "quota") || !strings.Contains(lower, "exhausted") {
		return DevinQuotaPeriodNone
	}
	if strings.Contains(lower, "weekly") {
		return DevinQuotaPeriodWeekly
	}
	if strings.Contains(lower, "daily") {
		return DevinQuotaPeriodDaily
	}
	return DevinQuotaPeriodNone
}

// DevinQuotaRetryAfter converts an observed quota reset timestamp (stored
// in auth Quota.Signals by the executor's Refresh path) into a retry delay.
// It returns nil unless the signal parses and is strictly in the future
// within a plausible daily/weekly horizon, in which case the caller should
// leave the cooldown duration to the conductor's credential backoff.
func DevinQuotaRetryAfter(resetAtSignal string, now time.Time) *time.Duration {
	resetAt, errParse := time.Parse(time.RFC3339, strings.TrimSpace(resetAtSignal))
	if errParse != nil {
		return nil
	}
	delay := resetAt.Sub(now)
	if delay <= 0 || delay > devinQuotaResetMaxDelay {
		return nil
	}
	return &delay
}
