package cliproxy

import (
	"context"
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestCopilotCatalogFailurePreservesRegistration(t *testing.T) {
	for _, tc := range []struct {
		name, prefix string
		aliases      []config.OAuthModelAlias
	}{
		{name: "prefix", prefix: "team"},
		{name: "alias", aliases: []config.OAuthModelAlias{{Name: "model", Alias: "public-model"}, {Name: "public-model", Alias: "second-alias"}}},
		{name: "alias and prefix", prefix: "team", aliases: []config.OAuthModelAlias{{Name: "model", Alias: "public-model"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{cfg: &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{"github-copilot": tc.aliases}}}
			s.cfg.ForceModelPrefix = true
			auth := &coreauth.Auth{ID: t.Name(), Provider: "github-copilot", Prefix: tc.prefix, Metadata: map[string]any{"auth_kind": "oauth"}}
			models := []*ModelInfo{{ID: "model", UpstreamEndpoint: "/responses"}}
			models = applyOAuthModelAliasForAuth(s.cfg, auth.Provider, auth.AuthKind(), nil, models)
			models = applyModelPrefixes(models, auth.Prefix, true)
			r := registry.GetGlobalRegistry()
			r.RegisterClient(auth.ID, auth.Provider, models)
			t.Cleanup(func() { r.UnregisterClient(auth.ID) })
			before, epoch := r.GetModelsAndEpochForClient(auth.ID)
			r.SuspendClientModel(auth.ID, before[0].ID, "test suspension")
			for range 2 {
				// Missing credentials fail catalog acquisition without network access.
				s.registerModelsForAuth(context.Background(), auth)
				after, newEpoch := r.GetModelsAndEpochForClient(auth.ID)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("catalog failure changed registration: before=%+v after=%+v", before[0], after)
				}
				if newEpoch != epoch || !r.IsModelSuspendedForClient(auth.ID, before[0].ID) {
					t.Fatal("catalog failure replaced the existing registration state")
				}
			}
		})
	}
}
