package security

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 全局限流器 — QPS 全局 + RPM 单 IP 双维度滑动窗口
// ============================================================

// GlobalRateLimiter 双维度限流器
type GlobalRateLimiter struct {
	mu sync.Mutex

	// --- 全局 QPS 维度 ---
	globalQPS    int
	globalWindow *slidingWindowTS

	// --- 单 IP RPM 维度 ---
	perIPRPM  int
	ipWindows map[string]*slidingWindowTS
}

// slidingWindowTS 基于时间戳的滑动窗口
type slidingWindowTS struct {
	timestamps []int64 // 毫秒级时间戳
}

// NewGlobalRateLimiter 创建全局限流器
// globalQPS: 全局每秒最大请求数, perIPRPM: 单 IP 每分钟最大请求数
func NewGlobalRateLimiter(globalQPS, perIPRPM int) *GlobalRateLimiter {
	return &GlobalRateLimiter{
		globalQPS:    globalQPS,
		globalWindow: &slidingWindowTS{},
		perIPRPM:     perIPRPM,
		ipWindows:    make(map[string]*slidingWindowTS),
	}
}

// ============================================================
// 核心判定
// ============================================================

// Allow 检查请求是否允许通过（同时计入全局 + IP 窗口）
func (g *GlobalRateLimiter) Allow(ip string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now().UnixMilli()

	// --- 全局 QPS 检查（1秒窗口）---
	if !g.windowAllow(g.globalWindow, now, 1_000, g.globalQPS) {
		return false
	}

	// --- 单 IP RPM 检查（60秒窗口）---
	w, ok := g.ipWindows[ip]
	if !ok {
		w = &slidingWindowTS{}
		g.ipWindows[ip] = w
	}
	if !g.windowAllow(w, now, 60_000, g.perIPRPM) {
		return false
	}

	// 两层都通过，记录时间戳
	g.globalWindow.timestamps = append(g.globalWindow.timestamps, now)
	w.timestamps = append(w.timestamps, now)
	return true
}

// windowAllow 检查窗口内是否超限（不写入，仅判定）
// limit <= 0 视为不限制，永远通过
func (g *GlobalRateLimiter) windowAllow(w *slidingWindowTS, nowMs int64, windowMs int64, limit int) bool {
	if limit <= 0 {
		return true // 不限制
	}

	cutoff := nowMs - windowMs

	// 移除过期记录
	valid := w.timestamps[:0]
	for _, ts := range w.timestamps {
		if ts > cutoff {
			valid = append(valid, ts)
		}
	}
	w.timestamps = valid

	return len(w.timestamps) < limit
}

// ============================================================
// Gin 中间件
// ============================================================

// Middleware 返回 Gin 限流中间件
func (g *GlobalRateLimiter) Middleware() gin.HandlerFunc {
	return func(gc *gin.Context) {
		ip := extractIP(gc)
		if !g.Allow(ip) {
			gc.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "请求过于频繁，请稍后重试",
			})
			return
		}
		gc.Next()
	}
}

// ============================================================
// 运维辅助
// ============================================================

// SetGlobalQPS 动态调整全局 QPS 上限
func (g *GlobalRateLimiter) SetGlobalQPS(qps int) {
	g.mu.Lock()
	g.globalQPS = qps
	g.mu.Unlock()
}

// SetPerIPRPM 动态调整单 IP RPM 上限
func (g *GlobalRateLimiter) SetPerIPRPM(rpm int) {
	g.mu.Lock()
	g.perIPRPM = rpm
	g.mu.Unlock()
}

// Clean 清理过期窗口数据，释放内存
func (g *GlobalRateLimiter) Clean() {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now().UnixMilli()

	// 清理全局窗口
	g.pruneWindow(g.globalWindow, now, 1_000)

	// 清理 IP 窗口（移除已无记录的 IP）
	for ip, w := range g.ipWindows {
		g.pruneWindow(w, now, 60_000)
		if len(w.timestamps) == 0 {
			delete(g.ipWindows, ip)
		}
	}
}

// pruneWindow 清除窗口中的过期时间戳
func (g *GlobalRateLimiter) pruneWindow(w *slidingWindowTS, nowMs int64, windowMs int64) {
	cutoff := nowMs - windowMs
	valid := w.timestamps[:0]
	for _, ts := range w.timestamps {
		if ts > cutoff {
			valid = append(valid, ts)
		}
	}
	w.timestamps = valid
}

// GlobalCount 获取当前全局 QPS 窗口内的请求数（诊断用）
func (g *GlobalRateLimiter) GlobalCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now().UnixMilli()
	cutoff := now - 1_000
	count := 0
	for _, ts := range g.globalWindow.timestamps {
		if ts > cutoff {
			count++
		}
	}
	return count
}

// IPCount 获取指定 IP 当前 RPM 窗口内的请求数（诊断用）
func (g *GlobalRateLimiter) IPCount(ip string) int {
	g.mu.Lock()
	defer g.mu.Unlock()

	w, ok := g.ipWindows[ip]
	if !ok {
		return 0
	}

	now := time.Now().UnixMilli()
	cutoff := now - 60_000
	count := 0
	for _, ts := range w.timestamps {
		if ts > cutoff {
			count++
		}
	}
	return count
}
