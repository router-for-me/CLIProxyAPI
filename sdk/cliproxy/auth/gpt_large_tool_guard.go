package auth

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	gptLargeToolHistoryMessages            = 100
	gptLargeToolHistoryTools               = 40
	gptLargeToolHistoryMaxRetryCredentials = 1
)

type gptLargeToolHistoryFallbackGuard struct {
	enabled             bool
	attemptedRouteGroup map[string]struct{}
}

func newGPTLargeToolHistoryFallbackGuard(providers []string, routeModel string, opts cliproxyexecutor.Options) *gptLargeToolHistoryFallbackGuard {
	if !isGPTLargeToolHistoryResponsesRequest(providers, routeModel, opts) {
		return nil
	}
	return &gptLargeToolHistoryFallbackGuard{
		enabled:             true,
		attemptedRouteGroup: make(map[string]struct{}),
	}
}

func (g *gptLargeToolHistoryFallbackGuard) effectiveMaxRetryCredentials(current int) int {
	if g == nil || !g.enabled {
		return current
	}
	if current == 0 || current > gptLargeToolHistoryMaxRetryCredentials {
		return gptLargeToolHistoryMaxRetryCredentials
	}
	return current
}

func (g *gptLargeToolHistoryFallbackGuard) shouldSkipAuth(auth *Auth) bool {
	if g == nil || !g.enabled || !isCodexAuth(auth) {
		return false
	}
	group := explicitAuthRoutingGroup(auth)
	if group == "" {
		return false
	}
	_, ok := g.attemptedRouteGroup[group]
	return ok
}

func (g *gptLargeToolHistoryFallbackGuard) markAuth(auth *Auth) {
	if g == nil || !g.enabled || !isCodexAuth(auth) {
		return
	}
	group := explicitAuthRoutingGroup(auth)
	if group == "" {
		return
	}
	g.attemptedRouteGroup[group] = struct{}{}
}

func isGPTLargeToolHistoryResponsesRequest(providers []string, routeModel string, opts cliproxyexecutor.Options) bool {
	if len(providers) != 1 || !isCodexProviderName(providers[0]) {
		return false
	}
	if !isGPTLargeToolHistoryResponsesModel(requestedModelAliasFromOptions(opts, routeModel)) &&
		!isGPTLargeToolHistoryResponsesModel(routeModel) {
		return false
	}
	if !isResponsesEndpointPath(metadataString(opts.Metadata, cliproxyexecutor.RequestPathMetadataKey)) {
		return false
	}
	shape := requestShapeFromOptions(opts)
	return shape.MessageCount >= gptLargeToolHistoryMessages || shape.ToolCount >= gptLargeToolHistoryTools
}

func isGPTLargeToolHistoryResponsesModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(thinking.ParseSuffix(model).ModelName))
	switch model {
	case "gpt-5.5", "gpt-5.4":
		return true
	default:
		return false
	}
}

func isResponsesEndpointPath(path string) bool {
	path = strings.ToLower(strings.TrimSpace(path))
	if path == "" {
		return false
	}
	if idx := strings.Index(path, " "); idx >= 0 {
		path = strings.TrimSpace(path[idx+1:])
	}
	if idx := strings.Index(path, "?"); idx >= 0 {
		path = strings.TrimSpace(path[:idx])
	}
	return path == "/v1/responses"
}

func metadataString(meta map[string]any, key string) string {
	if len(meta) == 0 {
		return ""
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return ""
	}
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case []byte:
		return strings.TrimSpace(string(value))
	default:
		return ""
	}
}

func explicitAuthRoutingGroup(auth *Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	for _, key := range []string{"routing_group", "routing-group"} {
		if value := normalizeRoutingGroupKey(auth.Attributes[key]); value != "" {
			return value
		}
	}
	return ""
}

func contextWithSelectedAuthRoutingGroup(ctx context.Context, auth *Auth) context.Context {
	if auth == nil {
		return ctx
	}
	return coreusage.WithRoutingGroup(ctx, authRoutingGroup(auth))
}
