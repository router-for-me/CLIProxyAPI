package auth

import "context"

// ResetQuotaIfUnchanged clears local quota/cooldown state only if the credential
// still matches the observed snapshot. A newer mutation, re-registration,
// disablement, or remote control-plane ownership leaves the state unchanged.
func (m *Manager) ResetQuotaIfUnchanged(ctx context.Context, expected *Auth) (*Auth, []string, error) {
	if expected == nil {
		return nil, nil, nil
	}
	if errContext := ctx.Err(); errContext != nil {
		return nil, nil, errContext
	}
	return m.resetQuota(ctx, expected.ID, expected)
}
