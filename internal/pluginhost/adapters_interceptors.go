package pluginhost

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
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
	return h.interceptRequest(ctx, req, "RequestInterceptor.InterceptRequestBeforeAuth", func(interceptor pluginapi.RequestInterceptor, ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return interceptor.InterceptRequestBeforeAuth(ctx, req)
	}, skipPluginID)
}

func (h *Host) InterceptRequestAfterAuth(ctx context.Context, req pluginapi.RequestInterceptRequest) pluginapi.RequestInterceptResponse {
	return h.InterceptRequestAfterAuthExcept(ctx, req, "")
}

func (h *Host) InterceptRequestAfterAuthExcept(ctx context.Context, req pluginapi.RequestInterceptRequest, skipPluginID string) pluginapi.RequestInterceptResponse {
	return h.interceptRequest(ctx, req, "RequestInterceptor.InterceptRequestAfterAuth", func(interceptor pluginapi.RequestInterceptor, ctx context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
		return interceptor.InterceptRequestAfterAuth(ctx, req)
	}, skipPluginID)
}

func (h *Host) interceptRequest(ctx context.Context, req pluginapi.RequestInterceptRequest, method string, invoke func(pluginapi.RequestInterceptor, context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error), skipPluginID string) pluginapi.RequestInterceptResponse {
	current := pluginapi.RequestInterceptResponse{
		Headers: cloneHeader(req.Headers),
		Body:    bytes.Clone(req.Body),
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	for _, record := range h.activeRecords() {
		interceptor := record.plugin.Capabilities.RequestInterceptor
		if h.isPluginFused(record.id) || interceptor == nil || record.id == skipPluginID {
			continue
		}
		nextReq := req
		nextReq.Headers = cloneHeader(current.Headers)
		nextReq.Body = bytes.Clone(current.Body)
		nextReq.Metadata = cloneInterceptorMetadata(req.Metadata)
		if resp, ok := h.callRequestInterceptor(ctx, record, method, func(callCtx context.Context, callReq pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
			return invoke(interceptor, callCtx, callReq)
		}, nextReq); ok {
			current.Headers = mergeHeaders(current.Headers, resp.Headers, resp.ClearHeaders)
			if len(resp.Body) > 0 {
				current.Body = bytes.Clone(resp.Body)
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
		if record.plugin.Capabilities.RequestInterceptor != nil {
			return true
		}
	}
	return false
}

func (h *Host) commitModelClients(snap *Snapshot, modelRegistry modelRegistry, registrations []modelClientRegistration, nextClients map[string]struct{}, nextProviders map[string]string, nextModelRegistrations map[string]pluginModelRegistration) {
	if h == nil || modelRegistry == nil {
		return
	}

	staleClients := make([]string, 0)
	h.mu.Lock()
	if h.Snapshot() != snap {
		h.mu.Unlock()
		return
	}
	for clientID := range h.modelClientIDs {
		if _, okClient := nextClients[clientID]; !okClient {
			staleClients = append(staleClients, clientID)
		}
	}
	h.modelClientIDs = nextClients
	h.modelProviders = nextProviders
	h.modelRegistrations = nextModelRegistrations
	h.mu.Unlock()

	for _, registration := range registrations {
		modelRegistry.RegisterClient(registration.clientID, registration.provider, registration.models)
	}
	for _, clientID := range staleClients {
		modelRegistry.UnregisterClient(clientID)
	}
}

func readAndRestoreRequestBody(r *http.Request) ([]byte, error) {
	if r == nil || r.Body == nil {
		return nil, nil
	}
	body, errReadAll := io.ReadAll(r.Body)
	if errReadAll != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
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
