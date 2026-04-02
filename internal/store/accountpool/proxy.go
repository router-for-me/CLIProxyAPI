package accountpool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Proxy represents a proxy entry in the pool.
type Proxy struct {
	ID        int64     `json:"id"`
	ProxyURL  string    `json:"proxy_url"`
	Type      string    `json:"type"` // "leader" or "member"
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListProxies returns proxies filtered by optional type and status.
func (s *Store) ListProxies(ctx context.Context, proxyType, status string, limit, offset int) ([]Proxy, int, error) {
	table := s.tableName("account_pool_proxies")
	var conditions []string
	var args []interface{}
	idx := 1

	if proxyType != "" {
		conditions = append(conditions, fmt.Sprintf("type = $%d", idx))
		args = append(args, proxyType)
		idx++
	}
	if status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", idx))
		args = append(args, status)
		idx++
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s%s", table, where), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count proxies: %w", err)
	}

	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf(`SELECT id, proxy_url, type, status, created_at, updated_at
		FROM %s%s ORDER BY id ASC LIMIT $%d OFFSET $%d`, table, where, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list proxies: %w", err)
	}
	defer rows.Close()

	var proxies []Proxy
	for rows.Next() {
		var p Proxy
		if err := rows.Scan(&p.ID, &p.ProxyURL, &p.Type, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan proxy: %w", err)
		}
		proxies = append(proxies, p)
	}
	return proxies, total, rows.Err()
}

// CreateProxy inserts a new proxy.
func (s *Store) CreateProxy(ctx context.Context, p *Proxy) error {
	table := s.tableName("account_pool_proxies")
	if p.Status == "" {
		p.Status = "available"
	}
	query := fmt.Sprintf(`INSERT INTO %s (proxy_url, type, status)
		VALUES ($1, $2, $3) RETURNING id, created_at, updated_at`, table)
	return s.db.QueryRowContext(ctx, query, p.ProxyURL, p.Type, p.Status).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

// UpdateProxy updates an existing proxy.
func (s *Store) UpdateProxy(ctx context.Context, p *Proxy) error {
	table := s.tableName("account_pool_proxies")
	query := fmt.Sprintf(`UPDATE %s SET proxy_url=$1, type=$2, status=$3, updated_at=NOW()
		WHERE id=$4 RETURNING updated_at`, table)
	err := s.db.QueryRowContext(ctx, query, p.ProxyURL, p.Type, p.Status, p.ID).Scan(&p.UpdatedAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("proxy not found")
	}
	return err
}

// DeleteProxy removes a proxy by ID.
func (s *Store) DeleteProxy(ctx context.Context, id int64) error {
	table := s.tableName("account_pool_proxies")
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id=$1", table), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("proxy not found")
	}
	return nil
}

// BatchCreateProxies inserts multiple proxies, skipping duplicates.
func (s *Store) BatchCreateProxies(ctx context.Context, proxies []Proxy) (int, []string, error) {
	if len(proxies) == 0 {
		return 0, nil, nil
	}
	table := s.tableName("account_pool_proxies")

	var created int
	var errors []string
	for i, p := range proxies {
		if p.Status == "" {
			p.Status = "available"
		}
		query := fmt.Sprintf(`INSERT INTO %s (proxy_url, type, status)
			VALUES ($1, $2, $3) ON CONFLICT (proxy_url) DO NOTHING`, table)
		res, err := s.db.ExecContext(ctx, query, p.ProxyURL, p.Type, p.Status)
		if err != nil {
			errors = append(errors, fmt.Sprintf("line %d (%s): %v", i+1, p.ProxyURL, err))
			continue
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			errors = append(errors, fmt.Sprintf("line %d (%s): duplicate proxy_url", i+1, p.ProxyURL))
		} else {
			created++
		}
	}
	return created, errors, nil
}

// PickNextAvailableProxy atomically selects and marks the next available proxy as used.
func (s *Store) PickNextAvailableProxy(ctx context.Context, proxyType string) (*Proxy, error) {
	table := s.tableName("account_pool_proxies")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var query string
	var args []interface{}
	if proxyType != "" {
		query = fmt.Sprintf(`SELECT id, proxy_url, type, status, created_at, updated_at
			FROM %s WHERE status = 'available' AND type = $1
			ORDER BY id ASC LIMIT 1 FOR UPDATE SKIP LOCKED`, table)
		args = []interface{}{proxyType}
	} else {
		query = fmt.Sprintf(`SELECT id, proxy_url, type, status, created_at, updated_at
			FROM %s WHERE status = 'available'
			ORDER BY id ASC LIMIT 1 FOR UPDATE SKIP LOCKED`, table)
	}

	var p Proxy
	err = tx.QueryRowContext(ctx, query, args...).Scan(&p.ID, &p.ProxyURL, &p.Type, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select proxy: %w", err)
	}

	if _, err := tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status='used', updated_at=NOW() WHERE id=$1", table), p.ID); err != nil {
		return nil, fmt.Errorf("update proxy status: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	p.Status = "used"
	return &p, nil
}
