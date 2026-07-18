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
	"strconv"
	"strings"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	defaultModel        = "gpt-5.5"
	defaultPrompt       = "Summarize host model callbacks in one short sentence."
	pluginName          = "host-model-callback"
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

type managementBodyOptions struct {
	Model         string          `json:"model"`
	Mode          string          `json:"mode"`
	EntryProtocol string          `json:"entry_protocol"`
	ExitProtocol  string          `json:"exit_protocol"`
	Prompt        string          `json:"prompt"`
	Stream        *bool           `json:"stream"`
	Body          json.RawMessage `json:"body"`
	Headers       http.Header     `json:"headers"`
	Query         url.Values      `json:"query"`
	Alt           string          `json:"alt"`
	ImplicitClose *bool           `json:"implicit_close"`
}

type hostModelExecutionRequest struct {
	pluginapi.HostModelExecutionRequest
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type runOptions struct {
	Model          string
	Mode           string
	EntryProtocol  string
	ExitProtocol   string
	Prompt         string
	Stream         bool
	Body           []byte
	Headers        http.Header
	Query          url.Values
	Alt            string
	ImplicitClose  bool
	HostCallbackID string
}

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Stream   bool          `json:"stream"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type streamPageData struct {
	StatusCode int
	Headers    http.Header
	StreamID   string
	Chunks     []string
	Error      string
	CloseMode  string
	CloseError string
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
				Menu:        "Host Model Callback",
				Description: "Runs a model request through host.model callbacks and displays the result.",
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
		page := renderPage(opts, 0, nil, nil, nil, errOptions.Error(), "", "")
		return okEnvelope(htmlResponse(http.StatusBadRequest, page))
	}
	if opts.Stream {
		data := executeStream(opts)
		page := renderPage(opts, data.StatusCode, data.Headers, nil, data.Chunks, data.Error, data.CloseMode, data.CloseError)
		return okEnvelope(htmlResponse(http.StatusOK, page))
	}
	resp, errExecute := executeOnce(opts)
	if errExecute != nil {
		page := renderPage(opts, 0, nil, nil, nil, errExecute.Error(), "", "")
		return okEnvelope(htmlResponse(http.StatusOK, page))
	}
	page := renderPage(opts, resp.StatusCode, resp.Headers, resp.Body, nil, "", "", "")
	return okEnvelope(htmlResponse(http.StatusOK, page))
}

func optionsFromManagementRequest(req managementRequest) (runOptions, error) {
	opts := runOptions{
		Model:         defaultModel,
		Mode:          "non-stream",
		EntryProtocol: "openai",
		ExitProtocol:  "openai",
		Prompt:        defaultPrompt,
		Headers:       http.Header{},
		Query:         url.Values{},
	}
	opts.HostCallbackID = strings.TrimSpace(req.HostCallbackID)
	if len(req.Body) > 0 {
		if errApplyBody := applyBodyOptions(&opts, req.Body); errApplyBody != nil {
			return opts, errApplyBody
		}
	}
	if errApplyQuery := applyQueryOptions(&opts, req.Query); errApplyQuery != nil {
		return opts, errApplyQuery
	}
	if opts.Stream {
		opts.Mode = "stream"
	} else {
		opts.Mode = "non-stream"
	}
	return opts, nil
}

func applyBodyOptions(opts *runOptions, raw []byte) error {
	var bodyOpts managementBodyOptions
	if errUnmarshal := json.Unmarshal(raw, &bodyOpts); errUnmarshal != nil {
		return fmt.Errorf("decode JSON request body: %w", errUnmarshal)
	}
	if strings.TrimSpace(bodyOpts.Model) != "" {
		opts.Model = strings.TrimSpace(bodyOpts.Model)
	}
	if strings.TrimSpace(bodyOpts.Mode) != "" {
		applyMode(opts, bodyOpts.Mode)
	}
	if strings.TrimSpace(bodyOpts.EntryProtocol) != "" {
		opts.EntryProtocol = strings.TrimSpace(bodyOpts.EntryProtocol)
	}
	if strings.TrimSpace(bodyOpts.ExitProtocol) != "" {
		opts.ExitProtocol = strings.TrimSpace(bodyOpts.ExitProtocol)
	}
	if bodyOpts.Prompt != "" {
		opts.Prompt = bodyOpts.Prompt
	}
	if bodyOpts.Stream != nil {
		opts.Stream = *bodyOpts.Stream
	}
	if len(bodyOpts.Body) > 0 && string(bodyOpts.Body) != "null" {
		if !json.Valid(bodyOpts.Body) {
			return fmt.Errorf("body must be valid JSON")
		}
		opts.Body = append([]byte(nil), bodyOpts.Body...)
	}
	if bodyOpts.Headers != nil {
		opts.Headers = cloneHeader(bodyOpts.Headers)
	}
	if bodyOpts.Query != nil {
		opts.Query = cloneValues(bodyOpts.Query)
	}
	if bodyOpts.Alt != "" {
		opts.Alt = bodyOpts.Alt
	}
	if bodyOpts.ImplicitClose != nil {
		opts.ImplicitClose = *bodyOpts.ImplicitClose
	}
	return nil
}

func applyQueryOptions(opts *runOptions, query url.Values) error {
	if query == nil {
		return nil
	}
	if raw := strings.TrimSpace(query.Get("model")); raw != "" {
		opts.Model = raw
	}
	if raw := strings.TrimSpace(query.Get("mode")); raw != "" {
		applyMode(opts, raw)
	}
	if raw := strings.TrimSpace(query.Get("entry_protocol")); raw != "" {
		opts.EntryProtocol = raw
	}
	if raw := strings.TrimSpace(query.Get("exit_protocol")); raw != "" {
		opts.ExitProtocol = raw
	}
	if raw := query.Get("prompt"); raw != "" {
		opts.Prompt = raw
	}
	if raw := strings.TrimSpace(query.Get("body")); raw != "" {
		body := []byte(raw)
		if !json.Valid(body) {
			return fmt.Errorf("query body must be valid JSON")
		}
		opts.Body = append([]byte(nil), body...)
	}
	if raw := strings.TrimSpace(query.Get("alt")); raw != "" {
		opts.Alt = raw
	}
	if errStream := applyBoolQuery(query, "stream", &opts.Stream); errStream != nil {
		return errStream
	}
	if errImplicitClose := applyBoolQuery(query, "implicit_close", &opts.ImplicitClose); errImplicitClose != nil {
		return errImplicitClose
	}
	return nil
}

func applyMode(opts *runOptions, mode string) {
	normalized := strings.ToLower(strings.TrimSpace(mode))
	switch normalized {
	case "stream", "streaming":
		opts.Stream = true
	case "non-stream", "non_stream", "nonstream", "sync":
		opts.Stream = false
	}
}

func applyBoolQuery(query url.Values, key string, target *bool) error {
	raw := strings.TrimSpace(query.Get(key))
	if raw == "" {
		return nil
	}
	parsed, errParse := strconv.ParseBool(raw)
	if errParse != nil {
		return fmt.Errorf("%s must be a boolean: %w", key, errParse)
	}
	*target = parsed
	return nil
}

func executeOnce(opts runOptions) (pluginapi.HostModelExecutionResponse, error) {
	body, errBody := modelRequestBody(opts)
	if errBody != nil {
		return pluginapi.HostModelExecutionResponse{}, errBody
	}
	// Forward HostCallbackID so the host skips this plugin's interceptors on the
	// nested model execution. Host model callbacks do not recursively call the
	// originating plugin's interceptor chain.
	result, errCall := callHost(pluginabi.MethodHostModelExecute, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: opts.EntryProtocol,
			ExitProtocol:  opts.ExitProtocol,
			Model:         opts.Model,
			Stream:        false,
			Body:          body,
			Headers:       cloneHeader(opts.Headers),
			Query:         cloneValues(opts.Query),
			Alt:           opts.Alt,
		},
		HostCallbackID: opts.HostCallbackID,
	})
	if errCall != nil {
		return pluginapi.HostModelExecutionResponse{}, errCall
	}
	var resp pluginapi.HostModelExecutionResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return pluginapi.HostModelExecutionResponse{}, fmt.Errorf("decode host.model.execute result: %w", errUnmarshal)
	}
	return resp, nil
}

func executeStream(opts runOptions) (data streamPageData) {
	body, errBody := modelRequestBody(opts)
	if errBody != nil {
		data.Error = errBody.Error()
		return data
	}
	// Forward HostCallbackID so the host skips this plugin's interceptors on the
	// nested model execution. Host model callbacks do not recursively call the
	// originating plugin's interceptor chain.
	result, errCall := callHost(pluginabi.MethodHostModelExecuteStream, hostModelExecutionRequest{
		HostModelExecutionRequest: pluginapi.HostModelExecutionRequest{
			EntryProtocol: opts.EntryProtocol,
			ExitProtocol:  opts.ExitProtocol,
			Model:         opts.Model,
			Stream:        true,
			Body:          body,
			Headers:       cloneHeader(opts.Headers),
			Query:         cloneValues(opts.Query),
			Alt:           opts.Alt,
		},
		HostCallbackID: opts.HostCallbackID,
	})
	if errCall != nil {
		data.Error = errCall.Error()
		return data
	}
	var resp pluginapi.HostModelStreamResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		data.Error = fmt.Sprintf("decode host.model.execute_stream result: %v", errUnmarshal)
		return data
	}
	data.StatusCode = resp.StatusCode
	data.Headers = cloneHeader(resp.Headers)
	data.StreamID = resp.StreamID
	if resp.StreamID == "" {
		data.Error = "host.model.execute_stream returned an empty stream_id"
		return data
	}
	if opts.ImplicitClose {
		// When implicit_close=true, the host closes this stream when the management.handle RPC callback scope returns.
		data.CloseMode = "implicit close at management.handle return"
	} else {
		data.CloseMode = "explicit close through host.model.stream_close"
		defer func() {
			if errClose := closeHostModelStream(resp.StreamID); errClose != nil {
				data.CloseError = errClose.Error()
			}
		}()
	}
	for {
		chunk, errRead := readHostModelStream(resp.StreamID)
		if errRead != nil {
			data.Error = errRead.Error()
			return data
		}
		if len(chunk.Payload) > 0 {
			data.Chunks = append(data.Chunks, string(chunk.Payload))
		}
		if chunk.Error != "" {
			data.Error = chunk.Error
			return data
		}
		if chunk.Done {
			return data
		}
	}
}

func readHostModelStream(streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	result, errCall := callHost(pluginabi.MethodHostModelStreamRead, pluginapi.HostModelStreamReadRequest{StreamID: streamID})
	if errCall != nil {
		return pluginapi.HostModelStreamReadResponse{}, errCall
	}
	var resp pluginapi.HostModelStreamReadResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return pluginapi.HostModelStreamReadResponse{}, fmt.Errorf("decode host.model.stream_read result: %w", errUnmarshal)
	}
	return resp, nil
}

func closeHostModelStream(streamID string) error {
	_, errCall := callHost(pluginabi.MethodHostModelStreamClose, pluginapi.HostModelStreamCloseRequest{StreamID: streamID})
	return errCall
}

func modelRequestBody(opts runOptions) ([]byte, error) {
	if len(opts.Body) > 0 {
		return append([]byte(nil), opts.Body...), nil
	}
	raw, errMarshal := json.Marshal(chatCompletionRequest{
		Model:  opts.Model,
		Stream: opts.Stream,
		Messages: []chatMessage{{
			Role:    "user",
			Content: opts.Prompt,
		}},
	})
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal OpenAI-compatible request body: %w", errMarshal)
	}
	return raw, nil
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

func renderPage(opts runOptions, status int, headers http.Header, body []byte, chunks []string, errText string, closeMode string, closeError string) []byte {
	var out bytes.Buffer
	out.WriteString("<!doctype html><html><head><meta charset=\"utf-8\"><title>Host Model Callback</title>")
	out.WriteString("<style>body{font-family:-apple-system,BlinkMacSystemFont,\"Segoe UI\",sans-serif;margin:2rem;line-height:1.45;color:#1f2933}code,pre{background:#f3f4f6;border-radius:6px}code{padding:.1rem .3rem}pre{padding:1rem;overflow:auto;white-space:pre-wrap}dl{display:grid;grid-template-columns:max-content 1fr;gap:.35rem 1rem}dt{font-weight:600}dd{margin:0}.error{color:#b42318}</style>")
	out.WriteString("</head><body><main>")
	out.WriteString("<h1>Host Model Callback</h1>")
	out.WriteString("<dl>")
	writeDefinition(&out, "model", opts.Model)
	writeDefinition(&out, "mode", opts.Mode)
	writeDefinition(&out, "entry_protocol", opts.EntryProtocol)
	writeDefinition(&out, "exit_protocol", opts.ExitProtocol)
	writeDefinition(&out, "stream", strconv.FormatBool(opts.Stream))
	writeDefinition(&out, "implicit_close", strconv.FormatBool(opts.ImplicitClose))
	if closeMode != "" {
		writeDefinition(&out, "close", closeMode)
	}
	writeDefinition(&out, "status", strconv.Itoa(status))
	out.WriteString("</dl>")
	if errText != "" {
		out.WriteString("<h2>Error</h2><pre class=\"error\">")
		out.WriteString(html.EscapeString(errText))
		out.WriteString("</pre>")
	}
	if closeError != "" {
		out.WriteString("<h2>Close Error</h2><pre class=\"error\">")
		out.WriteString(html.EscapeString(closeError))
		out.WriteString("</pre>")
	}
	if headers != nil {
		out.WriteString("<h2>Headers</h2><pre>")
		out.WriteString(html.EscapeString(prettyJSON(headers)))
		out.WriteString("</pre>")
	}
	if len(chunks) > 0 {
		out.WriteString("<h2>Stream Chunks</h2><pre>")
		out.WriteString(html.EscapeString(strings.Join(chunks, "")))
		out.WriteString("</pre>")
	}
	if len(body) > 0 {
		out.WriteString("<h2>Body</h2><pre>")
		out.WriteString(html.EscapeString(prettyBody(body)))
		out.WriteString("</pre>")
	}
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
