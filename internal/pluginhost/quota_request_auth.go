package pluginhost

import (
	"context"
	"fmt"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func (h *Host) requestQuotaAuth(ctx context.Context, id, index string, metadata map[string]any) (*coreauth.Auth, error) {
	manager := h.currentAuthManager()
	if manager == nil {
		if metadata[refreshPolicyStorageKey] == string(pluginapi.RefreshOnDemand) {
			return nil, fmt.Errorf("managed quota operation requires an auth manager")
		}
		return nil, nil
	}
	var auth *coreauth.Auth
	if strings.TrimSpace(index) != "" {
		found, err := h.authByIndex(index)
		if err != nil {
			if metadata[refreshPolicyStorageKey] == string(pluginapi.RefreshOnDemand) {
				return nil, err
			}
			if _, registered := manager.GetByID(id); id != "" && registered {
				return nil, fmt.Errorf("quota auth ID and index disagree")
			}
			return nil, nil
		}
		auth = found
		if id != "" && auth.ID != id {
			return nil, fmt.Errorf("quota auth ID and index disagree")
		}
	} else if strings.TrimSpace(id) != "" {
		var found bool
		auth, found = manager.GetByID(id)
		if !found {
			if metadata[refreshPolicyStorageKey] == string(pluginapi.RefreshOnDemand) {
				return nil, fmt.Errorf("quota auth is not registered")
			}
			return nil, nil
		}
	}
	if auth == nil {
		if metadata[refreshPolicyStorageKey] == string(pluginapi.RefreshOnDemand) {
			return nil, fmt.Errorf("managed quota operation requires registered auth")
		}
		return nil, nil
	}
	fresh, err := manager.EnsureRequestAuth(ctx, auth, "")
	if fresh == auth && err == nil {
		return nil, nil
	}
	return fresh, err
}
