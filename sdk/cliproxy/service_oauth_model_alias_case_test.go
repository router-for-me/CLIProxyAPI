package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestApplyOAuthModelAliasCaseOnly(t *testing.T) {
	const upstream = "minimax-m3.1-flash"
	const alias = "MiniMax-M3.1-Flash"
	for _, scenario := range []struct {
		name string
		fork bool
		want []string
	}{
		{"replace", false, []string{alias}},
		{"fork", true, []string{upstream, alias}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
				"opencode-go": {{Name: upstream, Alias: alias, Fork: scenario.fork}},
			}}
			cfg.SanitizeOAuthModelAlias()
			models := []*ModelInfo{{ID: upstream, Name: "models/" + upstream, DisplayName: "MiniMax M3.1 Flash"}}
			got := applyOAuthModelAlias(cfg, "opencode-go", "oauth", models)
			if len(got) != len(scenario.want) {
				t.Fatalf("published %d model IDs, want %v", len(got), scenario.want)
			}
			for index, id := range scenario.want {
				if got[index].ID != id || got[index].Name != "models/"+id || got[index].DisplayName != models[0].DisplayName {
					t.Fatalf("published model = %+v, want ID and Gemini name for %q", got[index], id)
				}
				if id == alias && got[index].MetadataModelID != upstream {
					t.Fatalf("alias metadata identity = %q, want %q", got[index].MetadataModelID, upstream)
				}
			}
			if models[0].ID != upstream || models[0].Name != "models/"+upstream {
				t.Fatal("alias changed the upstream model definition")
			}
		})
	}
}

func TestApplyOAuthModelAliasKeepsCaseInsensitiveCollisionProtection(t *testing.T) {
	models := []*ModelInfo{{ID: "model-a"}, {ID: "Model-A"}, {ID: "model-a"}, {ID: "model-b"}}
	got := applyOAuthModelAliasEntries([]config.OAuthModelAlias{{Name: "model-b", Alias: "public-b"}}, models)
	want := []string{"model-a", "public-b"}
	if len(got) != len(want) {
		t.Fatalf("published %d IDs, want %v", len(got), want)
	}
	for index, id := range want {
		if got[index].ID != id {
			t.Fatalf("published ID %q, want %q", got[index].ID, id)
		}
	}
}

func TestApplyOAuthModelAliasRejectsUnrelatedCaseCollision(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		models []*ModelInfo
		want   []string
	}{
		{"alias first", []*ModelInfo{{ID: "model-a"}, {ID: "Public-B"}}, []string{"public-b"}},
		{"native first", []*ModelInfo{{ID: "Public-B"}, {ID: "model-a"}}, []string{"Public-B", "model-a"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			got := applyOAuthModelAliasEntries([]config.OAuthModelAlias{{Name: "model-a", Alias: "public-b"}}, scenario.models)
			if len(got) != len(scenario.want) {
				t.Fatalf("published %d IDs, want %v", len(got), scenario.want)
			}
			for index, id := range scenario.want {
				if got[index].ID != id {
					t.Fatalf("published ID %q, want %q", got[index].ID, id)
				}
			}
		})
	}
}

func TestOAuthModelAliasCaseOnlySharesProviderRoute(t *testing.T) {
	const upstream = "minimax-m3.1-flash"
	const alias = "MiniMax-M3.1-Flash"
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"opencode-go": {{Name: upstream, Alias: alias}},
	}}
	cfg.SanitizeOAuthModelAlias()
	models := applyOAuthModelAlias(cfg, "opencode-go", "oauth", []*ModelInfo{{ID: upstream}})
	reg := registry.GetGlobalRegistry()
	oauthID, nativeID := t.Name()+"-oauth", t.Name()+"-native"
	reg.RegisterClient(oauthID, "opencode-go", models)
	reg.RegisterClient(nativeID, "minimax-plan", []*ModelInfo{{ID: alias}})
	t.Cleanup(func() {
		reg.UnregisterClient(oauthID)
		reg.UnregisterClient(nativeID)
	})
	providers := reg.GetModelProviders(alias)
	seen := map[string]bool{}
	for _, provider := range providers {
		seen[provider] = true
	}
	if !seen["opencode-go"] || !seen["minimax-plan"] {
		t.Fatalf("providers for %q = %v, want both OAuth and native providers", alias, providers)
	}
}
