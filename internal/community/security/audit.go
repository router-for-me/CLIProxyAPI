package security

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 审计日志 — 记录所有变更操作，保障平台可追溯性
// ============================================================

// AuditLogger 审计日志记录器
type AuditLogger struct {
	store db.SecurityStore
}

// NewAuditLogger 创建审计日志记录器
func NewAuditLogger(store db.SecurityStore) *AuditLogger {
	return &AuditLogger{store: store}
}

// ------------------------------------------------------------
// 核心方法
// ------------------------------------------------------------

// Record 记录审计日志
func (a *AuditLogger) Record(ctx context.Context, userID *int64, action, target, detail, ip string) error {
	return a.store.CreateAuditLog(ctx, &db.AuditLog{
		UserID:    userID,
		Action:    action,
		Target:    target,
		Detail:    detail,
		IP:        ip,
		CreatedAt: time.Now(),
	})
}

// ------------------------------------------------------------
// Gin 中间件
// ------------------------------------------------------------

// Middleware 自动记录 API 调用的 Gin 中间件
// 仅记录变更操作（POST / PUT / DELETE），GET 请求跳过
func (a *AuditLogger) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Next()

		// 只记录变更操作，GET 请求无需审计
		if c.Request.Method == "GET" {
			return
		}

		// 提取用户 ID（未认证请求 userID 为 nil）
		userID := c.GetInt64("userID")
		var uid *int64
		if userID > 0 {
			uid = &userID
		}

		// 异步写入不阻塞响应；此处简单同步写入，生产环境可改为异步队列
		_ = a.Record(
			c.Request.Context(),
			uid,
			c.Request.Method,
			c.Request.URL.Path,
			"",
			extractIP(c),
		)
	}
}
