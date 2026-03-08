package quota

import (
	"sync"
	"time"
)

// ============================================================
// RPM 限流器 — 滑动窗口算法
// ============================================================

// RPMLimiter 每分钟请求数限流器
type RPMLimiter struct {
	mu      sync.Mutex
	windows map[int64]*slidingWindow // userID -> window
	limit   int
}

type slidingWindow struct {
	timestamps []int64
}

// NewRPMLimiter 创建 RPM 限流器
func NewRPMLimiter(limit int) *RPMLimiter {
	return &RPMLimiter{
		windows: make(map[int64]*slidingWindow),
		limit:   limit,
	}
}

// Allow 检查并记录请求（返回是否允许）
func (r *RPMLimiter) Allow(userID int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UnixMilli()
	cutoff := now - 60_000 // 1分钟窗口

	w, ok := r.windows[userID]
	if !ok {
		w = &slidingWindow{}
		r.windows[userID] = w
	}

	// 移除过期记录
	valid := w.timestamps[:0]
	for _, ts := range w.timestamps {
		if ts > cutoff {
			valid = append(valid, ts)
		}
	}
	w.timestamps = valid

	// 检查是否超限
	if len(w.timestamps) >= r.limit {
		return false
	}

	w.timestamps = append(w.timestamps, now)
	return true
}

// Count 获取用户当前窗口内的请求数
func (r *RPMLimiter) Count(userID int64) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	w, ok := r.windows[userID]
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

// SetLimit 动态调整限制
func (r *RPMLimiter) SetLimit(limit int) {
	r.mu.Lock()
	r.limit = limit
	r.mu.Unlock()
}

// Clean 清理过期窗口数据
func (r *RPMLimiter) Clean() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now().UnixMilli()
	cutoff := now - 60_000

	for uid, w := range r.windows {
		valid := w.timestamps[:0]
		for _, ts := range w.timestamps {
			if ts > cutoff {
				valid = append(valid, ts)
			}
		}
		if len(valid) == 0 {
			delete(r.windows, uid)
		} else {
			w.timestamps = valid
		}
	}
}
