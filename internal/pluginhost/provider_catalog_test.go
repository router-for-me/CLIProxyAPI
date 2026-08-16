package pluginhost

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostProviderListCallbackReturnsSortedUnion(t *testing.T) {
	host := New()
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "claude-disabled",
		Provider: " Claude ",
		Disabled: true,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	host.executorProviders["xai"] = struct{}{}
	host.executorProviders["CLAUDE"] = struct{}{}

	rawResp, errCall := host.callFromPlugin(context.Background(), pluginabi.MethodHostProviderList, nil)
	if errCall != nil {
		t.Fatalf("callFromPlugin() error = %v", errCall)
	}
	resp, errDecode := decodeRPCEnvelope[pluginapi.HostProviderListResponse](rawResp)
	if errDecode != nil {
		t.Fatalf("decode response: %v", errDecode)
	}
	if len(resp.Providers) != 2 || resp.Providers[0].ID != "claude" || resp.Providers[1].ID != "xai" {
		t.Fatalf("providers = %#v, want claude and xai", resp.Providers)
	}
}
