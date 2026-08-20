package handlers

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
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
// bridge model id (empty = feature disabled for this router).
type autoRouterResolved struct {
	targetModel       string
	route             *autorouter.Resolved
	visionBridgeModel string
	tier              string
	routerID          string
	matched           bool
}

// resolveAutoRouterModel scores an incoming request and returns the upstream
// model id to send it to, when the requested model matches an Auto Router.
//
// matched is false when the requested model is not an Auto Router id (so the
// caller uses normal routing), when the router is disabled, or when no tier
// mapping resolves to a usable model.
//
// targetModel is the concrete upstream model id chosen by the router's tier
// mapping. It may carry a thinking suffix (e.g. "claude-opus-4-5(high)"), which
// the downstream thinking application already understands. route carries the
// tier's per-model routing (providers/strategy/priorities) so the caller can
// apply it at request time; its Providers may be empty (target model defaults
// then apply). Runtime cost attribution follows the resolved target model's
// pricing.
func (h *BaseAPIHandler) resolveAutoRouterModel(ctx context.Context, entryProtocol, modelName string, rawJSON []byte) autoRouterResolved {
	parsed := thinking.ParseSuffix(modelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	router := h.autoRouterFromModel(ctx, baseModel)
	if router == nil {
		return autoRouterResolved{}
	}

	cfg := storeRouterToConfig(router)
	score := autorouter.Score(rawJSON, entryProtocol)
	resolved, ok := autorouter.Resolve(score.Tier, cfg)
	if !ok || resolved == nil || strings.TrimSpace(resolved.Model) == "" {
		return autoRouterResolved{}
	}
	return autoRouterResolved{
		targetModel:       resolved.Model,
		route:             resolved,
		visionBridgeModel: strings.TrimSpace(router.VisionBridgeModel),
		tier:              string(score.Tier),
		routerID:          strings.TrimSpace(router.ModelID),
		matched:           true,
	}
}

// applyAutoRouterRoute applies a resolved tier's per-model routing (providers +
// strategy + priorities) to the provider list already computed for the target
// model, mirroring how a Models Group / Global Model route narrows and orders
// providers. It runs after providersForExecution so the tier route is the
// authoritative override for auto-routed requests.
//
// Behaviour matches the global/per-key route path (handlers_routing.go):
//   - intersect the computed providers with the tier's pinned Providers;
//   - when the intersection is empty, return an empty slice so the upstream
//     dispatch fails fast with a clear "no pinned provider available" error
//     instead of silently routing to a provider that was never pinned;
//   - when the tier strategy is "priority"/"failover", reorder by descending
//     priority and stash the strategy so the conductor honours it.
func (h *BaseAPIHandler) applyAutoRouterRoute(ctx context.Context, providers []string, route *autorouter.Resolved) []string {
	if route == nil || len(route.Providers) == 0 {
		return providers
	}
	filtered := intersectProviders(providers, route.Providers)
	if len(filtered) == 0 {
		// Pin is enforced strictly: returning the full computed list here would
		// silently forward the request to a provider that was never pinned in
		// the tier mapping, which is exactly what the operator's pin was meant
		// to prevent. Surface the failure by handing back an empty slice so
		// providersForExecution's caller treats it as "no provider available".
		return nil
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
	return filtered
}
