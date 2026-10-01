package auth

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// ClientSearchSupported reports whether every selectable route behind one
// public model alias can complete the client search loop. The result drives
// supports_search_tool in the model catalog. Aggregation is conservative:
// one disabled, unknown, or uncovered route vetoes the whole alias, while
// temporarily cooled-down candidates still count because they may recover.
// Web search and client tool_search are different capabilities and never
// share a boolean.
func (m *Manager) ClientSearchSupported(modelID string) *bool {
	if m == nil {
		return nil
	}
	state := m.responsesToolsSnapshot()
	if state == nil {
		return nil
	}
	state.mu.Lock()
	policy := state.policy
	enabled := state.enabled
	state.mu.Unlock()
	if !enabled {
		return nil
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	routes := m.clientSearchCandidateRoutes(modelID)
	if len(routes) == 0 {
		return nil
	}
	for _, route := range routes {
		routePolicy, err := responsestools.EffectivePolicy(policy, route)
		if err != nil {
			no := false
			return &no
		}
		switch routePolicy.ClientSearch {
		case responsestools.ClientSearchBridge, responsestools.ClientSearchNative:
			// Supported through a verified bridge or native path.
		default:
			no := false
			return &no
		}
	}
	yes := true
	return &yes
}

// clientSearchCandidateRoutes enumerates real credential and model-pool
// candidates for one public model alias using the same resolution the
// executor calls use. Cooled-down credentials stay in the set: they may
// recover, and a temporarily unavailable search route must not promote the
// alias based on whichever credential happens to be usable now.
func (m *Manager) clientSearchCandidateRoutes(modelID string) []responsestools.Route {
	return m.responsesToolsCandidateRoutes(modelID, "")
}

func (m *Manager) responsesToolsCandidateRoutes(modelID, authID string) []responsestools.Route {
	if m == nil {
		return nil
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil
	}
	m.mu.RLock()
	auths := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled || (authID != "" && auth.ID != authID) {
			continue
		}
		auths = append(auths, auth.Clone())
	}
	m.mu.RUnlock()
	var routes []responsestools.Route
	for _, auth := range auths {
		// Unknown model aliases never promote: only credentials registered
		// for this exact public model contribute candidate routes.
		if !registry.GetGlobalRegistry().ClientSupportsModel(auth.ID, modelID) {
			continue
		}
		providerKey := executorKeyFromAuth(auth)
		if providerKey == "" {
			continue
		}
		executor, ok := m.Executor(providerKey)
		if !ok || executor == nil {
			continue
		}
		models, _, _, _ := m.preparedExecutionModelsWithAlias(auth, modelID)
		if len(models) == 0 {
			continue
		}
		for _, upstreamModel := range models {
			probeReq := cliproxyexecutor.Request{Model: upstreamModel}
			probeOpts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex}
			toFormat := requestToFormat(providerKey, executor, probeReq, probeOpts)
			route := responsesToolsRoute(auth, providerKey, upstreamModel, toFormat)
			route.AuthKind = normalizeAuthKindForPolicy(route.AuthKind)
			routes = append(routes, route)
		}
	}
	return routes
}

// ResponsesToolsEnabled reports whether the core tool protocol feature has
// any active route. Handlers use it to decide per-turn WebSocket modes
// without parsing payloads when the feature is off.
func (m *Manager) ResponsesToolsEnabled() bool {
	if m == nil {
		return false
	}
	state := m.responsesToolsSnapshot()
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.enabled
}

// ResponsesToolsMayApplyToRoute reports whether a non-pass-through policy
// could match the route information known to a caller. Empty hints represent
// unknown dimensions rather than mismatches.
func (m *Manager) ResponsesToolsMayApplyToRoute(provider, upstreamModel string) bool {
	if m == nil {
		return false
	}
	state := m.responsesToolsSnapshot()
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if !state.enabled {
		return false
	}
	provider = strings.TrimSpace(provider)
	upstreamModel = strings.TrimSpace(upstreamModel)
	if len(state.policy.Routes) == 0 {
		// The convention policy covers every route, but a native Responses
		// upstream needs no rewriting and must keep its passthrough path.
		return providerNeedsToolsBridge(provider)
	}
	for _, route := range state.policy.Routes {
		if provider != "" && !strings.EqualFold(provider, strings.TrimSpace(route.Match.Provider)) {
			continue
		}
		if upstreamModel != "" && !strings.EqualFold(upstreamModel, strings.TrimSpace(route.Match.UpstreamModel)) {
			continue
		}
		if route.ClientSearch == responsestools.ClientSearchBridge ||
			route.ClientSearch == responsestools.ClientSearchDisabled ||
			route.CustomTools == responsestools.CustomToolsFunction ||
			route.CustomTools == responsestools.CustomToolsStrip ||
			route.CustomTools == responsestools.CustomToolsReject ||
			route.Schema.CompletesSearchSchemas() ||
			route.Schema.LocalRefs == responsestools.LocalRefsInline ||
			route.Schema.LocalRefs == responsestools.LocalRefsFlatten {
			return true
		}
	}
	return false
}

// providerNeedsToolsBridge reports whether a provider's route can be served
// without the core tool protocol. It requires both a native Responses wire
// format and an executor that forwards the tool_search built-in unchanged, so
// it stays consistent with responsestools.ConventionPolicy. Executor-level
// resolvers can only widen support, so an unknown provider needs the bridge.
func providerNeedsToolsBridge(provider string) bool {
	if providerDefaultFormat(provider) != sdktranslator.FormatCodex {
		return true
	}
	return !responsestools.ProviderForwardsToolSearch(provider)
}

// ResponsesToolsMayApplyToClientModel resolves the public model alias through
// every eligible credential before checking the tool policy. The optional
// provider and auth hints narrow the candidate set when a WebSocket session has
// already selected its route.
func (m *Manager) ResponsesToolsMayApplyToClientModel(modelID, provider, authID string) bool {
	if m == nil {
		return false
	}
	state := m.responsesToolsSnapshot()
	if state == nil {
		return false
	}
	state.mu.Lock()
	policy := state.policy
	enabled := state.enabled
	state.mu.Unlock()
	if !enabled {
		return false
	}
	provider = strings.TrimSpace(provider)
	for _, route := range m.responsesToolsCandidateRoutes(modelID, authID) {
		if provider != "" && !strings.EqualFold(provider, route.Provider) {
			continue
		}
		routePolicy, err := responsestools.EffectivePolicy(policy, route)
		if err != nil {
			continue
		}
		if routePolicy.ClientSearch == responsestools.ClientSearchBridge ||
			routePolicy.ClientSearch == responsestools.ClientSearchDisabled ||
			routePolicy.CustomTools == responsestools.CustomToolsFunction ||
			routePolicy.CustomTools == responsestools.CustomToolsStrip ||
			routePolicy.CustomTools == responsestools.CustomToolsReject ||
			routePolicy.Schema.CompletesSearchSchemas() ||
			routePolicy.Schema.LocalRefs == responsestools.LocalRefsInline ||
			routePolicy.Schema.LocalRefs == responsestools.LocalRefsFlatten {
			return true
		}
	}
	// A native Responses route resolves to client-search native with no custom
	// handling, which needs no rewriting and therefore no replay.
	return false
}
