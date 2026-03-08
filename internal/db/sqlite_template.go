package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ============================================================
// SQLite — 兑换码模板 TemplateStore 实现
// ============================================================

// CreateTemplate 创建兑换码模板
func (s *SQLiteStore) CreateTemplate(ctx context.Context, tpl *RedemptionTemplate) error {
	bonusJSON, err := json.Marshal(tpl.BonusQuota)
	if err != nil {
		return fmt.Errorf("序列化 bonus_quota 失败: %w", err)
	}

	enabled := 0
	if tpl.Enabled {
		enabled = 1
	}
	now := time.Now()

	result, err := s.db.ExecContext(ctx,
		`INSERT INTO redemption_templates
			(name, description, bonus_quota, max_per_user, total_limit, issued_count, enabled, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		tpl.Name, tpl.Description, string(bonusJSON),
		tpl.MaxPerUser, tpl.TotalLimit, tpl.IssuedCount,
		enabled, now,
	)
	if err != nil {
		return fmt.Errorf("插入模板失败: %w", err)
	}

	id, _ := result.LastInsertId()
	tpl.ID = id
	tpl.CreatedAt = now
	return nil
}

// GetTemplateByID 根据 ID 查询模板
func (s *SQLiteStore) GetTemplateByID(ctx context.Context, id int64) (*RedemptionTemplate, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, description, bonus_quota, max_per_user,
		        total_limit, issued_count, enabled, created_at
		 FROM redemption_templates WHERE id = ?`, id)
	return scanTemplate(row)
}

// ListTemplates 列出所有已启用的模板
func (s *SQLiteStore) ListTemplates(ctx context.Context) ([]*RedemptionTemplate, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, description, bonus_quota, max_per_user,
		        total_limit, issued_count, enabled, created_at
		 FROM redemption_templates WHERE enabled = 1
		 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("查询模板列表失败: %w", err)
	}
	defer rows.Close()

	var list []*RedemptionTemplate
	for rows.Next() {
		tpl, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, tpl)
	}
	return list, rows.Err()
}

// IncrementTemplateIssuedCount 原子递增模板已发放数
// WHERE 条件同时检查 issued_count < total_limit，防止 TOCTOU 竞态
func (s *SQLiteStore) IncrementTemplateIssuedCount(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx,
		`UPDATE redemption_templates SET issued_count = issued_count + 1 WHERE id = ? AND issued_count < total_limit`, id)
	if err != nil {
		return fmt.Errorf("更新模板发放数失败: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("模板已发放完毕或不存在 (id=%d)", id)
	}
	return nil
}

// DecrementTemplateIssuedCount 补偿性递减模板已发放数（后续步骤失败时回滚）
func (s *SQLiteStore) DecrementTemplateIssuedCount(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE redemption_templates SET issued_count = issued_count - 1 WHERE id = ? AND issued_count > 0`, id)
	if err != nil {
		return fmt.Errorf("回滚模板发放数失败: %w", err)
	}
	return nil
}

// CountTemplateClaimsByUser 统计用户对某模板的领取次数
// 通过 invite_codes 表统计: creator_id = userID 且 code 关联到该模板
// 由于当前 invite_codes 没有 template_id 字段，此处通过 bonus_quota JSON 匹配
// 简化实现：统计 creator_id=userID 的 admin_created 邀请码数量
func (s *SQLiteStore) CountTemplateClaimsByUser(ctx context.Context, userID, templateID int64) (int, error) {
	// 获取模板的 bonus_quota 作为匹配条件
	tpl, err := s.GetTemplateByID(ctx, templateID)
	if err != nil {
		return 0, err
	}

	bonusJSON, err := json.Marshal(tpl.BonusQuota)
	if err != nil {
		return 0, fmt.Errorf("序列化 bonus_quota 失败: %w", err)
	}

	var count int
	err = s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM invite_codes
		 WHERE creator_id = ? AND type = ? AND bonus_quota = ?`,
		userID, string(InviteAdminCreated), string(bonusJSON),
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("统计用户领取数失败: %w", err)
	}
	return count, nil
}

// ============================================================
// 内部扫描器
// ============================================================

// scanner 通用扫描接口
type templateScanner interface {
	Scan(dest ...any) error
}

func scanTemplate(sc templateScanner) (*RedemptionTemplate, error) {
	var tpl RedemptionTemplate
	var bonusJSON string
	var enabledInt int

	err := sc.Scan(
		&tpl.ID, &tpl.Name, &tpl.Description, &bonusJSON,
		&tpl.MaxPerUser, &tpl.TotalLimit, &tpl.IssuedCount,
		&enabledInt, &tpl.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("扫描模板行失败: %w", err)
	}

	tpl.Enabled = enabledInt != 0

	if err := json.Unmarshal([]byte(bonusJSON), &tpl.BonusQuota); err != nil {
		return nil, fmt.Errorf("反序列化 bonus_quota 失败: %w", err)
	}
	return &tpl, nil
}
