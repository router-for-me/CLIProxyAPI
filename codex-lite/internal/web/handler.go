package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/codex-lite/internal/auth"
	"github.com/codex-lite/internal/manager"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	manager     *manager.Manager
	authDir     string
	oauthPort   int
	sessions    map[string]*loginSession
	sessionsMu  sync.RWMutex
}

type loginSession struct {
	State        string
	PKCE         *auth.PKCECodes
	AuthURL      string
	CreatedAt    time.Time
}

func NewHandler(mgr *manager.Manager, authDir string, oauthPort int) *Handler {
	return &Handler{
		manager:   mgr,
		authDir:   authDir,
		oauthPort: oauthPort,
		sessions:  make(map[string]*loginSession),
	}
}

// ListAccounts 返回所有账号列表
func (h *Handler) ListAccounts(c *gin.Context) {
	accounts := h.manager.List()
	result := make([]gin.H, 0, len(accounts))
	for _, acc := range accounts {
		result = append(result, gin.H{
			"email":        acc.Email,
			"account_id":   acc.AccountID,
			"expire":       acc.Expire,
			"last_refresh": acc.LastRefresh,
			"type":         acc.Type,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"accounts": result,
		"total":    len(result),
	})
}

// GetStatus 返回服务状态
func (h *Handler) GetStatus(c *gin.Context) {
	accounts := h.manager.List()
	c.JSON(http.StatusOK, gin.H{
		"status":         "running",
		"accounts_count": len(accounts),
		"version":        "1.0.0",
	})
}

// generateState 生成随机状态字符串
func generateState() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// StartLogin 启动 OAuth 登录流程
func (h *Handler) StartLogin(c *gin.Context) {
	pkce, err := auth.GeneratePKCE()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	state := generateState()
	codexAuth := auth.NewCodexAuth()
	authURL := codexAuth.GenerateAuthURL(state, pkce)

	session := &loginSession{
		State:     state,
		PKCE:      pkce,
		AuthURL:   authURL,
		CreatedAt: time.Now(),
	}

	h.sessionsMu.Lock()
	h.sessions[state] = session
	h.sessionsMu.Unlock()

	c.JSON(http.StatusOK, gin.H{
		"auth_url": authURL,
		"state":    state,
	})
}

// HandleCallback 处理 OAuth 回调
func (h *Handler) HandleCallback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")
	errParam := c.Query("error")

	if errParam != "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": errParam})
		return
	}

	h.sessionsMu.RLock()
	session, exists := h.sessions[state]
	h.sessionsMu.RUnlock()

	if !exists {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid state"})
		return
	}

	// 交换 code 获取 token
	codexAuth := auth.NewCodexAuth()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	tokenResp, err := codexAuth.ExchangeCode(ctx, code, session.PKCE)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	// 保存 token
	tokenStorage := auth.NewTokenStorage(tokenResp)
	if err := h.manager.Add(tokenStorage); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// 清理 session
	h.sessionsMu.Lock()
	delete(h.sessions, state)
	h.sessionsMu.Unlock()

	// 返回成功页面
	c.Header("Content-Type", "text/html")
	c.String(http.StatusOK, `<!DOCTYPE html>
<html><head><title>Login Success</title></head>
<body style="font-family:sans-serif;text-align:center;padding:50px">
<h1>Login Successful</h1>
<p>Account: %s</p>
<p>You can close this window.</p>
</body></html>`, tokenStorage.Email)
}

// RefreshAccount 刷新指定账号的 token
func (h *Handler) RefreshAccount(c *gin.Context) {
	email := c.Param("email")
	if email == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "email required"})
		return
	}

	accounts := h.manager.List()
	var target *auth.TokenStorage
	for _, acc := range accounts {
		if acc.Email == email {
			target = acc
			break
		}
	}

	if target == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "account not found"})
		return
	}

	codexAuth := auth.NewCodexAuth()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	tokenResp, err := codexAuth.RefreshToken(ctx, target.RefreshToken)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}

	newToken := auth.NewTokenStorage(tokenResp)
	if err := h.manager.Add(newToken); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "token refreshed",
		"email":   newToken.Email,
		"expire":  newToken.Expire,
	})
}
