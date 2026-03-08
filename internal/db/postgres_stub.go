package db

import (
	"context"
	"fmt"
)

// ============================================================
// PostgreSQL 存储后端 — 占位实现
// 完整实现将在后续阶段补充
// ============================================================

// PostgresStore 基于 PostgreSQL 的存储实现（占位）
type PostgresStore struct{}

// NewPostgresStore 创建 PostgreSQL 存储实例
// TODO: 后续阶段实现完整的 PostgreSQL 后端
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	return nil, fmt.Errorf("PostgreSQL 后端尚未实现，请使用 SQLite（driver: sqlite）")
}
