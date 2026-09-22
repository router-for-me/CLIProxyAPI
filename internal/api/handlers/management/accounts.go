package management

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementauth"
)

func (h *Handler) managementAccountStore() *managementauth.Store {
	if h == nil {
		return nil
	}
	return h.accounts
}

type accountCredentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type accountPasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

type accountUpdateUserRequest struct {
	Disabled *bool   `json:"disabled"`
	Password *string `json:"password"`
}

func (h *Handler) AccountLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	store := h.managementAccountStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authentication unavailable"})
		return
	}
	if !h.managementAccountRemoteAllowed(c) {
		c.JSON(http.StatusForbidden, gin.H{"error": "remote management disabled"})
		return
	}
	var req accountCredentialsRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	token, expires, user, err := store.Login(req.Username, req.Password, accountPeerIP(c))
	if err != nil {
		if errors.Is(err, managementauth.ErrRateLimited) {
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "too many failed login attempts"})
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "expires_at": expires, "user": user})
}

func (h *Handler) AccountMe(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	user, ok := h.managementAccountFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": user})
}

func (h *Handler) AccountLogout(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	store := h.managementAccountStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authentication unavailable"})
		return
	}
	if token := bearerToken(c); token != "" {
		store.Logout(token)
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) AccountPassword(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	store := h.managementAccountStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authentication unavailable"})
		return
	}
	user, ok := h.managementAccountFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req accountPasswordRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if err := store.ChangePassword(user, req.CurrentPassword, req.NewPassword); err != nil {
		if errors.Is(err, managementauth.ErrInvalid) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
			return
		}
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid username or password"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *Handler) AccountUsers(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	store := h.managementAccountStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authentication unavailable"})
		return
	}
	actor, ok := h.managementAccountFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	users, err := store.ListUsers(actor)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

func (h *Handler) AccountCreateUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	store := h.managementAccountStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authentication unavailable"})
		return
	}
	actor, ok := h.managementAccountFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req accountCredentialsRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	user, err := store.CreateUser(actor, req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, managementauth.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		case errors.Is(err, managementauth.ErrConflict):
			c.JSON(http.StatusConflict, gin.H{"error": "user already exists"})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		}
		return
	}
	c.JSON(http.StatusCreated, gin.H{"user": user})
}

func (h *Handler) AccountUpdateUser(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	store := h.managementAccountStore()
	if store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "account authentication unavailable"})
		return
	}
	actor, ok := h.managementAccountFromContext(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var req accountUpdateUserRequest
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16*1024)
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	user, err := store.UpdateUser(actor, c.Param("username"), managementauth.UpdateUserRequest{Disabled: req.Disabled, Password: req.Password})
	if err != nil {
		switch {
		case errors.Is(err, managementauth.ErrForbidden):
			c.JSON(http.StatusForbidden, gin.H{"error": "forbidden"})
		case errors.Is(err, managementauth.ErrNotFound):
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": user})
}

func (h *Handler) managementAccountFromContext(c *gin.Context) (managementauth.User, bool) {
	if c == nil {
		return managementauth.User{}, false
	}
	if v, ok := c.Get("management_account"); ok {
		if user, ok := v.(managementauth.User); ok && user.Username != "" && (user.Role == managementauth.RoleAdmin || user.Role == managementauth.RoleUser) {
			return user, true
		}
	}
	store := h.managementAccountStore()
	if store == nil {
		return managementauth.User{}, false
	}
	return store.Authenticate(bearerToken(c))
}

func (h *Handler) managementAccountRemoteAllowed(c *gin.Context) bool {
	clientIP := accountPeerIP(c)
	localClient := clientIP == "127.0.0.1" || clientIP == "::1"
	if localClient {
		return true
	}
	if h == nil {
		return false
	}
	allowRemote := false
	if h.cfg != nil {
		allowRemote = h.cfg.RemoteManagement.AllowRemote
	}
	if h.allowRemoteOverride {
		allowRemote = true
	}
	return allowRemote
}

func bearerToken(c *gin.Context) string {
	if c == nil {
		return ""
	}
	ah := strings.TrimSpace(c.GetHeader("Authorization"))
	if ah == "" {
		return ""
	}
	parts := strings.SplitN(ah, " ", 2)
	if len(parts) == 2 && strings.EqualFold(parts[0], "bearer") {
		return strings.TrimSpace(parts[1])
	}
	return ah
}
