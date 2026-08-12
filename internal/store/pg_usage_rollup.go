package store

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
)

// maxRollupBackfillDays caps how many days BackfillRollup scans per invocation
// so a first-boot backfill over years of usage_events can never run unbounded.
// Each day is an indexed requested_at range scan; the bound is a safety valve.
const maxRollupBackfillDays = 366

// rollupFoldSQL is the shared INSERT ... SELECT fold used by RunRollup. It
// reads the day's usage_events rows, aggregates them per (user, api_key, model,
// provider, source) bucket, and writes the result into usage_stat_day.
//
// Idempotency contract: counters are overwritten (SET = EXCLUDED.x), never added
// (+=). usage_events is append-only — rows are never mutated or backdated — so
// re-running the fold over the same day re-reads the SAME rows and the DO UPDATE
// unconditionally replaces each bucket's totals. The result is identical to the
// first run (no double-counting). A daily driver can therefore re-fold "today"
// safely whenever new events flush without inflating totals.
//
// The fold's WHERE window uses $1 and $1 + '1 day' as half-open [day, day+1)
// bounds on the requested_at index. Bucket columns are COALESCE'd to ” so empty
// values collide with the PK rather than creating NULL-vs-” duplicates.
const rollupFoldSQL = `
INSERT INTO %s (stat_day, user_id, api_key_id, model, provider, source,
                request_count, fail_count, input_tokens, output_tokens, tot_tokens, cost_usd)
SELECT DATE_TRUNC('day', $1::timestamptz)::date,
       COALESCE(e.user_id, ''), COALESCE(e.api_key_id, ''), COALESCE(e.model, ''),
       COALESCE(e.provider, ''), COALESCE(e.source, ''),
       COUNT(*),
       COUNT(*) FILTER (WHERE e.failed),
       COALESCE(SUM(e.input_tokens), 0),
       COALESCE(SUM(e.output_tokens), 0),
       COALESCE(SUM(e.total_tokens), 0),
       COALESCE(SUM(e.cost_usd), 0)::numeric(12,6)
FROM %s e
WHERE e.requested_at >= $1
  AND e.requested_at <  $1 + interval '1 day'
GROUP BY 2, 3, 4, 5, 6
ON CONFLICT (stat_day, user_id, api_key_id, model, provider, source)
DO UPDATE SET
    request_count = EXCLUDED.request_count,
    fail_count    = EXCLUDED.fail_count,
    input_tokens  = EXCLUDED.input_tokens,
    output_tokens = EXCLUDED.output_tokens,
    tot_tokens    = EXCLUDED.tot_tokens,
    cost_usd      = EXCLUDED.cost_usd
`

// RunRollup folds all usage_events rows belonging to the calendar day of
// dayStart into usage_stat_day. The fold is idempotent (see rollupFoldSQL):
// re-running it for the same day yields the same totals because the append-only
// source is re-read wholesale and each bucket is overwritten. dayStart may carry
// a time-of-day; only its calendar day is used.
func (s *UsageStore) RunRollup(ctx context.Context, dayStart time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	// Normalize to the UTC calendar day so repeated calls with different
	// time-of-day values still select the same [day, day+1) window.
	dayStart = dayStart.UTC().Truncate(24 * time.Hour)
	query := fmt.Sprintf(rollupFoldSQL, s.rollupTable, s.eventsTable)
	if _, err := s.db.ExecContext(ctx, query, dayStart); err != nil {
		return fmt.Errorf("postgres store: rollup %s: %w", dayStart.Format("2006-01-02"), err)
	}
	return nil
}

// BackfillRollup folds every day strictly before endExclusive that is not today.
// It walks day-by-day from the cutoff (endExclusive minus backfillDays, clamped
// to the horizon the operator requested) down to endExclusive-1, calling
// RunRollup per day. Callers pass the number of prior days to fold; a caller of
// 7 folds the previous week. The fold is idempotent, so backfilling a range a
// second time is harmless.
//
// endExclusive is interpreted in UTC: the last folded bucket is the UTC day
// preceding this timestamp. days <= 0 is a no-op.
func (s *UsageStore) BackfillRollup(ctx context.Context, endExclusive time.Time, days int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: usage store not initialized")
	}
	if days <= 0 {
		return nil
	}
	if days > maxRollupBackfillDays {
		days = maxRollupBackfillDays
	}
	endExclusive = endExclusive.UTC().Truncate(24 * time.Hour)
	start := endExclusive.AddDate(0, 0, -days)
	for d := start; d.Before(endExclusive) && !d.IsZero(); d = d.AddDate(0, 0, 1) {
		if err := s.RunRollup(ctx, d); err != nil {
			return fmt.Errorf("postgres store: backfill rollup %s: %w", d.Format("2006-01-02"), err)
		}
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("postgres store: backfill rollup: %w", err)
	}
	return nil
}

// RunRollupLoop runs the daily rollup driver in the foreground until ctx is
// canceled. It first backfills backfillDays prior days, then folds "today" once
// per 24 hours on a ticker. The ticker starts immediately (first fold runs
// right after backfill) so a fresh day is captured at boot without waiting a
// full interval. Scheduling is intentionally simple: a single 24h ticker.
// Suitable for launching as a background goroutine from main.go.
func (s *UsageStore) RunRollupLoop(ctx context.Context, backfillDays int) {
	if s == nil || s.db == nil {
		log.Warn("postgres store: rollup driver skipped (usage store not initialized)")
		return
	}
	now := time.Now().UTC()
	if backfillDays < 0 {
		backfillDays = 0
	}
	if err := s.BackfillRollup(ctx, now, backfillDays); err != nil {
		log.WithError(err).Warn("postgres store: rollup backfill failed")
	}
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		if err := s.RunRollup(ctx, now); err != nil {
			log.WithError(err).Warnf("postgres store: rollup failed for %s", now.Format("2006-01-02"))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			now = time.Now().UTC()
		}
	}
}
