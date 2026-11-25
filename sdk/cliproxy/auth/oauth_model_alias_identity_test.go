package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestOAuthModelAlias_IdentityForceMapping(t *testing.T) {
	t.Parallel()

	t.Run("sanitize preserves identity force-mapping and drops non-force identity", func(t *testing.T) {
		t.Parallel()

		cfg := internalconfig.Config{
			OAuthModelAlias: map[string][]internalconfig.OAuthModelAlias{
				"antigravity": {
					{Name: "gemini-2.5-pro", Alias: "gemini-2.5-pro", ForceMapping: true},
					{Name: "gemini-2.5-flash", Alias: "gemini-2.5-flash", ForceMapping: false},
				},
			},
		}
		cfg.SanitizeOAuthModelAlias()

		got := cfg.OAuthModelAlias["antigravity"]
		if len(got) != 1 {
			t.Fatalf("expected 1 sanitized alias, got %d: %+v", len(got), got)
		}
		if got[0].Name != "gemini-2.5-pro" || got[0].Alias != "gemini-2.5-pro" || !got[0].ForceMapping {
			t.Fatalf("unexpected sanitized entry: %+v", got[0])
		}
	})

	t.Run("compiled table resolves identity force-mapping and skips non-force identity", func(t *testing.T) {
		t.Parallel()

		aliases := map[string][]internalconfig.OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-2.5-pro", Alias: "gemini-2.5-pro", ForceMapping: true},
				{Name: "gemini-2.5-flash", Alias: "gemini-2.5-flash", ForceMapping: false},
			},
		}
		table := compileOAuthModelAliasTable(aliases)
		if table == nil {
			t.Fatalf("expected non-nil compiled table")
		}

		m := NewManager(nil, nil, nil)
		m.oauthModelAlias.Store(table)
		a := &Auth{Provider: "antigravity"}

		res := resolveUpstreamModelFromAliasTable(m, a, "gemini-2.5-pro", "antigravity")
		if res.UpstreamModel != "gemini-2.5-pro" || !res.ForceMapping || res.OriginalAlias != "gemini-2.5-pro" {
			t.Fatalf("expected identity force-mapping result, got %+v", res)
		}

		// Case-insensitive match on identity force-mapping
		resCase := resolveUpstreamModelFromAliasTable(m, a, "GEMINI-2.5-PRO", "antigravity")
		if resCase.UpstreamModel != "gemini-2.5-pro" || !resCase.ForceMapping || resCase.OriginalAlias != "gemini-2.5-pro" {
			t.Fatalf("expected case-insensitive identity force-mapping result, got %+v", resCase)
		}

		// Non-force identity must remain skipped
		resSkipped := resolveUpstreamModelFromAliasTable(m, a, "gemini-2.5-flash", "antigravity")
		if resSkipped.UpstreamModel != "" || resSkipped.ForceMapping {
			t.Fatalf("expected non-force identity to be skipped, got %+v", resSkipped)
		}
	})

	t.Run("direct slice resolution handles case-insensitive identity entries", func(t *testing.T) {
		t.Parallel()

		aliases := []internalconfig.OAuthModelAlias{
			{Name: "gemini-2.5-pro", Alias: "gemini-2.5-pro", ForceMapping: true},
			{Name: "gemini-2.5-flash", Alias: "gemini-2.5-flash", ForceMapping: false},
		}

		res, matched := resolveUpstreamModelFromAliases(aliases, "GEMINI-2.5-PRO")
		if !matched || res.UpstreamModel != "gemini-2.5-pro" || !res.ForceMapping || res.OriginalAlias != "gemini-2.5-pro" {
			t.Fatalf("expected case-insensitive identity force-mapping match, got res=%+v matched=%v", res, matched)
		}

		resNoForce, matchedNoForce := resolveUpstreamModelFromAliases(aliases, "gemini-2.5-flash")
		if !matchedNoForce || resNoForce.UpstreamModel != "" || resNoForce.ForceMapping {
			t.Fatalf("expected non-force identity to return empty result, got res=%+v matched=%v", resNoForce, matchedNoForce)
		}
	})
}
