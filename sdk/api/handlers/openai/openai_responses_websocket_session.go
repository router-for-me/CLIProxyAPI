package openai

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func websocketUpstreamSupportsIncrementalInput(attributes map[string]string, metadata map[string]any) bool {
	if len(attributes) > 0 {
		if raw := strings.TrimSpace(attributes["websockets"]); raw != "" {
			parsed, errParse := strconv.ParseBool(raw)
			if errParse == nil {
				return parsed
			}
		}
	}
	if len(metadata) == 0 {
		return false
	}
	raw, ok := metadata["websockets"]
	if !ok || raw == nil {
		return false
	}
	switch value := raw.(type) {
	case bool:
		return value
	case string:
		parsed, errParse := strconv.ParseBool(strings.TrimSpace(value))
		if errParse == nil {
			return parsed
		}
	default:
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsIncrementalInputForModel(modelName string) bool {
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(modelName)
	for _, auth := range auths {
		if responsesWebsocketAuthSupportsIncrementalInput(auth) {
			return true
		}
	}
	return false
}

func (h *OpenAIResponsesAPIHandler) websocketUpstreamSupportsCompactionReplayForModel(modelName string) bool {
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(modelName)
	if len(auths) == 0 {
		return false
	}
	for _, auth := range auths {
		if !responsesWebsocketAuthSupportsCompactionReplay(auth) {
			return false
		}
	}
	return true
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketAvailableAuthsForModel(modelName string) ([]*coreauth.Auth, string) {
	if h == nil || h.AuthManager == nil {
		return nil, ""
	}
	resolvedModelName := responsesWebsocketResolvedModelName(modelName)
	providerSet, modelKey := responsesWebsocketProviderSetForModel(resolvedModelName)
	if len(providerSet) == 0 {
		return nil, modelKey
	}

	registryRef := registry.GetGlobalRegistry()
	now := time.Now()
	auths := h.AuthManager.List()
	available := make([]*coreauth.Auth, 0, len(auths))
	for _, auth := range auths {
		if !responsesWebsocketAuthMatchesModel(auth, providerSet, modelKey, registryRef, now) {
			continue
		}
		available = append(available, auth)
	}
	return available, modelKey
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketUsesCodexWebsocketPassthrough(modelName string) bool {
	return h.responsesWebsocketUsesUpstreamWebsocketPassthrough(modelName)
}

func (h *OpenAIResponsesAPIHandler) responsesWebsocketUsesUpstreamWebsocketPassthrough(modelName string) bool {
	modelName = strings.TrimSpace(modelName)
	if h == nil || h.AuthManager == nil || modelName == "" {
		return false
	}
	auths, _ := h.responsesWebsocketAvailableAuthsForModel(modelName)
	if len(auths) == 0 {
		return false
	}
	provider := ""
	for _, auth := range auths {
		if auth == nil {
			return false
		}
		authProvider := strings.ToLower(strings.TrimSpace(auth.Provider))
		if authProvider != "codex" && authProvider != "xai" {
			return false
		}
		if provider == "" {
			provider = authProvider
			if _, ok := h.AuthManager.Executor(provider); !ok {
				return false
			}
		} else if authProvider != provider {
			return false
		}
		if !websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata) {
			return false
		}
	}
	return provider != ""
}

func responsesWebsocketAuthSupportsIncrementalInput(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return websocketUpstreamSupportsIncrementalInput(auth.Attributes, auth.Metadata)
}

func responsesWebsocketPinnedAuthMatchesModel(auth *coreauth.Auth, modelName string, pinnedModelKey string, homeRuntime bool) bool {
	if auth == nil {
		return false
	}
	providerSet, modelKey := responsesWebsocketProviderSetForModel(responsesWebsocketResolvedModelName(modelName))
	providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
	if _, ok := providerSet[providerKey]; !ok {
		return false
	}
	if !responsesWebsocketAuthAvailableForModel(auth, modelKey, time.Now()) {
		return false
	}

	if homeRuntime {
		return strings.EqualFold(strings.TrimSpace(pinnedModelKey), strings.TrimSpace(modelKey))
	}
	return registry.GetGlobalRegistry().ClientSupportsModel(auth.ID, modelKey)
}

func responsesWebsocketResolvedModelName(modelName string) string {
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
		if initialSuffix.HasSuffix {
			return fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
		}
		return resolvedBase
	}
	return util.ResolveAutoModel(modelName)
}

func responsesWebsocketProviderSetForModel(resolvedModelName string) (map[string]struct{}, string) {
	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)
	providers := util.GetProviderName(baseModel)
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}
	providerSet := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		providerKey := strings.TrimSpace(strings.ToLower(provider))
		if providerKey == "" {
			continue
		}
		providerSet[providerKey] = struct{}{}
	}
	modelKey := baseModel
	if modelKey == "" {
		modelKey = strings.TrimSpace(resolvedModelName)
	}
	return providerSet, modelKey
}

func responsesWebsocketAuthMatchesModel(auth *coreauth.Auth, providerSet map[string]struct{}, modelKey string, registryRef *registry.ModelRegistry, now time.Time) bool {
	if auth == nil {
		return false
	}
	providerKey := strings.TrimSpace(strings.ToLower(auth.Provider))
	if _, ok := providerSet[providerKey]; !ok {
		return false
	}
	if modelKey != "" && registryRef != nil && !registryRef.ClientSupportsModel(auth.ID, modelKey) {
		return false
	}
	return responsesWebsocketAuthAvailableForModel(auth, modelKey, now)
}

func responsesWebsocketAuthSupportsCompactionReplay(auth *coreauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "codex")
}

func responsesWebsocketAuthAvailableForModel(auth *coreauth.Auth, modelName string, now time.Time) bool {
	if auth == nil {
		return false
	}
	if auth.Disabled || auth.Status == coreauth.StatusDisabled {
		return false
	}
	if modelName != "" && len(auth.ModelStates) > 0 {
		state, ok := auth.ModelStates[modelName]
		if (!ok || state == nil) && modelName != "" {
			baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
			if baseModel != "" && baseModel != modelName {
				state, ok = auth.ModelStates[baseModel]
			}
		}
		if ok && state != nil {
			if state.Status == coreauth.StatusDisabled {
				return false
			}
			if state.Unavailable && !state.NextRetryAfter.IsZero() && state.NextRetryAfter.After(now) {
				return false
			}
			return true
		}
	}
	if auth.Unavailable && !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(now) {
		return false
	}
	return true
}
