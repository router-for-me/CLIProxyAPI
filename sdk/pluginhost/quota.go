package pluginhost

import (
	"context"

	internalpluginhost "github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// RegisteredQuotaProviderInfo describes quota capabilities exposed to embedders.
type RegisteredQuotaProviderInfo = internalpluginhost.RegisteredQuotaProviderInfo

// QuotaProviders lists active quota providers using the request context.
func (h *Host) QuotaProviders(ctx context.Context) []RegisteredQuotaProviderInfo {
	if h == nil || h.inner == nil {
		return nil
	}
	return h.inner.QuotaProviders(ctx)
}

// HasQuotaProviderContext reports whether a provider handles credential quotas.
func (h *Host) HasQuotaProviderContext(ctx context.Context, provider string) bool {
	return h != nil && h.inner != nil && h.inner.HasQuotaProviderContext(ctx, provider)
}

// HasQuotaProviderForPlugin reports whether a plugin exposes quota operations.
func (h *Host) HasQuotaProviderForPlugin(pluginID string) bool {
	return h != nil && h.inner != nil && h.inner.HasQuotaProviderForPlugin(pluginID)
}

// FetchQuota invokes a quota provider through the public embedding API.
func (h *Host) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error) {
	if h == nil || h.inner == nil {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	return h.inner.FetchQuota(ctx, req)
}

// FetchQuotaByPlugin invokes a quota provider through the public embedding API.
func (h *Host) FetchQuotaByPlugin(ctx context.Context, pluginID string, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error) {
	if h == nil || h.inner == nil {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	return h.inner.FetchQuotaByPlugin(ctx, pluginID, req)
}

// ResetQuota invokes a quota provider through the public embedding API.
func (h *Host) ResetQuota(ctx context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, bool, error) {
	if h == nil || h.inner == nil {
		return pluginapi.QuotaResetResponse{}, false, nil
	}
	return h.inner.ResetQuota(ctx, req)
}

// ResetQuotaByPlugin invokes a quota provider through the public embedding API.
func (h *Host) ResetQuotaByPlugin(ctx context.Context, pluginID string, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, bool, error) {
	if h == nil || h.inner == nil {
		return pluginapi.QuotaResetResponse{}, false, nil
	}
	return h.inner.ResetQuotaByPlugin(ctx, pluginID, req)
}
