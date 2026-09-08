package handlers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"golang.org/x/net/context"
)

// recordPreExecutionFailure records a usage_errors row for a request that
// failed before any provider executor ran. The executors record their own
// failures via a deferred TrackFailure, so this must only be called on
// branches where no executor was invoked; otherwise attempts would be
// double-counted in the dashboard Errors feed.
func recordPreExecutionFailure(ctx context.Context, provider, model, alias string, errMsg *interfaces.ErrorMessage) {
	if errMsg == nil || errMsg.Error == nil {
		return
	}
	helps.PublishPreExecutionFailure(ctx, provider, model, alias, errMsg.StatusCode, errMsg.Error.Error())
}

// recordAuthManagerFailure records the conductor-level failure when it is
// certain the executor never ran (credential selection failed before any
// attempt, or a post-auth interceptor terminated the request). When an
// executor did run, its deferred TrackFailure already recorded the failure, so
// re-recording here would double-count the attempt.
func recordAuthManagerFailure(ctx context.Context, provider, model, alias string, err error) {
	if !isPreExecutionError(err) {
		return
	}
	statusCode := clienterror.HTTPStatusFromError(err)
	helps.PublishPreExecutionFailure(ctx, provider, model, alias, statusCode, err.Error())
}

// isPreExecutionError reports whether err is produced only before any provider
// executor runs. Conductor credential-selection failures (no usable auth,
// unknown provider/credential policy) qualify. Executor-produced errors
// (including auth-terminated and upstream status errors) are already recorded
// by the executor's deferred TrackFailure and must not be re-recorded here.
func isPreExecutionError(err error) bool {
	var authErr *coreauth.Error
	if !errors.As(err, &authErr) || authErr == nil {
		return false
	}
	switch authErr.Code {
	case "auth_not_found", "auth_unavailable", "provider_not_found", "invalid_auth_kind", "invalid_credential_policy":
		return true
	}
	return false
}

// PluginExecutorHost executes a routed request with a specific plugin executor.
type PluginExecutorHost interface {
	ExecutePluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	ExecutePluginExecutorStream(context.Context, string, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
	CountPluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
}

type pluginExecutorFormatResolver interface {
	PluginExecutorRequestToFormat(string, coreexecutor.Request, coreexecutor.Options) sdktranslator.Format
}

// ExecuteWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, false)
}

// ExecuteImageWithAuthManager executes an OpenAI-compatible image endpoint request.
func (h *BaseAPIHandler) ExecuteImageWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, true)
}

func (h *BaseAPIHandler) executeWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, allowImageModel bool) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeWithAuthManagerFormats(ctx, handlerType, handlerType, modelName, rawJSON, alt, allowImageModel, modelExecutionOptions{})
}

func (h *BaseAPIHandler) executeWithAuthManagerFormats(ctx context.Context, entryProtocol, exitProtocol, modelName string, rawJSON []byte, alt string, allowImageModel bool, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	originalRequestedModel := modelName
	routeDecision := h.applyModelRouter(ctx, entryProtocol, modelName, rawJSON, false, execOptions)
	responseProtocol := modelExecutionResponseProtocol(entryProtocol, exitProtocol)
	if errMsg := validateNativeInteractionsExecution(entryProtocol, execOptions, routeDecision); errMsg != nil {
		recordPreExecutionFailure(ctx, routeDecision.Provider, originalRequestedModel, modelName, errMsg)
		return nil, nil, errMsg
	}
	if routeDecision.ExecutorPluginID != "" {
		return h.executeWithPluginExecutor(ctx, entryProtocol, responseProtocol, modelName, originalRequestedModel, rawJSON, alt, routeDecision.ExecutorPluginID, execOptions)
	}
	// Auto Router: if the requested model is an Auto Router id, score the
	// request and forward it to the tier-appropriate upstream model. The
	// original requested model (the router id) is preserved for attribution
	// and logging, while execution uses the resolved target model. The tier's
	// per-model routing (providers/strategy/priorities) is applied after
	// provider resolution so it overrides the target model's default routing.
	executionModel := modelName
	var autoRoute *autorouter.Resolved
	var resolvedAR autoRouterResolved
	if rr := h.resolveAutoRouterModel(ctx, entryProtocol, modelName, rawJSON); rr.matched {
		resolvedAR = rr
		executionModel = resolvedAR.targetModel
		autoRoute = resolvedAR.route
		ctx = resolvedAR.withDecisionContext(ctx)
	} else if rr.resolveFailed {
		// The request targeted an enabled auto-router whose tier mapping cannot
		// resolve: an explicit 503 beats auth_not_found from the synthetic
		// auto-router provider (or an unknown model forwarded upstream).
		errMsg := rr.resolveFailureError()
		recordPreExecutionFailure(ctx, routeDecision.Provider, originalRequestedModel, modelName, errMsg)
		return nil, nil, errMsg
	}
	if resolvedAR.matched {
		rawJSON = h.autoRouterRequestAdjustments(ctx, resolvedAR, rawJSON)
	}
	providers, normalizedModel, errMsg := h.providersForExecution(ctx, executionModel, originalRequestedModel, allowImageModel, routeDecision, execOptions)
	if errMsg != nil {
		recordPreExecutionFailure(ctx, routeDecision.Provider, originalRequestedModel, normalizedModel, errMsg)
		return nil, nil, errMsg
	}
	if autoRoute != nil {
		var routeErr *interfaces.ErrorMessage
		providers, routeErr = h.applyAutoRouterRoute(ctx, providers, autoRoute)
		if routeErr != nil {
			recordPreExecutionFailure(ctx, routeDecision.Provider, originalRequestedModel, normalizedModel, routeErr)
			return nil, nil, routeErr
		}
	}
	providers = adjustExecutionProvidersForEntryProtocol(entryProtocol, providers)
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
	addAuthSelectionModelMetadata(reqMeta, execOptions.AuthSelectionModel)
	addModelExecutionSourceMetadata(reqMeta, execOptions.InternalSource)
	setReasoningEffortMetadata(reqMeta, entryProtocol, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	setRouteStrategyMetadata(ctx, reqMeta)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	afterAuthCapture := &requestAfterAuthCapture{}
	lifecycle := h.newRequestLifecycleTracker(ctx, entryProtocol, normalizedModel, originalRequestedModel, false, reqMeta, execOptions.SkipInterceptorPluginID)
	opts := coreexecutor.Options{
		Stream:                      false,
		Alt:                         alt,
		OriginalRequest:             rawJSON,
		SourceFormat:                sdktranslator.FromString(entryProtocol),
		ResponseFormat:              sdktranslator.FromString(responseProtocol),
		Headers:                     modelExecutionHeaders(ctx, execOptions.Headers),
		Query:                       modelExecutionQuery(ctx, execOptions.Query),
		RequestAfterAuthInterceptor: h.requestAfterAuthInterceptor(afterAuthCapture, lifecycle.requestID(), execOptions.SkipInterceptorPluginID),
	}
	opts.Metadata = reqMeta
	var interceptErr *interfaces.ErrorMessage
	req, opts, interceptErr = h.applyRequestInterceptorsBeforeAuth(ctx, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		recordPreExecutionFailure(ctx, routeDecision.Provider, originalRequestedModel, normalizedModel, interceptErr)
		return nil, nil, interceptErr
	}
	resp, err := h.AuthManager.Execute(ctx, providers, req, opts)
	if err != nil {
		// providers can be empty when upstream resolution (e.g., an Auto Router
		// tier pin with no intersecting upstream) leaves the slice nil; in that
		// case AuthManager returns provider_not_found. Fall back to the routing
		// decision's provider so we still attribute the pre-execution failure
		// instead of panicking on providers[0].
		providerForRecord := routeDecision.Provider
		if len(providers) > 0 {
			providerForRecord = providers[0]
		}
		recordAuthManagerFailure(ctx, providerForRecord, originalRequestedModel, normalizedModel, err)
		err = enrichAuthSelectionError(h, ctx, err, providers, normalizedModel)
		errMsg := executionErrorMessage(err)
		lifecycle.completeError(ctx, errMsg)
		return nil, nil, errMsg
	}
	executedReq, executedOpts := afterAuthCapture.apply(req, opts)
	if resolvedAR.matched {
		// A routed request that hit the token cap with zero visible content is
		// an upstream/config fault, not an empty success: surface it explicitly
		// so clients see the cause instead of an empty 200.
		if emptyErr := autoRouterEmptyCompletionError(resp.Payload); emptyErr != nil {
			lifecycle.completeError(ctx, emptyErr)
			return nil, nil, emptyErr
		}
	}
	rawResponseHeaders := cloneHeader(resp.Headers)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, PassthroughHeadersEnabled(h.Cfg))
	body, responseHeaders := h.applyResponseInterceptors(ctx, lifecycle.requestID(), responseProtocol, normalizedModel, originalRequestedModel, executedOpts, rawResponseHeaders, responseHeaders, executedOpts.OriginalRequest, executedReq.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID)
	lifecycle.complete(pluginapi.RequestCompletionSucceeded, http.StatusOK, nil)
	return body, responseHeaders, nil
}

// ExecuteCountWithAuthManager executes a non-streaming request via the core auth manager.
// This path is the only supported execution route.
func (h *BaseAPIHandler) ExecuteCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) ([]byte, http.Header, *interfaces.ErrorMessage) {
	return h.executeCountWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, modelExecutionOptions{})
}

func (h *BaseAPIHandler) executeCountWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	originalRequestedModel := modelName
	routeDecision := h.applyModelRouter(ctx, handlerType, modelName, rawJSON, false, execOptions)
	if routeDecision.ExecutorPluginID != "" {
		return h.countWithPluginExecutor(ctx, handlerType, modelName, originalRequestedModel, rawJSON, alt, routeDecision.ExecutorPluginID, execOptions)
	}
	executionModel := modelName
	var autoRoute *autorouter.Resolved
	var resolvedAR autoRouterResolved
	if rr := h.resolveAutoRouterModel(ctx, handlerType, modelName, rawJSON); rr.matched {
		resolvedAR = rr
		executionModel = resolvedAR.targetModel
		autoRoute = resolvedAR.route
		ctx = coreusage.WithRouterTier(ctx, resolvedAR.tier, resolvedAR.routerID)
	} else if rr.resolveFailed {
		errMsg := rr.resolveFailureError()
		recordPreExecutionFailure(ctx, routeDecision.Provider, originalRequestedModel, modelName, errMsg)
		return nil, nil, errMsg
	}
	if resolvedAR.matched {
		rawJSON = h.autoRouterRequestAdjustments(ctx, resolvedAR, rawJSON)
	}
	providers, normalizedModel, errMsg := h.providersForExecution(ctx, executionModel, originalRequestedModel, false, routeDecision, execOptions)
	if errMsg != nil {
		recordPreExecutionFailure(ctx, routeDecision.Provider, originalRequestedModel, normalizedModel, errMsg)
		return nil, nil, errMsg
	}
	if autoRoute != nil {
		var routeErr *interfaces.ErrorMessage
		providers, routeErr = h.applyAutoRouterRoute(ctx, providers, autoRoute)
		if routeErr != nil {
			recordPreExecutionFailure(ctx, routeDecision.Provider, originalRequestedModel, normalizedModel, routeErr)
			return nil, nil, routeErr
		}
	}
	providers = adjustExecutionProvidersForEntryProtocol(handlerType, providers)
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
	addAuthSelectionModelMetadata(reqMeta, execOptions.AuthSelectionModel)
	setReasoningEffortMetadata(reqMeta, handlerType, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	setRouteStrategyMetadata(ctx, reqMeta)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	afterAuthCapture := &requestAfterAuthCapture{}
	lifecycle := h.newRequestLifecycleTracker(ctx, handlerType, normalizedModel, originalRequestedModel, false, reqMeta, execOptions.SkipInterceptorPluginID)
	opts := coreexecutor.Options{
		Stream:                      false,
		Alt:                         alt,
		OriginalRequest:             rawJSON,
		SourceFormat:                sdktranslator.FromString(handlerType),
		Headers:                     modelExecutionHeaders(ctx, execOptions.Headers),
		Query:                       modelExecutionQuery(ctx, execOptions.Query),
		RequestAfterAuthInterceptor: h.requestAfterAuthInterceptor(afterAuthCapture, lifecycle.requestID(), execOptions.SkipInterceptorPluginID),
	}
	opts.Metadata = reqMeta
	var interceptErr *interfaces.ErrorMessage
	req, opts, interceptErr = h.applyRequestInterceptorsBeforeAuth(ctx, handlerType, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		// See executeWithAuthManagerFormats: providers may be empty when an
		// Auto Router tier pin yields no intersection; fall back to the
		// routing decision's provider instead of panicking on providers[0].
		providerForRecord := routeDecision.Provider
		if len(providers) > 0 {
			providerForRecord = providers[0]
		}
		recordAuthManagerFailure(ctx, providerForRecord, originalRequestedModel, normalizedModel, err)
		err = enrichAuthSelectionError(h, ctx, err, providers, normalizedModel)
		errMsg := executionErrorMessage(err)
		lifecycle.completeError(ctx, errMsg)
		return nil, nil, errMsg
	}
	executedReq, executedOpts := afterAuthCapture.apply(req, opts)
	rawResponseHeaders := cloneHeader(resp.Headers)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, PassthroughHeadersEnabled(h.Cfg))
	body, responseHeaders := h.applyResponseInterceptors(ctx, lifecycle.requestID(), handlerType, normalizedModel, originalRequestedModel, executedOpts, rawResponseHeaders, responseHeaders, executedOpts.OriginalRequest, executedReq.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID)
	lifecycle.complete(pluginapi.RequestCompletionSucceeded, http.StatusOK, nil)
	return body, responseHeaders, nil
}

func (h *BaseAPIHandler) executeWithPluginExecutor(ctx context.Context, entryProtocol, responseProtocol, modelName, originalRequestedModel string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	if h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("plugin executor routing is unavailable while Home is enabled")}
		recordPreExecutionFailure(ctx, "", originalRequestedModel, modelName, errMsg)
		return nil, nil, errMsg
	}
	host := h.pluginExecutorHost()
	if host == nil {
		errMsg := &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
		recordPreExecutionFailure(ctx, "", originalRequestedModel, modelName, errMsg)
		return nil, nil, errMsg
	}
	req, opts := h.pluginExecutorRequest(ctx, entryProtocol, responseProtocol, modelName, originalRequestedModel, rawJSON, alt, false, execOptions)
	lifecycle := h.newRequestLifecycleTracker(ctx, entryProtocol, modelName, originalRequestedModel, false, opts.Metadata, execOptions.SkipInterceptorPluginID)
	var interceptErr *interfaces.ErrorMessage
	req, opts, interceptErr = h.applyRequestInterceptorsBeforeAuth(ctx, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	req, opts, interceptErr = h.applyRequestInterceptorsAfterPluginExecutorRoute(ctx, host, executorPluginID, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	resp, errExecute := host.ExecutePluginExecutor(ctx, executorPluginID, req, opts)
	if errExecute != nil {
		errMsg := executionErrorMessage(errExecute)
		lifecycle.completeError(ctx, errMsg)
		return nil, nil, errMsg
	}
	rawResponseHeaders := cloneHeader(resp.Headers)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, PassthroughHeadersEnabled(h.Cfg))
	body, responseHeaders := h.applyResponseInterceptors(ctx, lifecycle.requestID(), responseProtocol, modelName, originalRequestedModel, opts, rawResponseHeaders, responseHeaders, opts.OriginalRequest, req.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID)
	lifecycle.complete(pluginapi.RequestCompletionSucceeded, http.StatusOK, nil)
	return body, responseHeaders, nil
}

func (h *BaseAPIHandler) countWithPluginExecutor(ctx context.Context, handlerType, modelName, originalRequestedModel string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	if h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("plugin executor routing is unavailable while Home is enabled")}
	}
	host := h.pluginExecutorHost()
	if host == nil {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
	}
	req, opts := h.pluginExecutorRequest(ctx, handlerType, handlerType, modelName, originalRequestedModel, rawJSON, alt, false, execOptions)
	lifecycle := h.newRequestLifecycleTracker(ctx, handlerType, modelName, originalRequestedModel, false, opts.Metadata, execOptions.SkipInterceptorPluginID)
	var interceptErr *interfaces.ErrorMessage
	req, opts, interceptErr = h.applyRequestInterceptorsBeforeAuth(ctx, handlerType, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	req, opts, interceptErr = h.applyRequestInterceptorsAfterPluginExecutorRoute(ctx, host, executorPluginID, handlerType, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	resp, errCount := host.CountPluginExecutor(ctx, executorPluginID, req, opts)
	if errCount != nil {
		errMsg := executionErrorMessage(errCount)
		lifecycle.completeError(ctx, errMsg)
		return nil, nil, errMsg
	}
	rawResponseHeaders := cloneHeader(resp.Headers)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, PassthroughHeadersEnabled(h.Cfg))
	body, responseHeaders := h.applyResponseInterceptors(ctx, lifecycle.requestID(), handlerType, modelName, originalRequestedModel, opts, rawResponseHeaders, responseHeaders, opts.OriginalRequest, req.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID)
	lifecycle.complete(pluginapi.RequestCompletionSucceeded, http.StatusOK, nil)
	return body, responseHeaders, nil
}

func (h *BaseAPIHandler) pluginExecutorRequest(ctx context.Context, entryProtocol, responseProtocol, modelName, originalRequestedModel string, rawJSON []byte, alt string, stream bool, execOptions modelExecutionOptions) (coreexecutor.Request, coreexecutor.Options) {
	reqMeta := requestExecutionMetadata(ctx)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
	addAuthSelectionModelMetadata(reqMeta, execOptions.AuthSelectionModel)
	addModelExecutionSourceMetadata(reqMeta, execOptions.InternalSource)
	setReasoningEffortMetadata(reqMeta, entryProtocol, modelName, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{Model: modelName, Payload: payload}
	opts := coreexecutor.Options{
		Stream:          stream,
		Alt:             alt,
		OriginalRequest: rawJSON,
		SourceFormat:    sdktranslator.FromString(entryProtocol),
		ResponseFormat:  sdktranslator.FromString(responseProtocol),
		Headers:         modelExecutionHeaders(ctx, execOptions.Headers),
		Query:           modelExecutionQuery(ctx, execOptions.Query),
		Metadata:        reqMeta,
	}
	return req, opts
}

func (h *BaseAPIHandler) applyRequestInterceptorsAfterPluginExecutorRoute(ctx context.Context, host PluginExecutorHost, executorPluginID, entryProtocol, originalRequestedModel, requestID string, req coreexecutor.Request, opts coreexecutor.Options, skipPluginID string) (coreexecutor.Request, coreexecutor.Options, *interfaces.ErrorMessage) {
	if !requestInterceptorsEnabled(h.interceptorHost()) {
		return req, opts, nil
	}
	toFormat := sdktranslator.FromString(entryProtocol)
	if resolver, ok := host.(pluginExecutorFormatResolver); ok && resolver != nil {
		if resolved := resolver.PluginExecutorRequestToFormat(executorPluginID, req, opts); resolved != "" {
			toFormat = resolved
		}
	}
	resp := h.applyRequestInterceptorsAfterAuth(ctx, coreexecutor.RequestAfterAuthInterceptRequest{
		SourceFormat:   opts.SourceFormat,
		ToFormat:       toFormat,
		Model:          req.Model,
		RequestedModel: originalRequestedModel,
		Stream:         opts.Stream,
		Headers:        cloneHeader(opts.Headers),
		Body:           cloneBytes(req.Payload),
		Metadata:       opts.Metadata,
	}, requestID, skipPluginID)
	opts.Headers = mergeRequestInterceptorHeaders(opts.Headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		req.Payload = cloneBytes(resp.Body)
		opts.OriginalRequest = cloneBytes(resp.Body)
	}
	if resp.Terminate {
		return req, opts, directTerminationError(resp.StatusCode, resp.ResponseHeaders, resp.ResponseBody)
	}
	return req, opts, nil
}

func ExecutionErrorMessage(err error) *interfaces.ErrorMessage {
	return executionErrorMessage(err)
}

func executionErrorMessage(err error) *interfaces.ErrorMessage {
	var terminated *coreexecutor.RequestTerminatedError
	if errors.As(err, &terminated) && terminated != nil {
		return &interfaces.ErrorMessage{
			StatusCode:     normalizedTerminationStatus(terminated.StatusCode()),
			Error:          err,
			DirectResponse: true,
			Body:           terminated.ResponseBody(),
			Headers:        terminated.ResponseHeaders(),
		}
	}
	status := http.StatusInternalServerError
	if code := clienterror.HTTPStatusFromError(err); code > 0 {
		status = code
	}
	var addon http.Header
	if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
		if hdr := he.Headers(); hdr != nil {
			addon = hdr.Clone()
		}
	}
	return &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
}

func (h *BaseAPIHandler) pluginExecutorHost() PluginExecutorHost {
	if h == nil {
		return nil
	}
	if executorHost, ok := h.ModelRouterHost.(PluginExecutorHost); ok && executorHost != nil {
		return executorHost
	}
	if executorHost, ok := h.PluginHost.(PluginExecutorHost); ok && executorHost != nil {
		return executorHost
	}
	return nil
}
