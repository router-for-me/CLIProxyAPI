package auth

import "context"

// Store abstracts persistence of Auth state across restarts.
type Store interface {
	// List returns all auth records stored in the backend.
	List(ctx context.Context) ([]*Auth, error)
	// Save persists the provided auth record, replacing any existing one with same ID.
	// It may enrich exported fields, but must not change ID, RegistrationEpoch or
	// Generation. The manager merges these changes even on non-fatal save errors.
	// Do not retain auth or mutate shared nested values in place: replace metadata
	// values, pointers, Storage and Runtime instead. Auth.Clone shares these objects.
	// Save may call manager read APIs, but must not synchronously mutate the
	// manager or call Load (the reload barrier remains held until publication).
	Save(ctx context.Context, auth *Auth) (string, error)
	// Delete removes the auth record identified by id.
	Delete(ctx context.Context, id string) error
}
