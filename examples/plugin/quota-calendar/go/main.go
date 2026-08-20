package main

/*
#cgo CFLAGS: -Wno-deprecated-declarations
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct { uint32_t abi_version; void* host_ctx; cliproxy_host_call_fn call; cliproxy_host_free_fn free_buffer; } cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct { uint32_t abi_version; cliproxy_plugin_call_fn call; cliproxy_plugin_free_fn free_buffer; cliproxy_plugin_shutdown_fn shutdown; } cliproxy_plugin_api;
extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
static const cliproxy_host_api* stored_host;
static void store_host_api(const cliproxy_host_api* host) { stored_host = host; }
static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) return 1;
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}
static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) stored_host->free_buffer(ptr, len);
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const pluginName = "quota-calendar"

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
	SchemaVersion uint32             `json:"schema_version"`
	Metadata      pluginapi.Metadata `json:"metadata"`
	Capabilities  map[string]bool    `json:"capabilities"`
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
	Query map[string][]string `json:"Query"`
}
type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}
type authListResponse struct {
	Files []pluginapi.HostAuthFileEntry `json:"files"`
}
type authRuntimeResponse struct {
	Auth pluginapi.HostAuthFileEntry `json:"auth"`
}

type event struct {
	Model    string
	Reset    time.Time
	Revision time.Time
	Provider string
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
	var raw []byte
	if request != nil && requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	result, err := handleMethod(C.GoString(method), raw)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, result)
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

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return okEnvelope(registration{SchemaVersion: pluginabi.SchemaVersion, Metadata: pluginapi.Metadata{Name: pluginName, Version: "0.1.0", Author: "TheVerwalter", GitHubRepository: "https://github.com/TheVerwalter/CLIProxyAPI-quota-calendar"}, Capabilities: map[string]bool{"management_api": true}})
	case pluginabi.MethodManagementRegister:
		return okEnvelope(managementRegistration{Resources: []managementResource{{Path: "/calendar.ics", Menu: "Quota Calendar", Description: "Current per-model quota reset events as an iCalendar feed."}}})
	case pluginabi.MethodManagementHandle:
		return handleManagement(raw)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var req struct {
		Query map[string][]string `json:"Query"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, err
		}
	}
	_ = req
	ics, err := buildCalendar()
	if err != nil {
		return nil, err
	}
	return okEnvelope(managementResponse{StatusCode: http.StatusOK, Headers: http.Header{"Content-Type": {"text/calendar; charset=utf-8"}, "Cache-Control": {"no-store, max-age=0"}}, Body: []byte(ics)})
}

func buildCalendar() (string, error) {
	listRaw, err := callHost(pluginabi.MethodHostAuthList, nil)
	if err != nil {
		return "", err
	}
	var list authListResponse
	if err := json.Unmarshal(listRaw, &list); err != nil {
		return "", err
	}
	events := make(map[string]event)
	for _, entry := range list.Files {
		if entry.AuthIndex == "" {
			continue
		}
		runtimeRaw, err := callHost(pluginabi.MethodHostAuthGetRuntime, mustJSON(pluginapi.HostAuthGetRequest{AuthIndex: entry.AuthIndex}))
		if err != nil {
			continue
		}
		var runtime authRuntimeResponse
		if err := json.Unmarshal(runtimeRaw, &runtime); err != nil {
			continue
		}
		for model, state := range runtime.Auth.ModelStates {
			if state.NextReset.IsZero() || !state.NextReset.After(time.Now()) {
				continue
			}
			key := strings.TrimSpace(runtime.Auth.Provider) + "\x00" + strings.TrimSpace(model)
			revision := state.UpdatedAt
			if revision.IsZero() {
				revision = state.NextReset
			}
			candidate := event{Model: model, Reset: state.NextReset, Revision: revision, Provider: runtime.Auth.Provider}
			if old, ok := events[key]; !ok || candidate.Reset.After(old.Reset) || (candidate.Reset.Equal(old.Reset) && candidate.Revision.After(old.Revision)) {
				events[key] = candidate
			}
		}
	}
	items := make([]event, 0, len(events))
	for _, item := range events {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Provider == items[j].Provider {
			return items[i].Model < items[j].Model
		}
		return items[i].Provider < items[j].Provider
	})
	var b strings.Builder
	writeICSLine(&b, "BEGIN:VCALENDAR")
	writeICSLine(&b, "PRODID:-//TheVerwalter//CLIProxyAPI Quota Calendar//EN")
	writeICSLine(&b, "VERSION:2.0")
	writeICSLine(&b, "CALSCALE:GREGORIAN")
	writeICSLine(&b, "NAME:Quota resets")
	writeICSLine(&b, "REFRESH-INTERVAL;VALUE=DURATION:PT15M")
	writeICSLine(&b, "X-PUBLISHED-TTL:PT15M")
	for _, item := range items {
		uid := stableUID(item.Provider, item.Model)
		writeICSLine(&b, "BEGIN:VEVENT")
		writeICSLine(&b, "UID:"+uid)
		writeICSLine(&b, "DTSTAMP:"+formatICS(item.Revision))
		writeICSLine(&b, "LAST-MODIFIED:"+formatICS(item.Revision))
		writeICSLine(&b, "DTSTART:"+formatICS(item.Reset))
		writeICSLine(&b, "DTEND:"+formatICS(item.Reset.Add(time.Minute)))
		writeICSLine(&b, "SUMMARY:"+escapeICS(item.Model+" quota reset"))
		writeICSLine(&b, "DESCRIPTION:"+escapeICS("Provider: "+item.Provider))
		writeICSLine(&b, "STATUS:CONFIRMED")
		writeICSLine(&b, "TRANSP:TRANSPARENT")
		writeICSLine(&b, "END:VEVENT")
	}
	writeICSLine(&b, "END:VCALENDAR")
	return b.String(), nil
}

func stableUID(provider, model string) string {
	return "quota-" + fmt.Sprintf("%x", fnv1a(provider+"\x00"+model)) + "@cliproxyapi"
}
func fnv1a(value string) uint64 {
	var hash uint64 = 14695981039346656037
	for i := 0; i < len(value); i++ {
		hash ^= uint64(value[i])
		hash *= 1099511628211
	}
	return hash
}
func formatICS(t time.Time) string { return t.UTC().Format("20060102T150405Z") }
func writeICSLine(builder *strings.Builder, line string) {
	first := true
	for len(line) > 75 {
		cut := 75
		if !first {
			cut = 74
		}
		for cut > 0 && (line[cut]&0xc0) == 0x80 {
			cut--
		}
		if cut == 0 {
			cut = 75
		}
		builder.WriteString(line[:cut])
		builder.WriteString("\r\n ")
		line = line[cut:]
		first = false
	}
	builder.WriteString(line)
	builder.WriteString("\r\n")
}
func escapeICS(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, ";", `\;`)
	value = strings.ReplaceAll(value, ",", `\,`)
	return strings.ReplaceAll(value, "\n", `\n`)
}
func mustJSON(value any) []byte { raw, _ := json.Marshal(value); return raw }

func callHost(method string, payload []byte) ([]byte, error) {
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var request *C.uint8_t
	if len(payload) > 0 {
		request = (*C.uint8_t)(C.CBytes(payload))
		defer C.free(unsafe.Pointer(request))
	}
	var response C.cliproxy_buffer
	if C.call_host_api(cMethod, request, C.size_t(len(payload)), &response) != 0 || response.ptr == nil {
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	defer C.free_host_buffer(response.ptr, response.len)
	raw := C.GoBytes(response.ptr, C.int(response.len))
	var envelopeResponse envelope
	if err := json.Unmarshal(raw, &envelopeResponse); err != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, err)
	}
	if !envelopeResponse.OK {
		if envelopeResponse.Error != nil {
			return nil, fmt.Errorf("%s: %s", envelopeResponse.Error.Code, envelopeResponse.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return envelopeResponse.Result, nil
}
func okEnvelope(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
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
