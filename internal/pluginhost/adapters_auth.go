package pluginhost

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func (h *Host) RegisterFrontendAuthProviders() {
	if h == nil {
		return
	}

	type exclusiveFrontendAuthCandidate struct {
		key      string
		pluginID string
		priority int
	}

	nextKeys := make(map[string]struct{})
	var bestExclusive exclusiveFrontendAuthCandidate
	for _, record := range h.activeRecords() {
		provider := record.plugin.Capabilities.FrontendAuthProvider
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		adapter := &accessAdapter{
			host:     h,
			pluginID: record.id,
			path:     record.path,
			version:  record.version,
			provider: provider,
		}
		key := strings.TrimSpace(adapter.Identifier())
		if key == "" {
			continue
		}
		sdkaccess.RegisterProvider(key, adapter)
		nextKeys[key] = struct{}{}
		if record.plugin.Capabilities.FrontendAuthProviderExclusive {
			candidate := exclusiveFrontendAuthCandidate{
				key:      key,
				pluginID: record.id,
				priority: record.priority,
			}
			if bestExclusive.key == "" ||
				candidate.priority > bestExclusive.priority ||
				(candidate.priority == bestExclusive.priority && candidate.pluginID < bestExclusive.pluginID) {
				bestExclusive = candidate
			}
		}
	}

	if bestExclusive.key != "" {
		sdkaccess.SetExclusiveProvider(bestExclusive.key)
	} else {
		sdkaccess.ClearExclusiveProvider()
	}
	h.pruneStaleAccessProviders(nextKeys)
}

func (h *Host) pruneStaleAccessProviders(nextKeys map[string]struct{}) {
	if h == nil {
		return
	}

	staleKeys := make([]string, 0)
	h.mu.Lock()
	for key := range h.accessProviderKeys {
		if _, okKey := nextKeys[key]; !okKey {
			staleKeys = append(staleKeys, key)
		}
	}
	h.accessProviderKeys = nextKeys
	h.mu.Unlock()

	for _, key := range staleKeys {
		sdkaccess.UnregisterProvider(key)
	}
}

type accessAdapter struct {
	host     *Host
	pluginID string
	path     string
	version  string
	provider pluginapi.FrontendAuthProvider
}

func (a *accessAdapter) Identifier() (identifier string) {
	if a == nil || a.provider == nil {
		return ""
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if a.host != nil {
				a.host.fusePlugin(a.pluginID, "FrontendAuthProvider.Identifier", recovered)
			}
			identifier = ""
		}
	}()
	pluginID := strings.TrimSpace(a.pluginID)
	providerID := strings.TrimSpace(a.provider.Identifier())
	if pluginID == "" || providerID == "" {
		return ""
	}
	return "plugin:" + pluginID + ":" + providerID
}

func (a *accessAdapter) Authenticate(ctx context.Context, r *http.Request) (result *sdkaccess.Result, authErr *sdkaccess.AuthError) {
	if a == nil || a.provider == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return nil, sdkaccess.NewNotHandledError()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "FrontendAuthProvider.Authenticate", recovered)
			result = nil
			authErr = sdkaccess.NewNotHandledError()
		}
	}()

	body, errReadAll := readAndRestoreRequestBody(r)
	if errReadAll != nil {
		return nil, sdkaccess.NewInternalAuthError("failed to read plugin auth request body", errReadAll)
	}
	resp, errAuthenticate := a.provider.Authenticate(ctx, pluginapi.FrontendAuthRequest{
		Method:  r.Method,
		Path:    r.URL.Path,
		Headers: cloneHeader(r.Header),
		Query:   cloneValues(r.URL.Query()),
		Body:    bytes.Clone(body),
	})
	if errAuthenticate != nil || !resp.Authenticated {
		return nil, sdkaccess.NewNotHandledError()
	}
	providerID := a.Identifier()
	if providerID == "" {
		return nil, sdkaccess.NewNotHandledError()
	}
	return &sdkaccess.Result{
		Provider:  providerID,
		Principal: resp.Principal,
		Metadata:  cloneStringMap(resp.Metadata),
	}, nil
}
