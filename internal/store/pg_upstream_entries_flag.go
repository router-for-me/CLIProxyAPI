package store

import (
	"context"
	"database/sql"
	"fmt"
)

// SetEntryAutoDisabled persists the server-side auto-disable sink's decision:
// it flips one upstream_provider_api_key_entries row to disabled=true +
// auto_disabled=true with the matched error code as the reason and NOW() as
// the timestamp, then reports whether a row actually changed.
//
// It is idempotent and never clobbers manual state: the UPDATE only targets
// rows that are not already disabled (which covers both an entry already
// auto-disabled and an operator's manual `disabled` toggle), re-firing on
// either is a (false, nil) no-op. A nonexistent entry id also reports
// (false, nil).
//
// Entry IDs are globally unique across upstream providers, so the entryID
// alone keys the UPDATE — the providerID is deliberately dropped from the
// signature (the auth AutoDisableEvent carries Provider only for logging).
func (s *pgUpstreamProviderStore) SetEntryAutoDisabled(ctx context.Context, entryID int64, code string) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("postgres store: upstream providers store not initialized")
	}
	// Bound the reason defensively in Go; the SQL left() truncation is the
	// authoritative guard for any other caller shape.
	if len(code) > 256 {
		code = code[:256]
	}
	var id int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		UPDATE %s e
		   SET disabled = true, auto_disabled = true, auto_disabled_at = NOW(),
		       auto_disabled_reason = left($2, 256)
		 WHERE e.id = $1
		   AND NOT e.disabled
	 RETURNING e.id
	`, s.entries), entryID, code).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			// No row matched: the entry does not exist, is already auto-disabled
			// (idempotent no-op), or is manually disabled (never clobbered).
			return false, nil
		}
		return false, fmt.Errorf("postgres store: set upstream provider api key entry auto-disabled %d: %w", entryID, err)
	}
	return true, nil
}
