package security

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 异常检测引擎 — 基于规则的实时行为分析
// 支持: 高频请求 / 模型扫描 / 错误飙升 三种检测模式
// ============================================================

// AnomalyType 异常类型枚举
type AnomalyType string

const (
	AnomalyHighFrequency AnomalyType = "high_frequency" // 高频请求
	AnomalyModelScan     AnomalyType = "model_scan"     // 模型扫描
	AnomalyErrorSpike    AnomalyType = "error_spike"     // 错误飙升
)

// AnomalyActionType 触发后的响应动作
type AnomalyActionType string

const (
	AnomalyActionWarn     AnomalyActionType = "warn"     // 仅记录告警
	AnomalyActionThrottle AnomalyActionType = "throttle" // 降速限流
	AnomalyActionBan      AnomalyActionType = "ban"      // 临时封禁
)

// ============================================================
// 规则配置
// ============================================================

// AnomalyRule 单条异常检测规则
type AnomalyRule struct {
	Type      AnomalyType       // 检测类型
	Threshold int               // 触发阈值
	Window    time.Duration     // 检测窗口
	Action    AnomalyActionType // 触发动作
}

// DefaultAnomalyRules 默认规则集
func DefaultAnomalyRules() []AnomalyRule {
	return []AnomalyRule{
		{
			Type:      AnomalyHighFrequency,
			Threshold: 100,
			Window:    5 * time.Minute,
			Action:    AnomalyActionThrottle,
		},
		{
			Type:      AnomalyModelScan,
			Threshold: 20,
			Window:    10 * time.Minute,
			Action:    AnomalyActionWarn,
		},
		{
			Type:      AnomalyErrorSpike,
			Threshold: 50,
			Window:    5 * time.Minute,
			Action:    AnomalyActionBan,
		},
	}
}

// ============================================================
// 异常检测引擎
// ============================================================

// AnomalyDetector 异常检测引擎
type AnomalyDetector struct {
	mu    sync.Mutex
	store db.SecurityStore
	rules []AnomalyRule

	// 内存事件缓冲: userID -> eventType -> 时间戳列表
	events map[int64]map[string][]int64
	// 模型扫描追踪: userID -> 窗口起始时间 -> 去重模型集合
	models map[int64]*modelScanTracker
}

// modelScanTracker 模型扫描追踪器
type modelScanTracker struct {
	windowStart int64
	uniqueSet   map[string]struct{}
}

// AnomalyAction 检测结果
type AnomalyAction struct {
	RuleType  AnomalyType
	Action    AnomalyActionType
	Detail    string
	Threshold int
	Actual    int
}

// NewAnomalyDetector 创建异常检测引擎
func NewAnomalyDetector(store db.SecurityStore, rules []AnomalyRule) *AnomalyDetector {
	if len(rules) == 0 {
		rules = DefaultAnomalyRules()
	}
	return &AnomalyDetector{
		store:  store,
		rules:  rules,
		events: make(map[int64]map[string][]int64),
		models: make(map[int64]*modelScanTracker),
	}
}

// ============================================================
// 事件观测
// ============================================================

// Observe 记录一次用户行为事件
// eventType 对应 AnomalyType 的字符串值，detail 为附加信息（如模型名）
func (d *AnomalyDetector) Observe(ctx context.Context, userID int64, eventType string, detail string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UnixMilli()

	// 通用事件记录
	if d.events[userID] == nil {
		d.events[userID] = make(map[string][]int64)
	}
	d.events[userID][eventType] = append(d.events[userID][eventType], now)

	// 模型扫描特殊处理：记录去重模型名
	if eventType == string(AnomalyModelScan) && detail != "" {
		d.trackModel(userID, now, detail)
	}
}

// trackModel 追踪用户访问的唯一模型
func (d *AnomalyDetector) trackModel(userID int64, nowMs int64, model string) {
	tracker, ok := d.models[userID]

	// 查找模型扫描规则的窗口
	windowMs := int64(10 * time.Minute / time.Millisecond) // 默认 10 分钟
	for _, r := range d.rules {
		if r.Type == AnomalyModelScan {
			windowMs = r.Window.Milliseconds()
			break
		}
	}

	// 窗口过期则重置
	if !ok || (nowMs-tracker.windowStart) > windowMs {
		tracker = &modelScanTracker{
			windowStart: nowMs,
			uniqueSet:   make(map[string]struct{}),
		}
		d.models[userID] = tracker
	}

	tracker.uniqueSet[model] = struct{}{}
}

// ============================================================
// 规则评估
// ============================================================

// Evaluate 评估用户是否触发异常规则
// 返回触发的最严重动作，nil 表示正常
func (d *AnomalyDetector) Evaluate(ctx context.Context, userID int64) *AnomalyAction {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UnixMilli()
	var worst *AnomalyAction

	for _, rule := range d.rules {
		var triggered bool
		var actual int

		switch rule.Type {
		case AnomalyHighFrequency:
			actual = d.countInWindow(userID, string(AnomalyHighFrequency), now, rule.Window.Milliseconds())
			triggered = actual >= rule.Threshold

		case AnomalyModelScan:
			actual = d.uniqueModelCount(userID, now, rule.Window.Milliseconds())
			triggered = actual >= rule.Threshold

		case AnomalyErrorSpike:
			actual = d.countInWindow(userID, string(AnomalyErrorSpike), now, rule.Window.Milliseconds())
			triggered = actual >= rule.Threshold
		}

		if triggered {
			action := &AnomalyAction{
				RuleType:  rule.Type,
				Action:    rule.Action,
				Detail:    fmt.Sprintf("%s: %d/%d (窗口 %v)", rule.Type, actual, rule.Threshold, rule.Window),
				Threshold: rule.Threshold,
				Actual:    actual,
			}

			// 选择最严重的动作
			if worst == nil || actionSeverity(action.Action) > actionSeverity(worst.Action) {
				worst = action
			}
		}
	}

	// 触发了异常 → 持久化事件 + 风险标记
	if worst != nil {
		d.persistAnomaly(ctx, userID, worst)
	}

	return worst
}

// countInWindow 统计窗口内的事件数量
func (d *AnomalyDetector) countInWindow(userID int64, eventType string, nowMs int64, windowMs int64) int {
	userEvents, ok := d.events[userID]
	if !ok {
		return 0
	}
	timestamps, ok := userEvents[eventType]
	if !ok {
		return 0
	}

	cutoff := nowMs - windowMs
	count := 0
	for _, ts := range timestamps {
		if ts > cutoff {
			count++
		}
	}
	return count
}

// uniqueModelCount 统计窗口内访问的唯一模型数
func (d *AnomalyDetector) uniqueModelCount(userID int64, nowMs int64, windowMs int64) int {
	tracker, ok := d.models[userID]
	if !ok {
		return 0
	}
	// 窗口过期则视为 0
	if (nowMs - tracker.windowStart) > windowMs {
		return 0
	}
	return len(tracker.uniqueSet)
}

// persistAnomaly 将异常事件写入数据库并创建风险标记
func (d *AnomalyDetector) persistAnomaly(ctx context.Context, userID int64, action *AnomalyAction) {
	if d.store == nil {
		return
	}

	// 记录异常事件
	event := &db.AnomalyEvent{
		UserID:    &userID,
		EventType: string(action.RuleType),
		Detail:    action.Detail,
		Action:    string(action.Action),
		CreatedAt: time.Now(),
	}
	_ = d.store.RecordAnomalyEvent(ctx, event)

	// 仅 throttle / ban 级别才创建风险标记
	if action.Action == AnomalyActionWarn {
		return
	}

	// 根据动作类型决定惩罚时长
	var penalty time.Duration
	switch action.Action {
	case AnomalyActionThrottle:
		penalty = 15 * time.Minute
	case AnomalyActionBan:
		penalty = 1 * time.Hour
	}

	now := time.Now()
	mark := &db.UserRiskMark{
		UserID:      userID,
		MarkType:    db.RiskAnomaly,
		Reason:      action.Detail,
		MarkedAt:    now,
		ExpiresAt:   now.Add(penalty),
		AutoApplied: true,
	}
	_ = d.store.CreateRiskMark(ctx, mark)
}

// ============================================================
// 运维辅助
// ============================================================

// CleanExpired 清理内存中过期的事件缓冲
func (d *AnomalyDetector) CleanExpired() {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UnixMilli()

	// 找到所有规则中最大的窗口
	var maxWindowMs int64
	for _, r := range d.rules {
		if ms := r.Window.Milliseconds(); ms > maxWindowMs {
			maxWindowMs = ms
		}
	}
	cutoff := now - maxWindowMs

	// 清理事件缓冲
	for userID, userEvents := range d.events {
		for eventType, timestamps := range userEvents {
			valid := timestamps[:0]
			for _, ts := range timestamps {
				if ts > cutoff {
					valid = append(valid, ts)
				}
			}
			if len(valid) == 0 {
				delete(userEvents, eventType)
			} else {
				userEvents[eventType] = valid
			}
		}
		if len(userEvents) == 0 {
			delete(d.events, userID)
		}
	}

	// 清理过期模型追踪器
	for userID, tracker := range d.models {
		if (now - tracker.windowStart) > maxWindowMs {
			delete(d.models, userID)
		}
	}
}

// UpdateRules 动态更新检测规则
func (d *AnomalyDetector) UpdateRules(rules []AnomalyRule) {
	d.mu.Lock()
	d.rules = rules
	d.mu.Unlock()
}

// ============================================================
// 内部工具
// ============================================================

// actionSeverity 返回动作严重等级（数值越大越严重）
func actionSeverity(a AnomalyActionType) int {
	switch a {
	case AnomalyActionWarn:
		return 1
	case AnomalyActionThrottle:
		return 2
	case AnomalyActionBan:
		return 3
	default:
		return 0
	}
}
