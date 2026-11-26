package handlers

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"golang.org/x/net/context"
)

// PluginExecutorHost executes a routed request with a specific plugin executor.
type PluginExecutorHost interface {
	ExecutePluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
	ExecutePluginExecutorStream(context.Context, string, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error)
	CountPluginExecutor(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error)
}

type pluginExecutorFormatResolver interface {
	PluginExecutorRequestToFormat(string, coreexecutor.Request, coreexecutor.Options) sdktranslator.Format
}

type pluginExecutorAttributionHost interface {
	ExecutePluginExecutorWithAttribution(context.Context, string, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, *pluginapi.UsageAttribution, error)
	ExecutePluginExecutorStreamWithAttribution(context.Context, string, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, *pluginapi.UsageAttribution, error)
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
	// Speech-only models are reachable solely through the speech entry protocol.
	execOptions.AllowSpeechModel = isModelExecutionSpeechProtocol(entryProtocol)
	var routeDecision modelRouteDecision
	if !h.isHomeLocalExecutorModel(modelName) {
		routeDecision = h.applyModelRouter(ctx, entryProtocol, modelName, rawJSON, false, execOptions)
	}
	responseProtocol := modelExecutionResponseProtocol(entryProtocol, exitProtocol)
	if errMsg := validateNativeInteractionsExecution(entryProtocol, execOptions, routeDecision); errMsg != nil {
		return nil, nil, errMsg
	}
	if routeDecision.ExecutorPluginID != "" {
		return h.executeWithPluginExecutor(ctx, entryProtocol, responseProtocol, modelName, originalRequestedModel, rawJSON, alt, routeDecision.ExecutorPluginID, execOptions)
	}
	providers, normalizedModel, errMsg := h.providersForExecution(modelName, originalRequestedModel, allowImageModel, routeDecision, execOptions)
	if errMsg != nil {
		return nil, nil, errMsg
	}
	providers = adjustExecutionProvidersForEntryProtocol(entryProtocol, providers)
	reqMeta := requestExecutionMetadata(ctx)
	if execOptions.Path != "" {
		reqMeta[coreexecutor.RequestPathMetadataKey] = execOptions.Path
	}
	addCallerScopeMetadata(reqMeta, execOptions.CallerScope)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
	addAuthSelectionModelMetadata(reqMeta, execOptions.AuthSelectionModel)
	addModelExecutionSourceMetadata(reqMeta, execOptions.InternalSource)
	setReasoningEffortMetadata(reqMeta, entryProtocol, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	originalRequest := rawJSON
	if ctxOriginal := originalRequestFromContext(ctx); len(ctxOriginal) > 0 {
		originalRequest = ctxOriginal
	}
	afterAuthCapture := &requestAfterAuthCapture{}
	lifecycle := h.newRequestLifecycleTracker(ctx, entryProtocol, normalizedModel, originalRequestedModel, false, reqMeta, execOptions.SkipInterceptorPluginID)
	opts := coreexecutor.Options{
		Stream:                      false,
		Alt:                         alt,
		OriginalRequest:             originalRequest,
		SourceFormat:                sdktranslator.FromString(entryProtocol),
		ResponseFormat:              sdktranslator.FromString(responseProtocol),
		Headers:                     modelExecutionHeaders(ctx, execOptions.Headers),
		Query:                       modelExecutionQuery(ctx, execOptions.Query),
		RequestAfterAuthInterceptor: h.requestAfterAuthInterceptor(afterAuthCapture, lifecycle.requestID(), execOptions.SkipInterceptorPluginID),
		WebSocketResponseObserver:   h.webSocketResponseObserver(lifecycle.requestID(), execOptions.SkipInterceptorPluginID),
		ProxyURL:                    execOptions.ProxyURL,
	}
	opts.Metadata = reqMeta
	ctx = enrichContextWithSessionHierarchy(ctx, opts.Headers, req.Payload, opts.Metadata)
	var interceptErr *interfaces.ErrorMessage
	req, opts, interceptErr = h.applyRequestInterceptorsBeforeAuth(ctx, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	grant, errGrant := h.authorizeHomeLocalExecution(ctx, modelName, opts, execOptions.SkipRouterPluginID)
	if errGrant != nil {
		lifecycle.completeError(ctx, errGrant)
		return nil, nil, errGrant
	}
	if grant != nil {
		if errMsg := validateNativeInteractionsExecution(entryProtocol, execOptions, modelRouteDecision{ExecutorPluginID: grant.Plugin}); errMsg != nil {
			lifecycle.completeError(ctx, errMsg)
			return nil, nil, errMsg
		}
		grant.request, grant.options, grant.lifecycle = req, opts, lifecycle
		execOptions.homeLocalExecutor = grant
		return h.executeWithPluginExecutor(ctx, entryProtocol, responseProtocol, grant.Model, originalRequestedModel, rawJSON, alt, grant.Plugin, execOptions)
	}
	ctx = coreauth.WithFreshRequestContinuitySession(enrichContextWithSessionHierarchy(ctx, opts.Headers, req.Payload, opts.Metadata))
	resp, err := h.AuthManager.Execute(ctx, providers, req, opts)
	if err != nil {
		err = enrichAuthSelectionError(err, providers, normalizedModel)
		errMsg := executionErrorMessage(err)
		lifecycle.completeError(ctx, errMsg)
		return nil, nil, errMsg
	}
	executedReq, executedOpts := afterAuthCapture.apply(req, opts)
	ctx = enrichContextWithSessionHierarchy(ctx, executedOpts.Headers, executedReq.Payload, executedOpts.Metadata)
	rawResponseHeaders := cloneHeader(resp.Headers)
	passthroughHeaders := executionPassthroughHeaders(h.CurrentConfig(), execOptions.InternalSource)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, passthroughHeaders)
	body, responseHeaders := h.applyResponseInterceptors(ctx, pluginapi.ResponseInterceptKindExecution, lifecycle.requestID(), responseProtocol, normalizedModel, originalRequestedModel, executedOpts, rawResponseHeaders, responseHeaders, executedOpts.OriginalRequest, executedReq.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID, passthroughHeaders)
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
	var routeDecision modelRouteDecision
	if !h.isHomeLocalExecutorModel(modelName) {
		routeDecision = h.applyModelRouter(ctx, handlerType, modelName, rawJSON, false, execOptions)
	}
	if routeDecision.ExecutorPluginID != "" {
		return h.countWithPluginExecutor(ctx, handlerType, modelName, originalRequestedModel, rawJSON, alt, routeDecision.ExecutorPluginID, execOptions)
	}
	providers, normalizedModel, errMsg := h.providersForExecution(modelName, originalRequestedModel, false, routeDecision, execOptions)
	if errMsg != nil {
		return nil, nil, errMsg
	}
	providers = adjustExecutionProvidersForEntryProtocol(handlerType, providers)
	reqMeta := requestExecutionMetadata(ctx)
	if execOptions.Path != "" {
		reqMeta[coreexecutor.RequestPathMetadataKey] = execOptions.Path
	}
	addCallerScopeMetadata(reqMeta, execOptions.CallerScope)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
	addAuthSelectionModelMetadata(reqMeta, execOptions.AuthSelectionModel)
	addModelExecutionSourceMetadata(reqMeta, execOptions.InternalSource)
	setReasoningEffortMetadata(reqMeta, handlerType, normalizedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	req := coreexecutor.Request{
		Model:   normalizedModel,
		Payload: payload,
	}
	originalRequest := rawJSON
	if ctxOriginal := originalRequestFromContext(ctx); len(ctxOriginal) > 0 {
		originalRequest = ctxOriginal
	}
	afterAuthCapture := &requestAfterAuthCapture{}
	lifecycle := h.newRequestLifecycleTracker(ctx, handlerType, normalizedModel, originalRequestedModel, false, reqMeta, execOptions.SkipInterceptorPluginID)
	opts := coreexecutor.Options{
		Stream:                      false,
		Alt:                         alt,
		OriginalRequest:             originalRequest,
		SourceFormat:                sdktranslator.FromString(handlerType),
		Headers:                     modelExecutionHeaders(ctx, execOptions.Headers),
		Query:                       modelExecutionQuery(ctx, execOptions.Query),
		RequestAfterAuthInterceptor: h.requestAfterAuthInterceptor(afterAuthCapture, lifecycle.requestID(), execOptions.SkipInterceptorPluginID),
		WebSocketResponseObserver:   h.webSocketResponseObserver(lifecycle.requestID(), execOptions.SkipInterceptorPluginID),
		ProxyURL:                    execOptions.ProxyURL,
	}
	opts.Metadata = reqMeta
	ctx = enrichContextWithSessionHierarchy(ctx, opts.Headers, req.Payload, opts.Metadata)
	var interceptErr *interfaces.ErrorMessage
	req, opts, interceptErr = h.applyRequestInterceptorsBeforeAuth(ctx, handlerType, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	grant, errGrant := h.authorizeHomeLocalExecution(ctx, modelName, opts, execOptions.SkipRouterPluginID)
	if errGrant != nil {
		lifecycle.completeError(ctx, errGrant)
		return nil, nil, errGrant
	}
	if grant != nil {
		grant.request, grant.options, grant.lifecycle = req, opts, lifecycle
		execOptions.homeLocalExecutor = grant
		return h.countWithPluginExecutor(ctx, handlerType, grant.Model, originalRequestedModel, rawJSON, alt, grant.Plugin, execOptions)
	}
	ctx = enrichContextWithSessionHierarchy(ctx, opts.Headers, req.Payload, opts.Metadata)
	resp, err := h.AuthManager.ExecuteCount(ctx, providers, req, opts)
	if err != nil {
		err = enrichAuthSelectionError(err, providers, normalizedModel)
		errMsg := executionErrorMessage(err)
		lifecycle.completeError(ctx, errMsg)
		return nil, nil, errMsg
	}
	executedReq, executedOpts := afterAuthCapture.apply(req, opts)
	ctx = enrichContextWithSessionHierarchy(ctx, executedOpts.Headers, executedReq.Payload, executedOpts.Metadata)
	rawResponseHeaders := cloneHeader(resp.Headers)
	passthroughHeaders := executionPassthroughHeaders(h.CurrentConfig(), execOptions.InternalSource)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, passthroughHeaders)
	body, responseHeaders := h.applyResponseInterceptors(ctx, pluginapi.ResponseInterceptKindCountTokens, lifecycle.requestID(), handlerType, normalizedModel, originalRequestedModel, executedOpts, rawResponseHeaders, responseHeaders, executedOpts.OriginalRequest, executedReq.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID, passthroughHeaders)
	lifecycle.complete(pluginapi.RequestCompletionSucceeded, http.StatusOK, nil)
	return body, responseHeaders, nil
}

func (h *BaseAPIHandler) executeWithPluginExecutor(ctx context.Context, entryProtocol, responseProtocol, modelName, originalRequestedModel string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	grant := execOptions.homeLocalExecutor
	if h.AuthManager != nil && h.AuthManager.HomeEnabled() && grant == nil {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("plugin executor routing is unavailable while Home is enabled")}
	}
	host := h.pluginExecutorHost()
	if host == nil {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
	}
	if grant != nil {
		ctx = grant.usageContext(ctx, originalRequestedModel)
	}
	execCtx, nestedTracker := withNestedExecutionTracker(coreusage.WithStream(ctx, false))
	req, opts, lifecycle, interceptErr := h.preparePluginExecutorRequest(execCtx, entryProtocol, responseProtocol, modelName, originalRequestedModel, rawJSON, alt, false, execOptions)
	if interceptErr != nil {
		lifecycle.completeError(execCtx, interceptErr)
		return nil, nil, interceptErr
	}
	req, opts, interceptErr = h.applyRequestInterceptorsAfterPluginExecutorRoute(execCtx, host, executorPluginID, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(execCtx, interceptErr)
		return nil, nil, interceptErr
	}
	execCtx = enrichContextWithSessionHierarchy(execCtx, opts.Headers, req.Payload, opts.Metadata)
	if grant != nil {
		execCtx = grant.prepare(execCtx, &req, originalRequestedModel)
	}
	var reporter *helps.UsageReporter
	if !execOptions.InternalSource {
		reporter = helps.NewUsageReporter(execCtx, executorPluginID, modelName, nil)
		reporter.ApplyPluginExecutorAttribution(executorPluginID, nil)
		reporter.SetTranslatedReasoningEffort(req.Payload, entryProtocol)
		reporter.StartResponseTTFT()
	}
	var (
		resp        coreexecutor.Response
		attribution *pluginapi.UsageAttribution
		errExecute  error
	)
	if attrHost, ok := host.(pluginExecutorAttributionHost); ok && attrHost != nil {
		resp, attribution, errExecute = attrHost.ExecutePluginExecutorWithAttribution(execCtx, executorPluginID, req, opts)
	} else {
		resp, errExecute = host.ExecutePluginExecutor(execCtx, executorPluginID, req, opts)
	}
	if errExecute != nil {
		if reporter != nil && !nestedTracker.hasNestedExecution() {
			reporter.PublishFailure(execCtx, errExecute)
		}
		errMsg := executionErrorMessage(errExecute)
		lifecycle.completeError(execCtx, errMsg)
		return nil, nil, errMsg
	}
	if reporter != nil && !nestedTracker.hasNestedExecution() {
		reporter.ApplyPluginExecutorAttribution(executorPluginID, attribution)
		reporter.RecordFirstPacket()
		detail := parsePluginExecutorResponseUsage(responseProtocol, resp.Payload)
		reporter.Publish(execCtx, detail)
		reporter.EnsurePublished(execCtx)
	}
	rawResponseHeaders := cloneHeader(resp.Headers)
	passthroughHeaders := executionPassthroughHeaders(h.CurrentConfig(), execOptions.InternalSource)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, passthroughHeaders)
	body, responseHeaders := h.applyResponseInterceptors(execCtx, pluginapi.ResponseInterceptKindExecution, lifecycle.requestID(), responseProtocol, modelName, originalRequestedModel, opts, rawResponseHeaders, responseHeaders, opts.OriginalRequest, req.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID, passthroughHeaders)
	lifecycle.complete(pluginapi.RequestCompletionSucceeded, http.StatusOK, nil)
	return body, responseHeaders, nil
}

func (h *BaseAPIHandler) countWithPluginExecutor(ctx context.Context, handlerType, modelName, originalRequestedModel string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) ([]byte, http.Header, *interfaces.ErrorMessage) {
	grant := execOptions.homeLocalExecutor
	if h.AuthManager != nil && h.AuthManager.HomeEnabled() && grant == nil {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("plugin executor routing is unavailable while Home is enabled")}
	}
	host := h.pluginExecutorHost()
	if host == nil {
		return nil, nil, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
	}
	if grant != nil {
		ctx = grant.usageContext(ctx, originalRequestedModel)
	}
	req, opts, lifecycle, interceptErr := h.preparePluginExecutorRequest(ctx, handlerType, handlerType, modelName, originalRequestedModel, rawJSON, alt, false, execOptions)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	req, opts, interceptErr = h.applyRequestInterceptorsAfterPluginExecutorRoute(ctx, host, executorPluginID, handlerType, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	if interceptErr != nil {
		lifecycle.completeError(ctx, interceptErr)
		return nil, nil, interceptErr
	}
	ctx = enrichContextWithSessionHierarchy(ctx, opts.Headers, req.Payload, opts.Metadata)
	if grant != nil {
		ctx = grant.prepare(ctx, &req, originalRequestedModel)
	}
	resp, errCount := host.CountPluginExecutor(ctx, executorPluginID, req, opts)
	if errCount != nil {
		errMsg := executionErrorMessage(errCount)
		lifecycle.completeError(ctx, errMsg)
		return nil, nil, errMsg
	}
	rawResponseHeaders := cloneHeader(resp.Headers)
	passthroughHeaders := executionPassthroughHeaders(h.CurrentConfig(), execOptions.InternalSource)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, passthroughHeaders)
	body, responseHeaders := h.applyResponseInterceptors(ctx, pluginapi.ResponseInterceptKindCountTokens, lifecycle.requestID(), handlerType, modelName, originalRequestedModel, opts, rawResponseHeaders, responseHeaders, opts.OriginalRequest, req.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID, passthroughHeaders)
	lifecycle.complete(pluginapi.RequestCompletionSucceeded, http.StatusOK, nil)
	return body, responseHeaders, nil
}

func (h *BaseAPIHandler) preparePluginExecutorRequest(ctx context.Context, entryProtocol, responseProtocol, modelName, originalRequestedModel string, rawJSON []byte, alt string, stream bool, execOptions modelExecutionOptions) (coreexecutor.Request, coreexecutor.Options, *requestLifecycleTracker, *interfaces.ErrorMessage) {
	if grant := execOptions.homeLocalExecutor; grant != nil {
		req := grant.request
		req.Model = modelName
		opts := grant.options
		opts.ResponseFormat = sdktranslator.FromString(responseProtocol)
		grant.lifecycle.completion.Model = modelName
		return req, opts, grant.lifecycle, nil
	}
	reqMeta := requestExecutionMetadata(ctx)
	if execOptions.Path != "" {
		reqMeta[coreexecutor.RequestPathMetadataKey] = execOptions.Path
	}
	addCallerScopeMetadata(reqMeta, execOptions.CallerScope)
	reqMeta[coreexecutor.RequestedModelMetadataKey] = originalRequestedModel
	addAuthSelectionModelMetadata(reqMeta, execOptions.AuthSelectionModel)
	addModelExecutionSourceMetadata(reqMeta, execOptions.InternalSource)
	setReasoningEffortMetadata(reqMeta, entryProtocol, originalRequestedModel, rawJSON)
	setServiceTierMetadata(reqMeta, rawJSON)
	setGenerateMetadata(reqMeta, rawJSON)
	payload := rawJSON
	if len(payload) == 0 {
		payload = nil
	}
	originalRequest := rawJSON
	if ctxOriginal := originalRequestFromContext(ctx); len(ctxOriginal) > 0 {
		originalRequest = ctxOriginal
	}
	req := coreexecutor.Request{Model: modelName, Payload: payload}
	opts := coreexecutor.Options{
		Stream:                    stream,
		Alt:                       alt,
		OriginalRequest:           originalRequest,
		SourceFormat:              sdktranslator.FromString(entryProtocol),
		ResponseFormat:            sdktranslator.FromString(responseProtocol),
		Headers:                   modelExecutionHeaders(ctx, execOptions.Headers),
		Query:                     modelExecutionQuery(ctx, execOptions.Query),
		WebSocketResponseObserver: h.webSocketResponseObserver("", execOptions.SkipInterceptorPluginID),
		Metadata:                  reqMeta,
		ProxyURL:                  execOptions.ProxyURL,
	}
	lifecycle := h.newRequestLifecycleTracker(ctx, entryProtocol, modelName, originalRequestedModel, stream, opts.Metadata, execOptions.SkipInterceptorPluginID)
	req, opts, interceptErr := h.applyRequestInterceptorsBeforeAuth(ctx, entryProtocol, originalRequestedModel, lifecycle.requestID(), req, opts, execOptions.SkipInterceptorPluginID)
	return req, opts, lifecycle, interceptErr
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
		Body:           req.Payload,
		Metadata:       opts.Metadata,
	}, requestID, skipPluginID)
	opts.Headers = mergeRequestInterceptorHeaders(opts.Headers, resp.Headers, resp.ClearHeaders)
	if len(resp.Body) > 0 {
		req.Payload = resp.Body
		opts.OriginalRequest = resp.Body
	}
	if path := strings.TrimSpace(resp.Path); path != "" {
		if opts.Metadata == nil {
			opts.Metadata = make(map[string]any, 1)
		} else {
			opts.Metadata = maps.Clone(opts.Metadata)
		}
		opts.Metadata[coreexecutor.RequestPathMetadataKey] = path
	}
	if resp.Terminate {
		return req, opts, directTerminationError(resp.StatusCode, resp.ResponseHeaders, resp.ResponseBody)
	}
	return req, opts, nil
}

func ExecutionErrorMessage(err error) *interfaces.ErrorMessage {
	return executionErrorMessage(err)
}

func stripOverlappingHeaders(addon, primary http.Header) http.Header {
	if len(addon) == 0 {
		return nil
	}
	if len(primary) == 0 {
		return addon
	}
	for primaryKey := range primary {
		addon.Del(primaryKey)
		for addonKey := range addon {
			if strings.EqualFold(addonKey, primaryKey) {
				delete(addon, addonKey)
			}
		}
	}
	if len(addon) == 0 {
		return nil
	}
	return addon
}

func executionErrorMessage(err error) *interfaces.ErrorMessage {
	var addon http.Header
	var he interface{ Headers() http.Header }
	if errors.As(err, &he) && he != nil {
		if hdr := he.Headers(); hdr != nil {
			addon = hdr.Clone()
		}
	}
	var terminated *coreexecutor.RequestTerminatedError
	if errors.As(err, &terminated) && terminated != nil {
		termHeaders := terminated.ResponseHeaders()
		return &interfaces.ErrorMessage{
			StatusCode:     normalizedTerminationStatus(terminated.StatusCode()),
			Error:          err,
			DirectResponse: true,
			Body:           terminated.ResponseBody(),
			Headers:        termHeaders,
			Addon:          stripOverlappingHeaders(addon, termHeaders),
		}
	}
	status := http.StatusInternalServerError
	if code := clienterror.HTTPStatusFromError(err); code > 0 {
		status = code
	}
	type directResponseError interface {
		DirectResponse() bool
		ResponseBody() []byte
	}
	var direct directResponseError
	if errors.As(err, &direct) && direct != nil && direct.DirectResponse() {
		body := direct.ResponseBody()
		var headers http.Header
		if len(body) > 0 {
			contentType := http.DetectContentType(body)
			if json.Valid(body) {
				contentType = "application/json"
			}
			headers = http.Header{"Content-Type": []string{contentType}}
		}
		return &interfaces.ErrorMessage{
			StatusCode:     status,
			Error:          err,
			DirectResponse: true,
			Body:           body,
			Headers:        headers,
			Addon:          stripOverlappingHeaders(addon, headers),
		}
	}
	return &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
}

func (h *BaseAPIHandler) pluginExecutorHost() PluginExecutorHost {
	if h == nil {
		return nil
	}
	h.runtimeMu.RLock()
	routerHost := h.ModelRouterHost
	pluginHost := h.PluginHost
	h.runtimeMu.RUnlock()
	if !isNilPluginModelRouterHost(routerHost) {
		if executorHost, ok := routerHost.(PluginExecutorHost); ok && executorHost != nil {
			return executorHost
		}
	}
	if !isNilPluginInterceptorHost(pluginHost) {
		if executorHost, ok := pluginHost.(PluginExecutorHost); ok && executorHost != nil {
			return executorHost
		}
	}
	return nil
}
