package pluginhost

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"github.com/MoeForge/moego/json"
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

func mergeLazyRefreshData(data *pluginapi.AuthData, auth *coreauth.Auth) error {
	preserveRefreshPolicy(data, auth)
	if !auth.UsesLazyRefresh() && (data.RefreshPolicy == nil || data.RefreshPolicy.Mode != pluginapi.AuthRefreshLazy) {
		return nil
	}
	var inherited map[string]any
	if raw := storageJSONFromAuth(auth); len(raw) > 0 && json.Unmarshal(raw, &inherited) != nil {
		return fmt.Errorf("invalid current plugin storage")
	}
	var stored map[string]any
	if len(data.StorageJSON) > 0 && json.Unmarshal(data.StorageJSON, &stored) != nil {
		return fmt.Errorf("invalid plugin refresh storage")
	}
	inherited = coreauth.MergePluginRefreshMetadata(inherited, nil, auth.Metadata)
	data.Metadata = coreauth.MergePluginRefreshMetadata(inherited, stored, data.Metadata)
	raw, errEncode := encodePluginStorage(data.Metadata)
	if errEncode != nil {
		return fmt.Errorf("invalid plugin refresh metadata")
	}
	data.StorageJSON = raw
	return nil
}

func encodePluginStorage(fields map[string]any) ([]byte, error) {
	doc := ojson.NewDocString("{}")
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		if errSet := doc.Set(":"+ojson.Escape(key), fields[key]); errSet != nil {
			return nil, errSet
		}
	}
	return doc.Bytes(), nil
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
		if byID != nil && (byID.UsesLazyRefresh() || manager.HasLazyRefreshWork(byID.ID)) && (byIndex == nil || byIndex.ID != byID.ID) {
			return nil, fmt.Errorf("quota auth ID and index do not match")
		}
	}
	auth := byID
	if index != "" {
		auth = byIndex
	}
	if auth == nil {
		return nil, nil
	}
	if (auth.UsesLazyRefresh() || manager.HasLazyRefreshWork(auth.ID)) && index != "" && id != "" && id != auth.ID {
		return nil, fmt.Errorf("quota auth ID and index do not match")
	}
	fresh, required, errFresh := manager.EnsureAuthFreshForOperation(ctx, auth)
	if errFresh != nil {
		return nil, errFresh
	}
	if !required {
		return nil, nil
	}
	if index != "" && id != "" && id != fresh.ID {
		return nil, fmt.Errorf("quota auth ID and index do not match")
	}
	return fresh, nil
}
