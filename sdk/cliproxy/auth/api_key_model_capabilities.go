package auth

import (
	"strings"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modelconfig"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
)

type apiKeyModelExecutionKind uint8

const (
	apiKeyModelExecutionAny apiKeyModelExecutionKind = iota
	apiKeyModelExecutionChat
	apiKeyModelExecutionImage
)

type apiKeyModelCapabilityRoute struct {
	upstreamModel string
	modelInfo     *registry.ModelInfo
	executionKind apiKeyModelExecutionKind
	configAlias   string
	forceMapping  bool
}

type apiKeyModelCapabilityTable map[string]map[string][]apiKeyModelCapabilityRoute

type apiKeyModelRoutingSnapshot struct {
	config       *internalconfig.Config
	aliases      apiKeyModelAliasTable
	capabilities apiKeyModelCapabilityTable
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

func compileAPIKeyModelCapabilities[T interface {
	GetName() string
	GetAlias() string
	GetThinking() *registry.ThinkingSupport
}](out map[string][]apiKeyModelCapabilityRoute, models []T, modelType string) {
	for i := range models {
		name := strings.TrimSpace(models[i].GetName())
		alias := strings.TrimSpace(models[i].GetAlias())
		if name == "" {
			name = alias
		}
		if name == "" || models[i].GetThinking() == nil {
			continue
		}
		if alias == "" {
			alias = name
		}
		addAPIKeyModelCapabilityRoute(out, alias, name, modelconfig.ResolveModelInfo(name, modelType, models[i].GetThinking()), apiKeyModelExecutionAny, false)
	}
}

func compileOpenAICompatModelCapabilities(out map[string][]apiKeyModelCapabilityRoute, models []internalconfig.OpenAICompatibilityModel) {
	for i := range models {
		model := models[i]
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" {
			name = alias
		}
		if name == "" {
			continue
		}
		if alias == "" {
			alias = name
		}

		executionKind := apiKeyModelExecutionChat
		if model.Image {
			executionKind = apiKeyModelExecutionImage
		}

		modelInfo := modelconfig.ResolveModelInfo(name, "openai", model.Thinking)
		if !model.Image && modelInfo.Thinking == nil {
			modelInfo.Thinking = &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
		}
		modelInfo.SupportedInputModalities = modelconfig.NormalizeModalities(model.InputModalities)
		modelInfo.SupportedOutputModalities = modelconfig.NormalizeModalities(model.OutputModalities)
		addAPIKeyModelCapabilityRoute(out, alias, name, modelInfo, executionKind, model.ForceMapping)
	}
}

func addAPIKeyModelCapabilityRoute(out map[string][]apiKeyModelCapabilityRoute, alias, name string, modelInfo *registry.ModelInfo, executionKind apiKeyModelExecutionKind, forceMapping bool) {
	if out == nil || strings.TrimSpace(name) == "" || modelInfo == nil {
		return
	}
	route := apiKeyModelCapabilityRoute{
		upstreamModel: strings.TrimSpace(name),
		modelInfo:     modelInfo,
		executionKind: executionKind,
		configAlias:   strings.TrimSpace(alias),
		forceMapping:  forceMapping,
	}
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
				if strings.EqualFold(existing.upstreamModel, route.upstreamModel) && existing.executionKind == route.executionKind {
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

func lookupAPIKeyModelCapability(snapshot *apiKeyModelRoutingSnapshot, auth *Auth, routeModel, upstreamModel string, executionKind apiKeyModelExecutionKind) (apiKeyModelCapabilityRoute, bool, bool) {
	if snapshot == nil || !IsConfigAPIKeyAuth(auth) {
		return apiKeyModelCapabilityRoute{}, false, false
	}
	byRoute := snapshot.capabilities[strings.TrimSpace(auth.ID)]
	if len(byRoute) == 0 {
		return apiKeyModelCapabilityRoute{}, false, false
	}

	requestedModel := rewriteModelForAuth(routeModel, auth)
	_, candidates := modelAliasLookupCandidates(requestedModel)
	configured := false
	for _, candidate := range candidates {
		routes := byRoute[strings.ToLower(strings.TrimSpace(candidate))]
		if route, matched, compatible := matchAPIKeyModelCapabilityRoute(routes, upstreamModel, executionKind); compatible {
			return route, true, true
		} else if matched {
			configured = true
		}
	}
	return apiKeyModelCapabilityRoute{}, configured, false
}

func matchAPIKeyModelCapabilityRoute(routes []apiKeyModelCapabilityRoute, upstreamModel string, executionKind apiKeyModelExecutionKind) (apiKeyModelCapabilityRoute, bool, bool) {
	upstreamModel = strings.TrimSpace(upstreamModel)
	route, exactMatched, exactCompatible := selectAPIKeyModelCapabilityRoute(routes, executionKind, func(route apiKeyModelCapabilityRoute) bool {
		return strings.EqualFold(route.upstreamModel, upstreamModel)
	})
	if exactCompatible {
		return route, true, true
	}
	upstreamBase := strings.TrimSpace(thinking.ParseSuffix(upstreamModel).ModelName)
	route, fallbackMatched, fallbackCompatible := selectAPIKeyModelCapabilityRoute(routes, executionKind, func(route apiKeyModelCapabilityRoute) bool {
		routeResult := thinking.ParseSuffix(route.upstreamModel)
		return !routeResult.HasSuffix && strings.EqualFold(strings.TrimSpace(routeResult.ModelName), upstreamBase)
	})
	return route, exactMatched || fallbackMatched, fallbackCompatible
}

func selectAPIKeyModelCapabilityRoute(routes []apiKeyModelCapabilityRoute, executionKind apiKeyModelExecutionKind, matches func(apiKeyModelCapabilityRoute) bool) (apiKeyModelCapabilityRoute, bool, bool) {
	matched := false
	for _, route := range routes {
		if !matches(route) {
			continue
		}
		matched = true
		if executionKind == apiKeyModelExecutionAny || route.executionKind == apiKeyModelExecutionAny || route.executionKind == executionKind {
			return route, true, true
		}
	}
	return apiKeyModelCapabilityRoute{}, matched, false
}

func apiKeyModelExecutionKindForImage(imageExecution bool) apiKeyModelExecutionKind {
	if imageExecution {
		return apiKeyModelExecutionImage
	}
	return apiKeyModelExecutionChat
}

func filterAPIKeyModelCandidates(snapshot *apiKeyModelRoutingSnapshot, auth *Auth, routeModel string, candidates []string, imageExecution bool) []string {
	if len(candidates) == 0 || !isOpenAICompatAPIKeyAuth(auth) {
		return candidates
	}
	out := make([]string, 0, len(candidates))
	executionKind := apiKeyModelExecutionKindForImage(imageExecution)
	for _, candidate := range candidates {
		_, configured, compatible := lookupAPIKeyModelCapability(snapshot, auth, routeModel, candidate, executionKind)
		if !configured || compatible {
			out = append(out, candidate)
		}
	}
	return out
}

func aliasResultForAPIKeyModelCandidate(snapshot *apiKeyModelRoutingSnapshot, auth *Auth, routeModel, upstreamModel string, imageExecution bool, fallback OAuthModelAliasResult) OAuthModelAliasResult {
	if !isOpenAICompatAPIKeyAuth(auth) {
		return fallback
	}
	route, configured, compatible := lookupAPIKeyModelCapability(snapshot, auth, routeModel, upstreamModel, apiKeyModelExecutionKindForImage(imageExecution))
	if !configured || !compatible {
		return fallback
	}
	requestedModel := rewriteModelForAuth(routeModel, auth)
	_, requestedCandidates := modelAliasLookupCandidates(requestedModel)
	matchedAlias := false
	for _, candidate := range requestedCandidates {
		if strings.EqualFold(strings.TrimSpace(candidate), route.configAlias) {
			matchedAlias = true
			break
		}
	}
	if !matchedAlias {
		return fallback
	}
	result := fallback
	result.UpstreamModel = upstreamModel
	result.ForceMapping = route.forceMapping
	result.OriginalAlias = ""
	if route.forceMapping {
		result.OriginalAlias = oauthModelAliasForceMappingResponseModel(route.configAlias)
	}
	return result
}
