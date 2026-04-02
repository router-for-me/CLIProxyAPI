package accountpool

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Leader represents a leader (母号) account in the pool.
type Leader struct {
	ID                        int64      `json:"id"`
	Email                     string     `json:"email"`
	Password                  string     `json:"password"`
	RecoveryEmail             string     `json:"recovery_email"`
	TOTPSecret                string     `json:"totp_secret"`
	Status                    string     `json:"status"`
	NstbrowserProfileID       string     `json:"nstbrowser_profile_id"`
	NstbrowserProfileName     string     `json:"nstbrowser_profile_name"`
	UltraSubscriptionExpiry   *time.Time `json:"ultra_subscription_expiry"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

// ListLeaders returns leaders filtered by optional status and search term.
func (s *Store) ListLeaders(ctx context.Context, status, search string, limit, offset int) ([]Leader, int, error) {
	table := s.tableName("account_pool_leaders")
	where, args := buildWhereClause(status, search, 1)

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", table, where)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count leaders: %w", err)
	}

	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf(`SELECT id, email, password, recovery_email, totp_secret, status,
		nstbrowser_profile_id, nstbrowser_profile_name, ultra_subscription_expiry, created_at, updated_at
		FROM %s%s ORDER BY id ASC LIMIT $%d OFFSET $%d`, table, where, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list leaders: %w", err)
	}
	defer rows.Close()

	var leaders []Leader
	for rows.Next() {
		var l Leader
		if err := rows.Scan(&l.ID, &l.Email, &l.Password, &l.RecoveryEmail, &l.TOTPSecret,
			&l.Status, &l.NstbrowserProfileID, &l.NstbrowserProfileName,
			&l.UltraSubscriptionExpiry, &l.CreatedAt, &l.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan leader: %w", err)
		}
		leaders = append(leaders, l)
	}
	return leaders, total, rows.Err()
}

// GetLeader returns a single leader by ID.
func (s *Store) GetLeader(ctx context.Context, id int64) (*Leader, error) {
	table := s.tableName("account_pool_leaders")
	query := fmt.Sprintf(`SELECT id, email, password, recovery_email, totp_secret, status,
		nstbrowser_profile_id, nstbrowser_profile_name, ultra_subscription_expiry, created_at, updated_at
		FROM %s WHERE id = $1`, table)
	var l Leader
	err := s.db.QueryRowContext(ctx, query, id).Scan(&l.ID, &l.Email, &l.Password, &l.RecoveryEmail,
		&l.TOTPSecret, &l.Status, &l.NstbrowserProfileID, &l.NstbrowserProfileName,
		&l.UltraSubscriptionExpiry, &l.CreatedAt, &l.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get leader: %w", err)
	}
	return &l, nil
}

// CreateLeader inserts a new leader.
func (s *Store) CreateLeader(ctx context.Context, l *Leader) error {
	table := s.tableName("account_pool_leaders")
	if l.Status == "" {
		l.Status = "available"
	}
	query := fmt.Sprintf(`INSERT INTO %s (email, password, recovery_email, totp_secret, status,
		nstbrowser_profile_id, nstbrowser_profile_name, ultra_subscription_expiry)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at, updated_at`, table)
	return s.db.QueryRowContext(ctx, query, l.Email, l.Password, l.RecoveryEmail, l.TOTPSecret,
		l.Status, l.NstbrowserProfileID, l.NstbrowserProfileName, l.UltraSubscriptionExpiry).Scan(&l.ID, &l.CreatedAt, &l.UpdatedAt)
}

// UpdateLeader updates an existing leader.
func (s *Store) UpdateLeader(ctx context.Context, l *Leader) error {
	table := s.tableName("account_pool_leaders")
	query := fmt.Sprintf(`UPDATE %s SET email=$1, password=$2, recovery_email=$3, totp_secret=$4,
		status=$5, nstbrowser_profile_id=$6, nstbrowser_profile_name=$7,
		ultra_subscription_expiry=$8, updated_at=NOW()
		WHERE id=$9 RETURNING updated_at`, table)
	err := s.db.QueryRowContext(ctx, query, l.Email, l.Password, l.RecoveryEmail, l.TOTPSecret,
		l.Status, l.NstbrowserProfileID, l.NstbrowserProfileName,
		l.UltraSubscriptionExpiry, l.ID).Scan(&l.UpdatedAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("leader not found")
	}
	return err
}

// UpdateLeaderStatus changes a leader's status.
func (s *Store) UpdateLeaderStatus(ctx context.Context, id int64, status string) error {
	table := s.tableName("account_pool_leaders")
	query := fmt.Sprintf("UPDATE %s SET status=$1, updated_at=NOW() WHERE id=$2", table)
	res, err := s.db.ExecContext(ctx, query, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("leader not found")
	}
	return nil
}

// DeleteLeader removes a leader by ID.
func (s *Store) DeleteLeader(ctx context.Context, id int64) error {
	table := s.tableName("account_pool_leaders")
	query := fmt.Sprintf("DELETE FROM %s WHERE id=$1", table)
	res, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("leader not found")
	}
	return nil
}

// BatchCreateLeaders inserts multiple leaders, skipping duplicates.
func (s *Store) BatchCreateLeaders(ctx context.Context, leaders []Leader) (int, []string, error) {
	if len(leaders) == 0 {
		return 0, nil, nil
	}
	table := s.tableName("account_pool_leaders")

	var created int
	var errors []string
	for i, l := range leaders {
		if l.Status == "" {
			l.Status = "available"
		}
		query := fmt.Sprintf(`INSERT INTO %s (email, password, recovery_email, totp_secret, status,
			nstbrowser_profile_id, nstbrowser_profile_name, ultra_subscription_expiry)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (email) DO NOTHING`, table)
		res, err := s.db.ExecContext(ctx, query, l.Email, l.Password, l.RecoveryEmail, l.TOTPSecret,
			l.Status, l.NstbrowserProfileID, l.NstbrowserProfileName, l.UltraSubscriptionExpiry)
		if err != nil {
			errors = append(errors, fmt.Sprintf("line %d (%s): %v", i+1, l.Email, err))
			continue
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			errors = append(errors, fmt.Sprintf("line %d (%s): duplicate email", i+1, l.Email))
		} else {
			created++
		}
	}
	return created, errors, nil
}
