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
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginIdentifier = "model-sequence-router"
	pluginVersion    = "0.3.1"
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

type lifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      pluginapi.Metadata     `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type registrationCapability struct {
	ModelRegistrar bool `json:"model_registrar"`
	ModelRouter    bool `json:"model_router"`
	UsagePlugin    bool `json:"usage_plugin"`
}

type rpcModelRouteRequest struct {
	pluginapi.ModelRouteRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type runtimeState struct {
	configMu        sync.Mutex
	config          atomic.Pointer[compiledConfig]
	cursors         *cursorStore
	observations    *laneObservationStore
	cleanup         cleanupLoop
	loadedAt        time.Time
	clock           func() time.Time
	fingerprintSalt []byte
	logger          pluginLogger
	diagnosticMu    sync.RWMutex
	diagnostic      *diagnosticSink
}

func newRuntimeState(clock func() time.Time) *runtimeState {
	now := time.Now
	if clock != nil {
		now = clock
	}
	return &runtimeState{
		cursors:         newCursorStore(clock),
		observations:    newLaneObservationStore(clock),
		loadedAt:        stableLoadTime(now()),
		clock:           now,
		fingerprintSalt: newFingerprintSalt(),
		logger:          hostPluginLogger,
	}
}

var runtimePlugin = newRuntimeState(nil)

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
func cliproxyPluginShutdown() {
	runtimePlugin.shutdown()
}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		if errConfigure := runtimePlugin.configure(request); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodModelRegister:
		return okEnvelope(runtimePlugin.registeredModels())
	case pluginabi.MethodModelRoute:
		var req rpcModelRouteRequest
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode model route request: %w", errUnmarshal)
		}
		return okEnvelope(runtimePlugin.routeWithCallback(req.ModelRouteRequest, req.HostCallbackID))
	case pluginabi.MethodUsageHandle:
		var record pluginapi.UsageRecord
		if errUnmarshal := json.Unmarshal(request, &record); errUnmarshal != nil {
			return nil, fmt.Errorf("decode usage record: %w", errUnmarshal)
		}
		runtimePlugin.handleUsage(record)
		return okEnvelope(map[string]any{})
	case pluginabi.MethodPluginShutdown:
		runtimePlugin.shutdown()
		return okEnvelope(map[string]any{})
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func (r *runtimeState) configure(raw []byte) error {
	var req lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
		}
	}
	r.configMu.Lock()
	defer r.configMu.Unlock()
	generation := uint64(1)
	if current := r.config.Load(); current != nil {
		generation = current.Generation + 1
	}
	next, errCompile := decodeAndCompileConfig(req.ConfigYAML, generation)
	if errCompile != nil {
		return errCompile
	}
	nextDiagnostic, errDiagnostic := newDiagnosticSink(next.Diagnostics, r.clock)
	if errDiagnostic != nil {
		return errDiagnostic
	}
	r.config.Store(next)
	r.cursors.reset()
	r.observations.reset()
	r.cleanup.restart(r.cursors, next.SessionTTL)
	r.replaceDiagnosticSink(nextDiagnostic)
	lengths := make(map[string]int, len(next.Aliases))
	for _, alias := range next.Aliases {
		lengths[alias.Alias] = len(alias.Sequence)
	}
	r.log("info", "model-sequence-router: configuration loaded and state reset", map[string]any{
		"event": "config", "alias_count": len(next.Aliases), "generation": generation, "sequence_lengths": lengths,
	}, "")
	return nil
}

func (r *runtimeState) loadedConfig() *compiledConfig {
	return r.config.Load()
}

func (r *runtimeState) shutdown() {
	if r == nil {
		return
	}
	r.cleanup.stop()
	r.replaceDiagnosticSink(nil)
	r.observations.reset()
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginIdentifier,
			Version:          pluginVersion,
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			ConfigFields: []pluginapi.ConfigField{
				{Name: "enabled", Type: pluginapi.ConfigFieldTypeBoolean, Description: "Enable per-conversation model sequence routing."},
				{Name: "session_ttl", Type: pluginapi.ConfigFieldTypeString, Description: "Sliding in-memory conversation cursor TTL (1m through 24h)."},
				{Name: "diagnostics", Type: pluginapi.ConfigFieldTypeObject, Description: "Bounded content-free JSONL routing and cache diagnostics."},
				{Name: "aliases", Type: pluginapi.ConfigFieldTypeArray, Description: "Client-visible aliases and ordered provider/model target sequences."},
			},
		},
		Capabilities: registrationCapability{ModelRegistrar: true, ModelRouter: true, UsagePlugin: true},
	}
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
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

func callHost(method string, payload []byte) error {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var request *C.uint8_t
	if len(payload) > 0 {
		request = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(request))
	}
	if callCode := C.call_host_api(cMethod, request, C.size_t(len(payload)), &response); callCode != 0 {
		return fmt.Errorf("host callback %s failed with code %d", method, int(callCode))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	return nil
}
