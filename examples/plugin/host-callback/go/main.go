package main

/*
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

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"net/http"
	"time"
	"unsafe"
)

const abiVersion uint32 = 1

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	raw, errHandle := handleMethod(C.GoString(method))
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
	_ = request
	_ = requestLen
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string) ([]byte, error) {
	_ = http.StatusOK
	_ = time.Second
	switch method {
	case "plugin.register":
		return okEnvelopeJSON("{\"schema_version\":1,\"metadata\":{\"Name\":\"example-host-callback-go\",\"Version\":\"0.1.0\",\"Author\":\"router-for-me\",\"GitHubRepository\":\"https://github.com/router-for-me/CLIProxyAPI\",\"Logo\":\"https://example.invalid/example-host-callback-go.png\",\"ConfigFields\":[]},\"capabilities\":{\"management_api\":true}}")
	case "plugin.reconfigure":
		return okEnvelopeJSON("{\"schema_version\":1,\"metadata\":{\"Name\":\"example-host-callback-go\",\"Version\":\"0.1.0\",\"Author\":\"router-for-me\",\"GitHubRepository\":\"https://github.com/router-for-me/CLIProxyAPI\",\"Logo\":\"https://example.invalid/example-host-callback-go.png\",\"ConfigFields\":[]},\"capabilities\":{\"management_api\":true}}")
	case "management.register":
		return okEnvelopeJSON("{\"resources\":[{\"Path\":\"/status\",\"Menu\":\"Host Callback\",\"Description\":\"CPA exposes this menu resource under /v0/resource/plugins/example-host-callback-go/status.\"}]}")
	case "management.handle":
		callHost("host.log", []byte(`{"level":"info","message":"example-host-callback-go host callback log","fields":{"plugin":"example-host-callback-go"}}`))
		callHost("host.http.do", []byte(`{"method":"GET","url":"https://example.com","headers":{"user-agent":["example-host-callback-go"]}}`))
		return okEnvelopeJSON("{\"StatusCode\":200,\"Headers\":{\"content-type\":[\"text/html; charset=utf-8\"]},\"Body\":\"PCFkb2N0eXBlIGh0bWw+PHRpdGxlPkhvc3QgQ2FsbGJhY2s8L3RpdGxlPjxtYWluPkhvc3QgQ2FsbGJhY2sgcmVzb3VyY2U8L21haW4+\"}")
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func okEnvelopeJSON(result string) ([]byte, error) {
	return json.Marshal(envelope{OK: true, Result: json.RawMessage(result)})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
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

func callHost(method string, payload []byte) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var req *C.uint8_t
	if len(payload) > 0 {
		req = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(req))
	}
	if C.call_host_api(cMethod, req, C.size_t(len(payload)), &response) == 0 && response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
}
