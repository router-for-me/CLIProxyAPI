package pluginhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type testQuotaProvider struct {
	identifier string
	describeFn func(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error)
	fetchFn    func(context.Context, pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error)
	resetFn    func(context.Context, pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error)
}

func (p *testQuotaProvider) Identifier() string {
	return p.identifier
}

func (p *testQuotaProvider) DescribeQuota(ctx context.Context, req pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
	if p.describeFn != nil {
		return p.describeFn(ctx, req)
	}
	return pluginapi.QuotaDescribeResponse{
		SupportedProviders: []string{p.identifier},
		DisplayName:        "Test Quota Provider",
		SupportsReset:      true,
	}, nil
}

func (p *testQuotaProvider) FetchQuota(ctx context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
	if p.fetchFn != nil {
		return p.fetchFn(ctx, req)
	}
	return pluginapi.QuotaFetchResponse{
		Subscription: &pluginapi.QuotaSubscription{
			Plan:     "Pro",
			TierName: "Pro Tier",
			TierID:   "pro-1",
		},
		ServerTimeOffsetMs: 50,
		Groups: []pluginapi.QuotaGroup{
			{
				DisplayName: "Rolling Limits",
				Buckets: []pluginapi.QuotaBucket{
					{
						Window:            "daily",
						RemainingFraction: 0.75,
						ResetTime:         "2026-09-13T00:00:00Z",
						Description:       "75% remaining",
					},
				},
			},
		},
	}, nil
}

func (p *testQuotaProvider) ResetQuota(ctx context.Context, req pluginapi.QuotaResetRequest) (pluginapi.QuotaResetResponse, error) {
	if p.resetFn != nil {
		return p.resetFn(ctx, req)
	}
	return pluginapi.QuotaResetResponse{
		Success: true,
		Message: "quota reset successful",
	}, nil
}

func TestQuotaProviderCapability_Direct(t *testing.T) {
	provider := &testQuotaProvider{identifier: "opencode-go"}
	host := quotaCredentialHost(t, provider, &coreauth.Auth{
		ID: "direct-auth", Index: "auth-123", Provider: "opencode-go",
		Metadata: map[string]any{"api_key": "synthetic-direct-key"},
	})

	if !host.HasQuotaProvider("opencode-go") {
		t.Fatal("expected host to have quota provider for opencode-go")
	}

	resp, handled, errFetch := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:  "opencode-go",
		AuthIndex: "auth-123",
	})
	if errFetch != nil {
		t.Fatalf("unexpected fetch error: %v", errFetch)
	}
	if !handled {
		t.Fatal("expected fetch to be handled")
	}
	if resp.Subscription == nil || resp.Subscription.Plan != "Pro" {
		t.Fatalf("unexpected subscription: %+v", resp.Subscription)
	}
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 1 {
		t.Fatalf("unexpected groups: %+v", resp.Groups)
	}
	if resp.Groups[0].Buckets[0].RemainingFraction != 0.75 {
		t.Fatalf("unexpected remaining fraction: %f", resp.Groups[0].Buckets[0].RemainingFraction)
	}
}

func TestQuotaProviderCapability_Reset(t *testing.T) {
	provider := &testQuotaProvider{identifier: "opencode-go"}
	host := newHostWithRecords(capabilityRecord{
		id: "opencode-go-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: provider,
			},
		},
	})

	resp, handled, errReset := host.ResetQuota(context.Background(), pluginapi.QuotaResetRequest{
		Provider:  "opencode-go",
		AuthIndex: "auth-123",
	})
	if errReset != nil {
		t.Fatalf("unexpected reset error: %v", errReset)
	}
	if !handled || !resp.Success {
		t.Fatalf("expected reset handled successfully, got handled=%v, resp=%+v", handled, resp)
	}
}

func TestQuotaProviderCapability_Describe(t *testing.T) {
	provider := &testQuotaProvider{identifier: "opencode-go"}
	host := newHostWithRecords(capabilityRecord{
		id: "opencode-go-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: provider,
			},
		},
	})

	desc, handled, errDesc := host.DescribeQuota(context.Background(), "opencode-go-plugin")
	if errDesc != nil {
		t.Fatalf("unexpected describe error: %v", errDesc)
	}
	if !handled || desc.DisplayName != "Test Quota Provider" {
		t.Fatalf("unexpected describe response: %+v", desc)
	}
}

func TestQuotaProviderCapability_RegisteredPlugins(t *testing.T) {
	provider := &testQuotaProvider{identifier: "opencode-go"}
	host := newHostWithRecords(capabilityRecord{
		id: "opencode-go-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: provider,
			},
		},
	})

	plugins := host.RegisteredPlugins()
	if len(plugins) != 1 {
		t.Fatalf("expected 1 registered plugin, got %d", len(plugins))
	}
	if !plugins[0].SupportsQuota {
		t.Fatal("expected SupportsQuota to be true")
	}
	if plugins[0].QuotaProvider != "opencode-go" {
		t.Fatalf("expected QuotaProvider to be opencode-go, got %q", plugins[0].QuotaProvider)
	}
}

func TestQuotaProviderCapability_PanicFusing(t *testing.T) {
	provider := &testQuotaProvider{
		identifier: "panic-provider",
		fetchFn: func(context.Context, pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
			panic("boom")
		},
	}
	host := newHostWithRecords(capabilityRecord{
		id: "panic-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: provider,
			},
		},
	})

	_, handled, errFetch := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider: "panic-provider",
	})
	if errFetch == nil {
		t.Fatal("expected error after panic")
	}
	if handled {
		t.Fatal("expected handled to be false after panic")
	}
	if !host.isPluginFused("panic-plugin") {
		t.Fatal("expected plugin to be fused after panic")
	}
}

func TestQuotaProviderCapability_RPC(t *testing.T) {
	provider := &testQuotaProvider{identifier: "rpc-quota-provider"}
	plugin := validTestPlugin("rpc-quota-plugin")
	plugin.Capabilities.QuotaProvider = provider

	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: plugin,
	})

	host := New()
	registered, errRegister := registerRPCPlugin(context.Background(), host, "rpc-quota-plugin", lookup, pluginabi.MethodPluginRegister, nil)
	if errRegister != nil {
		t.Fatalf("registerRPCPlugin() error = %v", errRegister)
	}
	if registered.Capabilities.QuotaProvider == nil {
		t.Fatal("expected registered plugin to have QuotaProvider capability")
	}

	hostWithRPC := quotaCredentialHost(t, registered.Capabilities.QuotaProvider, &coreauth.Auth{
		ID: "rpc-auth", Index: "auth-rpc-1", Provider: "rpc-quota-provider",
		Metadata: map[string]any{"api_key": "synthetic-rpc-key"},
	})

	if !hostWithRPC.HasQuotaProvider("rpc-quota-provider") {
		t.Fatal("expected host to recognize rpc-quota-provider")
	}

	fetchResp, handled, errFetch := hostWithRPC.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider:  "rpc-quota-provider",
		AuthIndex: "auth-rpc-1",
	})
	if errFetch != nil {
		t.Fatalf("unexpected rpc fetch error: %v", errFetch)
	}
	if !handled || fetchResp.Subscription == nil || fetchResp.Subscription.Plan != "Pro" {
		t.Fatalf("unexpected rpc fetch response: %+v", fetchResp)
	}
}

func TestValidPlugin_QuotaProviderOnly(t *testing.T) {
	plugin := pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             "quota-only",
			Version:          "1.0.0",
			Author:           "author",
			GitHubRepository: "https://github.com/example/quota-only",
		},
		Capabilities: pluginapi.Capabilities{
			QuotaProvider: &testQuotaProvider{identifier: "quota-only-provider"},
		},
	}

	if !validPlugin(plugin) {
		t.Fatal("expected validPlugin to return true for plugin with only QuotaProvider capability")
	}
}

func TestQuotaProvider_MultipleSupportedProviders(t *testing.T) {
	provider := &testQuotaProvider{
		identifier: "multi-quota",
		describeFn: func(ctx context.Context, req pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
			return pluginapi.QuotaDescribeResponse{
				SupportedProviders: []string{"provider-alpha", "provider-beta"},
				DisplayName:        "Multi Quota Provider",
			}, nil
		},
	}

	host := newHostWithRecords(capabilityRecord{
		id: "multi-quota-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: provider,
			},
		},
	})

	if !host.HasQuotaProvider("provider-alpha") {
		t.Fatal("expected host to recognize supported provider-alpha")
	}
	if !host.HasQuotaProvider("provider-beta") {
		t.Fatal("expected host to recognize supported provider-beta")
	}

	resp, handled, errFetch := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider: "provider-alpha",
	})
	if errFetch != nil || !handled || resp.Subscription == nil || resp.Subscription.Plan != "Pro" {
		t.Fatalf("fetch failed for provider-alpha: handled=%v err=%v resp=%+v", handled, errFetch, resp)
	}
}

func TestQuotaProvider_VersionReloadInvalidatesCache(t *testing.T) {
	providerV1 := &testQuotaProvider{
		identifier: "dynamic-quota",
		describeFn: func(ctx context.Context, req pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
			return pluginapi.QuotaDescribeResponse{
				SupportedProviders: []string{"provider-v1"},
			}, nil
		},
	}

	host := New()
	setHostSnapshotForTest(host, true, capabilityRecord{
		id:      "dynamic-plugin",
		path:    "plugins/dynamic",
		version: "1.0.0",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: providerV1,
			},
		},
	})

	if !host.HasQuotaProvider("provider-v1") {
		t.Fatal("expected host to recognize provider-v1")
	}
	if host.HasQuotaProvider("provider-v2") {
		t.Fatal("expected host not to recognize provider-v2 before upgrade")
	}

	// Reload with version 2.0.0 supporting provider-v2 instead
	providerV2 := &testQuotaProvider{
		identifier: "dynamic-quota",
		describeFn: func(ctx context.Context, req pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
			return pluginapi.QuotaDescribeResponse{
				SupportedProviders: []string{"provider-v2"},
			}, nil
		},
	}
	setHostSnapshotForTest(host, true, capabilityRecord{
		id:      "dynamic-plugin",
		path:    "plugins/dynamic",
		version: "2.0.0",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: providerV2,
			},
		},
	})

	if !host.HasQuotaProvider("provider-v2") {
		t.Fatal("expected host to recognize provider-v2 after version upgrade")
	}
	if host.HasQuotaProvider("provider-v1") {
		t.Fatal("expected host not to recognize provider-v1 after version upgrade")
	}
}

func TestQuotaProvider_CrossHostIndependence(t *testing.T) {
	providerA := &testQuotaProvider{
		identifier: "isolated-quota",
		describeFn: func(ctx context.Context, req pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
			return pluginapi.QuotaDescribeResponse{
				SupportedProviders: []string{"provider-only-on-host-a"},
			}, nil
		},
	}

	hostA := newHostWithRecords(capabilityRecord{
		id: "plugin-a",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: providerA,
			},
		},
	})

	hostB := newHostWithRecords(capabilityRecord{
		id: "plugin-b",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{},
		},
	})

	if !hostA.HasQuotaProvider("provider-only-on-host-a") {
		t.Fatal("hostA should recognize provider-only-on-host-a")
	}
	if hostB.HasQuotaProvider("provider-only-on-host-a") {
		t.Fatal("hostB must not leak or share quota cache with hostA")
	}
}

func TestQuotaProvider_ContextCancellation(t *testing.T) {
	host := newHostWithRecords(capabilityRecord{
		id: "cancel-plugin",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: &testQuotaProvider{identifier: "cancel-provider"},
			},
		},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Searching for unknown provider with canceled context should terminate promptly without error
	if host.HasQuotaProviderContext(ctx, "non-existent-provider") {
		t.Fatal("expected false for canceled context search")
	}
}

func TestQuotaProvider_InterleavedReloadDiscardStaleCache(t *testing.T) {
	describeStarted := make(chan struct{})
	allowDescribeFinish := make(chan struct{})
	goroutineDone := make(chan struct{})

	providerV1 := &testQuotaProvider{
		identifier: "interleaved-provider",
		describeFn: func(ctx context.Context, req pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
			select {
			case <-describeStarted:
			default:
				close(describeStarted)
			}
			<-allowDescribeFinish
			return pluginapi.QuotaDescribeResponse{
				SupportedProviders: []string{"stale-provider"},
			}, nil
		},
	}

	host := New()
	setHostSnapshotForTest(host, true, capabilityRecord{
		id:      "interleaved-plugin",
		path:    "plugins/interleaved",
		version: "1.0.0",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: providerV1,
			},
		},
	})

	// Run Describe in goroutine
	go func() {
		defer close(goroutineDone)
		_ = host.HasQuotaProvider("stale-provider")
	}()

	// Wait for describe to start
	<-describeStarted

	// Trigger reload of same plugin with version 1.0.0 supporting "fresh-provider", incrementing generation
	providerV2 := &testQuotaProvider{
		identifier: "interleaved-provider",
		describeFn: func(ctx context.Context, req pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
			return pluginapi.QuotaDescribeResponse{
				SupportedProviders: []string{"fresh-provider"},
			}, nil
		},
	}
	setHostSnapshotForTest(host, true, capabilityRecord{
		id:      "interleaved-plugin",
		path:    "plugins/interleaved",
		version: "1.0.0",
		plugin: pluginapi.Plugin{
			Capabilities: pluginapi.Capabilities{
				QuotaProvider: providerV2,
			},
		},
	})

	// Allow the old DescribeQuota call to finally return
	close(allowDescribeFinish)
	// Wait for old goroutine to fully finish
	<-goroutineDone

	// Verify that the stale-provider is NOT cached on host because generation changed
	if host.HasQuotaProvider("stale-provider") {
		t.Fatal("stale-provider should not be cached after generation change")
	}
	if !host.HasQuotaProvider("fresh-provider") {
		t.Fatal("fresh-provider should be recognized for new generation")
	}
}

func TestRegisterRPCPlugin_ContextCancellation(t *testing.T) {
	plugin := validTestPlugin("cancel-register-plugin")
	plugin.Capabilities.QuotaProvider = &testQuotaProvider{identifier: "cancel-reg-provider"}

	lookup := newTestSymbolLookup(&testPlugin{
		registerResult: plugin,
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled context

	host := New()
	_, err := registerRPCPlugin(ctx, host, "cancel-register-plugin", lookup, pluginabi.MethodPluginRegister, nil)
	if err == nil {
		t.Fatal("expected error when registering with canceled context")
	}
}

func TestQuotaProviderPluginIDsSkipsPluginsFusedDuringDescribe(t *testing.T) {
	for _, healthyIdentifier := range []string{"foo", "healthy-quota"} {
		t.Run(healthyIdentifier, func(t *testing.T) {
			broken := &testQuotaProvider{
				identifier: "foo",
				describeFn: func(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
					panic("broken quota description")
				},
			}
			healthy := &testQuotaProvider{
				identifier: healthyIdentifier,
				describeFn: func(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
					return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{"foo"}}, nil
				},
			}
			host := newHostWithRecords(
				capabilityRecord{id: "a-broken", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: broken}}},
				capabilityRecord{id: "b-healthy", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: healthy}}},
			)
			ctx := context.Background()
			initial := host.quotaProviderRecord(ctx, "foo")
			if initial == nil || initial.id != "a-broken" {
				t.Fatalf("initial selection = %#v, want a-broken", initial)
			}

			got := host.QuotaProviderPluginIDs(ctx)
			if !host.isPluginFused("a-broken") {
				t.Fatal("expected a-broken to be fused during DescribeQuota")
			}
			if got["foo"] != "b-healthy" {
				t.Fatalf("quota plugin for foo = %q, want b-healthy", got["foo"])
			}
			if _, exists := got["a-broken"]; exists {
				t.Fatal("fused plugin's unique alias must be removed")
			}
			for provider, pluginID := range got {
				if !host.HasQuotaProviderForPlugin(pluginID) {
					t.Fatalf("provider %q references unavailable plugin %q", provider, pluginID)
				}
				record := host.quotaProviderRecord(ctx, provider)
				if record == nil || record.id != pluginID {
					t.Fatalf("quotaProviderRecord(%q) = %#v, want plugin %q", provider, record, pluginID)
				}
			}
		})
	}
}

func TestQuotaProviderPluginIDsMatchesQuotaProviderRecordPrecedence(t *testing.T) {
	multi := &testQuotaProvider{
		identifier: "multi-quota",
		describeFn: func(context.Context, pluginapi.QuotaDescribeRequest) (pluginapi.QuotaDescribeResponse, error) {
			return pluginapi.QuotaDescribeResponse{SupportedProviders: []string{" FOO ", "bar"}}, nil
		},
	}
	host := newHostWithRecords(
		capabilityRecord{id: "a-multi", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: multi}}},
		// Same-pass collision: the earlier record wins "foo" over z-foo.
		capabilityRecord{id: "b-foo", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: &testQuotaProvider{identifier: "foo"}}}},
		// AuthProvider alias is a first-pass match and beats a-multi's SupportedProviders for "bar".
		capabilityRecord{id: "m-bar-auth", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			QuotaProvider: &testQuotaProvider{identifier: "m-quota"},
			AuthProvider:  fakeAuthProvider{identifier: " Bar "},
		}}},
		capabilityRecord{id: "z-foo", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: &testQuotaProvider{identifier: "foo"}}}},
	)

	got := host.QuotaProviderPluginIDs(context.Background())
	want := map[string]string{
		"multi-quota": "a-multi",
		"a-multi":     "a-multi",
		"foo":         "b-foo",
		"b-foo":       "b-foo",
		"bar":         "m-bar-auth",
		"m-quota":     "m-bar-auth",
		"m-bar-auth":  "m-bar-auth",
		"z-foo":       "z-foo",
	}
	if len(got) != len(want) {
		t.Fatalf("QuotaProviderPluginIDs() = %#v, want %#v", got, want)
	}
	for provider, pluginID := range want {
		if got[provider] != pluginID {
			t.Fatalf("QuotaProviderPluginIDs()[%q] = %q, want %q", provider, got[provider], pluginID)
		}
		record := host.quotaProviderRecord(context.Background(), provider)
		if record == nil || record.id != pluginID {
			t.Fatalf("quotaProviderRecord(%q) = %#v, want plugin %q", provider, record, pluginID)
		}
	}
}

func quotaCredentialHost(t *testing.T, provider pluginapi.QuotaProvider, auths ...*coreauth.Auth) *Host {
	t.Helper()
	host := newHostWithRecords(capabilityRecord{
		id:     "quota-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{QuotaProvider: provider}},
	})
	manager := coreauth.NewManager(nil, nil, nil)
	host.SetAuthManager(manager)
	for _, auth := range auths {
		if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
			t.Fatal("register synthetic auth:", err)
		}
	}
	return host
}

func TestQuotaFetchSelectedCredentialSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		disk     bool
		raw      bool
		override bool
	}{
		{name: "memory differs from disk", disk: true, raw: true},
		{name: "runtime only", raw: true},
		{name: "metadata fallback"},
		{name: "caller overrides", disk: true, raw: true, override: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectedJSON := []byte(`{"api_key":"synthetic-selected-key"}`)
			auth := &coreauth.Auth{
				ID: "selected-auth", Index: "selected-index", Provider: "quota-provider",
				ProxyURL:   "http://selected-proxy.invalid:3128",
				Metadata:   map[string]any{"api_key": "synthetic-selected-key", "access_token": "synthetic-selected-token"},
				Attributes: map[string]string{"owner": "selected"},
			}
			if tc.raw {
				auth.Storage = &memoryAuthStorage{payload: selectedJSON}
				auth.Metadata["api_key"] = "synthetic-stale-metadata-key"
			} else {
				selectedJSON = []byte(`{"access_token":"synthetic-selected-token","api_key":"synthetic-selected-key"}`)
			}
			if tc.disk {
				path := filepath.Join(t.TempDir(), "auth.json")
				if err := os.WriteFile(path, []byte(`{"api_key":"synthetic-stale-disk-key"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				auth.Attributes["path"] = path
			}
			var captured pluginapi.QuotaFetchRequest
			calls := 0
			provider := &testQuotaProvider{identifier: "quota-provider", fetchFn: func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
				captured = req
				calls++
				return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "synthetic-plan"}}, nil
			}}
			host := quotaCredentialHost(t, provider, auth)
			req := pluginapi.QuotaFetchRequest{AuthIndex: auth.Index}
			if tc.override {
				req.AuthID = auth.ID
				req.Provider = auth.Provider
				req.StorageJSON = []byte(`{"api_key":"synthetic-caller-key"}`)
				req.Metadata = map[string]any{"api_key": "synthetic-caller-key", "access_token": "synthetic-caller-token"}
				req.Attributes = map[string]string{"owner": "caller"}
				req.HTTPClient = host.newHTTPClient(nil, "caller-provider")
			}
			resp, handled, err := host.FetchQuotaByPlugin(context.Background(), "quota-plugin", req)
			if err != nil || !handled || calls != 1 {
				t.Fatalf("fetch: handled=%v calls=%d err=%v", handled, calls, err)
			}
			if !bytes.Equal(captured.StorageJSON, selectedJSON) {
				t.Error("plugin did not receive selected in-memory StorageJSON")
			}
			if captured.AuthID != auth.ID || captured.AuthIndex != auth.Index || captured.Provider != auth.Provider {
				t.Error("plugin received conflicting credential identity")
			}
			if captured.Metadata["access_token"] != "synthetic-selected-token" || captured.Metadata["api_key"] != auth.Metadata["api_key"] || captured.Attributes["owner"] != "selected" {
				t.Error("caller metadata or attributes replaced selected context")
			}
			client, ok := captured.HTTPClient.(*hostHTTPClient)
			if !ok || client.auth == nil || client.auth.ID != auth.ID || client.auth.ProxyURL != auth.ProxyURL || client.provider != auth.Provider {
				t.Error("HTTP client is not bound to selected credential and proxy context")
			}
			responseJSON, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(responseJSON, []byte("synthetic-selected-key")) || bytes.Contains(responseJSON, []byte("synthetic-selected-token")) {
				t.Error("secret canary escaped in quota response")
			}
			if t.Failed() {
				return
			}
			captured.StorageJSON[0] = '!'
			captured.Metadata["access_token"] = "mutated"
			captured.Attributes["owner"] = "mutated"
			stored, _ := host.currentAuthManager().GetByID(auth.ID)
			if stored.Metadata["access_token"] != "synthetic-selected-token" || stored.Attributes["owner"] != "selected" {
				t.Error("plugin mutated manager credential maps")
			}
			if tc.raw && !bytes.Equal(storageJSONFromAuth(stored), selectedJSON) {
				t.Error("plugin mutated manager credential storage")
			}
		})
	}
}

type quotaMarshalFailure struct{}

func (quotaMarshalFailure) MarshalJSON() ([]byte, error) {
	return nil, errors.New("synthetic-secret-marshal-error")
}

func TestQuotaFetchRejectsUnresolvedCredential(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*coreauth.Auth, *pluginapi.QuotaFetchRequest)
		noManager bool
		duplicate bool
	}{
		{name: "missing manager", noManager: true},
		{name: "missing index", mutate: func(_ *coreauth.Auth, req *pluginapi.QuotaFetchRequest) { req.AuthIndex = "missing-index" }},
		{name: "blank index", mutate: func(_ *coreauth.Auth, req *pluginapi.QuotaFetchRequest) { req.AuthIndex = "   " }},
		{name: "wrong ID", mutate: func(_ *coreauth.Auth, req *pluginapi.QuotaFetchRequest) { req.AuthID = "synthetic-secret-wrong-id" }},
		{name: "wrong provider", mutate: func(_ *coreauth.Auth, req *pluginapi.QuotaFetchRequest) {
			req.Provider = "synthetic-secret-wrong-provider"
		}},
		{name: "wrong plugin route", mutate: func(auth *coreauth.Auth, req *pluginapi.QuotaFetchRequest) {
			auth.Provider = "other-provider"
			req.Provider = "quota-plugin"
		}},
		{name: "ambiguous index", duplicate: true},
		{name: "metadata serialization error", mutate: func(auth *coreauth.Auth, _ *pluginapi.QuotaFetchRequest) {
			auth.Storage = nil
			auth.Metadata = map[string]any{"bad": quotaMarshalFailure{}}
		}},
		{name: "unsupported metadata", mutate: func(auth *coreauth.Auth, _ *pluginapi.QuotaFetchRequest) {
			auth.Storage = nil
			auth.Metadata = map[string]any{"bad": make(chan int)}
		}},
		{name: "empty storage cannot fall back", mutate: func(auth *coreauth.Auth, _ *pluginapi.QuotaFetchRequest) { auth.Storage = &memoryAuthStorage{} }},
		{name: "no credential data", mutate: func(auth *coreauth.Auth, _ *pluginapi.QuotaFetchRequest) { auth.Storage = nil; auth.Metadata = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			provider := &testQuotaProvider{identifier: "quota-provider", fetchFn: func(context.Context, pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
				calls++
				return pluginapi.QuotaFetchResponse{}, nil
			}}
			auth := &coreauth.Auth{
				ID: "selected-auth", Index: "selected-index", Provider: "quota-provider",
				Storage:  &memoryAuthStorage{payload: []byte(`{"api_key":"synthetic-selected-key"}`)},
				Metadata: map[string]any{"api_key": "synthetic-secret-metadata"},
			}
			req := pluginapi.QuotaFetchRequest{
				AuthIndex: auth.Index, AuthID: auth.ID, Provider: auth.Provider,
				StorageJSON: []byte(`{"api_key":"synthetic-secret-caller"}`),
				Metadata:    map[string]any{"access_token": "synthetic-secret-caller-token"},
			}
			if tc.mutate != nil {
				tc.mutate(auth, &req)
			}
			auths := []*coreauth.Auth{auth}
			if tc.duplicate {
				duplicate := auth.Clone()
				duplicate.ID = "another-auth"
				duplicate.Provider = "another-provider"
				auths = append(auths, duplicate)
			}
			host := quotaCredentialHost(t, provider, auths...)
			if tc.noManager {
				host.SetAuthManager(nil)
			}
			_, handled, err := host.FetchQuotaByPlugin(context.Background(), "quota-plugin", req)
			if err == nil || !handled {
				t.Errorf("expected handled resolution error, got handled=%v err=%v", handled, err)
			}
			if calls != 0 {
				t.Error("plugin fetch invoked for refused credential resolution")
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-secret") {
				t.Error("secret canary escaped in resolution error")
			}
			if host.isPluginFused("quota-plugin") {
				t.Error("invalid credential request fused healthy plugin")
			}
		})
	}
}

func TestQuotaFetchWithoutIndexPreservesCallerContext(t *testing.T) {
	calls := 0
	provider := &testQuotaProvider{identifier: "quota-provider", fetchFn: func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
		calls++
		if req.AuthIndex != "" || req.AuthID != "caller-auth" || string(req.StorageJSON) != `{"api_key":"synthetic-caller-key"}` || req.Metadata["access_token"] != "synthetic-caller-token" || req.Attributes["owner"] != "caller" {
			t.Error("quota request without index lost caller context")
		}
		if req.HTTPClient == nil {
			t.Error("missing HTTP client")
		}
		return pluginapi.QuotaFetchResponse{Subscription: &pluginapi.QuotaSubscription{Plan: "synthetic-plan"}}, nil
	}}
	host := quotaCredentialHost(t, provider)
	client := host.newHTTPClient(nil, "quota-provider")
	resp, handled, err := host.FetchQuota(context.Background(), pluginapi.QuotaFetchRequest{
		Provider: "quota-provider", AuthID: "caller-auth",
		StorageJSON: []byte(`{"api_key":"synthetic-caller-key"}`),
		Metadata:    map[string]any{"access_token": "synthetic-caller-token"},
		Attributes:  map[string]string{"owner": "caller"}, HTTPClient: client,
	})
	if err != nil || !handled || calls != 1 || resp.Subscription == nil || resp.Subscription.Plan != "synthetic-plan" {
		t.Fatalf("non-indexed fetch: handled=%v calls=%d err=%v", handled, calls, err)
	}
}

func TestQuotaFetchCredentialProviderAndPluginRoutes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		byPlugin bool
	}{
		{name: "provider route", provider: "quota-provider"},
		{name: "normalized provider", provider: " QUOTA-PROVIDER "},
		{name: "plugin route", provider: "quota-plugin"},
		{name: "explicit plugin target", provider: "quota-provider", byPlugin: true},
		{name: "inferred credential provider", byPlugin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			provider := &testQuotaProvider{identifier: "quota-provider", fetchFn: func(_ context.Context, req pluginapi.QuotaFetchRequest) (pluginapi.QuotaFetchResponse, error) {
				calls++
				if req.AuthID != "selected-auth" || req.AuthIndex != "selected-index" || req.Provider != "quota-provider" || string(req.StorageJSON) != `{"api_key":"synthetic-route-key"}` {
					t.Error("plugin did not receive canonical selected credential context")
				}
				return pluginapi.QuotaFetchResponse{}, nil
			}}
			host := quotaCredentialHost(t, provider, &coreauth.Auth{
				ID: "selected-auth", Index: "selected-index", Provider: "quota-provider",
				Storage: &memoryAuthStorage{payload: []byte(`{"api_key":"synthetic-route-key"}`)},
			})
			req := pluginapi.QuotaFetchRequest{AuthIndex: "selected-index", Provider: tc.provider}
			var handled bool
			var err error
			if tc.byPlugin {
				_, handled, err = host.FetchQuotaByPlugin(context.Background(), "quota-plugin", req)
			} else {
				_, handled, err = host.FetchQuota(context.Background(), req)
			}
			if err != nil || !handled || calls != 1 {
				t.Fatalf("fetch: handled=%v calls=%d err=%v", handled, calls, err)
			}
		})
	}
}
