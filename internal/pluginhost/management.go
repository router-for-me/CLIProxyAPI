package pluginhost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/htmlsanitize"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const (
	managementBasePath      = "/v0/management"
	resourcePluginBasePath  = "/v0/resource/plugins"
	legacyPluginRoutePrefix = "/plugins"
)

type managementRouteRecord struct {
	pluginID string
	path     string
	version  string
	route    pluginapi.ManagementRoute
}

type resourceRouteRecord struct {
	pluginID string
	path     string
	version  string
	route    pluginapi.ResourceRoute
}

// RegisterManagementRoutes rebuilds the plugin-owned Management API and resource route tables.
func (h *Host) RegisterManagementRoutes(ctx context.Context, reserved map[string]struct{}) {
	if h == nil {
		return
	}

	nextRoutes := make(map[string]managementRouteRecord)
	nextResources := make(map[string]resourceRouteRecord)
	for _, record := range h.activeRecords() {
		plugin := record.plugin.Capabilities.ManagementAPI
		if plugin == nil || h.isPluginFused(record.id) {
			continue
		}
		resp, errRegister := h.callManagementRegistrar(ctx, record, plugin)
		if errRegister != nil {
			log.Warnf("pluginhost: management registrar %s failed: %v", record.id, errRegister)
			continue
		}

		for _, item := range resp.Routes {
			method, path, okRoute := normalizeManagementRoute(item)
			if !okRoute {
				log.Warnf("pluginhost: plugin %s declared invalid management route %s %s", record.id, item.Method, item.Path)
				continue
			}
			if routeDeclaresLegacyMenuResource(method, item) {
				if !registerResourceRoute(nextResources, record, resourceRouteFromManagementRoute(item)) {
					log.Warnf("pluginhost: plugin %s declared invalid resource route %s", record.id, item.Path)
				}
				continue
			}
			key := managementRouteKey(method, path)
			if _, exists := reserved[key]; exists {
				log.Warnf("pluginhost: plugin %s management route %s conflicts with an existing route and was skipped", record.id, key)
				continue
			}
			if _, exists := nextRoutes[key]; exists {
				log.Warnf("pluginhost: plugin %s management route %s conflicts with a higher-priority plugin and was skipped", record.id, key)
				continue
			}
			item.Method = method
			item.Path = path
			nextRoutes[key] = managementRouteRecord{
				pluginID: record.id,
				path:     record.path,
				version:  record.version,
				route:    item,
			}
		}

		for _, item := range resp.Resources {
			if !registerResourceRoute(nextResources, record, item) {
				log.Warnf("pluginhost: plugin %s declared invalid resource route %s", record.id, item.Path)
			}
		}
	}

	h.mu.Lock()
	h.managementRoutes = nextRoutes
	h.resourceRoutes = nextResources
	h.mu.Unlock()
}

func (h *Host) callManagementRegistrar(ctx context.Context, record capabilityRecord, plugin pluginapi.ManagementAPI) (resp pluginapi.ManagementRegistrationResponse, err error) {
	if h == nil || plugin == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.ManagementRegistrationResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "ManagementAPI.RegisterManagement", recovered)
			resp = pluginapi.ManagementRegistrationResponse{}
			err = fmt.Errorf("management registrar panic: %v", recovered)
		}
	}()
	return plugin.RegisterManagement(ctx, pluginapi.ManagementRegistrationRequest{
		Plugin:           record.meta,
		BasePath:         managementBasePath,
		ResourceBasePath: resourcePluginBasePath + "/" + record.id,
	})
}

func normalizeManagementRoute(item pluginapi.ManagementRoute) (string, string, bool) {
	if item.Handler == nil {
		return "", "", false
	}
	method := strings.ToUpper(strings.TrimSpace(item.Method))
	if method == "" {
		method = http.MethodGet
	}
	if strings.ContainsAny(method, " \t\r\n") {
		return "", "", false
	}

	path := strings.TrimSpace(item.Path)
	if path == "" {
		return "", "", false
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.HasPrefix(path, managementBasePath+"/") {
		path = strings.TrimPrefix(path, managementBasePath)
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "", "", false
	}
	fullPath := managementBasePath + path
	if !strings.HasPrefix(fullPath, managementBasePath+"/") {
		return "", "", false
	}
	if strings.ContainsAny(fullPath, " \t\r\n") || strings.Contains(fullPath, ":") || strings.Contains(fullPath, "*") {
		return "", "", false
	}
	return method, fullPath, true
}

func routeDeclaresLegacyMenuResource(method string, item pluginapi.ManagementRoute) bool {
	return strings.EqualFold(strings.TrimSpace(method), http.MethodGet) && strings.TrimSpace(item.Menu) != ""
}

func resourceRouteFromManagementRoute(item pluginapi.ManagementRoute) pluginapi.ResourceRoute {
	return pluginapi.ResourceRoute{
		Path:        item.Path,
		Menu:        item.Menu,
		Description: item.Description,
		Handler:     item.Handler,
	}
}

func registerResourceRoute(routes map[string]resourceRouteRecord, record capabilityRecord, item pluginapi.ResourceRoute) bool {
	path, okRoute := normalizeResourceRoute(record.id, item)
	if !okRoute {
		return false
	}
	key := managementRouteKey(http.MethodGet, path)
	if _, exists := routes[key]; exists {
		log.Warnf("pluginhost: plugin %s resource route %s conflicts with a higher-priority plugin and was skipped", record.id, key)
		return true
	}
	item.Path = path
	routes[key] = resourceRouteRecord{
		pluginID: record.id,
		path:     record.path,
		version:  record.version,
		route:    item,
	}
	return true
}

func normalizeResourceRoute(pluginID string, item pluginapi.ResourceRoute) (string, bool) {
	if item.Handler == nil {
		return "", false
	}
	pluginID = strings.TrimSpace(pluginID)
	if pluginID == "" {
		return "", false
	}

	path := strings.TrimSpace(item.Path)
	if path == "" {
		return "", false
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	pluginBasePath := resourcePluginBasePath + "/" + pluginID
	if strings.HasPrefix(path, pluginBasePath+"/") {
		path = strings.TrimPrefix(path, pluginBasePath)
	} else if strings.HasPrefix(path, legacyPluginRoutePrefix+"/"+pluginID+"/") {
		path = strings.TrimPrefix(path, legacyPluginRoutePrefix+"/"+pluginID)
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return "", false
	}

	fullPath := pluginBasePath + path
	if !strings.HasPrefix(fullPath, pluginBasePath+"/") {
		return "", false
	}
	if strings.ContainsAny(fullPath, " \t\r\n") || strings.Contains(fullPath, ":") || strings.Contains(fullPath, "*") || strings.Contains(fullPath, "..") {
		return "", false
	}
	return fullPath, true
}

func managementRouteKey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + strings.TrimSpace(path)
}

// ServeManagementHTTP dispatches an authenticated Management API request to a plugin route.
func (h *Host) ServeManagementHTTP(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || w == nil || r == nil || r.URL == nil {
		return false
	}
	key := managementRouteKey(r.Method, r.URL.Path)
	h.mu.Lock()
	record, okRoute := h.managementRoutes[key]
	h.mu.Unlock()
	if !okRoute || record.route.Handler == nil || h.isPluginFused(record.pluginID) {
		return false
	}

	var body []byte
	if r.Body != nil {
		var errRead error
		body, errRead = io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, "failed to read plugin management request body", http.StatusBadRequest)
			return true
		}
		if errClose := r.Body.Close(); errClose != nil {
			log.Warnf("pluginhost: failed to close plugin management request body: %v", errClose)
		}
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	resp, errHandle := h.callManagementHandler(r.Context(), record, pluginapi.ManagementRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: cloneHeader(r.Header),
		Query:   cloneValues(r.URL.Query()),
		Body:    bytes.Clone(body),
	})
	if errHandle != nil {
		log.Warnf("pluginhost: management handler %s failed: %v", record.pluginID, errHandle)
		http.Error(w, "plugin management handler failed", http.StatusBadGateway)
		return true
	}
	resp.Body = escapeManagementResponseBody(resp)

	for keyHeader, values := range resp.Headers {
		for _, value := range values {
			w.Header().Add(keyHeader, value)
		}
	}
	statusCode := resp.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	w.WriteHeader(statusCode)
	if _, errWrite := w.Write(resp.Body); errWrite != nil {
		log.Warnf("pluginhost: failed to write plugin management response: %v", errWrite)
	}
	return true
}

// ServeResourceHTTP dispatches an unauthenticated browser-navigable resource request to a plugin route.
func (h *Host) ServeResourceHTTP(w http.ResponseWriter, r *http.Request) bool {
	if h == nil || w == nil || r == nil || r.URL == nil {
		return false
	}
	if !strings.EqualFold(r.Method, http.MethodGet) {
		return false
	}
	key := managementRouteKey(http.MethodGet, r.URL.Path)
	h.mu.Lock()
	record, okRoute := h.resourceRoutes[key]
	h.mu.Unlock()
	if !okRoute || record.route.Handler == nil || h.isPluginFused(record.pluginID) {
		return false
	}

	resp, errHandle := h.callResourceHandler(r.Context(), record, pluginapi.ManagementRequest{
		Method:  http.MethodGet,
		Path:    r.URL.Path,
		Headers: cloneHeader(r.Header),
		Query:   cloneValues(r.URL.Query()),
	})
	if errHandle != nil {
		log.Warnf("pluginhost: resource handler %s failed: %v", record.pluginID, errHandle)
		http.Error(w, "plugin resource handler failed", http.StatusBadGateway)
		return true
	}

	for keyHeader, values := range resp.Headers {
		for _, value := range values {
			w.Header().Add(keyHeader, value)
		}
	}
	statusCode := resp.StatusCode
	if statusCode == 0 {
		statusCode = http.StatusOK
	}
	w.WriteHeader(statusCode)
	if _, errWrite := w.Write(resp.Body); errWrite != nil {
		log.Warnf("pluginhost: failed to write plugin resource response: %v", errWrite)
	}
	return true
}

func (h *Host) callManagementHandler(ctx context.Context, record managementRouteRecord, req pluginapi.ManagementRequest) (resp pluginapi.ManagementResponse, err error) {
	if h == nil || record.route.Handler == nil || h.isPluginFused(record.pluginID) || !h.pluginIdentityCurrent(record.pluginID, record.path, record.version) {
		return pluginapi.ManagementResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.pluginID, "ManagementHandler.HandleManagement", recovered)
			resp = pluginapi.ManagementResponse{}
			err = fmt.Errorf("management handler panic: %v", recovered)
		}
	}()
	return record.route.Handler.HandleManagement(ctx, req)
}

func escapeManagementResponseBody(resp pluginapi.ManagementResponse) []byte {
	body, okEscaped := htmlsanitize.JSONBodyIfLikely(resp.Body, resp.Headers.Get("Content-Type"))
	if !okEscaped {
		return resp.Body
	}
	return body
}

func (h *Host) callResourceHandler(ctx context.Context, record resourceRouteRecord, req pluginapi.ManagementRequest) (resp pluginapi.ManagementResponse, err error) {
	if h == nil || record.route.Handler == nil || h.isPluginFused(record.pluginID) || !h.pluginIdentityCurrent(record.pluginID, record.path, record.version) {
		return pluginapi.ManagementResponse{}, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.pluginID, "ResourceHandler.HandleManagement", recovered)
			resp = pluginapi.ManagementResponse{}
			err = fmt.Errorf("resource handler panic: %v", recovered)
		}
	}()
	return record.route.Handler.HandleManagement(ctx, req)
}
