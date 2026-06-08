package pluginhost

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

const managementBasePath = "/v0/management"

type managementRouteRecord struct {
	pluginID string
	route    pluginapi.ManagementRoute
}

// RegisterManagementRoutes rebuilds the plugin-owned Management API route table.
func (h *Host) RegisterManagementRoutes(ctx context.Context, reserved map[string]struct{}) {
	if h == nil {
		return
	}

	nextRoutes := make(map[string]managementRouteRecord)
	for _, record := range h.Snapshot().records {
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
				route:    item,
			}
		}
	}

	h.mu.Lock()
	h.managementRoutes = nextRoutes
	h.mu.Unlock()
}

func (h *Host) callManagementRegistrar(ctx context.Context, record capabilityRecord, plugin pluginapi.ManagementAPI) (resp pluginapi.ManagementRegistrationResponse, err error) {
	if h == nil || plugin == nil || h.isPluginFused(record.id) {
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
		Plugin:   record.meta,
		BasePath: managementBasePath,
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

func (h *Host) callManagementHandler(ctx context.Context, record managementRouteRecord, req pluginapi.ManagementRequest) (resp pluginapi.ManagementResponse, err error) {
	if h == nil || record.route.Handler == nil || h.isPluginFused(record.pluginID) {
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
