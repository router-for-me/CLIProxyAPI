package session

import "context"

// Store abstracts persistence of Session state across restarts and requests.
// Implementations must be safe for concurrent access.
type Store interface {
	// Get retrieves a session by ID.
	// Returns nil if the session does not exist or has expired.
	Get(ctx context.Context, id string) (*Session, error)

	// Save persists a session, creating it if new or updating if existing.
	// Returns the session ID on success.
	Save(ctx context.Context, session *Session) (string, error)

	// Delete removes a session by ID.
	// Returns nil if the session does not exist (idempotent).
	Delete(ctx context.Context, id string) error

	// List returns all active (non-expired) sessions.
	// Optional filters can be applied via query parameters in metadata.
	List(ctx context.Context) ([]*Session, error)

	// Cleanup removes all expired sessions.
	// Returns the count of sessions purged.
	Cleanup(ctx context.Context) (int, error)
}
