package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ============================================================
// SQLite 系统设置存储 — SettingsStore 接口实现
// 基于 system_settings 表的键值对存储
// ============================================================

// ------------------------------------------------------------
// GetSetting 根据键获取单个配置值
// 若键不存在则返回空字符串与 sql.ErrNoRows
// ------------------------------------------------------------

func (s *SQLiteStore) GetSetting(ctx context.Context, key string) (string, error) {
	const query = `SELECT value FROM system_settings WHERE key = ?`

	var value string
	err := s.db.QueryRowContext(ctx, query, key).Scan(&value)
	if err != nil {
		return "", fmt.Errorf("获取设置 [%s] 失败: %w", key, err)
	}
	return value, nil
}

// ------------------------------------------------------------
// SetSetting 设置配置项，不存在则插入，已存在则替换
// 使用 INSERT OR REPLACE 实现 upsert 语义
// ------------------------------------------------------------

func (s *SQLiteStore) SetSetting(ctx context.Context, key, value string) error {
	const query = `INSERT OR REPLACE INTO system_settings (key, value) VALUES (?, ?)`

	_, err := s.db.ExecContext(ctx, query, key, value)
	if err != nil {
		return fmt.Errorf("设置 [%s] 失败: %w", key, err)
	}
	return nil
}

// ------------------------------------------------------------
// GetAllSettings 获取全部配置，返回 key->value 映射
// 结果集为空时返回空 map 而非 nil
// ------------------------------------------------------------

func (s *SQLiteStore) GetAllSettings(ctx context.Context) (map[string]string, error) {
	const query = `SELECT key, value FROM system_settings`

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("查询所有设置失败: %w", err)
	}
	defer rows.Close()

	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("扫描设置行失败: %w", err)
		}
		settings[k] = v
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("遍历设置结果集失败: %w", err)
	}
	return settings, nil
}

// ------------------------------------------------------------
// DeleteSetting 删除指定键的配置项
// 键不存在时静默成功（幂等操作）
// ------------------------------------------------------------

func (s *SQLiteStore) DeleteSetting(ctx context.Context, key string) error {
	const query = `DELETE FROM system_settings WHERE key = ?`

	_, err := s.db.ExecContext(ctx, query, key)
	if err != nil {
		return fmt.Errorf("删除设置 [%s] 失败: %w", key, err)
	}
	return nil
}

// ------------------------------------------------------------
// 编译期接口检查 — 确保 SQLiteStore 实现 SettingsStore
// ------------------------------------------------------------

var _ SettingsStore = (*SQLiteStore)(nil)

// ErrSettingNotFound 配置项未找到的哨兵错误（包装 sql.ErrNoRows）
var ErrSettingNotFound = sql.ErrNoRows
