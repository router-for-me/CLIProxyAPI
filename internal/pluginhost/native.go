package pluginhost

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const nativeBasePath = "/v1"

// nativeMethods matches the methods advertised by the server's CORS response; browsers cannot send others
// cross-origin, and OPTIONS is answered by CORS before routing.
var nativeMethods = map[string]struct{}{
	http.MethodGet: {}, http.MethodPost: {}, http.MethodPut: {}, http.MethodPatch: {}, http.MethodDelete: {},
}

type nativeRouteRecord struct {
	pluginID string
	path     string
	version  string
	route    pluginapi.NativeRoute
}

// HasNativeRoute reports whether a plugin owns an exact native path.
// The server authenticates the request before dispatching to that plugin.
func (h *Host) HasNativeRoute(path string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, record := range h.nativeRoutes {
		if record.route.Path == path && h.fused[record.pluginID] == "" && h.activePluginPaths[record.pluginID] == cleanPluginPath(record.path) && h.activePluginVersions[record.pluginID] == record.version {
			return true
		}
	}
	return false
}

func (h *Host) RegisterNativeRoutes(ctx context.Context, reserved map[string]struct{}) {
	if h == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !h.lockApply(ctx) {
		return
	}
	defer h.unlockApply()
	next := make(map[string]nativeRouteRecord)
	for _, record := range h.activeRecords() {
		api := record.plugin.Capabilities.NativeAPI
		if api == nil || record.plugin.SchemaVersion < pluginabi.SchemaVersionNativeAPI || h.isPluginFused(record.id) {
			continue
		}
		resp, err := h.callNativeRegistrar(ctx, record, api)
		if err != nil {
			log.Warnf("pluginhost: native registrar %s failed: %v", record.id, err)
			continue
		}
		for _, route := range resp.Routes {
			method, path, ok := normalizeNativeRoute(route)
			if !ok {
				log.Warnf("pluginhost: plugin %s declared invalid native route %s %s", record.id, route.Method, route.Path)
				continue
			}
			key := managementRouteKey(method, path)
			if _, exists := reserved[key]; exists {
				continue
			}
			if nativeRouteConflictsReserved(method, path, reserved) {
				continue
			}
			if _, exists := next[key]; exists {
				continue
			}
			route.Method, route.Path = method, path
			next[key] = nativeRouteRecord{pluginID: record.id, path: record.path, version: record.version, route: route}
		}
	}
	h.mu.Lock()
	h.nativeRoutes = next
	h.mu.Unlock()
}

func normalizeNativeRoute(route pluginapi.NativeRoute) (string, string, bool) {
	if route.Handler == nil {
		return "", "", false
	}
	method := strings.ToUpper(strings.TrimSpace(route.Method))
	if _, ok := nativeMethods[method]; !ok {
		return "", "", false
	}
	path := strings.TrimSpace(route.Path)
	if !strings.HasPrefix(path, nativeBasePath+"/") || strings.ContainsAny(path, "?#\\%") || strings.Contains(path, "//") || strings.Contains(path, "..") || strings.ContainsAny(path, "*{}: \t\r\n") {
		return "", "", false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, nativeBasePath+"/"), "/") {
		if segment == "" || segment == "." {
			return "", "", false
		}
	}
	return method, path, true
}

func nativeRouteConflictsReserved(method, path string, reserved map[string]struct{}) bool {
	for key := range reserved {
		space := strings.IndexByte(key, ' ')
		if space <= 0 || strings.ToUpper(key[:space]) != method {
			continue
		}
		pattern := key[space+1:]
		if pattern == path {
			return true
		}
		if strings.ContainsAny(pattern, ":*") && ginRoutePatternMatches(pattern, path) {
			return true
		}
	}
	return false
}

func ginRoutePatternMatches(pattern, path string) bool {
	patternParts := strings.Split(strings.Trim(pattern, "/"), "/")
	pathParts := strings.Split(strings.Trim(path, "/"), "/")
	for i, segment := range patternParts {
		if strings.HasPrefix(segment, "*") {
			return i < len(pathParts)
		}
		if i >= len(pathParts) {
			return false
		}
		if strings.HasPrefix(segment, ":") {
			continue
		}
		if segment != pathParts[i] {
			return false
		}
	}
	return len(patternParts) == len(pathParts)
}

func (h *Host) callNativeRegistrar(ctx context.Context, record capabilityRecord, api pluginapi.NativeAPI) (resp pluginapi.NativeRegistrationResponse, err error) {
	if h == nil || api == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.NativeRegistrationResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "NativeAPI.RegisterNative", recovered)
			resp = pluginapi.NativeRegistrationResponse{}
			err = fmt.Errorf("native registrar panic: %v", recovered)
		}
	}()
	return api.RegisterNative(ctx, pluginapi.NativeRegistrationRequest{Plugin: record.meta, BasePath: nativeBasePath})
}

func (h *Host) ServeNativeHTTP(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || w == nil || r == nil || r.URL == nil {
		return false
	}
	key := managementRouteKey(r.Method, r.URL.Path)
	h.mu.Lock()
	record, found := h.nativeRoutes[key]
	if !found {
		var methods []string
		for _, item := range h.nativeRoutes {
			if item.route.Path == r.URL.Path && h.fused[item.pluginID] == "" && h.activePluginPaths[item.pluginID] == cleanPluginPath(item.path) && h.activePluginVersions[item.pluginID] == item.version {
				methods = append(methods, item.route.Method)
			}
		}
		h.mu.Unlock()
		if len(methods) == 0 {
			return false
		}
		sort.Strings(methods)
		w.Header().Set("Allow", strings.Join(methods, ", "))
		w.WriteHeader(http.StatusMethodNotAllowed)
		return true
	}
	h.mu.Unlock()
	if h.isPluginFused(record.pluginID) || !h.pluginIdentityCurrent(record.pluginID, record.path, record.version) {
		http.NotFound(w, r)
		return true
	}
	limit := int64(0)
	if cfg := h.currentRuntimeConfig(); cfg != nil && cfg.MaxRequestBodyBytes > 0 {
		limit = cfg.MaxRequestBodyBytes
	}
	bodyReader := r.Body
	if bodyReader == nil {
		bodyReader = http.NoBody
	}
	if limit > 0 {
		bodyReader = http.MaxBytesReader(w, bodyReader, limit)
	}
	body, errRead := io.ReadAll(bodyReader)
	if errRead != nil {
		if _, tooLarge := errRead.(*http.MaxBytesError); tooLarge {
			http.Error(w, "http: request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "failed to read request body", http.StatusBadRequest)
		}
		return true
	}
	headers := cloneHeader(r.Header)
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Api-Key", "Proxy-Authorization", "Cookie"} {
		headers.Del(name)
	}
	query := cloneValues(r.URL.Query())
	query.Del("key")
	query.Del("auth_token")
	response, err := h.callNativeHandler(r.Context(), record, pluginapi.NativeRequest{Method: r.Method, Path: r.URL.Path, Headers: headers, Query: query, Body: body})
	if err != nil {
		log.Warnf("pluginhost: native handler %s failed: %v", record.pluginID, err)
		http.Error(w, "native plugin handler failed", http.StatusBadGateway)
		return true
	}
	status := response.StatusCode
	if status == 0 {
		status = http.StatusOK
	}
	// Native handlers return one final HTTP response: only 2xx-5xx are valid. Validate before copying
	// plugin headers so a fallback 502 never inherits headers such as Content-Encoding.
	if status < 200 || status > 599 {
		log.Warnf("pluginhost: native handler %s returned invalid status code %d", record.pluginID, status)
		http.Error(w, "native plugin handler failed", http.StatusBadGateway)
		return true
	}
	for name, values := range response.Headers {
		// The host's CORS middleware owns Access-Control-* headers; a plugin copy would duplicate them.
		if strings.HasPrefix(http.CanonicalHeaderKey(name), "Access-Control-") {
			continue
		}
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(status)
	if _, errWrite := w.Write(response.Body); errWrite != nil {
		log.Warnf("pluginhost: failed writing native response: %v", errWrite)
	}
	return true
}

func (h *Host) callNativeHandler(ctx context.Context, record nativeRouteRecord, req pluginapi.NativeRequest) (resp pluginapi.NativeResponse, err error) {
	if h == nil || record.route.Handler == nil || h.isPluginFused(record.pluginID) || !h.pluginIdentityCurrent(record.pluginID, record.path, record.version) {
		return pluginapi.NativeResponse{}, fmt.Errorf("native route unavailable")
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.pluginID, "NativeHandler.HandleNative", recovered)
			resp = pluginapi.NativeResponse{}
			err = fmt.Errorf("native handler panic: %v", recovered)
		}
	}()
	return record.route.Handler.HandleNative(ctx, req)
}
