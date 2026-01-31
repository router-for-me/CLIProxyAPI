package routing

import (
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
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

// RoutingDecision contains the resolved routing information.
type RoutingDecision struct {
	RequestedModel string              // Original model from request
	ResolvedModel  string              // After model-mappings
	Candidates     []ProviderCandidate // Ordered list of providers to try
}

// Resolve determines the routing decision for the requested model.
func (r *Router) Resolve(requestedModel string) *RoutingDecision {
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

	return &RoutingDecision{
		RequestedModel: requestedModel,
		ResolvedModel:  targetModel,
		Candidates:     candidates,
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
