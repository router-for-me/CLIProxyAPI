package router

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
//  熔断器 — 三态状态机
//  Closed  -> 正常放行
//  Open    -> 拒绝请求，等待 resetTimeout 后进入 HalfOpen
//  HalfOpen -> 放行单次探测，成功则恢复 Closed，失败回到 Open
// ============================================================

// CircuitState 熔断器状态枚举
type CircuitState int32

const (
	CircuitClosed   CircuitState = iota // 正常
	CircuitOpen                         // 熔断
	CircuitHalfOpen                     // 半开（探测中）
)

// String 返回状态的可读名称
func (cs CircuitState) String() string {
	switch cs {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half_open"
	default:
		return "unknown"
	}
}

// ============================================================
//  CircuitBreaker — 熔断器实现
//  基于连续失败计数 + 超时自动恢复
// ============================================================

// CircuitBreaker 熔断器
type CircuitBreaker struct {
	state        atomic.Int32  // 当前状态（CircuitState）
	failures     atomic.Int64  // 连续失败计数
	threshold    int64         // 连续失败阈值，达到后触发熔断
	resetTimeout time.Duration // 熔断后等待恢复的超时时间
	lastFailure  atomic.Int64  // 最近一次失败的 unix 时间戳(秒)
}

// NewCircuitBreaker 创建熔断器
// threshold    : 连续失败多少次后熔断
// resetTimeout : 熔断后等待多久自动进入半开状态
func NewCircuitBreaker(threshold int64, resetTimeout time.Duration) *CircuitBreaker {
	cb := &CircuitBreaker{
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
	cb.state.Store(int32(CircuitClosed))
	return cb
}

// State 获取当前熔断器状态
func (cb *CircuitBreaker) State() CircuitState {
	return CircuitState(cb.state.Load())
}

// ============================================================
//  Allow — 请求准入判断
// ============================================================

// Allow 检查是否允许请求通过
// 返回 true 表示放行，false 表示拒绝（熔断中）
func (cb *CircuitBreaker) Allow() bool {
	switch CircuitState(cb.state.Load()) {
	case CircuitClosed:
		// 正常状态，始终放行
		return true

	case CircuitOpen:
		// 检查是否已过 resetTimeout，若是则转为半开
		lastFail := cb.lastFailure.Load()
		elapsed := time.Since(time.Unix(lastFail, 0))
		if elapsed >= cb.resetTimeout {
			// CAS 尝试转为半开状态（仅一个 goroutine 能成功）
			if cb.state.CompareAndSwap(int32(CircuitOpen), int32(CircuitHalfOpen)) {
				return true
			}
			// CAS 失败说明其他 goroutine 已转换，当前仍拒绝
			return false
		}
		return false

	case CircuitHalfOpen:
		// 半开状态允许单次探测通过
		// 多个并发请求只有一个能进入，其余拒绝
		return true

	default:
		return false
	}
}

// ============================================================
//  RecordSuccess / RecordFailure — 结果记录
// ============================================================

// RecordSuccess 记录一次成功请求
// - 重置连续失败计数
// - 从 HalfOpen 恢复到 Closed
func (cb *CircuitBreaker) RecordSuccess() {
	cb.failures.Store(0)
	// 无论当前处于什么状态，成功都应恢复为 Closed
	cb.state.Store(int32(CircuitClosed))
}

// RecordFailure 记录一次失败请求
// - 递增连续失败计数
// - 若达到阈值则切换到 Open 状态
// - 更新最近失败时间戳
func (cb *CircuitBreaker) RecordFailure() {
	failures := cb.failures.Add(1)
	cb.lastFailure.Store(time.Now().Unix())

	if failures >= cb.threshold {
		cb.state.Store(int32(CircuitOpen))
	}
}

// ============================================================
//  HealthChecker — 后台健康检查器（预留结构）
//  Phase 8 集成时将实现主动探测逻辑
// ============================================================

// HealthChecker 后台健康检查器
// 定期对凭证执行健康探测，并更新熔断器状态
type HealthChecker struct {
	store    db.CredentialStore // 凭证存储（用于查询与状态更新）
	interval time.Duration     // 检查间隔
	stopCh   chan struct{}     // 停止信号通道
}

// NewHealthChecker 创建后台健康检查器
// store    : 凭证存储接口
// interval : 健康检查周期
func NewHealthChecker(store db.CredentialStore, interval time.Duration) *HealthChecker {
	return &HealthChecker{
		store:    store,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start 启动后台健康检查
// 当前为占位实现，仅维护 goroutine 生命周期
// TODO: Phase 8 补充主动探测逻辑（HTTP ping / token 验证等）
func (h *HealthChecker) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(h.interval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-h.stopCh:
				return
			case <-ticker.C:
				// 占位: 后续实现凭证健康探测
				// 1. 从 store 拉取所有已启用凭证
				// 2. 对每个凭证执行轻量探测
				// 3. 根据探测结果更新 CredentialHealthRecord
				// 4. 触发熔断器状态变更
			}
		}
	}()
}

// Stop 停止后台健康检查
func (h *HealthChecker) Stop() {
	select {
	case <-h.stopCh:
		// 已经关闭，避免重复 close
	default:
		close(h.stopCh)
	}
}
