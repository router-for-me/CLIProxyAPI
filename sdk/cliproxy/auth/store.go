package auth

import "context"

// Store abstracts persistence of Auth state across restarts.
type Store interface {
	// List returns all auth records stored in the backend.
	List(ctx context.Context) ([]*Auth, error)
	// Save persists the provided auth record, replacing any existing one with same ID.
	Save(ctx context.Context, auth *Auth) (string, error)
	// Delete removes the auth record identified by id.
	Delete(ctx context.Context, id string) error
}

// PersistenceTargetResolver resolves the exact store identity that Save would
// use for an auth record without performing I/O. Stores with a configurable
// base directory should implement this so stale-target reconciliation compares
// complete paths rather than ambiguous basenames.
type PersistenceTargetResolver interface {
	ResolveAuthPersistenceTarget(auth *Auth) (string, error)
}
