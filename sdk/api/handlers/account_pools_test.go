package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/tidwall/gjson"
)

func TestAccountPoolsModelListAndPolicyRefresh(t *testing.T) {
	h := keyScopeTestHandler(t)
	h.AuthManager = coreauth.NewManager(nil, nil, nil)
	for _, id := range []string{"key-scope-claude-client", "pool-personal"} {
		if _, err := h.AuthManager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: "claude"}); err != nil {
			t.Fatal(err)
		}
	}
	registry.GetGlobalRegistry().RegisterClient("pool-personal", "claude", []*registry.ModelInfo{{ID: "pool-personal-only"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient("pool-personal") })
	chain := []string{"reviewer"}
	h.Cfg.Client.AccountPools = &[]config.ClientAccountPool{{Name: "reviewer", AuthIDs: []string{"key-scope-claude-client"}}}
	h.Cfg.Client.KeyScopes = &[]config.ClientKeyScope{{KeyPrefix: "review-", Providers: []string{"claude"}, Pools: &chain}}
	c, _ := keyScopeRequestContext("review-key")
	body := []byte(`{"data":[{"id":"` + keyScopeClaudeModel + `"},{"id":"pool-personal-only"}]}`)
	filtered := h.scopeModelList(c, body)
	if gjson.GetBytes(filtered, "data.#").Int() != 1 || gjson.GetBytes(filtered, "data.0.id").String() != keyScopeClaudeModel {
		t.Fatalf("model list leaked: %s", filtered)
	}
	ctx := context.WithValue(context.Background(), "gin", c)
	ctx, errMsg := h.accountPoolExecutionContext(ctx)
	if errMsg != nil || !coreauth.HasAccountPools(ctx) {
		t.Fatalf("policy missing: %v", errMsg)
	}
	// A socket keeps its Gin context, but each turn must resolve the current config.
	missing := []string{"removed"}
	(*h.Cfg.Client.KeyScopes)[0].Pools = &missing
	if _, errMsg = h.accountPoolExecutionContext(ctx); errMsg == nil {
		t.Fatal("old policy survived revocation")
	}
	(*h.Cfg.Client.KeyScopes)[0].Pools = nil
	ctx, errMsg = h.accountPoolExecutionContext(ctx)
	if errMsg != nil || coreauth.HasAccountPools(ctx) {
		t.Fatal("old policy survived scope removal")
	}
}

// Executor plugins select credentials outside the scheduler, so a pooled key
// is refused before one runs, even when its scope names the plugin. Unpooled
// keys keep plugin routing.
func TestAccountPoolKeysRefusePluginExecutors(t *testing.T) {
	const targetPluginID = "plugin-executor"
	host := &handlerDirectExecutorRouteHost{}
	host.hasRouters = true
	host.route = func(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
		return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetExecutor, Target: targetPluginID}, true
	}
	h := keyScopeTestHandler(t)
	h.AuthManager = coreauth.NewManager(nil, nil, nil)
	h.SetModelRouterHost(host)
	chain := []string{"reviewer"}
	h.Cfg.Client.AccountPools = &[]config.ClientAccountPool{{Name: "reviewer"}}
	h.Cfg.Client.KeyScopes = &[]config.ClientKeyScope{{KeyPrefix: "review-", Providers: []string{"claude", targetPluginID}, Pools: &chain}}
	pooled := keyScopeExecutionContext("review-key")
	body := []byte(`{"model":"plugin-routed"}`)

	if _, _, errMsg := h.ExecuteWithAuthManager(pooled, "openai", "plugin-routed", body, ""); errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("execute: %#v, want 503", errMsg)
	}
	if _, _, errMsg := h.ExecuteCountWithAuthManager(pooled, "openai", "plugin-routed", body, ""); errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("count: %#v, want 503", errMsg)
	}
	data, _, errs := h.ExecuteStreamWithAuthManager(pooled, "openai", "plugin-routed", body, "")
	if data != nil {
		t.Fatal("stream returned data for a refused request")
	}
	if errMsg := <-errs; errMsg == nil || errMsg.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("stream: %#v, want 503", errMsg)
	}
	if host.lastPluginID != "" {
		t.Fatalf("a pooled key reached plugin executor %q", host.lastPluginID)
	}

	if _, _, errMsg := h.ExecuteWithAuthManager(keyScopeExecutionContext("admin-key"), "openai", "plugin-routed", body, ""); errMsg != nil {
		t.Fatalf("unpooled key: %#v", errMsg)
	}
	if host.lastPluginID != targetPluginID {
		t.Fatalf("unpooled key used plugin executor %q, want %q", host.lastPluginID, targetPluginID)
	}
}
