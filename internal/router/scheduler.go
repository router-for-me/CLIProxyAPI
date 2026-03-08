package router

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
//  Scheduler — 调度器核心
//  职责: 整合凭证池 + 运行时指标 + 调度策略，选出最优凭证
// ============================================================

// CredentialMetrics 凭证运行时指标
// 所有字段均为原子操作，无需外部加锁即可安全读写
type CredentialMetrics struct {
	ActiveConns  atomic.Int64 // 当前活跃连接数
	TotalReqs    atomic.Int64 // 累计请求数
	TotalErrors  atomic.Int64 // 累计错误数
	TotalLatency atomic.Int64 // 累计延迟(ms)
	CircuitOpen  atomic.Bool  // 熔断器状态标记
}

// Scheduler 调度器核心
type Scheduler struct {
	strategy  Strategy             // 当前调度策略
	poolMgr   *quota.PoolManager   // 凭证池管理器
	credStore db.CredentialStore   // 凭证存储（用于直接查询）
	mu        sync.RWMutex         // 保护 metrics map 和 strategy 的并发切换
	metrics   map[string]*CredentialMetrics // credentialID -> 运行时指标
}

// NewScheduler 创建调度器
// strategy : 初始调度策略
// poolMgr  : 凭证池管理器（负责按用户池模式获取候选凭证）
// credStore: 凭证存储接口
func NewScheduler(strategy Strategy, poolMgr *quota.PoolManager, credStore db.CredentialStore) *Scheduler {
	return &Scheduler{
		strategy:  strategy,
		poolMgr:   poolMgr,
		credStore: credStore,
		metrics:   make(map[string]*CredentialMetrics),
	}
}

// ============================================================
//  Select — 选择最优凭证
// ============================================================

// Select 根据用户 ID 和 provider 选择最优凭证
// 流程:
//  1. 通过 PoolManager 获取用户可用凭证列表
//  2. 为每个凭证附加运行时指标，构建 Candidate 列表
//  3. 委托给当前 Strategy 执行选择
//  4. 选中后递增活跃连接计数
func (s *Scheduler) Select(ctx context.Context, userID int64, provider string) (*db.Credential, error) {
	// ---- 获取候选凭证 ----
	creds, err := s.poolMgr.GetAvailableCredentials(ctx, userID, provider)
	if err != nil {
		return nil, fmt.Errorf("获取可用凭证失败: %w", err)
	}
	if len(creds) == 0 {
		return nil, fmt.Errorf("没有可用的 %s 凭证", provider)
	}

	// ---- 构建候选列表 ----
	candidates := make([]*Candidate, 0, len(creds))
	s.mu.RLock()
	for _, cred := range creds {
		if !cred.Enabled {
			continue
		}
		c := &Candidate{
			Credential: cred,
			Weight:     cred.Weight,
		}

		// 附加运行时指标（如果存在）
		if m, ok := s.metrics[cred.ID]; ok {
			c.ActiveConns = m.ActiveConns.Load()
			c.CircuitOpen = m.CircuitOpen.Load()

			totalReqs := m.TotalReqs.Load()
			if totalReqs > 0 {
				totalErrors := m.TotalErrors.Load()
				c.SuccessRate = 1.0 - float64(totalErrors)/float64(totalReqs)
				c.AvgLatency = float64(m.TotalLatency.Load()) / float64(totalReqs)
			} else {
				c.SuccessRate = 1.0
			}
		} else {
			// 无历史指标，假设健康
			c.SuccessRate = 1.0
		}

		candidates = append(candidates, c)
	}
	strategy := s.strategy
	s.mu.RUnlock()

	if len(candidates) == 0 {
		return nil, fmt.Errorf("没有已启用的 %s 凭证", provider)
	}

	// ---- 委托策略选择 ----
	selected := strategy.Select(candidates)
	if selected == nil {
		return nil, fmt.Errorf("所有 %s 凭证均不可用（可能已熔断）", provider)
	}

	// ---- 递增活跃连接数 ----
	s.getOrCreateMetrics(selected.Credential.ID).ActiveConns.Add(1)

	return selected.Credential, nil
}

// ============================================================
//  ReportResult — 报告请求结果
// ============================================================

// errRateWindow 用于判断熔断的滑动窗口大小
const errRateWindow = 10

// errRateThreshold 错误率阈值（超过则触发熔断）
const errRateThreshold = 0.5

// ReportResult 上报单次请求的执行结果
// credentialID : 使用的凭证 ID
// success      : 是否成功
// latencyMs    : 请求延迟（毫秒）
func (s *Scheduler) ReportResult(credentialID string, success bool, latencyMs int64) {
	m := s.getOrCreateMetrics(credentialID)

	// 递减活跃连接
	m.ActiveConns.Add(-1)

	// 累计请求数与延迟
	m.TotalReqs.Add(1)
	m.TotalLatency.Add(latencyMs)

	// 记录错误
	if !success {
		m.TotalErrors.Add(1)
	}

	// ---- 简易熔断判定 ----
	// 当累计请求 >= errRateWindow 时，检查错误率
	totalReqs := m.TotalReqs.Load()
	if totalReqs >= errRateWindow {
		totalErrors := m.TotalErrors.Load()
		errorRate := float64(totalErrors) / float64(totalReqs)
		if errorRate > errRateThreshold {
			m.CircuitOpen.Store(true)
		}
	}
}

// ============================================================
//  SetStrategy — 运行时切换调度策略
// ============================================================

// SetStrategy 热切换调度策略
// 切换后立即生效，对正在执行的 Select 无影响（读写锁保护）
func (s *Scheduler) SetStrategy(strategy Strategy) {
	s.mu.Lock()
	s.strategy = strategy
	s.mu.Unlock()
}

// ============================================================
//  GetMetrics — 获取指标快照（供 MetricsCollector 使用）
// ============================================================

// GetMetrics 返回所有凭证的运行时指标快照
func (s *Scheduler) GetMetrics() map[string]*CredentialMetrics {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// 返回 map 的浅拷贝，避免外部修改
	snapshot := make(map[string]*CredentialMetrics, len(s.metrics))
	for k, v := range s.metrics {
		snapshot[k] = v
	}
	return snapshot
}

// ============================================================
//  内部辅助方法
// ============================================================

// getOrCreateMetrics 获取或创建凭证的运行时指标
func (s *Scheduler) getOrCreateMetrics(credentialID string) *CredentialMetrics {
	s.mu.RLock()
	m, ok := s.metrics[credentialID]
	s.mu.RUnlock()
	if ok {
		return m
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// 二次检查，避免重复创建
	if m, ok = s.metrics[credentialID]; ok {
		return m
	}

	m = &CredentialMetrics{}
	s.metrics[credentialID] = m
	return m
}
