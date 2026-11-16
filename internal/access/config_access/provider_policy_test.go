package configaccess

import (
	"context"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
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
	p := newProviderForTest(t, cfg)

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

func newProviderForTest(t *testing.T, cfg *sdkconfig.SDKConfig) *provider {
	t.Helper()
	policies, errPolicies := modelPolicies(cfg.APIKeyModelAccess)
	if errPolicies != nil {
		t.Fatalf("unexpected policy error: %v", errPolicies)
	}
	return newProvider("test", normalizeKeys(cfg.APIKeys), policies)
}

func TestModelPoliciesRejectsNormalizedKeyCollision(t *testing.T) {
	t.Cleanup(func() { sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey) })
	cfg := &sdkconfig.SDKConfig{
		APIKeys: []string{"client"},
		APIKeyModelAccess: map[string]sdkconfig.APIKeyModelAccess{
			"client":   {AllowedModels: []string{"claude-*"}},
			" client ": {DeniedModels: []string{"claude-*"}},
		},
	}
	if _, errPolicies := modelPolicies(cfg.APIKeyModelAccess); errPolicies == nil {
		t.Fatal("expected a collision error for keys that normalize to the same API key")
	}

	Register(cfg)
	for _, existing := range sdkaccess.RegisteredProviders() {
		if existing.Identifier() == sdkaccess.DefaultAccessProviderName {
			t.Fatalf("config API key provider must stay disabled on a colliding policy config, got %q", existing.Identifier())
		}
	}
}

type authResult struct{ opus, claude3, gpt bool }
