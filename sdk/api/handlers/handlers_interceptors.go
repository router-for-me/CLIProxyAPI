package handlers

import (
	"net/http"
	"sync"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"golang.org/x/net/context"
)

// PluginInterceptorHost applies plugin interceptors around handler execution.
type PluginInterceptorHost interface {
	InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse
	InterceptRequestAfterAuth(context.Context, pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse
	InterceptResponse(context.Context, pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse
	InterceptStreamChunk(context.Context, pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse
}

type pluginInterceptorSkipHost interface {
	InterceptRequestBeforeAuthExcept(context.Context, pluginapi.RequestInterceptRequest, string) pluginapi.RequestInterceptResponse
	InterceptRequestAfterAuthExcept(context.Context, pluginapi.RequestInterceptRequest, string) pluginapi.RequestInterceptResponse
	InterceptResponseExcept(context.Context, pluginapi.ResponseInterceptRequest, string) pluginapi.ResponseInterceptResponse
	InterceptStreamChunkExcept(context.Context, pluginapi.StreamChunkInterceptRequest, string) pluginapi.StreamChunkInterceptResponse
}

type streamInterceptorDetector interface {
	HasStreamInterceptors() bool
}

type requestInterceptorDetector interface {
	HasRequestInterceptors() bool
}

func cloneHeader(src http.Header) http.Header {
	if src == nil {
		return nil
	}
	dst := make(http.Header, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func cloneByteSlices(src [][]byte) [][]byte {
	if len(src) == 0 {
		return nil
	}
	dst := make([][]byte, 0, len(src))
	for _, item := range src {
		dst = append(dst, cloneBytes(item))
	}
	return dst
}

func nextStreamChunk(ctx context.Context, pending *[]coreexecutor.StreamChunk, closed *bool, chunks <-chan coreexecutor.StreamChunk) (coreexecutor.StreamChunk, bool, bool) {
	if pending != nil && len(*pending) > 0 {
		chunk := (*pending)[0]
		(*pending)[0] = coreexecutor.StreamChunk{}
		*pending = (*pending)[1:]
		return chunk, true, false
	}
	if closed != nil && *closed {
		return coreexecutor.StreamChunk{}, false, false
	}
	var chunk coreexecutor.StreamChunk
	var ok bool
	if ctx != nil {
		select {
		case <-ctx.Done():
			return coreexecutor.StreamChunk{}, false, true
		case chunk, ok = <-chunks:
		}
	} else {
		chunk, ok = <-chunks
	}
	if !ok && closed != nil {
		*closed = true
	}
	return chunk, ok, false
}

func appendStreamInterceptorHistory(history [][]byte, chunk []byte) [][]byte {
	if len(chunk) == 0 {
		return history
	}
	history = append(history, cloneBytes(chunk))
	for len(history) > maxStreamInterceptorHistoryChunks || byteSlicesSize(history) > maxStreamInterceptorHistoryBytes {
		history[0] = nil
		history = history[1:]
	}
	if len(history) == 0 {
		return nil
	}
	return history
}

func byteSlicesSize(items [][]byte) int {
	total := 0
	for _, item := range items {
		total += len(item)
	}
	return total
}

func finalInterceptorHeaders(current, intercepted http.Header) http.Header {
	if intercepted == nil {
		return current
	}
	if len(intercepted) == 0 {
		return nil
	}
	return cloneHeader(intercepted)
}

func downstreamHeadersFromExecutor(headers http.Header, passthrough bool) http.Header {
	if !passthrough {
		return nil
	}
	return FilterUpstreamHeaders(headers)
}

func downstreamHeadersAfterInterceptors(baseRaw, finalRaw http.Header, passthrough bool) http.Header {
	if passthrough {
		return FilterUpstreamHeaders(finalRaw)
	}
	return FilterUpstreamHeaders(diffHeaders(baseRaw, finalRaw))
}

func diffHeaders(base, next http.Header) http.Header {
	if len(next) == 0 {
		return nil
	}
	baseValues := make(map[string][]string, len(base))
	for key, values := range base {
		baseValues[http.CanonicalHeaderKey(key)] = values
	}
	out := make(http.Header)
	for key, values := range next {
		canonicalKey := http.CanonicalHeaderKey(key)
		if stringSlicesEqual(baseValues[canonicalKey], values) {
			continue
		}
		out[canonicalKey] = append([]string(nil), values...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (h *BaseAPIHandler) interceptorHost() PluginInterceptorHost {
	if h == nil {
		return nil
	}
	return h.PluginHost
}

func streamInterceptorsEnabled(host PluginInterceptorHost) bool {
	if host == nil {
		return false
	}
	if detector, ok := host.(streamInterceptorDetector); ok {
		return detector.HasStreamInterceptors()
	}
	return true
}

func requestInterceptorsEnabled(host PluginInterceptorHost) bool {
	if host == nil {
		return false
	}
	if detector, ok := host.(requestInterceptorDetector); ok {
		return detector.HasRequestInterceptors()
	}
	return true
}

type requestAfterAuthCapture struct {
	mu                      sync.Mutex
	set                     bool
	headers                 http.Header
	body                    []byte
	originalRequest         []byte
	originalRequestReplaced bool
}

func (c *requestAfterAuthCapture) record(req coreexecutor.RequestAfterAuthInterceptRequest, resp coreexecutor.RequestAfterAuthInterceptResponse) {
	if c == nil {
		return
	}
	headers := mergeRequestInterceptorHeaders(req.Headers, resp.Headers, resp.ClearHeaders)
	body := cloneBytes(req.Body)
	var originalRequest []byte
	originalRequestReplaced := false
	if len(resp.Body) > 0 {
		body = cloneBytes(resp.Body)
		originalRequest = cloneBytes(resp.Body)
		originalRequestReplaced = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.set = true
	c.headers = headers
	c.body = body
	c.originalRequest = originalRequest
	c.originalRequestReplaced = originalRequestReplaced
}

func (c *requestAfterAuthCapture) apply(req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Request, coreexecutor.Options) {
	if c == nil {
		return req, opts
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.set {
		return req, opts
	}
	req.Payload = cloneBytes(c.body)
	opts.Headers = cloneHeader(c.headers)
	if c.originalRequestReplaced {
		opts.OriginalRequest = cloneBytes(c.originalRequest)
	}
	return req, opts
}

func mergeRequestInterceptorHeaders(current, updates http.Header, clear []string) http.Header {
	if updates == nil && len(clear) == 0 {
		return cloneHeader(current)
	}
	out := cloneHeader(current)
	if out == nil && (len(updates) > 0 || len(clear) > 0) {
		out = make(http.Header)
	}
	for _, key := range clear {
		out.Del(key)
	}
	for key, values := range updates {
		out.Del(key)
		for _, value := range values {
			out.Add(key, value)
		}
	}
	return out
}

func interceptRequestBeforeAuth(ctx context.Context, host PluginInterceptorHost, req pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	if skipPluginID != "" {
		if skipper, ok := host.(pluginInterceptorSkipHost); ok {
			return skipper.InterceptRequestBeforeAuthExcept(ctx, req, skipPluginID)
		}
	}
	return host.InterceptRequestBeforeAuth(ctx, req)
}

func interceptRequestAfterAuth(ctx context.Context, host PluginInterceptorHost, req pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	if skipPluginID != "" {
		if skipper, ok := host.(pluginInterceptorSkipHost); ok {
			return skipper.InterceptRequestAfterAuthExcept(ctx, req, skipPluginID)
		}
	}
	return host.InterceptRequestAfterAuth(ctx, req)
}

func interceptResponse(ctx context.Context, host PluginInterceptorHost, req pluginapi.ResponseInterceptRequest, skipPluginID string) pluginapi.ResponseInterceptResponse {
	if skipPluginID != "" {
		if skipper, ok := host.(pluginInterceptorSkipHost); ok {
			return skipper.InterceptResponseExcept(ctx, req, skipPluginID)
		}
	}
	return host.InterceptResponse(ctx, req)
}

func interceptStreamChunk(ctx context.Context, host PluginInterceptorHost, req pluginapi.StreamChunkInterceptRequest, skipPluginID string) pluginapi.StreamChunkInterceptResponse {
	if skipPluginID != "" {
		if skipper, ok := host.(pluginInterceptorSkipHost); ok {
			return skipper.InterceptStreamChunkExcept(ctx, req, skipPluginID)
		}
	}
	return host.InterceptStreamChunk(ctx, req)
}

func (h *BaseAPIHandler) applyRequestInterceptorsBeforeAuth(ctx context.Context, handlerType, requestedModel string, req coreexecutor.Request, opts coreexecutor.Options, skipPluginID string) (coreexecutor.Request, coreexecutor.Options) {
	host := h.interceptorHost()
	if host == nil {
		return req, opts
	}
	resp := interceptRequestBeforeAuth(ctx, host, pluginapi.RequestInterceptRequest{
		SourceFormat:   handlerType,
		Model:          req.Model,
		RequestedModel: requestedModel,
		Stream:         opts.Stream,
		Headers:        cloneHeader(opts.Headers),
		Body:           cloneBytes(req.Payload),
		Metadata:       opts.Metadata,
	}, skipPluginID)
	opts.Headers = finalInterceptorHeaders(opts.Headers, resp.Headers)
	if len(resp.Body) > 0 {
		req.Payload = cloneBytes(resp.Body)
		opts.OriginalRequest = cloneBytes(resp.Body)
	}
	return req, opts
}

func (h *BaseAPIHandler) requestAfterAuthInterceptor(capture *requestAfterAuthCapture, skipPluginID string) coreexecutor.RequestAfterAuthInterceptor {
	if !requestInterceptorsEnabled(h.interceptorHost()) {
		return nil
	}
	return func(ctx context.Context, req coreexecutor.RequestAfterAuthInterceptRequest) coreexecutor.RequestAfterAuthInterceptResponse {
		resp := h.applyRequestInterceptorsAfterAuth(ctx, req, skipPluginID)
		if capture != nil {
			capture.record(req, resp)
		}
		return resp
	}
}

func (h *BaseAPIHandler) applyRequestInterceptorsAfterAuth(ctx context.Context, req coreexecutor.RequestAfterAuthInterceptRequest, skipPluginID string) coreexecutor.RequestAfterAuthInterceptResponse {
	host := h.interceptorHost()
	if !requestInterceptorsEnabled(host) {
		return coreexecutor.RequestAfterAuthInterceptResponse{}
	}
	resp := interceptRequestAfterAuth(ctx, host, pluginapi.RequestInterceptRequest{
		SourceFormat:   req.SourceFormat.String(),
		ToFormat:       req.ToFormat.String(),
		Model:          req.Model,
		RequestedModel: req.RequestedModel,
		Stream:         req.Stream,
		Headers:        cloneHeader(req.Headers),
		Body:           cloneBytes(req.Body),
		Metadata:       req.Metadata,
	}, skipPluginID)
	return coreexecutor.RequestAfterAuthInterceptResponse{
		Headers:      resp.Headers,
		Body:         resp.Body,
		ClearHeaders: resp.ClearHeaders,
	}
}

func (h *BaseAPIHandler) applyResponseInterceptors(ctx context.Context, handlerType, normalizedModel, requestedModel string, opts coreexecutor.Options, rawResponseHeaders, responseHeaders http.Header, originalRequest, requestBody, body []byte, statusCode int, skipPluginID string) ([]byte, http.Header) {
	host := h.interceptorHost()
	if host == nil {
		return body, responseHeaders
	}
	resp := interceptResponse(ctx, host, pluginapi.ResponseInterceptRequest{
		SourceFormat:    handlerType,
		Model:           normalizedModel,
		RequestedModel:  requestedModel,
		Stream:          false,
		RequestHeaders:  cloneHeader(opts.Headers),
		ResponseHeaders: cloneHeader(rawResponseHeaders),
		OriginalRequest: cloneBytes(originalRequest),
		RequestBody:     cloneBytes(requestBody),
		Body:            cloneBytes(body),
		StatusCode:      statusCode,
		Metadata:        opts.Metadata,
	}, skipPluginID)
	responseHeaders = downstreamHeadersAfterInterceptors(rawResponseHeaders, finalInterceptorHeaders(rawResponseHeaders, resp.Headers), PassthroughHeadersEnabled(h.Cfg))
	if len(resp.Body) > 0 {
		body = cloneBytes(resp.Body)
	}
	return body, responseHeaders
}
