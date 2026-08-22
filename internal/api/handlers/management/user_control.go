package management

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usercontrol"
)

func (h *Handler) ListManagedUsers(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	users, err := service.ListUsers(c.Request.Context())
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"users": users})
}

func (h *Handler) CreateManagedUser(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	var input usercontrol.CreateUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user payload"})
		return
	}
	user, err := service.CreateUser(c.Request.Context(), input)
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	c.JSON(http.StatusCreated, user)
}

func (h *Handler) GetManagedUser(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	user, err := service.GetUser(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	keys, err := service.ListAPIKeys(c.Request.Context(), user.ID)
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	usage, err := service.GetUsage(c.Request.Context(), user.ID)
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"user": user, "api_keys": keys, "usage": usage})
}

func (h *Handler) UpdateManagedUser(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	var input usercontrol.UpdateUserInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid user payload"})
		return
	}
	user, err := service.UpdateUser(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	c.JSON(http.StatusOK, user)
}

func (h *Handler) DeleteManagedUser(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	if err := service.DeleteUser(c.Request.Context(), c.Param("id")); err != nil {
		writeUserControlError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) ListManagedAPIKeys(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	keys, err := service.ListAPIKeys(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"api_keys": keys})
}

func (h *Handler) CreateManagedAPIKey(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	var input usercontrol.CreateAPIKeyInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid API key payload"})
		return
	}
	key, err := service.CreateAPIKey(c.Request.Context(), c.Param("id"), input)
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	// The complete key is intentionally returned only by this response.
	c.JSON(http.StatusCreated, key)
}

func (h *Handler) RevokeManagedAPIKey(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	if err := service.RevokeAPIKey(c.Request.Context(), c.Param("id")); err != nil {
		writeUserControlError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) GetManagedUserUsage(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	usage, err := service.GetUsage(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	c.JSON(http.StatusOK, usage)
}

func (h *Handler) ListOAuthInvites(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	invites, err := service.ListOAuthInvites(c.Request.Context())
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"invitations": invites})
}

func (h *Handler) CreateOAuthInvite(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	var input usercontrol.CreateOAuthInviteInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid invitation payload"})
		return
	}
	invite, err := service.CreateOAuthInvite(c.Request.Context(), input)
	if err != nil {
		writeUserControlError(c, err)
		return
	}
	invite.URL = publicInviteURL(c, invite.Token)
	c.JSON(http.StatusCreated, invite)
}

func (h *Handler) RevokeOAuthInvite(c *gin.Context) {
	service, ok := h.requireUserControl(c)
	if !ok {
		return
	}
	if err := service.RevokeOAuthInvite(c.Request.Context(), c.Param("id")); err != nil {
		writeUserControlError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) requireUserControl(c *gin.Context) (*usercontrol.Service, bool) {
	h.mu.Lock()
	service := h.userControl
	h.mu.Unlock()
	if service == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "managed users require PGSTORE_DSN"})
		return nil, false
	}
	return service, true
}

func writeUserControlError(c *gin.Context, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, usercontrol.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, usercontrol.ErrInviteUnavailable):
		status = http.StatusGone
	case strings.Contains(strings.ToLower(err.Error()), "required"), strings.Contains(strings.ToLower(err.Error()), "unsupported"):
		status = http.StatusBadRequest
	}
	c.JSON(status, gin.H{"error": err.Error()})
}

func publicInviteURL(c *gin.Context, token string) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	} else if forwarded := strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]); forwarded == "http" || forwarded == "https" {
		scheme = forwarded
	}
	host := strings.TrimSpace(c.Request.Host)
	return scheme + "://" + host + "/oauth/invite/" + token
}
