package accountpool

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// GroupRun represents a single group execution for a given date.
type GroupRun struct {
	ID          int64           `json:"id"`
	GroupID     int             `json:"group_id"`
	RunDate     string          `json:"run_date"`
	LeaderID    int64           `json:"leader_id"`
	LeaderProxy string          `json:"leader_proxy"`
	Status      string          `json:"status"`
	ToRemove    json.RawMessage `json:"to_remove"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// GroupMember represents a member assignment within a group run.
type GroupMember struct {
	ID         int64     `json:"id"`
	GroupRunID int64     `json:"group_run_id"`
	MemberID   int64     `json:"member_id"`
	Proxy      string    `json:"proxy"`
	Port       int       `json:"port"`
	ProfileID  string    `json:"profile_id"`
	Status     string    `json:"status"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// GroupRunWithMembers combines a run with its members for the detail view.
type GroupRunWithMembers struct {
	GroupRun
	Members []GroupMember `json:"members"`
}

// ListGroupRuns returns group runs filtered by optional date, group_id, and status.
func (s *Store) ListGroupRuns(ctx context.Context, date string, groupID int, status string, limit, offset int) ([]GroupRun, int, error) {
	table := s.tableName("account_pool_group_runs")
	var conditions []string
	var args []interface{}
	idx := 1

	if date != "" {
		conditions = append(conditions, fmt.Sprintf("run_date = $%d", idx))
		args = append(args, date)
		idx++
	}
	if groupID >= 0 {
		conditions = append(conditions, fmt.Sprintf("group_id = $%d", idx))
		args = append(args, groupID)
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
		return nil, 0, fmt.Errorf("count group_runs: %w", err)
	}

	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit, offset)
	query := fmt.Sprintf(`SELECT id, group_id, run_date, leader_id, leader_proxy, status, to_remove, created_at, updated_at
		FROM %s%s ORDER BY group_id ASC LIMIT $%d OFFSET $%d`, table, where, idx, idx+1)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list group_runs: %w", err)
	}
	defer rows.Close()

	var runs []GroupRun
	for rows.Next() {
		var r GroupRun
		if err := rows.Scan(&r.ID, &r.GroupID, &r.RunDate, &r.LeaderID, &r.LeaderProxy,
			&r.Status, &r.ToRemove, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan group_run: %w", err)
		}
		runs = append(runs, r)
	}
	return runs, total, rows.Err()
}

// GetGroupRun returns a single group run by ID.
func (s *Store) GetGroupRun(ctx context.Context, id int64) (*GroupRun, error) {
	table := s.tableName("account_pool_group_runs")
	query := fmt.Sprintf(`SELECT id, group_id, run_date, leader_id, leader_proxy, status, to_remove, created_at, updated_at
		FROM %s WHERE id = $1`, table)
	var r GroupRun
	err := s.db.QueryRowContext(ctx, query, id).Scan(&r.ID, &r.GroupID, &r.RunDate, &r.LeaderID,
		&r.LeaderProxy, &r.Status, &r.ToRemove, &r.CreatedAt, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get group_run: %w", err)
	}
	return &r, nil
}

// GetGroupRunMembers returns all members for a given group run.
func (s *Store) GetGroupRunMembers(ctx context.Context, runID int64) ([]GroupMember, error) {
	table := s.tableName("account_pool_group_members")
	query := fmt.Sprintf(`SELECT id, group_run_id, member_id, proxy, port, profile_id, status, message, created_at, updated_at
		FROM %s WHERE group_run_id = $1 ORDER BY port ASC`, table)
	rows, err := s.db.QueryContext(ctx, query, runID)
	if err != nil {
		return nil, fmt.Errorf("list group_members: %w", err)
	}
	defer rows.Close()

	var members []GroupMember
	for rows.Next() {
		var m GroupMember
		if err := rows.Scan(&m.ID, &m.GroupRunID, &m.MemberID, &m.Proxy, &m.Port,
			&m.ProfileID, &m.Status, &m.Message, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan group_member: %w", err)
		}
		members = append(members, m)
	}
	return members, rows.Err()
}

// GetGroupRunWithMembers returns a group run with all its members.
func (s *Store) GetGroupRunWithMembers(ctx context.Context, id int64) (*GroupRunWithMembers, error) {
	run, err := s.GetGroupRun(ctx, id)
	if err != nil || run == nil {
		return nil, err
	}
	members, err := s.GetGroupRunMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	if members == nil {
		members = []GroupMember{}
	}
	return &GroupRunWithMembers{GroupRun: *run, Members: members}, nil
}

// CreateGroupRun inserts a new group run.
func (s *Store) CreateGroupRun(ctx context.Context, r *GroupRun) error {
	table := s.tableName("account_pool_group_runs")
	query := fmt.Sprintf(`INSERT INTO %s (group_id, run_date, leader_id, leader_proxy, status, to_remove)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at, updated_at`, table)
	date := r.RunDate
	if date == "" {
		date = time.Now().Format("2006-01-02")
	}
	if r.Status == "" {
		r.Status = "pending"
	}
	toRemove := r.ToRemove
	if toRemove == nil {
		toRemove = json.RawMessage("[]")
	}
	return s.db.QueryRowContext(ctx, query, r.GroupID, date, r.LeaderID, r.LeaderProxy, r.Status, toRemove).
		Scan(&r.ID, &r.CreatedAt, &r.UpdatedAt)
}

// UpdateGroupRun updates an existing group run.
func (s *Store) UpdateGroupRun(ctx context.Context, r *GroupRun) error {
	table := s.tableName("account_pool_group_runs")
	query := fmt.Sprintf(`UPDATE %s SET group_id=$1, run_date=$2, leader_id=$3, leader_proxy=$4,
		status=$5, to_remove=$6, updated_at=NOW()
		WHERE id=$7 RETURNING updated_at`, table)
	err := s.db.QueryRowContext(ctx, query, r.GroupID, r.RunDate, r.LeaderID, r.LeaderProxy,
		r.Status, r.ToRemove, r.ID).Scan(&r.UpdatedAt)
	if err == sql.ErrNoRows {
		return fmt.Errorf("group_run not found")
	}
	return err
}

// DeleteGroupRun removes a group run and its members (cascade).
func (s *Store) DeleteGroupRun(ctx context.Context, id int64) error {
	table := s.tableName("account_pool_group_runs")
	res, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id=$1", table), id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("group_run not found")
	}
	return nil
}

// AddGroupMembers inserts multiple members into a group run.
func (s *Store) AddGroupMembers(ctx context.Context, runID int64, members []GroupMember) (int, error) {
	table := s.tableName("account_pool_group_members")
	created := 0
	for i := range members {
		m := &members[i]
		m.GroupRunID = runID
		if m.Status == "" {
			m.Status = "new"
		}
		query := fmt.Sprintf(`INSERT INTO %s (group_run_id, member_id, proxy, port, profile_id, status, message)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id, created_at, updated_at`, table)
		err := s.db.QueryRowContext(ctx, query, m.GroupRunID, m.MemberID, m.Proxy, m.Port,
			m.ProfileID, m.Status, m.Message).Scan(&m.ID, &m.CreatedAt, &m.UpdatedAt)
		if err != nil {
			return created, fmt.Errorf("add group_member %d: %w", m.MemberID, err)
		}
		created++
	}
	return created, nil
}

// UpdateGroupMember updates fields of a single group member.
func (s *Store) UpdateGroupMember(ctx context.Context, runID, memberID int64, fields map[string]interface{}) error {
	table := s.tableName("account_pool_group_members")

	allowed := map[string]bool{"proxy": true, "port": true, "profile_id": true, "status": true, "message": true}
	var sets []string
	var args []interface{}
	idx := 1
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		sets = append(sets, fmt.Sprintf("%s = $%d", k, idx))
		args = append(args, v)
		idx++
	}
	if len(sets) == 0 {
		return fmt.Errorf("no valid fields to update")
	}
	sets = append(sets, "updated_at = NOW()")
	args = append(args, runID, memberID)
	query := fmt.Sprintf(`UPDATE %s SET %s WHERE group_run_id = $%d AND member_id = $%d`,
		table, strings.Join(sets, ", "), idx, idx+1)
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update group_member: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("group_member not found")
	}
	return nil
}

// UpdateGroupMemberStatus is a shorthand to update status and message.
func (s *Store) UpdateGroupMemberStatus(ctx context.Context, runID, memberID int64, status, message string) error {
	return s.UpdateGroupMember(ctx, runID, memberID, map[string]interface{}{
		"status":  status,
		"message": message,
	})
}

// DeleteGroupMembers removes all members from a group run.
func (s *Store) DeleteGroupMembers(ctx context.Context, runID int64) error {
	table := s.tableName("account_pool_group_members")
	_, err := s.db.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE group_run_id = $1", table), runID)
	return err
}

// ReplaceMemberResult holds the result of a member replacement.
type ReplaceMemberResult struct {
	OldMemberID int64                  `json:"old_member_id"`
	OldEmail    string                 `json:"old_email"`
	NewMember   map[string]interface{} `json:"new_member"`
}

// ReplaceMember atomically replaces a failed member in a group run with a new available member.
// It marks the old member's pool status to the given reason, picks a new available member,
// and updates the group_member row to point to the new member.
func (s *Store) ReplaceMember(ctx context.Context, runID, memberID int64, reason string, reuseProxy bool) (*ReplaceMemberResult, error) {
	gmTable := s.tableName("account_pool_group_members")
	poolTable := s.tableName("account_pool_members")

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// 1. Lock and fetch the existing group_member row
	var gmID, oldPoolMemberID int64
	var proxy string
	var port int
	gmQuery := fmt.Sprintf(`SELECT gm.id, gm.member_id, gm.proxy, gm.port
		FROM %s gm WHERE gm.group_run_id = $1 AND gm.member_id = $2 FOR UPDATE`, gmTable)
	err = tx.QueryRowContext(ctx, gmQuery, runID, memberID).Scan(&gmID, &oldPoolMemberID, &proxy, &port)
	if err != nil {
		return nil, fmt.Errorf("group_member not found: %w", err)
	}

	// Get old member email for reporting
	var oldEmail string
	_ = tx.QueryRowContext(ctx, fmt.Sprintf("SELECT email FROM %s WHERE id = $1", poolTable), oldPoolMemberID).Scan(&oldEmail)

	// 2. Mark old member's pool status
	if reason == "" {
		reason = "failed"
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = $1, updated_at = NOW() WHERE id = $2", poolTable), reason, oldPoolMemberID)
	if err != nil {
		return nil, fmt.Errorf("mark old member: %w", err)
	}

	// 3. Pick next available member (FOR UPDATE SKIP LOCKED)
	pickQuery := fmt.Sprintf(`SELECT id, email, password, recovery_email, totp_secret
		FROM %s WHERE status = 'available' ORDER BY id ASC LIMIT 1 FOR UPDATE SKIP LOCKED`, poolTable)
	var newID int64
	var newEmail, newPwd, newRecovery, newTOTP string
	err = tx.QueryRowContext(ctx, pickQuery).Scan(&newID, &newEmail, &newPwd, &newRecovery, &newTOTP)
	if err != nil {
		return nil, fmt.Errorf("no available member to pick: %w", err)
	}

	// 4. Mark new member as used
	_, err = tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = 'used', updated_at = NOW() WHERE id = $1", poolTable), newID)
	if err != nil {
		return nil, fmt.Errorf("mark new member used: %w", err)
	}

	// 5. Update the group_member row to point to new member
	newProxy := proxy // reuse by default
	if !reuseProxy {
		// Pick a new proxy
		proxyTable := s.tableName("account_pool_proxies")
		var proxyURL string
		proxyQuery := fmt.Sprintf(`SELECT proxy_url FROM %s WHERE type = 'member' AND status = 'available'
			ORDER BY id ASC LIMIT 1 FOR UPDATE SKIP LOCKED`, proxyTable)
		if err := tx.QueryRowContext(ctx, proxyQuery).Scan(&proxyURL); err == nil {
			newProxy = proxyURL
			_, _ = tx.ExecContext(ctx, fmt.Sprintf("UPDATE %s SET status = 'used', updated_at = NOW() WHERE proxy_url = $1", proxyTable), proxyURL)
		}
		// If no proxy available, keep old proxy
	}

	updateGM := fmt.Sprintf(`UPDATE %s SET member_id = $1, proxy = $2, profile_id = '', status = 'new',
		message = $3, updated_at = NOW() WHERE id = $4`, gmTable)
	msg := fmt.Sprintf("replaced %s (%s)", oldEmail, reason)
	_, err = tx.ExecContext(ctx, updateGM, newID, newProxy, msg, gmID)
	if err != nil {
		return nil, fmt.Errorf("update group_member: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &ReplaceMemberResult{
		OldMemberID: oldPoolMemberID,
		OldEmail:    oldEmail,
		NewMember: map[string]interface{}{
			"member_id":      newID,
			"email":          newEmail,
			"password":       newPwd,
			"recovery_email": newRecovery,
			"totp_secret":    newTOTP,
			"proxy":          newProxy,
			"port":           port,
			"status":         "new",
		},
	}, nil
}

// GetGroupRunJSON returns the groupN.json-compatible structure by JOINing credentials.
func (s *Store) GetGroupRunJSON(ctx context.Context, id int64) (map[string]interface{}, error) {
	runTable := s.tableName("account_pool_group_runs")
	leaderTable := s.tableName("account_pool_leaders")
	memberTable := s.tableName("account_pool_group_members")
	poolMemberTable := s.tableName("account_pool_members")

	// Fetch run + leader credentials
	runQuery := fmt.Sprintf(`SELECT r.id, r.group_id, r.leader_id, r.leader_proxy, r.to_remove,
		l.email, l.password, l.recovery_email, l.totp_secret, l.nstbrowser_profile_id
		FROM %s r JOIN %s l ON r.leader_id = l.id
		WHERE r.id = $1`, runTable, leaderTable)

	var (
		runID                                                          int64
		groupID                                                        int
		leaderID                                                       int64
		leaderProxy, toRemoveRaw                                       string
		leaderEmail, leaderPwd, leaderRecovery, leaderTOTP, leaderPID string
	)
	err := s.db.QueryRowContext(ctx, runQuery, id).Scan(
		&runID, &groupID, &leaderID, &leaderProxy, &toRemoveRaw,
		&leaderEmail, &leaderPwd, &leaderRecovery, &leaderTOTP, &leaderPID,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get group_run json: %w", err)
	}

	var toRemove interface{}
	if err := json.Unmarshal([]byte(toRemoveRaw), &toRemove); err != nil {
		toRemove = []interface{}{}
	}

	// Fetch members with credentials
	memQuery := fmt.Sprintf(`SELECT gm.member_id, pm.email, pm.password, pm.recovery_email, pm.totp_secret,
		gm.proxy, gm.port, gm.profile_id, gm.status, gm.message
		FROM %s gm JOIN %s pm ON gm.member_id = pm.id
		WHERE gm.group_run_id = $1 ORDER BY gm.port ASC`, memberTable, poolMemberTable)

	rows, err := s.db.QueryContext(ctx, memQuery, id)
	if err != nil {
		return nil, fmt.Errorf("get group_run json members: %w", err)
	}
	defer rows.Close()

	var members []map[string]interface{}
	for rows.Next() {
		var (
			memberID                                              int64
			port                                                  int
			email, pwd, recovery, totp, proxy, profileID, status, message string
		)
		if err := rows.Scan(&memberID, &email, &pwd, &recovery, &totp,
			&proxy, &port, &profileID, &status, &message); err != nil {
			return nil, fmt.Errorf("scan group_run json member: %w", err)
		}
		members = append(members, map[string]interface{}{
			"member_id":      memberID,
			"email":          email,
			"password":       pwd,
			"recovery_email": recovery,
			"totp_secret":    totp,
			"proxy":          proxy,
			"port":           port,
			"profile_id":     profileID,
			"status":         status,
			"message":        message,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if members == nil {
		members = []map[string]interface{}{}
	}

	return map[string]interface{}{
		"group_id": groupID,
		"leader": map[string]interface{}{
			"leader_id":      leaderID,
			"email":          leaderEmail,
			"password":       leaderPwd,
			"recovery_email": leaderRecovery,
			"totp_secret":    leaderTOTP,
			"proxy":          leaderProxy,
			"profile_id":     leaderPID,
		},
		"members":   members,
		"to_remove": toRemove,
	}, nil
}
