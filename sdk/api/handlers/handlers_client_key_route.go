package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/context"
)

// clientKeyRoute precedes plugin routing and registry lookup. The provider list
// contains exactly one provider, so exhaustion cannot fall back to another backend.
// Missing or ambiguous policy targets fail closed instead of restoring default routing.
func (h *BaseAPIHandler) clientKeyRoute(ctx context.Context, requestedModel string) (modelRouteDecision, bool) {
	if h == nil || h.CurrentConfig() == nil {
		return modelRouteDecision{}, false
	}
	key := helps.APIKeyFromContext(ctx)
	if key == "" {
		return modelRouteDecision{}, false
	}
	var decision modelRouteDecision
	matches := 0
	policyName := ""
	for _, policy := range h.CurrentConfig().APIKeyRoutes {
		if policy.APIKey != key {
			continue
		}
		matches++
		policyName = strings.TrimSpace(policy.Name)
		decision.Provider = strings.ToLower(strings.TrimSpace(policy.Provider))
		decision.Model = resolveClientKeyRouteModel(requestedModel, policy)
	}
	if matches == 0 {
		return decision, false
	}
	if matches != 1 || decision.Provider == "" || decision.Model == "" || (h.AuthManager != nil && h.AuthManager.HomeEnabled()) {
		decision.PolicyError = &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("client key routing policy is invalid or unavailable")}
		return decision, true
	}
	log.WithFields(log.Fields{"route": policyName, "requested_model": requestedModel, "provider": decision.Provider, "target_model": decision.Model}).Info("client key routing policy applied")
	return decision, true
}

func resolveClientKeyRouteModel(requestedModel string, policy sdkconfig.APIKeyRoute) string {
	defaultModel := strings.TrimSpace(policy.Model)
	if defaultModel == "" {
		return ""
	}
	base := strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(requestedModel).ModelName))
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[idx+1:]
	}
	base = strings.TrimSuffix(base, "[1m]")
	switch {
	case strings.HasPrefix(base, "claude-haiku-") && strings.TrimSpace(policy.HaikuModel) != "":
		return strings.TrimSpace(policy.HaikuModel)
	case strings.HasPrefix(base, "claude-sonnet-") && strings.TrimSpace(policy.SonnetModel) != "":
		return strings.TrimSpace(policy.SonnetModel)
	case (strings.HasPrefix(base, "claude-opus-") || strings.HasPrefix(base, "claude-fable-") || strings.HasPrefix(base, "claude-mythos-")) && strings.TrimSpace(policy.OpusModel) != "":
		return strings.TrimSpace(policy.OpusModel)
	default:
		return defaultModel
	}
}
