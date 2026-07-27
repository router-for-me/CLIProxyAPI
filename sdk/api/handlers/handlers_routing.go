package handlers

import (
	"fmt"
	"net/http"
	"strings"

	. "github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
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
	if preferredIndex <= 0 {
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

func (h *BaseAPIHandler) getRequestDetails(modelName string) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
	return h.getRequestDetailsWithOptions(modelName, false)
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
func (h *BaseAPIHandler) providersForExecution(modelName, originalRequestedModel string, allowImageModel bool, routeDecision modelRouteDecision, execOptions modelExecutionOptions) ([]string, string, *interfaces.ErrorMessage) {
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
	return h.getRequestDetailsWithOptions(modelName, allowImageModel)
}

func (h *BaseAPIHandler) getRequestDetailsWithOptions(modelName string, allowImageModel bool) (providers []string, normalizedModel string, err *interfaces.ErrorMessage) {
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

	providers = util.GetProviderName(baseModel)
	// Fallback: if baseModel has no provider but differs from resolvedModelName,
	// try using the full model name. This handles edge cases where custom models
	// may be registered with their full suffixed name (e.g., "my-model(8192)").
	// Evaluated in Story 11.8: This fallback is intentionally preserved to support
	// custom model registrations that include thinking suffixes.
	if len(providers) == 0 && baseModel != resolvedModelName {
		providers = util.GetProviderName(resolvedModelName)
	}

	if len(providers) == 0 {
		return nil, "", &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("unknown provider for model %s", modelName)}
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
