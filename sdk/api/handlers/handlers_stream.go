package handlers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"golang.org/x/net/context"
)

// ExecuteStreamWithAuthManager executes a streaming request via the core auth manager.
// This path is the only supported execution route.
// The returned http.Header carries upstream response headers captured before streaming begins.
func (h *BaseAPIHandler) ExecuteStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, false)
}

// ExecuteImageStreamWithAuthManager executes a streaming OpenAI-compatible image endpoint request.
func (h *BaseAPIHandler) ExecuteImageStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManager(ctx, handlerType, modelName, rawJSON, alt, true)
}

func (h *BaseAPIHandler) streamWithPluginExecutor(ctx context.Context, entryProtocol, responseProtocol, modelName, originalRequestedModel string, rawJSON []byte, alt, executorPluginID string, execOptions modelExecutionOptions) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	if h.AuthManager != nil && h.AuthManager.HomeEnabled() {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusServiceUnavailable, Error: fmt.Errorf("plugin executor routing is unavailable while Home is enabled")}
		close(errChan)
		return nil, nil, errChan
	}
	host := h.pluginExecutorHost()
	if host == nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor host is unavailable")}
		close(errChan)
		return nil, nil, errChan
	}
	req, opts := h.pluginExecutorRequest(ctx, entryProtocol, responseProtocol, modelName, originalRequestedModel, rawJSON, alt, true, execOptions)
	req, opts = h.applyRequestInterceptorsBeforeAuth(ctx, entryProtocol, originalRequestedModel, req, opts, execOptions.SkipInterceptorPluginID)
	req, opts = h.applyRequestInterceptorsAfterPluginExecutorRoute(ctx, host, executorPluginID, entryProtocol, originalRequestedModel, req, opts, execOptions.SkipInterceptorPluginID)
	streamResult, errStream := host.ExecutePluginExecutorStream(ctx, executorPluginID, req, opts)
	if errStream != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- executionErrorMessage(errStream)
		close(errChan)
		return nil, nil, errChan
	}
	if streamResult == nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("plugin executor returned nil stream")}
		close(errChan)
		return nil, nil, errChan
	}

	passthroughHeadersEnabled := PassthroughHeadersEnabled(h.Cfg)
	interceptorHost := h.interceptorHost()
	streamInterceptorsActive := streamInterceptorsEnabled(interceptorHost)
	rawStreamHeaders := cloneHeader(streamResult.Headers)
	baseStreamHeaders := cloneHeader(streamResult.Headers)
	applyStreamHeaders := func(headers http.Header) {
		rawStreamHeaders = finalInterceptorHeaders(rawStreamHeaders, headers)
	}
	if streamInterceptorsActive {
		intercepted := interceptStreamChunk(ctx, interceptorHost, pluginapi.StreamChunkInterceptRequest{
			SourceFormat:    responseProtocol,
			Model:           modelName,
			RequestedModel:  originalRequestedModel,
			RequestHeaders:  cloneHeader(opts.Headers),
			ResponseHeaders: cloneHeader(rawStreamHeaders),
			OriginalRequest: cloneBytes(opts.OriginalRequest),
			RequestBody:     cloneBytes(req.Payload),
			ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
			Metadata:        opts.Metadata,
		}, execOptions.SkipInterceptorPluginID)
		applyStreamHeaders(intercepted.Headers)
	}
	upstreamHeaders := downstreamHeadersAfterInterceptors(baseStreamHeaders, rawStreamHeaders, passthroughHeadersEnabled)
	if upstreamHeaders == nil && (passthroughHeadersEnabled || streamInterceptorsActive) {
		upstreamHeaders = make(http.Header)
	}

	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	chunks := streamResult.Chunks
	if chunks == nil {
		closed := make(chan coreexecutor.StreamChunk)
		close(closed)
		chunks = closed
	}
	go func() {
		defer close(dataChan)
		defer close(errChan)
		chunkIndex := 0
		var historyChunks [][]byte
		for {
			chunk, ok, canceled := nextStreamChunk(ctx, nil, nil, chunks)
			if canceled {
				return
			}
			if !ok {
				return
			}
			if chunk.Err != nil {
				select {
				case errChan <- executionErrorMessage(chunk.Err):
				case <-done:
				}
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			payload := cloneBytes(chunk.Payload)
			if streamInterceptorsActive {
				intercepted := interceptStreamChunk(ctx, interceptorHost, pluginapi.StreamChunkInterceptRequest{
					SourceFormat:    responseProtocol,
					Model:           modelName,
					RequestedModel:  originalRequestedModel,
					RequestHeaders:  cloneHeader(opts.Headers),
					ResponseHeaders: cloneHeader(rawStreamHeaders),
					OriginalRequest: cloneBytes(opts.OriginalRequest),
					RequestBody:     cloneBytes(req.Payload),
					Body:            payload,
					HistoryChunks:   cloneByteSlices(historyChunks),
					ChunkIndex:      chunkIndex,
					Metadata:        opts.Metadata,
				}, execOptions.SkipInterceptorPluginID)
				applyStreamHeaders(intercepted.Headers)
				if len(intercepted.Body) > 0 {
					payload = cloneBytes(intercepted.Body)
				}
				chunkIndex++
				if intercepted.DropChunk {
					continue
				}
			} else {
				chunkIndex++
			}
			if responseProtocol == "openai-response" {
				if errValidate := validateSSEDataJSON(payload); errValidate != nil {
					select {
					case errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errValidate}:
					case <-done:
					}
					return
				}
			}
			select {
			case dataChan <- payload:
				if streamInterceptorsActive {
					historyChunks = appendStreamInterceptorHistory(historyChunks, payload)
				}
			case <-done:
				return
			}
		}
	}()
	return dataChan, upstreamHeaders, errChan
}

func (h *BaseAPIHandler) executeStreamWithAuthManager(ctx context.Context, handlerType, modelName string, rawJSON []byte, alt string, allowImageModel bool) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	return h.executeStreamWithAuthManagerFormats(ctx, handlerType, handlerType, modelName, rawJSON, alt, allowImageModel, modelExecutionOptions{})
}

func (h *BaseAPIHandler) executeStreamWithAuthManagerFormats(ctx context.Context, entryProtocol, exitProtocol, modelName string, rawJSON []byte, alt string, allowImageModel bool, execOptions modelExecutionOptions) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	originalRequestedModel := modelName
	routeDecision, preparedRoute := preparedModelRouteFromContext(ctx)
	if !preparedRoute {
		routeDecision = h.applyModelRouter(ctx, entryProtocol, modelName, rawJSON, true, execOptions)
	}
	responseProtocol := modelExecutionResponseProtocol(entryProtocol, exitProtocol)
	if errMsg := validateNativeInteractionsExecution(entryProtocol, execOptions, routeDecision); errMsg != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, nil, errChan
	}
	if routeDecision.ExecutorPluginID != "" {
		return h.streamWithPluginExecutor(ctx, entryProtocol, responseProtocol, modelName, originalRequestedModel, rawJSON, alt, routeDecision.ExecutorPluginID, execOptions)
	}
	providers, normalizedModel, errMsg := h.providersForExecution(modelName, originalRequestedModel, allowImageModel, routeDecision, execOptions)
	if errMsg != nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- errMsg
		close(errChan)
		return nil, nil, errChan
	}
	providers = adjustExecutionProvidersForEntryProtocol(entryProtocol, providers)
	reqMeta := requestExecutionMetadata(ctx)
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
	afterAuthCapture := &requestAfterAuthCapture{}
	opts := coreexecutor.Options{
		Stream:                      true,
		Alt:                         alt,
		OriginalRequest:             rawJSON,
		SourceFormat:                sdktranslator.FromString(entryProtocol),
		ResponseFormat:              sdktranslator.FromString(responseProtocol),
		Headers:                     modelExecutionHeaders(ctx, execOptions.Headers),
		Query:                       modelExecutionQuery(ctx, execOptions.Query),
		RequestAfterAuthInterceptor: h.requestAfterAuthInterceptor(afterAuthCapture, execOptions.SkipInterceptorPluginID),
	}
	opts.Metadata = reqMeta
	req, opts = h.applyRequestInterceptorsBeforeAuth(ctx, entryProtocol, originalRequestedModel, req, opts, execOptions.SkipInterceptorPluginID)
	streamResult, err := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
	if err != nil {
		err = enrichAuthSelectionError(err, providers, normalizedModel)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		status := http.StatusInternalServerError
		if se, ok := err.(interface{ StatusCode() int }); ok && se != nil {
			if code := se.StatusCode(); code > 0 {
				status = code
			}
		}
		var addon http.Header
		if he, ok := err.(interface{ Headers() http.Header }); ok && he != nil {
			if hdr := he.Headers(); hdr != nil {
				addon = hdr.Clone()
			}
		}
		errChan <- &interfaces.ErrorMessage{StatusCode: status, Error: err, Addon: addon}
		close(errChan)
		return nil, nil, errChan
	}
	if streamResult == nil {
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: fmt.Errorf("auth manager returned nil stream")}
		close(errChan)
		return nil, nil, errChan
	}
	executedRequest := func() (coreexecutor.Request, coreexecutor.Options) {
		return afterAuthCapture.apply(req, opts)
	}
	passthroughHeadersEnabled := PassthroughHeadersEnabled(h.Cfg)
	interceptorHost := h.interceptorHost()
	streamInterceptorsActive := streamInterceptorsEnabled(interceptorHost)
	// Resolve bootstrap retries and header initialization before returning so the
	// returned header snapshot is never modified by the stream goroutine.
	rawStreamHeaders := cloneHeader(streamResult.Headers)
	baseStreamHeaders := cloneHeader(streamResult.Headers)
	chunks := streamResult.Chunks
	if chunks == nil {
		closed := make(chan coreexecutor.StreamChunk)
		close(closed)
		chunks = closed
	}
	streamClosedBeforeRead := false
	streamCanceledBeforeRead := false
	streamHeaderInitialized := false

	applyStreamHeaders := func(headers http.Header) {
		rawStreamHeaders = finalInterceptorHeaders(rawStreamHeaders, headers)
	}

	applyStreamHeaderInit := func() {
		if !streamInterceptorsActive || streamHeaderInitialized {
			return
		}
		executedReq, executedOpts := executedRequest()
		intercepted := interceptStreamChunk(ctx, interceptorHost, pluginapi.StreamChunkInterceptRequest{
			SourceFormat:    responseProtocol,
			Model:           normalizedModel,
			RequestedModel:  originalRequestedModel,
			RequestHeaders:  cloneHeader(executedOpts.Headers),
			ResponseHeaders: cloneHeader(rawStreamHeaders),
			OriginalRequest: cloneBytes(executedOpts.OriginalRequest),
			RequestBody:     cloneBytes(executedReq.Payload),
			ChunkIndex:      pluginapi.StreamChunkHeaderInitIndex,
			Metadata:        executedOpts.Metadata,
		}, execOptions.SkipInterceptorPluginID)
		applyStreamHeaders(intercepted.Headers)
		streamHeaderInitialized = true
	}

	transformStreamPayload := func(payload []byte, chunkIndex *int, historyChunks [][]byte) ([]byte, bool, *interfaces.ErrorMessage) {
		applyStreamHeaderInit()
		payload = cloneBytes(payload)
		if streamInterceptorsActive {
			executedReq, executedOpts := executedRequest()
			intercepted := interceptStreamChunk(ctx, interceptorHost, pluginapi.StreamChunkInterceptRequest{
				SourceFormat:    responseProtocol,
				Model:           normalizedModel,
				RequestedModel:  originalRequestedModel,
				RequestHeaders:  cloneHeader(executedOpts.Headers),
				ResponseHeaders: cloneHeader(rawStreamHeaders),
				OriginalRequest: cloneBytes(executedOpts.OriginalRequest),
				RequestBody:     cloneBytes(executedReq.Payload),
				Body:            payload,
				HistoryChunks:   cloneByteSlices(historyChunks),
				ChunkIndex:      *chunkIndex,
				Metadata:        executedOpts.Metadata,
			}, execOptions.SkipInterceptorPluginID)
			applyStreamHeaders(intercepted.Headers)
			if len(intercepted.Body) > 0 {
				payload = cloneBytes(intercepted.Body)
			}
			(*chunkIndex)++
			if intercepted.DropChunk {
				return nil, false, nil
			}
		} else {
			(*chunkIndex)++
		}
		if responseProtocol == "openai-response" {
			if errValidate := validateSSEDataJSON(payload); errValidate != nil {
				return nil, false, &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errValidate}
			}
		}
		return payload, true, nil
	}

	var bootstrapPayload []byte
	bootstrapChunkIndex := 0
	var bootstrapHistoryChunks [][]byte
	var bootstrapStreamErr error
	var bootstrapErr *interfaces.ErrorMessage
	readInitialStreamChunks := func() {
		for {
			var chunk coreexecutor.StreamChunk
			var ok bool
			if ctx != nil {
				select {
				case <-ctx.Done():
					streamCanceledBeforeRead = true
					return
				case chunk, ok = <-chunks:
				}
			} else {
				chunk, ok = <-chunks
			}
			if !ok {
				streamClosedBeforeRead = true
				applyStreamHeaderInit()
				return
			}
			if chunk.Err != nil {
				bootstrapStreamErr = chunk.Err
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			payload, deliverable, errMsg := transformStreamPayload(chunk.Payload, &bootstrapChunkIndex, bootstrapHistoryChunks)
			if errMsg != nil {
				bootstrapErr = errMsg
				return
			}
			if !deliverable {
				continue
			}
			bootstrapPayload = payload
			return
		}
	}

	bootstrapEligible := func(err error) bool {
		status := statusFromError(err)
		if status == 0 {
			return true
		}
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusPaymentRequired,
			http.StatusRequestTimeout, http.StatusTooManyRequests:
			return true
		default:
			return status >= http.StatusInternalServerError
		}
	}

	maxBootstrapRetries := StreamingBootstrapRetries(h.Cfg)
	if h.AuthManager.HomeEnabled() {
		maxBootstrapRetries = 0
	}
	for bootstrapRetries := 0; !streamCanceledBeforeRead; {
		readInitialStreamChunks()
		if streamCanceledBeforeRead || bootstrapErr != nil || bootstrapStreamErr == nil {
			break
		}
		if bootstrapRetries >= maxBootstrapRetries || !bootstrapEligible(bootstrapStreamErr) {
			bootstrapErr = executionErrorMessage(bootstrapStreamErr)
			break
		}
		bootstrapRetries++
		retryResult, retryErr := h.AuthManager.ExecuteStream(ctx, providers, req, opts)
		if retryErr != nil {
			bootstrapErr = executionErrorMessage(enrichAuthSelectionError(retryErr, providers, normalizedModel))
			break
		}
		if retryResult == nil {
			bootstrapErr = executionErrorMessage(fmt.Errorf("auth manager returned nil stream"))
			break
		}
		rawStreamHeaders = cloneHeader(retryResult.Headers)
		baseStreamHeaders = cloneHeader(retryResult.Headers)
		streamHeaderInitialized = false
		streamClosedBeforeRead = false
		bootstrapStreamErr = nil
		bootstrapPayload = nil
		bootstrapChunkIndex = 0
		bootstrapHistoryChunks = nil
		chunks = retryResult.Chunks
		if chunks == nil {
			closed := make(chan coreexecutor.StreamChunk)
			close(closed)
			chunks = closed
		}
	}

	upstreamHeaders := downstreamHeadersAfterInterceptors(baseStreamHeaders, rawStreamHeaders, passthroughHeadersEnabled)
	if upstreamHeaders == nil && (passthroughHeadersEnabled || streamInterceptorsActive) {
		upstreamHeaders = make(http.Header)
	}
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)

	go func() {
		defer close(dataChan)
		defer close(errChan)
		if streamCanceledBeforeRead {
			return
		}

		sendErr := func(msg *interfaces.ErrorMessage) bool {
			if ctx == nil {
				errChan <- msg
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case errChan <- msg:
				return true
			}
		}

		sendData := func(chunk []byte) bool {
			if ctx == nil {
				dataChan <- chunk
				return true
			}
			select {
			case <-ctx.Done():
				return false
			case dataChan <- chunk:
				return true
			}
		}

		if bootstrapErr != nil {
			_ = sendErr(bootstrapErr)
			return
		}

		chunkIndex := bootstrapChunkIndex
		historyChunks := bootstrapHistoryChunks
		if bootstrapPayload != nil {
			if okSendData := sendData(bootstrapPayload); !okSendData {
				return
			}
			if streamInterceptorsActive {
				historyChunks = appendStreamInterceptorHistory(historyChunks, bootstrapPayload)
			}
		}
		for {
			chunk, ok, canceled := nextStreamChunk(ctx, nil, &streamClosedBeforeRead, chunks)
			if canceled || !ok {
				return
			}
			if chunk.Err != nil {
				_ = sendErr(executionErrorMessage(chunk.Err))
				return
			}
			if len(chunk.Payload) == 0 {
				continue
			}
			payload, deliverable, errMsg := transformStreamPayload(chunk.Payload, &chunkIndex, historyChunks)
			if errMsg != nil {
				_ = sendErr(errMsg)
				return
			}
			if !deliverable {
				continue
			}
			if okSendData := sendData(payload); !okSendData {
				return
			}
			if streamInterceptorsActive {
				historyChunks = appendStreamInterceptorHistory(historyChunks, payload)
			}
		}
	}()
	return dataChan, upstreamHeaders, errChan
}

func validateSSEDataJSON(chunk []byte) error {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[5:])
		if len(data) == 0 {
			continue
		}
		if bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if json.Valid(data) {
			continue
		}
		const max = 512
		preview := data
		if len(preview) > max {
			preview = preview[:max]
		}
		return fmt.Errorf("invalid SSE data JSON (len=%d): %q", len(data), preview)
	}
	return nil
}
