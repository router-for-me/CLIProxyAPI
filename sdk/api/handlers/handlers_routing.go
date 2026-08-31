package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/tidwall/sjson"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"golang.org/x/net/context"
)

// PluginModelRouterHost routes matching requests to a plugin executor, the router's own executor,
// or a built-in provider before model-to-provider resolution and auth selection.
type PluginModelRouterHost interface {
	RouteModel(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool)
}

type pluginModelRouterSkipHost interface {
	RouteModelExcept(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool)
}

type modelRouterDetector interface {
	HasModelRouters() bool
}

type modelRouterSkipDetector interface {
	HasModelRoutersExcept(string) bool
}

func preferExecutionProvider(providers []string, preferred string) []string {
	preferred = strings.ToLower(strings.TrimSpace(preferred))
	if preferred == "" || len(providers) < 2 {
		return providers
	}
	preferredIndex := -1
	for i := range providers {
		if strings.ToLower(strings.TrimSpace(providers[i])) == preferred {
			preferredIndex = i
			break
		}
	}
	// Two distinct no-op cases, kept separate so a future edit cannot flip one
	// into the other: -1 means the preferred provider is not in the list at
	// all (nothing to reorder), 0 means it is already first (no rotation needed).
	if preferredIndex == -1 {
		return providers
	}
	if preferredIndex == 0 {
		return providers
	}
	out := make([]string, 0, len(providers))
	out = append(out, providers[preferredIndex])
	out = append(out, providers[:preferredIndex]...)
	out = append(out, providers[preferredIndex+1:]...)
	return out
}

func adjustExecutionProvidersForEntryProtocol(entryProtocol string, providers []string) []string {
	if entryProtocol == Interactions {
		return preferExecutionProvider(providers, GeminiInteractions)
	}
	if supportsNativeInteractionsEntryProtocol(entryProtocol) {
		return providers
	}
	return excludeExecutionProvider(providers, GeminiInteractions)
}

func supportsNativeInteractionsEntryProtocol(entryProtocol string) bool {
	switch entryProtocol {
	case Interactions, OpenAI, OpenaiResponse, Claude, Gemini:
		return true
	default:
		return false
	}
}

func excludeExecutionProvider(providers []string, excluded string) []string {
	excluded = strings.ToLower(strings.TrimSpace(excluded))
	if excluded == "" || len(providers) == 0 {
		return providers
	}
	excludedIndex := -1
	for i := range providers {
		if strings.ToLower(strings.TrimSpace(providers[i])) == excluded {
			excludedIndex = i
			break
		}
	}
	if excludedIndex == -1 {
		return providers
	}
	out := make([]string, 0, len(providers)-1)
	out = append(out, providers[:excludedIndex]...)
	out = append(out, providers[excludedIndex+1:]...)
	return out
}

// applyPinnedRoute intersects providers against a pinned route's provider set
// (empty intersection is a hard 503) and applies the route's strategy ordering
// when one is configured ("priority" reorders by descending priority, "failover"
// reorders and stashes the strategy so the conductor uses its cross-provider
// failover loop). Shared by the Global Model route and the per-API-key route
// so both pin with identical mechanics.
func applyPinnedRoute(ctx context.Context, providers []string, modelName string, route *store.ModelRoute) ([]string, *interfaces.ErrorMessage) {
	if route == nil {
		return providers, nil
	}
	filtered := intersectProviders(providers, route.Providers)
	if len(filtered) == 0 {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusServiceUnavailable,
			Error:      fmt.Errorf("no available upstream for model %s on allowed providers %v", modelName, route.Providers),
		}
	}
	strategy := strings.ToLower(strings.TrimSpace(route.Strategy))
	if strategy == "priority" || strategy == "failover" {
		filtered = orderProvidersByPriority(filtered, route.Priorities)
		stashRouteStrategy(ctx, strategy)
	}
	return filtered, nil
}

func validateNativeInteractionsExecution(entryProtocol string, execOptions modelExecutionOptions, routeDecision modelRouteDecision) *interfaces.ErrorMessage {
	forcedProvider := strings.ToLower(strings.TrimSpace(execOptions.ForcedProvider))
	if forcedProvider == "" || entryProtocol != Interactions {
		return nil
	}
	if routeDecision.ExecutorPluginID != "" {
		return nativeInteractionsExecutionError()
	}
	if routeProvider := strings.ToLower(strings.TrimSpace(routeDecision.Provider)); routeProvider != "" && routeProvider != forcedProvider {
		return nativeInteractionsExecutionError()
	}
	return nil
}

func nativeInteractionsExecutionError() *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      fmt.Errorf("agent is only supported for native interactions execution"),
	}
}

// providersForExecution resolves the providers and normalized model for a request. When a model
// router selected a built-in provider, it skips model->provider resolution and uses the router's
// provider (with an optional target model); otherwise it falls back to the registry-based path.
func (h *BaseAPIHandler) providersForExecution(ctx context.Context, modelName, originalRequestedModel string, allowImageModel bool, routeDecision modelRouteDecision, execOptions modelExecutionOptions) ([]string, string, *interfaces.ErrorMessage) {
	forcedProvider := strings.ToLower(strings.TrimSpace(execOptions.ForcedProvider))
	if forcedProvider != "" {
		if routeDecision.ExecutorPluginID != "" {
			return nil, "", nativeInteractionsExecutionError()
		}
		if routeProvider := strings.ToLower(strings.TrimSpace(routeDecision.Provider)); routeProvider != "" && routeProvider != forcedProvider {
			return nil, "", nativeInteractionsExecutionError()
		}
		normalizedModel := strings.TrimSpace(modelName)
		if normalizedModel == "" {
			normalizedModel = strings.TrimSpace(originalRequestedModel)
		}
		if errMsg := h.validateImageOnlyModel(normalizedModel, allowImageModel); errMsg != nil {
			return nil, "", errMsg
		}
		return []string{forcedProvider}, normalizedModel, nil
	}
	if routeDecision.Provider != "" {
		normalizedModel := originalRequestedModel
		if routeDecision.Model != "" {
			normalizedModel = routeDecision.Model
		}
		if errMsg := h.validateImageOnlyModel(normalizedModel, allowImageModel); errMsg != nil {
			return nil, "", errMsg
		}
		return []string{routeDecision.Provider}, normalizedModel, nil
	}
	return h.getRequestDetailsWithOptions(ctx, modelName, allowImageModel)
}

// policyRouteForModel reads the per-API-key model routes stashed by the policy
// middleware into the gin context (embedded in ctx) and returns the matched
// route entry (model + providers + optional strategy/priorities), or nil when
// no route is configured for modelID. The registry-derived provider list is
// intersected with route.Providers so requests are confined to the pinned set.
func policyRouteForModel(ctx context.Context, modelID string) *store.ModelRoute {
	if ctx == nil {
		return nil
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return nil
	}
	return middleware.RouteForModel(ginCtx, modelID)
}

// policyRoutesForModel returns only the Providers slice of the matched route,
// for callers that do not need the strategy/priorities. Kept for backward
// compatibility with the prior signature.
func policyRoutesForModel(ctx context.Context, modelID string) []string {
	if r := policyRouteForModel(ctx, modelID); r != nil {
		return r.Providers
	}
	return nil
}

// orderProvidersByPriority reorders providers in descending priority order.
// Providers not listed in priorities default to priority 0. The original
// (registry) order is preserved among providers sharing the same priority,
// keeping precedence stable.
func orderProvidersByPriority(providers []string, priorities []store.ProviderPriority) []string {
	if len(providers) <= 1 || len(priorities) == 0 {
		return providers
	}
	weight := make(map[string]int, len(priorities))
	for _, pr := range priorities {
		weight[strings.ToLower(strings.TrimSpace(pr.Provider))] = pr.Priority
	}
	indexed := make([]struct {
		key      string
		priority int
		pos      int
	}, len(providers))
	for i, p := range providers {
		indexed[i] = struct {
			key      string
			priority int
			pos      int
		}{key: strings.ToLower(strings.TrimSpace(p)), priority: weight[strings.ToLower(strings.TrimSpace(p))], pos: i}
	}
	sort.SliceStable(indexed, func(i, j int) bool {
		if indexed[i].priority != indexed[j].priority {
			return indexed[i].priority > indexed[j].priority
		}
		return indexed[i].pos < indexed[j].pos
	})
	out := make([]string, len(providers))
	for i, e := range indexed {
		out[i] = providers[e.pos]
	}
	return out
}

// stashRouteStrategy records the per-model routing strategy on the gin context
// embedded in ctx so the execution handler can transfer it into the conductor
// options metadata. A no-op when ctx has no gin context (e.g. programmatic
// calls) — the global routing.strategy then applies unchanged.
func stashRouteStrategy(ctx context.Context, strategy string) {
	if ctx == nil {
		return
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return
	}
	middleware.StashRouteStrategy(ginCtx, strategy)
}

// intersectProviders returns the subset of providers that appear (case-
// insensitively) in pinned, preserving the order/precedence of providers.
// Returns nil when the intersection is empty.
func intersectProviders(providers, pinned []string) []string {
	if len(pinned) == 0 {
		return providers
	}
	pinnedSet := make(map[string]struct{}, len(pinned))
	for _, p := range pinned {
		pinnedSet[strings.ToLower(strings.TrimSpace(p))] = struct{}{}
	}
	out := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, p := range providers {
		key := strings.ToLower(strings.TrimSpace(p))
		if key == "" {
			continue
		}
		if _, ok := pinnedSet[key]; !ok {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (h *BaseAPIHandler) getRequestDetailsWithOptions(ctx context.Context, modelName string, allowImageModel bool) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	resolvedModelName := modelName
	initialSuffix := thinking.ParseSuffix(modelName)
	if initialSuffix.ModelName == "auto" {
		if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
			resolvedModelName = modelName
		} else {
			resolvedBase := util.ResolveAutoModel(initialSuffix.ModelName)
			if initialSuffix.HasSuffix {
				resolvedModelName = fmt.Sprintf("%s(%s)", resolvedBase, initialSuffix.RawSuffix)
			} else {
				resolvedModelName = resolvedBase
			}
		}
	} else {
		if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
			resolvedModelName = modelName
		} else {
			resolvedModelName = util.ResolveAutoModel(modelName)
		}
	}

	parsed := thinking.ParseSuffix(resolvedModelName)
	baseModel := strings.TrimSpace(parsed.ModelName)

	if errMsg := h.validateImageOnlyModel(baseModel, allowImageModel); errMsg != nil {
		return nil, "", errMsg
	}

	if h != nil && h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		return []string{"home"}, resolvedModelName, nil
	}

	// registryProviders is the default candidate provider list derived from the
	// in-memory registry. Both the global model route and the per-API-key route
	// intersect against this baseline; the per-key route wins by recomputing from
	// it, so a per-key route overrides a global route for the same model.
	registryProviders := util.GetProviderName(baseModel)
	// Fallback: if baseModel has no provider but differs from resolvedModelName,
	// try using the full model name. This handles edge cases where custom models
	// may be registered with their full suffixed name (e.g., "my-model(8192)").
	// Evaluated in Story 11.8: This fallback is intentionally preserved to support
	// custom model registrations that include thinking suffixes.
	if len(registryProviders) == 0 && baseModel != resolvedModelName {
		registryProviders = util.GetProviderName(resolvedModelName)
	}

	if len(registryProviders) == 0 {
		// The client asked for a model this proxy cannot route. Report it as a request
		// error so streaming clients receive an actionable message instead of a
		// gateway failure they would keep retrying. 400 is used rather than 404 to keep
		// it distinguishable from an unregistered HTTP route.
		// The model name is client supplied, so it is inserted through sjson rather
		// than formatted into the JSON literal: an unescaped quote would otherwise
		// corrupt the body or let the caller overwrite the error code.
		body := `{"error":{"message":"","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
		body, errSet := sjson.Set(body, "error.message", "unknown provider for model "+modelName)
		if errSet != nil {
			body = `{"error":{"message":"unknown provider for model","type":"invalid_request_error","code":"model_not_found","param":"model"}}`
		}
		return nil, "", &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      errors.New(body),
		}
	}

	providers = registryProviders

	// Apply the "Global Model" routing override from the model catalog: when the
	// operator pinned this model id to a subset of upstream providers, confine the
	// candidate list to that subset. This applies to every request for the model,
	// independent of any per-API-key policy. It uses the same intersect/order/stash
	// mechanics as a Models Group route, so a Global Model routes identically to a
	// model pinned inside a group.
	if h != nil && h.GlobalModelRouter != nil {
		if route := h.GlobalModelRouter.GlobalModelRoute(ctx, baseModel); route != nil {
			filtered, errMsg := applyPinnedRoute(ctx, providers, modelName, route)
			if errMsg != nil {
				return nil, "", errMsg
			}
			providers = filtered
		}
	}

	// Apply per-API-key model routing: when the policy pins this model to a
	// subset of upstream providers, confine the candidate list to that subset,
	// recomputing from the registry baseline so a per-key route takes precedence
	// over any Global Model route for the same model. There is no failover to
	// registry providers outside the pinned set; an empty intersection (no pinned
	// provider serves the model) is a hard 503. When the route carries a strategy
	// ("priority" or "failover"), reorder the providers by descending priority and
	// stash the strategy so the conductor pins credential selection to the primary
	// provider (priority) or relies on its cross-provider failover loop (failover).
	// An empty strategy inherits the global routing.strategy unchanged.
	if route := policyRouteForModel(ctx, baseModel); route != nil {
		filtered, errMsg := applyPinnedRoute(ctx, registryProviders, modelName, route)
		if errMsg != nil {
			return nil, "", errMsg
		}
		providers = filtered
	}

	// The thinking suffix is preserved in the model name itself, so no
	// metadata-based configuration passing is needed.
	return providers, resolvedModelName, nil
}

func (h *BaseAPIHandler) validateImageOnlyModel(modelName string, allowImageModel bool) *interfaces.ErrorMessage {
	baseModel := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	if baseModel == "" {
		baseModel = strings.TrimSpace(modelName)
	}
	if isOpenAIImageOnlyModel(baseModel) && !allowImageModel {
		return &interfaces.ErrorMessage{
			StatusCode: http.StatusServiceUnavailable,
			Error:      fmt.Errorf("model %s is only supported on /v1/images/generations and /v1/images/edits", routeModelBaseName(baseModel)),
		}
	}
	return nil
}

func isOpenAIImageOnlyModel(model string) bool {
	switch strings.ToLower(strings.TrimSpace(routeModelBaseName(model))) {
	case "gpt-image-1.5", "gpt-image-2", "grok-imagine-image", "grok-imagine-image-quality":
		return true
	default:
		return false
	}
}

func routeModelBaseName(model string) string {
	model = strings.TrimSpace(model)
	if idx := strings.LastIndex(model, "/"); idx >= 0 && idx < len(model)-1 {
		return strings.TrimSpace(model[idx+1:])
	}
	return model
}

func cloneBytes(src []byte) []byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}

func (h *BaseAPIHandler) modelRouterHost() PluginModelRouterHost {
	if h == nil {
		return nil
	}
	if !isNilPluginModelRouterHost(h.ModelRouterHost) {
		return h.ModelRouterHost
	}
	host := h.interceptorHost()
	if host == nil {
		return nil
	}
	router, ok := host.(PluginModelRouterHost)
	if !ok {
		return nil
	}
	return router
}

type modelRouteDecision struct {
	ExecutorPluginID string
	Provider         string
	Model            string
}

func routeModel(ctx context.Context, host PluginModelRouterHost, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if host == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	if skipPluginID != "" {
		if skipper, ok := host.(pluginModelRouterSkipHost); ok {
			return skipper.RouteModelExcept(ctx, req, skipPluginID)
		}
		return pluginapi.ModelRouteResponse{}, false
	}
	return host.RouteModel(ctx, req)
}

func modelRoutersEnabled(host PluginModelRouterHost, skipPluginID string) bool {
	if host == nil {
		return false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	if skipPluginID != "" {
		if _, ok := host.(pluginModelRouterSkipHost); !ok {
			return false
		}
		if detector, ok := host.(modelRouterSkipDetector); ok {
			return detector.HasModelRoutersExcept(skipPluginID)
		}
	}
	if detector, ok := host.(modelRouterDetector); ok {
		return detector.HasModelRouters()
	}
	// No detector: treat routing as disabled (same conservative default as before any
	// ModelRouter existed). Hosts that route must implement HasModelRouters (pluginhost.Host does).
	return false
}

func (h *BaseAPIHandler) applyModelRouter(ctx context.Context, handlerType, modelName string, rawJSON []byte, stream bool, execOptions modelExecutionOptions) modelRouteDecision {
	var decision modelRouteDecision
	host := h.modelRouterHost()
	if host == nil || !modelRoutersEnabled(host, execOptions.SkipRouterPluginID) {
		return decision
	}
	meta := requestExecutionMetadata(ctx)
	meta[coreexecutor.RequestedModelMetadataKey] = modelName
	addModelExecutionSourceMetadata(meta, execOptions.InternalSource)
	resp, ok := routeModel(ctx, host, pluginapi.ModelRouteRequest{
		SourceFormat:   handlerType,
		RequestedModel: modelName,
		Stream:         stream,
		Headers:        modelExecutionHeaders(ctx, execOptions.Headers),
		Query:          modelExecutionQuery(ctx, execOptions.Query),
		Body:           cloneBytes(rawJSON),
		Metadata:       meta,
	}, execOptions.SkipRouterPluginID)
	if !ok || !resp.Handled {
		return decision
	}
	switch resp.TargetKind {
	case pluginapi.ModelRouteTargetSelf, pluginapi.ModelRouteTargetExecutor:
		decision.ExecutorPluginID = strings.TrimSpace(resp.Target)
	case pluginapi.ModelRouteTargetProvider:
		decision.Provider = strings.ToLower(strings.TrimSpace(resp.Target))
		decision.Model = strings.TrimSpace(resp.TargetModel)
	}
	return decision
}
