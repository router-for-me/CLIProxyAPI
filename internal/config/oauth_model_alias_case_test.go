package config

import "testing"

func TestSanitizeOAuthModelAliasCaseOnly(t *testing.T) {
	for _, model := range []struct{ name, alias string }{
		{"minimax-m3.1-flash", "MiniMax-M3.1-Flash"},
		{"glm-5.3", "GLM-5.3"},
	} {
		t.Run(model.alias, func(t *testing.T) {
			cfg := Config{OAuthModelAlias: map[string][]OAuthModelAlias{
				"opencode-go": {
					{Name: " " + model.name + " ", Alias: " " + model.alias + " ", Fork: true},
					{Name: "unrelated", Alias: model.name},
					{Name: "same", Alias: "same"},
				},
			}}
			cfg.SanitizeOAuthModelAlias()
			aliases := cfg.OAuthModelAlias["opencode-go"]
			if len(aliases) != 1 || aliases[0].Name != model.name || aliases[0].Alias != model.alias || !aliases[0].Fork {
				t.Fatalf("case-only mapping and duplicate filtering = %#v", aliases)
			}
		})
	}
}
