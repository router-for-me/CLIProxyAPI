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
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	pluginName          = "host-callback-auth-files"
	resourcePath        = "/status"
	resourceContentType = "text/html; charset=utf-8"
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

type registration struct {
	SchemaVersion uint32                   `json:"schema_version"`
	Metadata      pluginapi.Metadata       `json:"metadata"`
	Capabilities  registrationCapabilities `json:"capabilities"`
}

type registrationCapabilities struct {
	ManagementAPI bool `json:"management_api"`
}

type managementRegistration struct {
	Resources []managementResource `json:"resources,omitempty"`
}

type managementResource struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu"`
	Description string `json:"Description"`
}

type managementRequest struct {
	Method         string
	Path           string
	Headers        http.Header
	Query          url.Values
	Body           []byte
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}

type authOpOptions struct {
	Op        string
	AuthIndex string
	Name      string
	JSON      json.RawMessage
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
	_ = len
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {}

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(pluginRegistration())
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{
			Resources: []managementResource{{
				Path:        resourcePath,
				Menu:        "Host Auth Files",
				Description: "Lists auth files and demonstrates host.auth list/get/runtime/save callbacks.",
			}},
		})
	case pluginabi.MethodManagementHandle:
		return handleManagement(request)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func pluginRegistration() registration {
	return registration{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          "0.1.0",
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields:     []pluginapi.ConfigField{},
		},
		Capabilities: registrationCapabilities{
			ManagementAPI: true,
		},
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req managementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
		}
	}
	opts, errOptions := optionsFromManagementRequest(req)
	if errOptions != nil {
		page := renderPage(opts, nil, errOptions.Error())
		return okEnvelope(htmlResponse(http.StatusBadRequest, page))
	}
	result, errRun := runAuthOp(opts)
	if errRun != nil {
		page := renderPage(opts, nil, errRun.Error())
		return okEnvelope(htmlResponse(http.StatusOK, page))
	}
	page := renderPage(opts, result, "")
	return okEnvelope(htmlResponse(http.StatusOK, page))
}

func optionsFromManagementRequest(req managementRequest) (authOpOptions, error) {
	opts := authOpOptions{Op: "list"}
	if len(req.Body) > 0 {
		var bodyOpts authOpOptions
		if errUnmarshal := json.Unmarshal(req.Body, &bodyOpts); errUnmarshal != nil {
			return opts, fmt.Errorf("decode JSON request body: %w", errUnmarshal)
		}
		applyAuthOpOptions(&opts, bodyOpts)
	}
	if errApply := applyQueryAuthOptions(&opts, req.Query); errApply != nil {
		return opts, errApply
	}
	return opts, nil
}

func applyAuthOpOptions(dst *authOpOptions, src authOpOptions) {
	if strings.TrimSpace(src.Op) != "" {
		dst.Op = strings.ToLower(strings.TrimSpace(src.Op))
	}
	if strings.TrimSpace(src.AuthIndex) != "" {
		dst.AuthIndex = strings.TrimSpace(src.AuthIndex)
	}
	if strings.TrimSpace(src.Name) != "" {
		dst.Name = strings.TrimSpace(src.Name)
	}
	if len(src.JSON) > 0 && string(src.JSON) != "null" {
		dst.JSON = append(json.RawMessage(nil), src.JSON...)
	}
}

func applyQueryAuthOptions(opts *authOpOptions, query url.Values) error {
	if query == nil {
		return nil
	}
	if raw := strings.TrimSpace(query.Get("op")); raw != "" {
		opts.Op = strings.ToLower(raw)
	}
	if raw := strings.TrimSpace(query.Get("auth_index")); raw != "" {
		opts.AuthIndex = raw
	}
	if raw := strings.TrimSpace(query.Get("name")); raw != "" {
		opts.Name = raw
	}
	if raw := strings.TrimSpace(query.Get("json")); raw != "" {
		if !json.Valid([]byte(raw)) {
			return fmt.Errorf("query json must be valid JSON")
		}
		opts.JSON = json.RawMessage(raw)
	}
	return nil
}

func runAuthOp(opts authOpOptions) (any, error) {
	switch opts.Op {
	case "list", "":
		return callHostAuthList()
	case "get":
		if opts.AuthIndex == "" {
			return nil, fmt.Errorf("auth_index is required for op=get")
		}
		return callHostAuthGet(opts.AuthIndex)
	case "runtime", "get_runtime":
		if opts.AuthIndex == "" {
			return nil, fmt.Errorf("auth_index is required for op=runtime")
		}
		return callHostAuthGetRuntime(opts.AuthIndex)
	case "save":
		if opts.Name == "" {
			return nil, fmt.Errorf("name is required for op=save")
		}
		if len(opts.JSON) == 0 {
			return nil, fmt.Errorf("json is required for op=save")
		}
		return callHostAuthSave(opts.Name, opts.JSON)
	default:
		return nil, fmt.Errorf("unknown op %q: use list, get, runtime, or save", opts.Op)
	}
}

func callHostAuthList() (authListResponse, error) {
	result, errCall := callHost(pluginabi.MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return authListResponse{}, errCall
	}
	var resp authListResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return authListResponse{}, fmt.Errorf("decode host.auth.list result: %w", errUnmarshal)
	}
	return resp, nil
}

func callHostAuthGet(authIndex string) (pluginapi.HostAuthGetResponse, error) {
	result, errCall := callHost(pluginabi.MethodHostAuthGet, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return pluginapi.HostAuthGetResponse{}, errCall
	}
	var resp pluginapi.HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthGetResponse{}, fmt.Errorf("decode host.auth.get result: %w", errUnmarshal)
	}
	return resp, nil
}

func callHostAuthGetRuntime(authIndex string) (pluginapi.HostAuthGetRuntimeResponse, error) {
	result, errCall := callHost(pluginabi.MethodHostAuthGetRuntime, pluginapi.HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, errCall
	}
	var resp pluginapi.HostAuthGetRuntimeResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthGetRuntimeResponse{}, fmt.Errorf("decode host.auth.get_runtime result: %w", errUnmarshal)
	}
	return resp, nil
}

func callHostAuthSave(name string, rawJSON json.RawMessage) (pluginapi.HostAuthSaveResponse, error) {
	result, errCall := callHost(pluginabi.MethodHostAuthSave, pluginapi.HostAuthSaveRequest{
		Name: name,
		JSON: rawJSON,
	})
	if errCall != nil {
		return pluginapi.HostAuthSaveResponse{}, errCall
	}
	var resp pluginapi.HostAuthSaveResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return pluginapi.HostAuthSaveResponse{}, fmt.Errorf("decode host.auth.save result: %w", errUnmarshal)
	}
	return resp, nil
}

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate host callback payload %s", method)
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
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, errUnmarshal)
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

func htmlResponse(statusCode int, body []byte) managementResponse {
	return managementResponse{
		StatusCode: statusCode,
		Headers: http.Header{
			"content-type": []string{resourceContentType},
		},
		Body: body,
	}
}

func renderPage(opts authOpOptions, result any, errText string) []byte {
	var out bytes.Buffer
	out.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>Host Auth Files</title>")
	out.WriteString("<style>body{font-family:-apple-system,BlinkMacSystemFont,\"Segoe UI\",sans-serif;margin:2rem;line-height:1.45;color:#1f2933}code,pre{background:#f3f4f6;border-radius:6px}code{padding:.1rem .3rem}pre{padding:1rem;overflow:auto;white-space:pre-wrap}dl{display:grid;grid-template-columns:max-content 1fr;gap:.35rem 1rem}dt{font-weight:600}dd{margin:0}.error{color:#b42318}</style>")
	out.WriteString("</head><body><main>")
	out.WriteString("<h1>Host Auth Files</h1>")
	out.WriteString("<dl>")
	writeDefinition(&out, "op", opts.Op)
	if opts.AuthIndex != "" {
		writeDefinition(&out, "auth_index", opts.AuthIndex)
	}
	if opts.Name != "" {
		writeDefinition(&out, "name", opts.Name)
	}
	out.WriteString("</dl>")
	if errText != "" {
		out.WriteString("<h2>Error</h2><pre class=\"error\">")
		out.WriteString(html.EscapeString(errText))
		out.WriteString("</pre>")
	}
	if result != nil {
		out.WriteString("<h2>Result</h2><pre>")
		out.WriteString(html.EscapeString(prettyJSON(result)))
		out.WriteString("</pre>")
	}
	out.WriteString("<h2>Usage</h2><ul>")
	out.WriteString("<li><code>?op=list</code></li>")
	out.WriteString("<li><code>?op=get&amp;auth_index=&lt;AUTH_INDEX&gt;</code></li>")
	out.WriteString("<li><code>?op=runtime&amp;auth_index=&lt;AUTH_INDEX&gt;</code></li>")
	out.WriteString("<li><code>?op=save&amp;name=example.json&amp;json=...</code></li>")
	out.WriteString("</ul>")
	out.WriteString("</main></body></html>")
	return out.Bytes()
}

func writeDefinition(out *bytes.Buffer, key string, value string) {
	out.WriteString("<dt>")
	out.WriteString(html.EscapeString(key))
	out.WriteString("</dt><dd><code>")
	out.WriteString(html.EscapeString(value))
	out.WriteString("</code></dd>")
}

func prettyBody(raw []byte) string {
	var buf bytes.Buffer
	if errIndent := json.Indent(&buf, raw, "", "  "); errIndent == nil {
		return buf.String()
	}
	return string(raw)
}

func prettyJSON(v any) string {
	raw, errMarshal := json.MarshalIndent(v, "", "  ")
	if errMarshal != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(raw)
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

func cloneHeader(headers http.Header) http.Header {
	if headers == nil {
		return nil
	}
	cloned := make(http.Header, len(headers))
	for key, values := range headers {
		cloned[key] = append([]string(nil), values...)
	}
	return cloned
}

func cloneValues(values url.Values) url.Values {
	if values == nil {
		return nil
	}
	cloned := make(url.Values, len(values))
	for key, items := range values {
		cloned[key] = append([]string(nil), items...)
	}
	return cloned
}
