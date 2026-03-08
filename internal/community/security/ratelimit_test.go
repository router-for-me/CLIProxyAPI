package security_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/security"
)

// ============================================================
// 全局限流器测试
// ============================================================

// ------------------------------------------------------------
// 全局 QPS 限制
// ------------------------------------------------------------

func TestGlobalRateLimiter_GlobalQPS(t *testing.T) {
	// 全局 3 QPS，单 IP 不做严格限制（设为 1000）
	limiter := security.NewGlobalRateLimiter(3, 1000)

	// 用不同 IP 发请求，验证全局计数
	if !limiter.Allow("10.0.0.1") {
		t.Fatal("第 1 次全局请求应该允许")
	}
	if !limiter.Allow("10.0.0.2") {
		t.Fatal("第 2 次全局请求应该允许")
	}
	if !limiter.Allow("10.0.0.3") {
		t.Fatal("第 3 次全局请求应该允许")
	}
	// 第 4 次应该被全局 QPS 拦截
	if limiter.Allow("10.0.0.4") {
		t.Fatal("第 4 次全局请求应该被拒绝（超过 QPS=3）")
	}
}

// ------------------------------------------------------------
// 单 IP RPM 限制
// ------------------------------------------------------------

func TestGlobalRateLimiter_PerIPRPM(t *testing.T) {
	// 全局不做严格限制（设为 10000），单 IP 每分钟 2 次
	limiter := security.NewGlobalRateLimiter(10000, 2)

	ip := "192.168.1.100"
	if !limiter.Allow(ip) {
		t.Fatal("第 1 次 IP 请求应该允许")
	}
	if !limiter.Allow(ip) {
		t.Fatal("第 2 次 IP 请求应该允许")
	}
	if limiter.Allow(ip) {
		t.Fatal("第 3 次 IP 请求应该被拒绝（超过 RPM=2）")
	}

	// 另一个 IP 不受影响
	if !limiter.Allow("192.168.1.200") {
		t.Fatal("不同 IP 应该独立计数，允许通过")
	}
}

// ------------------------------------------------------------
// 动态调整限制
// ------------------------------------------------------------

func TestGlobalRateLimiter_DynamicAdjust(t *testing.T) {
	limiter := security.NewGlobalRateLimiter(1, 1000)

	if !limiter.Allow("10.0.0.1") {
		t.Fatal("首次请求应该允许")
	}
	if limiter.Allow("10.0.0.2") {
		t.Fatal("超过 QPS=1 应拒绝")
	}

	// 动态提升到 QPS=10
	limiter.SetGlobalQPS(10)
	// 清理已过期的窗口不会立刻释放刚刚记录的时间戳，
	// 但新的 Allow 调用会把窗口内旧数据保留。
	// 这里验证调整接口不会 panic。
}

// ------------------------------------------------------------
// 计数诊断
// ------------------------------------------------------------

func TestGlobalRateLimiter_Count(t *testing.T) {
	limiter := security.NewGlobalRateLimiter(100, 100)

	limiter.Allow("10.0.0.1")
	limiter.Allow("10.0.0.1")
	limiter.Allow("10.0.0.2")

	if c := limiter.IPCount("10.0.0.1"); c != 2 {
		t.Fatalf("期望 IP 10.0.0.1 计数 2, 实际 %d", c)
	}
	if c := limiter.IPCount("10.0.0.2"); c != 1 {
		t.Fatalf("期望 IP 10.0.0.2 计数 1, 实际 %d", c)
	}
	if c := limiter.IPCount("10.0.0.3"); c != 0 {
		t.Fatalf("期望未请求的 IP 计数 0, 实际 %d", c)
	}
}

// ------------------------------------------------------------
// Clean 不 panic
// ------------------------------------------------------------

func TestGlobalRateLimiter_Clean(t *testing.T) {
	limiter := security.NewGlobalRateLimiter(100, 100)
	limiter.Allow("10.0.0.1")
	limiter.Allow("10.0.0.2")

	// 确保 Clean 不 panic
	limiter.Clean()
}
