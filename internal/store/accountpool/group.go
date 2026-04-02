package accountpool

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Group represents a family group assignment.
type Group struct {
	ID           int64     `json:"id"`
	GroupID      string    `json:"group_id"`
	Date         string    `json:"date"` // YYYY-MM-DD
	LeaderEmail  string    `json:"leader_email"`
	MemberEmail  string    `json:"member_email"`
	FamilyStatus string    `json:"family_status"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ListGroups returns groups filtered by optional group_id and leader_email.
func (s *Store) ListGroups(ctx context.Context, groupID, leaderEmail string, limit, offset int) ([]Group, int, error) {
	table := s.tableName("account_pool_groups")
	var conditions []string
	var args []interface{}
	idx := 1

	if groupID != "" {
		conditions = append(conditions, fmt.Sprintf("group_id = $%d", idx))
		args = append(args, groupID)
		idx++
	}
	if leaderEmail != "" {
		conditions = append(conditions, fmt.Sprintf("leader_email ILIKE $%d", idx))
		args = append(args, "%"+strings.ReplaceAll(leaderEmail, "%", "\\%")+"%")
		idx++
	}

	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}

	var total int
	if err := s.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s%s", table, where), args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count groups: %w", err)
	}

	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf(`SELECT id, group_id, date, leader_email, member_email, family_status, created_at, updated_at
		FROM %s%s ORDER BY id ASC LIMIT $%d OFFSET $%d`, table, where, len(args)-1, len(args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list groups: %w", err)
	}
	defer rows.Close()

	var groups []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.GroupID, &g.Date, &g.LeaderEmail, &g.MemberEmail,
			&g.FamilyStatus, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan group: %w", err)
		}
		groups = append(groups, g)
	}
	return groups, total, rows.Err()
}

// GetGroup returns a single group by ID.
func (s *Store) GetGroup(ctx context.Context, id int64) (*Group, error) {
	table := s.tableName("account_pool_groups")
	query := fmt.Sprintf(`SELECT id, group_id, date, leader_email, member_email, family_status, created_at, updated_at
		FROM %s WHERE id = $1`, table)
	var g Group
	err := s.db.QueryRowContext(ctx, query, id).Scan(&g.ID, &g.GroupID, &g.Date, &g.LeaderEmail,
		&g.MemberEmail, &g.FamilyStatus, &g.CreatedAt, &g.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get group: %w", err)
	}
	return &g, nil
}

// CreateGroup inserts a new group.
func (s *Store) CreateGroup(ctx context.Context, g *Group) error {
	table := s.tableName("account_pool_groups")
	query := fmt.Sprintf(`INSERT INTO %s (group_id, date, leader_email, member_email, family_status)
		VALUES ($1, $2, $3, $4, $5) RETURNING id, created_at, updated_at`, table)
	date := g.Date
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	return s.db.QueryRowContext(ctx, query, g.GroupID, date, g.LeaderEmail, g.MemberEmail, g.FamilyStatus).
		Scan(&g.ID, &g.CreatedAt, &g.UpdatedAt)
}

// UpdateGroup updates an existing group.
func (s *Store) UpdateGroup(ctx context.Context, g *Group) error {
	table := s.tableName("account_pool_groups")
	query := fmt.Sprintf(`UPDATE %s SET group_id=$1, date=$2, leader_email=$3, member_email=$4,
		family_status=$5, updated_at=NOW()
		WHERE id=$6 RETURNING updated_at`, table)
	err := s.db.QueryRowContext(ctx, query, g.GroupID, g.Date, g.LeaderEmail, g.MemberEmail,
		g.FamilyStatus, g.ID).Scan(&g.UpdatedAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("group not found")
	}
	return err
}

// DeleteGroup removes a group by ID.
func (s *Store) DeleteGroup(ctx context.Context, id int64) error {
	table := s.tableName("account_pool_groups")
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id=$1", table), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("group not found")
	}
	return nil
}
