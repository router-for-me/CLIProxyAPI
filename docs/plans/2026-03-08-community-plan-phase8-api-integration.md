# Phase 8: 后端 API 集成

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将所有 community 模块集成到现有 CLIProxyAPI 的 Gin 路由和中间件链中。

**Architecture:** 修改现有代码的入口点，初始化社区模块并注册路由。

**Tech Stack:** Go 1.26 / Gin

**Depends on:** Phase 1-7

---

### Task 1: 创建社区模块初始化器

**Files:**
- Create: `internal/community/community.go`

**Step 1:** 编写 `Community` struct，聚合所有子模块（user/quota/credential/security/stats）。

```go
package community

import (
	"context"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/credential"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/quota"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/security"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/stats"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/community/user"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// Community 公益站平台核心
type Community struct {
	store       db.Store
	userSvc     *user.Service
	jwtMgr      *user.JWTManager
	emailSvc    *user.EmailService
	quotaEngine *quota.Engine
	poolMgr     *quota.PoolManager
	riskEngine  *quota.RiskEngine
	secStack    *security.SecurityStack
	statsSvc    *stats.Aggregator
	credSvc     *credential.RedemptionService
}

// New 初始化社区模块
func New(ctx context.Context, cfg config.CommunityConfig) (*Community, error) {
	// 1. 初始化数据库
	store, err := db.NewStore(ctx, cfg.Database.Driver, cfg.Database.DSN)
	if err != nil {
		return nil, fmt.Errorf("初始化社区数据库失败: %w", err)
	}

	// 2. 初始化各模块
	userSvc := user.NewService(store)

	accessTTL := time.Duration(cfg.Auth.AccessTokenTTL) * time.Second
	if accessTTL == 0 {
		accessTTL = 2 * time.Hour
	}
	refreshTTL := time.Duration(cfg.Auth.RefreshTokenTTL) * time.Second
	if refreshTTL == 0 {
		refreshTTL = 7 * 24 * time.Hour
	}
	jwtMgr := user.NewJWTManager(cfg.Auth.JWTSecret, accessTTL, refreshTTL)

	emailSvc := user.NewEmailService(
		cfg.SMTP.Host, cfg.SMTP.Port, cfg.SMTP.Username,
		cfg.SMTP.Password, cfg.SMTP.From, cfg.SMTP.UseTLS,
	)

	quotaEngine := quota.NewEngine(store)
	poolMgr := quota.NewPoolManager(store, store)

	riskEngine := quota.NewRiskEngine(store, quota.RiskConfig{
		Enabled:              cfg.Quota.RiskRule.Enabled,
		RPMExceedThreshold:   cfg.Quota.RiskRule.RPMExceedThreshold,
		RPMExceedWindow:      time.Duration(cfg.Quota.RiskRule.RPMExceedWindowSec) * time.Second,
		PenaltyDuration:      time.Duration(cfg.Quota.RiskRule.PenaltyDurationSec) * time.Second,
		PenaltyProbability:   cfg.Quota.RiskRule.PenaltyProbability,
		ProbEnabled:          cfg.Quota.ProbabilityLimit.Enabled,
		ContributorWeight:    cfg.Quota.ProbabilityLimit.ContributorWeight,
		NonContributorWeight: cfg.Quota.ProbabilityLimit.NonContributorWeight,
	})

	return &Community{
		store:       store,
		userSvc:     userSvc,
		jwtMgr:      jwtMgr,
		emailSvc:    emailSvc,
		quotaEngine: quotaEngine,
		poolMgr:     poolMgr,
		riskEngine:  riskEngine,
	}, nil
}

// RegisterRoutes 注册所有社区 API 路由
func (c *Community) RegisterRoutes(engine *gin.Engine) {
	api := engine.Group("/api/v1")

	// 认证路由（无需 JWT）
	authHandler := user.NewAuthHandler(c.userSvc, c.jwtMgr, c.emailSvc)
	authHandler.RegisterRoutes(api)

	// 需要 JWT 的路由
	authed := api.Group("")
	authed.Use(user.JWTMiddleware(c.jwtMgr))

	userHandler := user.NewUserHandler(c.userSvc)
	userHandler.RegisterRoutes(authed)

	// 管理员路由
	admin := authed.Group("")
	admin.Use(user.AdminMiddleware())

	adminUserHandler := user.NewAdminUserHandler(c.userSvc)
	adminUserHandler.RegisterRoutes(admin)
}

// Close 清理资源
func (c *Community) Close() error {
	return c.store.Close()
}
```

**Step 2:** Commit: `git commit -m "feat(community): create unified community module initializer"`

---

### Task 2: 修改主入口集成社区模块

**Files:**
- Modify: `cmd/server/main.go` (~Line 436)
- Modify: `internal/api/server.go` (setupRoutes)

**Step 1:** 在 `main.go` 的 `managementasset.SetCurrentConfig(cfg)` 之后添加社区模块初始化。
**Step 2:** 在 `server.go` 的 `NewServer()` 中注入社区中间件和路由。
**Step 3:** 运行编译测试。
**Step 4:** Commit: `git commit -m "feat: integrate community platform into main server"`

---

### Task 3: 前端面板嵌入和托管

**Files:**
- Create: `internal/panel/embed.go`
- Modify: `internal/api/server.go` (setupRoutes, 添加 /panel/* 路由)

**Step 1:** 编写 `embed.go`：

```go
package panel

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed web/dist/*
var distFS embed.FS

// RegisterRoutes 注册前端面板路由
func RegisterRoutes(engine *gin.Engine) {
	sub, err := fs.Sub(distFS, "web/dist")
	if err != nil {
		return
	}
	fileServer := http.FileServer(http.FS(sub))

	engine.GET("/panel/*filepath", func(c *gin.Context) {
		path := c.Param("filepath")
		// SPA fallback: 非静态资源请求返回 index.html
		f, err := sub.Open(path[1:]) // 去掉前导 /
		if err != nil {
			c.FileFromFS("/index.html", http.FS(sub))
			return
		}
		f.Close()
		c.Request.URL.Path = path
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
```

**Step 2:** 在 `server.go` 的 `setupRoutes()` 中调用 `panel.RegisterRoutes(s.engine)`。
**Step 3:** Commit: `git commit -m "feat(panel): embed and serve React frontend via go:embed"`

---

### Task 4: 更新 config.example.yaml

**Files:**
- Modify: `config.example.yaml`

**Step 1:** 在文件末尾添加 `community:` 配置段示例：

```yaml
# Community platform settings
# community:
#   enabled: true
#   database:
#     driver: "sqlite"
#     dsn: "./community.db"
#   auth:
#     jwt-secret: "your-jwt-secret-change-me"
#     email-register: true
#     invite-required: false
#     referral-enabled: true
#   quota:
#     default-pool-mode: "public"
#     rpm:
#       enabled: true
#       contributor-rpm: 30
#       non-contributor-rpm: 10
#   security:
#     ip-control:
#       enabled: false
#     rate-limit:
#       enabled: true
#       global-qps: 100
#       per-ip-rpm: 60
#   smtp:
#     host: "smtp.qq.com"
#     port: 587
#     username: "your-email@qq.com"
#     password: "your-smtp-password"
#     from: "your-email@qq.com"
#     use-tls: true
#   panel:
#     enabled: true
#     base-path: "/panel"
```

**Step 2:** Commit: `git commit -m "docs(config): add community platform configuration example"`
