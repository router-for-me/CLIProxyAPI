package router

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
//  SchedulerAdapter — 适配器层
//  桥接新调度器与现有代码库的凭证选择逻辑
//  当前暴露简洁的对外 API，Phase 8 集成时对接 existing selector
// ============================================================

// NOTE: 适配器将新 Scheduler 封装为外部可消费的接口
// 现有的 selector 接口将在 Phase 8 集成阶段对接
// 目前暴露干净的 API，后续可无缝适配

// SchedulerAdapter 调度器适配器
type SchedulerAdapter struct {
	scheduler *Scheduler
}

// NewSchedulerAdapter 创建调度器适配器
func NewSchedulerAdapter(scheduler *Scheduler) *SchedulerAdapter {
	return &SchedulerAdapter{scheduler: scheduler}
}

// SelectCredential 选择凭证（适配层入口）
// 对外统一入口，屏蔽内部调度器的复杂性
func (a *SchedulerAdapter) SelectCredential(ctx context.Context, userID int64, provider string) (*db.Credential, error) {
	return a.scheduler.Select(ctx, userID, provider)
}

// ReportResult 上报请求结果（适配层透传）
func (a *SchedulerAdapter) ReportResult(credentialID string, success bool, latencyMs int64) {
	a.scheduler.ReportResult(credentialID, success, latencyMs)
}

// GetStrategyName 获取当前调度策略名称
func (a *SchedulerAdapter) GetStrategyName() string {
	a.scheduler.mu.RLock()
	defer a.scheduler.mu.RUnlock()
	return a.scheduler.strategy.Name()
}

// SetStrategy 切换调度策略
func (a *SchedulerAdapter) SetStrategy(strategy Strategy) {
	a.scheduler.SetStrategy(strategy)
}
