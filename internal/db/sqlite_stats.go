package db

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ============================================================
// SQLite 统计存储 — StatsStore 接口实现
// 基于 request_logs 表的请求日志与聚合统计
// ============================================================

// ------------------------------------------------------------
// RecordRequest 写入单条请求日志
// 所有字段直接映射到 request_logs 表列
// ------------------------------------------------------------

func (s *SQLiteStore) RecordRequest(ctx context.Context, log *RequestLog) error {
	const query = `
		INSERT INTO request_logs (
			user_id, model, provider, credential_id,
			input_tokens, output_tokens, latency_ms,
			status_code, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

	// 若调用方未指定时间，自动填充当前时刻
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now()
	}

	result, err := s.db.ExecContext(ctx, query,
		log.UserID, log.Model, log.Provider, log.CredentialID,
		log.InputTokens, log.OutputTokens, log.Latency,
		log.StatusCode, log.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("写入请求日志失败: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("获取请求日志 ID 失败: %w", err)
	}
	log.ID = id

	return nil
}

// ------------------------------------------------------------
// GetRequestStats 获取全局请求统计
// 支持按时间区间和模型过滤
// 分三步查询: 聚合指标 -> 按模型分组 -> 按提供商分组
// ------------------------------------------------------------

func (s *SQLiteStore) GetRequestStats(ctx context.Context, opts RequestStatsOpts) (*RequestStats, error) {
	// ---- 构建 WHERE 条件 ----
	where, args := buildStatsWhere(opts)

	stats := &RequestStats{
		ByModel:    make(map[string]int64),
		ByProvider: make(map[string]int64),
	}

	// ---- 第一步: 聚合指标（总请求数、总 token、平均延迟）----
	aggQuery := fmt.Sprintf(`
		SELECT
			COALESCE(COUNT(*), 0),
			COALESCE(SUM(input_tokens + output_tokens), 0),
			COALESCE(AVG(latency_ms), 0)
		FROM request_logs
		%s
	`, where)

	err := s.db.QueryRowContext(ctx, aggQuery, args...).Scan(
		&stats.TotalRequests,
		&stats.TotalTokens,
		&stats.AvgLatency,
	)
	if err != nil {
		return nil, fmt.Errorf("查询聚合统计失败: %w", err)
	}

	// ---- 第二步: 按模型分组 ----
	if err := s.fillGroupStats(ctx, stats.ByModel, "model", where, args); err != nil {
		return nil, fmt.Errorf("查询模型分组统计失败: %w", err)
	}

	// ---- 第三步: 按提供商分组 ----
	if err := s.fillGroupStats(ctx, stats.ByProvider, "provider", where, args); err != nil {
		return nil, fmt.Errorf("查询提供商分组统计失败: %w", err)
	}

	return stats, nil
}

// ------------------------------------------------------------
// GetUserRequestStats 获取指定用户的请求统计
// 在全局条件基础上追加 user_id 过滤
// ------------------------------------------------------------

func (s *SQLiteStore) GetUserRequestStats(ctx context.Context, userID int64, opts RequestStatsOpts) (*UserRequestStats, error) {
	// ---- 构建 WHERE 条件（含 user_id）----
	where, args := buildUserStatsWhere(userID, opts)

	stats := &UserRequestStats{
		UserID:  userID,
		ByModel: make(map[string]int64),
	}

	// ---- 聚合指标 ----
	aggQuery := fmt.Sprintf(`
		SELECT
			COALESCE(COUNT(*), 0),
			COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM request_logs
		%s
	`, where)

	err := s.db.QueryRowContext(ctx, aggQuery, args...).Scan(
		&stats.TotalRequests,
		&stats.TotalTokens,
	)
	if err != nil {
		return nil, fmt.Errorf("查询用户 [%d] 聚合统计失败: %w", userID, err)
	}

	// ---- 按模型分组 ----
	if err := s.fillGroupStats(ctx, stats.ByModel, "model", where, args); err != nil {
		return nil, fmt.Errorf("查询用户 [%d] 模型分组统计失败: %w", userID, err)
	}

	return stats, nil
}

// ============================================================
// 内部辅助函数
// ============================================================

// fillGroupStats 通用分组聚合查询，将结果填充到目标 map
// groupCol 指定分组列名（model / provider）
func (s *SQLiteStore) fillGroupStats(
	ctx context.Context,
	target map[string]int64,
	groupCol string,
	where string,
	args []any,
) error {
	query := fmt.Sprintf(`
		SELECT %s, COUNT(*)
		FROM request_logs
		%s
		GROUP BY %s
	`, groupCol, where, groupCol)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var key string
		var count int64
		if err := rows.Scan(&key, &count); err != nil {
			return err
		}
		target[key] = count
	}
	return rows.Err()
}

// buildStatsWhere 根据 RequestStatsOpts 构建 WHERE 子句
// 返回带占位符的 SQL 片段和对应参数切片
func buildStatsWhere(opts RequestStatsOpts) (string, []any) {
	var conds []string
	var args []any

	if opts.After != nil {
		conds = append(conds, "created_at >= ?")
		args = append(args, *opts.After)
	}
	if opts.Before != nil {
		conds = append(conds, "created_at < ?")
		args = append(args, *opts.Before)
	}
	if opts.Model != nil {
		conds = append(conds, "model = ?")
		args = append(args, *opts.Model)
	}

	if len(conds) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(conds, " AND "), args
}

// buildUserStatsWhere 在 buildStatsWhere 基础上追加 user_id 条件
func buildUserStatsWhere(userID int64, opts RequestStatsOpts) (string, []any) {
	conds := []string{"user_id = ?"}
	args := []any{userID}

	if opts.After != nil {
		conds = append(conds, "created_at >= ?")
		args = append(args, *opts.After)
	}
	if opts.Before != nil {
		conds = append(conds, "created_at < ?")
		args = append(args, *opts.Before)
	}
	if opts.Model != nil {
		conds = append(conds, "model = ?")
		args = append(args, *opts.Model)
	}

	return "WHERE " + strings.Join(conds, " AND "), args
}

// ------------------------------------------------------------
// 编译期接口检查 — 确保 SQLiteStore 实现 StatsStore
// ------------------------------------------------------------

var _ StatsStore = (*SQLiteStore)(nil)
