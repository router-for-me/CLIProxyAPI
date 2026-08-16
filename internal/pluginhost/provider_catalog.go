package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (h *Host) callHostProviderList(_ context.Context, request []byte) ([]byte, error) {
	if len(bytesTrimSpace(request)) > 0 {
		var req map[string]any
		if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode host provider list request: %w", errUnmarshal)
		}
	}
	seen := make(map[string]struct{})
	if manager := h.currentAuthManager(); manager != nil {
		for _, provider := range manager.ProviderCatalog() {
			provider = strings.ToLower(strings.TrimSpace(provider))
			if provider != "" {
				seen[provider] = struct{}{}
			}
		}
	}
	h.mu.Lock()
	for provider := range h.executorProviders {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "" {
			seen[provider] = struct{}{}
		}
	}
	h.mu.Unlock()
	providers := make([]pluginapi.HostProviderInfo, 0, len(seen))
	for provider := range seen {
		providers = append(providers, pluginapi.HostProviderInfo{ID: provider})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return marshalRPCResult(pluginapi.HostProviderListResponse{Providers: providers})
}
