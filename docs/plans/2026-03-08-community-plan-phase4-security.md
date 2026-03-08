# Phase 4: 安全防御体系

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 实现完整的安全防御中间件栈——IP 访问控制、全局限流、异常行为检测、审计日志。

**Architecture:** `internal/community/security/` 模块，所有安全组件为独立的 Gin 中间件，可独立开关。

**Tech Stack:** Go 1.26 / Gin middleware / 滑动窗口 / CIDR 匹配

**Depends on:** Phase 1, Phase 2

---

### Task 1: 实现 IP 访问控制中间件

**Files:**
- Create: `internal/community/security/ipcontrol.go`
- Test: `internal/community/security/ipcontrol_test.go`

**Step 1: 编写 IP 控制器**

```go
package security

import (
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// IPController IP 访问控制器
type IPController struct {
	mu        sync.RWMutex
	enabled   bool
	whitelist []*net.IPNet
	blacklist []*net.IPNet
	store     db.SecurityStore
}

// NewIPController 创建 IP 控制器
func NewIPController(store db.SecurityStore, enabled bool) *IPController {
	return &IPController{store: store, enabled: enabled}
}

// LoadRules 从数据库加载 IP 规则
func (c *IPController) LoadRules() error {
	rules, err := c.store.ListIPRules(nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.whitelist = nil
	c.blacklist = nil
	for _, rule := range rules {
		_, cidr, err := net.ParseCIDR(normalizeCIDR(rule.CIDR))
		if err != nil {
			continue
		}
		if rule.RuleType == "whitelist" {
			c.whitelist = append(c.whitelist, cidr)
		} else {
			c.blacklist = append(c.blacklist, cidr)
		}
	}
	return nil
}

// Middleware Gin 中间件
func (c *IPController) Middleware() gin.HandlerFunc {
	return func(ctx *gin.Context) {
		if !c.enabled {
			ctx.Next()
			return
		}
		ip := net.ParseIP(extractIP(ctx))
		if ip == nil {
			ctx.Next()
			return
		}
		c.mu.RLock()
		defer c.mu.RUnlock()

		// 白名单优先
		if len(c.whitelist) > 0 {
			allowed := false
			for _, cidr := range c.whitelist {
				if cidr.Contains(ip) {
					allowed = true
					break
				}
			}
			if !allowed {
				ctx.JSON(http.StatusForbidden, gin.H{"error": "IP 不在白名单中"})
				ctx.Abort()
				return
			}
		}

		// 黑名单检查
		for _, cidr := range c.blacklist {
			if cidr.Contains(ip) {
				ctx.JSON(http.StatusForbidden, gin.H{"error": "IP 已被封禁"})
				ctx.Abort()
				return
			}
		}
		ctx.Next()
	}
}

func extractIP(c *gin.Context) string {
	ip := c.GetHeader("X-Real-IP")
	if ip == "" {
		ip = c.GetHeader("X-Forwarded-For")
		if idx := strings.Index(ip, ","); idx > 0 {
			ip = ip[:idx]
		}
	}
	if ip == "" {
		ip, _, _ = net.SplitHostPort(c.Request.RemoteAddr)
	}
	return strings.TrimSpace(ip)
}

func normalizeCIDR(s string) string {
	if !strings.Contains(s, "/") {
		if strings.Contains(s, ":") {
			return s + "/128"
		}
		return s + "/32"
	}
	return s
}
```

**Step 2: 编写测试**

```go
package security_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/security"
)

func TestIPController_Blacklist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ctrl := security.NewIPController(nil, true)

	r := gin.New()
	r.Use(ctrl.Middleware())
	r.GET("/test", func(c *gin.Context) { c.JSON(200, gin.H{"ok": true}) })

	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.1:1234"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("无规则时应放行, got %d", w.Code)
	}
}
```

**Step 3:** Run `go test ./internal/community/security/... -v`

**Step 4:** Commit: `git commit -m "feat(security): implement IP access control middleware"`

---

### Task 2: 实现全局请求限流

**Files:**
- Create: `internal/community/security/ratelimit.go`
- Test: `internal/community/security/ratelimit_test.go`

滑动窗口全局 QPS + 单 IP RPM 限流器。结构与 quota/ratelimit.go 类似但作用于 IP 层级。

**Step 1:** 编写 `GlobalRateLimiter` struct，支持全局 QPS 和单 IP RPM。
**Step 2:** 编写 `Middleware()` 返回 Gin 中间件。
**Step 3:** 测试在限制内通过、超限返回 429。
**Step 4:** Commit: `git commit -m "feat(security): implement global QPS and per-IP rate limiter"`

---

### Task 3: 实现异常行为检测器

**Files:**
- Create: `internal/community/security/anomaly.go`
- Test: `internal/community/security/anomaly_test.go`

实现规则引擎，检测：HighFrequency / ModelScan / ErrorSpike 模式。超阈值自动触发 warn/throttle/ban。

**Step 1:** 定义 `AnomalyDetector` struct 和 `AnomalyRule` 配置。
**Step 2:** 实现 `Observe(event)` 方法记录事件，`Evaluate()` 方法检查规则。
**Step 3:** 测试 HighFrequency 检测和自动封禁。
**Step 4:** Commit: `git commit -m "feat(security): implement anomaly behavior detection engine"`

---

### Task 4: 实现审计日志记录

**Files:**
- Create: `internal/community/security/audit.go`

实现审计日志中间件，记录所有管理操作和安全事件到 audit_logs 表。

**Step 1:** 编写 `AuditLogger` struct 和 `Record()` 方法。
**Step 2:** 编写 `Middleware()` 自动记录 API 调用。
**Step 3:** Commit: `git commit -m "feat(security): implement audit logging middleware"`

---

### Task 5: 安全中间件栈集成

**Files:**
- Create: `internal/community/security/stack.go`

将所有安全中间件组装为有序栈，提供统一初始化入口。

**Step 1:** 编写 `SecurityStack` struct，按顺序注册所有中间件。
**Step 2:** 编写 `RegisterMiddlewares(engine *gin.Engine)` 方法。
**Step 3:** Commit: `git commit -m "feat(security): assemble security middleware stack"`
