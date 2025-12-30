package management

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	log "github.com/sirupsen/logrus"
)

// extractClaudeQuota safely extracts ClaudeCodeQuotaInfo from auth metadata.
// Handles both direct struct pointers and JSON-deserialized maps.
func extractClaudeQuota(metadata map[string]any) *executor.ClaudeCodeQuotaInfo {
	if metadata == nil {
		return nil
	}

	raw, exists := metadata["claude_code_quota"]
	if !exists {
		return nil
	}

	// Try direct type assertion first
	if quota, ok := raw.(*executor.ClaudeCodeQuotaInfo); ok {
		return quota
	}

	// Handle JSON-deserialized map[string]interface{}
	// This happens when auth is loaded from disk
	jsonBytes, err := json.Marshal(raw)
	if err != nil {
		return nil
	}

	var quota executor.ClaudeCodeQuotaInfo
	if err := json.Unmarshal(jsonBytes, &quota); err != nil {
		return nil
	}

	return &quota
}

// GetClaudeCodeQuotas returns quota information for all Claude OAuth accounts.
// GET /v0/management/claude-api-key/quotas
func (h *Handler) GetClaudeCodeQuotas(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusOK, gin.H{"quotas": []interface{}{}})
		return
	}

	quotas := make([]map[string]interface{}, 0)
	for _, auth := range h.authManager.List() {
		// Only include Claude OAuth accounts
		if auth.Provider != "claude" {
			continue
		}
		if auth.Metadata == nil {
			continue
		}
		if _, hasToken := auth.Metadata["access_token"]; !hasToken {
			continue
		}

		quota := extractClaudeQuota(auth.Metadata)
		email := authEmail(auth)
		quotas = append(quotas, map[string]interface{}{
			"auth_id": auth.ID,
			"email":   email,
			"label":   auth.Label,
			"quota":   quota,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"quotas": quotas,
		"count":  len(quotas),
	})
}

// GetClaudeCodeQuota returns quota information for a specific Claude OAuth account.
// GET /v0/management/claude-api-key/quota/:authId
func (h *Handler) GetClaudeCodeQuota(c *gin.Context) {
	authID := c.Param("authId")
	if authID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth ID required"})
		return
	}

	if h == nil || h.authManager == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth manager not available"})
		return
	}

	auth, ok := h.authManager.GetByID(authID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	if auth.Provider != "claude" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth is not a Claude account"})
		return
	}

	if auth.Metadata == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not an OAuth account"})
		return
	}
	if _, hasToken := auth.Metadata["access_token"]; !hasToken {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not an OAuth account"})
		return
	}

	quota := extractClaudeQuota(auth.Metadata)
	email := authEmail(auth)
	c.JSON(http.StatusOK, gin.H{
		"auth_id": authID,
		"email":   email,
		"label":   auth.Label,
		"quota":   quota,
	})
}

// RefreshClaudeCodeQuota performs a quota check for a specific Claude OAuth account.
// POST /v0/management/claude-api-key/quota/:authId/refresh
func (h *Handler) RefreshClaudeCodeQuota(c *gin.Context) {
	authID := c.Param("authId")
	if authID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth ID required"})
		return
	}

	if h == nil || h.authManager == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth manager not available"})
		return
	}

	auth, ok := h.authManager.GetByID(authID)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	if auth.Provider != "claude" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth is not a Claude account"})
		return
	}

	if auth.Metadata == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not an OAuth account"})
		return
	}
	if _, hasToken := auth.Metadata["access_token"]; !hasToken {
		c.JSON(http.StatusBadRequest, gin.H{"error": "not an OAuth account"})
		return
	}

	// Ensure Metadata map exists (defensive programming)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}

	// Perform quota check using minimal request
	// Note: CheckQuota modifies auth.Metadata in place
	exec := executor.NewClaudeExecutor(h.cfg)
	quotaInfo, err := exec.CheckQuota(c.Request.Context(), auth)
	if err != nil {
		log.Warnf("failed to check quota for auth %s: %v", authID, err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	// Persist the modified auth (CheckQuota updated auth.Metadata)
	updatedAuth, err := h.authManager.Update(c.Request.Context(), auth)
	if err != nil {
		log.Warnf("failed to persist auth after quota refresh: %v", err)
		// Continue with the local auth object even if persistence failed
		updatedAuth = auth
	}

	email := authEmail(updatedAuth)
	c.JSON(http.StatusOK, gin.H{
		"auth_id": authID,
		"email":   email,
		"label":   updatedAuth.Label,
		"quota":   quotaInfo,
	})
}
