package user

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 用户端 Handler — 个人信息 / API Key 重置
// ============================================================

// UserHandler 用户端 HTTP Handler
type UserHandler struct {
	userSvc *Service
}

// NewUserHandler 创建用户端 Handler
func NewUserHandler(userSvc *Service) *UserHandler {
	return &UserHandler{userSvc: userSvc}
}

// RegisterRoutes 注册用户端路由
func (h *UserHandler) RegisterRoutes(rg *gin.RouterGroup) {
	user := rg.Group("/user")
	user.GET("/profile", h.GetProfile)
	user.PUT("/profile", h.UpdateProfile)
	user.POST("/reset-api-key", h.ResetAPIKey)
}

// GetProfile 获取个人信息
func (h *UserHandler) GetProfile(c *gin.Context) {
	userID := c.GetInt64("userID")
	user, err := h.userSvc.GetByID(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "用户不存在"})
		return
	}
	c.JSON(http.StatusOK, user)
}

// UpdateProfile 更新个人信息
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	// 后续实现密码修改、邮箱绑定等
	c.JSON(http.StatusOK, gin.H{"message": "更新成功"})
}

// ResetAPIKey 重置 API Key
func (h *UserHandler) ResetAPIKey(c *gin.Context) {
	userID := c.GetInt64("userID")
	newKey, err := h.userSvc.ResetAPIKey(c.Request.Context(), userID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "重置失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_key": newKey})
}
