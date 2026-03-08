package stats_test

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/stats"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// Aggregator 单元测试
// 基于 SQLite 内存数据库，验证全局统计与用户统计的正确性
// ============================================================

// newTestAggregator 创建测试用的 Aggregator + Store
func newTestAggregator(t *testing.T) (*stats.Aggregator, db.Store) {
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

	return stats.NewAggregator(store), store
}

// seedRequestLogs 向存储写入测试请求日志
func seedRequestLogs(t *testing.T, store db.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now()

	logs := []*db.RequestLog{
		{
			UserID:       1,
			Model:        "claude-sonnet-4",
			Provider:     "anthropic",
			CredentialID: "cred-1",
			InputTokens:  100,
			OutputTokens: 200,
			Latency:      150,
			StatusCode:   200,
			CreatedAt:    now.Add(-2 * time.Hour),
		},
		{
			UserID:       1,
			Model:        "gpt-5",
			Provider:     "openai",
			CredentialID: "cred-2",
			InputTokens:  50,
			OutputTokens: 100,
			Latency:      120,
			StatusCode:   200,
			CreatedAt:    now.Add(-1 * time.Hour),
		},
		{
			UserID:       2,
			Model:        "claude-sonnet-4",
			Provider:     "anthropic",
			CredentialID: "cred-1",
			InputTokens:  80,
			OutputTokens: 160,
			Latency:      180,
			StatusCode:   200,
			CreatedAt:    now.Add(-30 * time.Minute),
		},
	}

	for _, log := range logs {
		if err := store.RecordRequest(ctx, log); err != nil {
			t.Fatalf("写入请求日志失败: %v", err)
		}
	}
}

// ------------------------------------------------------------
// TestGetGlobalStats_Empty 无数据时返回零值
// ------------------------------------------------------------

func TestGetGlobalStats_Empty(t *testing.T) {
	agg, _ := newTestAggregator(t)
	ctx := context.Background()

	result, err := agg.GetGlobalStats(ctx, nil, nil)
	if err != nil {
		t.Fatalf("查询全局统计失败: %v", err)
	}
	if result.TotalRequests != 0 {
		t.Errorf("期望 0 条请求，实际 %d", result.TotalRequests)
	}
	if result.TotalTokens != 0 {
		t.Errorf("期望 0 token，实际 %d", result.TotalTokens)
	}
}

// ------------------------------------------------------------
// TestGetGlobalStats_WithData 有数据时返回正确聚合结果
// ------------------------------------------------------------

func TestGetGlobalStats_WithData(t *testing.T) {
	agg, store := newTestAggregator(t)
	ctx := context.Background()
	seedRequestLogs(t, store)

	result, err := agg.GetGlobalStats(ctx, nil, nil)
	if err != nil {
		t.Fatalf("查询全局统计失败: %v", err)
	}

	// 总请求数: 3
	if result.TotalRequests != 3 {
		t.Errorf("期望 3 条请求，实际 %d", result.TotalRequests)
	}

	// 总 token: (100+200) + (50+100) + (80+160) = 690
	if result.TotalTokens != 690 {
		t.Errorf("期望 690 token，实际 %d", result.TotalTokens)
	}

	// 按模型分组: claude-sonnet-4 = 2, gpt-5 = 1
	if result.ByModel["claude-sonnet-4"] != 2 {
		t.Errorf("期望 claude-sonnet-4 有 2 条，实际 %d", result.ByModel["claude-sonnet-4"])
	}
	if result.ByModel["gpt-5"] != 1 {
		t.Errorf("期望 gpt-5 有 1 条，实际 %d", result.ByModel["gpt-5"])
	}

	// 按提供商分组: anthropic = 2, openai = 1
	if result.ByProvider["anthropic"] != 2 {
		t.Errorf("期望 anthropic 有 2 条，实际 %d", result.ByProvider["anthropic"])
	}
	if result.ByProvider["openai"] != 1 {
		t.Errorf("期望 openai 有 1 条，实际 %d", result.ByProvider["openai"])
	}
}

// ------------------------------------------------------------
// TestGetGlobalStats_TimeFilter 时间区间过滤
// ------------------------------------------------------------

func TestGetGlobalStats_TimeFilter(t *testing.T) {
	agg, store := newTestAggregator(t)
	ctx := context.Background()
	seedRequestLogs(t, store)

	// 仅查询最近 45 分钟内的请求（应只包含第三条）
	after := time.Now().Add(-45 * time.Minute)
	result, err := agg.GetGlobalStats(ctx, &after, nil)
	if err != nil {
		t.Fatalf("查询全局统计失败: %v", err)
	}
	if result.TotalRequests != 1 {
		t.Errorf("期望 1 条请求，实际 %d", result.TotalRequests)
	}
}

// ------------------------------------------------------------
// TestGetUserStats_SingleUser 单用户统计
// ------------------------------------------------------------

func TestGetUserStats_SingleUser(t *testing.T) {
	agg, store := newTestAggregator(t)
	ctx := context.Background()
	seedRequestLogs(t, store)

	// 用户 1 有 2 条请求
	result, err := agg.GetUserStats(ctx, 1, nil, nil)
	if err != nil {
		t.Fatalf("查询用户统计失败: %v", err)
	}
	if result.UserID != 1 {
		t.Errorf("期望 UserID=1，实际 %d", result.UserID)
	}
	if result.TotalRequests != 2 {
		t.Errorf("期望 2 条请求，实际 %d", result.TotalRequests)
	}

	// 总 token: (100+200) + (50+100) = 450
	if result.TotalTokens != 450 {
		t.Errorf("期望 450 token，实际 %d", result.TotalTokens)
	}

	// 按模型分组: claude-sonnet-4 = 1, gpt-5 = 1
	if result.ByModel["claude-sonnet-4"] != 1 {
		t.Errorf("期望 claude-sonnet-4 有 1 条，实际 %d", result.ByModel["claude-sonnet-4"])
	}
	if result.ByModel["gpt-5"] != 1 {
		t.Errorf("期望 gpt-5 有 1 条，实际 %d", result.ByModel["gpt-5"])
	}
}

// ------------------------------------------------------------
// TestGetUserStats_NoData 无请求的用户返回零值
// ------------------------------------------------------------

func TestGetUserStats_NoData(t *testing.T) {
	agg, store := newTestAggregator(t)
	ctx := context.Background()
	seedRequestLogs(t, store)

	// 用户 999 没有任何请求
	result, err := agg.GetUserStats(ctx, 999, nil, nil)
	if err != nil {
		t.Fatalf("查询用户统计失败: %v", err)
	}
	if result.TotalRequests != 0 {
		t.Errorf("期望 0 条请求，实际 %d", result.TotalRequests)
	}
}

// ------------------------------------------------------------
// TestGetModelStats 按模型过滤的全局统计
// ------------------------------------------------------------

func TestGetModelStats(t *testing.T) {
	agg, store := newTestAggregator(t)
	ctx := context.Background()
	seedRequestLogs(t, store)

	result, err := agg.GetModelStats(ctx, "claude-sonnet-4", nil, nil)
	if err != nil {
		t.Fatalf("查询模型统计失败: %v", err)
	}
	if result.TotalRequests != 2 {
		t.Errorf("期望 2 条请求，实际 %d", result.TotalRequests)
	}

	// 总 token: (100+200) + (80+160) = 540
	if result.TotalTokens != 540 {
		t.Errorf("期望 540 token，实际 %d", result.TotalTokens)
	}
}
