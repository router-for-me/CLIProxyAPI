package management

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// requireLiteLLMRuntime returns the runtime PG stores backing the compat
// /litellm routes, plus false (after writing a 503 response) when they are not
// wired. Mirrors requirePG / requireUsers. Guard before every compat handler.
func (h *Handler) requireLiteLLMRuntime(c *gin.Context) (*store.UserStore, *store.APIKeyStore, *store.UsageStore, bool) {
	if h == nil {
		h.pgNotConfigured(c)
		return nil, nil, nil, false
	}
	h.mu.Lock()
	users, keys, usage := h.pgUsers, h.pgAPIKeys, h.pgUsage
	h.mu.Unlock()
	if users == nil || keys == nil || usage == nil {
		h.pgNotConfigured(c)
		return nil, nil, nil, false
	}
	return users, keys, usage, true
}

// litellmCompatError is the shared error envelope for the compat /litellm
// routes, mirroring LiteLLM's {"error": {"type", "message"}} shape.
func litellmCompatError(c *gin.Context, status int, typ, msg string) {
	c.JSON(status, gin.H{"error": gin.H{"type": typ, "message": msg}})
}

// liteLLMCompatUserNewRequest is the JSON payload for POST /litellm/user/new.
type liteLLMCompatUserNewRequest struct {
	UserID              string         `json:"user_id"`
	UserAlias           string         `json:"user_alias"`
	UserEmail           string         `json:"user_email"`
	UserRole            string         `json:"user_role"`
	Models              []string       `json:"models"`
	Metadata            map[string]any `json:"metadata"`
	MaxBudget           *float64       `json:"max_budget"`
	BudgetDuration      string         `json:"budget_duration"`
	RPMLimit            *int64         `json:"rpm_limit"`
	TPMLimit            *int64         `json:"tpm_limit"`
	MaxParallelRequests *int           `json:"max_parallel_requests"`
}

// CreateLiteLLMUserCompat is the runtime-backed POST /litellm/user/new handler.
// It binds a LiteLLM-shaped request and creates an internal user, returning the
// created user with LiteLLM field names.
func (h *Handler) CreateLiteLLMUserCompat(c *gin.Context) {
	users, _, _, ok := h.requireLiteLLMRuntime(c)
	if !ok {
		return
	}
	var req liteLLMCompatUserNewRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		litellmCompatError(c, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	u, err := users.Create(c.Request.Context(), store.InternalUser{
		ID:                  req.UserID,
		UserAlias:           req.UserAlias,
		UserEmail:           req.UserEmail,
		UserRole:            req.UserRole,
		Models:              req.Models,
		Metadata:            req.Metadata,
		MaxBudget:           req.MaxBudget,
		BudgetDuration:      req.BudgetDuration,
		RPMLimit:            req.RPMLimit,
		TPMLimit:            req.TPMLimit,
		MaxParallelRequests: req.MaxParallelRequests,
	})
	if err != nil {
		litellmCompatError(c, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	c.JSON(http.StatusCreated, internalUserToCompat(u))
}

// liteLLMCompatUserResponse is the typed /litellm/user/new response. Typed
// struct + omitempty so unset optional fields are omitted from the JSON,
// matching LiteLLM's Optional-model / OpenAPI parity. Pointer fields (and
// omitempty on strings) mean user_alias="" or max_budget=nil are omitted.
type liteLLMCompatUserResponse struct {
	UserID              string         `json:"user_id"`
	UserAlias           string         `json:"user_alias,omitempty"`
	UserEmail           string         `json:"user_email,omitempty"`
	UserRole            string         `json:"user_role"`
	Models              []string       `json:"models,omitempty"`
	Metadata            map[string]any `json:"metadata,omitempty"`
	MaxBudget           *float64       `json:"max_budget,omitempty"`
	BudgetDuration      string         `json:"budget_duration,omitempty"`
	BudgetResetAt       *time.Time     `json:"budget_reset_at,omitempty"`
	RPMLimit            *int64         `json:"rpm_limit,omitempty"`
	TPMLimit            *int64         `json:"tpm_limit,omitempty"`
	MaxParallelRequests *int           `json:"max_parallel_requests,omitempty"`
	Spend               float64        `json:"spend"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
}

// internalUserToCompat maps an internal user to LiteLLM's /user/new response
// field names (snake_case) rather than NixLLM's internal casing.
func internalUserToCompat(u store.InternalUser) liteLLMCompatUserResponse {
	return liteLLMCompatUserResponse{
		UserID:              u.ID,
		UserAlias:           u.UserAlias,
		UserEmail:           u.UserEmail,
		UserRole:            u.UserRole,
		Models:              u.Models,
		Metadata:            u.Metadata,
		MaxBudget:           u.MaxBudget,
		BudgetDuration:      u.BudgetDuration,
		BudgetResetAt:       u.BudgetResetAt,
		RPMLimit:            u.RPMLimit,
		TPMLimit:            u.TPMLimit,
		MaxParallelRequests: u.MaxParallelRequests,
		Spend:               u.Spend,
		CreatedAt:           u.CreatedAt,
		UpdatedAt:           u.UpdatedAt,
	}
}
