package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginID           = "codex-tool-search-shim"
	pluginVersion      = "0.4.0"
	probeHeader        = "X-Codex-Tool-Search-Shim"
	probeRequestHeader = "X-Codex-Tool-Search-Shim-Probe"
)

var structureLogMu sync.Mutex

type requestState struct {
	bridge      bool
	bridgeKnown bool
	catalog     *toolCatalog
}

const maxRequestStates = 512

var (
	requestStateMu    sync.RWMutex
	requestStates     = make(map[string]requestState)
	requestStateOrder []string
)

func rememberRequestState(requestID string, state requestState) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	requestStateMu.Lock()
	defer requestStateMu.Unlock()
	previous, exists := requestStates[requestID]
	if exists {
		if state.catalog == nil {
			state.catalog = previous.catalog
		}
		if !state.bridgeKnown {
			state.bridge = previous.bridge
			state.bridgeKnown = previous.bridgeKnown
		}
	} else {
		requestStateOrder = append(requestStateOrder, requestID)
	}
	requestStates[requestID] = state
	for len(requestStateOrder) > maxRequestStates {
		oldest := requestStateOrder[0]
		requestStateOrder = requestStateOrder[1:]
		delete(requestStates, oldest)
	}
}

func loadRequestState(requestID string) (requestState, bool) {
	requestStateMu.RLock()
	defer requestStateMu.RUnlock()
	state, ok := requestStates[requestID]
	return state, ok
}

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	RequestInterceptor     bool `json:"request_interceptor"`
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"response_stream_interceptor"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) (status C.int) {
	// A Go panic must never unwind across the cgo boundary: the host cannot
	// recover a panic raised by a c-shared plugin, and the process would abort.
	// Convert it into a plugin error envelope so the host logs it and skips
	// this plugin for the request.
	defer func() {
		if recovered := recover(); recovered != nil {
			status = 1
			if response != nil && response.ptr == nil {
				writeResponse(response, errorEnvelope("plugin_panic", panicMessage(recovered)))
			}
		}
	}()
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}

	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	var raw []byte
	var errHandle error
	func() {
		defer containPanic(&errHandle)
		raw, errHandle = dispatchMethod(method, request)
	}()
	if errHandle != nil {
		return nil, errHandle
	}
	return raw, nil
}

func dispatchMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configurePlugin(request); errConfigure != nil {
			return errorEnvelope("invalid_config", errConfigure.Error()), nil
		}
		return okEnvelope(currentRegistration())
	case pluginabi.MethodRequestInterceptBefore:
		return interceptRequest("request_before", request)
	case pluginabi.MethodRequestInterceptAfter:
		return interceptRequest("request_after", request)
	case pluginabi.MethodResponseInterceptAfter:
		return interceptResponse(request)
	case pluginabi.MethodResponseInterceptStreamChunk:
		return interceptStreamChunk(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

// containPanic converts a recovered panic into an error. Plugins are loaded as
// c-shared libraries, so an unrecovered panic aborts the entire CLIProxyAPI
// process; every ABI entry point funnels through this guard instead.
func containPanic(target *error) {
	recovered := recover()
	if recovered == nil {
		return
	}
	*target = fmt.Errorf("%s: %s", pluginID, panicMessage(recovered))
}

// panicMessage renders a recovered panic value. It is truncated so a panic that
// carries request data cannot flood the host log.
func panicMessage(recovered any) string {
	const limit = 200
	message := fmt.Sprintf("%v", recovered)
	if len(message) > limit {
		return message[:limit] + "..."
	}
	return message
}

func currentRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "Codex Tool Search Shim",
			Version:          pluginVersion,
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "enabled",
					Type:        pluginapi.ConfigFieldTypeBoolean,
					Description: "Bridges Codex tool_search declarations and history to ordinary function tools for upstreams that do not speak the Responses protocol.",
				},
				{
					Name:        "strict_responses_models",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Native Responses models (exact names or prefix*) that need strict tool_search schemas and local $ref inlining, for example OpenCode Go Muse.",
				},
				{
					Name:        "extra_source_formats",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Additional client protocol identifiers to treat as Responses-family when deciding whether to bridge tool_search. Built-in values are openai-response, openai-responses, responses, response and codex.",
				},
				{
					Name:        "diagnostics_log_path",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "File that probe requests append structure-only diagnostics to. Defaults to codex-tool-search-shim-structure.jsonl in the platform temporary directory.",
				},
			},
		},
		Capabilities: registrationCapability{
			RequestInterceptor:     true,
			ResponseInterceptor:    true,
			StreamChunkInterceptor: true,
		},
	}
}

func interceptRequest(kind string, request []byte) ([]byte, error) {
	var req pluginapi.RequestInterceptRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if probeRequested(req.Headers) {
		appendStructureLog(kind, req.Body)
	}
	catalog := extractToolCatalog(req.Body)
	policy := requestPolicyFor(kind, req.SourceFormat, req.ToFormat)
	rememberRequestState(req.RequestID, requestState{
		bridge:      policy.bridge,
		bridgeKnown: policy.bridgeKnown,
		catalog:     catalog,
	})
	body, changed, errRewrite := rewriteRequestBodyWithPolicy(req.Body, policy, catalog)
	if errRewrite != nil {
		return nil, errRewrite
	}
	if !changed && policy.native && strictNativeModelMatches(req.Model, req.RequestedModel) {
		normalized, normalizedChanged, errNormalize := normalizeStrictNativeBody(req.Body)
		if errNormalize != nil {
			return nil, errNormalize
		}
		if normalizedChanged {
			body = normalized
			changed = true
		}
	}
	if changed && probeRequested(req.Headers) {
		appendStructureLog(kind+"_upstream", body)
	}
	response := pluginapi.RequestInterceptResponse{}
	if changed {
		response.Body = body
	}
	return okEnvelope(response)
}

func interceptResponse(request []byte) ([]byte, error) {
	var req pluginapi.ResponseInterceptRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if probeRequested(req.RequestHeaders) {
		appendStructureLog("response", req.Body)
	}
	response := pluginapi.ResponseInterceptResponse{}
	bridge, catalog := responseBridgeState(req.RequestID, req.SourceFormat, req.OriginalRequest, req.RequestBody)
	if bridge {
		body, changed, errRewrite := rewriteResponseBodyWithPolicy(req.Body, catalog, bridge)
		if errRewrite != nil {
			return nil, errRewrite
		}
		if changed {
			response.Body = body
		}
	}
	if probeRequested(req.RequestHeaders) {
		response.Headers = http.Header{probeHeader: []string{pluginVersion}}
	}
	return okEnvelope(response)
}

func interceptStreamChunk(request []byte) ([]byte, error) {
	var req pluginapi.StreamChunkInterceptRequest
	if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if probeRequested(req.RequestHeaders) {
		appendStructureLog("stream", req.Body)
	}
	// Payload chunks omit OriginalRequest, so the header-init call or the
	// request interceptors must have cached the catalog by now.
	cacheRequestCatalog(req.RequestID, req.OriginalRequest, req.RequestBody)
	response := pluginapi.StreamChunkInterceptResponse{}
	bridge, catalog := responseBridgeState(req.RequestID, req.SourceFormat)
	if bridge {
		body, changed, errRewrite := rewriteResponseBodyWithPolicy(req.Body, catalog, bridge)
		if errRewrite != nil {
			return nil, errRewrite
		}
		if changed {
			response.Body = body
		}
	}
	if probeRequested(req.RequestHeaders) {
		response.Headers = http.Header{probeHeader: []string{pluginVersion}}
	}
	return okEnvelope(response)
}

// cacheRequestCatalog remembers the tool catalog for a request so later stream
// chunks can restore namespaced tool identities. It merges into any state the
// request interceptors already recorded.
func cacheRequestCatalog(requestID string, bodies ...[]byte) {
	if strings.TrimSpace(requestID) == "" {
		return
	}
	if state, exists := loadRequestState(requestID); exists && state.catalog != nil {
		return
	}
	for _, body := range bodies {
		catalog := extractToolCatalog(body)
		if catalog == nil {
			continue
		}
		rememberRequestState(requestID, requestState{catalog: catalog})
		return
	}
}

// responseBridgeState resolves whether a downstream response must be translated
// back into the Responses protocol and which tool catalog restores namespaced
// identities. Only a Responses client can consume tool_search_call items, so
// every other client protocol is left alone even when request state is missing.
func responseBridgeState(requestID, sourceFormat string, fallbackBodies ...[]byte) (bool, *toolCatalog) {
	// The client protocol is authoritative: only a Responses client can consume
	// tool_search_call items and namespaced function calls, so a stale or
	// mismatched request state must never widen the rewrite to another protocol.
	if !isResponsesFormat(sourceFormat) {
		return false, nil
	}
	state, exists := loadRequestState(requestID)
	if exists && state.bridgeKnown && !state.bridge {
		// The upstream speaks native Responses, so its response is already in
		// the shape the client expects.
		return false, nil
	}
	catalog := state.catalog
	if catalog != nil {
		return true, catalog
	}
	for _, body := range fallbackBodies {
		if catalog = extractToolCatalog(body); catalog != nil {
			break
		}
	}
	return true, catalog
}

type structureLog struct {
	Kind       string          `json:"kind"`
	Time       string          `json:"ts"`
	Model      string          `json:"model,omitempty"`
	ToolBytes  int             `json:"tool_bytes,omitempty"`
	InputBytes int             `json:"input_bytes,omitempty"`
	Tools      []structureRef  `json:"tools,omitempty"`
	Input      []structureItem `json:"input,omitempty"`
	Output     []structureItem `json:"output,omitempty"`
}

type structureRef struct {
	Type         string         `json:"type"`
	Name         string         `json:"name,omitempty"`
	Namespace    string         `json:"namespace,omitempty"`
	Execution    string         `json:"execution,omitempty"`
	DeferLoading *bool          `json:"defer_loading,omitempty"`
	Tools        []structureRef `json:"tools,omitempty"`
}

type structureItem struct {
	Type      string         `json:"type"`
	Name      string         `json:"name,omitempty"`
	Namespace string         `json:"namespace,omitempty"`
	CallID    string         `json:"call_id,omitempty"`
	Execution string         `json:"execution,omitempty"`
	Tools     []structureRef `json:"tools,omitempty"`
}

func appendStructureLog(kind string, body []byte) {
	if len(body) == 0 {
		return
	}
	record := structureLog{Kind: kind, Time: time.Now().UTC().Format(time.RFC3339Nano)}
	if payload, ok := responseBodyPayload(body); ok {
		body = payload
	}
	var root map[string]any
	if errUnmarshal := json.Unmarshal(body, &root); errUnmarshal != nil {
		return
	}
	if model, ok := root["model"].(string); ok {
		record.Model = model
	}
	if tools, exists := root["tools"]; exists {
		if encoded, errMarshal := json.Marshal(tools); errMarshal == nil {
			record.ToolBytes = len(encoded)
		}
	}
	if input, exists := root["input"]; exists {
		if encoded, errMarshal := json.Marshal(input); errMarshal == nil {
			record.InputBytes = len(encoded)
		}
	}
	record.Tools = structureRefs(root["tools"])
	record.Input = structureItems(root["input"])
	record.Output = structureItems(root["output"])
	if len(record.Tools) == 0 && len(record.Input) == 0 && len(record.Output) == 0 {
		return
	}
	encoded, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		return
	}
	structureLogMu.Lock()
	defer structureLogMu.Unlock()
	file, errOpen := os.OpenFile(diagnosticsLogPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(append(encoded, '\n'))
}

func responseBodyPayload(body []byte) ([]byte, bool) {
	trimmed := strings.TrimSpace(string(body))
	if !strings.HasPrefix(trimmed, "data:") {
		return body, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
	return []byte(payload), true
}

func structureRefs(value any) []structureRef {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	refs := make([]structureRef, 0, len(values))
	for _, raw := range values {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ref := structureRef{
			Type:      stringField(tool, "type"),
			Name:      stringField(tool, "name"),
			Namespace: stringField(tool, "namespace"),
			Execution: stringField(tool, "execution"),
			Tools:     structureRefs(tool["tools"]),
		}
		if deferLoading, ok := tool["defer_loading"].(bool); ok {
			ref.DeferLoading = &deferLoading
		}
		refs = append(refs, ref)
	}
	return refs
}

func structureItems(value any) []structureItem {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	items := make([]structureItem, 0, len(values))
	for _, raw := range values {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		items = append(items, structureItem{
			Type:      stringField(item, "type"),
			Name:      stringField(item, "name"),
			Namespace: stringField(item, "namespace"),
			CallID:    stringField(item, "call_id"),
			Execution: stringField(item, "execution"),
			Tools:     structureRefs(item["tools"]),
		})
	}
	return items
}

func probeRequested(headers http.Header) bool {
	return headers != nil && strings.EqualFold(strings.TrimSpace(headers.Get(probeRequestHeader)), "1")
}

func okEnvelope(value any) ([]byte, error) {
	result, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: result})
}

func errorEnvelope(code, message string) []byte {
	raw, errMarshal := json.Marshal(envelope{
		OK: false,
		Error: &envelopeError{
			Code:    code,
			Message: message,
		},
	})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error"}}`)
	}
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		// Allocation failure must not panic across the cgo boundary; report a
		// plugin error the host can log and skip instead.
		raw = errorEnvelope("plugin_error", "failed to allocate response buffer")
		ptr = C.CBytes(raw)
		if ptr == nil {
			return
		}
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
