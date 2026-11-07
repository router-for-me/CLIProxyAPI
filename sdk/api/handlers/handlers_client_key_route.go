package handlers

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
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
		decision.FallbackModels = resolveClientKeyRouteFallbackModels(decision.Model, policy)
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

func resolveClientKeyRouteFallbackModels(primaryModel string, policy sdkconfig.APIKeyRoute) []string {
	raw := strings.TrimSpace(policy.FallbackModel)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	seen := map[string]struct{}{}
	if p := strings.ToLower(strings.TrimSpace(primaryModel)); p != "" {
		seen[p] = struct{}{}
	}
	for _, part := range parts {
		candidate := strings.TrimSpace(part)
		if candidate == "" {
			continue
		}
		lower := strings.ToLower(candidate)
		if _, ok := seen[lower]; ok {
			continue
		}
		seen[lower] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func shouldFallbackClientKeyRouteModel(ctx context.Context, err error) bool {
	if err == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	switch statusFromError(err) {
	case http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	}
	return isAuthSelectionUnavailable(err)
}

func (h *BaseAPIHandler) executeWithRouteFallbacks(ctx context.Context, providers []string, normalizedModel *string, req *coreexecutor.Request, opts coreexecutor.Options, fallbacks []string) (coreexecutor.Response, error) {
	resp, err := h.AuthManager.Execute(ctx, providers, *req, opts)
	if err == nil || len(fallbacks) == 0 || !shouldFallbackClientKeyRouteModel(ctx, err) {
		return resp, err
	}
	for _, fbModel := range fallbacks {
		fbReq := *req
		fbReq.Model = fbModel
		fbResp, fbErr := h.AuthManager.Execute(ctx, providers, fbReq, opts)
		if fbErr == nil {
			log.WithFields(log.Fields{"provider": strings.Join(providers, ","), "from_model": *normalizedModel, "fallback_model": fbModel}).Info("client key route fallback model applied")
			*normalizedModel = fbModel
			*req = fbReq
			return fbResp, nil
		}
		if !shouldFallbackClientKeyRouteModel(ctx, fbErr) {
			break
		}
	}
	return resp, err
}

func (h *BaseAPIHandler) executeStreamWithRouteFallbacks(ctx context.Context, providers []string, normalizedModel *string, req *coreexecutor.Request, opts coreexecutor.Options, fallbacks *[]string) (*coreexecutor.StreamResult, error) {
	streamResult, err := h.AuthManager.ExecuteStream(ctx, providers, *req, opts)
	if err == nil || fallbacks == nil || len(*fallbacks) == 0 || !shouldFallbackClientKeyRouteModel(ctx, err) {
		return streamResult, err
	}
	for len(*fallbacks) > 0 {
		fbModel := (*fallbacks)[0]
		*fallbacks = (*fallbacks)[1:]
		fbReq := *req
		fbReq.Model = fbModel
		fbResult, fbErr := h.AuthManager.ExecuteStream(ctx, providers, fbReq, opts)
		if fbErr == nil {
			log.WithFields(log.Fields{"provider": strings.Join(providers, ","), "from_model": *normalizedModel, "fallback_model": fbModel}).Info("client key route stream fallback model applied")
			*normalizedModel = fbModel
			*req = fbReq
			return fbResult, nil
		}
		if !shouldFallbackClientKeyRouteModel(ctx, fbErr) {
			break
		}
	}
	return streamResult, err
}
