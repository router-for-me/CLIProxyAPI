package pluginhost

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestHasAuthModelProviderDoesNotRunDiscovery(t *testing.T) {
	for _, mode := range []string{"auth", "executor", "registered", "static-only", "fused", "unloaded", "no-model-provider"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			caps := pluginapi.Capabilities{
				ModelProvider: modelProviderFunc{
					staticModels: func(context.Context, pluginapi.StaticModelRequest) (pluginapi.ModelResponse, error) {
						t.Error("ownership lookup called StaticModels")
						return pluginapi.ModelResponse{}, nil
					},
					modelsForAuth: func(context.Context, pluginapi.AuthModelRequest) (pluginapi.ModelResponse, error) {
						calls++
						return pluginapi.ModelResponse{Provider: "antigravity", Models: []pluginapi.ModelInfo{{ID: "plugin-model"}}, AuthUpdate: pluginapi.AuthData{Metadata: map[string]any{"access_token": "rotated"}}}, nil
					},
				},
			}
			switch mode {
			case "executor", "static-only":
				caps.Executor = &fakeExecutor{identifier: "antigravity"}
				caps.ExecutorModelScope = pluginapi.ExecutorModelScopeOAuth
				if mode == "static-only" {
					caps.ExecutorModelScope = pluginapi.ExecutorModelScopeStatic
				}
			case "registered":
			default:
				caps.AuthProvider = fakeAuthProvider{identifier: "antigravity"}
			}
			if mode == "no-model-provider" {
				caps.ModelProvider = nil
			}
			host := newHostWithRecords(capabilityRecord{id: "ownership", plugin: pluginapi.Plugin{Capabilities: caps}})
			if mode == "registered" {
				host.modelProviders["ownership"] = "antigravity"
			}
			if mode == "fused" {
				host.fused["ownership"] = "test"
			}
			if mode == "unloaded" {
				setHostSnapshotForTest(host, true)
			}
			want := mode == "auth" || mode == "executor" || mode == "registered"
			for range 100 {
				if got := host.HasAuthModelProvider(" ANTIGRAVITY "); got != want {
					t.Fatalf("ownership=%v, want %v", got, want)
				}
			}
			if calls != 0 || host.HasAuthModelProvider("claude") || host.HasAuthModelProvider("") {
				t.Fatal("ownership lookup executed discovery or matched an unrelated provider")
			}
			// The normal registration path must still consume discovery and auth updates.
			result := host.ModelsForAuth(t.Context(), &coreauth.Auth{ID: "account", Provider: "antigravity"})
			if result.Handled != want {
				t.Fatal("ownership lookup disagrees with per-auth dispatch")
			}
			if want && (calls != 1 || result.Auth == nil || result.Auth.Metadata["access_token"] != "rotated" || len(result.Models) != 1) {
				t.Fatal("normal discovery lost model or credential updates")
			}
		})
	}
	var host *Host
	if host.HasAuthModelProvider("antigravity") {
		t.Fatal("nil host claims model ownership")
	}
}
