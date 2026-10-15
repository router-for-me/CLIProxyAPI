package pluginhost

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type quotaSDKFixture struct{ t *testing.T }

func (p quotaSDKFixture) Identifier() string { return "fixture" }
func (p quotaSDKFixture) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{"fixture"}, SupportsReset: true}, nil
}
func (p quotaSDKFixture) FetchQuota(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	if req.AuthID != "credential" || req.Metadata["fixture"] != "value" {
		p.t.Fatal("SDK dropped credential context")
	}
	return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "fixture-plan"}}, nil
}
func (p quotaSDKFixture) ResetQuota(_ context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	if req.AuthID != "credential" {
		p.t.Fatal("SDK dropped reset credential")
	}
	return pluginapi.QuotaResetResponse{Success: true}, nil
}

type quotaSelectionSDKFixture struct {
	quotaSDKFixture
	identifier string
	broken     bool
}

func (p quotaSelectionSDKFixture) Identifier() string { return p.identifier }

func (p quotaSelectionSDKFixture) DescribeQuota(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	if p.broken {
		panic("broken quota description")
	}
	return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{"foo"}}, nil
}

func TestQuotaProviderPluginIDsSDK(t *testing.T) {
	for _, healthyIdentifier := range []string{"foo", "healthy-quota"} {
		t.Run(healthyIdentifier, func(t *testing.T) {
			host := New()
			host.inner.RegisterPluginForTest("a-broken", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				QuotaProvider: quotaSelectionSDKFixture{identifier: "foo", broken: true},
			}})
			host.inner.RegisterPluginForTest("b-healthy", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
				QuotaProvider: quotaSelectionSDKFixture{identifier: healthyIdentifier},
			}})
			got := host.QuotaProviderPluginIDs(context.Background())
			if got["foo"] != "b-healthy" {
				t.Fatalf("selected plugin = %q, want b-healthy", got["foo"])
			}
			if _, exists := got["a-broken"]; exists {
				t.Fatal("mapping includes a fused plugin's alias")
			}
			if host.HasQuotaProviderForPlugin("a-broken") {
				t.Fatal("broken plugin must be unavailable through the public API")
			}
			for provider, pluginID := range got {
				if !host.HasQuotaProviderForPlugin(pluginID) {
					t.Fatalf("provider %q references unavailable plugin %q", provider, pluginID)
				}
			}
		})
	}
}

func TestQuotaProviderPluginIDsSDKWithoutHost(t *testing.T) {
	for _, host := range []*Host{nil, {}, New()} {
		for _, ctx := range []context.Context{nil, context.Background()} {
			if got := host.QuotaProviderPluginIDs(ctx); len(got) != 0 {
				t.Fatalf("expected no quota plugins, got %#v", got)
			}
		}
	}
}

func TestQuotaSDKEmbedding(t *testing.T) {
	host := New()
	host.inner.RegisterPluginForTest("fixture-plugin", pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: quotaSDKFixture{t}}})
	ctx := context.Background()
	if len(host.QuotaProviders(ctx)) != 1 || !host.HasQuotaProviderContext(ctx, "fixture") || !host.HasQuotaProviderForPlugin("fixture-plugin") {
		t.Fatal("missing public quota capabilities")
	}
	for _, byPlugin := range []bool{false, true} {
		req := pluginapi.QuotaFetchRequest{Provider: "fixture", AuthID: "credential", Metadata: map[string]any{"fixture": "value"}}
		var result pluginapi.QuotaFetchResponse
		var handled bool
		var err error
		if byPlugin {
			result, handled, err = host.FetchQuotaByPlugin(ctx, "fixture-plugin", req)
		} else {
			result, handled, err = host.FetchQuota(ctx, req)
		}
		if err != nil || !handled || result.Subscription == nil || result.Subscription.Plan != "fixture-plan" {
			t.Fatalf("fetch: handled=%v err=%v", handled, err)
		}
		reset := pluginapi.QuotaResetRequest{Provider: "fixture", AuthID: "credential"}
		var resetResult pluginapi.QuotaResetResponse
		if byPlugin {
			resetResult, handled, err = host.ResetQuotaByPlugin(ctx, "fixture-plugin", reset)
		} else {
			resetResult, handled, err = host.ResetQuota(ctx, reset)
		}
		if err != nil || !handled || !resetResult.Success {
			t.Fatalf("reset: handled=%v err=%v", handled, err)
		}
	}
}
