package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func (h *Host) callRequestInterceptor(ctx context.Context, record capabilityRecord, method string, call func(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error), req pluginapi.RequestInterceptRequest) (out pluginapi.RequestInterceptResponse, ok bool) {
	if h == nil || call == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.RequestInterceptResponse{}, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, method, recovered)
			out = pluginapi.RequestInterceptResponse{}
			ok = false
		}
	}()
	resp, errIntercept := call(ctx, req)
	if errIntercept != nil {
		log.Warnf("pluginhost: request interceptor %s failed: %v", record.id, errIntercept)
		return pluginapi.RequestInterceptResponse{}, false
	}
	return resp, true
}

func (h *Host) callRequestBodyPatcher(ctx context.Context, record capabilityRecord, method string, patcher pluginapi.RequestBodyPatcher, body []byte) (out pluginapi.RequestBodyPatchResponse, ok bool) {
	if h == nil || patcher == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.RequestBodyPatchResponse{}, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, method, recovered)
			out = pluginapi.RequestBodyPatchResponse{}
			ok = false
		}
	}()
	resp, errPatch := patcher.PatchRequestBody(ctx, body)
	if errPatch != nil {
		log.Warnf("pluginhost: request body patcher %s failed: %v", record.id, errPatch)
		return pluginapi.RequestBodyPatchResponse{}, false
	}
	return resp, true
}

func (h *Host) callResponseInterceptor(ctx context.Context, record capabilityRecord, interceptor pluginapi.ResponseInterceptor, req pluginapi.ResponseInterceptRequest) (out pluginapi.ResponseInterceptResponse, ok bool) {
	if h == nil || interceptor == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.ResponseInterceptResponse{}, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "ResponseInterceptor.InterceptResponse", recovered)
			out = pluginapi.ResponseInterceptResponse{}
			ok = false
		}
	}()
	resp, errIntercept := interceptor.InterceptResponse(ctx, req)
	if errIntercept != nil {
		log.Warnf("pluginhost: response interceptor %s failed: %v", record.id, errIntercept)
		return pluginapi.ResponseInterceptResponse{}, false
	}
	return resp, true
}

func (h *Host) callStreamChunkInterceptor(ctx context.Context, record capabilityRecord, interceptor pluginapi.StreamChunkInterceptor, req pluginapi.StreamChunkInterceptRequest) (out pluginapi.StreamChunkInterceptResponse, ok bool) {
	if h == nil || interceptor == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.StreamChunkInterceptResponse{}, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "StreamChunkInterceptor.InterceptStreamChunk", recovered)
			out = pluginapi.StreamChunkInterceptResponse{}
			ok = false
		}
	}()
	resp, errIntercept := interceptor.InterceptStreamChunk(ctx, req)
	if errIntercept != nil {
		log.Warnf("pluginhost: stream chunk interceptor %s failed: %v", record.id, errIntercept)
		return pluginapi.StreamChunkInterceptResponse{}, false
	}
	return resp, true
}

func (h *Host) callWebSocketResponseObserver(ctx context.Context, record capabilityRecord, observer pluginapi.WebSocketResponseObserver, event pluginapi.WebSocketResponseEvent) {
	if h == nil || observer == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "WebSocketResponseObserver.ObserveWebSocketResponseEvent", recovered)
		}
	}()
	if errObserve := observer.ObserveWebSocketResponseEvent(ctx, event); errObserve != nil {
		log.Warnf("pluginhost: websocket response observer %s failed: %v", record.id, errObserve)
	}
}

func (h *Host) InterceptRequestBeforeAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return h.InterceptRequestBeforeAuthExcept(ctx, req, "")
}

func (h *Host) InterceptRequestBeforeAuthExcept(ctx context.Context, req pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	return h.interceptRequest(ctx, req, true, "RequestInterceptor.InterceptRequestBeforeAuth", func(interceptor pluginapi.RequestInterceptor, ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return interceptor.InterceptRequestBeforeAuth(ctx, req)
	}, skipPluginID)
}

func (h *Host) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return h.InterceptRequestAfterAuthExcept(ctx, req, "")
}

func (h *Host) InterceptRequestAfterAuthExcept(ctx context.Context, req pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	return h.interceptRequest(ctx, req, false, "RequestInterceptor.InterceptRequestAfterAuth", func(interceptor pluginapi.RequestInterceptor, ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return interceptor.InterceptRequestAfterAuth(ctx, req)
	}, skipPluginID)
}

func (h *Host) interceptRequest(ctx context.Context, req pluginapi.RequestInterceptRequest, beforeAuth bool, method string, invoke func(pluginapi.RequestInterceptor, context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error), skipPluginID string) pluginapi.RequestInterceptResponse {
	current := pluginapi.RequestInterceptResponse{
		Headers: cloneHeader(req.Headers),
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	currentBase := req.Body
	bodyModified := false
	patchMethod := "RequestBodyPatcher.PatchRequestBodyAfterAuth"
	if beforeAuth {
		patchMethod = "RequestBodyPatcher.PatchRequestBodyBeforeAuth"
	}

	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) || record.id == skipPluginID {
			continue
		}
		patcher := requestRecordBodyPatcher(record, beforeAuth)
		interceptor := record.plugin.Capabilities.RequestInterceptor
		if patcher == nil && interceptor == nil {
			continue
		}
		if patcher != nil {
			if len(currentBase) > 0 {
				patchInput := requestPatcherInputBody(patcher, currentBase)
				if patchResp, ok := h.callRequestBodyPatcher(ctx, record, patchMethod, patcher, patchInput); ok && len(patchResp.Edits) > 0 {
					patched, modified, errPatch := applyRequestBodyEdits(currentBase, patchResp.Edits)
					if errPatch != nil {
						log.Warnf("pluginhost: request body patcher %s returned invalid edits: %v", record.id, errPatch)
					} else if modified {
						currentBase = patched
						bodyModified = true
					}
				}
			}
			// When a plugin opts into raw body patching for this phase without also requesting
			// metadata-only request interception (omit_body), skip the legacy full-body RPC call.
			if interceptor == nil || !requestInterceptorOmitsBody(record, beforeAuth) {
				continue
			}
		}
		nextReq := req
		nextReq.Headers = cloneHeader(current.Headers)
		if requestInterceptorOmitsBody(record, beforeAuth) {
			nextReq.Body = nil
		} else if len(currentBase) > 0 {
			nextReq.Body = requestInterceptorInputBody(interceptor, currentBase)
		} else {
			nextReq.Body = nil
		}
		nextReq.Metadata = cloneInterceptorMetadata(req.Metadata)
		if nextReq.CallerScope == "" {
			if scope, ok := nextReq.Metadata[coreexecutor.CallerScopeMetadataKey].(string); ok {
				nextReq.CallerScope = strings.TrimSpace(scope)
			}
		} else if _, ok := nextReq.Metadata[coreexecutor.CallerScopeMetadataKey]; !ok {
			if nextReq.Metadata == nil {
				nextReq.Metadata = make(map[string]any, 1)
			}
			nextReq.Metadata[coreexecutor.CallerScopeMetadataKey] = strings.TrimSpace(nextReq.CallerScope)
		}
		if current.Path != "" {
			if nextReq.Metadata == nil {
				nextReq.Metadata = make(map[string]any, 1)
			}
			nextReq.Metadata[coreexecutor.RequestPathMetadataKey] = current.Path
		}
		if resp, ok := h.callRequestInterceptor(ctx, record, method, func(callCtx context.Context, callReq pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
			return invoke(interceptor, callCtx, callReq)
		}, nextReq); ok {
			current.Headers = mergeHeaders(current.Headers, resp.Headers, resp.ClearHeaders)
			if len(resp.ClearHeaders) > 0 {
				current.ClearHeaders = append(current.ClearHeaders, resp.ClearHeaders...)
			}
			if len(resp.Body) > 0 {
				currentBase = requestInterceptorOutputBody(interceptor, resp.Body)
				bodyModified = true
			} else if edits := requestInterceptResponseEdits(resp); len(edits) > 0 && len(currentBase) > 0 {
				patched, modified, errPatch := applyRequestBodyEdits(currentBase, edits)
				if errPatch != nil {
					log.Warnf("pluginhost: request interceptor %s returned invalid edits: %v", record.id, errPatch)
				} else if modified {
					currentBase = patched
					bodyModified = true
				}
			}
			if strings.TrimSpace(resp.Path) != "" {
				current.Path = strings.TrimSpace(resp.Path)
			}
			if resp.Terminate {
				current.Terminate = true
				current.StatusCode = resp.StatusCode
				current.ResponseHeaders = cloneHeader(resp.ResponseHeaders)
				current.ResponseBody = bytes.Clone(resp.ResponseBody)
				break
			}
		}
	}
	if len(current.ClearHeaders) > 0 && len(current.Headers) > 0 {
		filtered := current.ClearHeaders[:0]
		for _, key := range current.ClearHeaders {
			if current.Headers.Values(key) == nil {
				filtered = append(filtered, key)
			}
		}
		if len(filtered) == 0 {
			current.ClearHeaders = nil
		} else {
			current.ClearHeaders = filtered
		}
	}
	if bodyModified {
		current.Body = currentBase
	}
	return current
}

// CompleteRequest schedules terminal notifications without blocking response delivery.
func (h *Host) CompleteRequest(ctx context.Context, completion pluginapi.RequestCompletion) {
	h.CompleteRequestExcept(ctx, completion, "")
}

func (h *Host) HasWebSocketResponseObservers() bool {
	if h == nil {
		return false
	}
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) {
			continue
		}
		if record.plugin.Capabilities.WebSocketResponseObserver != nil {
			return true
		}
	}
	return false
}

func (h *Host) ObserveWebSocketResponseEvent(ctx context.Context, event pluginapi.WebSocketResponseEvent) {
	h.ObserveWebSocketResponseEventExcept(ctx, event, "")
}

func (h *Host) ObserveWebSocketResponseEventExcept(ctx context.Context, event pluginapi.WebSocketResponseEvent, skipPluginID string) {
	if h == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	for _, record := range h.activeRecords() {
		observer := record.plugin.Capabilities.WebSocketResponseObserver
		if h.isPluginFused(record.id) || observer == nil || record.id == skipPluginID || !h.recordCurrent(record) {
			continue
		}
		next := event
		next.Payload = bytes.Clone(event.Payload)
		next.Metadata = cloneInterceptorMetadata(event.Metadata)
		h.callWebSocketResponseObserver(ctx, record, observer, next)
	}
}

// CompleteRequestExcept notifies lifecycle plugins except the plugin that initiated a nested host execution.
func (h *Host) CompleteRequestExcept(ctx context.Context, completion pluginapi.RequestCompletion, skipPluginID string) {
	if h == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	for _, record := range h.activeRecords() {
		plugin := record.plugin.Capabilities.RequestLifecyclePlugin
		if h.isPluginFused(record.id) || plugin == nil || record.id == skipPluginID || !h.recordCurrent(record) {
			continue
		}
		next := completion
		next.Metadata = cloneInterceptorMetadata(completion.Metadata)
		go func(record capabilityRecord, plugin pluginapi.RequestLifecyclePlugin, completion pluginapi.RequestCompletion) {
			defer func() {
				if recovered := recover(); recovered != nil {
					h.fusePlugin(record.id, "RequestLifecyclePlugin.HandleRequestComplete", recovered)
				}
			}()
			if errComplete := plugin.HandleRequestComplete(ctx, completion); errComplete != nil {
				log.Warnf("pluginhost: request lifecycle plugin %s failed: %v", record.id, errComplete)
			}
		}(record, plugin, next)
	}
}

func (h *Host) InterceptResponse(ctx context.Context, req pluginapi.ResponseInterceptRequest) pluginapi.ResponseInterceptResponse {
	return h.InterceptResponseExcept(ctx, req, "")
}

func (h *Host) InterceptResponseExcept(ctx context.Context, req pluginapi.ResponseInterceptRequest, skipPluginID string) pluginapi.ResponseInterceptResponse {
	current := pluginapi.ResponseInterceptResponse{
		Headers: cloneHeader(req.ResponseHeaders),
		Body:    bytes.Clone(req.Body),
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	for _, record := range h.activeRecords() {
		interceptor := record.plugin.Capabilities.ResponseInterceptor
		if h.isPluginFused(record.id) || interceptor == nil || record.id == skipPluginID {
			continue
		}
		nextReq := req
		nextReq.RequestHeaders = cloneHeader(req.RequestHeaders)
		nextReq.ResponseHeaders = cloneHeader(current.Headers)
		nextReq.OriginalRequest = bytes.Clone(req.OriginalRequest)
		nextReq.RequestBody = bytes.Clone(req.RequestBody)
		nextReq.Body = bytes.Clone(current.Body)
		nextReq.Metadata = cloneInterceptorMetadata(req.Metadata)
		if resp, ok := h.callResponseInterceptor(ctx, record, interceptor, nextReq); ok {
			current.Headers = mergeHeaders(current.Headers, resp.Headers, resp.ClearHeaders)
			if len(resp.Body) > 0 {
				current.Body = bytes.Clone(resp.Body)
			}
		}
	}
	return current
}

func (h *Host) InterceptStreamChunk(ctx context.Context, req pluginapi.StreamChunkInterceptRequest) pluginapi.StreamChunkInterceptResponse {
	return h.InterceptStreamChunkExcept(ctx, req, "")
}

func (h *Host) InterceptStreamChunkExcept(ctx context.Context, req pluginapi.StreamChunkInterceptRequest, skipPluginID string) pluginapi.StreamChunkInterceptResponse {
	current := pluginapi.StreamChunkInterceptResponse{
		Headers: cloneHeader(req.ResponseHeaders),
		Body:    bytes.Clone(req.Body),
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	for _, record := range h.activeRecords() {
		interceptor := record.plugin.Capabilities.StreamChunkInterceptor
		if h.isPluginFused(record.id) || interceptor == nil || current.DropChunk || record.id == skipPluginID {
			continue
		}
		nextReq := req
		nextReq.RequestHeaders = cloneHeader(req.RequestHeaders)
		nextReq.ResponseHeaders = cloneHeader(current.Headers)
		// Schema v3+ omits request bodies on payload chunks to avoid re-sending multi-MB
		// prompts across cgo/JSON for every frame. Legacy plugins still receive them.
		if req.ChunkIndex != pluginapi.StreamChunkHeaderInitIndex && streamChunkOmitsRequestBodies(record.plugin.SchemaVersion) {
			nextReq.OriginalRequest = nil
			nextReq.RequestBody = nil
		} else {
			nextReq.OriginalRequest = bytes.Clone(req.OriginalRequest)
			nextReq.RequestBody = bytes.Clone(req.RequestBody)
		}
		nextReq.Body = bytes.Clone(current.Body)
		// Schema v5+ omits HistoryChunks on payload chunks to avoid cloning and re-encoding
		// recent chunk windows across cgo/JSON for every frame. Legacy plugins still receive them.
		if req.ChunkIndex != pluginapi.StreamChunkHeaderInitIndex && streamChunkOmitsHistory(record.plugin.SchemaVersion) {
			nextReq.HistoryChunks = nil
		} else {
			nextReq.HistoryChunks = cloneByteSlices(req.HistoryChunks)
		}
		nextReq.Metadata = cloneInterceptorMetadata(req.Metadata)
		if resp, ok := h.callStreamChunkInterceptor(ctx, record, interceptor, nextReq); ok {
			current.Headers = mergeHeaders(current.Headers, resp.Headers, resp.ClearHeaders)
			if len(resp.Body) > 0 {
				current.Body = bytes.Clone(resp.Body)
			}
			if resp.DropChunk {
				current.DropChunk = true
			}
		}
	}
	return current
}

func (h *Host) HasStreamInterceptors() bool {
	if h == nil {
		return false
	}
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) {
			continue
		}
		if record.plugin.Capabilities.StreamChunkInterceptor != nil {
			return true
		}
	}
	return false
}

// StreamChunkPayloadIncludesRequestBody reports whether any active stream chunk
// interceptor still requires OriginalRequest/RequestBody on payload chunks
// (schema_version < SchemaVersionStreamChunkOmitRequestBody).
func (h *Host) StreamChunkPayloadIncludesRequestBody() bool {
	if h == nil {
		return false
	}
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.StreamChunkInterceptor == nil {
			continue
		}
		if !streamChunkOmitsRequestBodies(record.plugin.SchemaVersion) {
			return true
		}
	}
	return false
}

func streamChunkOmitsRequestBodies(schemaVersion uint32) bool {
	return schemaVersion >= pluginabi.SchemaVersionStreamChunkOmitRequestBody
}

// StreamChunkPayloadIncludesHistory reports whether any active stream chunk
// interceptor still requires HistoryChunks on payload chunks
// (schema_version < SchemaVersionStreamChunkOmitHistory).
func (h *Host) StreamChunkPayloadIncludesHistory() bool {
	if h == nil {
		return false
	}
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) || record.plugin.Capabilities.StreamChunkInterceptor == nil {
			continue
		}
		if !streamChunkOmitsHistory(record.plugin.SchemaVersion) {
			return true
		}
	}
	return false
}

func requestInterceptorOmitsBody(record capabilityRecord, beforeAuth bool) bool {
	if record.plugin.SchemaVersion < pluginabi.SchemaVersionRequestInterceptorOmitBody {
		return false
	}
	caps := record.plugin.Capabilities
	if caps.RequestInterceptor == nil {
		return false
	}
	return requestInterceptorWantsOmitBody(caps, beforeAuth)
}

func streamChunkOmitsHistory(schemaVersion uint32) bool {
	return schemaVersion >= pluginabi.SchemaVersionStreamChunkOmitHistory
}

func (h *Host) HasRequestInterceptors() bool {
	if h == nil {
		return false
	}
	for _, record := range h.activeRecords() {
		if h.isPluginFused(record.id) {
			continue
		}
		if recordHasRequestInterceptor(record) {
			return true
		}
	}
	return false
}

func recordHasRequestInterceptor(record capabilityRecord) bool {
	caps := record.plugin.Capabilities
	if caps.RequestInterceptor != nil {
		return true
	}
	return requestRecordBodyPatcher(record, true) != nil || requestRecordBodyPatcher(record, false) != nil
}

type funcBodyPatcher func(context.Context, []byte) (pluginapi.RequestBodyPatchResponse, error)

func (f funcBodyPatcher) PatchRequestBody(ctx context.Context, body []byte) (pluginapi.RequestBodyPatchResponse, error) {
	return f(ctx, body)
}

func requestRecordBodyPatcher(record capabilityRecord, beforeAuth bool) pluginapi.RequestBodyPatcher {
	if record.plugin.SchemaVersion < pluginabi.SchemaVersionRequestInterceptorBodyPatch {
		return nil
	}
	caps := record.plugin.Capabilities
	if beforeAuth {
		if caps.RequestBodyPatcherBefore != nil {
			return caps.RequestBodyPatcherBefore
		}
		if caps.RequestInterceptorPatchBodyBefore && caps.RequestInterceptor != nil {
			if p, ok := caps.RequestInterceptor.(interface {
				PatchRequestBodyBeforeAuth(context.Context, []byte) (pluginapi.RequestBodyPatchResponse, error)
			}); ok && p != nil {
				return funcBodyPatcher(p.PatchRequestBodyBeforeAuth)
			}
			if p, ok := caps.RequestInterceptor.(pluginapi.RequestBodyPatcher); ok && p != nil {
				return p
			}
		}
		return nil
	}
	if caps.RequestBodyPatcherAfter != nil {
		return caps.RequestBodyPatcherAfter
	}
	if caps.RequestInterceptorPatchBodyAfter && caps.RequestInterceptor != nil {
		if p, ok := caps.RequestInterceptor.(interface {
			PatchRequestBodyAfterAuth(context.Context, []byte) (pluginapi.RequestBodyPatchResponse, error)
		}); ok && p != nil {
			return funcBodyPatcher(p.PatchRequestBodyAfterAuth)
		}
		if p, ok := caps.RequestInterceptor.(pluginapi.RequestBodyPatcher); ok && p != nil {
			return p
		}
	}
	return nil
}

func requestPatcherInputBody(patcher pluginapi.RequestBodyPatcher, body []byte) []byte {
	if _, isRPC := patcher.(rpcRequestBodyPatcher); isRPC {
		return body
	}
	return bytes.Clone(body)
}

func requestInterceptorInputBody(interceptor pluginapi.RequestInterceptor, body []byte) []byte {
	if _, isRPC := interceptor.(*rpcPluginAdapter); isRPC {
		return body
	}
	return bytes.Clone(body)
}

func requestInterceptorOutputBody(interceptor pluginapi.RequestInterceptor, body []byte) []byte {
	if _, isRPC := interceptor.(*rpcPluginAdapter); isRPC {
		return body
	}
	return bytes.Clone(body)
}

func requestInterceptResponseEdits(resp pluginapi.RequestInterceptResponse) []pluginapi.RequestBodyEdit {
	if len(resp.BodyEdits) > 0 {
		return resp.BodyEdits
	}
	return resp.Edits
}

func requestBodyEditReplacementLen(edit pluginapi.RequestBodyEdit) int {
	if len(edit.ReplacementBytes) > 0 {
		return len(edit.ReplacementBytes)
	}
	return len(edit.Replacement)
}

func appendRequestBodyEditReplacement(dst []byte, edit pluginapi.RequestBodyEdit) []byte {
	if len(edit.ReplacementBytes) > 0 {
		return append(dst, edit.ReplacementBytes...)
	}
	return append(dst, edit.Replacement...)
}

func applyRequestBodyEdits(base []byte, edits []pluginapi.RequestBodyEdit) ([]byte, bool, error) {
	if len(edits) == 0 {
		return base, false, nil
	}
	type indexedEdit struct {
		edit pluginapi.RequestBodyEdit
	}
	sorted := make([]indexedEdit, len(edits))
	for i, edit := range edits {
		sorted[i] = indexedEdit{edit: edit}
	}
	if len(sorted) > 1 {
		sort.SliceStable(sorted, func(i, j int) bool {
			if sorted[i].edit.Start != sorted[j].edit.Start {
				return sorted[i].edit.Start < sorted[j].edit.Start
			}
			return sorted[i].edit.End < sorted[j].edit.End
		})
	}
	baseLen := len(base)
	outLen := baseLen
	prevEnd := 0
	anyChange := false
	for i, item := range sorted {
		e := item.edit
		if e.Start < 0 || e.End < e.Start || e.End > baseLen {
			return base, false, fmt.Errorf("edit[%d] range [%d:%d] out of bounds for body length %d", i, e.Start, e.End, baseLen)
		}
		if i > 0 && e.Start < prevEnd {
			return base, false, fmt.Errorf("edit[%d] range [%d:%d] overlaps previous end %d", i, e.Start, e.End, prevEnd)
		}
		replLen := requestBodyEditReplacementLen(e)
		if e.Start != e.End || replLen > 0 {
			if len(e.ReplacementBytes) > 0 {
				if !bytes.Equal(base[e.Start:e.End], e.ReplacementBytes) {
					anyChange = true
				}
			} else if string(base[e.Start:e.End]) != e.Replacement {
				anyChange = true
			}
		}
		outLen += replLen - (e.End - e.Start)
		prevEnd = e.End
	}
	if !anyChange {
		return base, false, nil
	}
	out := make([]byte, 0, outLen)
	cursor := 0
	for _, item := range sorted {
		e := item.edit
		if e.Start > cursor {
			out = append(out, base[cursor:e.Start]...)
		}
		out = appendRequestBodyEditReplacement(out, e)
		cursor = e.End
	}
	if cursor < baseLen {
		out = append(out, base[cursor:]...)
	}
	if !json.Valid(out) {
		return base, false, fmt.Errorf("patched request body is not valid JSON")
	}
	return out, true, nil
}

func readAndRestoreRequestBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	origBody := r.Body
	body, errReadAll := io.ReadAll(origBody)
	if errReadAll != nil {
		r.Body = struct {
			io.Reader
			io.Closer
		}{
			Reader: io.MultiReader(bytes.NewReader(body), origBody),
			Closer: origBody,
		}
		return nil, errReadAll
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func authID(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.ID
}

func authProvider(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return auth.Provider
}

func authMetadata(auth *coreauth.Auth) map[string]any {
	if auth == nil {
		return nil
	}
	return auth.Metadata
}

func cloneHeader(in http.Header) http.Header {
	if len(in) == 0 {
		return nil
	}
	out := make(http.Header, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func mergeHeaders(current, updates http.Header, clear []string) http.Header {
	out := cloneHeader(current)
	if out == nil {
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

func cloneByteSlices(in [][]byte) [][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(in))
	for _, item := range in {
		out = append(out, bytes.Clone(item))
	}
	return out
}

func cloneValues(in url.Values) url.Values {
	if len(in) == 0 {
		return nil
	}
	out := make(url.Values, len(in))
	for key, values := range in {
		out[key] = append([]string(nil), values...)
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneInterceptorMetadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	visited := make(map[metadataCloneVisit]reflect.Value)
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneInterceptorMetadataAny(reflect.ValueOf(value), visited)
	}
	return out
}

type metadataCloneVisit struct {
	typ reflect.Type
	ptr uintptr
}

func cloneInterceptorMetadataAny(value reflect.Value, visited map[metadataCloneVisit]reflect.Value) any {
	cloned := cloneInterceptorMetadataReflectValue(value, visited)
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
}

func cloneInterceptorMetadataReflectValue(value reflect.Value, visited map[metadataCloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Value{}
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		return cloneInterceptorMetadataReflectValue(value.Elem(), visited)
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := metadataCloneVisit{typ: value.Type(), ptr: value.Pointer()}
		if existing, okExisting := visited[visit]; okExisting {
			return existing
		}
		out := reflect.New(value.Type().Elem())
		visited[visit] = out
		clonedElem := cloneInterceptorMetadataReflectValue(value.Elem(), visited)
		if clonedElem.IsValid() {
			outElem := out.Elem()
			if clonedElem.Type().AssignableTo(outElem.Type()) {
				outElem.Set(clonedElem)
			} else if clonedElem.Type().ConvertibleTo(outElem.Type()) {
				outElem.Set(clonedElem.Convert(outElem.Type()))
			}
		}
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := metadataCloneVisit{typ: value.Type(), ptr: value.Pointer()}
		if existing, okExisting := visited[visit]; okExisting {
			return existing
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		visited[visit] = out
		iter := value.MapRange()
		for iter.Next() {
			keyValue := adaptClonedValue(iter.Key(), cloneInterceptorMetadataReflectValue(iter.Key(), visited))
			valValue := adaptClonedValue(iter.Value(), cloneInterceptorMetadataReflectValue(iter.Value(), visited))
			out.SetMapIndex(keyValue, valValue)
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		if value.Type().Elem().Kind() == reflect.Uint8 {
			out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
			reflect.Copy(out, value)
			return out
		}
		visit := metadataCloneVisit{typ: value.Type(), ptr: value.Pointer()}
		if existing, okExisting := visited[visit]; okExisting {
			return existing
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		visited[visit] = out
		for i := 0; i < value.Len(); i++ {
			clonedItem := cloneInterceptorMetadataReflectValue(value.Index(i), visited)
			if !clonedItem.IsValid() {
				continue
			}
			out.Index(i).Set(adaptClonedValue(value.Index(i), clonedItem))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			clonedItem := cloneInterceptorMetadataReflectValue(value.Index(i), visited)
			if !clonedItem.IsValid() {
				continue
			}
			out.Index(i).Set(adaptClonedValue(value.Index(i), clonedItem))
		}
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		// Preserve unexported fields and deep-clone exported fields on a best-effort basis.
		out.Set(value)
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			if !out.Field(i).CanSet() {
				continue
			}
			fieldClone := cloneInterceptorMetadataReflectValue(field, visited)
			if !fieldClone.IsValid() {
				continue
			}
			out.Field(i).Set(adaptClonedValue(field, fieldClone))
		}
		return out
	default:
		return value
	}
}

func adaptClonedValue(original, cloned reflect.Value) reflect.Value {
	if !cloned.IsValid() {
		return original
	}
	if cloned.Type().AssignableTo(original.Type()) {
		return cloned
	}
	if cloned.Type().ConvertibleTo(original.Type()) {
		return cloned.Convert(original.Type())
	}
	return original
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
