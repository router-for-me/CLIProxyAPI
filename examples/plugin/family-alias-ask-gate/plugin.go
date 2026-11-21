package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef struct { uint32_t abi_version; void* host_ctx; void* call; void* free_buffer; } cliproxy_host_api;
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
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type gateContext struct {
	Requested        string               `json:"requested"`
	Models           map[string]ModelTier `json:"models"`
	ApprovedModel    string               `json:"approved_model"`
	AskAnswer        string               `json:"ask_answer"`
	SameModelRetries int                  `json:"same_model_retries"`
}

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
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var input []byte
	if request != nil && requestLen > 0 {
		input = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), input)
	if err != nil {
		// Never echo the request body or headers, which can contain credentials.
		writeResponse(response, errorEnvelope("invalid_gate", "family alias gate input is invalid"))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, _ C.size_t) {
	C.free(ptr)
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(map[string]any{
			"schema_version": pluginabi.SchemaVersion,
			"metadata": map[string]any{
				"Name": "family-alias-ask-gate", "Version": "0.1.0",
				"GitHubRepository": "https://github.com/RaviTharuma/CLIProxyAPI",
			},
			"capabilities": map[string]bool{"request_interceptor": true},
		})
	case pluginabi.MethodRequestInterceptBefore:
		var request pluginapi.RequestInterceptRequest
		if err := json.Unmarshal(raw, &request); err != nil {
			return nil, err
		}
		result, err := interceptRequest(request)
		if err != nil {
			return nil, err
		}
		return okEnvelope(result)
	case pluginabi.MethodRequestInterceptAfter:
		return okEnvelope(pluginapi.RequestInterceptResponse{})
	case pluginabi.MethodPluginQuiesce, pluginabi.MethodPluginShutdown:
		return okEnvelope(struct{}{})
	default:
		return errorEnvelope("unknown_method", "unsupported plugin method"), nil
	}
}

func interceptRequest(request pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	value, isFallback := request.Metadata["family_alias_ask_gate"]
	if !isFallback {
		// A primary request is not a fallback and must not be blocked or swapped.
		return pluginapi.RequestInterceptResponse{}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return pluginapi.RequestInterceptResponse{}, errInvalidGate
	}
	var gate gateContext
	if err := json.Unmarshal(raw, &gate); err != nil || gate.Requested == "" || request.Model == "" {
		return terminated(stop(request.RequestedModel, false, "invalid_gate_context")), nil
	}
	var ask Ask
	if gate.AskAnswer == "allow" && gate.ApprovedModel == request.Model {
		ask = func(_, _ string) bool { return true }
	}
	// Metadata is trusted host context, never taken from the client body. It
	// carries an actual Ask decision for this one exact proposed candidate.
	decision := Intercept(gate.Requested, request.Model, consentHeader(request.Headers), gate.Models, gate.SameModelRetries, ask)
	if decision.Stopped {
		return terminated(decision), nil
	}
	return pluginapi.RequestInterceptResponse{}, nil
}

func consentHeader(headers http.Header) string {
	var values []string
	for name, current := range headers {
		if http.CanonicalHeaderKey(name) == "X-Open-Design-Cross-Family" {
			values = append(values, current...)
		}
	}
	if len(values) == 1 {
		return values[0]
	}
	return ""
}

func terminated(decision Decision) pluginapi.RequestInterceptResponse {
	body, _ := json.Marshal(map[string]any{
		"error":                map[string]string{"code": decision.Reason, "message": decision.Line},
		"cross_family_consent": decision.CrossFamilyConsent,
		"requested_model":      decision.Model,
	})
	return pluginapi.RequestInterceptResponse{
		Terminate: true, StatusCode: http.StatusConflict,
		ResponseHeaders: http.Header{"Content-Type": {"application/json"}},
		ResponseBody:    body,
	}
}

func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := json.Marshal(envelope{Error: &envelopeError{Code: code, Message: message}})
	return raw
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if len(raw) == 0 {
		return
	}
	response.ptr = C.CBytes(raw)
	response.len = C.size_t(len(raw))
}
