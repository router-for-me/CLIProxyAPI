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
	"context"
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

const pluginIdentifier = "claude-web-search-router"

type routeBackend string

const (
	backendFallback          routeBackend = "fallback"
	backendAntigravityGoogle routeBackend = "antigravity_google"
	backendCodexWebSearch    routeBackend = "codex_web_search"
	backendXAIWebSearch      routeBackend = "xai_web_search"
	backendTavily            routeBackend = "tavily"
	backendDefaultProvider   routeBackend = "default_provider"
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
	Enabled              bool     `yaml:"enabled"`
	Route                string   `yaml:"route"`
	AntigravityModel     string   `yaml:"antigravity_model"`
	CodexModel           string   `yaml:"codex_model"`
	XAIModel             string   `yaml:"xai_model"`
	DefaultProvider      string   `yaml:"default_provider"`
	DefaultProviderModel string   `yaml:"default_provider_model"`
	TavilyAPIKeys        []string `yaml:"tavily_api_keys"`
	RequireWebSearchOnly bool     `yaml:"require_web_search_only"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRouter           bool     `json:"model_router"`
	Executor              bool     `json:"executor"`
	ExecutorModelScope    string   `json:"executor_model_scope"`
	ExecutorInputFormats  []string `json:"executor_input_formats"`
	ExecutorOutputFormats []string `json:"executor_output_formats"`
}

type rpcExecutorRequest struct {
	pluginapi.ExecutorRequest
	StreamID       string `json:"stream_id,omitempty"`
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
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
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
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
	case pluginabi.MethodModelRoute:
		return routeModel(request)
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(map[string]string{"identifier": pluginIdentifier})
	case pluginabi.MethodExecutorExecute:
		return execute(request)
	case pluginabi.MethodExecutorExecuteStream:
		return executeStream(request)
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"input_tokens":0}`)})
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
		decoded, errDecode := decodeConfig(req.ConfigYAML)
		if errDecode != nil {
			return errDecode
		}
		cfg = decoded
	}
	currentConfig.Store(cfg)
	return nil
}

func defaultPluginConfig() pluginConfig {
	return pluginConfig{
		Enabled:              true,
		Route:                string(backendFallback),
		RequireWebSearchOnly: true,
	}
}

func decodeConfig(raw []byte) (pluginConfig, error) {
	cfg := defaultPluginConfig()
	if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
		return pluginConfig{}, errUnmarshal
	}
	cfg.Route = strings.TrimSpace(cfg.Route)
	cfg.AntigravityModel = strings.TrimSpace(cfg.AntigravityModel)
	cfg.CodexModel = strings.TrimSpace(cfg.CodexModel)
	cfg.XAIModel = strings.TrimSpace(cfg.XAIModel)
	cfg.DefaultProvider = strings.ToLower(strings.TrimSpace(cfg.DefaultProvider))
	cfg.DefaultProviderModel = strings.TrimSpace(cfg.DefaultProviderModel)
	return cfg, nil
}

func loadedConfig() pluginConfig {
	raw := currentConfig.Load()
	if cfg, ok := raw.(pluginConfig); ok {
		return cfg
	}
	return defaultPluginConfig()
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "claude-web-search-router",
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "When false, the router declines all Claude web_search requests."},
				{Name: "route", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{
					string(backendFallback), string(backendAntigravityGoogle), string(backendCodexWebSearch),
					string(backendXAIWebSearch), string(backendTavily), string(backendDefaultProvider),
				}, Description: "Backend for Claude Code web_search. fallback (default): antigravity → codex → xai → tavily."},
				{Name: "antigravity_model", Type: pluginapi.ConfigFieldTypeString, Description: "Antigravity googleSearch model (empty: registry lookup, then first supports_web_search)."},
				{Name: "codex_model", Type: pluginapi.ConfigFieldTypeString, Description: "Codex Responses model for web_search (empty defaults to gpt-5.4, never client Claude model)."},
				{Name: "xai_model", Type: pluginapi.ConfigFieldTypeString, Description: "xAI Responses model with web_search (empty uses grok-4.3, not the client Claude model)."},
				{Name: "default_provider", Type: pluginapi.ConfigFieldTypeString, Description: "Built-in provider key when route=default_provider."},
				{Name: "default_provider_model", Type: pluginapi.ConfigFieldTypeString, Description: "Optional execution model on default_provider route."},
				{Name: "tavily_api_keys", Type: pluginapi.ConfigFieldTypeArray, Description: "Tavily API keys (round-robin) when route=tavily."},
				{Name: "require_web_search_only", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Require tools to be exclusively typed web_search (matches antigravity-only path)."},
			},
		},
		Capabilities: registrationCapability{
			ModelRouter:           true,
			Executor:              true,
			ExecutorModelScope:    string(pluginapi.ExecutorModelScopeStatic),
			ExecutorInputFormats:  []string{"claude"},
			ExecutorOutputFormats: []string{"claude"},
		},
	}
}

func routeModel(raw []byte) ([]byte, error) {
	var req rpcModelRouteRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	cfg := loadedConfig()
	if !cfg.Enabled {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	if !isClaudeSourceFormat(req.SourceFormat) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	if !isClaudeCodeBuiltinWebSearchRequest(req.Body, cfg.RequireWebSearchOnly) {
		return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
	}
	route := strings.TrimSpace(cfg.Route)
	if isFallbackRoute(route) {
		return okEnvelope(routeWithFallback(cfg, req.ModelRouteRequest))
	}
	if plans := executionPlansForRoute(cfg, req.ModelRouteRequest, route); len(plans) > 0 {
		return okEnvelope(pluginapi.ModelRouteResponse{
			Handled:    true,
			TargetKind: pluginapi.ModelRouteTargetSelf,
			Reason:     "claude_code_web_search_orchestrated",
		})
	}
	backend := routeBackend(route)
	resp, ok := tryRouteBackend(backend, cfg, req.ModelRouteRequest)
	if ok {
		return okEnvelope(resp)
	}
	if strings.TrimSpace(resp.Reason) != "" {
		return okEnvelope(resp)
	}
	return okEnvelope(pluginapi.ModelRouteResponse{Handled: false})
}

func hasProvider(providers []string, key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, p := range providers {
		if strings.ToLower(strings.TrimSpace(p)) == key {
			return true
		}
	}
	return false
}

func execute(raw []byte) ([]byte, error) {
	var req rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	body, headers, errRun := runWebSearchWithExecutionFallback(context.Background(), req.ExecutorRequest, req.HostCallbackID)
	if errRun != nil {
		return errorEnvelope("executor_error", errRun.Error()), nil
	}
	return okEnvelope(pluginapi.ExecutorResponse{Payload: body, Headers: headers})
}

func runTavilyClaude(ctx context.Context, req pluginapi.ExecutorRequest) ([]byte, http.Header, error) {
	return runTavilyClaudeWithClient(ctx, req, newTavilyClient(loadedConfig().TavilyAPIKeys))
}

func runTavilyClaudeWithClient(ctx context.Context, req pluginapi.ExecutorRequest, client *tavilyClient) ([]byte, http.Header, error) {
	query := extractClaudeWebSearchQuery(req.OriginalRequest)
	if query == "" {
		query = extractClaudeWebSearchQuery(req.Payload)
	}
	maxResults := extractClaudeWebSearchMaxUses(req.OriginalRequest, 5)
	hits, answer, errSearch := client.search(ctx, query, maxResults)
	if errSearch != nil {
		return nil, nil, errSearch
	}
	model := strings.TrimSpace(req.Model)
	builder := newClaudeStreamBuilder(model)
	payload := builder.buildMessageJSON(query, hits, answer)
	headers := http.Header{"Content-Type": []string{"application/json"}}
	return payload, headers, nil
}

func runTavilyClaudeStream(ctx context.Context, req pluginapi.ExecutorRequest) ([]byte, http.Header, error) {
	return runTavilyClaudeStreamWithClient(ctx, req, newTavilyClient(loadedConfig().TavilyAPIKeys))
}

func runTavilyClaudeStreamWithClient(ctx context.Context, req pluginapi.ExecutorRequest, client *tavilyClient) ([]byte, http.Header, error) {
	query := extractClaudeWebSearchQuery(req.OriginalRequest)
	if query == "" {
		query = extractClaudeWebSearchQuery(req.Payload)
	}
	maxResults := extractClaudeWebSearchMaxUses(req.OriginalRequest, 5)
	hits, answer, errSearch := client.search(ctx, query, maxResults)
	if errSearch != nil {
		return nil, nil, errSearch
	}
	model := strings.TrimSpace(req.Model)
	builder := newClaudeStreamBuilder(model)
	payload := builder.buildStreamWithQuery(query, hits, answer)
	headers := http.Header{"Content-Type": []string{"text/event-stream"}}
	return payload, headers, nil
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}

	var env envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func hostHTTPStatusFromError(err error) int {
	if err == nil {
		return 0
	}
	msg := err.Error()
	for _, code := range []int{429, 503, 502} {
		if strings.Contains(msg, fmt.Sprintf("%d", code)) {
			return code
		}
	}
	return 0
}

func isRetryableHTTPStatus(code int) bool {
	return code == 429 || code == 503 || code == 502
}
func okEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(envelope{OK: true, Result: raw})
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
