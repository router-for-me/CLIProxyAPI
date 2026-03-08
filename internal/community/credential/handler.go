package credential

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/db"
)

// ============================================================
// HTTP Handler — 兑换码 / 模板 / 推荐 API
// 聚合 RedemptionService、TemplateService、ReferralService
// 提供用户端和管理端两组路由
// ============================================================

// Handler 凭证管理 HTTP 处理器
type Handler struct {
	redemptionSvc *RedemptionService
	templateSvc   *TemplateService
	referralSvc   *ReferralService
}

// NewHandler 创建凭证管理 Handler
func NewHandler(
	redemptionSvc *RedemptionService,
	templateSvc *TemplateService,
	referralSvc *ReferralService,
) *Handler {
	return &Handler{
		redemptionSvc: redemptionSvc,
		templateSvc:   templateSvc,
		referralSvc:   referralSvc,
	}
}

// RegisterRoutes 注册用户端凭证路由（需 JWT 保护）
func (h *Handler) RegisterRoutes(rg *gin.RouterGroup) {
	cred := rg.Group("/credential")
	cred.POST("/redeem", h.Redeem)
	cred.GET("/templates", h.ListTemplates)
	cred.POST("/claim-template", h.ClaimTemplate)
}

// RegisterAdminRoutes 注册管理端凭证路由（需 JWT + Admin 中间件保护）
func (h *Handler) RegisterAdminRoutes(rg *gin.RouterGroup) {
	admin := rg.Group("/admin/credential")
	admin.POST("/generate-codes", h.GenerateCodes)
	admin.POST("/templates", h.CreateTemplate)
	admin.GET("/codes", h.ListCodes)
}

// ============================================================
// 用户端 — 兑换码核销
// ============================================================

type redeemRequest struct {
	Code string `json:"code" binding:"required"`
}

// Redeem 用户兑换码核销
func (h *Handler) Redeem(c *gin.Context) {
	var req redeemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	userID := c.GetInt64("userID")
	if err := h.redemptionSvc.Redeem(c.Request.Context(), userID, req.Code); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "兑换成功"})
}

// ============================================================
// 用户端 — 模板列表 & 领取
// ============================================================

// ListTemplates 列出可用兑换码模板
func (h *Handler) ListTemplates(c *gin.Context) {
	templates, err := h.templateSvc.ListTemplates(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询模板失败"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"templates": templates})
}

type claimTemplateRequest struct {
	TemplateID int64 `json:"template_id" binding:"required"`
}

// ClaimTemplate 用户从模板领取兑换码
func (h *Handler) ClaimTemplate(c *gin.Context) {
	var req claimTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	userID := c.GetInt64("userID")
	code, err := h.templateSvc.ClaimTemplate(c.Request.Context(), userID, req.TemplateID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"code":    code,
		"message": "领取成功",
	})
}

// ============================================================
// 管理端 — 批量生成兑换码
// ============================================================

type generateCodesRequest struct {
	Count    int    `json:"count" binding:"required,min=1,max=500"`
	MaxUses  int    `json:"max_uses" binding:"required,min=1"`
	ExpiresH int    `json:"expires_hours"`
	BonusQuota *db.QuotaGrant `json:"bonus_quota"`
	ReferralBonus *db.QuotaGrant `json:"referral_bonus"`
	RequireEmail bool `json:"require_email"`
}

// GenerateCodes 管理员批量生成兑换码
func (h *Handler) GenerateCodes(c *gin.Context) {
	var req generateCodesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	creatorID := c.GetInt64("userID")

	cfg := CodeGenConfig{
		MaxUses:       req.MaxUses,
		BonusQuota:    req.BonusQuota,
		ReferralBonus: req.ReferralBonus,
		RequireEmail:  req.RequireEmail,
	}
	if req.ExpiresH > 0 {
		cfg.ExpiresIn = time.Duration(req.ExpiresH) * time.Hour
	}

	codes, err := h.redemptionSvc.GenerateCodes(
		c.Request.Context(), int(creatorID), req.Count, cfg,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"codes": codes,
		"count": len(codes),
	})
}

// ============================================================
// 管理端 — 创建模板
// ============================================================

type createTemplateRequest struct {
	Name        string         `json:"name" binding:"required"`
	Description string         `json:"description"`
	BonusQuota  db.QuotaGrant  `json:"bonus_quota" binding:"required"`
	MaxPerUser  int            `json:"max_per_user" binding:"required,min=1"`
	TotalLimit  int            `json:"total_limit" binding:"required,min=1"`
}

// CreateTemplate 管理员创建兑换码模板
func (h *Handler) CreateTemplate(c *gin.Context) {
	var req createTemplateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请求参数无效: " + err.Error()})
		return
	}

	err := h.templateSvc.CreateTemplate(
		c.Request.Context(),
		req.Name,
		req.Description,
		req.BonusQuota,
		req.MaxPerUser,
		req.TotalLimit,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusCreated, gin.H{"message": "模板创建成功"})
}

// ============================================================
// 管理端 — 查询兑换码列表
// ============================================================

// ListCodes 管理员查询兑换码列表（分页 + 筛选）
func (h *Handler) ListCodes(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))

	opts := db.ListInviteCodesOpts{
		Page:     page,
		PageSize: pageSize,
	}

	// 可选筛选
	if t := c.Query("type"); t != "" {
		opts.Type = &t
	}
	if s := c.Query("status"); s != "" {
		opts.Status = &s
	}

	codes, total, err := h.redemptionSvc.inviteStore.ListInviteCodes(
		c.Request.Context(), opts,
	)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "查询失败"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"codes": codes,
		"total": total,
		"page":  page,
	})
}
