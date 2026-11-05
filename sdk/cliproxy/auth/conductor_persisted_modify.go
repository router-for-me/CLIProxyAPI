package auth

import "context"

type persistedAuthMutationKey struct{}

func requiresPersistedAuthMutation(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	required, _ := ctx.Value(persistedAuthMutationKey{}).(bool)
	return required
}

// ModifyPersisted is Modify with a strict storage acknowledgement: persistence
// failures or skipped persistence prevent publication of the runtime mutation.
// Callers can safely acknowledge durable metadata ownership only after success.
func (m *Manager) ModifyPersisted(ctx context.Context, id string, expectedEpoch uint64, edit func(*Auth) error) (*Auth, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	return m.Modify(context.WithValue(ctx, persistedAuthMutationKey{}, true), id, expectedEpoch, edit)
}
