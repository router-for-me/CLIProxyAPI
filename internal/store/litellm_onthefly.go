package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// On-the-fly validation outcomes recorded in litellm_onthefly_log.
const (
	OnTheFlyOutcomeSynced    = "synced"
	OnTheFlyOutcomeUnmatched = "unmatched"
	OnTheFlyOutcomeInvalid   = "invalid"
	OnTheFlyOutcomeError     = "error"
)

// onTheFlyLogRetention bounds how long on-the-fly validation rows are kept.
const onTheFlyLogRetention = 30 * 24 * time.Hour

// OnTheFlyLogEvent mirrors a row in litellm_onthefly_log. The plaintext key is
// never recorded — only a non-secret prefix and the resolved token hash.
type OnTheFlyLogEvent struct {
	ID           int64     `json:"id"`
	OccurredAt   time.Time `json:"occurred_at"`
	RequestID    string    `json:"request_id,omitempty"`
	Outcome      string    `json:"outcome"`
	KeyPrefix    string    `json:"key_prefix,omitempty"`
	KeyID        string    `json:"key_id,omitempty"`
	UserID       string    `json:"user_id,omitempty"`
	Source       string    `json:"source,omitempty"`
	LatencyMs    int64     `json:"latency_ms"`
	ErrorMessage string    `json:"error_message,omitempty"`
}

// OnTheFlyLogFilter captures the optional filters accepted by ListOnTheFlyLog.
type OnTheFlyLogFilter struct {
	Outcome   string
	KeyPrefix string
}

// OnTheFlyLogStore provides insert/list/purge for litellm_onthefly_log.
type OnTheFlyLogStore struct {
	db    *sql.DB
	table string
}

// NewOnTheFlyLogStore reuses the PostgresStore connection and table name.
// Returns nil when the parent store is nil so callers feature-detect with one
// nil check.
func NewOnTheFlyLogStore(parent *PostgresStore) *OnTheFlyLogStore {
	if parent == nil {
		return nil
	}
	return &OnTheFlyLogStore{db: parent.DB(), table: parent.LiteLLMOnTheFlyLogTable()}
}

// RecordOnTheFly persists one validation outcome. Best-effort by design: the
// caller logs (does not fail auth on) any error.
func (s *OnTheFlyLogStore) RecordOnTheFly(ctx context.Context, e OnTheFlyLogEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: on-the-fly log store not initialized")
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (occurred_at, request_id, outcome, key_prefix, key_id, user_id, source, latency_ms, error_message)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, s.table),
		e.OccurredAt, e.RequestID, e.Outcome, e.KeyPrefix, e.KeyID, e.UserID, e.Source,
		e.LatencyMs, truncateAuditBody(e.ErrorMessage),
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert litellm_onthefly_log: %w", err)
	}
	return nil
}

// ListOnTheFlyLog returns up to limit rows (newest first) matching the filter.
func (s *OnTheFlyLogStore) ListOnTheFlyLog(ctx context.Context, f OnTheFlyLogFilter, limit int) ([]*OnTheFlyLogEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: on-the-fly log store not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	var (
		where []string
		args  []any
	)
	addFilter := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if strings.TrimSpace(f.Outcome) != "" {
		addFilter("outcome = $%d", strings.TrimSpace(f.Outcome))
	}
	if strings.TrimSpace(f.KeyPrefix) != "" {
		addFilter("key_prefix = $%d", strings.TrimSpace(f.KeyPrefix))
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}
	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, limit)
	query := fmt.Sprintf(`
		SELECT id, occurred_at, request_id, outcome, key_prefix, key_id, user_id, source, latency_ms, error_message
		FROM %s%s
		ORDER BY occurred_at DESC
		LIMIT $%d
	`, s.table, whereClause, len(listArgs))

	rows, err := s.db.QueryContext(ctx, query, listArgs...)
	if err != nil {
		return nil, fmt.Errorf("postgres store: list litellm_onthefly_log: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close on-the-fly log rows failed")
		}
	}()
	entries := make([]*OnTheFlyLogEvent, 0, limit)
	for rows.Next() {
		var e OnTheFlyLogEvent
		if err := rows.Scan(&e.ID, &e.OccurredAt, &e.RequestID, &e.Outcome, &e.KeyPrefix, &e.KeyID,
			&e.UserID, &e.Source, &e.LatencyMs, &e.ErrorMessage); err != nil {
			return nil, fmt.Errorf("postgres store: scan litellm_onthefly_log row: %w", err)
		}
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate litellm_onthefly_log: %w", err)
	}
	return entries, nil
}

// PurgeOnTheFlyLog removes all rows. Returns the number deleted.
func (s *OnTheFlyLogStore) PurgeOnTheFlyLog(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: on-the-fly log store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, s.table))
	if err != nil {
		return 0, fmt.Errorf("postgres store: purge litellm_onthefly_log: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeOnTheFlyLogBefore deletes rows older than cutoff. Returns rows deleted.
func (s *OnTheFlyLogStore) PurgeOnTheFlyLogBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: on-the-fly log store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE occurred_at < $1`, s.table), cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres store: purge litellm_onthefly_log before: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// StartOnTheFlyLogSweep launches the 30-day retention sweep. No-op when nil.
func StartOnTheFlyLogSweep(store *OnTheFlyLogStore) {
	if store == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().Add(-onTheFlyLogRetention)
			deleted, err := store.PurgeOnTheFlyLogBefore(context.Background(), cutoff)
			if err != nil {
				log.WithError(err).Warn("postgres store: on-the-fly log sweep failed")
				continue
			}
			if deleted > 0 {
				log.WithField("deleted", deleted).Debug("on-the-fly log sweep purged rows")
			}
		}
	}()
}
