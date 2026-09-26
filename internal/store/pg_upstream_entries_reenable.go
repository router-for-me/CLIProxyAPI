package store

import (
	"context"
	"fmt"
)

// ReenableExpiredAutoDisabled is the auto-re-enable sweeper's persistence
// primitive (Fitur 2 re-enable side): one UPDATE (joined against the provider
// row) clears the auto-disabled runtime flags on every upstream_provider_api_key_entries
// row whose auto_disabled_at has passed the provider-configured cooldown:
//
//	auto_disabled=false, disabled=false, reason/at nulled
//
// It returns the number of rows re-enabled (sql.Result.RowsAffected()).
//
// Provider-level auto_disable_cooldown_seconds (nullable INTEGER, nil/0 = manual
// re-enable only) sits on the upstream_providers row; the WHERE requires it to
// be strictly positive so a NULL or 0 cooldown never triggers a sweep.
//
// The belt-and-suspenders tail guard `NOT (e.disabled AND NOT e.auto_disabled)`
// is deliberately DIFFERENT from Task 7's sink guard (`NOT e.disabled`): the
// sweeper's WHERE already requires `e.auto_disabled = true`, so that clause can
// only ever exclude a manual row (disabled=true, auto_disabled=false), which
// cannot match the UPDATE's other conditions anyway. It is retained verbatim
// from the design to make the "never clears manual state" invariant structurally
// explicit (D5) — it is NOT a `NOT e.disabled`, which would be a behavior change.
//
// Concurrency: this method is safe to run concurrently with SetEntryAutoDisabled.
// A re-enable that clears a row while the sink is firing a fresh auto-disable is
// acceptable last-writer-wins: the sink's UPDATE re-flips the row (it targets
// rows that are not already disabled), and the re-enable's clear followed by the
// sink's re-set converges on the sink's verdict. The only invariant that matters
// is that a manual `disabled` is never cleared — guaranteed by the WHERE (which
// acts only on auto_disabled=true rows) plus the belt-and-suspenders clause.
func (s *pgUpstreamProviderStore) ReenableExpiredAutoDisabled(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: upstream providers store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s e
		   SET auto_disabled = false, disabled = false, auto_disabled_reason = NULL, auto_disabled_at = NULL
		  FROM %s p
		 WHERE e.auto_disabled = true
		   AND e.provider_id = p.id
		   AND p.auto_disable_cooldown_seconds IS NOT NULL
		   AND p.auto_disable_cooldown_seconds > 0
		   AND e.auto_disabled_at <= NOW() - (p.auto_disable_cooldown_seconds * INTERVAL '1 second')
		   AND NOT (e.disabled AND NOT e.auto_disabled)
	`, s.entries, s.table))
	if err != nil {
		return 0, fmt.Errorf("postgres store: re-enable expired auto-disabled upstream provider api key entries: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("postgres store: rows affected for auto-re-enable sweep: %w", err)
	}
	return n, nil
}
