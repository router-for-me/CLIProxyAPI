package auth

import (
	"reflect"
	"time"
)

// capQuotaRecoveryBindingsToRetainedWindow follows an intentional force-only
// clear. The scheduler has already chosen the retained quota deadline; request
// causes cannot outlive that truncated layer or revive on a later extension.
func capQuotaRecoveryBindingsToRetainedWindow(q *QuotaState, now time.Time) bool {
	if q == nil || len(q.recoveryBindings) == 0 {
		return false
	}
	var kept []quotaRecoveryBinding
	if q.Exceeded && q.NextRecoverAt.After(now) {
		kept = mergeQuotaRecoveryBindings(now, q.recoveryBindings)
		for i := range kept {
			if kept[i].Until.After(q.NextRecoverAt) {
				kept[i].Until = q.NextRecoverAt
			}
		}
	}
	if reflect.DeepEqual(q.recoveryBindings, kept) {
		return false
	}
	q.recoveryBindings = kept
	return true
}
