package pluginhost

import (
	"context"
	"fmt"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// RegisterPluginForTest registers a plugin directly on the host for testing.
func (h *Host) RegisterPluginForTest(id string, plugin pluginapi.Plugin) {
	if h == nil {
		return
	}
	record := capabilityRecord{
		id:       id,
		path:     "test/" + id,
		version:  "1.0.0",
		meta:     plugin.Metadata,
		plugin:   plugin,
		priority: 1,
	}
	records := append(h.activeRecords(), record)
	h.mu.Lock()
	h.rebuildActivePluginMapsLocked(records)
	h.snapshot.Store(&Snapshot{enabled: true, records: records, quotaSupportedProviders: make(map[string][]string)})
	h.mu.Unlock()
}

type RegisteredQuotaProviderInfo struct {
	PluginID           string   `json:"plugin_id"`
	Provider           string   `json:"provider"`
	DisplayName        string   `json:"display_name,omitempty"`
	SupportedProviders []string `json:"supported_providers,omitempty"`
	SupportsReset      bool     `json:"supports_reset"`
}

// QuotaProviderIdentifiers returns the list of unique quota provider identifiers.
func (h *Host) QuotaProviderIdentifiers() []string {
	if h == nil {
		return nil
	}
	out := make([]string, 0)
	seen := make(map[string]struct{})
	for _, record := range h.activeRecords() {
		provider := record.plugin.Capabilities.QuotaProvider
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		identifier, okIdentifier := h.callQuotaIdentifier(record.id, provider)
		if okIdentifier && identifier != "" {
			if _, exists := seen[identifier]; !exists {
				seen[identifier] = struct{}{}
				out = append(out, identifier)
			}
		}
	}
	return out
}

// QuotaProviders returns information about all active registered quota providers.
func (h *Host) QuotaProviders(ctx context.Context) []RegisteredQuotaProviderInfo {
	if h == nil {
		return nil
	}
	out := make([]RegisteredQuotaProviderInfo, 0)
	for _, record := range h.activeRecords() {
		provider := record.plugin.Capabilities.QuotaProvider
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		identifier, okIdentifier := h.callQuotaIdentifier(record.id, provider)
		if !okIdentifier {
			identifier = normalizeProviderID(record.id)
		}
		desc, _, _ := h.callQuotaDescribe(ctx, record, provider)
		supported := desc.SupportedProviders
		if len(supported) == 0 && identifier != "" {
			supported = []string{identifier}
		}
		displayName := desc.DisplayName
		if displayName == "" {
			displayName = record.meta.Name
			if displayName == "" {
				displayName = identifier
			}
		}
		out = append(out, RegisteredQuotaProviderInfo{
			PluginID:           record.id,
			Provider:           identifier,
			DisplayName:        displayName,
			SupportedProviders: supported,
			SupportsReset:      desc.SupportsReset,
		})
	}
	return out
}

// HasQuotaProvider reports whether a quota provider exists for the given provider key.
func (h *Host) HasQuotaProvider(provider string) bool {
	return h.HasQuotaProviderContext(context.Background(), provider)
}

// HasQuotaProviderContext reports whether a quota provider exists with context cancellation support.
func (h *Host) HasQuotaProviderContext(ctx context.Context, provider string) bool {
	return h.quotaProviderRecord(ctx, provider) != nil
}

// HasQuotaProviderForPlugin reports whether the given plugin ID provides quota capabilities.
func (h *Host) HasQuotaProviderForPlugin(pluginID string) bool {
	return h.quotaProviderRecordByPlugin(pluginID) != nil
}

// QuotaSupportedProvidersSet returns a point-in-time set of all provider keys supported by active quota providers.
// This allows callers (e.g. ListAuthFiles) to discover all quota-capable providers with zero per-credential RPCs.
func (h *Host) QuotaSupportedProvidersSet(ctx context.Context) map[string]struct{} {
	if h == nil {
		return nil
	}
	snap := h.Snapshot()
	if snap == nil {
		return nil
	}
	records := h.activeRecordsFromSnapshot(snap)
	out := make(map[string]struct{})
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return out
		}
		quotaProvider := record.plugin.Capabilities.QuotaProvider
		if quotaProvider == nil || h.isPluginFused(record.id) {
			continue
		}
		if identifier, okID := h.callQuotaIdentifier(record.id, quotaProvider); okID && identifier != "" {
			out[identifier] = struct{}{}
		}
		if normID := normalizeProviderID(record.id); normID != "" {
			out[normID] = struct{}{}
		}
		if record.plugin.Capabilities.AuthProvider != nil {
			if authID, okAuth := h.callAuthProviderIdentifier(record.id, record.plugin.Capabilities.AuthProvider); okAuth && authID != "" {
				out[authID] = struct{}{}
			}
		}
		supported := snap.cachedQuotaSupportedProviders(ctx, h, record, quotaProvider)
		for _, p := range supported {
			if clean := normalizeProviderID(p); clean != "" {
				out[clean] = struct{}{}
			}
		}
	}
	return out
}

func (h *Host) quotaProviderRecord(ctx context.Context, provider string) *capabilityRecord {
	provider = normalizeProviderID(provider)
	if h == nil || provider == "" {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	snap := h.Snapshot()
	if snap == nil {
		return nil
	}
	records := h.activeRecordsFromSnapshot(snap)
	// First pass: match exact QuotaProvider.Identifier(), plugin ID, or AuthProvider.Identifier()
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil
		}
		quotaProvider := record.plugin.Capabilities.QuotaProvider
		if quotaProvider == nil || h.isPluginFused(record.id) {
			continue
		}
		identifier, okIdentifier := h.callQuotaIdentifier(record.id, quotaProvider)
		if okIdentifier && identifier == provider {
			r := record
			return &r
		}
		if normalizeProviderID(record.id) == provider {
			r := record
			return &r
		}
		if record.plugin.Capabilities.AuthProvider != nil {
			if authID, okAuth := h.callAuthProviderIdentifier(record.id, record.plugin.Capabilities.AuthProvider); okAuth && authID == provider {
				r := record
				return &r
			}
		}
	}
	// Second pass: match SupportedProviders declared by DescribeQuota
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil
		}
		quotaProvider := record.plugin.Capabilities.QuotaProvider
		if quotaProvider == nil || h.isPluginFused(record.id) {
			continue
		}
		supported := snap.cachedQuotaSupportedProviders(ctx, h, record, quotaProvider)
		for _, p := range supported {
			if normalizeProviderID(p) == provider {
				r := record
				return &r
			}
		}
	}
	return nil
}

func (snap *Snapshot) cachedQuotaSupportedProviders(ctx context.Context, h *Host, record capabilityRecord, provider pluginapi.QuotaProvider) []string {
	if snap == nil {
		return nil
	}
	snap.quotaSupportedProvidersMu.RLock()
	cached, found := snap.quotaSupportedProviders[record.id]
	snap.quotaSupportedProvidersMu.RUnlock()
	if found {
		return cached
	}

	if ctx == nil {
		ctx = context.Background()
	}
	desc, okDesc, errDesc := h.callQuotaDescribe(ctx, record, provider)
	if !okDesc || errDesc != nil {
		return nil
	}

	supported := append([]string(nil), desc.SupportedProviders...)
	snap.quotaSupportedProvidersMu.Lock()
	if snap.quotaSupportedProviders == nil {
		snap.quotaSupportedProviders = make(map[string][]string)
	}
	// Cache the result (even if empty slice) on this snapshot instance
	snap.quotaSupportedProviders[record.id] = supported
	snap.quotaSupportedProvidersMu.Unlock()
	return supported
}

func (h *Host) quotaProviderRecordByPlugin(pluginID string) *capabilityRecord {
	pluginID = strings.TrimSpace(pluginID)
	if h == nil || pluginID == "" {
		return nil
	}
	for _, record := range h.activeRecords() {
		if record.id == pluginID && record.plugin.Capabilities.QuotaProvider != nil && !h.isPluginFused(record.id) {
			r := record
			return &r
		}
	}
	return nil
}

// DescribeQuota queries a plugin quota provider for metadata.
func (h *Host) DescribeQuota(ctx context.Context, pluginID string) (pluginapi.QuotaDescribeResponse, bool, error) {
	record := h.quotaProviderRecordByPlugin(pluginID)
	if record == nil {
		record = h.quotaProviderRecord(ctx, pluginID)
	}
	if record == nil || record.plugin.Capabilities.QuotaProvider == nil {
		return pluginapi.QuotaDescribeResponse{}, false, nil
	}
	return h.callQuotaDescribe(ctx, *record, record.plugin.Capabilities.QuotaProvider)
}

// FetchQuota queries quota information for a credential by provider or plugin ID.
func (h *Host) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error) {
	record := h.quotaProviderRecord(ctx, req.Provider)
	if record == nil && req.Provider != "" {
		record = h.quotaProviderRecordByPlugin(req.Provider)
	}
	if record == nil {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	return h.callQuotaFetch(ctx, *record, record.plugin.Capabilities.QuotaProvider, req)
}

// FetchQuotaByPlugin queries quota information for a credential targeting a specific plugin.
func (h *Host) FetchQuotaByPlugin(ctx context.Context, pluginID string, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, bool, error) {
	record := h.quotaProviderRecordByPlugin(pluginID)
	if record == nil {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	return h.callQuotaFetch(ctx, *record, record.plugin.Capabilities.QuotaProvider, req)
}

// ResetQuota resets quota or usage for a credential if supported.
func (h *Host) ResetQuota(ctx context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, bool, error) {
	record := h.quotaProviderRecord(ctx, req.Provider)
	if record == nil && req.Provider != "" {
		record = h.quotaProviderRecordByPlugin(req.Provider)
	}
	if record == nil {
		return pluginapi.QuotaResetResponse{}, false, nil
	}
	return h.callQuotaReset(ctx, *record, record.plugin.Capabilities.QuotaProvider, req)
}

// ResetQuotaByPlugin resets quota or usage for a credential targeting a specific plugin.
func (h *Host) ResetQuotaByPlugin(ctx context.Context, pluginID string, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, bool, error) {
	record := h.quotaProviderRecordByPlugin(pluginID)
	if record == nil {
		return pluginapi.QuotaResetResponse{}, false, nil
	}
	return h.callQuotaReset(ctx, *record, record.plugin.Capabilities.QuotaProvider, req)
}

func (h *Host) callQuotaIdentifier(pluginID string, provider pluginapi.QuotaProvider) (identifier string, ok bool) {
	if h == nil || provider == nil || h.isPluginFused(pluginID) {
		return "", false
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(pluginID, "QuotaProvider.Identifier", recovered)
			identifier = ""
			ok = false
		}
	}()
	identifier = normalizeProviderID(provider.Identifier())
	return identifier, identifier != ""
}

func (h *Host) callQuotaDescribe(ctx context.Context, record capabilityRecord, provider pluginapi.QuotaProvider) (resp pluginapi.QuotaDescribeResponse, handled bool, err error) {
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.QuotaDescribeResponse{}, false, nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "QuotaProvider.DescribeQuota", recovered)
			resp = pluginapi.QuotaDescribeResponse{}
			handled = false
			err = fmt.Errorf("plugin %s panicked during DescribeQuota: %v", record.id, recovered)
		}
	}()
	desc, errDescribe := provider.DescribeQuota(ctx, pluginapi.QuotaDescribeRequest{
		Plugin: record.meta,
	})
	if errDescribe != nil {
		return pluginapi.QuotaDescribeResponse{}, true, errDescribe
	}
	return desc, true, nil
}

func (h *Host) callQuotaFetch(ctx context.Context, record capabilityRecord, provider pluginapi.QuotaProvider, req pluginapi.QuotaFetchRequest) (resp pluginapi.QuotaFetchResponse, handled bool, err error) {
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.QuotaFetchResponse{}, false, nil
	}
	var authRecord *coreauth.Auth
	if req.AuthIndex != "" {
		if auth, rawJSON, errLookup := h.authPhysicalJSONByIndex(req.AuthIndex); errLookup == nil && auth != nil {
			authRecord = auth
			if req.AuthID == "" {
				req.AuthID = auth.ID
			}
			if req.Provider == "" {
				req.Provider = auth.Provider
			}
			if len(req.StorageJSON) == 0 {
				req.StorageJSON = rawJSON
			}
			if req.Metadata == nil {
				req.Metadata = auth.Metadata
			}
			if req.Attributes == nil {
				req.Attributes = auth.Attributes
			}
		}
	}
	if req.HTTPClient == nil {
		req.HTTPClient = h.newHTTPClient(authRecord, req.Provider)
	}
	req.Host = h.hostConfigSummary()
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "QuotaProvider.FetchQuota", recovered)
			resp = pluginapi.QuotaFetchResponse{}
			handled = false
			err = fmt.Errorf("plugin %s panicked during FetchQuota: %v", record.id, recovered)
		}
	}()
	quotaResp, errFetch := provider.FetchQuota(ctx, req)
	if errFetch != nil {
		return pluginapi.QuotaFetchResponse{}, true, errFetch
	}
	return quotaResp, true, nil
}

func (h *Host) callQuotaReset(ctx context.Context, record capabilityRecord, provider pluginapi.QuotaProvider, req pluginapi.QuotaResetRequest) (resp pluginapi.QuotaResetResponse, handled bool, err error) {
	if h == nil || provider == nil || h.isPluginFused(record.id) || !h.recordCurrent(record) {
		return pluginapi.QuotaResetResponse{}, false, nil
	}
	var authRecord *coreauth.Auth
	if req.AuthIndex != "" {
		if auth, rawJSON, errLookup := h.authPhysicalJSONByIndex(req.AuthIndex); errLookup == nil && auth != nil {
			authRecord = auth
			if req.AuthID == "" {
				req.AuthID = auth.ID
			}
			if req.Provider == "" {
				req.Provider = auth.Provider
			}
			if len(req.StorageJSON) == 0 {
				req.StorageJSON = rawJSON
			}
			if req.Metadata == nil {
				req.Metadata = auth.Metadata
			}
			if req.Attributes == nil {
				req.Attributes = auth.Attributes
			}
		}
	}
	if req.HTTPClient == nil {
		req.HTTPClient = h.newHTTPClient(authRecord, req.Provider)
	}
	req.Host = h.hostConfigSummary()
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "QuotaProvider.ResetQuota", recovered)
			resp = pluginapi.QuotaResetResponse{}
			handled = false
			err = fmt.Errorf("plugin %s panicked during ResetQuota: %v", record.id, recovered)
		}
	}()
	resetResp, errReset := provider.ResetQuota(ctx, req)
	if errReset != nil {
		return pluginapi.QuotaResetResponse{}, true, errReset
	}
	return resetResp, true, nil
}
