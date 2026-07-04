// fingerprint-observatory — a thin CLIProxyAPI plugin that adds a "指纹观测台" menu to the
// management panel and serves a dashboard page. The page fetches the fork's core endpoint
// GET /v0/management/fingerprint-observe (fed by internal/fpobserve) for live per-account
// outbound-fingerprint data. The plugin itself holds no fingerprint logic — the executor
// is the only place that can see the applied fingerprint, so the data lives in core and the
// plugin is UI-only. Pure C-ABI + stdlib (no fork module dependency); built with
// `go build -buildmode=c-shared`.
package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
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
static void store_host_api(const cliproxy_host_api* host) { stored_host = host; }
*/
import "C"

import (
	_ "embed"
	"encoding/json"
	"unsafe"
)

//go:embed ui.html
var uiHTML []byte

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

type pluginMeta struct {
	Name             string `json:"Name"`
	Version          string `json:"Version"`
	Author           string `json:"Author"`
	GitHubRepository string `json:"GitHubRepository"`
	Logo             string `json:"Logo"`
	ConfigFields     []any  `json:"ConfigFields"`
}
type registerResult struct {
	SchemaVersion int        `json:"schema_version"`
	Metadata      pluginMeta `json:"metadata"`
	Capabilities  struct {
		ManagementAPI bool `json:"management_api"`
	} `json:"capabilities"`
}
type resourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}
type mgmtRegisterResult struct {
	Resources []resourceRoute `json:"resources"`
}
type mgmtResponse struct {
	StatusCode int                 `json:"StatusCode"`
	Headers    map[string][]string `json:"Headers"`
	Body       []byte              `json:"Body"` // json.Marshal base64-encodes []byte
}

func meta() pluginMeta {
	return pluginMeta{
		Name:             "fingerprint-observatory",
		Version:          "0.1.0",
		Author:           "fingerprint-hardening fork",
		GitHubRepository: "https://github.com/qinghua362330/CLIProxyAPI",
		Logo:             "",
		ConfigFields:     []any{},
	}
}

func registerEnvelope() ([]byte, error) {
	r := registerResult{SchemaVersion: 1, Metadata: meta()}
	r.Capabilities.ManagementAPI = true
	return okEnvelope(r)
}

func handleMethod(method string) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		return registerEnvelope()
	case "management.register":
		return okEnvelope(mgmtRegisterResult{Resources: []resourceRoute{{
			Path:        "/ui",
			Menu:        "指纹观测台",
			Description: "每账号实际出站指纹(UA / TLS profile / 关键头形状)观测与自洽性检查",
		}}})
	case "management.handle":
		return okEnvelope(mgmtResponse{
			StatusCode: 200,
			Headers:    map[string][]string{"content-type": {"text/html; charset=utf-8"}},
			Body:       uiHTML,
		})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func okEnvelope(v any) ([]byte, error) {
	result, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
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
	raw, err := handleMethod(C.GoString(method))
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	_ = request
	_ = requestLen
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}
