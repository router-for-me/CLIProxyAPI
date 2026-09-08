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
	log "github.com/sirupsen/logrus"
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
// used by the scorer and resolver. Kept as a thin wrapper over the store's
// BridgeAutoRouterConfig (which also feeds the per-router config cache).
func storeRouterToConfig(r *store.AutoRouter) *autorouter.Config {
	return store.BridgeAutoRouterConfig(r)
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
	// resolveFailed reports that the request targeted an enabled auto-router
	// whose tier could not be resolved to any mapping — surfaced to the client
	// as an explicit 503 instead of falling through to the synthetic
	// auto-router provider (auth_not_found or an unknown upstream model).
	resolveFailed bool
}

// resolveFailureError builds the explicit 503 for an auto-router match whose
// tier mapping could not resolve. tier may be empty for legacy zero values.
func (r autoRouterResolved) resolveFailureError() *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusServiceUnavailable,
		Error: fmt.Errorf("auto router %s has no resolvable tier mapping for tier %s; configure a mapping or fix the fallback chain",
			r.routerID, r.tier),
	}
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

// maxAutoRouterCacheableBody caps score-cache participation: the cache key is
// a SHA-256 over the full body, so beyond this size hashing costs more than
// the (head+tail windowed) scoring itself and the cache is skipped.
const maxAutoRouterCacheableBody = 256 * 1024

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

	// Prefer the store's per-router config cache (router + bridged config in
	// one lookup, no per-request bridging); fall back to the plain resolver
	// and on-demand bridging (also covers test resolvers stubbing only
	// AutoRouterForModel).
	var router *store.AutoRouter
	var routerCfg *autorouter.Config
	type configResolver interface {
		AutoRouterConfigForModel(context.Context, string) (*store.AutoRouter, *autorouter.Config, bool)
	}
	if cr, okCfg := h.AutoRouterResolver.(configResolver); okCfg {
		if r2, cfg, okCfg2 := cr.AutoRouterConfigForModel(ctx, baseModel); okCfg2 {
			router = r2
			routerCfg = cfg
		}
	}
	if router == nil {
		router = h.autoRouterFromModel(ctx, baseModel)
	}
	if router == nil || !router.Enabled {
		return autoRouterResolved{}
	}
	if routerCfg == nil {
		routerCfg = storeRouterToConfig(router)
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
		if len(rawJSON) <= maxAutoRouterCacheableBody {
			if cached, hit := autoRouterScoreCache.Get(cacheKey); hit {
				return h.autoRouterResolvedFromScore(router, routerCfg, cached, compiled.Hash, compiledVersion(compiled))
			}
		}
		result := autorouter.ScoreWithProfileCompiled(rawJSON, entryProtocol, compiled)
		if len(rawJSON) <= maxAutoRouterCacheableBody {
			autoRouterScoreCache.Put(cacheKey, result)
		}
		return h.autoRouterResolvedFromScore(router, routerCfg, result, result.ProfileHash, result.ProfileVersion)
	}
	var profile *autorouter.Profile
	if h.AutoRouterProfileResolver != nil {
		profile = h.AutoRouterProfileResolver.AutoRouterProfile(ctx, router.ID)
	}
	result := autorouter.ScoreWithProfile(rawJSON, entryProtocol, profile)
	return h.autoRouterResolvedFromScore(router, routerCfg, result, result.ProfileHash, result.ProfileVersion)
}

// autoRouterResolvedFromScore turns a score result into the concrete upstream
// resolution: resolves the tier against the router's config, builds the
// explainability snapshot, and returns the matched outcome. Shared by the
// compiled-profile path (with its score cache) and the legacy path.
func (h *BaseAPIHandler) autoRouterResolvedFromScore(router *store.AutoRouter, routerCfg *autorouter.Config, result autorouter.ScoreResult, profileHash string, profileVersion int64) autoRouterResolved {
	resolved, ok := autorouter.Resolve(result.EffectiveTier, routerCfg)
	if !ok || resolved == nil || strings.TrimSpace(resolved.Model) == "" {
		log.WithFields(log.Fields{
			"router_id": strings.TrimSpace(router.ID),
			"tier":      string(result.EffectiveTier),
		}).Warn("auto-router: no resolvable tier mapping; rejecting request")
		return autoRouterResolved{
			resolveFailed: true,
			tier:          string(result.EffectiveTier),
			routerID:      strings.TrimSpace(router.ID),
		}
	}
	decision := autorouter.DecisionSnapshot{
		ProfileVersion: profileVersion, ProfileHash: profileHash, ProfileSnapshot: result.ProfileConfig,
		ScoreTotal: result.Score.Total, ScoreFields: result.Score.Fields, ReasoningMarkers: result.Score.ReasoningMarkers,
		ScoredTier: result.Score.Tier, EffectiveTier: result.EffectiveTier, DecisionCause: result.DecisionCause,
		MatchedRules: result.MatchedRules, MappingTier: resolved.MappingTier, FallbackChain: resolved.FallbackChain, TargetModel: resolved.Model,
	}
	// routerID is the router's PK id, not the requestable model id: usage
	// attribution (usage_events.router_id) must key on the same identifier the
	// management endpoints (decisions/simulate/replay) address the router by.
	// ModelID previously landed here and made those endpoints miss every
	// persisted event.
	return autoRouterResolved{targetModel: resolved.Model, route: resolved, visionBridgeModel: strings.TrimSpace(router.VisionBridgeModel), tier: string(result.EffectiveTier), routerID: strings.TrimSpace(router.ID), decision: decision, matched: true}
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
