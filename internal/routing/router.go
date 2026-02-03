package routing

import (
	"context"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// Router resolves models to provider candidates.
type Router struct {
	registry      *Registry
	modelMappings map[string]string      // normalized from -> to
	oauthAliases  map[string][]string    // normalized model -> []alias
}

// NewRouter creates a new router with the given configuration.
func NewRouter(registry *Registry, cfg *config.Config) *Router {
	r := &Router{
		registry:      registry,
		modelMappings: make(map[string]string),
		oauthAliases:  make(map[string][]string),
	}

	if cfg != nil {
		r.loadModelMappings(cfg.AmpCode.ModelMappings)
		r.loadOAuthAliases(cfg.OAuthModelAlias)
	}

	return r
}

// LegacyRoutingDecision contains the resolved routing information.
// Deprecated: Will be replaced by RoutingDecision from types.go in T-013.
type LegacyRoutingDecision struct {
	RequestedModel string              // Original model from request
	ResolvedModel  string              // After model-mappings
	Candidates     []ProviderCandidate // Ordered list of providers to try
}

// Resolve determines the routing decision for the requested model.
// Deprecated: Will be updated to use RoutingRequest and return *RoutingDecision in T-013.
func (r *Router) Resolve(requestedModel string) *LegacyRoutingDecision {
	// 1. Extract thinking suffix
	suffixResult := thinking.ParseSuffix(requestedModel)
	baseModel := suffixResult.ModelName

	// 2. Apply model-mappings
	targetModel := r.applyMappings(baseModel)

	// 3. Find primary providers
	candidates := r.findCandidates(targetModel, suffixResult)

	// 4. Add fallback aliases
	for _, alias := range r.oauthAliases[strings.ToLower(targetModel)] {
		candidates = append(candidates, r.findCandidates(alias, suffixResult)...)
	}

	// 5. Sort by priority
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Provider.Priority() < candidates[j].Provider.Priority()
	})

	return &LegacyRoutingDecision{
		RequestedModel: requestedModel,
		ResolvedModel:  targetModel,
		Candidates:     candidates,
	}
}

// ResolveV2 determines the routing decision for a routing request.
// It uses the new RoutingRequest and RoutingDecision types.
func (r *Router) ResolveV2(req RoutingRequest) *RoutingDecision {
	// 1. Extract thinking suffix
	suffixResult := thinking.ParseSuffix(req.RequestedModel)
	baseModel := suffixResult.ModelName
	thinkingSuffix := ""
	if suffixResult.HasSuffix {
		thinkingSuffix = "(" + suffixResult.RawSuffix + ")"
	}

	// 2. Check for local providers
	localCandidates := r.findLocalCandidates(baseModel, suffixResult)

	// 3. Apply model-mappings if needed
	mappedModel := r.applyMappings(baseModel)
	mappingCandidates := r.findLocalCandidates(mappedModel, suffixResult)

	// 4. Determine route type based on preferences and availability
	var decision *RoutingDecision

	if req.ForceModelMapping && mappedModel != baseModel && len(mappingCandidates) > 0 {
		// FORCE MODE: Use mapping even if local provider exists
		decision = r.buildMappingDecision(req.RequestedModel, mappedModel, mappingCandidates, thinkingSuffix, mappingCandidates[1:])
	} else if req.PreferLocalProvider && len(localCandidates) > 0 {
		// DEFAULT MODE with local preference: Use local provider first
		decision = r.buildLocalProviderDecision(req.RequestedModel, localCandidates, thinkingSuffix)
	} else if len(localCandidates) > 0 {
		// DEFAULT MODE: Local provider available
		decision = r.buildLocalProviderDecision(req.RequestedModel, localCandidates, thinkingSuffix)
	} else if mappedModel != baseModel && len(mappingCandidates) > 0 {
		// DEFAULT MODE: No local provider, but mapping available
		decision = r.buildMappingDecision(req.RequestedModel, mappedModel, mappingCandidates, thinkingSuffix, mappingCandidates[1:])
	} else {
		// No local provider, no mapping - use amp credits proxy
		decision = &RoutingDecision{
			RouteType:     RouteTypeAmpCredits,
			ResolvedModel: req.RequestedModel,
			ShouldProxy:   true,
		}
	}

	return decision
}

// findLocalCandidates finds local provider candidates for a model.
// If the internal registry is empty, it falls back to the global model registry.
func (r *Router) findLocalCandidates(model string, suffixResult thinking.SuffixResult) []ProviderCandidate {
	var candidates []ProviderCandidate

	// Check internal registry first
	registryProviders := r.registry.All()
	if len(registryProviders) > 0 {
		for _, p := range registryProviders {
			if !p.SupportsModel(model) {
				continue
			}

			// Apply thinking suffix if needed
			actualModel := model
			if suffixResult.HasSuffix && !thinking.ParseSuffix(model).HasSuffix {
				actualModel = model + "(" + suffixResult.RawSuffix + ")"
			}

			if p.Available(actualModel) {
				candidates = append(candidates, ProviderCandidate{
					Provider: p,
					Model:    actualModel,
				})
			}
		}
	} else {
		// Fallback to global model registry (same logic as FallbackHandler)
		// This ensures compatibility when the wrapper is initialized with an empty registry
		providers := registry.GetGlobalRegistry().GetModelProviders(model)
		if len(providers) > 0 {
			actualModel := model
			if suffixResult.HasSuffix && !thinking.ParseSuffix(model).HasSuffix {
				actualModel = model + "(" + suffixResult.RawSuffix + ")"
			}
			// Create a synthetic provider candidate for each provider
			for _, providerName := range providers {
				candidates = append(candidates, ProviderCandidate{
					Provider: &globalRegistryProvider{name: providerName, model: actualModel},
					Model:    actualModel,
				})
			}
		}
	}

	// Sort by priority
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Provider.Priority() < candidates[j].Provider.Priority()
	})

	return candidates
}

// globalRegistryProvider is a synthetic Provider implementation that wraps
// a provider name from the global model registry. It is used only for routing
// decisions when the internal registry is empty - actual execution goes through
// the normal handler path, not through this provider's Execute methods.
type globalRegistryProvider struct {
	name  string
	model string
}

func (p *globalRegistryProvider) Name() string                   { return p.name }
func (p *globalRegistryProvider) Type() ProviderType             { return ProviderTypeOAuth }
func (p *globalRegistryProvider) Priority() int                  { return 0 }
func (p *globalRegistryProvider) SupportsModel(string) bool      { return true }
func (p *globalRegistryProvider) Available(string) bool          { return true }

// Execute is not used for globalRegistryProvider - routing wrapper calls the handler directly.
func (p *globalRegistryProvider) Execute(ctx context.Context, model string, req executor.Request) (executor.Response, error) {
	return executor.Response{}, nil
}

// ExecuteStream is not used for globalRegistryProvider - routing wrapper calls the handler directly.
func (p *globalRegistryProvider) ExecuteStream(ctx context.Context, model string, req executor.Request) (<-chan executor.StreamChunk, error) {
	return nil, nil
}

// buildLocalProviderDecision creates a decision for local provider routing.
func (r *Router) buildLocalProviderDecision(requestedModel string, candidates []ProviderCandidate, thinkingSuffix string) *RoutingDecision {
	resolvedModel := requestedModel
	if thinkingSuffix != "" {
		// Ensure thinking suffix is preserved
		sr := thinking.ParseSuffix(requestedModel)
		if !sr.HasSuffix {
			resolvedModel = requestedModel + thinkingSuffix
		}
	}

	var fallbackModels []string
	if len(candidates) > 1 {
		for _, c := range candidates[1:] {
			fallbackModels = append(fallbackModels, c.Model)
		}
	}

	return &RoutingDecision{
		RouteType:      RouteTypeLocalProvider,
		ResolvedModel:  resolvedModel,
		ProviderName:   candidates[0].Provider.Name(),
		FallbackModels: fallbackModels,
		ShouldProxy:    false,
	}
}

// buildMappingDecision creates a decision for model mapping routing.
func (r *Router) buildMappingDecision(requestedModel, mappedModel string, candidates []ProviderCandidate, thinkingSuffix string, fallbackCandidates []ProviderCandidate) *RoutingDecision {
	// Apply thinking suffix to resolved model if needed
	resolvedModel := mappedModel
	if thinkingSuffix != "" {
		sr := thinking.ParseSuffix(mappedModel)
		if !sr.HasSuffix {
			resolvedModel = mappedModel + thinkingSuffix
		}
	}

	var fallbackModels []string
	for _, c := range fallbackCandidates {
		fallbackModels = append(fallbackModels, c.Model)
	}

	// Also add oauth aliases as fallbacks
	baseMapped := thinking.ParseSuffix(mappedModel).ModelName
	for _, alias := range r.oauthAliases[strings.ToLower(baseMapped)] {
		// Check if this alias has providers
		aliasCandidates := r.findLocalCandidates(alias, thinking.SuffixResult{ModelName: alias})
		for _, c := range aliasCandidates {
			fallbackModels = append(fallbackModels, c.Model)
		}
	}

	return &RoutingDecision{
		RouteType:      RouteTypeModelMapping,
		ResolvedModel:  resolvedModel,
		ProviderName:   candidates[0].Provider.Name(),
		FallbackModels: fallbackModels,
		ShouldProxy:    false,
	}
}

// applyMappings applies model-mappings configuration.
func (r *Router) applyMappings(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if mapped, ok := r.modelMappings[key]; ok {
		return mapped
	}
	return model
}

// findCandidates finds all provider candidates for a model.
func (r *Router) findCandidates(model string, suffixResult thinking.SuffixResult) []ProviderCandidate {
	var candidates []ProviderCandidate

	for _, p := range r.registry.All() {
		if !p.SupportsModel(model) {
			continue
		}

		// Apply thinking suffix if needed
		actualModel := model
		if suffixResult.HasSuffix && !thinking.ParseSuffix(model).HasSuffix {
			actualModel = model + "(" + suffixResult.RawSuffix + ")"
		}

		if p.Available(actualModel) {
			candidates = append(candidates, ProviderCandidate{
				Provider: p,
				Model:    actualModel,
			})
		}
	}

	return candidates
}

// loadModelMappings loads model-mappings from config.
func (r *Router) loadModelMappings(mappings []config.AmpModelMapping) {
	for _, m := range mappings {
		from := strings.ToLower(strings.TrimSpace(m.From))
		to := strings.TrimSpace(m.To)
		if from != "" && to != "" {
			r.modelMappings[from] = to
		}
	}
}

// loadOAuthAliases loads oauth-model-alias from config.
func (r *Router) loadOAuthAliases(aliases map[string][]config.OAuthModelAlias) {
	for _, entries := range aliases {
		for _, entry := range entries {
			name := strings.ToLower(strings.TrimSpace(entry.Name))
			alias := strings.TrimSpace(entry.Alias)
			if name != "" && alias != "" && name != alias {
				r.oauthAliases[name] = append(r.oauthAliases[name], alias)
			}
		}
	}
}
