package auth

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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

func TestClientAPIKeyPrincipalFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	c.Set("userApiKey", "client-secret")
	ctx := context.WithValue(context.Background(), "gin", c)
	if got := ClientAPIKeyPrincipalFromContext(ctx); got != "client-secret" {
		t.Fatalf("principal = %q", got)
	}
}
