package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelalias"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ApplyClientAPIKeyModelAliases rewrites model list entries for the authenticated client API key.
func ApplyClientAPIKeyModelAliases(h *BaseAPIHandler, c *gin.Context, models []map[string]any) []map[string]any {
	if h == nil || h.Cfg == nil || c == nil {
		return models
	}
	clientKey := clientAPIKeyFromGin(c)
	if clientKey == "" {
		return models
	}
	var compat []internalconfig.OpenAICompatibility
	if h.AuthManager != nil {
		if cfg := h.AuthManager.RuntimeConfig(); cfg != nil {
			compat = cfg.OpenAICompatibility
		}
	}
	return modelalias.ApplyClientAPIKeyModelAliasesToOpenAIMaps(h.Cfg.ClientAPIKeys, clientKey, models, compat)
}

func clientAPIKeyFromGin(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if raw, ok := c.Get("userApiKey"); ok {
		if key, ok := raw.(string); ok {
			return strings.TrimSpace(key)
		}
	}
	return coreauth.ClientAPIKeyPrincipalFromContext(c)
}
