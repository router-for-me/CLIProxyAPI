package auth

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestResolveClientAPIKeyModelAliasWithResult(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetClientAPIKeyModelAliases(internalconfig.ClientAPIKeys{
		{Key: "key1", ModelAliases: []internalconfig.OAuthModelAlias{
			{Name: "deepseek-v4", Alias: "claude-opus-4.7"},
		}},
		{Key: "key2", ModelAliases: []internalconfig.OAuthModelAlias{
			{Name: "deepseek-v4", Alias: "gpt-5.5"},
		}},
	})

	res1 := mgr.resolveClientAPIKeyModelAliasWithResult("key1", "claude-opus-4.7")
	if res1.UpstreamModel != "deepseek-v4" {
		t.Fatalf("key1 upstream = %q, want deepseek-v4", res1.UpstreamModel)
	}
	res2 := mgr.resolveClientAPIKeyModelAliasWithResult("key2", "gpt-5.5")
	if res2.UpstreamModel != "deepseek-v4" {
		t.Fatalf("key2 upstream = %q, want deepseek-v4", res2.UpstreamModel)
	}
}

func TestResolveClientAPIKeyModelAliasWithResult_ForceMapping(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	mgr.SetClientAPIKeyModelAliases(internalconfig.ClientAPIKeys{
		{Key: "sk-test", ModelAliases: []internalconfig.OAuthModelAlias{
			{Name: "mimo-v2.5", Alias: "gpt5.3", ForceMapping: true},
		}},
	})
	res := mgr.resolveClientAPIKeyModelAliasWithResult("sk-test", "gpt5.3")
	if res.UpstreamModel != "mimo-v2.5" || !res.ForceMapping || res.OriginalAlias != "gpt5.3" {
		t.Fatalf("result = %+v", res)
	}
	resp := cliproxyexecutor.Response{Payload: []byte(`{"model":"mimo-v2.5","choices":[]}`)}
	rewriteForceMappedResponse(&resp, res)
	if string(resp.Payload) != `{"model":"gpt5.3","choices":[]}` {
		t.Fatalf("payload = %s", resp.Payload)
	}
}

func TestResolveExecutionAliasPerKeyBeforeGlobal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mgr := NewManager(nil, nil, nil)
	mgr.SetClientAPIKeyModelAliases(internalconfig.ClientAPIKeys{
		{Key: "sk-test", ModelAliases: []internalconfig.OAuthModelAlias{
			{Name: "mimo-v2.5", Alias: "gpt5.3"},
		}},
	})
	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"openai-compatible-test": {{Name: "mimo-v2.5", Alias: "opus4.5"}},
	})
	c, _ := gin.CreateTestContext(nil)
	c.Set("userApiKey", "sk-test")
	ctx := context.WithValue(context.Background(), "gin", c)
	auth := &Auth{Provider: "openai-compatibility", Attributes: map[string]string{"compat_name": "test"}}
	res := mgr.resolveExecutionAliasResultForRequestedWithClient(ctx, auth, "gpt5.3")
	if res.UpstreamModel != "mimo-v2.5" {
		t.Fatalf("per-key upstream = %q, want mimo-v2.5 (not global opus4.5)", res.UpstreamModel)
	}
}

func TestClientAPIKeyPrincipalFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Set("userApiKey", "client-secret")
	ctx := context.WithValue(context.Background(), "gin", c)
	if got := ClientAPIKeyPrincipalFromContext(ctx); got != "client-secret" {
		t.Fatalf("principal = %q", got)
	}
}