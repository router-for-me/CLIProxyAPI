// Package runtimeconfig exposes the transaction-aware PG operations over
// runtime_config / config_revisions / config_imports that the dashboard's
// optimistic-concurrency contract relies on. It depends on both the store
// and the configsnapshot packages, so those two must never depend on each
// other (a cycle would be impossible here).
package runtimeconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/configsnapshot"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// Store is the handle the caller passes in. Usually built via New; a test
// double can satisfy it by setting the fields directly.
type Store struct {
	DB             *sql.DB
	RevisionsTable string
	ImportsTable   string
	RuntimeTable   string
}

// New builds a Store from a live PostgresStore.
func New(pg *store.PostgresStore) *Store {
	if pg == nil {
		return nil
	}
	return &Store{
		DB:             pg.DB(),
		RevisionsTable: pg.ConfigRevisionsTable(),
		ImportsTable:   pg.ConfigImportsTable(),
		RuntimeTable:   pg.RuntimeConfigTable(),
	}
}

// LoadRuntimeConfig reads the active runtime_config singleton and returns
// the parsed Snapshot. Absent row yields a fresh NewEmpty snapshot with
// Planned=true so callers can distinguish "not yet imported" from
// "imported and empty".
func (s *Store) LoadRuntimeConfig(ctx context.Context) (*configsnapshot.Snapshot, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("runtimeconfig: store not initialized")
	}
	row := s.DB.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT settings, extra, revision, updated_at, updated_by, updated_source
		 FROM %s WHERE id = 1`, s.RuntimeTable,
	))
	var (
		settingsRaw []byte
		extraRaw    []byte
		revision    int64
		updatedAt   sql.NullTime
		updatedBy   sql.NullString
		updatedSrc  sql.NullString
	)
	if err := row.Scan(&settingsRaw, &extraRaw, &revision, &updatedAt, &updatedBy, &updatedSrc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			snap := configsnapshot.NewEmpty()
			return &snap, nil
		}
		return nil, fmt.Errorf("runtimeconfig: load runtime_config: %w", err)
	}
	snap := configsnapshot.NewEmpty()
	if err := json.Unmarshal(settingsRaw, &snap.Settings); err != nil {
		return nil, fmt.Errorf("runtimeconfig: decode settings: %w", err)
	}
	if err := json.Unmarshal(extraRaw, &snap.Extra); err != nil {
		return nil, fmt.Errorf("runtimeconfig: decode extra: %w", err)
	}
	snap.Revision = revision
	if updatedAt.Valid {
		snap.UpdatedAt = updatedAt.Time
	}
	if updatedBy.Valid {
		snap.UpdatedBy = updatedBy.String
	}
	if updatedSrc.Valid {
		snap.UpdatedSource = updatedSrc.String
	}
	return &snap, nil
}

// RollbackRuntimeConfig rebuilds the active runtime_config row from a prior
// config_revisions row. The caller supplies the target revision and an
// optional human-readable reason recorded in the audit trail.
func (s *Store) RollbackRuntimeConfig(ctx context.Context, targetRevision int64, reason string) error {
	if s == nil || s.DB == nil {
		return fmt.Errorf("runtimeconfig: store not initialized")
	}
	if targetRevision <= 0 {
		return fmt.Errorf("runtimeconfig: rollback target must be positive, got %d", targetRevision)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("runtimeconfig: begin rollback tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	var settingsJSON []byte
	err = tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT settings FROM %s WHERE revision = $1`, s.RevisionsTable,
	), targetRevision).Scan(&settingsJSON)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("runtimeconfig: rollback target revision %d not found", targetRevision)
		}
		return fmt.Errorf("runtimeconfig: rollback scan: %w", err)
	}
	var current int64
	var hasActive bool
	err = tx.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT revision FROM %s WHERE id = 1 FOR UPDATE`, s.RuntimeTable,
	)).Scan(&current)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		hasActive = false
	case err != nil:
		return fmt.Errorf("runtimeconfig: rollback lock: %w", err)
	default:
		hasActive = true
	}
	newRevision := current + 1
	reasonText := strings.TrimSpace(reason)
	if reasonText == "" {
		reasonText = fmt.Sprintf("rollback to revision %d", targetRevision)
	} else {
		reasonText = fmt.Sprintf("rollback to revision %d: %s", targetRevision, reasonText)
	}
	if hasActive {
		_, err = tx.ExecContext(ctx, fmt.Sprintf(
			`UPDATE %s SET settings = $1::jsonb, revision = $2, updated_source = 'rollback'
			 WHERE id = 1`, s.RuntimeTable,
		), settingsJSON, newRevision)
		if err != nil {
			return fmt.Errorf("runtimeconfig: rollback update active: %w", err)
		}
	} else {
		_, err = tx.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (id, settings, extra, revision, updated_at, updated_by, updated_source)
			 VALUES (1, $1::jsonb, '{}'::jsonb, $2, NOW(), '', 'rollback')`, s.RuntimeTable,
		), settingsJSON, newRevision)
		if err != nil {
			return fmt.Errorf("runtimeconfig: rollback insert active: %w", err)
		}
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (revision, settings, resource_snapshot, checksum, created_at, created_by, reason)
		 VALUES ($1, $2::jsonb, '{}'::jsonb, '', NOW(), '', $3)`, s.RevisionsTable,
	), newRevision, settingsJSON, reasonText)
	if err != nil {
		return fmt.Errorf("runtimeconfig: rollback insert revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("runtimeconfig: rollback commit: %w", err)
	}
	committed = true
	return nil
}

// ListRevisions returns the most recent N config_revisions rows.
func (s *Store) ListRevisions(ctx context.Context, limit int) ([]RevisionRow, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("runtimeconfig: store not initialized")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(
		`SELECT revision, coalesce(reason,''), coalesce(created_by,''), created_at::text, coalesce(checksum,'')
		 FROM %s ORDER BY revision DESC LIMIT $1`, s.RevisionsTable,
	), limit)
	if err != nil {
		return nil, fmt.Errorf("runtimeconfig: list revisions: %w", err)
	}
	defer rows.Close()
	out := make([]RevisionRow, 0, limit)
	for rows.Next() {
		var r RevisionRow
		if err := rows.Scan(&r.Revision, &r.Reason, &r.Actor, &r.CreatedAt, &r.Checksum); err != nil {
			return nil, fmt.Errorf("runtimeconfig: scan revision: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListImports returns the most recent N config_imports rows.
func (s *Store) ListImports(ctx context.Context, limit int) ([]ImportRow, error) {
	if s == nil || s.DB == nil {
		return nil, fmt.Errorf("runtimeconfig: store not initialized")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, fmt.Sprintf(
		`SELECT id, status, coalesce(actor,''), mode, revision, created_at::text, coalesce(error_summary,'')
		 FROM %s ORDER BY id DESC LIMIT $1`, s.ImportsTable,
	), limit)
	if err != nil {
		return nil, fmt.Errorf("runtimeconfig: list imports: %w", err)
	}
	defer rows.Close()
	out := make([]ImportRow, 0, limit)
	for rows.Next() {
		var r ImportRow
		var rev sql.NullInt64
		if err := rows.Scan(&r.ID, &r.Status, &r.Actor, &r.Mode, &rev, &r.CreatedAt, &r.Error); err != nil {
			return nil, fmt.Errorf("runtimeconfig: scan import: %w", err)
		}
		if rev.Valid {
			v := rev.Int64
			r.Revision = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ActiveRevision returns the active runtime_config revision, or 0 when the
// singleton has not been imported yet.
func (s *Store) ActiveRevision(ctx context.Context) (int64, error) {
	if s == nil || s.DB == nil {
		return 0, fmt.Errorf("runtimeconfig: store not initialized")
	}
	var rev sql.NullInt64
	err := s.DB.QueryRowContext(ctx, fmt.Sprintf(
		`SELECT revision FROM %s WHERE id = 1`, s.RuntimeTable,
	)).Scan(&rev)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("runtimeconfig: active revision: %w", err)
	}
	if !rev.Valid {
		return 0, nil
	}
	return rev.Int64, nil
}

// RevisionRow is the dashboard-facing config_revisions shape.
type RevisionRow struct {
	Revision  int64  `json:"revision"`
	Reason    string `json:"reason"`
	Actor     string `json:"actor"`
	CreatedAt string `json:"created_at"`
	Checksum  string `json:"checksum,omitempty"`
}

// ImportRow is the dashboard-facing config_imports shape.
type ImportRow struct {
	ID        int64  `json:"id"`
	Status    string `json:"status"`
	Actor     string `json:"actor"`
	Mode      string `json:"mode"`
	Revision  *int64 `json:"revision,omitempty"`
	CreatedAt string `json:"created_at"`
	Error     string `json:"error,omitempty"`
}
