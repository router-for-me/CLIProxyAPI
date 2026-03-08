# Phase 3: 额度引擎

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 实现额度计量系统——支持次数/token 双计量、独立池/公共池/贡献者池三种模式、RPM限流+概率限流+风险联动。

**Architecture:** `internal/community/quota/` 模块实现额度引擎。通过 Gin 中间件在请求链中拦截检查。

**Tech Stack:** Go 1.26 / Gin middleware / 滑动窗口算法

**Depends on:** Phase 1, Phase 2

---

### Task 1: 实现额度检查引擎核心

**Files:**
- Create: `internal/community/quota/engine.go`
- Test: `internal/community/quota/engine_test.go`

**Step 1: 编写额度引擎**

创建 `internal/community/quota/engine.go`：

```go
package quota

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 额度引擎 — 检查 / 扣减 / 恢复
// ============================================================

// Engine 额度引擎
type Engine struct {
	store db.QuotaStore
}

// NewEngine 创建额度引擎
func NewEngine(store db.QuotaStore) *Engine {
	return &Engine{store: store}
}

// CheckResult 额度检查结果
type CheckResult struct {
	Allowed        bool   `json:"allowed"`
	Reason         string `json:"reason,omitempty"`
	RemainingReqs  int64  `json:"remaining_requests,omitempty"`
	RemainingToks  int64  `json:"remaining_tokens,omitempty"`
}

// Check 检查用户是否有足够额度调用指定模型
func (e *Engine) Check(ctx context.Context, userID int64, model string) (*CheckResult, error) {
	// 查找匹配的额度配置
	cfg, err := e.findMatchingConfig(ctx, model)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		// 无配置 = 无限制
		return &CheckResult{Allowed: true}, nil
	}

	// 获取用户额度状态
	quota, err := e.store.GetUserQuota(ctx, userID, cfg.ModelPattern)
	if err != nil {
		return nil, fmt.Errorf("查询用户额度失败: %w", err)
	}

	// 计算剩余额度
	result := &CheckResult{Allowed: true}

	// 检查请求次数
	if cfg.QuotaType == db.QuotaCount || cfg.QuotaType == db.QuotaBoth {
		if cfg.MaxRequests > 0 {
			totalAllowed := cfg.MaxRequests
			if quota != nil {
				totalAllowed += quota.BonusRequests
			}
			used := int64(0)
			if quota != nil {
				used = quota.UsedRequests
			}
			result.RemainingReqs = totalAllowed - used
			if result.RemainingReqs <= 0 {
				result.Allowed = false
				result.Reason = fmt.Sprintf("模型 %s 请求次数已用尽", model)
				return result, nil
			}
		}
	}

	// 检查 token 额度
	if cfg.QuotaType == db.QuotaToken || cfg.QuotaType == db.QuotaBoth {
		if cfg.MaxTokens > 0 {
			totalAllowed := cfg.MaxTokens
			if quota != nil {
				totalAllowed += quota.BonusTokens
			}
			used := int64(0)
			if quota != nil {
				used = quota.UsedTokens
			}
			result.RemainingToks = totalAllowed - used
			if result.RemainingToks <= 0 {
				result.Allowed = false
				result.Reason = fmt.Sprintf("模型 %s token 额度已用尽", model)
				return result, nil
			}
		}
	}

	return result, nil
}

// Deduct 扣减额度（请求完成后调用）
func (e *Engine) Deduct(ctx context.Context, userID int64, model string, requests int64, tokens int64) error {
	cfg, err := e.findMatchingConfig(ctx, model)
	if err != nil {
		return err
	}
	if cfg == nil {
		return nil // 无配置 = 无需扣减
	}
	return e.store.DeductUserQuota(ctx, userID, cfg.ModelPattern, requests, tokens)
}

// GrantBonus 赠送额度（兑换码/邀请奖励）
func (e *Engine) GrantBonus(ctx context.Context, userID int64, grant *db.QuotaGrant) error {
	if grant == nil {
		return nil
	}
	quota, err := e.store.GetUserQuota(ctx, userID, grant.ModelPattern)
	if err != nil {
		return err
	}
	if quota == nil {
		quota = &db.UserQuota{
			UserID:       userID,
			ModelPattern: grant.ModelPattern,
		}
	}
	quota.BonusRequests += grant.Requests
	quota.BonusTokens += grant.Tokens
	return e.store.UpsertUserQuota(ctx, quota)
}

// findMatchingConfig 查找匹配的额度配置（支持通配符）
func (e *Engine) findMatchingConfig(ctx context.Context, model string) (*db.QuotaConfig, error) {
	configs, err := e.store.GetQuotaConfigs(ctx)
	if err != nil {
		return nil, err
	}
	// 精确匹配优先
	for _, cfg := range configs {
		if cfg.ModelPattern == model {
			return cfg, nil
		}
	}
	// 通配符匹配
	for _, cfg := range configs {
		if strings.Contains(cfg.ModelPattern, "*") {
			matched, _ := filepath.Match(cfg.ModelPattern, model)
			if matched {
				return cfg, nil
			}
		}
	}
	return nil, nil
}
```

**Step 2: 编写测试**

创建 `internal/community/quota/engine_test.go`：

```go
package quota_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func newTestEngine(t *testing.T) (*quota.Engine, db.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := db.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("创建存储失败: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return quota.NewEngine(store), store
}

func TestEngine_Check_NoConfig(t *testing.T) {
	engine, _ := newTestEngine(t)
	ctx := context.Background()

	result, err := engine.Check(ctx, 1, "claude-sonnet-4")
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if !result.Allowed {
		t.Fatal("无额度配置时应该允许")
	}
}

func TestEngine_Check_WithConfig_Allowed(t *testing.T) {
	engine, store := newTestEngine(t)
	ctx := context.Background()

	// 创建额度配置
	store.CreateQuotaConfig(ctx, &db.QuotaConfig{
		ModelPattern:  "claude-*",
		QuotaType:     db.QuotaCount,
		MaxRequests:   100,
		RequestPeriod: db.PeriodDaily,
	})

	result, err := engine.Check(ctx, 1, "claude-sonnet-4")
	if err != nil {
		t.Fatalf("检查失败: %v", err)
	}
	if !result.Allowed {
		t.Fatal("有额度时应该允许")
	}
}

func TestEngine_Deduct_And_Exhaust(t *testing.T) {
	engine, store := newTestEngine(t)
	ctx := context.Background()

	store.CreateQuotaConfig(ctx, &db.QuotaConfig{
		ModelPattern:  "gpt-5",
		QuotaType:     db.QuotaCount,
		MaxRequests:   2,
		RequestPeriod: db.PeriodDaily,
	})

	// 扣减两次
	engine.Deduct(ctx, 1, "gpt-5", 1, 0)
	engine.Deduct(ctx, 1, "gpt-5", 1, 0)

	// 第三次应该被拒绝
	result, _ := engine.Check(ctx, 1, "gpt-5")
	if result.Allowed {
		t.Fatal("额度耗尽后应该拒绝")
	}
}
```

**Step 3: 运行测试**

Run: `go test ./internal/community/quota/... -v`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/community/quota/
git commit -m "feat(quota): implement quota check and deduction engine"
```

---

### Task 2: 实现凭证池管理

**Files:**
- Create: `internal/community/quota/pool.go`
- Test: `internal/community/quota/pool_test.go`

**Step 1: 编写凭证池管理器**

创建 `internal/community/quota/pool.go`：

```go
package quota

import (
	"context"
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 凭证池管理器 — 独立池 / 公共池 / 贡献者池
// ============================================================

// PoolManager 凭证池管理器
type PoolManager struct {
	credStore db.CredentialStore
	userStore db.UserStore
}

// NewPoolManager 创建池管理器
func NewPoolManager(credStore db.CredentialStore, userStore db.UserStore) *PoolManager {
	return &PoolManager{credStore: credStore, userStore: userStore}
}

// GetAvailableCredentials 根据用户池模式获取可用凭证
func (pm *PoolManager) GetAvailableCredentials(ctx context.Context, userID int64, provider string) ([]*db.Credential, error) {
	user, err := pm.userStore.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}

	switch user.PoolMode {
	case db.PoolPrivate:
		return pm.credStore.GetUserCredentials(ctx, userID, provider)

	case db.PoolPublic:
		return pm.credStore.GetPublicPoolCredentials(ctx, provider)

	case db.PoolContributor:
		// 检查用户是否贡献过凭证
		userCreds, err := pm.credStore.GetUserCredentials(ctx, userID, "")
		if err != nil {
			return nil, err
		}
		if len(userCreds) == 0 {
			return nil, fmt.Errorf("贡献者模式: 请先上传凭证才能使用公共池")
		}
		// 合并公共池和用户私有池
		publicCreds, err := pm.credStore.GetPublicPoolCredentials(ctx, provider)
		if err != nil {
			return nil, err
		}
		privateCreds, err := pm.credStore.GetUserCredentials(ctx, userID, provider)
		if err != nil {
			return nil, err
		}
		return append(publicCreds, privateCreds...), nil

	default:
		return nil, fmt.Errorf("未知的池模式: %s", user.PoolMode)
	}
}

// IsContributor 检查用户是否为凭证贡献者
func (pm *PoolManager) IsContributor(ctx context.Context, userID int64) (bool, error) {
	creds, err := pm.credStore.GetUserCredentials(ctx, userID, "")
	if err != nil {
		return false, err
	}
	return len(creds) > 0, nil
}
```

**Step 2: 编写测试**

创建 `internal/community/quota/pool_test.go`：

```go
package quota_test

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func TestPoolManager_PublicMode(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	store.Migrate(ctx)
	defer store.Close()

	// 创建用户（公共池模式）
	user := &db.User{
		UUID: "test-uuid", Username: "testuser", APIKey: "cpk-test",
		Role: db.RoleUser, Status: db.StatusActive, PoolMode: db.PoolPublic,
		InviteCode: "abc123",
	}
	store.CreateUser(ctx, user)

	// 添加公共凭证
	store.CreateCredential(ctx, &db.Credential{
		ID: "pub-1", Provider: "claude", Health: db.HealthHealthy, Weight: 1, Enabled: true,
	})

	pm := quota.NewPoolManager(store, store)
	creds, err := pm.GetAvailableCredentials(ctx, user.ID, "claude")
	if err != nil {
		t.Fatalf("获取凭证失败: %v", err)
	}
	if len(creds) != 1 {
		t.Fatalf("期望 1 个凭证, got %d", len(creds))
	}
}
```

**Step 3: 运行测试**

Run: `go test ./internal/community/quota/... -v -run TestPool`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/community/quota/pool.go internal/community/quota/pool_test.go
git commit -m "feat(quota): implement credential pool manager with three modes"
```

---

### Task 3: 实现 RPM 限流器

**Files:**
- Create: `internal/community/quota/ratelimit.go`
- Test: `internal/community/quota/ratelimit_test.go`

**Step 1: 编写滑动窗口 RPM 限流器**

创建 `internal/community/quota/ratelimit.go`：

```go
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
```

**Step 2: 编写测试**

创建 `internal/community/quota/ratelimit_test.go`：

```go
package quota_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
)

func TestRPMLimiter_Allow(t *testing.T) {
	limiter := quota.NewRPMLimiter(3) // 每分钟 3 次

	if !limiter.Allow(1) {
		t.Fatal("第 1 次请求应该允许")
	}
	if !limiter.Allow(1) {
		t.Fatal("第 2 次请求应该允许")
	}
	if !limiter.Allow(1) {
		t.Fatal("第 3 次请求应该允许")
	}
	if limiter.Allow(1) {
		t.Fatal("第 4 次请求应该拒绝")
	}
}

func TestRPMLimiter_DifferentUsers(t *testing.T) {
	limiter := quota.NewRPMLimiter(1)

	if !limiter.Allow(1) {
		t.Fatal("用户 1 应该允许")
	}
	if !limiter.Allow(2) {
		t.Fatal("用户 2 应该允许（独立计数）")
	}
	if limiter.Allow(1) {
		t.Fatal("用户 1 第二次应该拒绝")
	}
}

func TestRPMLimiter_Count(t *testing.T) {
	limiter := quota.NewRPMLimiter(10)

	limiter.Allow(1)
	limiter.Allow(1)
	limiter.Allow(1)

	if count := limiter.Count(1); count != 3 {
		t.Fatalf("期望 3, got %d", count)
	}
	if count := limiter.Count(2); count != 0 {
		t.Fatalf("期望 0, got %d", count)
	}
}
```

**Step 3: 运行测试**

Run: `go test ./internal/community/quota/... -v -run TestRPM`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/community/quota/ratelimit.go internal/community/quota/ratelimit_test.go
git commit -m "feat(quota): implement sliding window RPM rate limiter"
```

---

### Task 4: 实现风险联动概率限流

**Files:**
- Create: `internal/community/quota/risk.go`
- Test: `internal/community/quota/risk_test.go`

**Step 1: 编写风险联动引擎**

创建 `internal/community/quota/risk.go`：

```go
package quota

import (
	"context"
	"math/rand"
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
	count    int
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
	if !r.enabled {
		return
	}
	r.exceedMu.Lock()
	defer r.exceedMu.Unlock()

	now := time.Now()
	rec, ok := r.exceedCounts[userID]
	if !ok || now.Sub(rec.windowStart) > r.rpmExceedWindow {
		r.exceedCounts[userID] = &exceedRecord{count: 1, windowStart: now}
		return
	}
	rec.count++

	// 超过阈值 → 标记用户
	if rec.count >= r.rpmExceedThreshold {
		mark := &db.UserRiskMark{
			UserID:      userID,
			MarkType:    db.RiskRPMAbuse,
			Reason:      "短时间内多次超过 RPM 限制",
			MarkedAt:    now,
			ExpiresAt:   now.Add(r.penaltyDuration),
			AutoApplied: true,
		}
		r.securityStore.CreateRiskMark(ctx, mark)
		// 重置计数
		delete(r.exceedCounts, userID)
	}
}

// ProbabilityCheck 概率限流检查
func (r *RiskEngine) ProbabilityCheck(ctx context.Context, userID int64, isContributor bool) bool {
	if !r.probEnabled {
		return true
	}

	// 检查是否在惩罚期
	marks, err := r.securityStore.GetActiveRiskMarks(ctx, userID)
	if err == nil && len(marks) > 0 {
		// 在惩罚期，使用惩罚概率
		return rand.Float64() < r.penaltyProbability
	}

	// 正常概率
	if isContributor {
		return rand.Float64() < r.contributorWeight
	}
	return rand.Float64() < r.nonContributorWeight
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
```

**Step 2: 编写测试**

创建 `internal/community/quota/risk_test.go`：

```go
package quota_test

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

func newTestRiskEngine(t *testing.T) (*quota.RiskEngine, db.Store) {
	t.Helper()
	ctx := context.Background()
	store, _ := db.NewSQLiteStore(ctx, ":memory:")
	store.Migrate(ctx)
	t.Cleanup(func() { store.Close() })

	cfg := quota.RiskConfig{
		Enabled:              true,
		RPMExceedThreshold:   3,
		RPMExceedWindow:      5 * time.Minute,
		PenaltyDuration:      10 * time.Minute,
		PenaltyProbability:   0.0, // 惩罚期完全阻止
		ProbEnabled:          true,
		ContributorWeight:    1.0, // 贡献者 100% 通过
		NonContributorWeight: 1.0, // 测试时也 100%
	}
	return quota.NewRiskEngine(store, cfg), store
}

func TestRiskEngine_ProbabilityCheck_Normal(t *testing.T) {
	engine, _ := newTestRiskEngine(t)
	ctx := context.Background()

	// 无惩罚标记，贡献者 100% 通过
	if !engine.ProbabilityCheck(ctx, 1, true) {
		t.Fatal("贡献者权重 1.0 应该通过")
	}
}

func TestRiskEngine_RPMExceed_TriggersRiskMark(t *testing.T) {
	engine, store := newTestRiskEngine(t)
	ctx := context.Background()

	// 创建测试用户
	store.CreateUser(ctx, &db.User{
		UUID: "u1", Username: "test", APIKey: "key1",
		Role: db.RoleUser, Status: db.StatusActive, PoolMode: db.PoolPublic,
		InviteCode: "inv1",
	})

	// 触发 3 次超限
	engine.RecordRPMExceed(ctx, 1)
	engine.RecordRPMExceed(ctx, 1)
	engine.RecordRPMExceed(ctx, 1)

	// 检查是否被标记
	marks, err := store.GetActiveRiskMarks(ctx, 1)
	if err != nil {
		t.Fatalf("查询标记失败: %v", err)
	}
	if len(marks) == 0 {
		t.Fatal("超过阈值后应该被标记")
	}
}
```

**Step 3: 运行测试**

Run: `go test ./internal/community/quota/... -v -run TestRisk`
Expected: PASS

**Step 4: Commit**

```bash
git add internal/community/quota/risk.go internal/community/quota/risk_test.go
git commit -m "feat(quota): implement risk-linked probability rate limiter"
```

---

### Task 5: 实现额度检查 Gin 中间件

**Files:**
- Create: `internal/community/quota/middleware.go`

**Step 1: 编写 Gin 中间件**

创建 `internal/community/quota/middleware.go`：

```go
package quota

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 额度中间件 — 集成到 Gin 请求链
// ============================================================

// Middleware 额度检查中间件
func Middleware(engine *Engine, contributorRPM *RPMLimiter, nonContributorRPM *RPMLimiter,
	riskEngine *RiskEngine, poolMgr *PoolManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := c.GetInt64("userID")
		if userID == 0 {
			c.Next() // 非社区用户，跳过
			return
		}

		// 1. RPM 检查
		isContributor, _ := poolMgr.IsContributor(c.Request.Context(), userID)
		var limiter *RPMLimiter
		if isContributor {
			limiter = contributorRPM
		} else {
			limiter = nonContributorRPM
		}
		if limiter != nil && !limiter.Allow(userID) {
			// 记录超限
			riskEngine.RecordRPMExceed(c.Request.Context(), userID)
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "请求频率超限，请稍后再试"})
			c.Abort()
			return
		}

		// 2. 概率限流检查
		if !riskEngine.ProbabilityCheck(c.Request.Context(), userID, isContributor) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "服务繁忙，请稍后再试"})
			c.Abort()
			return
		}

		// 3. 额度检查
		model := c.GetString("model")
		if model != "" {
			result, err := engine.Check(c.Request.Context(), userID, model)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "额度检查失败"})
				c.Abort()
				return
			}
			if !result.Allowed {
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":  result.Reason,
					"detail": result,
				})
				c.Abort()
				return
			}
		}

		c.Next()

		// 4. 请求完成后扣减额度
		if model != "" && c.Writer.Status() < 400 {
			// 从响应中提取 token 数（如果可用）
			inputTokens := c.GetInt64("inputTokens")
			outputTokens := c.GetInt64("outputTokens")
			engine.Deduct(c.Request.Context(), userID, model, 1, inputTokens+outputTokens)
		}
	}
}
```

**Step 2: 运行编译检查**

Run: `go build ./internal/community/quota/...`
Expected: 无错误

**Step 3: Commit**

```bash
git add internal/community/quota/middleware.go
git commit -m "feat(quota): add Gin middleware integrating RPM, probability, and quota checks"
```
