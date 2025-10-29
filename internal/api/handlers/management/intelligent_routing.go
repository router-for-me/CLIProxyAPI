package management

import (
    "net/http"

    "github.com/gin-gonic/gin"
    coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// GetIntelligentRoutingStats returns usage statistics tracked by the intelligent selector
func (h *Handler) GetIntelligentRoutingStats(c *gin.Context) {
    if h.authManager == nil {
        c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager not available"})
        return
    }

    // Check if intelligent routing is enabled
    enabled := false
    if h.cfg != nil {
        enabled = h.cfg.IntelligentRouting.Enabled
    }

    response := gin.H{
        "enabled": enabled,
    }

    if !enabled {
        response["message"] = "Intelligent routing is disabled"
        c.JSON(http.StatusOK, response)
        return
    }

    // Try to get the intelligent selector instance
    intelligentSelector := coreauth.GetIntelligentSelector()
    if intelligentSelector == nil {
        response["message"] = "Intelligent selector not initialized"
        c.JSON(http.StatusOK, response)
        return
    }

    // Get all auth entries
    auths := h.authManager.List()
    if len(auths) == 0 {
        response["auths"] = []gin.H{}
        c.JSON(http.StatusOK, response)
        return
    }

    // Collect statistics for each auth
    authStats := make([]gin.H, 0, len(auths))
    for _, auth := range auths {
        stats := intelligentSelector.GetAuthUsageStats(auth.ID)
        
        authInfo := gin.H{
            "id":       auth.ID,
            "provider": auth.Provider,
            "label":    auth.Label,
            "status":   string(auth.Status),
        }

        // Add account info if available
        accountType, accountInfo := auth.AccountInfo()
        if accountType != "" {
            authInfo["account_type"] = accountType
            authInfo["account_info"] = accountInfo
        }

        // Add model-specific statistics
        if stats != nil && len(stats) > 0 {
            modelStats := make([]gin.H, 0, len(stats))
            for model, stat := range stats {
                modelStat := gin.H{
                    "model":             model,
                    "total_requests":    stat.TotalRequests,
                    "total_tokens":      stat.TotalTokens,
                    "success_rate":      stat.SuccessRate,
                    "consecutive_errors": stat.ConsecutiveErrors,
                }
                if !stat.LastUsedAt.IsZero() {
                    modelStat["last_used_at"] = stat.LastUsedAt.Format("2006-01-02T15:04:05Z07:00")
                }
                if stat.LastError != nil {
                    modelStat["last_error"] = stat.LastError.Error()
                    if !stat.LastErrorAt.IsZero() {
                        modelStat["last_error_at"] = stat.LastErrorAt.Format("2006-01-02T15:04:05Z07:00")
                    }
                }
                if stat.AvgResponseTime > 0 {
                    modelStat["avg_response_time_ms"] = stat.AvgResponseTime.Milliseconds()
                }
                modelStats = append(modelStats, modelStat)
            }
            authInfo["models"] = modelStats
        } else {
            authInfo["models"] = []gin.H{}
        }

        authStats = append(authStats, authInfo)
    }

    response["auths"] = authStats
    c.JSON(http.StatusOK, response)
}

// UpdateIntelligentRoutingConfig updates intelligent routing configuration
func (h *Handler) UpdateIntelligentRoutingConfig(c *gin.Context) {
    var body struct {
        Enabled               *bool `json:"enabled"`
        StatsRetentionHours   *int  `json:"stats_retention_hours"`
        CleanupIntervalMinutes *int  `json:"cleanup_interval_minutes"`
    }

    if err := c.ShouldBindJSON(&body); err != nil {
        c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
        return
    }

    h.mu.Lock()
    if body.Enabled != nil {
        h.cfg.IntelligentRouting.Enabled = *body.Enabled
    }
    if body.StatsRetentionHours != nil && *body.StatsRetentionHours > 0 {
        h.cfg.IntelligentRouting.StatsRetentionHours = *body.StatsRetentionHours
    }
    if body.CleanupIntervalMinutes != nil && *body.CleanupIntervalMinutes > 0 {
        h.cfg.IntelligentRouting.CleanupIntervalMinutes = *body.CleanupIntervalMinutes
    }
    h.mu.Unlock()

    h.persist(c)
}

// GetIntelligentRoutingConfig returns the current intelligent routing configuration
func (h *Handler) GetIntelligentRoutingConfig(c *gin.Context) {
    h.mu.Lock()
    cfg := h.cfg.IntelligentRouting
    h.mu.Unlock()

    c.JSON(http.StatusOK, gin.H{
        "enabled":                   cfg.Enabled,
        "stats_retention_hours":     cfg.StatsRetentionHours,
        "cleanup_interval_minutes":  cfg.CleanupIntervalMinutes,
    })
}
