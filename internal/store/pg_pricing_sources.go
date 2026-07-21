package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PricingSource is one operator-managed external pricing catalog. The
// pricingsource package loads each enabled row on refresh and merges its
// entries into the in-memory match index.
//
// SourceType is "url" (fetch the JSON at URL on refresh) or "file" (parse
// the locally uploaded file at FilePath). Format is currently always
// "litellm"; the field is kept explicit so future native-format catalogs
// can be added without a schema change.
type PricingSource struct {
	ID            int64      `json:"id"`
	Name          string     `json:"name"`
	SourceType    string     `json:"source_type"` // "url" | "file"
	URL           string     `json:"url,omitempty"`
	FilePath      string     `json:"file_path,omitempty"`
	Format        string     `json:"format"` // "litellm"
	Enabled       bool       `json:"enabled"`
	LastFetchedAt *time.Time `json:"last_fetched_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	EntryCount    int        `json:"entry_count"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// PricingSourceStore is the contract the management API consumes for the
// /v0/management/pricing-sources routes. The PG-backed implementation lives
// in pg_pricing_sources.go; a nil implementation is used when PG is not
// configured (routes then return 503).
type PricingSourceStore interface {
	List(ctx context.Context) ([]PricingSource, error)
	Get(ctx context.Context, id int64) (*PricingSource, error)
	GetByName(ctx context.Context, name string) (*PricingSource, error)
	Create(ctx context.Context, src PricingSource) (*PricingSource, error)
	Update(ctx context.Context, src PricingSource) (*PricingSource, error)
	Delete(ctx context.Context, id int64) error
	// RecordRefresh writes the outcome of a refresh attempt: entry_count
	// models loaded, and errMsg empty on success. Bumps last_fetched_at.
	RecordRefresh(ctx context.Context, id int64, entryCount int, errMsg string) error
}

// pgPricingSourceStore implements PricingSourceStore against the
// pricing_sources table.
type pgPricingSourceStore struct {
	db    *sql.DB
	table string
}

// NewPricingSourceStore wires the store against a parent PostgresStore.
// Returns nil when parent is nil so callers can feature-detect via nil.
func NewPricingSourceStore(parent *PostgresStore) PricingSourceStore {
	if parent == nil {
		return nil
	}
	return &pgPricingSourceStore{
		db:    parent.DB(),
		table: parent.PricingSourcesTable(),
	}
}

// List loads every pricing source row, newest first by id.
func (s *pgPricingSourceStore) List(ctx context.Context) ([]PricingSource, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: pricing sources store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, name, source_type, url, file_path, format, enabled,
		       last_fetched_at, last_error, entry_count, created_at, updated_at
		FROM %s
		ORDER BY id
	`, s.table))
	if err != nil {
		return nil, fmt.Errorf("postgres store: list pricing sources: %w", err)
	}
	defer rows.Close()
	out := make([]PricingSource, 0, 8)
	for rows.Next() {
		var p PricingSource
		if err = scanPricingSource(rows, &p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get fetches one row by id.
func (s *pgPricingSourceStore) Get(ctx context.Context, id int64) (*PricingSource, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: pricing sources store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, name, source_type, url, file_path, format, enabled,
		       last_fetched_at, last_error, entry_count, created_at, updated_at
		FROM %s WHERE id = $1
	`, s.table), id)
	var p PricingSource
	if err := scanPricingSource(row, &p); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPricingSourceNotFound
		}
		return nil, err
	}
	return &p, nil
}

// GetByName fetches one row by name.
func (s *pgPricingSourceStore) GetByName(ctx context.Context, name string) (*PricingSource, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: pricing sources store not initialized")
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT id, name, source_type, url, file_path, format, enabled,
		       last_fetched_at, last_error, entry_count, created_at, updated_at
		FROM %s WHERE name = $1
	`, s.table), name)
	var p PricingSource
	if err := scanPricingSource(row, &p); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPricingSourceNotFound
		}
		return nil, err
	}
	return &p, nil
}

// Create inserts a new pricing source. Name must be unique.
func (s *pgPricingSourceStore) Create(ctx context.Context, src PricingSource) (*PricingSource, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: pricing sources store not initialized")
	}
	if err := validatePricingSource(src); err != nil {
		return nil, err
	}
	format := src.Format
	if format == "" {
		format = "litellm"
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (name, source_type, url, file_path, format, enabled)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, name, source_type, url, file_path, format, enabled,
		          last_fetched_at, last_error, entry_count, created_at, updated_at
	`, s.table), src.Name, src.SourceType, nilIfEmpty(src.URL), nilIfEmpty(src.FilePath),
		format, src.Enabled)
	var p PricingSource
	if err := scanPricingSource(row, &p); err != nil {
		return nil, fmt.Errorf("postgres store: create pricing source: %w", err)
	}
	return &p, nil
}

// Update replaces the mutable fields of an existing row.
func (s *pgPricingSourceStore) Update(ctx context.Context, src PricingSource) (*PricingSource, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: pricing sources store not initialized")
	}
	if src.ID == 0 {
		return nil, fmt.Errorf("postgres store: pricing source id required for update")
	}
	if err := validatePricingSource(src); err != nil {
		return nil, err
	}
	format := src.Format
	if format == "" {
		format = "litellm"
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		UPDATE %s SET
			name = $1,
			source_type = $2,
			url = $3,
			file_path = $4,
			format = $5,
			enabled = $6,
			updated_at = NOW()
		WHERE id = $7
		RETURNING id, name, source_type, url, file_path, format, enabled,
		          last_fetched_at, last_error, entry_count, created_at, updated_at
	`, s.table), src.Name, src.SourceType, nilIfEmpty(src.URL), nilIfEmpty(src.FilePath),
		format, src.Enabled, src.ID)
	var p PricingSource
	if err := scanPricingSource(row, &p); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrPricingSourceNotFound
		}
		return nil, fmt.Errorf("postgres store: update pricing source: %w", err)
	}
	return &p, nil
}

// Delete removes a row by id. Idempotent.
func (s *pgPricingSourceStore) Delete(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: pricing sources store not initialized")
	}
	_, err := s.db.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE id = $1`, s.table,
	), id)
	if err != nil {
		return fmt.Errorf("postgres store: delete pricing source: %w", err)
	}
	return nil
}

// RecordRefresh writes the outcome of a refresh: entryCount models loaded,
// errMsg empty on success. Bumps last_fetched_at to NOW().
func (s *pgPricingSourceStore) RecordRefresh(ctx context.Context, id int64, entryCount int, errMsg string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: pricing sources store not initialized")
	}
	var errBody any
	if errMsg != "" {
		errBody = errMsg
	}
	res, err := s.db.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s SET
			last_fetched_at = NOW(),
			last_error = $1,
			entry_count = $2,
			updated_at = NOW()
		WHERE id = $3
	`, s.table), errBody, entryCount, id)
	if err != nil {
		return fmt.Errorf("postgres store: record pricing source refresh: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrPricingSourceNotFound
	}
	return nil
}

// ErrPricingSourceNotFound is returned by Get/Update/RecordRefresh when no
// row matches the supplied id.
var ErrPricingSourceNotFound = errors.New("postgres store: pricing source not found")

// validatePricingSource enforces required fields + source_type xor rule.
func validatePricingSource(src PricingSource) error {
	if src.Name == "" {
		return fmt.Errorf("postgres store: pricing source name is required")
	}
	switch src.SourceType {
	case "url":
		if src.URL == "" {
			return fmt.Errorf("postgres store: pricing source url is required for source_type=url")
		}
	case "file":
		if src.FilePath == "" {
			return fmt.Errorf("postgres store: pricing source file_path is required for source_type=file")
		}
	default:
		return fmt.Errorf("postgres store: pricing source source_type must be 'url' or 'file' (got %q)", src.SourceType)
	}
	return nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// scanner abstracts *sql.Row and *sql.Rows so the scan helpers can serve
// both Get (single row) and List (multi-row) paths.
type scanner interface {
	Scan(dest ...any) error
}

func scanPricingSource(sc scanner, p *PricingSource) error {
	var url, filePath, lastErr sql.NullString
	var lastFetched sql.NullTime
	if err := sc.Scan(
		&p.ID, &p.Name, &p.SourceType, &url, &filePath, &p.Format, &p.Enabled,
		&lastFetched, &lastErr, &p.EntryCount, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return err
	}
	if url.Valid {
		p.URL = url.String
	}
	if filePath.Valid {
		p.FilePath = filePath.String
	}
	if lastFetched.Valid {
		t := lastFetched.Time
		p.LastFetchedAt = &t
	}
	if lastErr.Valid {
		p.LastError = lastErr.String
	}
	return nil
}

// Compile-time assertion that *pgPricingSourceStore implements the contract.
var _ PricingSourceStore = (*pgPricingSourceStore)(nil)
