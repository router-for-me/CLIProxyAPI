package stats

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 聚合统计服务 — 全局 / 用户 / 模型维度的数据聚合
// 封装 StatsStore 查询逻辑，为 Handler 和 Exporter 提供统一入口
// ============================================================

// Aggregator 聚合统计服务
type Aggregator struct {
	store db.StatsStore
}

// NewAggregator 创建聚合统计服务
func NewAggregator(store db.StatsStore) *Aggregator {
	return &Aggregator{store: store}
}

// ------------------------------------------------------------
// GetGlobalStats 全局统计
// 返回指定时间区间内的全局请求统计（总请求数、总 token、
// 按模型/提供商分组、平均延迟）
// after / before 均为可选，nil 表示不限制该边界
// ------------------------------------------------------------

func (a *Aggregator) GetGlobalStats(ctx context.Context, after, before *time.Time) (*db.RequestStats, error) {
	return a.store.GetRequestStats(ctx, db.RequestStatsOpts{
		After:  after,
		Before: before,
	})
}

// ------------------------------------------------------------
// GetUserStats 用户维度统计
// 返回指定用户在时间区间内的请求统计（总请求数、总 token、
// 按模型分组）
// ------------------------------------------------------------

func (a *Aggregator) GetUserStats(ctx context.Context, userID int64, after, before *time.Time) (*db.UserRequestStats, error) {
	return a.store.GetUserRequestStats(ctx, userID, db.RequestStatsOpts{
		After:  after,
		Before: before,
	})
}

// ------------------------------------------------------------
// GetModelStats 按模型过滤的全局统计
// 在全局统计基础上追加模型过滤条件
// ------------------------------------------------------------

func (a *Aggregator) GetModelStats(ctx context.Context, model string, after, before *time.Time) (*db.RequestStats, error) {
	return a.store.GetRequestStats(ctx, db.RequestStatsOpts{
		After:  after,
		Before: before,
		Model:  &model,
	})
}
