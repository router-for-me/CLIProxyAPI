package user

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 认证 Handler — 注册 / 登录 / 刷新 / 验证码
// ============================================================

// AuthHandler 认证相关 HTTP Handler
type AuthHandler struct {
	userSvc  *Service
	jwtMgr   *JWTManager
	emailSvc *EmailService
}

// NewAuthHandler 创建认证 Handler
func NewAuthHandler(userSvc *Service, jwtMgr *JWTManager, emailSvc *EmailService) *AuthHandler {
	return &AuthHandler{userSvc: userSvc, jwtMgr: jwtMgr, emailSvc: emailSvc}
}

// RegisterRoutes 注册认证路由
func (h *AuthHandler) RegisterRoutes(rg *gin.RouterGroup) {
	auth := rg.Group("/auth")
	auth.POST("/register", h.Register)
	auth.POST("/login", h.Login)
	auth.POST("/refresh", h.RefreshToken)
	auth.POST("/send-code", h.SendVerificationCode)
}

type registerRequest struct {
	Username   string `json:"username" binding:"required"`
	Email      string `json:"email"`
	Password   string `json:"password" binding:"required,min=6"`
	InviteCode string `json:"invite_code"`
	EmailCode  string `json:"email_code"`
}

// Register 用户注册
func (h *AuthHandler) Register(c *gin.Context) {
	var req registerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	// 邮箱验证码检查（提供邮箱时必须提供验证码）
	if req.Email != "" {
		if req.EmailCode == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "提供邮箱时必须同时提供验证码"})
			return
		}
		if !h.emailSvc.VerifyCode(req.Email, req.EmailCode) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "邮箱验证码错误或已过期"})
			return
		}
	}

	user, err := h.userSvc.Register(c.Request.Context(), RegisterInput{
		Username:   req.Username,
		Email:      req.Email,
		Password:   req.Password,
		InviteCode: req.InviteCode,
	})
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}

	tokens, err := h.jwtMgr.GenerateTokenPair(user.ID, user.UUID, string(user.Role))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 Token 失败"})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"user":   user,
		"tokens": tokens,
	})
}

type loginRequest struct {
	Login    string `json:"login" binding:"required"`    // 用户名或邮箱
	Password string `json:"password" binding:"required"`
}

// Login 用户登录
func (h *AuthHandler) Login(c *gin.Context) {
	var req loginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}

	user, err := h.userSvc.Authenticate(c.Request.Context(), req.Login, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}

	tokens, err := h.jwtMgr.GenerateTokenPair(user.ID, user.UUID, string(user.Role))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 Token 失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"user":   user,
		"tokens": tokens,
	})
}

type refreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// RefreshToken 刷新 Token
func (h *AuthHandler) RefreshToken(c *gin.Context) {
	var req refreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}

	claims, err := h.jwtMgr.ValidateToken(req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Refresh Token 无效"})
		return
	}
	if claims.Type != "refresh" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Token 类型错误"})
		return
	}

	// 检查用户是否仍然存在且未被封禁
	u, err := h.userSvc.GetByID(c.Request.Context(), claims.UserID)
	if err != nil || u == nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "用户不存在"})
		return
	}
	if u.Status == "banned" {
		c.JSON(http.StatusForbidden, gin.H{"error": "账户已被封禁"})
		return
	}

	tokens, err := h.jwtMgr.GenerateTokenPair(claims.UserID, claims.UUID, claims.Role)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "生成 Token 失败"})
		return
	}

	c.JSON(http.StatusOK, tokens)
}

type sendCodeRequest struct {
	Email string `json:"email" binding:"required,email"`
}

// SendVerificationCode 发送邮箱验证码
func (h *AuthHandler) SendVerificationCode(c *gin.Context) {
	var req sendCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效"})
		return
	}

	if err := h.emailSvc.SendVerificationCode(req.Email); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "发送验证码失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "验证码已发送"})
}
