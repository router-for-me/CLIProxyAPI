package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// RefreshAuthForObservation reuses the provider's credential refresh when an
// observation needs a valid access token. Neither failures nor successful token
// rotation recover, disable, or otherwise change routing/cooldown state.
// rotated is true only when this call rotated the supplied snapshot's credentials.
func (m *Manager) RefreshAuthForObservation(ctx context.Context, snapshot *Auth) (auth *Auth, rotated bool, err error) {
	if m == nil || snapshot == nil || snapshot.ID == "" {
		return nil, false, errors.New("observation credential is missing")
	}
	ctx = cliproxyexecutor.WithoutRequestProxyURL(ctx)
	lockValue, _ := m.refreshLocks.LoadOrStore(snapshot.ID, &authRefreshLock{})
	lock := lockValue.(*authRefreshLock)
	lock.mu.Lock()
	defer lock.mu.Unlock()

	m.mu.RLock()
	current := m.auths[snapshot.ID].Clone()
	var exec ProviderExecutor
	if current != nil {
		exec, _ = m.executorLocked(executorKeyFromAuth(current))
	}
	m.mu.RUnlock()
	if current == nil || current.RegistrationEpoch != snapshot.RegistrationEpoch {
		return nil, false, errors.New("observation credential registration changed")
	}
	if !observationCredentialScopeMatches(snapshot, current, false) {
		return nil, false, errors.New("observation credential scope changed")
	}
	if current.HasValidAccessToken(time.Now()) {
		return current, false, nil
	}
	if exec == nil {
		return nil, false, errors.New("observation credential executor is missing")
	}
	updated, errRefresh := exec.Refresh(ctx, current.Clone())
	if errRefresh != nil {
		return nil, false, errRefresh
	}
	if updated == nil {
		return nil, false, errors.New("observation credential refresh returned no credential")
	}
	if !observationCredentialScopeMatches(current, updated, true) {
		return nil, false, errors.New("observation credential refresh changed scope")
	}
	refreshed, errUpdate := m.updateInternal(ctx, current, updated, updateModeObservation)
	if errUpdate != nil {
		return nil, false, errUpdate
	}
	if refreshed == nil || !refreshed.HasValidAccessToken(time.Now()) {
		return nil, false, errors.New("observation credential refresh returned no valid access token")
	}
	rotated = current.CredentialVersion == snapshot.CredentialVersion &&
		!CredentialsChanged(current, snapshot) && !CredentialsChanged(refreshed, updated) &&
		CredentialsChanged(current, refreshed)
	return refreshed, rotated, nil
}

// Only the refresh result may resolve previously missing identity fields.
// A concurrent replacement must never inherit the old account's refreshed token.
func observationCredentialScopeMatches(base, candidate *Auth, allowIdentityResolution bool) bool {
	if base == nil || candidate == nil || base.ID != candidate.ID ||
		!strings.EqualFold(strings.TrimSpace(base.Provider), strings.TrimSpace(candidate.Provider)) ||
		base.AuthKind() != AuthKindOAuth || candidate.AuthKind() != AuthKindOAuth {
		return false
	}
	for _, key := range []string{"account_uuid", "organization_uuid"} {
		before, after := authMetadataString(base, key), authMetadataString(candidate, key)
		if before != after && (!allowIdentityResolution || before != "") {
			return false
		}
	}
	if authMetadataString(base, "account_uuid") == "" {
		before, after := authMetadataString(base, "email"), authMetadataString(candidate, "email")
		if !strings.EqualFold(before, after) && (!allowIdentityResolution || before != "") {
			return false
		}
	}
	return true
}
