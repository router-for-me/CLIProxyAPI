package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/router"
)

// ============================================================
//  测试: CircuitBreaker 状态转换
//  Closed -> Open -> HalfOpen -> Closed (成功路径)
//  Closed -> Open -> HalfOpen -> Open   (失败路径)
// ============================================================

func TestCircuitBreaker_InitialState(t *testing.T) {
	cb := router.NewCircuitBreaker(3, 5*time.Second)

	if cb.State() != router.CircuitClosed {
		t.Errorf("初始状态应为 Closed，实际为 %s", cb.State())
	}
	if !cb.Allow() {
		t.Error("Closed 状态应允许请求通过")
	}
}

func TestCircuitBreaker_ClosedToOpen(t *testing.T) {
	cb := router.NewCircuitBreaker(3, 5*time.Second)

	// 前两次失败不应触发熔断
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != router.CircuitClosed {
		t.Errorf("2 次失败后应仍为 Closed，实际为 %s", cb.State())
	}
	if !cb.Allow() {
		t.Error("未达阈值时应允许请求通过")
	}

	// 第三次失败达到阈值，触发熔断
	cb.RecordFailure()
	if cb.State() != router.CircuitOpen {
		t.Errorf("3 次失败后应为 Open，实际为 %s", cb.State())
	}
	if cb.Allow() {
		t.Error("Open 状态应拒绝请求")
	}
}

func TestCircuitBreaker_OpenToHalfOpen(t *testing.T) {
	// 使用极短的 resetTimeout 以便测试
	cb := router.NewCircuitBreaker(1, 10*time.Millisecond)

	// 触发熔断
	cb.RecordFailure()
	if cb.State() != router.CircuitOpen {
		t.Fatalf("应为 Open 状态，实际为 %s", cb.State())
	}

	// 等待 resetTimeout 过期
	time.Sleep(20 * time.Millisecond)

	// Allow 应返回 true 并转换到 HalfOpen
	if !cb.Allow() {
		t.Error("resetTimeout 过期后 Allow 应返回 true")
	}
	if cb.State() != router.CircuitHalfOpen {
		t.Errorf("超时后应为 HalfOpen，实际为 %s", cb.State())
	}
}

func TestCircuitBreaker_HalfOpenToClosed(t *testing.T) {
	cb := router.NewCircuitBreaker(1, 10*time.Millisecond)

	// 触发熔断 -> 等待 -> 进入半开
	cb.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	cb.Allow() // 转为 HalfOpen

	// 探测成功 -> 恢复 Closed
	cb.RecordSuccess()
	if cb.State() != router.CircuitClosed {
		t.Errorf("探测成功后应恢复 Closed，实际为 %s", cb.State())
	}
	if !cb.Allow() {
		t.Error("恢复 Closed 后应允许请求")
	}
}

func TestCircuitBreaker_HalfOpenToOpen(t *testing.T) {
	cb := router.NewCircuitBreaker(1, 10*time.Millisecond)

	// 触发熔断 -> 等待 -> 进入半开
	cb.RecordFailure()
	time.Sleep(20 * time.Millisecond)
	cb.Allow() // 转为 HalfOpen

	// 探测失败 -> 回到 Open
	cb.RecordFailure()
	if cb.State() != router.CircuitOpen {
		t.Errorf("探测失败后应回到 Open，实际为 %s", cb.State())
	}
}

func TestCircuitBreaker_SuccessResetsFailures(t *testing.T) {
	cb := router.NewCircuitBreaker(3, 5*time.Second)

	// 2 次失败后 1 次成功，计数重置
	cb.RecordFailure()
	cb.RecordFailure()
	cb.RecordSuccess()

	// 再连续失败 2 次不应触发熔断（因为计数已重置）
	cb.RecordFailure()
	cb.RecordFailure()
	if cb.State() != router.CircuitClosed {
		t.Errorf("成功重置后，2 次失败不应熔断，实际为 %s", cb.State())
	}

	// 第 3 次失败应触发
	cb.RecordFailure()
	if cb.State() != router.CircuitOpen {
		t.Errorf("重置后第 3 次失败应触发熔断，实际为 %s", cb.State())
	}
}

func TestCircuitBreaker_AllowBlocksDuringOpen(t *testing.T) {
	cb := router.NewCircuitBreaker(1, 1*time.Hour)

	// 触发熔断（resetTimeout 设为 1 小时，不会自动恢复）
	cb.RecordFailure()

	// 多次调用 Allow 均应返回 false
	for i := 0; i < 10; i++ {
		if cb.Allow() {
			t.Errorf("Open 状态下第 %d 次 Allow 不应放行", i)
		}
	}
}

// ============================================================
//  测试: HealthChecker 生命周期
// ============================================================

func TestHealthChecker_StartStop(t *testing.T) {
	hc := router.NewHealthChecker(nil, 100*time.Millisecond)

	// 验证 Start/Stop 不会 panic 或阻塞
	ctx, cancel := context.WithCancel(context.Background())
	hc.Start(ctx)

	// 等一小段时间让 goroutine 启动
	time.Sleep(50 * time.Millisecond)

	// 停止
	hc.Stop()
	cancel()

	// 重复 Stop 不应 panic
	hc.Stop()
}
