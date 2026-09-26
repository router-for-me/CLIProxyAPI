package auth

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/responsestools"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
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
		routePolicy, matched, err := responsestools.ResolvePolicy(policy, route)
		if err != nil || !matched {
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
	if m == nil {
		return nil
	}
	m.mu.RLock()
	auths := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled {
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
