package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"golang.org/x/net/context"
)

// autoRouterFromModel returns the Auto Router definition owned by modelID, or
// nil when none matches. It consults the (optionally wired) resolver and
// honours the router's Enabled flag.
func (h *BaseAPIHandler) autoRouterFromModel(ctx context.Context, modelID string) *store.AutoRouter {
	if h == nil || h.AutoRouterResolver == nil || ctx == nil {
		return nil
	}
	r := h.AutoRouterResolver.AutoRouterForModel(ctx, modelID)
	if r == nil || !r.Enabled {
		return nil
	}
	return r
}

// storeRouterToConfig bridges a store.AutoRouter into the pure autorouter.Config
// used by the scorer and resolver.
func storeRouterToConfig(r *store.AutoRouter) *autorouter.Config {
	if r == nil {
		return nil
	}
	c := &autorouter.Config{
		ID:                r.ID,
		Name:              r.Name,
		ModelID:           r.ModelID,
		Description:       r.Description,
		DisplayName:       r.DisplayName,
		Enabled:           r.Enabled,
		VisionBridgeModel: strings.TrimSpace(r.VisionBridgeModel),
		Mappings:          make([]autorouter.TierMapping, 0, len(r.Mappings)),
	}
	for _, m := range r.Mappings {
		arm := autorouter.TierMapping{
			Tier:           autorouter.Tier(strings.ToLower(strings.TrimSpace(m.Tier))),
			Model:          strings.TrimSpace(m.Model),
			TargetStrategy: strings.TrimSpace(m.TargetStrategy),
			Strategy:       strings.TrimSpace(m.Strategy),
		}
		if len(m.Targets) > 0 {
			arm.Targets = make([]autorouter.TierTarget, 0, len(m.Targets))
			for _, t := range m.Targets {
				target := autorouter.TierTarget{
					Model:    strings.TrimSpace(t.Model),
					Weight:   t.Weight,
					Strategy: strings.TrimSpace(t.Strategy),
				}
				if len(t.Providers) > 0 {
					target.Providers = append([]string{}, t.Providers...)
				}
				if len(t.Priorities) > 0 {
					target.Priorities = make([]autorouter.ProviderPriority, 0, len(t.Priorities))
					for _, p := range t.Priorities {
						target.Priorities = append(target.Priorities, autorouter.ProviderPriority{
							Provider: strings.TrimSpace(p.Provider),
							Priority: p.Priority,
						})
					}
				}
				arm.Targets = append(arm.Targets, target)
			}
		}
		if len(m.Providers) > 0 {
			arm.Providers = append([]string{}, m.Providers...)
		}
		if len(m.Priorities) > 0 {
			arm.Priorities = make([]autorouter.ProviderPriority, 0, len(m.Priorities))
			for _, p := range m.Priorities {
				arm.Priorities = append(arm.Priorities, autorouter.ProviderPriority{
					Provider: strings.TrimSpace(p.Provider),
					Priority: p.Priority,
				})
			}
		}
		c.Mappings = append(c.Mappings, arm)
	}
	return c
}

// autoRouterResolved bundles the outcome of resolving an auto router request:
// the concrete target model plus its per-model routing (providers/strategy/
// priorities). matched reports whether the requested model was an auto router
// that resolved to a usable target. visionBridgeModel is the per-router vision
// bridge model id (empty = feature disabled for this router). decision is the
// explainability snapshot persisted with the usage event.
type autoRouterResolved struct {
	targetModel       string
	route             *autorouter.Resolved
	visionBridgeModel string
	tier              string
	routerID          string
	decision          autorouter.DecisionSnapshot
	matched           bool
}

func (r autoRouterResolved) withDecisionContext(ctx context.Context) context.Context {
	if !r.matched {
		return ctx
	}
	raw, _ := json.Marshal(r.decision)
	ctx = coreusage.WithRouterTier(ctx, r.tier, r.routerID)
	return coreusage.WithAutoRouterDecision(ctx, string(r.decision.ScoredTier), string(r.decision.MappingTier), r.decision.DecisionCause, r.decision.ProfileVersion, r.decision.ProfileHash, raw)
}

// autoRouterScoreCache is the process-local score cache shared by every
// handler instance. Bodies never enter the cache (SHA-256 keys only); a
// profile upsert changes the profile hash and so invalidates naturally.
var autoRouterScoreCache = autorouter.NewScoreCache(0)

// compiledVersion extracts the profile version from a compiled profile
// (0 when nil).
func compiledVersion(compiled *autorouter.CompiledProfile) int64 {
	if compiled == nil {
		return 0
	}
	return compiled.Version
}

// resolveAutoRouterModel scores an incoming request and returns the upstream
// model id to send it to, when the requested model matches an Auto Router.
func (h *BaseAPIHandler) resolveAutoRouterModel(ctx context.Context, entryProtocol, modelName string, rawJSON []byte) autoRouterResolved {
	parsed := thinking.ParseSuffix(modelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	router := h.autoRouterFromModel(ctx, baseModel)
	if router == nil {
		return autoRouterResolved{}
	}
	// Prefer the compiled profile (normalization done once per version); fall
	// back to the legacy per-request profile path when only that resolver is
	// wired.
	if compiledResolver, ok := h.AutoRouterProfileResolver.(interface {
		AutoRouterProfileCompiled(context.Context, string) *autorouter.CompiledProfile
	}); ok {
		compiled := compiledResolver.AutoRouterProfileCompiled(ctx, router.ID)
		hash := ""
		if compiled != nil {
			hash = compiled.Hash
		}
		cacheKey := autoRouterScoreCache.Key(rawJSON, entryProtocol, router.ID, hash)
		if cached, hit := autoRouterScoreCache.Get(cacheKey); hit {
			return h.autoRouterResolvedFromScore(router, cached, compiled.Hash, compiledVersion(compiled))
		}
		result := autorouter.ScoreWithProfileCompiled(rawJSON, entryProtocol, compiled)
		autoRouterScoreCache.Put(cacheKey, result)
		return h.autoRouterResolvedFromScore(router, result, result.ProfileHash, result.ProfileVersion)
	}
	var profile *autorouter.Profile
	if h.AutoRouterProfileResolver != nil {
		profile = h.AutoRouterProfileResolver.AutoRouterProfile(ctx, router.ID)
	}
	result := autorouter.ScoreWithProfile(rawJSON, entryProtocol, profile)
	return h.autoRouterResolvedFromScore(router, result, result.ProfileHash, result.ProfileVersion)
}

// autoRouterResolvedFromScore turns a score result into the concrete upstream
// resolution: resolves the tier against the router's config, builds the
// explainability snapshot, and returns the matched outcome. Shared by the
// compiled-profile path (with its score cache) and the legacy path.
func (h *BaseAPIHandler) autoRouterResolvedFromScore(router *store.AutoRouter, result autorouter.ScoreResult, profileHash string, profileVersion int64) autoRouterResolved {
	resolved, ok := autorouter.Resolve(result.EffectiveTier, storeRouterToConfig(router))
	if !ok || resolved == nil || strings.TrimSpace(resolved.Model) == "" {
		return autoRouterResolved{}
	}
	decision := autorouter.DecisionSnapshot{
		ProfileVersion: profileVersion, ProfileHash: profileHash, ProfileSnapshot: result.ProfileConfig,
		ScoreTotal: result.Score.Total, ScoreFields: result.Score.Fields, ReasoningMarkers: result.Score.ReasoningMarkers,
		ScoredTier: result.Score.Tier, EffectiveTier: result.EffectiveTier, DecisionCause: result.DecisionCause,
		MatchedRules: result.MatchedRules, MappingTier: resolved.MappingTier, FallbackChain: resolved.FallbackChain, TargetModel: resolved.Model,
	}
	return autoRouterResolved{targetModel: resolved.Model, route: resolved, visionBridgeModel: strings.TrimSpace(router.VisionBridgeModel), tier: string(result.EffectiveTier), routerID: strings.TrimSpace(router.ModelID), decision: decision, matched: true}
}

// applyAutoRouterRoute applies a resolved tier's per-model routing (providers +
// strategy + priorities) to the provider list already computed for the target
// model, mirroring how a Models Group / Global Model route narrows and orders
// providers. It runs after providersForExecution so the tier route is the
// authoritative override for auto-routed requests.
//
// Behaviour matches the global/per-key route path (handlers_routing.go):
//   - intersect the computed providers with the tier's pinned Providers;
//   - when the intersection is empty, return an explicit error naming the
//     model and the pinned providers (same wording as the per-key route path)
//     so the client sees a clear 503 instead of the generic provider_not_found
//     that an empty provider list produces downstream;
//   - when the tier strategy is "priority"/"failover", reorder by descending
//     priority and stash the strategy so the conductor honours it.
func (h *BaseAPIHandler) applyAutoRouterRoute(ctx context.Context, providers []string, route *autorouter.Resolved) ([]string, *interfaces.ErrorMessage) {
	if route == nil || len(route.Providers) == 0 {
		return providers, nil
	}
	filtered := intersectProviders(providers, route.Providers)
	if len(filtered) == 0 {
		// Pin is enforced strictly: returning the full computed list here would
		// silently forward the request to a provider that was never pinned in
		// the tier mapping, which is exactly what the operator's pin was meant
		// to prevent. Surface the failure with the same diagnostic the per-key
		// route path produces so operators see one consistent error.
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusServiceUnavailable,
			Error:      fmt.Errorf("no available upstream for model %s on allowed providers %v", route.Model, route.Providers),
		}
	}
	strategy := strings.ToLower(strings.TrimSpace(route.Strategy))
	if strategy == "priority" || strategy == "failover" {
		priorities := make([]store.ProviderPriority, 0, len(route.Priorities))
		for _, p := range route.Priorities {
			priorities = append(priorities, store.ProviderPriority{Provider: p.Provider, Priority: p.Priority})
		}
		filtered = orderProvidersByPriority(filtered, priorities)
		stashRouteStrategy(ctx, strategy)
	}
	return filtered, nil
}
