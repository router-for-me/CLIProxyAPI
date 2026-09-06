package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProxyPool is one row of the proxy_pools table — a named egress-proxy pool.
// Standard pools (type "http") carry a proxy URL consumed as the entry's
// ProxyURL (the renderer appends the composite suffix ?no_proxy=…&strict=…).
// Relay pools (type "vercel"|"cloudflare"|"deno") carry a relay base URL
// consumed as Auth.RelayBaseURL with the x-relay-target/x-relay-path header
// contract.
type ProxyPool struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	ProxyURL     string     `json:"proxy_url"`
	NoProxy      string     `json:"no_proxy,omitempty"`
	Type         string     `json:"type,omitempty"`
	IsActive     bool       `json:"is_active"`
	StrictProxy  bool       `json:"strict_proxy"`
	TestStatus   string     `json:"test_status,omitempty"`
	LastTestedAt *time.Time `json:"last_tested_at,omitempty"`
	LastError    string     `json:"last_error,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ProxyPoolStore is the contract consumed by the /v0/management/proxy-pools
// routes. A nil implementation (PG unconfigured) yields 503 at the handler.
type ProxyPoolStore interface {
	List(ctx context.Context) ([]ProxyPool, error)
	Get(ctx context.Context, id int64) (*ProxyPool, error)
	Create(ctx context.Context, p ProxyPool) (*ProxyPool, error)
	Update(ctx context.Context, p ProxyPool) (*ProxyPool, error)
	Delete(ctx context.Context, id int64) error
	// BoundEntryCount returns how many upstream provider rows and API-key
	// entries reference the pool (row-level + entry-level bindings summed).
	BoundEntryCount(ctx context.Context, poolID int64) (int64, error)
}

// ErrProxyPoolNotFound is returned by Get/Update/Delete when no row matches.
var ErrProxyPoolNotFound = errors.New("postgres store: proxy pool not found")

// ErrProxyPoolDuplicateName is returned by Create/Update when the pool name
// collides with an existing pool (case-insensitive uniqueness).
var ErrProxyPoolDuplicateName = errors.New("postgres store: duplicate proxy pool name")

// proxyPoolColumns is the canonical column list shared by the SELECTs.
const proxyPoolColumns = `id, name, proxy_url, no_proxy, type, is_active, strict_proxy,
	test_status, last_tested_at, last_error, created_at, updated_at`

// validProxyPoolTypes is the closed set of pool types the renderer understands.
var validProxyPoolTypes = map[string]bool{"http": true, "vercel": true, "cloudflare": true, "deno": true}

// ValidProxyPoolType reports whether the pool type is one of the closed set.
func ValidProxyPoolType(t string) bool {
	return validProxyPoolTypes[t]
}

// pgProxyPoolStore implements ProxyPoolStore against the proxy_pools table.
type pgProxyPoolStore struct {
	db             *sql.DB
	table          string
	providersTable string
	entriesTable   string
}

// NewProxyPoolStore wires the store against a parent PostgresStore.
// Returns nil when parent is nil so callers can feature-detect via nil.
func NewProxyPoolStore(parent *PostgresStore) ProxyPoolStore {
	if parent == nil {
		return nil
	}
	return &pgProxyPoolStore{
		db:             parent.DB(),
		table:          parent.ProxyPoolsTable(),
		providersTable: parent.UpstreamProvidersTable(),
		entriesTable:   parent.UpstreamProviderEntriesTable(),
	}
}

// scanProxyPool scans one row of proxyPoolColumns into a ProxyPool.
func scanProxyPool(scanner interface{ Scan(dest ...any) error }) (ProxyPool, error) {
	var p ProxyPool
	var lastError sql.NullString
	if err := scanner.Scan(&p.ID, &p.Name, &p.ProxyURL, &p.NoProxy, &p.Type, &p.IsActive, &p.StrictProxy,
		&p.TestStatus, &p.LastTestedAt, &lastError, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return ProxyPool{}, err
	}
	p.LastError = lastError.String
	return p, nil
}

// List loads every pool ordered by name for stable dashboard display.
func (s *pgProxyPoolStore) List(ctx context.Context) ([]ProxyPool, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: proxy pools store not initialized")
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+proxyPoolColumns+" FROM "+s.table+" ORDER BY name, id")
	if err != nil {
		return nil, fmt.Errorf("postgres store: list proxy pools: %w", err)
	}
	defer rows.Close()
	out := make([]ProxyPool, 0)
	for rows.Next() {
		p, errScan := scanProxyPool(rows)
		if errScan != nil {
			return nil, fmt.Errorf("postgres store: scan proxy pool: %w", errScan)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Get loads one pool by id.
func (s *pgProxyPoolStore) Get(ctx context.Context, id int64) (*ProxyPool, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: proxy pools store not initialized")
	}
	row := s.db.QueryRowContext(ctx, "SELECT "+proxyPoolColumns+" FROM "+s.table+" WHERE id = $1", id)
	p, errScan := scanProxyPool(row)
	if errors.Is(errScan, sql.ErrNoRows) {
		return nil, ErrProxyPoolNotFound
	}
	if errScan != nil {
		return nil, fmt.Errorf("postgres store: get proxy pool: %w", errScan)
	}
	return &p, nil
}

// validateProxyPoolInput checks store-level invariants (the handler validates
// earlier with richer messages; this is the backstop for direct store users).
func validateProxyPoolInput(p ProxyPool) error {
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("proxy pool name is required")
	}
	if strings.TrimSpace(p.ProxyURL) == "" {
		return fmt.Errorf("proxy pool proxy_url is required")
	}
	poolType := p.Type
	if poolType == "" {
		poolType = "http"
	}
	if !validProxyPoolTypes[poolType] {
		return fmt.Errorf("proxy pool type %q is not one of http/vercel/cloudflare/deno", p.Type)
	}
	return nil
}

// isDuplicateNameError detects the unique-index violation on lower(name).
func isDuplicateNameError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") && strings.Contains(msg, "idx_proxy_pools_name")
}

// Create inserts one pool. Type defaults to "http"; name uniqueness is
// case-insensitive per idx_proxy_pools_name.
func (s *pgProxyPoolStore) Create(ctx context.Context, p ProxyPool) (*ProxyPool, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: proxy pools store not initialized")
	}
	if errValidate := validateProxyPoolInput(p); errValidate != nil {
		return nil, errValidate
	}
	if p.Type == "" {
		p.Type = "http"
	}
	if p.TestStatus == "" {
		p.TestStatus = "unknown"
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (name, proxy_url, no_proxy, type, is_active, strict_proxy, test_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING %s`, s.table, proxyPoolColumns),
		strings.TrimSpace(p.Name), strings.TrimSpace(p.ProxyURL), p.NoProxy, p.Type,
		p.IsActive, p.StrictProxy, p.TestStatus,
	)
	created, errScan := scanProxyPool(row)
	if isDuplicateNameError(errScan) {
		return nil, ErrProxyPoolDuplicateName
	}
	if errScan != nil {
		return nil, fmt.Errorf("postgres store: create proxy pool: %w", errScan)
	}
	return &created, nil
}

// Update replaces all mutable columns of one pool. Test-result fields are
// plain columns so the test endpoint writes them through the same path.
func (s *pgProxyPoolStore) Update(ctx context.Context, p ProxyPool) (*ProxyPool, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("postgres store: proxy pools store not initialized")
	}
	if errValidate := validateProxyPoolInput(p); errValidate != nil {
		return nil, errValidate
	}
	row := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		UPDATE %s SET
			name = $2, proxy_url = $3, no_proxy = $4, type = $5,
			is_active = $6, strict_proxy = $7,
			test_status = $8, last_tested_at = $9, last_error = $10,
			updated_at = NOW()
		WHERE id = $1
		RETURNING %s`, s.table, proxyPoolColumns),
		p.ID, strings.TrimSpace(p.Name), strings.TrimSpace(p.ProxyURL), p.NoProxy, p.Type,
		p.IsActive, p.StrictProxy, p.TestStatus, p.LastTestedAt, nullString(p.LastError),
	)
	updated, errScan := scanProxyPool(row)
	if errors.Is(errScan, sql.ErrNoRows) {
		return nil, ErrProxyPoolNotFound
	}
	if isDuplicateNameError(errScan) {
		return nil, ErrProxyPoolDuplicateName
	}
	if errScan != nil {
		return nil, fmt.Errorf("postgres store: update proxy pool: %w", errScan)
	}
	return &updated, nil
}

// Delete removes one pool. The FK on upstream_providers.proxy_pool_id and
// upstream_provider_api_key_entries.proxy_pool_id makes this fail while any
// row/entry still references the pool; the handler checks BoundEntryCount
// first for a friendly 409, this is the backstop.
func (s *pgProxyPoolStore) Delete(ctx context.Context, id int64) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("postgres store: proxy pools store not initialized")
	}
	res, errExec := s.db.ExecContext(ctx, "DELETE FROM "+s.table+" WHERE id = $1", id)
	if errExec != nil {
		return fmt.Errorf("postgres store: delete proxy pool: %w", errExec)
	}
	affected, errRows := res.RowsAffected()
	if errRows != nil {
		return fmt.Errorf("postgres store: delete proxy pool rows: %w", errRows)
	}
	if affected == 0 {
		return ErrProxyPoolNotFound
	}
	return nil
}

// BoundEntryCount sums the row-level and entry-level bindings in one query.
func (s *pgProxyPoolStore) BoundEntryCount(ctx context.Context, poolID int64) (int64, error) {
	if s == nil || s.db == nil {
		return 0, fmt.Errorf("postgres store: proxy pools store not initialized")
	}
	var count int64
	err := s.db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT
			(SELECT COUNT(*) FROM %s WHERE proxy_pool_id = $1) +
			(SELECT COUNT(*) FROM %s WHERE proxy_pool_id = $1)`,
		s.providersTable, s.entriesTable), poolID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("postgres store: bound entry count: %w", err)
	}
	return count, nil
}

// nullString converts "" into a NULL-able column value.
func nullString(v string) sql.NullString {
	return sql.NullString{String: v, Valid: v != ""}
}
