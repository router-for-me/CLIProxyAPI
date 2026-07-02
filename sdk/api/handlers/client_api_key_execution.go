package handlers

import (
	"strings"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/net/context"
)

// resolveModelForProviderLookup applies per-client API key model aliases before registry-based
// provider resolution. The returned model is used only for GetProviderName; execution still
// receives the client-visible model via RequestedModelMetadataKey and conductor alias resolution.
func (h *BaseAPIHandler) resolveModelForProviderLookup(ctx context.Context, modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" || h == nil {
		return modelName
	}
	clientKey := clientAPIKeyFromContext(ctx)
	if clientKey == "" || h.AuthManager == nil {
		return modelName
	}
	upstream := h.AuthManager.ApplyClientAPIKeyModelAlias(clientKey, modelName)
	if strings.TrimSpace(upstream) == "" {
		return modelName
	}
	return upstream
}

func clientAPIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
		return clientAPIKeyFromGin(ginCtx)
	}
	return coreauth.ClientAPIKeyPrincipalFromContext(ctx)
}

// providersForModelName resolves provider keys for a model after auto-model and per-client alias rewrite.
func (h *BaseAPIHandler) providersForModelName(ctx context.Context, resolvedModelName string) []string {
	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)
	providers := util.GetProviderName(baseModel)
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}
	if len(providers) == 0 {
		var cfg *internalconfig.Config
		if h != nil && h.AuthManager != nil {
			cfg = h.AuthManager.RuntimeConfig()
		}
		providers = openAICompatProvidersForUpstreamName(cfg, baseModel)
	}
	return providers
}

func openAICompatProvidersForUpstreamName(cfg *internalconfig.Config, upstreamName string) []string {
	upstreamName = strings.TrimSpace(upstreamName)
	if upstreamName == "" || cfg == nil {
		return nil
	}
	out := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if compat.Disabled {
			continue
		}
		for _, model := range compat.Models {
			if !strings.EqualFold(strings.TrimSpace(model.Name), upstreamName) {
				continue
			}
			key := util.OpenAICompatibleProviderKey(compat.Name)
			if key == "" {
				continue
			}
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, key)
		}
	}
	return out
}
