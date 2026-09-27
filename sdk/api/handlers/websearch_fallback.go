package handlers

import (
	"context"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/websearch"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// websearchConfigFor resolves the proxy-side search configuration. The
// explicit search proxy wins, then the request proxy, then the global one.
// A zero config means search is off; the returned bool reports whether any
// provider in the chain could actually run a search.
func (h *BaseAPIHandler) websearchConfigFor(opts coreexecutor.Options, providers []string) (websearch.Config, bool) {
	if h == nil || h.Cfg == nil || !h.Cfg.WebSearch.Enabled {
		return websearch.Config{}, false
	}
	cfg := h.Cfg.WebSearch
	if strings.TrimSpace(cfg.ProxyURL) == "" {
		if strings.TrimSpace(opts.ProxyURL) != "" {
			cfg.ProxyURL = opts.ProxyURL
		} else {
			cfg.ProxyURL = h.Cfg.ProxyURL
		}
	}
	// An operator who logged in to a model provider gets grounded search
	// from that same session, so search does not silently degrade to
	// keyless providers just because no search key was configured.
	applyBorrowedSearchCredentials(&cfg, h.AuthManager, providers)
	return cfg, true
}

// runWebSearchLoop runs the proxy-side search fallback: server search tools
// are rewritten to function calls, each model-issued search runs through the
// configured provider chain, and results feed back until the model answers.
func (h *BaseAPIHandler) runWebSearchLoop(ctx context.Context, providers []string, req coreexecutor.Request, opts coreexecutor.Options, searchCfg websearch.Config) (coreexecutor.Response, error) {
	var last coreexecutor.Response
	exec := func(execCtx context.Context, payload []byte) ([]byte, error) {
		execCtx = enrichContextWithSessionHierarchy(execCtx, opts.Headers, payload, opts.Metadata)
		attempt := req
		attempt.Payload = payload
		resp, errExec := h.AuthManager.Execute(execCtx, providers, attempt, opts)
		if errExec != nil {
			return nil, errExec
		}
		last = resp
		return resp.Payload, nil
	}
	body, errRun := websearch.Run(ctx, searchCfg, string(opts.SourceFormat), req.Payload, exec)
	if errRun != nil {
		return coreexecutor.Response{}, errRun
	}
	last.Payload = body
	return last, nil
}

// streamWithWebSearchFallback serves a streaming request through the
// non-streaming search loop, then re-emits the final response as downstream
// wire chunks. Headers are uncommitted when this runs, so the synthesized
// stream is indistinguishable from a live one.
func (h *BaseAPIHandler) streamWithWebSearchFallback(ctx context.Context, responseProtocol, normalizedModel, originalRequestedModel string, providers []string, req coreexecutor.Request, opts coreexecutor.Options, searchCfg websearch.Config, afterAuthCapture *requestAfterAuthCapture, lifecycle *requestLifecycleTracker, execOptions modelExecutionOptions) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	fail := func(msg *interfaces.ErrorMessage) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		lifecycle.completeError(ctx, msg)
		errChan := make(chan *interfaces.ErrorMessage, 1)
		errChan <- msg
		close(errChan)
		return nil, nil, errChan
	}
	nonStreamOpts := opts
	nonStreamOpts.Stream = false
	resp, errExec := h.runWebSearchLoop(ctx, providers, req, nonStreamOpts, searchCfg)
	if errExec != nil {
		errExec = enrichAuthSelectionError(errExec, providers, normalizedModel)
		return fail(executionErrorMessage(errExec))
	}
	executedReq, executedOpts := afterAuthCapture.apply(req, nonStreamOpts)
	ctx = enrichContextWithSessionHierarchy(ctx, executedOpts.Headers, executedReq.Payload, executedOpts.Metadata)
	rawResponseHeaders := cloneHeader(resp.Headers)
	passthroughHeaders := executionPassthroughHeaders(h.Cfg, execOptions.InternalSource)
	responseHeaders := downstreamHeadersFromExecutor(rawResponseHeaders, passthroughHeaders)
	body, responseHeaders := h.applyResponseInterceptors(ctx, lifecycle.requestID(), responseProtocol, normalizedModel, originalRequestedModel, executedOpts, rawResponseHeaders, responseHeaders, executedOpts.OriginalRequest, executedReq.Payload, resp.Payload, http.StatusOK, execOptions.SkipInterceptorPluginID, passthroughHeaders)
	chunks, errSynth := websearch.SynthesizeStream(string(opts.ResponseFormat), normalizedModel, body)
	if errSynth != nil {
		return fail(&interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errSynth})
	}
	dataChan := make(chan []byte)
	errChan := make(chan *interfaces.ErrorMessage, 1)
	go func() {
		completionOutcome := pluginapi.RequestCompletionSucceeded
		completionStatus := http.StatusOK
		var completionErr error
		defer func() { lifecycle.complete(completionOutcome, completionStatus, completionErr) }()
		defer close(dataChan)
		defer close(errChan)
		for _, chunk := range chunks {
			if ctx == nil {
				dataChan <- chunk
				continue
			}
			select {
			case <-ctx.Done():
				completionOutcome = pluginapi.RequestCompletionCanceled
				completionStatus = 0
				completionErr = ctx.Err()
				return
			case dataChan <- chunk:
			}
		}
	}()
	return dataChan, responseHeaders, errChan
}
