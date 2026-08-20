package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestPoolsidePrefixedModelStripsAuthPrefix proves the execution model sent to
// the Poolside upstream strips the auth prefix. The proxy advertises
// "<prefix>/poolside/<model>" in /v1/models (e.g. "sp/poolside/laguna-s-2.1"),
// but the Poolside upstream only accepts "poolside/<model>". Runtime evidence
// (released -dc9/-dc10 binary + live upstream): requesting the prefixed model
// produced 404 model_not_found from the upstream, while the same unprefixed
// model name returns 200 directly.
func TestPoolsidePrefixedModelStripsAuthPrefix(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{
		PoolsideKey: []internalconfig.PoolsideKey{
			{APIKey: "poolside-key", Prefix: "sp"},
		},
	})
	auth := &Auth{
		ID:       "auth-poolside-sp",
		Provider: "poolside",
		Prefix:   "sp",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindAPIKey,
			AttributeAPIKey:   "poolside-key",
			AttributeSource:   "config:poolside[0]",
		},
	}
	registerCapabilityTestAuth(t, manager, auth)

	got := manager.ResolveExecutionModel(auth, "sp/poolside/laguna-s-2.1")
	if got != "poolside/laguna-s-2.1" {
		t.Fatalf("ResolveExecutionModel(sp/poolside/laguna-s-2.1) = %q, want %q (prefix must be stripped before upstream)", got, "poolside/laguna-s-2.1")
	}
}
