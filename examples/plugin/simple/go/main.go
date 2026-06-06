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
*/
import "C"

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

var usageCount atomic.Int64

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

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar           bool                         `json:"model_registrar"`
	ModelProvider            bool                         `json:"model_provider"`
	AuthProvider             bool                         `json:"auth_provider"`
	FrontendAuthProvider     bool                         `json:"frontend_auth_provider"`
	Executor                 bool                         `json:"executor"`
	ExecutorModelScope       pluginapi.ExecutorModelScope `json:"executor_model_scope"`
	ExecutorInputFormats     []string                     `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats    []string                     `json:"executor_output_formats,omitempty"`
	RequestTranslator        bool                         `json:"request_translator"`
	RequestNormalizer        bool                         `json:"request_normalizer"`
	ResponseTranslator       bool                         `json:"response_translator"`
	ResponseBeforeTranslator bool                         `json:"response_before_translator"`
	ResponseAfterTranslator  bool                         `json:"response_after_translator"`
	ThinkingApplier          bool                         `json:"thinking_applier"`
	UsagePlugin              bool                         `json:"usage_plugin"`
	CommandLinePlugin        bool                         `json:"command_line_plugin"`
	ManagementAPI            bool                         `json:"management_api"`
}

type identifierResponse struct {
	Identifier string `json:"identifier"`
}

type streamResponse struct {
	Headers http.Header                     `json:"headers,omitempty"`
	Chunks  []pluginapi.ExecutorStreamChunk `json:"chunks,omitempty"`
}

type managementRegistrationResponse struct {
	Routes []pluginapi.ManagementRoute `json:"routes,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
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
		return okEnvelope(exampleRegistration())
	case pluginabi.MethodModelRegister:
		return okEnvelope(pluginapi.ModelRegistrationResponse{Provider: "plugin-example", Models: exampleModels()})
	case pluginabi.MethodModelStatic, pluginabi.MethodModelForAuth:
		return okEnvelope(pluginapi.ModelResponse{Provider: "plugin-example", Models: exampleModels()})
	case pluginabi.MethodAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: "plugin-example"})
	case pluginabi.MethodAuthParse:
		return okEnvelope(pluginapi.AuthParseResponse{Handled: true, Auth: exampleAuthData(request)})
	case pluginabi.MethodAuthLoginStart:
		return okEnvelope(pluginapi.AuthLoginStartResponse{
			Provider:  "plugin-example",
			URL:       "https://example.invalid/plugin-login",
			State:     "example-state",
			ExpiresAt: time.Now().Add(5 * time.Minute).UTC(),
		})
	case pluginabi.MethodAuthLoginPoll:
		return okEnvelope(pluginapi.AuthLoginPollResponse{Status: pluginapi.AuthLoginStatusError, Message: "example plugin has no interactive login"})
	case pluginabi.MethodAuthRefresh:
		return okEnvelope(pluginapi.AuthRefreshResponse{Auth: exampleAuthData(request)})
	case pluginabi.MethodFrontendAuthIdentifier:
		return okEnvelope(identifierResponse{Identifier: "plugin-example"})
	case pluginabi.MethodFrontendAuthAuthenticate:
		return okEnvelope(pluginapi.FrontendAuthResponse{Authenticated: true, Principal: "plugin-example"})
	case pluginabi.MethodExecutorIdentifier:
		return okEnvelope(identifierResponse{Identifier: "plugin-example"})
	case pluginabi.MethodExecutorExecute:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"id":"plugin-example","object":"chat.completion"}`)})
	case pluginabi.MethodExecutorExecuteStream:
		return okEnvelope(streamResponse{Chunks: []pluginapi.ExecutorStreamChunk{{Payload: []byte("plugin-example")}}})
	case pluginabi.MethodExecutorCountTokens:
		return okEnvelope(pluginapi.ExecutorResponse{Payload: []byte(`{"total_tokens":0}`)})
	case pluginabi.MethodExecutorHTTPRequest:
		return okEnvelope(pluginapi.ExecutorHTTPResponse{StatusCode: http.StatusOK, Body: []byte(`{"plugin":"example"}`)})
	case pluginabi.MethodRequestTranslate, pluginabi.MethodRequestNormalize:
		return payloadEcho(request)
	case pluginabi.MethodResponseTranslate, pluginabi.MethodResponseNormalizeBefore, pluginabi.MethodResponseNormalizeAfter:
		return responsePayloadEcho(request)
	case pluginabi.MethodThinkingIdentifier:
		return okEnvelope(identifierResponse{Identifier: "plugin-example"})
	case pluginabi.MethodThinkingApply:
		return applyThinking(request)
	case pluginabi.MethodUsageHandle:
		usageCount.Add(1)
		return okEnvelope(map[string]any{})
	case pluginabi.MethodCommandLineRegister:
		return okEnvelope(pluginapi.CommandLineRegistrationResponse{Flags: []pluginapi.CommandLineFlag{{
			Name:  "plugin-example-command",
			Usage: "Run the example C ABI plugin command",
			Type:  "bool",
		}}})
	case pluginabi.MethodCommandLineExecute:
		return okEnvelope(pluginapi.CommandLineExecutionResponse{Stdout: []byte("plugin example command\n")})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistrationResponse{Routes: []pluginapi.ManagementRoute{{
			Method:      http.MethodGet,
			Path:        "/plugins/example/status",
			Menu:        "Example Plugin",
			Description: "Shows example plugin status.",
		}}})
	case pluginabi.MethodManagementHandle:
		return okEnvelope(pluginapi.ManagementResponse{StatusCode: http.StatusOK, Body: []byte(`{"plugin":"example"}`)})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func exampleRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             "example",
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "config1", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enables the example boolean option."},
				{Name: "config2", Type: pluginapi.ConfigFieldTypeString, Description: "Stores the example string option."},
				{Name: "config3", Type: pluginapi.ConfigFieldTypeInteger, Description: "Stores the example integer option."},
				{Name: "mode", Type: pluginapi.ConfigFieldTypeEnum, EnumValues: []string{"safe", "fast"}, Description: "Selects the example execution mode."},
			},
		},
		Capabilities: registrationCapability{
			ModelRegistrar:           true,
			ModelProvider:            true,
			AuthProvider:             true,
			FrontendAuthProvider:     true,
			Executor:                 true,
			ExecutorModelScope:       pluginapi.ExecutorModelScopeBoth,
			ExecutorInputFormats:     []string{"chat-completions"},
			ExecutorOutputFormats:    []string{"chat-completions"},
			RequestTranslator:        true,
			RequestNormalizer:        true,
			ResponseTranslator:       true,
			ResponseBeforeTranslator: true,
			ResponseAfterTranslator:  true,
			ThinkingApplier:          true,
			UsagePlugin:              true,
			CommandLinePlugin:        true,
			ManagementAPI:            true,
		},
	}
}

func exampleModels() []pluginapi.ModelInfo {
	return []pluginapi.ModelInfo{{
		ID:                         "plugin-example-model",
		Object:                     "model",
		OwnedBy:                    "plugin-example",
		DisplayName:                "Plugin Example Model",
		SupportedGenerationMethods: []string{"chat"},
		ContextLength:              8192,
		MaxCompletionTokens:        1024,
		UserDefined:                true,
	}}
}

func exampleAuthData(raw []byte) pluginapi.AuthData {
	return pluginapi.AuthData{
		Provider:    "plugin-example",
		ID:          "plugin-example",
		FileName:    "plugin-example.json",
		Label:       "Plugin Example",
		StorageJSON: append([]byte(nil), raw...),
		Metadata:    map[string]any{"type": "plugin-example"},
	}
}

func payloadEcho(raw []byte) ([]byte, error) {
	var req pluginapi.RequestTransformRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return okEnvelope(pluginapi.PayloadResponse{Body: req.Body})
}

func responsePayloadEcho(raw []byte) ([]byte, error) {
	var req pluginapi.ResponseTransformRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return okEnvelope(pluginapi.PayloadResponse{Body: req.Body})
}

func applyThinking(raw []byte) ([]byte, error) {
	var req pluginapi.ThinkingApplyRequest
	if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	body := map[string]any{}
	_ = json.Unmarshal(req.Body, &body)
	body["plugin_example_thinking"] = map[string]any{
		"mode":   req.Config.Mode,
		"budget": req.Config.Budget,
		"level":  req.Config.Level,
	}
	out, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return okEnvelope(pluginapi.PayloadResponse{Body: out})
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
