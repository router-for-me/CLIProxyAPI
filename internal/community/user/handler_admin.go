package user

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// 管理端 Handler — 用户列表 / 封禁 / 解封
// ============================================================

// AdminUserHandler 管理端用户 Handler
type AdminUserHandler struct {
	userSvc *Service
}

// NewAdminUserHandler 创建管理端用户 Handler
func NewAdminUserHandler(userSvc *Service) *AdminUserHandler {
	return &AdminUserHandler{userSvc: userSvc}
}

// RegisterRoutes 注册管理端路由
func (h *AdminUserHandler) RegisterRoutes(rg *gin.RouterGroup) {
	admin := rg.Group("/admin/users")
	admin.GET("", h.ListUsers)
	admin.POST("/:id/ban", h.BanUser)
	admin.POST("/:id/unban", h.UnbanUser)
}

// ListUsers 列出所有用户
func (h *AdminUserHandler) ListUsers(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	search := c.Query("search")

	users, total, err := h.userSvc.ListUsers(c.Request.Context(), db.ListUsersOpts{
		Page:     page,
		PageSize: pageSize,
		Search:   search,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"users": users,
		"total": total,
		"page":  page,
	})
}

// BanUser 封禁用户
func (h *AdminUserHandler) BanUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户 ID"})
		return
	}
	if err := h.userSvc.BanUser(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "用户已封禁"})
}

// UnbanUser 解封用户
func (h *AdminUserHandler) UnbanUser(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的用户 ID"})
		return
	}
	if err := h.userSvc.UnbanUser(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "用户已解封"})
}
