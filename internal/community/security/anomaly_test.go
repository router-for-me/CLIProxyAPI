package security_test

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/security"
)

// ============================================================
// 异常检测引擎测试
// ============================================================

// ------------------------------------------------------------
// 测试：低于阈值不触发
// ------------------------------------------------------------

func TestAnomalyDetector_BelowThreshold(t *testing.T) {
	detector := security.NewAnomalyDetector(nil, []security.AnomalyRule{
		{
			Type:      security.AnomalyHighFrequency,
			Threshold: 5,
			Window:    5 * time.Minute,
			Action:    security.AnomalyActionThrottle,
		},
	})

	ctx := context.Background()
	var userID int64 = 42

	// 发送 4 次事件（低于阈值 5）
	for i := 0; i < 4; i++ {
		detector.Observe(ctx, userID, string(security.AnomalyHighFrequency), "")
	}

	action := detector.Evaluate(ctx, userID)
	if action != nil {
		t.Fatalf("低于阈值不应触发异常, 但得到: %+v", action)
	}
}

// ------------------------------------------------------------
// 测试：高频请求检测
// ------------------------------------------------------------

func TestAnomalyDetector_HighFrequency(t *testing.T) {
	detector := security.NewAnomalyDetector(nil, []security.AnomalyRule{
		{
			Type:      security.AnomalyHighFrequency,
			Threshold: 5,
			Window:    5 * time.Minute,
			Action:    security.AnomalyActionThrottle,
		},
	})

	ctx := context.Background()
	var userID int64 = 42

	// 发送 5 次事件（达到阈值）
	for i := 0; i < 5; i++ {
		detector.Observe(ctx, userID, string(security.AnomalyHighFrequency), "")
	}

	action := detector.Evaluate(ctx, userID)
	if action == nil {
		t.Fatal("达到阈值应触发异常")
	}
	if action.RuleType != security.AnomalyHighFrequency {
		t.Fatalf("期望类型 high_frequency, 实际 %s", action.RuleType)
	}
	if action.Action != security.AnomalyActionThrottle {
		t.Fatalf("期望动作 throttle, 实际 %s", action.Action)
	}
	if action.Actual < 5 {
		t.Fatalf("期望实际计数 >= 5, 实际 %d", action.Actual)
	}
}

// ------------------------------------------------------------
// 测试：不同用户独立计数
// ------------------------------------------------------------

func TestAnomalyDetector_IsolatedUsers(t *testing.T) {
	detector := security.NewAnomalyDetector(nil, []security.AnomalyRule{
		{
			Type:      security.AnomalyHighFrequency,
			Threshold: 3,
			Window:    5 * time.Minute,
			Action:    security.AnomalyActionWarn,
		},
	})

	ctx := context.Background()

	// 用户 1 发送 2 次
	detector.Observe(ctx, 1, string(security.AnomalyHighFrequency), "")
	detector.Observe(ctx, 1, string(security.AnomalyHighFrequency), "")

	// 用户 2 发送 3 次
	detector.Observe(ctx, 2, string(security.AnomalyHighFrequency), "")
	detector.Observe(ctx, 2, string(security.AnomalyHighFrequency), "")
	detector.Observe(ctx, 2, string(security.AnomalyHighFrequency), "")

	// 用户 1 不触发
	if action := detector.Evaluate(ctx, 1); action != nil {
		t.Fatalf("用户 1 低于阈值不应触发, 但得到: %+v", action)
	}

	// 用户 2 触发
	if action := detector.Evaluate(ctx, 2); action == nil {
		t.Fatal("用户 2 达到阈值应触发异常")
	}
}

// ------------------------------------------------------------
// 测试：动作严重度选择（多规则同时触发取最严重）
// ------------------------------------------------------------

func TestAnomalyDetector_WorstAction(t *testing.T) {
	detector := security.NewAnomalyDetector(nil, []security.AnomalyRule{
		{
			Type:      security.AnomalyHighFrequency,
			Threshold: 2,
			Window:    5 * time.Minute,
			Action:    security.AnomalyActionWarn, // 严重度 1
		},
		{
			Type:      security.AnomalyErrorSpike,
			Threshold: 2,
			Window:    5 * time.Minute,
			Action:    security.AnomalyActionBan, // 严重度 3
		},
	})

	ctx := context.Background()
	var userID int64 = 99

	// 同时触发两条规则
	for i := 0; i < 3; i++ {
		detector.Observe(ctx, userID, string(security.AnomalyHighFrequency), "")
		detector.Observe(ctx, userID, string(security.AnomalyErrorSpike), "")
	}

	action := detector.Evaluate(ctx, userID)
	if action == nil {
		t.Fatal("应触发异常")
	}
	// 应选择最严重的 ban
	if action.Action != security.AnomalyActionBan {
		t.Fatalf("期望最严重动作 ban, 实际 %s", action.Action)
	}
}

// ------------------------------------------------------------
// 测试：默认规则集加载
// ------------------------------------------------------------

func TestAnomalyDetector_DefaultRules(t *testing.T) {
	// 传入 nil 规则应使用默认规则
	detector := security.NewAnomalyDetector(nil, nil)

	ctx := context.Background()
	var userID int64 = 1

	// 发送少量事件不应触发（默认阈值较高）
	for i := 0; i < 10; i++ {
		detector.Observe(ctx, userID, string(security.AnomalyHighFrequency), "")
	}

	action := detector.Evaluate(ctx, userID)
	if action != nil {
		t.Fatalf("默认阈值 100，10 次请求不应触发, 但得到: %+v", action)
	}
}

// ------------------------------------------------------------
// 测试：CleanExpired 不 panic
// ------------------------------------------------------------

func TestAnomalyDetector_CleanExpired(t *testing.T) {
	detector := security.NewAnomalyDetector(nil, nil)
	ctx := context.Background()

	detector.Observe(ctx, 1, string(security.AnomalyHighFrequency), "")
	detector.Observe(ctx, 2, string(security.AnomalyModelScan), "gpt-4")

	// 确保清理不 panic
	detector.CleanExpired()
}
