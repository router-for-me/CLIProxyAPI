package main

/*
#define _GNU_SOURCE
#include <dlfcn.h>
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
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

extern int JSHandlerPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void JSHandlerPluginFree(void*, size_t);
extern void JSHandlerPluginShutdown(void);

static const char* jshandler_shared_object_path() {
	Dl_info info;
	if (dladdr((void*)&JSHandlerPluginCall, &info) == 0 || info.dli_fname == NULL) {
		return NULL;
	}
	return info.dli_fname;
}

static int jshandler_call_host(cliproxy_host_api* api, const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	return api->call(api->host_ctx, method, request, request_len, response);
}

static void jshandler_free_host_buffer(cliproxy_host_api* api, void* ptr, size_t len) {
	api->free_buffer(ptr, len);
}
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var jsHandlerABIState = struct {
	sync.RWMutex
	host         *C.cliproxy_host_api
	plugin       *jsHandlerPlugin
	shuttingDown bool
	inFlight     sync.WaitGroup
}{}

const maxCGoBytesLen = C.size_t(1<<31 - 1)

type abiEnvelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *abiError       `json:"error,omitempty"`
}

type abiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type abiLifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
	PluginDir  string `json:"plugin_dir,omitempty"`
}

type abiRequestInterceptRequest struct {
	pluginapi.RequestInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiResponseInterceptRequest struct {
	pluginapi.ResponseInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiStreamChunkInterceptRequest struct {
	pluginapi.StreamChunkInterceptRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type abiHostLogRequest struct {
	HostCallbackID string         `json:"host_callback_id,omitempty"`
	Level          string         `json:"level,omitempty"`
	Message        string         `json:"message,omitempty"`
	Fields         map[string]any `json:"fields,omitempty"`
}

type abiRegistration struct {
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  abiCapabilities    `json:"capabilities"`
}

type abiCapabilities struct {
	RequestInterceptor     bool `json:"request_interceptor"`
	ResponseInterceptor    bool `json:"response_interceptor"`
	StreamChunkInterceptor bool `json:"response_stream_interceptor"`
}

type abiIdentifierResponse struct {
	Identifier string `json:"identifier"`
}

func main() {}

func inferPluginDir() string {
	sharedObjectPath := C.jshandler_shared_object_path()
	if sharedObjectPath == nil {
		return ""
	}
	return filepath.Dir(C.GoString(sharedObjectPath))
}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil {
		return 1
	}
	jsHandlerABIState.Lock()
	jsHandlerABIState.host = host
	jsHandlerABIState.shuttingDown = false
	jsHandlerABIState.Unlock()

	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.JSHandlerPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.JSHandlerPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.JSHandlerPluginShutdown)
	return 0
}

//export JSHandlerPluginCall
func JSHandlerPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeABIResponse(response, abiErrorEnvelope("invalid_method", "method is required"))
		return 0
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		if requestLen > maxCGoBytesLen {
			writeABIResponse(response, abiErrorEnvelope("request_too_large", "request payload is too large"))
			return 0
		}
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleJSHandlerABIMethod(context.Background(), C.GoString(method), requestBytes)
	if errHandle != nil {
		writeABIResponse(response, abiErrorEnvelope("plugin_error", errHandle.Error()))
		return 0
	}
	writeABIResponse(response, raw)
	return 0
}

//export JSHandlerPluginFree
func JSHandlerPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export JSHandlerPluginShutdown
func JSHandlerPluginShutdown() {
	jsHandlerABIState.Lock()
	jsHandlerABIState.shuttingDown = true
	jsHandlerABIState.plugin = nil
	jsHandlerABIState.host = nil
	jsHandlerABIState.Unlock()
	jsHandlerABIState.inFlight.Wait()
}

func handleJSHandlerABIMethod(ctx context.Context, method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return handleJSHandlerRegister(request)
	}

	p, done, errPlugin := beginJSHandlerPluginCall()
	if errPlugin != nil {
		return nil, errPlugin
	}
	defer done()
	switch method {
	case pluginabi.MethodRequestInterceptBefore:
		var req abiRequestInterceptRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.interceptRequest(ctx, req.RequestInterceptRequest, req.HostCallbackID)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodResponseInterceptAfter:
		var req abiResponseInterceptRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.interceptResponse(ctx, req.ResponseInterceptRequest, req.HostCallbackID)
		return abiOKEnvelopeWithError(resp, errCall)
	case pluginabi.MethodResponseInterceptStreamChunk:
		var req abiStreamChunkInterceptRequest
		if errDecode := json.Unmarshal(request, &req); errDecode != nil {
			return nil, errDecode
		}
		resp, errCall := p.interceptStreamChunk(ctx, req.StreamChunkInterceptRequest, req.HostCallbackID)
		return abiOKEnvelopeWithError(resp, errCall)
	default:
		return abiErrorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleJSHandlerRegister(request []byte) ([]byte, error) {
	var req abiLifecycleRequest
	if errDecode := json.Unmarshal(request, &req); errDecode != nil {
		return nil, errDecode
	}
	plugin, errBuild := buildPlugin(req.ConfigYAML, req.PluginDir)
	if errBuild != nil {
		return nil, errBuild
	}
	p, ok := plugin.Capabilities.RequestInterceptor.(*jsHandlerPlugin)
	if !ok || p == nil {
		return nil, fmt.Errorf("jshandler plugin registration returned invalid interceptor")
	}
	jsHandlerABIState.Lock()
	jsHandlerABIState.plugin = p
	jsHandlerABIState.shuttingDown = false
	jsHandlerABIState.Unlock()
	return abiOKEnvelope(abiRegistration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata:      plugin.Metadata,
		Capabilities: abiCapabilities{
			RequestInterceptor:     plugin.Capabilities.RequestInterceptor != nil,
			ResponseInterceptor:    plugin.Capabilities.ResponseInterceptor != nil,
			StreamChunkInterceptor: plugin.Capabilities.StreamChunkInterceptor != nil,
		},
	})
}

func beginJSHandlerPluginCall() (*jsHandlerPlugin, func(), error) {
	jsHandlerABIState.Lock()
	defer jsHandlerABIState.Unlock()
	if jsHandlerABIState.shuttingDown {
		return nil, nil, fmt.Errorf("jshandler plugin is shutting down")
	}
	if jsHandlerABIState.plugin == nil {
		return nil, nil, fmt.Errorf("jshandler plugin is not registered")
	}
	jsHandlerABIState.inFlight.Add(1)
	return jsHandlerABIState.plugin, jsHandlerABIState.inFlight.Done, nil
}

func abiOKEnvelopeWithError(v any, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	}
	return abiOKEnvelope(v)
}

func abiOKEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(abiEnvelope{OK: true, Result: raw})
}

func abiErrorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(abiEnvelope{OK: false, Error: &abiError{Code: code, Message: message}})
	return raw
}

func writeABIResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func newHostJSConsoleLogger(hostCallbackID string) jsConsoleLogger {
	return func(message string) error {
		if errLog := writeHostJSConsoleLog(hostCallbackID, message); errLog != nil {
			return defaultJSConsoleLogger(message)
		}
		return nil
	}
}

func writeHostJSConsoleLog(hostCallbackID string, message string) error {
	raw, errMarshal := json.Marshal(abiHostLogRequest{
		HostCallbackID: hostCallbackID,
		Level:          "info",
		Message:        "JS console log: " + message,
		Fields: map[string]any{
			"plugin_id": pluginName,
		},
	})
	if errMarshal != nil {
		return errMarshal
	}

	rawResp, errCall := callHost(pluginabi.MethodHostLog, raw)
	if errCall != nil {
		return errCall
	}
	if len(rawResp) == 0 {
		return nil
	}
	var resp abiEnvelope
	if errDecode := json.Unmarshal(rawResp, &resp); errDecode != nil {
		return fmt.Errorf("decode host log response: %w", errDecode)
	}
	if !resp.OK {
		if resp.Error != nil {
			return fmt.Errorf("host log failed: %s", resp.Error.Message)
		}
		return fmt.Errorf("host log failed")
	}
	return nil
}

func callHost(method string, payload []byte) ([]byte, error) {
	jsHandlerABIState.RLock()
	defer jsHandlerABIState.RUnlock()
	if jsHandlerABIState.host == nil {
		return nil, fmt.Errorf("host callback is unavailable")
	}

	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var cPayload unsafe.Pointer
	if len(payload) > 0 {
		cPayload = C.CBytes(payload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload")
		}
		defer C.free(cPayload)
	}

	var response C.cliproxy_buffer
	rc := C.jshandler_call_host(
		jsHandlerABIState.host,
		cMethod,
		(*C.uint8_t)(cPayload),
		C.size_t(len(payload)),
		&response,
	)
	var out []byte
	if response.ptr != nil && response.len > 0 {
		out = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.jshandler_free_host_buffer(jsHandlerABIState.host, response.ptr, response.len)
	}
	if rc != 0 {
		return nil, fmt.Errorf("host callback %s returned %d: %s", method, int(rc), string(out))
	}
	return out, nil
}
