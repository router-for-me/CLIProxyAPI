package accountpool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Member represents a member (子号) account in the pool.
type Member struct {
	ID                    int64     `json:"id"`
	Email                 string    `json:"email"`
	Password              string    `json:"password"`
	RecoveryEmail         string    `json:"recovery_email"`
	TOTPSecret            string    `json:"totp_secret"`
	Status                string    `json:"status"`
	NstbrowserProfileID   string    `json:"nstbrowser_profile_id"`
	NstbrowserProfileName string    `json:"nstbrowser_profile_name"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
}

// ListMembers returns members filtered by optional status and search term.
func (s *Store) ListMembers(ctx context.Context, status, search string, limit, offset int) ([]Member, int, error) {
	table := s.tableName("account_pool_members")
	where, args := buildWhereClause(status, search, 1)

	var total int
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM %s%s", table, where)
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count members: %w", err)
	}

	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf(`SELECT id, email, password, recovery_email, totp_secret, status,
		nstbrowser_profile_id, nstbrowser_profile_name, created_at, updated_at
		FROM %s%s ORDER BY id ASC LIMIT $%d OFFSET $%d`, table, where, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list members: %w", err)
	}
	defer rows.Close()

	var members []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.Email, &m.Password, &m.RecoveryEmail, &m.TOTPSecret,
			&m.Status, &m.NstbrowserProfileID, &m.NstbrowserProfileName, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan member: %w", err)
		}
		members = append(members, m)
	}
	return members, total, rows.Err()
}

// GetMember returns a single member by ID.
func (s *Store) GetMember(ctx context.Context, id int64) (*Member, error) {
	table := s.tableName("account_pool_members")
	query := fmt.Sprintf(`SELECT id, email, password, recovery_email, totp_secret, status,
		nstbrowser_profile_id, nstbrowser_profile_name, created_at, updated_at
		FROM %s WHERE id = $1`, table)
	var m Member
	err := s.db.QueryRowContext(ctx, query, id).Scan(&m.ID, &m.Email, &m.Password, &m.RecoveryEmail,
		&m.TOTPSecret, &m.Status, &m.NstbrowserProfileID, &m.NstbrowserProfileName, &m.CreatedAt, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get member: %w", err)
	}
	return &m, nil
}

// CreateMember inserts a new member.
func (s *Store) CreateMember(ctx context.Context, m *Member) error {
	table := s.tableName("account_pool_members")
	if m.Status == "" {
		m.Status = "available"
	}
	query := fmt.Sprintf(`INSERT INTO %s (email, password, recovery_email, totp_secret, status,
		nstbrowser_profile_id, nstbrowser_profile_name)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id, created_at, updated_at`, table)
	return s.db.QueryRowContext(ctx, query, m.Email, m.Password, m.RecoveryEmail, m.TOTPSecret,
		m.Status, m.NstbrowserProfileID, m.NstbrowserProfileName).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
}

// UpdateMember updates an existing member.
func (s *Store) UpdateMember(ctx context.Context, m *Member) error {
	table := s.tableName("account_pool_members")
	query := fmt.Sprintf(`UPDATE %s SET email=$1, password=$2, recovery_email=$3, totp_secret=$4,
		status=$5, nstbrowser_profile_id=$6, nstbrowser_profile_name=$7, updated_at=NOW()
		WHERE id=$8 RETURNING updated_at`, table)
	err := s.db.QueryRowContext(ctx, query, m.Email, m.Password, m.RecoveryEmail, m.TOTPSecret,
		m.Status, m.NstbrowserProfileID, m.NstbrowserProfileName, m.ID).Scan(&m.UpdatedAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("member not found")
	}
	return err
}

// UpdateMemberStatus changes a member's status.
func (s *Store) UpdateMemberStatus(ctx context.Context, id int64, status string) error {
	table := s.tableName("account_pool_members")
	query := fmt.Sprintf("UPDATE %s SET status=$1, updated_at=NOW() WHERE id=$2", table)
	res, err := s.db.ExecContext(ctx, query, status, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("member not found")
	}
	return nil
}

// DeleteMember removes a member by ID.
func (s *Store) DeleteMember(ctx context.Context, id int64) error {
	table := s.tableName("account_pool_members")
	query := fmt.Sprintf("DELETE FROM %s WHERE id=$1", table)
	res, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("member not found")
	}
	return nil
}

// BatchCreateMembers inserts multiple members, skipping duplicates.
func (s *Store) BatchCreateMembers(ctx context.Context, members []Member) (int, []string, error) {
	if len(members) == 0 {
		return 0, nil, nil
	}
	table := s.tableName("account_pool_members")

	var created int
	var errors []string
	for i, m := range members {
		if m.Status == "" {
			m.Status = "available"
		}
		query := fmt.Sprintf(`INSERT INTO %s (email, password, recovery_email, totp_secret, status,
			nstbrowser_profile_id, nstbrowser_profile_name)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (email) DO NOTHING`, table)
		res, err := s.db.ExecContext(ctx, query, m.Email, m.Password, m.RecoveryEmail, m.TOTPSecret,
			m.Status, m.NstbrowserProfileID, m.NstbrowserProfileName)
		if err != nil {
			errors = append(errors, fmt.Sprintf("line %d (%s): %v", i+1, m.Email, err))
			continue
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			errors = append(errors, fmt.Sprintf("line %d (%s): duplicate email", i+1, m.Email))
		} else {
			created++
		}
	}
	return created, errors, nil
}

// PickNextAvailableMember atomically selects and marks the next available member as used.
func (s *Store) PickNextAvailableMember(ctx context.Context) (*Member, error) {
	table := s.tableName("account_pool_members")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	query := fmt.Sprintf(`SELECT id, email, password, recovery_email, totp_secret, status,
		nstbrowser_profile_id, nstbrowser_profile_name, created_at, updated_at
		FROM %s WHERE status = 'available' ORDER BY id ASC LIMIT 1 FOR UPDATE SKIP LOCKED`, table)

	var m Member
	err = tx.QueryRowContext(ctx, query).Scan(&m.ID, &m.Email, &m.Password, &m.RecoveryEmail,
		&m.TOTPSecret, &m.Status, &m.NstbrowserProfileID, &m.NstbrowserProfileName, &m.CreatedAt, &m.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select member: %w", err)
	}

	updateQuery := fmt.Sprintf("UPDATE %s SET status='used', updated_at=NOW() WHERE id=$1", table)
	if _, err := tx.ExecContext(ctx, updateQuery, m.ID); err != nil {
		return nil, fmt.Errorf("update member status: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	m.Status = "used"
	return &m, nil
}

// buildWhereClause builds a WHERE clause for status and email search.
func buildWhereClause(status, search string, paramStart int) (string, []interface{}) {
	var conditions []string
	var args []interface{}
	idx := paramStart

	if status != "" {
		conditions = append(conditions, fmt.Sprintf("status = $%d", idx))
		args = append(args, status)
		idx++
	}
	if search != "" {
		conditions = append(conditions, fmt.Sprintf("email ILIKE $%d", idx))
		args = append(args, "%"+strings.ReplaceAll(search, "%", "\\%")+"%")
		idx++
	}

	if len(conditions) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conditions, " AND "), args
}
