package pluginhost

import (
	"context"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

// ResolveEgressProxy resolves request-scoped proxy behavior using the highest-priority
// active resolver. An unhandled resolver preserves the configured proxy behavior.
func (h *Host) ResolveEgressProxy(ctx context.Context, req pluginapi.EgressProxyRequest) (pluginapi.EgressProxyResponse, bool, error) {
	if h == nil {
		return pluginapi.EgressProxyResponse{}, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	req.Model = strings.TrimSpace(req.Model)
	req.Operation = strings.ToLower(strings.TrimSpace(req.Operation))
	req.Auth.ID = strings.TrimSpace(req.Auth.ID)
	req.Auth.Index = strings.TrimSpace(req.Auth.Index)
	req.Auth.Provider = strings.ToLower(strings.TrimSpace(req.Auth.Provider))
	req.Auth.Label = strings.TrimSpace(req.Auth.Label)
	req.Auth.Email = strings.TrimSpace(req.Auth.Email)

	for _, record := range h.activeRecords() {
		resolver := record.plugin.Capabilities.EgressProxyResolver
		if resolver == nil || h.isPluginFused(record.id) {
			continue
		}
		resp, ok, errResolve := h.callEgressProxyResolver(ctx, record, resolver, req)
		if errResolve != nil {
			return pluginapi.EgressProxyResponse{}, true, fmt.Errorf("egress proxy resolver %s failed", record.id)
		}
		if !ok || !resp.Handled {
			continue
		}
		normalized, errNormalize := normalizeEgressProxyResponse(resp)
		if errNormalize != nil {
			return pluginapi.EgressProxyResponse{}, true, fmt.Errorf("resolve egress proxy with plugin %s: %w", record.id, errNormalize)
		}
		return normalized, true, nil
	}
	return pluginapi.EgressProxyResponse{}, false, nil
}

func (h *Host) callEgressProxyResolver(ctx context.Context, record capabilityRecord, resolver pluginapi.EgressProxyResolver, req pluginapi.EgressProxyRequest) (out pluginapi.EgressProxyResponse, ok bool, err error) {
	if h == nil || resolver == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.EgressProxyResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "EgressProxyResolver.ResolveEgressProxy", recovered)
			out = pluginapi.EgressProxyResponse{}
			ok = false
			err = fmt.Errorf("egress proxy resolver panic: %v", recovered)
		}
	}()
	resp, errResolve := resolver.ResolveEgressProxy(ctx, req)
	if errResolve != nil {
		log.WithField("plugin_id", record.id).Warn("pluginhost: egress proxy resolver failed")
		return pluginapi.EgressProxyResponse{}, true, errResolve
	}
	return resp, true, nil
}

func normalizeEgressProxyResponse(resp pluginapi.EgressProxyResponse) (pluginapi.EgressProxyResponse, error) {
	if !resp.Handled {
		return pluginapi.EgressProxyResponse{}, nil
	}
	resp.Mode = pluginapi.EgressProxyMode(strings.ToLower(strings.TrimSpace(string(resp.Mode))))
	resp.ProxyURL = strings.TrimSpace(resp.ProxyURL)
	switch resp.Mode {
	case pluginapi.EgressProxyModeInherit:
		resp.ProxyURL = ""
		return resp, nil
	case pluginapi.EgressProxyModeDirect:
		resp.ProxyURL = ""
		return resp, nil
	case pluginapi.EgressProxyModeProxy:
		setting, errParse := proxyutil.Parse(resp.ProxyURL)
		if errParse != nil || setting.Mode != proxyutil.ModeProxy {
			return pluginapi.EgressProxyResponse{}, fmt.Errorf("plugin returned invalid proxy URL")
		}
		return resp, nil
	default:
		return pluginapi.EgressProxyResponse{}, fmt.Errorf("plugin returned invalid proxy mode")
	}
}

// HasEgressProxyResolver reports whether the active plugin snapshot contains a resolver.
func (h *Host) HasEgressProxyResolver() bool {
	if h == nil {
		return false
	}
	for _, record := range h.activeRecords() {
		if record.plugin.Capabilities.EgressProxyResolver != nil && !h.isPluginFused(record.id) {
			return true
		}
	}
	return false
}
