package management

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// AntigravityQuotaResponse represents the quota response for a single auth file.
type AntigravityQuotaResponse struct {
	Success bool             `json:"success"`
	Email   string           `json:"email,omitempty"`
	Error   string           `json:"error,omitempty"`
	Quotas  []ModelQuotaInfo `json:"quotas,omitempty"`
}

// ModelQuotaInfo represents quota info for a single model with category grouping.
type ModelQuotaInfo struct {
	Model             string  `json:"model"`
	Category          string  `json:"category"`
	RemainingFraction float64 `json:"remainingFraction"`
	RemainingPercent  float64 `json:"remainingPercent"`
	ResetTime         string  `json:"resetTime"`
	ResetTimeLocal    string  `json:"resetTimeLocal"`
}

// GetAntigravityQuotas returns quota information for a specific antigravity auth file.
// GET /v0/management/antigravity-quotas?id=<auth-file-id>
func (h *Handler) GetAntigravityQuotas(c *gin.Context) {
	authID := c.Query("id")
	if authID == "" {
		c.JSON(http.StatusBadRequest, AntigravityQuotaResponse{
			Success: false,
			Error:   "missing 'id' query parameter",
		})
		return
	}

	if h.authManager == nil {
		c.JSON(http.StatusInternalServerError, AntigravityQuotaResponse{
			Success: false,
			Error:   "auth manager not available",
		})
		return
	}

	var targetAuth *coreauth.Auth
	auths := h.authManager.List()
	for _, auth := range auths {
		if auth.ID == authID {
			targetAuth = auth
			break
		}
	}

	if targetAuth == nil {
		c.JSON(http.StatusNotFound, AntigravityQuotaResponse{
			Success: false,
			Error:   "auth file not found",
		})
		return
	}

	if targetAuth.Provider != "antigravity" {
		c.JSON(http.StatusBadRequest, AntigravityQuotaResponse{
			Success: false,
			Error:   "auth file is not an antigravity type",
		})
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	quotas, err := executor.FetchAntigravityQuotas(ctx, targetAuth, h.cfg)
	if err != nil {
		c.JSON(http.StatusInternalServerError, AntigravityQuotaResponse{
			Success: false,
			Error:   err.Error(),
		})
		return
	}

	email := ""
	if targetAuth.Metadata != nil {
		if e, ok := targetAuth.Metadata["email"].(string); ok {
			email = e
		}
	}

	modelQuotas := make([]ModelQuotaInfo, 0, len(quotas))
	for modelName, quota := range quotas {
		category := categorizeModel(modelName)
		resetTimeLocal := formatResetTime(quota.ResetTime)

		modelQuotas = append(modelQuotas, ModelQuotaInfo{
			Model:             modelName,
			Category:          category,
			RemainingFraction: quota.RemainingFraction,
			RemainingPercent:  quota.RemainingFraction * 100,
			ResetTime:         quota.ResetTime,
			ResetTimeLocal:    resetTimeLocal,
		})
	}

	sort.Slice(modelQuotas, func(i, j int) bool {
		if modelQuotas[i].Category != modelQuotas[j].Category {
			return categoryOrder(modelQuotas[i].Category) < categoryOrder(modelQuotas[j].Category)
		}
		return modelQuotas[i].Model < modelQuotas[j].Model
	})

	c.JSON(http.StatusOK, AntigravityQuotaResponse{
		Success: true,
		Email:   email,
		Quotas:  modelQuotas,
	})
}

// GetAllAntigravityQuotas returns quota information for all antigravity auth files.
// GET /v0/management/antigravity-quotas/all
func (h *Handler) GetAllAntigravityQuotas(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"success": false,
			"error":   "auth manager not available",
		})
		return
	}

	auths := h.authManager.List()
	results := make([]AntigravityQuotaResponse, 0)

	ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
	defer cancel()

	for _, auth := range auths {
		if auth.Provider != "antigravity" {
			continue
		}

		email := ""
		if auth.Metadata != nil {
			if e, ok := auth.Metadata["email"].(string); ok {
				email = e
			}
		}

		quotas, err := executor.FetchAntigravityQuotas(ctx, auth, h.cfg)
		if err != nil {
			results = append(results, AntigravityQuotaResponse{
				Success: false,
				Email:   email,
				Error:   err.Error(),
			})
			continue
		}

		modelQuotas := make([]ModelQuotaInfo, 0, len(quotas))
		for modelName, quota := range quotas {
			category := categorizeModel(modelName)
			resetTimeLocal := formatResetTime(quota.ResetTime)

			modelQuotas = append(modelQuotas, ModelQuotaInfo{
				Model:             modelName,
				Category:          category,
				RemainingFraction: quota.RemainingFraction,
				RemainingPercent:  quota.RemainingFraction * 100,
				ResetTime:         quota.ResetTime,
				ResetTimeLocal:    resetTimeLocal,
			})
		}

		sort.Slice(modelQuotas, func(i, j int) bool {
			if modelQuotas[i].Category != modelQuotas[j].Category {
				return categoryOrder(modelQuotas[i].Category) < categoryOrder(modelQuotas[j].Category)
			}
			return modelQuotas[i].Model < modelQuotas[j].Model
		})

		results = append(results, AntigravityQuotaResponse{
			Success: true,
			Email:   email,
			Quotas:  modelQuotas,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    results,
	})
}

func categorizeModel(modelName string) string {
	lower := strings.ToLower(modelName)
	if strings.Contains(lower, "claude") {
		return "Claude"
	}
	if strings.Contains(lower, "gemini") {
		return "Gemini"
	}
	if strings.Contains(lower, "gpt") {
		return "GPT"
	}
	return "Other"
}

func categoryOrder(category string) int {
	switch category {
	case "Claude":
		return 0
	case "Gemini":
		return 1
	case "GPT":
		return 2
	default:
		return 3
	}
}

func formatResetTime(resetTime string) string {
	if resetTime == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, resetTime)
	if err != nil {
		return resetTime
	}
	return t.Local().Format("01-02 15:04")
}
