package controller

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// CustomOAuthProviderResponse is the response structure for custom OAuth providers
// It excludes sensitive fields like client_secret
type CustomOAuthProviderResponse struct {
	Id                    int    `json:"id"`
	Name                  string `json:"name"`
	Slug                  string `json:"slug"`
	Icon                  string `json:"icon"`
	Enabled               bool   `json:"enabled"`
	ClientId              string `json:"client_id"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserInfoEndpoint      string `json:"user_info_endpoint"`
	Scopes                string `json:"scopes"`
	UserIdField           string `json:"user_id_field"`
	UsernameField         string `json:"username_field"`
	DisplayNameField      string `json:"display_name_field"`
	EmailField            string `json:"email_field"`
	WellKnown             string `json:"well_known"`
	AuthStyle             int    `json:"auth_style"`
	AccessPolicy          string `json:"access_policy"`
	AccessDeniedMessage   string `json:"access_denied_message"`
}

type UserOAuthBindingResponse struct {
	ProviderId     int    `json:"provider_id"`
	ProviderName   string `json:"provider_name"`
	ProviderSlug   string `json:"provider_slug"`
	ProviderIcon   string `json:"provider_icon"`
	ProviderUserId string `json:"provider_user_id"`
}

func toCustomOAuthProviderResponse(p *model.CustomOAuthProvider) *CustomOAuthProviderResponse {
	return &CustomOAuthProviderResponse{
		Id:                    p.Id,
		Name:                  p.Name,
		Slug:                  p.Slug,
		Icon:                  p.Icon,
		Enabled:               p.Enabled,
		ClientId:              p.ClientId,
		AuthorizationEndpoint: p.AuthorizationEndpoint,
		TokenEndpoint:         p.TokenEndpoint,
		UserInfoEndpoint:      p.UserInfoEndpoint,
		Scopes:                p.Scopes,
		UserIdField:           p.UserIdField,
		UsernameField:         p.UsernameField,
		DisplayNameField:      p.DisplayNameField,
		EmailField:            p.EmailField,
		WellKnown:             p.WellKnown,
		AuthStyle:             p.AuthStyle,
		AccessPolicy:          p.AccessPolicy,
		AccessDeniedMessage:   p.AccessDeniedMessage,
	}
}

// GetCustomOAuthProviders returns all custom OAuth providers
func GetCustomOAuthProviders(c *gin.Context) {
	providers, err := model.GetAllCustomOAuthProviders()
	if err != nil {
		common.ApiError(c, err)
		return
	}

	response := make([]*CustomOAuthProviderResponse, len(providers))
	for i, p := range providers {
		response[i] = toCustomOAuthProviderResponse(p)
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    response,
	})
}

// GetCustomOAuthProvider returns a single custom OAuth provider by ID
func GetCustomOAuthProvider(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiErrorMsg(c, "无效的 ID")
		return
	}

	provider, err := model.GetCustomOAuthProviderById(id)
	if err != nil {
		common.ApiErrorMsg(c, "未找到该 OAuth 提供商")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    toCustomOAuthProviderResponse(provider),
	})
}

// CreateCustomOAuthProviderRequest is the request structure for creating a custom OAuth provider
type CreateCustomOAuthProviderRequest struct {
	Name                  string `json:"name" binding:"required"`
	Slug                  string `json:"slug" binding:"required"`
	Icon                  string `json:"icon"`
	Enabled               bool   `json:"enabled"`
	ClientId              string `json:"client_id" binding:"required"`
	ClientSecret          string `json:"client_secret" binding:"required"`
	AuthorizationEndpoint string `json:"authorization_endpoint" binding:"required"`
	TokenEndpoint         string `json:"token_endpoint" binding:"required"`
	UserInfoEndpoint      string `json:"user_info_endpoint" binding:"required"`
	Scopes                string `json:"scopes"`
	UserIdField           string `json:"user_id_field"`
	UsernameField         string `json:"username_field"`
	DisplayNameField      string `json:"display_name_field"`
	EmailField            string `json:"email_field"`
	WellKnown             string `json:"well_known"`
	AuthStyle             int    `json:"auth_style"`
	AccessPolicy          string `json:"access_policy"`
	AccessDeniedMessage   string `json:"access_denied_message"`
}

type FetchCustomOAuthDiscoveryRequest struct {
	WellKnownURL string `json:"well_known_url"`
	IssuerURL    string `json:"issuer_url"`
}

// FetchCustomOAuthDiscovery fetches OIDC discovery document via backend (root-only route)
func FetchCustomOAuthDiscovery(c *gin.Context) {
	var req FetchCustomOAuthDiscoveryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "无效的请求参数: "+err.Error())
		return
	}

	wellKnownURL := strings.TrimSpace(req.WellKnownURL)
	issuerURL := strings.TrimSpace(req.IssuerURL)

	if wellKnownURL == "" && issuerURL == "" {
		common.ApiErrorMsg(c, "请先填写 Discovery URL 或 Issuer URL")
		return
	}

	targetURL := wellKnownURL
	if targetURL == "" {
		targetURL = strings.TrimRight(issuerURL, "/") + "/.well-known/openid-configuration"
	}
	targetURL = strings.TrimSpace(targetURL)

	parsedURL, err := url.Parse(targetURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		common.ApiErrorMsg(c, "Discovery URL 无效，仅支持 http/https")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		common.ApiErrorMsg(c, "创建 Discovery 请求失败: "+err.Error())
		return
	}
	httpReq.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		common.ApiErrorMsg(c, "获取 Discovery 配置失败: "+err.Error())
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		message := strings.TrimSpace(string(body))
		if message == "" {
			message = resp.Status
		}
		common.ApiErrorMsg(c, "获取 Discovery 配置失败: "+message)
		return
	}

	var discovery map[string]any
	if err = common.DecodeJson(resp.Body, &discovery); err != nil {
		common.ApiErrorMsg(c, "解析 Discovery 配置失败: "+err.Error())
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"well_known_url": targetURL,
			"discovery":      discovery,
		},
	})
}

// CreateCustomOAuthProvider creates a new custom OAuth provider
func CreateCustomOAuthProvider(c *gin.Context) {
	var req CreateCustomOAuthProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "无效的请求参数: "+err.Error())
		return
	}

	// Check if slug is already taken
	if model.IsSlugTaken(req.Slug, 0) {
		common.ApiErrorMsg(c, "该 Slug 已被使用")
		return
	}

	// Check if slug conflicts with built-in providers
	if oauth.IsProviderRegistered(req.Slug) && !oauth.IsCustomProvider(req.Slug) {
		common.ApiErrorMsg(c, "该 Slug 与内置 OAuth 提供商冲突")
		return
	}

	provider := &model.CustomOAuthProvider{
		Name:                  req.Name,
		Slug:                  req.Slug,
		Icon:                  req.Icon,
		Enabled:               req.Enabled,
		ClientId:              req.ClientId,
		ClientSecret:          req.ClientSecret,
		AuthorizationEndpoint: req.AuthorizationEndpoint,
		TokenEndpoint:         req.TokenEndpoint,
		UserInfoEndpoint:      req.UserInfoEndpoint,
		Scopes:                req.Scopes,
		UserIdField:           req.UserIdField,
		UsernameField:         req.UsernameField,
		DisplayNameField:      req.DisplayNameField,
		EmailField:            req.EmailField,
		WellKnown:             req.WellKnown,
		AuthStyle:             req.AuthStyle,
		AccessPolicy:          req.AccessPolicy,
		AccessDeniedMessage:   req.AccessDeniedMessage,
	}

	if err := model.CreateCustomOAuthProvider(provider); err != nil {
		common.ApiError(c, err)
		return
	}

	// Register the provider in the OAuth registry
	oauth.RegisterOrUpdateCustomProvider(provider)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "创建成功",
		"data":    toCustomOAuthProviderResponse(provider),
	})
}

// UpdateCustomOAuthProviderRequest is the request structure for updating a custom OAuth provider
type UpdateCustomOAuthProviderRequest struct {
	Name                  string  `json:"name"`
	Slug                  string  `json:"slug"`
	Icon                  *string `json:"icon"`    // Optional: if nil, keep existing
	Enabled               *bool   `json:"enabled"` // Optional: if nil, keep existing
	ClientId              string  `json:"client_id"`
	ClientSecret          string  `json:"client_secret"` // Optional: if empty, keep existing
	AuthorizationEndpoint string  `json:"authorization_endpoint"`
	TokenEndpoint         string  `json:"token_endpoint"`
	UserInfoEndpoint      string  `json:"user_info_endpoint"`
	Scopes                string  `json:"scopes"`
	UserIdField           string  `json:"user_id_field"`
	UsernameField         string  `json:"username_field"`
	DisplayNameField      string  `json:"display_name_field"`
	EmailField            string  `json:"email_field"`
	WellKnown             *string `json:"well_known"`            // Optional: if nil, keep existing
	AuthStyle             *int    `json:"auth_style"`            // Optional: if nil, keep existing
	AccessPolicy          *string `json:"access_policy"`         // Optional: if nil, keep existing
	AccessDeniedMessage   *string `json:"access_denied_message"` // Optional: if nil, keep existing
}

// UpdateCustomOAuthProvider updates an existing custom OAuth provider
func UpdateCustomOAuthProvider(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiErrorMsg(c, "无效的 ID")
		return
	}

	var req UpdateCustomOAuthProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ApiErrorMsg(c, "无效的请求参数: "+err.Error())
		return
	}

	// Get existing provider
	provider, err := model.GetCustomOAuthProviderById(id)
	if err != nil {
		common.ApiErrorMsg(c, "未找到该 OAuth 提供商")
		return
	}

	oldSlug := provider.Slug

	// Check if new slug is taken by another provider
	if req.Slug != "" && req.Slug != provider.Slug {
		if model.IsSlugTaken(req.Slug, id) {
			common.ApiErrorMsg(c, "该 Slug 已被使用")
			return
		}
		// Check if slug conflicts with built-in providers
		if oauth.IsProviderRegistered(req.Slug) && !oauth.IsCustomProvider(req.Slug) {
			common.ApiErrorMsg(c, "该 Slug 与内置 OAuth 提供商冲突")
			return
		}
	}

	// Update fields
	if req.Name != "" {
		provider.Name = req.Name
	}
	if req.Slug != "" {
		provider.Slug = req.Slug
	}
	if req.Icon != nil {
		provider.Icon = *req.Icon
	}
	if req.Enabled != nil {
		provider.Enabled = *req.Enabled
	}
	if req.ClientId != "" {
		provider.ClientId = req.ClientId
	}
	if req.ClientSecret != "" {
		provider.ClientSecret = req.ClientSecret
	}
	if req.AuthorizationEndpoint != "" {
		provider.AuthorizationEndpoint = req.AuthorizationEndpoint
	}
	if req.TokenEndpoint != "" {
		provider.TokenEndpoint = req.TokenEndpoint
	}
	if req.UserInfoEndpoint != "" {
		provider.UserInfoEndpoint = req.UserInfoEndpoint
	}
	if req.Scopes != "" {
		provider.Scopes = req.Scopes
	}
	if req.UserIdField != "" {
		provider.UserIdField = req.UserIdField
	}
	if req.UsernameField != "" {
		provider.UsernameField = req.UsernameField
	}
	if req.DisplayNameField != "" {
		provider.DisplayNameField = req.DisplayNameField
	}
	if req.EmailField != "" {
		provider.EmailField = req.EmailField
	}
	if req.WellKnown != nil {
		provider.WellKnown = *req.WellKnown
	}
	if req.AuthStyle != nil {
		provider.AuthStyle = *req.AuthStyle
	}
	if req.AccessPolicy != nil {
		provider.AccessPolicy = *req.AccessPolicy
	}
	if req.AccessDeniedMessage != nil {
		provider.AccessDeniedMessage = *req.AccessDeniedMessage
	}

	if err := model.UpdateCustomOAuthProvider(provider); err != nil {
		common.ApiError(c, err)
		return
	}

	// Update the provider in the OAuth registry
	if oldSlug != provider.Slug {
		oauth.UnregisterCustomProvider(oldSlug)
	}
	oauth.RegisterOrUpdateCustomProvider(provider)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "更新成功",
		"data":    toCustomOAuthProviderResponse(provider),
	})
}

// DeleteCustomOAuthProvider deletes a custom OAuth provider
func DeleteCustomOAuthProvider(c *gin.Context) {
	idStr := c.Param("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		common.ApiErrorMsg(c, "无效的 ID")
		return
	}

	// Get existing provider to get slug
	provider, err := model.GetCustomOAuthProviderById(id)
	if err != nil {
		common.ApiErrorMsg(c, "未找到该 OAuth 提供商")
		return
	}

	// Check if there are any user bindings
	count, err := model.GetBindingCountByProviderId(id)
	if err != nil {
		common.SysError("Failed to get binding count for provider " + strconv.Itoa(id) + ": " + err.Error())
		common.ApiErrorMsg(c, "检查用户绑定时发生错误，请稍后重试")
		return
	}
	if count > 0 {
		common.ApiErrorMsg(c, "该 OAuth 提供商还有用户绑定，无法删除。请先解除所有用户绑定。")
		return
	}

	if err := model.DeleteCustomOAuthProvider(id); err != nil {
		common.ApiError(c, err)
		return
	}

	// Unregister the provider from the OAuth registry
	oauth.UnregisterCustomProvider(provider.Slug)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "删除成功",
	})
}

func buildUserOAuthBindingsResponse(userId int) ([]UserOAuthBindingResponse, error) {
	bindings, err := model.GetUserOAuthBindingsByUserId(userId)
	if err != nil {
		return nil, err
	}

	response := make([]UserOAuthBindingResponse, 0, len(bindings))
	for _, binding := range bindings {
		provider, err := model.GetCustomOAuthProviderById(binding.ProviderId)
		if err != nil {
			continue
		}
		response = append(response, UserOAuthBindingResponse{
			ProviderId:     binding.ProviderId,
			ProviderName:   provider.Name,
			ProviderSlug:   provider.Slug,
			ProviderIcon:   provider.Icon,
			ProviderUserId: binding.ProviderUserId,
		})
	}

	return response, nil
}

// GetUserOAuthBindings returns all OAuth bindings for the current user
func GetUserOAuthBindings(c *gin.Context) {
	userId := c.GetInt("id")
	if userId == 0 {
		common.ApiErrorMsg(c, "未登录")
		return
	}

	response, err := buildUserOAuthBindingsResponse(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    response,
	})
}

func GetUserOAuthBindingsByAdmin(c *gin.Context) {
	userIdStr := c.Param("id")
	userId, err := strconv.Atoi(userIdStr)
	if err != nil {
		common.ApiErrorMsg(c, "invalid user id")
		return
	}

	targetUser, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	myRole := c.GetInt("role")
	if !canManageTargetRole(myRole, targetUser.Role) {
		common.ApiErrorMsg(c, "no permission")
		return
	}

	response, err := buildUserOAuthBindingsResponse(userId)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    response,
	})
}

// UnbindCustomOAuth unbinds a custom OAuth provider from the current user
func UnbindCustomOAuth(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		writeSecurityOperationError(c, service.ErrAuthTokenInvalid)
		return
	}

	providerIdStr := c.Param("provider_id")
	providerId, err := strconv.Atoi(providerIdStr)
	if err != nil || providerId <= 0 {
		common.ApiErrorMsg(c, "无效的提供商 ID")
		return
	}

	succeeded, notificationFailed := false, false
	defer func() {
		recordUserSecurityAudit(c, identity.UserID, "user.binding_unbind", map[string]any{"provider_id": providerId, "success": succeeded, "notification_failed": notificationFailed})
	}()
	context, err := common.Marshal(service.AccountUnbindingContext{ProviderID: providerId})
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeAccountUnbind, Context: context}) == nil {
		return
	}
	if err := service.UnbindAccountOAuth(identity, providerId); err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	succeeded = true
	user, err := model.GetUserById(identity.UserID, false)
	if err != nil {
		writeSecurityOperationError(c, err)
		return
	}
	notificationFailed = service.NotifyAccountSecurityChange(user.Email, "Login account unlinked") != nil

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "解绑成功",
		"data":    gin.H{"notification_warning": notificationFailed},
	})
}

func UnbindCustomOAuthByAdmin(c *gin.Context) {
	userIdStr := c.Param("id")
	userId, err := strconv.Atoi(userIdStr)
	if err != nil {
		common.ApiErrorMsg(c, "invalid user id")
		return
	}

	targetUser, err := model.GetUserById(userId, false)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	myRole := c.GetInt("role")
	if !canManageTargetRole(myRole, targetUser.Role) {
		common.ApiErrorMsg(c, "no permission")
		return
	}

	providerIdStr := c.Param("provider_id")
	providerId, err := strconv.Atoi(providerIdStr)
	if err != nil {
		common.ApiErrorMsg(c, "invalid provider id")
		return
	}

	if err := model.DeleteUserOAuthBinding(userId, providerId); err != nil {
		common.ApiError(c, err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "success",
	})
}
