package auth

import (
	"maps"
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const resolvedAPIKeyModelInfoMetadataKey = "cliproxy.resolved_api_key_model_info"

type apiKeyModelCapabilityRoute struct {
	upstreamModel string
	modelInfo     *registry.ModelInfo
}

type apiKeyModelCapabilityTable map[string]map[string][]apiKeyModelCapabilityRoute

type apiKeyModelRoutingSnapshot struct {
	config       *internalconfig.Config
	aliases      apiKeyModelAliasTable
	capabilities apiKeyModelCapabilityTable
}

func isConfiguredModelRoutingAuth(auth *Auth) bool {
	if auth != nil && auth.AuthKind() == AuthKindAPIKey {
		return true
	}
	if auth == nil || auth.AuthSourceKind() != AuthSourceConfig || auth.Attributes == nil {
		return false
	}
	return strings.TrimSpace(auth.Attributes["compat_name"]) != ""
}

func (m *Manager) loadAPIKeyModelRouting() *apiKeyModelRoutingSnapshot {
	if m == nil {
		return &apiKeyModelRoutingSnapshot{config: &internalconfig.Config{}}
	}
	snapshot, _ := m.apiKeyModelRouting.Load().(*apiKeyModelRoutingSnapshot)
	if snapshot == nil {
		return &apiKeyModelRoutingSnapshot{config: &internalconfig.Config{}}
	}
	return snapshot
}

// ResolvedAPIKeyModelInfo returns the exact configured model definition bound to
// this API-key execution attempt.
func ResolvedAPIKeyModelInfo(req cliproxyexecutor.Request) (*registry.ModelInfo, bool) {
	modelInfo, ok := req.Metadata[resolvedAPIKeyModelInfoMetadataKey].(*registry.ModelInfo)
	if !ok || modelInfo == nil {
		return nil, false
	}
	return modelInfo, true
}

func (m *Manager) attachResolvedAPIKeyModelInfo(req cliproxyexecutor.Request, auth *Auth, routeModel, upstreamModel string) cliproxyexecutor.Request {
	return attachResolvedAPIKeyModelInfo(m.loadAPIKeyModelRouting(), req, auth, routeModel, upstreamModel)
}

func attachResolvedAPIKeyModelInfo(routing *apiKeyModelRoutingSnapshot, req cliproxyexecutor.Request, auth *Auth, routeModel, upstreamModel string) cliproxyexecutor.Request {
	modelInfo, ok := lookupAPIKeyModelCapability(routing, auth, routeModel, upstreamModel)
	if !ok {
		return req
	}
	metadata := make(map[string]any, len(req.Metadata)+1)
	maps.Copy(metadata, req.Metadata)
	metadata[resolvedAPIKeyModelInfoMetadataKey] = modelInfo
	req.Metadata = metadata
	return req
}

func lookupAPIKeyModelCapability(routing *apiKeyModelRoutingSnapshot, auth *Auth, routeModel, upstreamModel string) (*registry.ModelInfo, bool) {
	if !isConfiguredModelRoutingAuth(auth) || routing == nil {
		return nil, false
	}
	byRoute := routing.capabilities[strings.TrimSpace(auth.ID)]
	if len(byRoute) == 0 {
		return nil, false
	}
	requestedModel := rewriteModelForAuth(strings.TrimSpace(routeModel), auth)
	_, candidates := modelAliasLookupCandidates(requestedModel)
	routes := make([]apiKeyModelCapabilityRoute, 0)
	for _, candidate := range candidates {
		routes = append(routes, byRoute[strings.ToLower(strings.TrimSpace(candidate))]...)
	}
	selected := strings.TrimSpace(upstreamModel)
	for _, route := range routes {
		if strings.EqualFold(strings.TrimSpace(route.upstreamModel), selected) {
			return route.modelInfo, route.modelInfo != nil
		}
	}
	for _, route := range routes {
		if configuredUpstreamFallbackMatches(route.upstreamModel, selected) {
			return route.modelInfo, route.modelInfo != nil
		}
	}
	return nil, false
}

func configuredUpstreamFallbackMatches(configured, selected string) bool {
	configuredResult := thinking.ParseSuffix(strings.TrimSpace(configured))
	if configuredResult.HasSuffix {
		return false
	}
	selectedResult := thinking.ParseSuffix(strings.TrimSpace(selected))
	return strings.EqualFold(strings.TrimSpace(configuredResult.ModelName), strings.TrimSpace(selectedResult.ModelName))
}

func compileAPIKeyModelCapabilitiesForAuth(cfg *internalconfig.Config, auth *Auth) map[string][]apiKeyModelCapabilityRoute {
	if cfg == nil || !isConfiguredModelRoutingAuth(auth) {
		return nil
	}
	out := make(map[string][]apiKeyModelCapabilityRoute)
	switch strings.ToLower(strings.TrimSpace(auth.Provider)) {
	case "gemini":
		if entry := resolveGeminiAPIKeyConfig(cfg, auth); entry != nil {
			compileConfiguredModelCapabilities(out, entry.Models, "gemini")
		}
	case "gemini-interactions":
		if entry := resolveInteractionsAPIKeyConfig(cfg, auth); entry != nil {
			compileConfiguredModelCapabilities(out, entry.Models, "interactions")
		}
	case "claude":
		if entry := resolveClaudeAPIKeyConfig(cfg, auth); entry != nil {
			compileConfiguredModelCapabilities(out, entry.Models, "claude")
		}
	case "codex":
		if entry := resolveCodexAPIKeyConfig(cfg, auth); entry != nil {
			compileConfiguredModelCapabilities(out, entry.Models, "codex")
		}
	case "xai":
		if entry := resolveXAIAPIKeyConfig(cfg, auth); entry != nil {
			compileConfiguredModelCapabilities(out, entry.Models, "xai")
		}
	case "vertex":
		if entry := resolveVertexAPIKeyConfig(cfg, auth); entry != nil {
			compileConfiguredModelCapabilities(out, entry.Models, "gemini")
		}
	default:
		providerKey, compatName := "", ""
		if auth.Attributes != nil {
			providerKey = strings.TrimSpace(auth.Attributes["provider_key"])
			compatName = strings.TrimSpace(auth.Attributes["compat_name"])
		}
		if entry := resolveOpenAICompatConfigForAuth(cfg, auth, providerKey, compatName); entry != nil {
			compileOpenAICompatibleModelCapabilities(out, entry.Models)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func compileConfiguredModelCapabilities[T interface {
	GetName() string
	GetAlias() string
	GetThinking() *registry.ThinkingSupport
}](out map[string][]apiKeyModelCapabilityRoute, models []T, modelType string) {
	for i := range models {
		addConfiguredModelCapability(out, models[i].GetName(), models[i].GetAlias(), modelType, models[i].GetThinking())
	}
}

func compileOpenAICompatibleModelCapabilities(out map[string][]apiKeyModelCapabilityRoute, models []internalconfig.OpenAICompatibilityModel) {
	for i := range models {
		support := models[i].Thinking
		if support == nil && !models[i].Image {
			support = &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
		}
		addConfiguredModelCapability(out, models[i].Name, models[i].Alias, "openai-compatibility", support)
	}
}

func addConfiguredModelCapability(out map[string][]apiKeyModelCapabilityRoute, name, alias, modelType string, support *registry.ThinkingSupport) {
	name = strings.TrimSpace(name)
	alias = strings.TrimSpace(alias)
	if name == "" {
		name = alias
	}
	if alias == "" {
		alias = name
	}
	if name == "" {
		return
	}
	modelInfo := modelconfig.ResolveModelInfo(name, modelType, support)
	route := apiKeyModelCapabilityRoute{upstreamModel: name, modelInfo: modelInfo}
	seenKeys := make(map[string]struct{})
	for _, routeModel := range []string{alias, name} {
		_, candidates := modelAliasLookupCandidates(routeModel)
		for _, candidate := range candidates {
			key := strings.ToLower(strings.TrimSpace(candidate))
			if key == "" {
				continue
			}
			if _, exists := seenKeys[key]; exists {
				continue
			}
			seenKeys[key] = struct{}{}
			duplicate := false
			for _, existing := range out[key] {
				if strings.EqualFold(existing.upstreamModel, route.upstreamModel) {
					duplicate = true
					break
				}
			}
			if !duplicate {
				out[key] = append(out[key], route)
			}
		}
	}
}
