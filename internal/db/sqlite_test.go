package db_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 测试基础设施
// ============================================================

// newTestStore 创建基于内存的 SQLite 测试存储
// 自动执行迁移并注册清理回调
func newTestStore(t *testing.T) db.Store {
	t.Helper()
	ctx := context.Background()

	store, err := db.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("创建测试存储失败: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	return store
}

// ============================================================
// 基础生命周期测试
// ============================================================

func TestSQLiteStore_MigrateAndClose(t *testing.T) {
	store := newTestStore(t)
	_ = store
}

// ============================================================
// SettingsStore 测试
// ============================================================

func TestSettings_SetAndGet(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 写入配置项 ----
	if err := store.SetSetting(ctx, "site_name", "公益站"); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}

	// ---- 读回并验证 ----
	val, err := store.GetSetting(ctx, "site_name")
	if err != nil {
		t.Fatalf("读取设置失败: %v", err)
	}
	if val != "公益站" {
		t.Errorf("期望值 '公益站', 实际得到 %q", val)
	}
}

func TestSettings_GetNotFound(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 查询不存在的键 ----
	_, err := store.GetSetting(ctx, "nonexistent_key")
	if err == nil {
		t.Fatal("期望返回错误, 但得到 nil")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("期望 sql.ErrNoRows, 实际得到: %v", err)
	}
}

func TestSettings_Upsert(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 首次写入 ----
	if err := store.SetSetting(ctx, "version", "1.0"); err != nil {
		t.Fatalf("首次写入失败: %v", err)
	}

	// ---- 覆盖更新 ----
	if err := store.SetSetting(ctx, "version", "2.0"); err != nil {
		t.Fatalf("覆盖更新失败: %v", err)
	}

	val, err := store.GetSetting(ctx, "version")
	if err != nil {
		t.Fatalf("读取更新后的值失败: %v", err)
	}
	if val != "2.0" {
		t.Errorf("期望 '2.0', 实际得到 %q", val)
	}
}

func TestSettings_Delete(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 写入后删除 ----
	if err := store.SetSetting(ctx, "temp_key", "temp_val"); err != nil {
		t.Fatalf("写入设置失败: %v", err)
	}
	if err := store.DeleteSetting(ctx, "temp_key"); err != nil {
		t.Fatalf("删除设置失败: %v", err)
	}

	// ---- 验证已被删除 ----
	_, err := store.GetSetting(ctx, "temp_key")
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("期望 sql.ErrNoRows, 实际得到: %v", err)
	}
}

func TestSettings_DeleteNonExistent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 删除不存在的键应幂等成功 ----
	if err := store.DeleteSetting(ctx, "ghost_key"); err != nil {
		t.Errorf("删除不存在的键应成功, 但得到: %v", err)
	}
}

func TestSettings_GetAll(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 写入多个配置 ----
	pairs := map[string]string{
		"key_a": "val_a",
		"key_b": "val_b",
		"key_c": "val_c",
	}
	for k, v := range pairs {
		if err := store.SetSetting(ctx, k, v); err != nil {
			t.Fatalf("写入 [%s] 失败: %v", k, err)
		}
	}

	// ---- 获取全部并验证 ----
	all, err := store.GetAllSettings(ctx)
	if err != nil {
		t.Fatalf("获取所有设置失败: %v", err)
	}
	if len(all) != len(pairs) {
		t.Errorf("期望 %d 条, 实际 %d 条", len(pairs), len(all))
	}
	for k, want := range pairs {
		if got, ok := all[k]; !ok {
			t.Errorf("缺少键 %q", k)
		} else if got != want {
			t.Errorf("键 %q: 期望 %q, 实际 %q", k, want, got)
		}
	}
}

func TestSettings_GetAllEmpty(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 空表应返回空 map ----
	all, err := store.GetAllSettings(ctx)
	if err != nil {
		t.Fatalf("获取空设置失败: %v", err)
	}
	if all == nil {
		t.Fatal("期望空 map, 实际得到 nil")
	}
	if len(all) != 0 {
		t.Errorf("期望长度 0, 实际 %d", len(all))
	}
}

// ============================================================
// StatsStore 测试
// ============================================================

// newRequestLog 创建测试用的请求日志对象
func newRequestLog(userID int64, model, provider string) *db.RequestLog {
	return &db.RequestLog{
		UserID:       userID,
		Model:        model,
		Provider:     provider,
		CredentialID: "cred-test-001",
		InputTokens:  100,
		OutputTokens: 200,
		Latency:      50,
		StatusCode:   200,
		CreatedAt:    time.Now(),
	}
}

func TestStats_RecordRequest(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	log := newRequestLog(1, "gpt-4", "openai")

	// ---- 写入请求日志 ----
	if err := store.RecordRequest(ctx, log); err != nil {
		t.Fatalf("写入请求日志失败: %v", err)
	}
	if log.ID == 0 {
		t.Error("写入后 ID 应大于 0")
	}
}

func TestStats_RecordRequestAutoTime(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 不设 CreatedAt，验证自动填充 ----
	log := &db.RequestLog{
		UserID:     1,
		Model:      "claude-3",
		Provider:   "anthropic",
		StatusCode: 200,
	}
	before := time.Now()
	if err := store.RecordRequest(ctx, log); err != nil {
		t.Fatalf("写入请求日志失败: %v", err)
	}
	if log.CreatedAt.Before(before) {
		t.Error("自动填充的时间不应早于写入前的时间")
	}
}

func TestStats_GetRequestStats_NoData(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 空表应返回零值统计 ----
	stats, err := store.GetRequestStats(ctx, db.RequestStatsOpts{})
	if err != nil {
		t.Fatalf("查询空统计失败: %v", err)
	}
	if stats.TotalRequests != 0 {
		t.Errorf("期望 TotalRequests=0, 实际 %d", stats.TotalRequests)
	}
	if stats.TotalTokens != 0 {
		t.Errorf("期望 TotalTokens=0, 实际 %d", stats.TotalTokens)
	}
	if stats.AvgLatency != 0 {
		t.Errorf("期望 AvgLatency=0, 实际 %f", stats.AvgLatency)
	}
}

func TestStats_GetRequestStats_WithData(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 插入多条日志 ----
	logs := []*db.RequestLog{
		{UserID: 1, Model: "gpt-4", Provider: "openai", InputTokens: 100, OutputTokens: 200, Latency: 40, StatusCode: 200, CreatedAt: time.Now()},
		{UserID: 2, Model: "gpt-4", Provider: "openai", InputTokens: 150, OutputTokens: 250, Latency: 60, StatusCode: 200, CreatedAt: time.Now()},
		{UserID: 1, Model: "claude-3", Provider: "anthropic", InputTokens: 80, OutputTokens: 120, Latency: 30, StatusCode: 200, CreatedAt: time.Now()},
	}
	for i, l := range logs {
		if err := store.RecordRequest(ctx, l); err != nil {
			t.Fatalf("写入第 %d 条日志失败: %v", i+1, err)
		}
	}

	// ---- 查询全局统计 ----
	stats, err := store.GetRequestStats(ctx, db.RequestStatsOpts{})
	if err != nil {
		t.Fatalf("查询统计失败: %v", err)
	}

	// 总请求数 = 3
	if stats.TotalRequests != 3 {
		t.Errorf("期望 TotalRequests=3, 实际 %d", stats.TotalRequests)
	}

	// 总 token = (100+200) + (150+250) + (80+120) = 900
	if stats.TotalTokens != 900 {
		t.Errorf("期望 TotalTokens=900, 实际 %d", stats.TotalTokens)
	}

	// 按模型分组: gpt-4=2, claude-3=1
	if stats.ByModel["gpt-4"] != 2 {
		t.Errorf("期望 ByModel[gpt-4]=2, 实际 %d", stats.ByModel["gpt-4"])
	}
	if stats.ByModel["claude-3"] != 1 {
		t.Errorf("期望 ByModel[claude-3]=1, 实际 %d", stats.ByModel["claude-3"])
	}

	// 按提供商分组: openai=2, anthropic=1
	if stats.ByProvider["openai"] != 2 {
		t.Errorf("期望 ByProvider[openai]=2, 实际 %d", stats.ByProvider["openai"])
	}
	if stats.ByProvider["anthropic"] != 1 {
		t.Errorf("期望 ByProvider[anthropic]=1, 实际 %d", stats.ByProvider["anthropic"])
	}

	// 平均延迟 = (40+60+30) / 3 ≈ 43.33
	if stats.AvgLatency < 43 || stats.AvgLatency > 44 {
		t.Errorf("期望 AvgLatency ≈ 43.33, 实际 %f", stats.AvgLatency)
	}
}

func TestStats_GetRequestStats_WithModelFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 插入混合模型日志 ----
	for _, m := range []string{"gpt-4", "gpt-4", "claude-3"} {
		log := newRequestLog(1, m, "test-provider")
		if err := store.RecordRequest(ctx, log); err != nil {
			t.Fatalf("写入日志失败: %v", err)
		}
	}

	// ---- 仅查 gpt-4 ----
	model := "gpt-4"
	stats, err := store.GetRequestStats(ctx, db.RequestStatsOpts{Model: &model})
	if err != nil {
		t.Fatalf("查询过滤统计失败: %v", err)
	}
	if stats.TotalRequests != 2 {
		t.Errorf("期望 TotalRequests=2, 实际 %d", stats.TotalRequests)
	}
}

func TestStats_GetRequestStats_WithTimeFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 插入不同时间的日志 ----
	now := time.Now()
	past := now.Add(-2 * time.Hour)
	future := now.Add(2 * time.Hour)

	pastLog := &db.RequestLog{UserID: 1, Model: "gpt-4", Provider: "openai", InputTokens: 50, OutputTokens: 50, Latency: 20, StatusCode: 200, CreatedAt: past}
	nowLog := &db.RequestLog{UserID: 1, Model: "gpt-4", Provider: "openai", InputTokens: 100, OutputTokens: 100, Latency: 30, StatusCode: 200, CreatedAt: now}

	for _, l := range []*db.RequestLog{pastLog, nowLog} {
		if err := store.RecordRequest(ctx, l); err != nil {
			t.Fatalf("写入日志失败: %v", err)
		}
	}

	// ---- After 过滤: 只包含 now 之后 1 小时前插入的 ----
	oneHourAgo := now.Add(-1 * time.Hour)
	stats, err := store.GetRequestStats(ctx, db.RequestStatsOpts{After: &oneHourAgo})
	if err != nil {
		t.Fatalf("查询时间过滤统计失败: %v", err)
	}
	if stats.TotalRequests != 1 {
		t.Errorf("After 过滤: 期望 TotalRequests=1, 实际 %d", stats.TotalRequests)
	}

	// ---- Before 过滤: 只包含 now 之前的 ----
	stats2, err := store.GetRequestStats(ctx, db.RequestStatsOpts{Before: &now})
	if err != nil {
		t.Fatalf("查询 Before 过滤统计失败: %v", err)
	}
	if stats2.TotalRequests != 1 {
		t.Errorf("Before 过滤: 期望 TotalRequests=1, 实际 %d", stats2.TotalRequests)
	}

	// ---- After + Before 组合: 获取全部 ----
	stats3, err := store.GetRequestStats(ctx, db.RequestStatsOpts{After: &past, Before: &future})
	if err != nil {
		t.Fatalf("查询组合过滤统计失败: %v", err)
	}
	if stats3.TotalRequests != 2 {
		t.Errorf("组合过滤: 期望 TotalRequests=2, 实际 %d", stats3.TotalRequests)
	}
}

func TestStats_GetUserRequestStats(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 插入两个用户的日志 ----
	for _, uid := range []int64{1, 1, 1, 2, 2} {
		model := "gpt-4"
		if uid == 2 {
			model = "claude-3"
		}
		log := newRequestLog(uid, model, "test")
		if err := store.RecordRequest(ctx, log); err != nil {
			t.Fatalf("写入日志失败: %v", err)
		}
	}

	// ---- 查用户 1 的统计 ----
	stats, err := store.GetUserRequestStats(ctx, 1, db.RequestStatsOpts{})
	if err != nil {
		t.Fatalf("查询用户统计失败: %v", err)
	}
	if stats.UserID != 1 {
		t.Errorf("期望 UserID=1, 实际 %d", stats.UserID)
	}
	if stats.TotalRequests != 3 {
		t.Errorf("期望 TotalRequests=3, 实际 %d", stats.TotalRequests)
	}

	// 每条: 100 + 200 = 300 token, 3 条 = 900
	if stats.TotalTokens != 900 {
		t.Errorf("期望 TotalTokens=900, 实际 %d", stats.TotalTokens)
	}
	if stats.ByModel["gpt-4"] != 3 {
		t.Errorf("期望 ByModel[gpt-4]=3, 实际 %d", stats.ByModel["gpt-4"])
	}

	// ---- 查用户 2 的统计 ----
	stats2, err := store.GetUserRequestStats(ctx, 2, db.RequestStatsOpts{})
	if err != nil {
		t.Fatalf("查询用户 2 统计失败: %v", err)
	}
	if stats2.TotalRequests != 2 {
		t.Errorf("期望 TotalRequests=2, 实际 %d", stats2.TotalRequests)
	}
	if stats2.ByModel["claude-3"] != 2 {
		t.Errorf("期望 ByModel[claude-3]=2, 实际 %d", stats2.ByModel["claude-3"])
	}
}

func TestStats_GetUserRequestStats_NoData(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 不存在的用户应返回零值 ----
	stats, err := store.GetUserRequestStats(ctx, 9999, db.RequestStatsOpts{})
	if err != nil {
		t.Fatalf("查询不存在用户统计失败: %v", err)
	}
	if stats.TotalRequests != 0 {
		t.Errorf("期望 TotalRequests=0, 实际 %d", stats.TotalRequests)
	}
	if stats.TotalTokens != 0 {
		t.Errorf("期望 TotalTokens=0, 实际 %d", stats.TotalTokens)
	}
}

func TestStats_GetUserRequestStats_WithModelFilter(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// ---- 为同一用户插入不同模型的日志 ----
	models := []string{"gpt-4", "gpt-4", "claude-3"}
	for _, m := range models {
		log := newRequestLog(1, m, "test")
		if err := store.RecordRequest(ctx, log); err != nil {
			t.Fatalf("写入日志失败: %v", err)
		}
	}

	// ---- 仅查 claude-3 ----
	model := "claude-3"
	stats, err := store.GetUserRequestStats(ctx, 1, db.RequestStatsOpts{Model: &model})
	if err != nil {
		t.Fatalf("查询用户模型过滤统计失败: %v", err)
	}
	if stats.TotalRequests != 1 {
		t.Errorf("期望 TotalRequests=1, 实际 %d", stats.TotalRequests)
	}
}
