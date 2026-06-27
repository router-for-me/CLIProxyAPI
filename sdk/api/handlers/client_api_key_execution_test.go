package handlers

import (
	"context"
	"reflect"
	"testing"

	"github.com/gin-gonic/gin"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestGetRequestDetails_ClientAPIKeyAliasOpenAICompat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mgr := coreauth.NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			ClientAPIKeys: internalconfig.ClientAPIKeys{
				{Key: "sk-test", ModelAliases: []internalconfig.OAuthModelAlias{
					{Name: "mimo-v2.5", Alias: "gpt5.3"},
				}},
			},
		},
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{
				Name: "test",
				Models: []internalconfig.OpenAICompatibilityModel{
					{Name: "mimo-v2.5", Alias: "gpt5.5"},
				},
			},
		},
	})

	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, mgr)

	c, _ := gin.CreateTestContext(nil)
	c.Set("userApiKey", "sk-test")
	ctx := context.WithValue(context.Background(), "gin", c)

	providers, model, errMsg := handler.getRequestDetails(ctx, "gpt5.3")
	if errMsg != nil {
		t.Fatalf("getRequestDetails() error = %+v", errMsg)
	}
	wantProvider := "openai-compatible-test"
	if len(providers) != 1 || providers[0] != wantProvider {
		t.Fatalf("providers = %v, want [%s]", providers, wantProvider)
	}
	if model != "gpt5.3" {
		t.Fatalf("normalized model = %q, want gpt5.3", model)
	}
}

func TestProvidersForModelName_OpenAICompatUpstream(t *testing.T) {
	mgr := coreauth.NewManager(nil, nil, nil)
	mgr.SetConfig(&internalconfig.Config{
		OpenAICompatibility: []internalconfig.OpenAICompatibility{
			{Name: "test", Models: []internalconfig.OpenAICompatibilityModel{
				{Name: "mimo-v2.5", Alias: "gpt5.5"},
			}},
		},
	})
	handler := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, mgr)
	got := handler.providersForModelName(context.Background(), "mimo-v2.5")
	want := []string{"openai-compatible-test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("providers = %v, want %v", got, want)
	}
}