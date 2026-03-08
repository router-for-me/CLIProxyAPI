package security

import (
	"context"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 安全中间件栈 — 统一组装 IP 控制 / 限流 / 异常检测 / 审计
// ============================================================

// SecurityStack 安全中间件栈
type SecurityStack struct {
	IPCtrl    *IPController
	RateLimit *GlobalRateLimiter
	Anomaly   *AnomalyDetector
	Audit     *AuditLogger
}

// NewSecurityStack 创建安全中间件栈
// 根据配置初始化各子模块，并从数据库加载 IP 规则
func NewSecurityStack(store db.SecurityStore, cfg config.SecuritySettings) *SecurityStack {
	// 限流参数: 未启用时传 0 让 Allow 永远通过
	globalQPS := cfg.RateLimit.GlobalQPS
	perIPRPM := cfg.RateLimit.PerIPRPM
	if !cfg.RateLimit.Enabled {
		globalQPS = 0
		perIPRPM = 0
	}

	// 异常检测: 未启用时传空规则集
	var anomalyRules []AnomalyRule
	if cfg.AnomalyDetect.Enabled {
		anomalyRules = DefaultAnomalyRules()
	}

	stack := &SecurityStack{
		IPCtrl:    NewIPController(store, cfg.IPControl.Enabled),
		RateLimit: NewGlobalRateLimiter(globalQPS, perIPRPM),
		Anomaly:   NewAnomalyDetector(store, anomalyRules),
		Audit:     NewAuditLogger(store),
	}

	// 启动时从数据库加载 IP 黑白名单规则
	if err := stack.IPCtrl.LoadRules(context.Background()); err != nil {
		log.WithError(err).Warn("启动时加载 IP 规则失败，将以空规则集运行")
	}

	return stack
}

// ------------------------------------------------------------
// 中间件注册
// ------------------------------------------------------------

// RegisterMiddlewares 按顺序注册所有安全中间件到 Gin Engine
// 执行顺序：IP 控制 -> 限流 -> 审计
func (s *SecurityStack) RegisterMiddlewares(engine *gin.Engine) {
	engine.Use(s.IPCtrl.Middleware())
	engine.Use(s.RateLimit.Middleware())
	engine.Use(s.Audit.Middleware())
}

// Middlewares 返回安全中间件列表（可用于 RouterGroup.Use）
// 执行顺序：IP 控制 -> 限流 -> 审计
func (s *SecurityStack) Middlewares() []gin.HandlerFunc {
	return []gin.HandlerFunc{
		s.IPCtrl.Middleware(),
		s.RateLimit.Middleware(),
		s.Audit.Middleware(),
	}
}

// ------------------------------------------------------------
// 运行时管理
// ------------------------------------------------------------

// ReloadIPRules 重新加载 IP 规则（管理员修改后调用）
func (s *SecurityStack) ReloadIPRules(ctx context.Context) error {
	return s.IPCtrl.LoadRules(ctx)
}
