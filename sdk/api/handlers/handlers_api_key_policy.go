package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// FilterModelsForRequest keeps only models that at least one credential allowed for the
// request's client key (access.api-key-policies) can serve. Unrestricted requests are unchanged.
func (h *BaseAPIHandler) FilterModelsForRequest(c *gin.Context, models []map[string]any) []map[string]any {
	if h == nil || c == nil || c.Request == nil || len(models) == 0 {
		return models
	}
	allowed := coreauth.AllowedAuthsFromContext(c.Request.Context())
	if allowed == nil {
		if raw, ok := c.Get(coreauth.AllowedAuthsGinKey); ok {
			allowed, _ = raw.(map[string]struct{})
		}
	}
	if allowed == nil {
		return models
	}
	var authIDs []string
	if h.AuthManager != nil {
		for _, auth := range h.AuthManager.List() {
			if auth != nil && coreauth.AuthAllowedBy(allowed, auth) {
				authIDs = append(authIDs, auth.ID)
			}
		}
	}
	modelRegistry := registry.GetGlobalRegistry()
	out := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id, _ := model["id"].(string)
		if id == "" {
			name, _ := model["name"].(string)
			id = strings.TrimPrefix(name, "models/")
		}
		for _, authID := range authIDs {
			if modelRegistry.ClientSupportsModel(authID, id) {
				out = append(out, model)
				break
			}
		}
	}
	return out
}
