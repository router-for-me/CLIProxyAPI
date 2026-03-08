package quota

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 风险联动引擎 — RPM 超限检测 + 概率限流 + 自动标记
// ============================================================

// RiskEngine 风险联动引擎
type RiskEngine struct {
	securityStore db.SecurityStore

	// 风险规则配置
	mu                 sync.RWMutex
	enabled            bool
	rpmExceedThreshold int           // 短时间超 RPM 次数阈值
	rpmExceedWindow    time.Duration // 检测窗口
	penaltyDuration    time.Duration // 惩罚持续时间
	penaltyProbability float64       // 惩罚期通过概率

	// 概率限流配置
	probEnabled          bool
	contributorWeight    float64
	nonContributorWeight float64

	// RPM 超限计数器
	exceedMu     sync.Mutex
	exceedCounts map[int64]*exceedRecord // userID -> record
}

type exceedRecord struct {
	count       int
	windowStart time.Time
}

// RiskConfig 风险引擎配置
type RiskConfig struct {
	Enabled            bool
	RPMExceedThreshold int
	RPMExceedWindow    time.Duration
	PenaltyDuration    time.Duration
	PenaltyProbability float64

	ProbEnabled          bool
	ContributorWeight    float64
	NonContributorWeight float64
}

// NewRiskEngine 创建风险引擎
func NewRiskEngine(store db.SecurityStore, cfg RiskConfig) *RiskEngine {
	return &RiskEngine{
		securityStore:        store,
		enabled:              cfg.Enabled,
		rpmExceedThreshold:   cfg.RPMExceedThreshold,
		rpmExceedWindow:      cfg.RPMExceedWindow,
		penaltyDuration:      cfg.PenaltyDuration,
		penaltyProbability:   cfg.PenaltyProbability,
		probEnabled:          cfg.ProbEnabled,
		contributorWeight:    cfg.ContributorWeight,
		nonContributorWeight: cfg.NonContributorWeight,
		exceedCounts:         make(map[int64]*exceedRecord),
	}
}

// RecordRPMExceed 记录一次 RPM 超限事件
func (r *RiskEngine) RecordRPMExceed(ctx context.Context, userID int64) {
	// 先读取配置（加锁保护）
	r.mu.RLock()
	enabled := r.enabled
	threshold := r.rpmExceedThreshold
	window := r.rpmExceedWindow
	penalty := r.penaltyDuration
	r.mu.RUnlock()

	if !enabled {
		return
	}
	r.exceedMu.Lock()
	defer r.exceedMu.Unlock()

	now := time.Now()
	rec, ok := r.exceedCounts[userID]
	if !ok || now.Sub(rec.windowStart) > window {
		r.exceedCounts[userID] = &exceedRecord{count: 1, windowStart: now}
		return
	}
	rec.count++

	// 超过阈值 → 标记用户
	if rec.count >= threshold {
		mark := &db.UserRiskMark{
			UserID:      userID,
			MarkType:    db.RiskRPMAbuse,
			Reason:      "短时间内多次超过 RPM 限制",
			MarkedAt:    now,
			ExpiresAt:   now.Add(penalty),
			AutoApplied: true,
		}
		r.securityStore.CreateRiskMark(ctx, mark)
		// 重置计数
		delete(r.exceedCounts, userID)
	}
}

// ProbabilityCheck 概率限流检查
func (r *RiskEngine) ProbabilityCheck(ctx context.Context, userID int64, isContributor bool) bool {
	// 先读取配置（加锁保护）
	r.mu.RLock()
	probEnabled := r.probEnabled
	penaltyProb := r.penaltyProbability
	contribWeight := r.contributorWeight
	nonContribWeight := r.nonContributorWeight
	r.mu.RUnlock()

	if !probEnabled {
		return true
	}

	// 检查是否在惩罚期
	marks, err := r.securityStore.GetActiveRiskMarks(ctx, userID)
	if err == nil && len(marks) > 0 {
		// 在惩罚期，使用惩罚概率
		return cryptoRandFloat64() < penaltyProb
	}

	// 正常概率
	if isContributor {
		return cryptoRandFloat64() < contribWeight
	}
	return cryptoRandFloat64() < nonContribWeight
}

// cryptoRandFloat64 使用 crypto/rand 生成 [0, 1) 范围的随机浮点数
func cryptoRandFloat64() float64 {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 1.0 // 读取失败时拒绝请求（返回 1.0 >= 任何权重 = denied）
	}
	// 取 uint64 的高 53 位，除以 2^53，得到 [0, 1) 范围
	v := binary.LittleEndian.Uint64(buf[:]) >> 11
	return float64(v) / (1 << 53)
}

// UpdateConfig 动态更新配置
func (r *RiskEngine) UpdateConfig(cfg RiskConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enabled = cfg.Enabled
	r.rpmExceedThreshold = cfg.RPMExceedThreshold
	r.rpmExceedWindow = cfg.RPMExceedWindow
	r.penaltyDuration = cfg.PenaltyDuration
	r.penaltyProbability = cfg.PenaltyProbability
	r.probEnabled = cfg.ProbEnabled
	r.contributorWeight = cfg.ContributorWeight
	r.nonContributorWeight = cfg.NonContributorWeight
}
