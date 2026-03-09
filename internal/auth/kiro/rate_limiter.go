package kiro

import (
	"math"
	"math/rand"
	"strings"
	"sync"
	"time"
)

const (
	DefaultMinTokenInterval  = 1 * time.Second
	DefaultMaxTokenInterval  = 2 * time.Second
	DefaultDailyMaxRequests  = 500
	DefaultJitterPercent     = 0.3
	DefaultBackoffBase       = 30 * time.Second
	DefaultBackoffMax        = 5 * time.Minute
	DefaultBackoffMultiplier = 1.5
	DefaultSuspendCooldown   = 1 * time.Hour
)

// TokenState Token 状态
type TokenState struct {
	LastRequest    time.Time
	RequestCount   int
	CooldownEnd    time.Time
	FailCount      int
	DailyRequests  int
	DailyResetTime time.Time
	IsSuspended    bool
	SuspendedAt    time.Time
	SuspendReason  string
}

// RateLimiter 频率限制器
type RateLimiter struct {
	mu                sync.RWMutex
	states            map[string]*TokenState
	minTokenInterval  time.Duration
	maxTokenInterval  time.Duration
	dailyMaxRequests  int
	jitterPercent     float64
	backoffBase       time.Duration
	backoffMax        time.Duration
	backoffMultiplier float64
	suspendCooldown   time.Duration
	rng               *rand.Rand
}

// NewRateLimiter 创建默认配置的频率限制器
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		states:            make(map[string]*TokenState),
		minTokenInterval:  DefaultMinTokenInterval,
		maxTokenInterval:  DefaultMaxTokenInterval,
		dailyMaxRequests:  DefaultDailyMaxRequests,
		jitterPercent:     DefaultJitterPercent,
		backoffBase:       DefaultBackoffBase,
		backoffMax:        DefaultBackoffMax,
		backoffMultiplier: DefaultBackoffMultiplier,
		suspendCooldown:   DefaultSuspendCooldown,
		rng:               rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// RateLimiterConfig 频率限制器配置
type RateLimiterConfig struct {
	MinTokenInterval  time.Duration
	MaxTokenInterval  time.Duration
	DailyMaxRequests  int
	JitterPercent     float64
	BackoffBase       time.Duration
	BackoffMax        time.Duration
	BackoffMultiplier float64
	SuspendCooldown   time.Duration
}

// NewRateLimiterWithConfig 使用自定义配置创建频率限制器
func NewRateLimiterWithConfig(cfg RateLimiterConfig) *RateLimiter {
	rl := NewRateLimiter()
	if cfg.MinTokenInterval > 0 {
		rl.minTokenInterval = cfg.MinTokenInterval
	}
	if cfg.MaxTokenInterval > 0 {
		rl.maxTokenInterval = cfg.MaxTokenInterval
	}
	if cfg.DailyMaxRequests > 0 {
		rl.dailyMaxRequests = cfg.DailyMaxRequests
	}
	if cfg.JitterPercent > 0 {
		rl.jitterPercent = cfg.JitterPercent
	}
	if cfg.BackoffBase > 0 {
		rl.backoffBase = cfg.BackoffBase
	}
	if cfg.BackoffMax > 0 {
		rl.backoffMax = cfg.BackoffMax
	}
	if cfg.BackoffMultiplier > 0 {
		rl.backoffMultiplier = cfg.BackoffMultiplier
	}
	if cfg.SuspendCooldown > 0 {
		rl.suspendCooldown = cfg.SuspendCooldown
	}
	return rl
}

// getOrCreateState 获取或创建 Token 状态
func (rl *RateLimiter) getOrCreateState(tokenKey string) *TokenState {
	state, exists := rl.states[tokenKey]
	if !exists {
		state = &TokenState{
			DailyResetTime: time.Now().Truncate(24 * time.Hour).Add(24 * time.Hour),
		}
		rl.states[tokenKey] = state
	}
	return state
}

// resetDailyIfNeeded 如果需要则重置每日计数
func (rl *RateLimiter) resetDailyIfNeeded(state *TokenState) {
	now := time.Now()
	if now.After(state.DailyResetTime) {
		state.DailyRequests = 0
		state.DailyResetTime = now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	}
}

// calculateInterval 计算带抖动的随机间隔
func (rl *RateLimiter) calculateInterval() time.Duration {
	baseInterval := rl.minTokenInterval + time.Duration(rl.rng.Int63n(int64(rl.maxTokenInterval-rl.minTokenInterval)))
	jitter := time.Duration(float64(baseInterval) * rl.jitterPercent * (rl.rng.Float64()*2 - 1))
	return baseInterval + jitter
}

// WaitForToken 等待 Token 可用（带抖动的随机间隔）
func (rl *RateLimiter) WaitForToken(tokenKey string) {
	rl.mu.Lock()
	state := rl.getOrCreateState(tokenKey)
	rl.resetDailyIfNeeded(state)

	now := time.Now()

	// 检查是否在冷却期
	if now.Before(state.CooldownEnd) {
		waitTime := state.CooldownEnd.Sub(now)
		rl.mu.Unlock()
		time.Sleep(waitTime)
		rl.mu.Lock()
		state = rl.getOrCreateState(tokenKey)
		now = time.Now()
	}

	// 计算距离上次请求的间隔
	interval := rl.calculateInterval()
	nextAllowedTime := state.LastRequest.Add(interval)

	if now.Before(nextAllowedTime) {
		waitTime := nextAllowedTime.Sub(now)
		rl.mu.Unlock()
		time.Sleep(waitTime)
		rl.mu.Lock()
		state = rl.getOrCreateState(tokenKey)
	}

	state.LastRequest = time.Now()
	state.RequestCount++
	state.DailyRequests++
	rl.mu.Unlock()
}

// MarkTokenFailed 标记 Token 失败
func (rl *RateLimiter) MarkTokenFailed(tokenKey string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	state := rl.getOrCreateState(tokenKey)
	state.FailCount++
	state.CooldownEnd = time.Now().Add(rl.calculateBackoff(state.FailCount))
}

// MarkTokenSuccess 标记 Token 成功
func (rl *RateLimiter) MarkTokenSuccess(tokenKey string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	state := rl.getOrCreateState(tokenKey)
	state.FailCount = 0
	state.CooldownEnd = time.Time{}
}

// CheckAndMarkSuspended 检测暂停错误并标记
func (rl *RateLimiter) CheckAndMarkSuspended(tokenKey string, errorMsg string) bool {
	suspendKeywords := []string{
		"suspended",
		"banned",
		"disabled",
		"account has been",
		"access denied",
		"rate limit exceeded",
		"too many requests",
		"quota exceeded",
	}

	lowerMsg := strings.ToLower(errorMsg)
	for _, keyword := range suspendKeywords {
		if strings.Contains(lowerMsg, keyword) {
			rl.mu.Lock()
			defer rl.mu.Unlock()

			state := rl.getOrCreateState(tokenKey)
			state.IsSuspended = true
			state.SuspendedAt = time.Now()
			state.SuspendReason = errorMsg
			state.CooldownEnd = time.Now().Add(rl.suspendCooldown)
			return true
		}
	}
	return false
}

// IsTokenAvailable 检查 Token 是否可用
func (rl *RateLimiter) IsTokenAvailable(tokenKey string) bool {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	state, exists := rl.states[tokenKey]
	if !exists {
		return true
	}

	now := time.Now()

	// 检查是否被暂停
	if state.IsSuspended {
		if now.After(state.SuspendedAt.Add(rl.suspendCooldown)) {
			return true
		}
		return false
	}

	// 检查是否在冷却期
	if now.Before(state.CooldownEnd) {
		return false
	}

	// 检查每日请求限制
	rl.mu.RUnlock()
	rl.mu.Lock()
	rl.resetDailyIfNeeded(state)
	dailyRequests := state.DailyRequests
	dailyMax := rl.dailyMaxRequests
	rl.mu.Unlock()
	rl.mu.RLock()

	if dailyRequests >= dailyMax {
		return false
	}

	return true
}

// calculateBackoff 计算指数退避时间
func (rl *RateLimiter) calculateBackoff(failCount int) time.Duration {
	if failCount <= 0 {
		return 0
	}

	backoff := float64(rl.backoffBase) * math.Pow(rl.backoffMultiplier, float64(failCount-1))

	// 添加抖动
	jitter := backoff * rl.jitterPercent * (rl.rng.Float64()*2 - 1)
	backoff += jitter

	if time.Duration(backoff) > rl.backoffMax {
		return rl.backoffMax
	}
	return time.Duration(backoff)
}

// GetTokenState 获取 Token 状态（只读）
func (rl *RateLimiter) GetTokenState(tokenKey string) *TokenState {
	rl.mu.RLock()
	defer rl.mu.RUnlock()

	state, exists := rl.states[tokenKey]
	if !exists {
		return nil
	}

	// 返回副本以防止外部修改
	stateCopy := *state
	return &stateCopy
}

// ClearTokenState 清除 Token 状态
func (rl *RateLimiter) ClearTokenState(tokenKey string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	delete(rl.states, tokenKey)
}

// ResetSuspension 重置暂停状态
func (rl *RateLimiter) ResetSuspension(tokenKey string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	state, exists := rl.states[tokenKey]
	if exists {
		state.IsSuspended = false
		state.SuspendedAt = time.Time{}
		state.SuspendReason = ""
		state.CooldownEnd = time.Time{}
		state.FailCount = 0
	}
}
