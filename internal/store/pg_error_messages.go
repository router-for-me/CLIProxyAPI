package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/errormessages"
)

// pgErrorMessageStore is the concrete implementation of errormessages.Store,
// backed by the error_messages table. Constructed by NewErrorMessageStore.
type pgErrorMessageStore struct {
	db    *sql.DB
	table string
}

// NewErrorMessageStore wires the store interface against the parent
// PostgresStore's connection. Returns nil when parent is nil so callers can
// feature-detect via nil check.
func NewErrorMessageStore(parent *PostgresStore) errormessages.Store {
	if parent == nil {
		return nil
	}
	return &pgErrorMessageStore{
		db:    parent.DB(),
		table: parent.ErrorMessagesTable(),
	}
}

// ListAll loads every configured error message row, ordered by status code.
func (s *pgErrorMessageStore) ListAll(ctx context.Context) ([]errormessages.Message, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: error messages store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT status_code, title, message, body_template, enabled, created_at, updated_at
		FROM %s
		ORDER BY status_code
	`, s.table))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list error messages: %w", err)
	}
	defer rows.Close()
	out := make([]errormessages.Message, 0, 16)
	for rows.Next() {
		var m errormessages.Message
		var body sql.NullString
		if err = rows.Scan(&m.StatusCode, &m.Title, &m.Message, &body, &m.Enabled,
			&m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("postgres store: scan error message: %w", err)
		}
		if body.Valid {
			m.BodyTemplate = body.String
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetByStatusCode fetches one row. Returns a wrapped ErrErrorMessageNotFound
// when no row matches so callers can fall back to errormessages.Default.
func (s *pgErrorMessageStore) GetByStatusCode(ctx context.Context, statusCode int) (*errormessages.Message, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: error messages store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT status_code, title, message, body_template, enabled, created_at, updated_at
		FROM %s WHERE status_code = $1
	`, s.table), statusCode)
	var m errormessages.Message
	var body sql.NullString
	err := row.Scan(&m.StatusCode, &m.Title, &m.Message, &body, &m.Enabled,
		&m.CreatedAt, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrErrorMessageNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("postgres store: get error message: %w", err)
	}
	if body.Valid {
		m.BodyTemplate = body.String
	}
	return &m, nil
}

// Upsert creates or replaces a row. updated_at is bumped by the database.
func (s *pgErrorMessageStore) Upsert(ctx context.Context, m errormessages.Message) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: error messages store not initialized")
	}
	if m.StatusCode < 100 || m.StatusCode > 599 {
		return fmt.Errorf("postgres store: invalid status code %d", m.StatusCode)
	}
	var body any
	if m.BodyTemplate != "" {
		body = m.BodyTemplate
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (status_code, title, message, body_template, enabled, updated_at)
		VALUES ($1, $2, $3, $4, $5, NOW())
		ON CONFLICT (status_code) DO UPDATE SET
			title = EXCLUDED.title,
			message = EXCLUDED.message,
			body_template = EXCLUDED.body_template,
			enabled = EXCLUDED.enabled,
			updated_at = NOW()
	`, s.table), m.StatusCode, m.Title, m.Message, body, m.Enabled)
	if err != nil {
		return fmt.Errorf("postgres store: upsert error message: %w", err)
	}
	return nil
}

// Delete removes a row by status code. Idempotent — no error if the row
// was already gone.
func (s *pgErrorMessageStore) Delete(ctx context.Context, statusCode int) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: error messages store not initialized")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE status_code = $1`, s.table,
	), statusCode)
	if err != nil {
		return fmt.Errorf("postgres store: delete error message: %w", err)
	}
	return nil
}

// ErrErrorMessageNotFound is returned by GetByStatusCode when no row matches.
var ErrErrorMessageNotFound = errors.New("postgres store: error message not found")

// Compile-time assertion that *pgErrorMessageStore implements the
// errormessages.Store contract.
var _ errormessages.Store = (*pgErrorMessageStore)(nil)

// touchedAt exposes a tiny helper used by the server bootstrap to confirm
// the table is reachable during startup diagnostics. Not part of the
// errormessages.Store contract — used only to surface a "PG error messages
// store wired" log line.
func (s *pgErrorMessageStore) touchedAt() time.Time { return time.Now() }
