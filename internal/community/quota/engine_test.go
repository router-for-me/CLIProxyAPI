package quota_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func newTestEngine(t *testing.T) (*quota.Engine, db.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := db.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return quota.NewEngine(store), store
}

func TestEngine_Check_NoConfig(t *testing.T) {
	engine, _ := newTestEngine(t)
	ctx := context.Background()

	result, err := engine.Check(ctx, 1, "claude-sonnet-4")
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if !result.Allowed {
		t.Fatal("无额度配置时应该允许")
	}
}

func TestEngine_Check_WithConfig_Allowed(t *testing.T) {
	engine, store := newTestEngine(t)
	ctx := context.Background()

	// 创建额度配置
	store.CreateQuotaConfig(ctx, &db.QuotaConfig{
		ModelPattern:  "claude-*",
		QuotaType:     db.QuotaCount,
		MaxRequests:   100,
		RequestPeriod: db.PeriodDaily,
	})

	result, err := engine.Check(ctx, 1, "claude-sonnet-4")
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if !result.Allowed {
		t.Fatal("有额度时应该允许")
	}
}

func TestEngine_Deduct_And_Exhaust(t *testing.T) {
	engine, store := newTestEngine(t)
	ctx := context.Background()

	store.CreateQuotaConfig(ctx, &db.QuotaConfig{
		ModelPattern:  "gpt-5",
		QuotaType:     db.QuotaCount,
		MaxRequests:   2,
		RequestPeriod: db.PeriodDaily,
	})

	// 扣减两次
	engine.Deduct(ctx, 1, "gpt-5", 1, 0)
	engine.Deduct(ctx, 1, "gpt-5", 1, 0)

	// 第三次应该被拒绝
	result, _ := engine.Check(ctx, 1, "gpt-5")
	if result.Allowed {
		t.Fatal("额度耗尽后应该拒绝")
	}
}
