package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// ============================================================
// SQLite 凭证存储 — CredentialStore 接口实现
// 管理 API 凭证 (credentials) 及其健康记录 (credential_health)
// 支持公共池与用户私有凭证的分层管理
// ============================================================

// ------------------------------------------------------------
// 编译期接口检查 — 确保 SQLiteStore 实现 CredentialStore
// ------------------------------------------------------------

var _ CredentialStore = (*SQLiteStore)(nil)

// ============================================================
// 内部辅助函数
// ============================================================

// ------------------------------------------------------------
// scanCredential 从结果行扫描单条凭证记录
// 将 owner_id 的 NULL 语义正确映射为 *int64
// ------------------------------------------------------------

func scanCredential(scanner interface{ Scan(...any) error }) (*Credential, error) {
	c := &Credential{}
	var ownerID sql.NullInt64

	err := scanner.Scan(
		&c.ID,
		&c.Provider,
		&ownerID,
		&c.Data,
		&c.Health,
		&c.Weight,
		&c.Enabled,
		&c.AddedAt,
	)
	if err != nil {
		return nil, err
	}

	if ownerID.Valid {
		c.OwnerID = &ownerID.Int64
	}
	return c, nil
}

// credentialColumns 凭证表的标准查询列
const credentialColumns = `id, provider, owner_id, data, health, weight, enabled, added_at`

// ============================================================
// 凭证 CRUD
// ============================================================

// ------------------------------------------------------------
// CreateCredential 创建新的 API 凭证
// ID 由调用方在外部生成（通常为 UUID）
// ------------------------------------------------------------

func (s *SQLiteStore) CreateCredential(ctx context.Context, cred *Credential) error {
	const query = `
		INSERT INTO credentials (id, provider, owner_id, data, health, weight, enabled, added_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`

	var ownerID any
	if cred.OwnerID != nil {
		ownerID = *cred.OwnerID
	}

	_, err := s.db.ExecContext(ctx, query,
		cred.ID,
		cred.Provider,
		ownerID,
		cred.Data,
		cred.Health,
		cred.Weight,
		cred.Enabled,
	)
	if err != nil {
		return fmt.Errorf("创建凭证 [%s] 失败: %w", cred.ID, err)
	}
	cred.AddedAt = time.Now()
	return nil
}

// ------------------------------------------------------------
// GetCredentialByID 根据 ID 获取单条凭证
// 未找到时返回 sql.ErrNoRows
// ------------------------------------------------------------

func (s *SQLiteStore) GetCredentialByID(ctx context.Context, id string) (*Credential, error) {
	query := fmt.Sprintf(`SELECT %s FROM credentials WHERE id = ?`, credentialColumns)

	cred, err := scanCredential(s.db.QueryRowContext(ctx, query, id))
	if err != nil {
		return nil, fmt.Errorf("查询凭证 [%s] 失败: %w", id, err)
	}
	return cred, nil
}

// ------------------------------------------------------------
// ListCredentials 分页查询凭证列表
// 支持按 owner_id / provider / health 筛选
// 返回当前页数据与符合条件的总记录数
// ------------------------------------------------------------

func (s *SQLiteStore) ListCredentials(ctx context.Context, opts ListCredentialsOpts) ([]*Credential, int64, error) {
	// -- 动态构建 WHERE 子句 --
	var conditions []string
	var args []any

	if opts.OwnerID != nil {
		conditions = append(conditions, "owner_id = ?")
		args = append(args, *opts.OwnerID)
	}
	if opts.Provider != nil {
		conditions = append(conditions, "provider = ?")
		args = append(args, *opts.Provider)
	}
	if opts.Health != nil {
		conditions = append(conditions, "health = ?")
		args = append(args, *opts.Health)
	}

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}

	// -- 查询总数 --
	countQuery := fmt.Sprintf(`SELECT COUNT(*) FROM credentials %s`, where)
	var total int64
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("统计凭证总数失败: %w", err)
	}

	// -- 分页查询 --
	page, pageSize := normalizePage(opts.Page, opts.PageSize)
	offset := (page - 1) * pageSize

	dataQuery := fmt.Sprintf(
		`SELECT %s FROM credentials %s ORDER BY added_at DESC LIMIT ? OFFSET ?`,
		credentialColumns, where,
	)
	dataArgs := append(args, pageSize, offset) //nolint:gocritic // 此处 append 到新切片是预期行为

	rows, err := s.db.QueryContext(ctx, dataQuery, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("查询凭证列表失败: %w", err)
	}
	defer rows.Close()

	var creds []*Credential
	for rows.Next() {
		cred, err := scanCredential(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("扫描凭证行失败: %w", err)
		}
		creds = append(creds, cred)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("遍历凭证结果集失败: %w", err)
	}
	return creds, total, nil
}

// ------------------------------------------------------------
// UpdateCredential 更新凭证信息
// 按 ID 匹配，更新 provider / owner_id / data / health / weight / enabled
// ------------------------------------------------------------

func (s *SQLiteStore) UpdateCredential(ctx context.Context, cred *Credential) error {
	const query = `
		UPDATE credentials
		SET provider = ?, owner_id = ?, data = ?, health = ?, weight = ?, enabled = ?
		WHERE id = ?
	`

	var ownerID any
	if cred.OwnerID != nil {
		ownerID = *cred.OwnerID
	}

	_, err := s.db.ExecContext(ctx, query,
		cred.Provider,
		ownerID,
		cred.Data,
		cred.Health,
		cred.Weight,
		cred.Enabled,
		cred.ID,
	)
	if err != nil {
		return fmt.Errorf("更新凭证 [%s] 失败: %w", cred.ID, err)
	}
	return nil
}

// ------------------------------------------------------------
// DeleteCredential 根据 ID 删除凭证
// 删除不存在的记录时静默成功（幂等操作）
// ------------------------------------------------------------

func (s *SQLiteStore) DeleteCredential(ctx context.Context, id string) error {
	const query = `DELETE FROM credentials WHERE id = ?`

	_, err := s.db.ExecContext(ctx, query, id)
	if err != nil {
		return fmt.Errorf("删除凭证 [%s] 失败: %w", id, err)
	}
	return nil
}

// ============================================================
// 凭证池查询
// ============================================================

// ------------------------------------------------------------
// GetPublicPoolCredentials 获取公共池中可用的凭证
// 筛选条件：无 owner、指定 provider、已启用、健康状态非 down
// 按权重降序排列，优先选择高权重凭证
// ------------------------------------------------------------

func (s *SQLiteStore) GetPublicPoolCredentials(ctx context.Context, provider string) ([]*Credential, error) {
	query := fmt.Sprintf(
		`SELECT %s FROM credentials WHERE owner_id IS NULL AND provider = ? AND enabled = 1 AND health != 'down' ORDER BY weight DESC`,
		credentialColumns,
	)

	rows, err := s.db.QueryContext(ctx, query, provider)
	if err != nil {
		return nil, fmt.Errorf("查询公共池凭证 [%s] 失败: %w", provider, err)
	}
	defer rows.Close()

	var creds []*Credential
	for rows.Next() {
		cred, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描公共池凭证行失败: %w", err)
		}
		creds = append(creds, cred)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历公共池凭证结果集失败: %w", err)
	}
	return creds, nil
}

// ------------------------------------------------------------
// GetUserCredentials 获取用户私有的已启用凭证
// 按权重降序排列
// ------------------------------------------------------------

func (s *SQLiteStore) GetUserCredentials(ctx context.Context, userID int64, provider string) ([]*Credential, error) {
	query := fmt.Sprintf(
		`SELECT %s FROM credentials WHERE owner_id = ? AND provider = ? AND enabled = 1 ORDER BY weight DESC`,
		credentialColumns,
	)

	rows, err := s.db.QueryContext(ctx, query, userID, provider)
	if err != nil {
		return nil, fmt.Errorf("查询用户 [%d] 凭证 [%s] 失败: %w", userID, provider, err)
	}
	defer rows.Close()

	var creds []*Credential
	for rows.Next() {
		cred, err := scanCredential(rows)
		if err != nil {
			return nil, fmt.Errorf("扫描用户凭证行失败: %w", err)
		}
		creds = append(creds, cred)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历用户凭证结果集失败: %w", err)
	}
	return creds, nil
}

// ============================================================
// 凭证健康记录
// ============================================================

// ------------------------------------------------------------
// RecordCredentialHealth 记录一次凭证健康检查结果
// 同时更新 credentials 表中的 health 字段以保持一致
// ------------------------------------------------------------

func (s *SQLiteStore) RecordCredentialHealth(ctx context.Context, record *CredentialHealthRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开启健康记录事务失败: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // 提交后 Rollback 为无操作

	// 插入健康检查记录
	const insertQuery = `
		INSERT INTO credential_health (credential_id, status, latency_ms, error_msg, checked_at)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
	`
	result, err := tx.ExecContext(ctx, insertQuery,
		record.CredentialID,
		record.Status,
		record.Latency,
		record.ErrorMsg,
	)
	if err != nil {
		return fmt.Errorf("插入健康记录失败: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("获取健康记录 ID 失败: %w", err)
	}
	record.ID = id
	record.CheckedAt = time.Now()

	// 同步更新凭证主表的健康状态
	const updateQuery = `UPDATE credentials SET health = ? WHERE id = ?`
	if _, err := tx.ExecContext(ctx, updateQuery, record.Status, record.CredentialID); err != nil {
		return fmt.Errorf("同步更新凭证健康状态失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交健康记录事务失败: %w", err)
	}
	return nil
}

// ============================================================
// 内部工具
// ============================================================

// ------------------------------------------------------------
// normalizePage 规范化分页参数
// 保证 page >= 1 且 pageSize 在 [1, 100] 范围内
// ------------------------------------------------------------

func normalizePage(page, pageSize int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	return page, pageSize
}
