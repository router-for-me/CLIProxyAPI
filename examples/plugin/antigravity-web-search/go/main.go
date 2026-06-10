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
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

var currentConfig atomic.Value

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type pluginConfig struct {
	BackendModel string   `yaml:"backend_model"`
	MaxUses      int      `yaml:"max_uses"`
	BaseURLs     []string `yaml:"base_urls"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ServerToolHandler bool `json:"server_tool_handler"`
}

type rpcServerToolRequest struct {
	pluginapi.ServerToolRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type serverToolStreamResponse struct {
	Handled  bool                              `json:"handled"`
	Headers  http.Header                       `json:"headers,omitempty"`
	Chunks   []pluginapi.ServerToolStreamChunk `json:"chunks,omitempty"`
	Metadata map[string]any                    `json:"metadata,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
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
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodServerToolHandle:
		return handleServerTool(request, false)
	case pluginabi.MethodServerToolHandleStream:
		return handleServerTool(request, true)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg := defaultPluginConfig()
	if len(req.ConfigYAML) > 0 {
		if errUnmarshal := yaml.Unmarshal(req.ConfigYAML, &cfg); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg = normalizePluginConfig(cfg)
	currentConfig.Store(cfg)
	return nil
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		BackendModel: defaultWebSearchBackendModel,
		MaxUses:      8,
		BaseURLs: []string{
			"https://daily-cloudcode-pa.googleapis.com",
			"https://cloudcode-pa.googleapis.com",
		},
	}
}

func normalizePluginConfig(cfg pluginConfig) pluginConfig {
	if strings.TrimSpace(cfg.BackendModel) == "" {
		cfg.BackendModel = defaultWebSearchBackendModel
	}
	if cfg.MaxUses <= 0 {
		cfg.MaxUses = 8
	}
	if len(cfg.BaseURLs) == 0 {
		cfg.BaseURLs = defaultPluginConfig().BaseURLs
	}
	out := cfg.BaseURLs[:0]
	for _, baseURL := range cfg.BaseURLs {
		baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
		if baseURL != "" {
			out = append(out, baseURL)
		}
	}
	if len(out) == 0 {
		out = defaultPluginConfig().BaseURLs
	}
	cfg.BaseURLs = out
	return cfg
}

func loadedConfig() pluginConfig {
	cfg, _ := currentConfig.Load().(pluginConfig)
	if strings.TrimSpace(cfg.BackendModel) == "" {
		return defaultPluginConfig()
	}
	return normalizePluginConfig(cfg)
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "antigravity-web-search",
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields: []pluginapi.ConfigField{
				{
					Name:        "backend_model",
					Type:        pluginapi.ConfigFieldTypeString,
					Description: "Antigravity Gemini model used for the googleSearch backend.",
				},
				{
					Name:        "max_uses",
					Type:        pluginapi.ConfigFieldTypeInteger,
					Description: "Maximum Claude typed web_search uses accepted by this handler.",
				},
				{
					Name:        "base_urls",
					Type:        pluginapi.ConfigFieldTypeArray,
					Description: "Ordered Antigravity base URLs for the web-search backend.",
				},
			},
		},
		Capabilities: registrationCapability{ServerToolHandler: true},
	}
}

func handleServerTool(raw []byte, stream bool) ([]byte, error) {
	var req rpcServerToolRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if !shouldHandleServerTool(req.ServerToolRequest, loadedConfig()) {
		if stream {
			return okEnvelope(serverToolStreamResponse{Handled: false})
		}
		return okEnvelope(pluginapi.ServerToolResponse{Handled: false})
	}

	query := extractUserQuery(req.Payload)
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("no user query found for typed web_search request")
	}
	geminiResp, errSearch := executeAntigravityWebSearch(req, query, loadedConfig())
	if errSearch != nil {
		return nil, errSearch
	}
	if stream {
		events := convertGeminiToClaudeSSEStream(req.UpstreamModel, geminiResp)
		chunks := make([]pluginapi.ServerToolStreamChunk, 0, len(events))
		for _, event := range events {
			chunks = append(chunks, pluginapi.ServerToolStreamChunk{Payload: []byte(event)})
		}
		return okEnvelope(serverToolStreamResponse{
			Handled: true,
			Headers: http.Header{"Content-Type": {"text/event-stream"}},
			Chunks:  chunks,
		})
	}

	return okEnvelope(pluginapi.ServerToolResponse{
		Handled: true,
		Headers: http.Header{"Content-Type": {"application/json"}},
		Payload: []byte(convertGeminiToClaudeNonStream(req.UpstreamModel, geminiResp)),
	})
}

func okEnvelope(v any) ([]byte, error) {
	result, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: result})
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

func callHost(method string, payload []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var req *C.uint8_t
	if len(payload) > 0 {
		req = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(req))
	}
	if C.call_host_api(cMethod, req, C.size_t(len(payload)), &response) != 0 {
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if response.ptr == nil || response.len == 0 {
		return nil, nil
	}
	defer C.free_host_buffer(response.ptr, response.len)
	return C.GoBytes(response.ptr, C.int(response.len)), nil
}
