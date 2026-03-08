package db

import (
	"context"
	"fmt"
	"strings"
)

// ============================================================
// 存储工厂 — 根据驱动类型创建对应的存储实例
// ============================================================

// NewStore 根据驱动类型创建存储实例
// 支持的驱动: sqlite（默认）、postgres
func NewStore(ctx context.Context, driver, dsn string) (Store, error) {
	switch strings.ToLower(strings.TrimSpace(driver)) {
	case "sqlite", "":
		store, err := NewSQLiteStore(ctx, dsn)
		if err != nil {
			return nil, err
		}
		if err := store.Migrate(ctx); err != nil {
			store.Close()
			return nil, fmt.Errorf("SQLite 迁移失败: %w", err)
		}
		return store, nil
	case "postgres", "postgresql":
		store, err := NewPostgresStore(ctx, dsn)
		if err != nil {
			return nil, err
		}
		if err := store.Migrate(ctx); err != nil {
			store.Close()
			return nil, fmt.Errorf("PostgreSQL 迁移失败: %w", err)
		}
		return store, nil
	default:
		return nil, fmt.Errorf("不支持的数据库驱动: %s（支持: sqlite, postgres）", driver)
	}
}
