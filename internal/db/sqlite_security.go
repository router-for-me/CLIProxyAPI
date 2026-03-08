package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ============================================================
// SQLite 安全存储 — SecurityStore 接口实现
// 管理 IP 规则、风险标记、异常事件与审计日志
// ============================================================

// ------------------------------------------------------------
// 编译期接口检查 — 确保 SQLiteStore 实现 SecurityStore
// ------------------------------------------------------------

var _ SecurityStore = (*SQLiteStore)(nil)

// ============================================================
// IP 规则管理 (ip_rules)
// ============================================================

// ------------------------------------------------------------
// CreateIPRule 创建 IP 黑白名单规则
// rule_type 为 "whitelist" 或 "blacklist"
// ------------------------------------------------------------

func (s *SQLiteStore) CreateIPRule(ctx context.Context, rule *IPRule) error {
	const query = `
		INSERT INTO ip_rules (cidr, rule_type, comment, created_at)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	`

	result, err := s.db.ExecContext(ctx, query,
		rule.CIDR,
		rule.RuleType,
		rule.Comment,
	)
	if err != nil {
		return fmt.Errorf("创建 IP 规则失败: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("获取 IP 规则 ID 失败: %w", err)
	}
	rule.ID = id
	rule.CreatedAt = time.Now()
	return nil
}

// ------------------------------------------------------------
// ListIPRules 获取全部 IP 规则
// 按创建时间降序排列（最新规则优先）
// ------------------------------------------------------------

func (s *SQLiteStore) ListIPRules(ctx context.Context) ([]*IPRule, error) {
	const query = `
		SELECT id, cidr, rule_type, comment, created_at
		FROM ip_rules
		ORDER BY created_at DESC
	`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询 IP 规则列表失败: %w", err)
	}
	defer rows.Close()

	var rules []*IPRule
	for rows.Next() {
		r := &IPRule{}
		if err := rows.Scan(&r.ID, &r.CIDR, &r.RuleType, &r.Comment, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("扫描 IP 规则行失败: %w", err)
		}
		rules = append(rules, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历 IP 规则结果集失败: %w", err)
	}
	return rules, nil
}

// ------------------------------------------------------------
// DeleteIPRule 根据 ID 删除 IP 规则
// 删除不存在的记录时静默成功（幂等操作）
// ------------------------------------------------------------

func (s *SQLiteStore) DeleteIPRule(ctx context.Context, id int64) error {
	const query = `DELETE FROM ip_rules WHERE id = ?`

	_, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("删除 IP 规则 [%d] 失败: %w", id, err)
	}
	return nil
}

// ============================================================
// 风险标记管理 (risk_marks)
// ============================================================

// ------------------------------------------------------------
// CreateRiskMark 创建用户风险标记
// 支持自动检测与手动标记两种来源
// ------------------------------------------------------------

func (s *SQLiteStore) CreateRiskMark(ctx context.Context, mark *UserRiskMark) error {
	const query = `
		INSERT INTO risk_marks (user_id, mark_type, reason, marked_at, expires_at, auto_applied)
		VALUES (?, ?, ?, CURRENT_TIMESTAMP, ?, ?)
	`

	result, err := s.db.ExecContext(ctx, query,
		mark.UserID,
		mark.MarkType,
		mark.Reason,
		mark.ExpiresAt,
		mark.AutoApplied,
	)
	if err != nil {
		return fmt.Errorf("创建风险标记失败: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("获取风险标记 ID 失败: %w", err)
	}
	mark.ID = id
	mark.MarkedAt = time.Now()
	return nil
}

// ------------------------------------------------------------
// GetActiveRiskMarks 获取用户当前生效的风险标记
// 仅返回 expires_at 晚于当前时间的记录
// ------------------------------------------------------------

func (s *SQLiteStore) GetActiveRiskMarks(ctx context.Context, userID int64) ([]*UserRiskMark, error) {
	const query = `
		SELECT id, user_id, mark_type, reason, marked_at, expires_at, auto_applied
		FROM risk_marks
		WHERE user_id = ? AND expires_at > CURRENT_TIMESTAMP
		ORDER BY marked_at DESC
	`

	rows, err := s.db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("查询用户 [%d] 活跃风险标记失败: %w", userID, err)
	}
	defer rows.Close()

	var marks []*UserRiskMark
	for rows.Next() {
		m := &UserRiskMark{}
		if err := rows.Scan(
			&m.ID, &m.UserID, &m.MarkType, &m.Reason,
			&m.MarkedAt, &m.ExpiresAt, &m.AutoApplied,
		); err != nil {
			return nil, fmt.Errorf("扫描风险标记行失败: %w", err)
		}
		marks = append(marks, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历风险标记结果集失败: %w", err)
	}
	return marks, nil
}

// ------------------------------------------------------------
// ListRiskMarks 分页查询风险标记列表
// 支持按 user_id 和活跃状态筛选
// 返回当前页数据与符合条件的总记录数
// ------------------------------------------------------------

func (s *SQLiteStore) ListRiskMarks(ctx context.Context, opts ListRiskMarksOpts) ([]*UserRiskMark, int64, error) {
	// -- 动态构建 WHERE 子句 --
	var conditions []string
	var args []any

	if opts.UserID != nil {
		conditions = append(conditions, "user_id = ?")
		args = append(args, *opts.UserID)
	}
	if opts.Active != nil {
		if *opts.Active {
			conditions = append(conditions, "expires_at > CURRENT_TIMESTAMP")
		} else {
			conditions = append(conditions, "expires_at <= CURRENT_TIMESTAMP")
		}
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// -- 查询总数 --
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM risk_marks %s`, where)
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计风险标记总数失败: %w", err)
	}

	// -- 分页查询 --
	page, pageSize := normalizePage(opts.Page, opts.PageSize)
	offset := (page - 1) * pageSize

	dataQuery := fmt.Sprintf(
		`SELECT id, user_id, mark_type, reason, marked_at, expires_at, auto_applied
		 FROM risk_marks %s ORDER BY marked_at DESC LIMIT ? OFFSET ?`,
		where,
	)
	dataArgs := append(args, pageSize, offset) //nolint:gocritic // 此处 append 到新切片是预期行为

	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询风险标记列表失败: %w", err)
	}
	defer rows.Close()

	var marks []*UserRiskMark
	for rows.Next() {
		m := &UserRiskMark{}
		if err := rows.Scan(
			&m.ID, &m.UserID, &m.MarkType, &m.Reason,
			&m.MarkedAt, &m.ExpiresAt, &m.AutoApplied,
		); err != nil {
			return nil, 0, fmt.Errorf("扫描风险标记行失败: %w", err)
		}
		marks = append(marks, m)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历风险标记结果集失败: %w", err)
	}
	return marks, total, nil
}

// ------------------------------------------------------------
// ExpireRiskMarks 清理已过期的风险标记
// 删除 expires_at 早于或等于当前时间的记录
// 返回删除的行数
// ------------------------------------------------------------

func (s *SQLiteStore) ExpireRiskMarks(ctx context.Context) (int64, error) {
	const query = `DELETE FROM risk_marks WHERE expires_at <= CURRENT_TIMESTAMP`

	result, err := s.db.ExecContext(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("清理过期风险标记失败: %w", err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("获取过期清理影响行数失败: %w", err)
	}
	return affected, nil
}

// ============================================================
// 异常事件记录 (anomaly_events)
// ============================================================

// ------------------------------------------------------------
// RecordAnomalyEvent 记录一条异常事件
// user_id 可为 nil（匿名 / 未认证请求）
// ------------------------------------------------------------

func (s *SQLiteStore) RecordAnomalyEvent(ctx context.Context, event *AnomalyEvent) error {
	const query = `
		INSERT INTO anomaly_events (user_id, ip, event_type, detail, action, created_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`

	var userID any
	if event.UserID != nil {
		userID = *event.UserID
	}

	result, err := s.db.ExecContext(ctx, query,
		userID,
		event.IP,
		event.EventType,
		event.Detail,
		event.Action,
	)
	if err != nil {
		return fmt.Errorf("记录异常事件失败: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("获取异常事件 ID 失败: %w", err)
	}
	event.ID = id
	event.CreatedAt = time.Now()
	return nil
}

// ------------------------------------------------------------
// ListAnomalyEvents 分页查询异常事件列表
// 支持按 user_id 和时间范围筛选
// 返回当前页数据与符合条件的总记录数
// ------------------------------------------------------------

func (s *SQLiteStore) ListAnomalyEvents(ctx context.Context, opts ListAnomalyEventsOpts) ([]*AnomalyEvent, int64, error) {
	// -- 动态构建 WHERE 子句 --
	var conditions []string
	var args []any

	if opts.UserID != nil {
		conditions = append(conditions, "user_id = ?")
		args = append(args, *opts.UserID)
	}
	if opts.After != nil {
		conditions = append(conditions, "created_at > ?")
		args = append(args, *opts.After)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// -- 查询总数 --
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM anomaly_events %s`, where)
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计异常事件总数失败: %w", err)
	}

	// -- 分页查询 --
	page, pageSize := normalizePage(opts.Page, opts.PageSize)
	offset := (page - 1) * pageSize

	dataQuery := fmt.Sprintf(
		`SELECT id, user_id, ip, event_type, detail, action, created_at
		 FROM anomaly_events %s ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		where,
	)
	dataArgs := append(args, pageSize, offset) //nolint:gocritic // 此处 append 到新切片是预期行为

	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询异常事件列表失败: %w", err)
	}
	defer rows.Close()

	var events []*AnomalyEvent
	for rows.Next() {
		e := &AnomalyEvent{}
		var userID sql.NullInt64
		if err := rows.Scan(
			&e.ID, &userID, &e.IP, &e.EventType,
			&e.Detail, &e.Action, &e.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("扫描异常事件行失败: %w", err)
		}
		if userID.Valid {
			e.UserID = &userID.Int64
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历异常事件结果集失败: %w", err)
	}
	return events, total, nil
}

// ============================================================
// 审计日志 (audit_logs)
// ============================================================

// ------------------------------------------------------------
// CreateAuditLog 创建审计日志条目
// user_id 可为 nil（系统级操作）
// ------------------------------------------------------------

func (s *SQLiteStore) CreateAuditLog(ctx context.Context, log *AuditLog) error {
	const query = `
		INSERT INTO audit_logs (user_id, action, target, detail, ip, created_at)
		VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`

	var userID any
	if log.UserID != nil {
		userID = *log.UserID
	}

	result, err := s.db.ExecContext(ctx, query,
		userID,
		log.Action,
		log.Target,
		log.Detail,
		log.IP,
	)
	if err != nil {
		return fmt.Errorf("创建审计日志失败: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("获取审计日志 ID 失败: %w", err)
	}
	log.ID = id
	log.CreatedAt = time.Now()
	return nil
}

// ------------------------------------------------------------
// ListAuditLogs 分页查询审计日志
// 支持按 user_id / action / 时间范围筛选
// 返回当前页数据与符合条件的总记录数
// ------------------------------------------------------------

func (s *SQLiteStore) ListAuditLogs(ctx context.Context, opts ListAuditLogsOpts) ([]*AuditLog, int64, error) {
	// -- 动态构建 WHERE 子句 --
	var conditions []string
	var args []any

	if opts.UserID != nil {
		conditions = append(conditions, "user_id = ?")
		args = append(args, *opts.UserID)
	}
	if opts.Action != nil {
		conditions = append(conditions, "action = ?")
		args = append(args, *opts.Action)
	}
	if opts.After != nil {
		conditions = append(conditions, "created_at > ?")
		args = append(args, *opts.After)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// -- 查询总数 --
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM audit_logs %s`, where)
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计审计日志总数失败: %w", err)
	}

	// -- 分页查询 --
	page, pageSize := normalizePage(opts.Page, opts.PageSize)
	offset := (page - 1) * pageSize

	dataQuery := fmt.Sprintf(
		`SELECT id, user_id, action, target, detail, ip, created_at
		 FROM audit_logs %s ORDER BY created_at DESC LIMIT ? OFFSET ?`,
		where,
	)
	dataArgs := append(args, pageSize, offset) //nolint:gocritic // 此处 append 到新切片是预期行为

	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询审计日志列表失败: %w", err)
	}
	defer rows.Close()

	var logs []*AuditLog
	for rows.Next() {
		l := &AuditLog{}
		var userID sql.NullInt64
		if err := rows.Scan(
			&l.ID, &userID, &l.Action, &l.Target,
			&l.Detail, &l.IP, &l.CreatedAt,
		); err != nil {
			return nil, 0, fmt.Errorf("扫描审计日志行失败: %w", err)
		}
		if userID.Valid {
			l.UserID = &userID.Int64
		}
		logs = append(logs, l)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历审计日志结果集失败: %w", err)
	}
	return logs, total, nil
}
