package quota_test

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func newTestRiskEngine(t *testing.T) (*quota.RiskEngine, db.Store) {
	t.Helper()
	ctx := context.Background()
	store, _ := db.NewSQLiteStore(ctx, ":memory:")
	store.Migrate(ctx)
	t.Cleanup(func() { store.Close() })

	cfg := quota.RiskConfig{
		Enabled:              true,
		RPMExceedThreshold:   3,
		RPMExceedWindow:      5 * time.Minute,
		PenaltyDuration:      10 * time.Minute,
		PenaltyProbability:   0.0, // 惩罚期完全阻止
		ProbEnabled:          true,
		ContributorWeight:    1.0, // 贡献者 100% 通过
		NonContributorWeight: 1.0, // 测试时也 100%
	}
	return quota.NewRiskEngine(store, cfg), store
}

func TestRiskEngine_ProbabilityCheck_Normal(t *testing.T) {
	engine, _ := newTestRiskEngine(t)
	ctx := context.Background()

	// 无惩罚标记，贡献者 100% 通过
	if !engine.ProbabilityCheck(ctx, 1, true) {
		t.Fatal("贡献者权重 1.0 应该通过")
	}
}

func TestRiskEngine_RPMExceed_TriggersRiskMark(t *testing.T) {
	engine, store := newTestRiskEngine(t)
	ctx := context.Background()

	// 创建测试用户
	store.CreateUser(ctx, &db.User{
		UUID: "u1", Username: "test", APIKey: "key1",
		Role: db.RoleUser, Status: db.StatusActive, PoolMode: db.PoolPublic,
		InviteCode: "inv1",
	})

	// 触发 3 次超限
	engine.RecordRPMExceed(ctx, 1)
	engine.RecordRPMExceed(ctx, 1)
	engine.RecordRPMExceed(ctx, 1)

	// 检查是否被标记
	marks, err := store.GetActiveRiskMarks(ctx, 1)
	if err != nil {
		t.Fatalf("查询标记失败: %v", err)
	}
	if len(marks) == 0 {
		t.Fatal("超过阈值后应该被标记")
	}
}
