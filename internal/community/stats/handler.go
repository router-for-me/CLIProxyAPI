package stats

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 统计 API Handler — 用户端 + 管理端路由
// 用户端: 查看个人统计
// 管理端: 全局统计 / 指定用户统计 / 数据导出
// ============================================================

// Handler 统计 API Handler
type Handler struct {
	aggregator *Aggregator
	exporter   *Exporter
}

// NewHandler 创建统计 Handler
func NewHandler(aggregator *Aggregator, exporter *Exporter) *Handler {
	return &Handler{
		aggregator: aggregator,
		exporter:   exporter,
	}
}

// ------------------------------------------------------------
// RegisterRoutes 注册路由
// 用户路由 (需 JWT 鉴权):
//   GET /stats/me            — 当前用户统计
// 管理路由 (需 JWT + Admin):
//   GET /admin/stats/global  — 全局统计
//   GET /admin/stats/user/:id — 指定用户统计
//   GET /admin/stats/export  — 数据导出 (支持 csv/json)
// ------------------------------------------------------------

func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	// ---- 用户端路由 ----
	user := rg.Group("/stats")
	user.GET("/me", h.GetMyStats)

	// ---- 管理端路由 ----
	admin := rg.Group("/admin/stats")
	admin.GET("/global", h.GetGlobalStats)
	admin.GET("/user/:id", h.GetUserStats)
	admin.GET("/export", h.ExportStats)
}

// ============================================================
// 用户端接口
// ============================================================

// GetMyStats 获取当前登录用户的统计数据
func (h *Handler) GetMyStats(c *gin.Context) {
	userID := c.GetInt64("userID")
	after, before := parseTimeRange(c)

	result, err := h.aggregator.GetUserStats(c.Request.Context(), userID, after, before)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询统计失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// ============================================================
// 管理端接口
// ============================================================

// GetGlobalStats 获取全局统计数据
func (h *Handler) GetGlobalStats(c *gin.Context) {
	after, before := parseTimeRange(c)

	result, err := h.aggregator.GetGlobalStats(c.Request.Context(), after, before)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询全局统计失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// GetUserStats 获取指定用户的统计数据
func (h *Handler) GetUserStats(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户 ID"})
		return
	}

	after, before := parseTimeRange(c)

	result, err := h.aggregator.GetUserStats(c.Request.Context(), id, after, before)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询用户统计失败"})
		return
	}
	c.JSON(http.StatusOK, result)
}

// ExportStats 导出统计数据
// 查询参数 format: csv(默认) / json
func (h *Handler) ExportStats(c *gin.Context) {
	format := c.DefaultQuery("format", "csv")
	after, before := parseTimeRange(c)

	switch format {
	case "json":
		c.Header("Content-Type", "application/json")
		c.Header("Content-Disposition", "attachment; filename=stats.json")
		if err := h.exporter.ExportJSON(c.Request.Context(), c.Writer, after, before); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "导出失败"})
		}
	default:
		c.Header("Content-Type", "text/csv; charset=utf-8")
		c.Header("Content-Disposition", "attachment; filename=stats.csv")
		if err := h.exporter.ExportCSV(c.Request.Context(), c.Writer, after, before); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "导出失败"})
		}
	}
}

// ============================================================
// 内部辅助函数
// ============================================================

// parseTimeRange 从查询参数解析时间区间
// 支持参数: after (RFC3339), before (RFC3339)
// 解析失败时静默忽略，返回 nil 表示不限制该边界
func parseTimeRange(c *gin.Context) (after, before *time.Time) {
	if raw := c.Query("after"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			after = &t
		}
	}
	if raw := c.Query("before"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			before = &t
		}
	}
	return
}
