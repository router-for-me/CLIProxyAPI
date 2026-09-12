package pluginhost

import (
	"bytes"
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func (h *Host) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	return h.RouteModelExcept(ctx, req, "")
}

func (h *Host) HasModelRouters() bool {
	return h.HasModelRoutersExcept("")
}

func (h *Host) HasModelRoutersExcept(skipPluginID string) bool {
	if h == nil {
		return false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	for _, record := range h.activeRecords() {
		if record.plugin.Capabilities.ModelRouter != nil && !h.isPluginFused(record.id) && record.id != skipPluginID {
			return true
		}
	}
	return false
}

func (h *Host) RouteModelExcept(ctx context.Context, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if h == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	skipPluginID = strings.TrimSpace(skipPluginID)
	req.AvailableProviders = h.availableProvidersSnapshot()
	for _, record := range h.activeRecords() {
		router := record.plugin.Capabilities.ModelRouter
		if router == nil || h.isPluginFused(record.id) || record.id == skipPluginID {
			continue
		}
		nextReq := cloneModelRouteRequest(req)
		nextReq.Plugin = clonePluginMetadata(record.meta)
		nextReq.PluginID = record.id
		resp, ok := h.callModelRouter(ctx, record.id, router, nextReq)
		if !ok || !resp.Handled {
			continue
		}
		resp, valid := normalizeModelRouteResponse(record.id, resp)
		if !valid {
			log.WithFields(log.Fields{"plugin_id": record.id, "target_kind": resp.TargetKind, "target": resp.Target}).Warn("pluginhost: model router returned invalid target")
			continue
		}
		switch resp.TargetKind {
		case pluginapi.ModelRouteTargetProvider:
			if !h.HasBuiltinProvider(resp.Target) {
				log.WithFields(log.Fields{"plugin_id": record.id, "target_provider": resp.Target}).Warn("pluginhost: model router returned unavailable provider")
				continue
			}
			return resp, true
		case pluginapi.ModelRouteTargetSelf, pluginapi.ModelRouteTargetExecutor:
			if !h.executorPluginReady(resp.Target, nextReq) {
				log.WithFields(log.Fields{"plugin_id": record.id, "target_plugin_id": resp.Target}).Warn("pluginhost: model router returned unavailable executor plugin")
				continue
			}
			return resp, true
		default:
			log.WithFields(log.Fields{"plugin_id": record.id, "target_kind": resp.TargetKind}).Warn("pluginhost: model router returned unsupported target kind")
			continue
		}
	}
	return pluginapi.ModelRouteResponse{}, false
}

func (h *Host) callModelRouter(ctx context.Context, pluginID string, router pluginapi.ModelRouter, req pluginapi.ModelRouteRequest) (out pluginapi.ModelRouteResponse, ok bool) {
	if h == nil || router == nil || h.isPluginFused(pluginID) {
		return pluginapi.ModelRouteResponse{}, false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "ModelRouter.RouteModel", recovered)
			out = pluginapi.ModelRouteResponse{}
			ok = false
		}
	}()
	resp, errRoute := router.RouteModel(ctx, req)
	if errRoute != nil {
		log.WithField("plugin_id", pluginID).WithError(errRoute).Warn("pluginhost: model router failed")
		return pluginapi.ModelRouteResponse{}, false
	}
	return resp, true
}

func normalizeModelRouteResponse(routerPluginID string, resp pluginapi.ModelRouteResponse) (pluginapi.ModelRouteResponse, bool) {
	resp.TargetModel = strings.TrimSpace(resp.TargetModel)
	switch resp.TargetKind {
	case pluginapi.ModelRouteTargetSelf:
		resp.Target = strings.TrimSpace(routerPluginID)
		if resp.Target == "" {
			return pluginapi.ModelRouteResponse{}, false
		}
		return resp, true
	case pluginapi.ModelRouteTargetExecutor:
		resp.Target = strings.TrimSpace(resp.Target)
		if resp.Target == "" {
			return pluginapi.ModelRouteResponse{}, false
		}
		return resp, true
	case pluginapi.ModelRouteTargetProvider:
		resp.Target = strings.ToLower(strings.TrimSpace(resp.Target))
		if resp.Target == "" {
			return pluginapi.ModelRouteResponse{}, false
		}
		return resp, true
	default:
		return pluginapi.ModelRouteResponse{}, false
	}
}

func cloneModelRouteRequest(req pluginapi.ModelRouteRequest) pluginapi.ModelRouteRequest {
	req.Headers = cloneHeader(req.Headers)
	req.Query = cloneValues(req.Query)
	req.Body = bytes.Clone(req.Body)
	req.Metadata = cloneInterceptorMetadata(req.Metadata)
	req.AvailableProviders = cloneStringSlice(req.AvailableProviders)
	return req
}

// HasBuiltinProvider reports whether a built-in provider currently has at least one
// registered auth record.
func (h *Host) HasBuiltinProvider(provider string) bool {
	if h == nil || h.authManager == nil {
		return false
	}
	return h.authManager.HasProviderAuth(provider)
}

// BuiltinProviders returns built-in provider keys that currently have auth registered.
func (h *Host) BuiltinProviders() []string {
	if h == nil || h.authManager == nil {
		return nil
	}
	return h.authManager.AvailableProviders()
}

// availableProvidersSnapshot returns a defensive copy of BuiltinProviders for routing input.
func (h *Host) availableProvidersSnapshot() []string {
	providers := h.BuiltinProviders()
	if len(providers) == 0 {
		return nil
	}
	return cloneStringSlice(providers)
}
