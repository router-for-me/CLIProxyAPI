package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestProviderAttachesModelPolicyPerKey(t *testing.T) {
	cfg := &sdkconfig.SDKConfig{
		APIKeys: []string{"open", "limited"},
		APIKeyModelAccess: map[string]sdkconfig.APIKeyModelAccess{
			" limited ": {AllowedModels: []string{"claude-*"}, DeniedModels: []string{"claude-3-*"}},
			"blank":     {},
		},
	}
	p := newProvider("test", normalizeKeys(cfg.APIKeys), modelPolicies(cfg.APIKeyModelAccess))

	authenticate := func(key string) *authResult {
		req := httptest.NewRequest("GET", "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		res, err := p.Authenticate(context.Background(), req)
		if err != nil {
			t.Fatalf("Authenticate(%q): %v", key, err)
		}
		return &authResult{res.ModelPolicy.Allows("claude-opus"), res.ModelPolicy.Allows("claude-3-haiku"), res.ModelPolicy.Allows("gpt-5")}
	}

	if got := authenticate("open"); !got.opus || !got.claude3 || !got.gpt {
		t.Fatalf("unlisted key must be unrestricted, got %+v", got)
	}
	if got := authenticate("limited"); !got.opus || got.claude3 || got.gpt {
		t.Fatalf("limited key policy not applied, got %+v", got)
	}
}

type authResult struct{ opus, claude3, gpt bool }
