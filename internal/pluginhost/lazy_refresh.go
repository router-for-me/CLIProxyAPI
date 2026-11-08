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
	if !auth.UsesLazyRefresh() && (data.RefreshPolicy == nil || data.RefreshPolicy.Mode != pluginapi.AuthRefreshLazy) {
		return
	}
	if len(data.StorageJSON) == 0 {
		data.StorageJSON = storageJSONFromAuth(auth)
	}
	var stored map[string]any
	if len(data.StorageJSON) > 0 && json.Unmarshal(data.StorageJSON, &stored) != nil {
		return
	}
	data.Metadata = coreauth.MergePluginRefreshMetadata(auth.Metadata, stored, data.Metadata)
	normalized := coreauth.MergePluginRefreshMetadata(stored, nil, data.Metadata)
	if raw, errMarshal := json.Marshal(normalized); errMarshal == nil {
		data.StorageJSON = raw
	}
}

func (h *Host) freshQuotaAuth(ctx context.Context, id, index string) (*coreauth.Auth, error) {
	manager := h.currentAuthManager()
	if manager == nil {
		return nil, nil
	}
	var byID, byIndex *coreauth.Auth
	if id != "" {
		byID, _ = manager.GetByID(id)
	}
	if index != "" {
		byIndex, _ = h.authByIndex(index)
		if byID.UsesLazyRefresh() && (byIndex == nil || byIndex.ID != byID.ID) {
			return nil, fmt.Errorf("quota auth ID and index do not match")
		}
	}
	auth := byID
	if index != "" {
		auth = byIndex
	}
	if auth == nil || !auth.UsesLazyRefresh() {
		return nil, nil
	}
	if index != "" && id != "" && id != auth.ID {
		return nil, fmt.Errorf("quota auth ID and index do not match")
	}
	return manager.EnsureAuthFresh(ctx, auth)
}
