package pluginhost

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MoeForge/moego/ojson"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func storedRefreshPolicy(raw []byte) (*pluginapi.AuthRefreshPolicy, error) {
	value := ojson.GetBytes(raw, "host_refresh_policy")
	if !value.Exists() {
		return nil, nil
	}
	var policy pluginapi.AuthRefreshPolicy
	if errDecode := json.Unmarshal([]byte(value.Raw), &policy); errDecode != nil || !policy.Valid() {
		return nil, fmt.Errorf("invalid host_refresh_policy")
	}
	return &policy, nil
}

func preserveRefreshPolicy(data *pluginapi.AuthData, auth *coreauth.Auth) {
	if data.RefreshPolicy == nil && auth != nil && auth.RefreshPolicy != nil {
		policy := *auth.RefreshPolicy
		data.RefreshPolicy = &policy
	}
}

func lazyRefreshMetadata(data *pluginapi.AuthData, auth *coreauth.Auth) {
	preserveRefreshPolicy(data, auth)
	if data.RefreshPolicy == nil || data.RefreshPolicy.Mode != pluginapi.AuthRefreshLazy || len(data.StorageJSON) == 0 {
		return
	}
	var stored map[string]any
	if errDecode := json.Unmarshal(data.StorageJSON, &stored); errDecode != nil {
		return
	}
	metadata := cloneAnyMap(auth.Metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	for key, value := range stored {
		if coreauth.IsAuthTokenPayloadKey(key) || key == "expires_at" || key == "expiresAt" || key == "expiry" {
			metadata[key] = value
		}
	}
	for key, value := range data.Metadata {
		metadata[key] = value
	}
	data.Metadata = metadata
}

func (h *Host) freshQuotaAuth(ctx context.Context, id, index string) (*coreauth.Auth, error) {
	manager := h.currentAuthManager()
	if manager == nil {
		return nil, nil
	}
	var auth *coreauth.Auth
	if index != "" {
		var errLookup error
		auth, errLookup = h.authByIndex(index)
		if errLookup != nil {
			return nil, nil
		}
	} else if id != "" {
		auth, _ = manager.GetByID(id)
	}
	if auth == nil || !auth.UsesLazyRefresh() {
		return nil, nil
	}
	if index != "" && id != "" && id != auth.ID {
		return nil, fmt.Errorf("quota auth ID and index do not match")
	}
	return manager.EnsureAuthFresh(ctx, auth)
}
