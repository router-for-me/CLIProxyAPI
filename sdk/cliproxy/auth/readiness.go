package auth

import "time"

// IsSelectableCredential reports whether a credential could be selected for a
// model-agnostic request under the same availability rules as the scheduler.
// Parent lifecycle StatusError alone does not make a credential unusable when
// updateAggregatedAvailability left Unavailable false (another model remains
// selectable).
func IsSelectableCredential(a *Auth) bool {
	if a == nil {
		return false
	}
	blocked, _, _ := isAuthBlockedForModel(a, "", time.Now())
	return !blocked
}

// HasPositiveCredentialWeight reports whether weighted-round-robin routing
// would admit this credential (weight > 0). Unset weight defaults to 1.
func HasPositiveCredentialWeight(a *Auth) bool {
	return authWeight(a) > 0
}

// HasRegisteredExecutor reports whether the manager has an executor for the
// same provider key used during selection. Entries without one are skipped
// (mixed) or fail with executor_not_found (legacy pick).
func HasRegisteredExecutor(m *Manager, a *Auth) bool {
	if m == nil || a == nil {
		return false
	}
	_, ok := m.Executor(executorKeyFromAuth(a))
	return ok
}
