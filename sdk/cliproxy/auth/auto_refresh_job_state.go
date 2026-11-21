package auth

// Refresh job fields and pending-marker ownership are inspected under manager.mu.
func refreshJobMatchesAuth(auth *Auth, job *authRefreshJob) bool {
	return auth != nil && job != nil && auth.ID == job.id &&
		auth.RegistrationEpoch == job.registrationEpoch && auth.CredentialVersion == job.credentialVersion
}

func refreshJobOwnsPendingMarker(auth *Auth, job *authRefreshJob) bool {
	return refreshJobMatchesAuth(auth, job) && job.markerOwned && auth.NextRefreshAfter.Equal(job.pendingUntil)
}

// retireRefreshJobMarkerLocked runs in the same critical section that commits
// a refresh outcome. It reports whether the old timestamp was a job marker,
// without clearing or replacing the outcome being installed by the caller.
func (m *Manager) retireRefreshJobMarkerLocked(auth *Auth) bool {
	if auth == nil {
		return false
	}
	job := m.refreshJobs[auth.ID]
	if !refreshJobMatchesAuth(auth, job) {
		return false
	}
	owned := refreshJobOwnsPendingMarker(auth, job)
	job.markerOwned = false
	return owned
}

// refreshOutcomeCurrentLocked excludes an owned pending marker from refresh
// result and save merges. A job may appear during provider I/O or Store.Save.
// All other concurrent fields retain normal merge behavior. The manager record
// is untouched.
func (m *Manager) refreshOutcomeCurrentLocked(before, current *Auth) *Auth {
	if before == nil || current == nil || !refreshJobOwnsPendingMarker(current, m.refreshJobs[current.ID]) {
		return current
	}
	masked := current.Clone()
	masked.NextRefreshAfter = before.NextRefreshAfter
	return masked
}
