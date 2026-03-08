package db_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 存储工厂测试
// ============================================================

func TestNewStore_SQLite(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewStore(ctx, "sqlite", ":memory:")
	if err != nil {
		t.Fatalf("创建 SQLite store 失败: %v", err)
	}
	defer store.Close()
}

func TestNewStore_EmptyDriver(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewStore(ctx, "", ":memory:")
	if err != nil {
		t.Fatalf("空驱动应默认使用 SQLite: %v", err)
	}
	defer store.Close()
}

func TestNewStore_InvalidDriver(t *testing.T) {
	ctx := context.Background()
	_, err := db.NewStore(ctx, "mysql", "")
	if err == nil {
		t.Fatal("期望返回错误，但得到 nil")
	}
}

func TestNewStore_Postgres_NotImplemented(t *testing.T) {
	ctx := context.Background()
	_, err := db.NewStore(ctx, "postgres", "")
	if err == nil {
		t.Fatal("PostgreSQL 尚未实现，期望返回错误")
	}
}
