package auth

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// quotaRecoveryModelHeadersMatchLocked checks every route whose restriction a
// reset can release. Model headers are applied after credential headers by the
// Codex executors, so the canonical usage probe must cover their final identity.
// Requires m.mu; resolve candidates without advancing the model-pool rotation,
// whose normal execution helper also acquires m.mu.
func (m *Manager) quotaRecoveryModelHeadersMatchLocked(auth *Auth) bool {
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") {
		return true
	}
	routes := make(map[string]struct{})
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(auth.ID) {
		if model != nil {
			routes[model.ID] = struct{}{}
		}
	}
	for model, state := range auth.ModelStates {
		if state != nil && state.Quota.Exceeded {
			routes[model] = struct{}{}
		}
	}
	token, _ := auth.Metadata["access_token"].(string)
	account, _ := auth.Metadata["account_id"].(string)
	token, account = strings.TrimSpace(token), strings.TrimSpace(account)
	nestedToken := ""
	if IsCodexUseSIWC(auth) && quotaRecoverySIWCAccountMatches(auth) {
		nestedToken = authSIWCTokenField(auth, "access_token")
		if nestedToken == "" {
			nestedToken = authSIWCTokenField(auth, "access")
		}
	}
	routing := m.loadAPIKeyModelRouting()
	for route := range routes {
		requested := rewriteModelForAuth(route, auth)
		alias := m.resolveExecutionAliasResultForRequestedWithRouting(routing, auth, requested)
		upstream := executionAliasPoolModel(auth, requested, alias)
		var candidates []string
		if homeModel := strings.TrimSpace(auth.Attributes[homeUpstreamModelAttributeKey]); homeModel != "" {
			candidates = []string{homeModel}
		}
		if len(candidates) == 0 {
			candidates = resolveOpenAICompatUpstreamModelPool(routing.config, auth, upstream)
			if len(candidates) == 0 {
				resolved := m.applyAPIKeyModelAliasWithRouting(routing, auth, upstream)
				if strings.TrimSpace(resolved) == "" {
					resolved = upstream
				}
				candidates = []string{resolved}
			}
		}
		var finalizedModels []string
		for _, model := range candidates {
			// Executors remove the thinking suffix before building the business payload.
			model = thinking.ParseSuffix(model).ModelName
			models, safe := quotaRecoveryPayloadModels(routing.config, model, route, requested)
			if !safe {
				return false
			}
			finalizedModels = append(finalizedModels, models...)
		}
		for _, model := range finalizedModels {
			for key, value := range registry.ModelOverrideHeaders(model) {
				switch strings.ToLower(strings.TrimSpace(key)) {
				case "authorization":
					if (token == "" || value != "Bearer "+token) && (nestedToken == "" || value != "Bearer "+nestedToken) {
						return false
					}
				case "chatgpt-account-id":
					if account == "" || value != account {
						return false
					}
				}
			}
		}
	}
	return true
}
