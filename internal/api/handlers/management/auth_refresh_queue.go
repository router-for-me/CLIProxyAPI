package management

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// GetAuthRefreshQueue returns the current auth auto-refresh queue snapshot.
func (h *Handler) GetAuthRefreshQueue(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler not initialized"})
		return
	}

	generatedAt := time.Now().UTC()
	manager := h.currentAuthManager()
	if manager == nil {
		c.JSON(http.StatusOK, gin.H{
			"queue":        []gin.H{},
			"count":        0,
			"generated_at": generatedAt,
		})
		return
	}

	snapshot := manager.RefreshQueueSnapshot(generatedAt)
	queue := make([]gin.H, 0, len(snapshot))
	for _, item := range snapshot {
		entry := h.buildAuthRefreshQueueEntry(item)
		if entry == nil {
			continue
		}
		queue = append(queue, entry)
	}

	c.JSON(http.StatusOK, gin.H{
		"queue":        queue,
		"count":        len(queue),
		"generated_at": generatedAt,
	})
}

func (h *Handler) currentAuthManager() *coreauth.Manager {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.authManager
}

func (h *Handler) buildAuthRefreshQueueEntry(item coreauth.RefreshQueueEntry) gin.H {
	auth := item.Auth
	if auth == nil || item.NextRefreshAt.IsZero() {
		return nil
	}
	auth.EnsureIndex()

	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}

	entry := gin.H{
		"id":              auth.ID,
		"auth_index":      auth.Index,
		"name":            name,
		"provider":        strings.TrimSpace(auth.Provider),
		"status":          string(auth.Status),
		"unavailable":     auth.Unavailable,
		"disabled":        auth.Disabled,
		"next_refresh_at": item.NextRefreshAt.UTC(),
	}

	accountType, account := auth.AccountInfo()
	accountType = strings.TrimSpace(accountType)
	account = strings.TrimSpace(account)
	if accountType != "" {
		entry["account_type"] = accountType
	}
	if account != "" {
		entry["account"] = account
	}
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}

	return entry
}
