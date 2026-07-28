package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Sync log refresh-trigger labels. The trigger records what caused the auth
// manager to refresh an upstream OAuth/auth credential. Mirrored on the
// dashboard so an operator can distinguish a routine background refresh from a
// reactive one driven by a failed request.
const (
	SyncLogTriggerAuto              = "auto"
	SyncLogTriggerOnDemand          = "on_demand"
	SyncLogTriggerUnauthorizedRetry = "unauthorized_retry"
)

// syncLogRetention bounds how long upstream sync log rows are kept. The sweep
// goroutine (StartSyncLogSweep) deletes rows older than this window hourly.
const syncLogRetention = 30 * 24 * time.Hour

// SyncLogEvent mirrors a row in the upstream_sync_log table. It records the
// outcome of a single upstream OAuth/auth token refresh performed by the auth
// manager: which auth was refreshed, which provider owns it, what triggered the
// refresh, whether it succeeded, the error (if any) and how long it took.
//
// The ErrorMessage column is AES-GCM-sealed at rest when
// PGSTORE_ENCRYPTION_KEY is configured (see Sealer); otherwise it is persisted
// in plaintext (legacy rows remain readable, forward-encrypting).
type SyncLogEvent struct {
	ID           int64     `json:"id"`
	AuthID       string    `json:"auth_id"`
	Provider     string    `json:"provider"`
	Trigger      string    `json:"trigger"`
	Success      bool      `json:"success"`
	ErrorMessage string    `json:"error_message,omitempty"`
	DurationMs   int64     `json:"duration_ms"`
	OccurredAt   time.Time `json:"occurred_at"`
}

// SyncLogFilter captures the optional filter dimensions accepted by
// ListSyncLogPaged. Zero values (and empty strings / nil pointers) are ignored.
type SyncLogFilter struct {
	Provider string
	Trigger  string
	// Success filters by outcome. nil = both success and failure rows.
	Success *bool
	From    time.Time
	To      time.Time
}

// SyncLogStore provides insert/list/purge for the upstream_sync_log table. It
// is backed by the same *sql.DB connection as PostgresStore and reuses the
// shared Sealer so error messages are sealed at rest when configured.
type SyncLogStore struct {
	db     *sql.DB
	table  string
	sealer *Sealer
}

// NewSyncLogStore builds a SyncLogStore that reuses the PostgresStore
// connection and table name. Returns nil if the parent store is nil so callers
// can feature-detect the absence of the PG backend with a nil check (mirrors
// NewManagementTokenStore).
func NewSyncLogStore(parent *PostgresStore) *SyncLogStore {
	if parent == nil {
		return nil
	}
	sealer, err := NewSealer(parent.cfg.UsageEncryptionKey)
	if err != nil {
		log.WithError(err).Warn("postgres store: upstream sync log encryption disabled due to key error")
		sealer = nil
	}
	return &SyncLogStore{
		db:     parent.DB(),
		table:  parent.UpstreamSyncLogTable(),
		sealer: sealer,
	}
}

// SetSealer overrides the in-memory sealer. Used by tests that construct a
// SyncLogStore directly without going through NewPostgresStore.
func (s *SyncLogStore) SetSealer(sealer *Sealer) {
	if s == nil {
		return
	}
	s.sealer = sealer
}

// InsertSyncLog persists a single sync-log row. ErrorMessage is sealed at rest
// via the Sealer when PGSTORE_ENCRYPTION_KEY is configured and truncated to
// maxAuditBodyBytes before sealing to bound row size. The call is
// fire-and-forget from the refresh sink; errors are surfaced to the caller so
// the sink can log them without blocking the refresh pipeline.
func (s *SyncLogStore) InsertSyncLog(ctx context.Context, e SyncLogEvent) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: sync log store not initialized")
	}
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	errMsg := truncateAuditBody(e.ErrorMessage)
	if s.sealer != nil && s.sealer.Enabled() && errMsg != "" {
		if sealed, errSeal := s.sealer.Seal(errMsg); errSeal == nil {
			errMsg = sealed
		} else {
			log.WithError(errSeal).Debug("postgres store: seal sync log error_message failed; storing plaintext")
		}
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (auth_id, provider, trigger, success, error_message, duration_ms, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, s.table),
		e.AuthID, e.Provider, e.Trigger, e.Success,
		nullableString(errMsg), e.DurationMs, e.OccurredAt,
	)
	if err != nil {
		return fmt.Errorf("postgres store: insert upstream sync log: %w", err)
	}
	return nil
}

// ListSyncLogPaged returns a page of sync-log entries plus the total row count
// matching the filter. ErrorMessage is unsealed (decrypted) when a Sealer is
// configured; legacy plaintext rows pass through unchanged.
func (s *SyncLogStore) ListSyncLogPaged(ctx context.Context, f SyncLogFilter, page, pageSize int) ([]*SyncLogEvent, int64, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("postgres store: sync log store not initialized")
	}
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}

	var (
		where []string
		args  []any
	)
	addFilter := func(clause string, val any) {
		args = append(args, val)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if strings.TrimSpace(f.Provider) != "" {
		addFilter("provider = $%d", strings.TrimSpace(f.Provider))
	}
	if strings.TrimSpace(f.Trigger) != "" {
		addFilter("trigger = $%d", strings.TrimSpace(f.Trigger))
	}
	if f.Success != nil {
		addFilter("success = $%d", *f.Success)
	}
	if !f.From.IsZero() {
		addFilter("occurred_at >= $%d", f.From)
	}
	if !f.To.IsZero() {
		addFilter("occurred_at <= $%d", f.To)
	}
	whereClause := ""
	if len(where) > 0 {
		whereClause = " WHERE " + strings.Join(where, " AND ")
	}

	var total int64
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM %s%s`, s.table, whereClause)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres store: count upstream sync log (paged): %w", err)
	}

	listArgs := append([]any{}, args...)
	listArgs = append(listArgs, pageSize, (page-1)*pageSize)
	listQuery := fmt.Sprintf(`
		SELECT id, auth_id, provider, trigger, success, error_message, duration_ms, occurred_at
		FROM %s%s
		ORDER BY occurred_at DESC
		LIMIT $%d OFFSET $%d
	`, s.table, whereClause, len(listArgs)-1, len(listArgs))

	rows, err := s.db.QueryContext(ctx, listQuery, listArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres store: list upstream sync log (paged): %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close sync log rows failed")
		}
	}()

	entries := make([]*SyncLogEvent, 0, pageSize)
	for rows.Next() {
		var (
			e       SyncLogEvent
			errMsg  sql.NullString
			trigger string
		)
		if err := rows.Scan(&e.ID, &e.AuthID, &e.Provider, &trigger, &e.Success, &errMsg, &e.DurationMs, &e.OccurredAt); err != nil {
			return nil, 0, fmt.Errorf("postgres store: scan upstream sync log row (paged): %w", err)
		}
		e.Trigger = trigger
		e.ErrorMessage = unsealAuditBody(s.sealer, errMsg.String)
		entries = append(entries, &e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres store: iterate upstream sync log (paged): %w", err)
	}
	return entries, total, nil
}

// GetSyncLog returns a single sync-log entry by ID. ErrorMessage is unsealed.
func (s *SyncLogStore) GetSyncLog(ctx context.Context, id int64) (*SyncLogEvent, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: sync log store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, auth_id, provider, trigger, success, error_message, duration_ms, occurred_at
		FROM %s WHERE id = $1
	`, s.table), id)
	var (
		e      SyncLogEvent
		errMsg sql.NullString
	)
	if err := row.Scan(&e.ID, &e.AuthID, &e.Provider, &e.Trigger, &e.Success, &errMsg, &e.DurationMs, &e.OccurredAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSyncLogNotFound
		}
		return nil, fmt.Errorf("postgres store: get upstream sync log: %w", err)
	}
	e.ErrorMessage = unsealAuditBody(s.sealer, errMsg.String)
	return &e, nil
}

// ClearSyncLog removes all sync-log rows. Used by the operator-facing
// DELETE /v0/management/upstream-sync-log endpoint. Returns the number of rows
// deleted. Auto-sweep (PurgeSyncLogBefore) handles routine retention; this is
// an explicit manual clear.
func (s *SyncLogStore) ClearSyncLog(ctx context.Context) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: sync log store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s`, s.table))
	if err != nil {
		return 0, fmt.Errorf("postgres store: clear upstream sync log: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PurgeSyncLogBefore deletes rows older than the cutoff, returning the number
// of rows deleted. Invoked by the StartSyncLogSweep goroutine.
func (s *SyncLogStore) PurgeSyncLogBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: sync log store not initialized")
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE occurred_at < $1`, s.table), cutoff)
	if err != nil {
		return 0, fmt.Errorf("postgres store: purge upstream sync log: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ErrSyncLogNotFound is returned when no sync-log row matches the supplied ID.
var ErrSyncLogNotFound = errors.New("postgres store: upstream sync log not found")

// SyncLogDistinctProviders returns the distinct provider values currently
// present in the sync log, ordered alphabetically. Used by the dashboard to
// populate the provider filter dropdown. Returns an empty slice when the store
// is nil/PG is not configured.
func (s *SyncLogStore) SyncLogDistinctProviders(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: sync log store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT DISTINCT provider FROM %s ORDER BY provider ASC
	`, s.table))
	if err != nil {
		return nil, fmt.Errorf("postgres store: distinct upstream sync log providers: %w", err)
	}
	defer func() {
		if errClose := rows.Close(); errClose != nil {
			log.WithError(errClose).Debug("postgres store: close distinct providers rows failed")
		}
	}()
	var providers []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("postgres store: scan distinct provider: %w", err)
		}
		providers = append(providers, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres store: iterate distinct providers: %w", err)
	}
	return providers, nil
}

// StartSyncLogSweep launches a background goroutine that periodically purges
// sync-log rows older than the 30-day retention window. Call once at startup.
// No-op (returns immediately) when the store is nil so callers can unconditionally
// invoke it from the management handler constructor.
func StartSyncLogSweep(store *SyncLogStore) {
	if store == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			cutoff := time.Now().Add(-syncLogRetention)
			deleted, err := store.PurgeSyncLogBefore(context.Background(), cutoff)
			if err != nil {
				log.WithError(err).Warn("postgres store: upstream sync log sweep failed")
				continue
			}
			if deleted > 0 {
				log.WithField("deleted", deleted).WithField("cutoff", cutoff).Debug("upstream sync log sweep purged rows")
			}
		}
	}()
}
