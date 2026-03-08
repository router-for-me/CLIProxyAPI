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
