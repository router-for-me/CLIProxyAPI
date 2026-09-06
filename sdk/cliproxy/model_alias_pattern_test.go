package cliproxy

import (
	"slices"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestApplyOAuthModelAliasEntries_WildcardAliasIsNotPublished(t *testing.T) {
	t.Parallel()

	aliases := []config.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
	}
	models := []*ModelInfo{{ID: "gpt-5.6-luna"}}

	out := applyOAuthModelAliasEntries(aliases, models)
	if len(out) != 1 {
		t.Fatalf("model count = %d, want 1", len(out))
	}
	if out[0].ID != "gpt-5.6-luna" {
		t.Errorf("model id = %q, want the upstream model to keep its catalog entry", out[0].ID)
	}
}

func TestApplyOAuthModelAliasEntries_ExactAliasStillPublished(t *testing.T) {
	t.Parallel()

	aliases := []config.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-20251001"},
	}
	models := []*ModelInfo{{ID: "gpt-5.6-luna"}}

	out := applyOAuthModelAliasEntries(aliases, models)
	if len(out) != 1 {
		t.Fatalf("model count = %d, want 1", len(out))
	}
	if out[0].ID != "claude-haiku-4-5-20251001" {
		t.Errorf("model id = %q, want the exact alias", out[0].ID)
	}
}

func TestCollectModelAliasPatterns(t *testing.T) {
	t.Parallel()

	patterns := collectModelAliasPatterns(map[string][]config.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
			{Name: "gpt-5.6-sol", Alias: "claude-opus-5"},
		},
		"antigravity": {
			{Name: "gemini-pro-agent", Alias: "gemini-*-preview"},
			{Name: "", Alias: "broken-*"},
		},
	})

	if len(patterns) != 2 {
		t.Fatalf("pattern count = %d, want 2", len(patterns))
	}
	// Channels are walked in sorted order, so antigravity comes first.
	if patterns[0].Pattern != "gemini-*-preview" || patterns[0].Target != "gemini-pro-agent" {
		t.Errorf("first pattern = %+v, want the antigravity entry", patterns[0])
	}
	if patterns[1].Pattern != "claude-haiku-4-5-*" || patterns[1].Target != "gpt-5.6-luna" {
		t.Errorf("second pattern = %+v, want the codex entry", patterns[1])
	}
}

func TestCollectModelAliasPatterns_NoWildcards(t *testing.T) {
	t.Parallel()

	patterns := collectModelAliasPatterns(map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-sol", Alias: "claude-opus-5"}},
	})
	if patterns != nil {
		t.Errorf("patterns = %+v, want nil when no wildcard alias is configured", patterns)
	}
}

func TestApplyOAuthModelAliasEntries_WildcardTargetSurvivesExactReplacement(t *testing.T) {
	t.Parallel()

	// The exact alias sets no fork, so on its own it would replace "gpt-5.6-luna" in
	// the catalog. The wildcard resolves to that upstream name, so it has to stay
	// published or nothing can serve the pattern.
	aliases := []config.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
		{Name: "gpt-5.6-luna", Alias: "my-friendly-name"},
	}

	published := applyOAuthModelAliasEntries(aliases, []*ModelInfo{{ID: "gpt-5.6-luna"}})
	ids := make([]string, 0, len(published))
	for _, model := range published {
		ids = append(ids, model.ID)
	}

	if !slices.Contains(ids, "gpt-5.6-luna") {
		t.Errorf("published ids = %v, want the wildcard target to remain", ids)
	}
	if !slices.Contains(ids, "my-friendly-name") {
		t.Errorf("published ids = %v, want the exact alias as well", ids)
	}
}

func TestApplyOAuthModelAliasEntries_ExactAliasStillReplacesWithoutWildcard(t *testing.T) {
	t.Parallel()

	// Without a wildcard on the same model the non-fork replacement is unchanged.
	aliases := []config.OAuthModelAlias{
		{Name: "gpt-5.6-luna", Alias: "my-friendly-name"},
	}

	published := applyOAuthModelAliasEntries(aliases, []*ModelInfo{{ID: "gpt-5.6-luna"}})
	if len(published) != 1 || published[0].ID != "my-friendly-name" {
		t.Fatalf("published = %v, want only the exact alias", published)
	}
}

func TestCollectModelAliasPatterns_TargetIsTheUpstreamModel(t *testing.T) {
	t.Parallel()

	patterns := collectModelAliasPatterns(map[string][]config.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
			{Name: "gpt-5.6-luna", Alias: "my-friendly-name"},
		},
	})

	if len(patterns) != 1 {
		t.Fatalf("pattern count = %d, want 1", len(patterns))
	}
	if patterns[0].Target != "gpt-5.6-luna" {
		t.Errorf("target = %q, want the upstream model", patterns[0].Target)
	}
}

func TestCollectModelAliasPatterns_ForkKeepsTheUpstreamTarget(t *testing.T) {
	t.Parallel()

	patterns := collectModelAliasPatterns(map[string][]config.OAuthModelAlias{
		"codex": {
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
			{Name: "gpt-5.6-luna", Alias: "my-friendly-name", Fork: true},
		},
	})

	if len(patterns) != 1 {
		t.Fatalf("pattern count = %d, want 1", len(patterns))
	}
	if patterns[0].Target != "gpt-5.6-luna" {
		t.Errorf("target = %q, want the upstream model kept by fork", patterns[0].Target)
	}
}

func TestCollectModelAliasPatterns_TargetIsAlwaysAPublishedCatalogID(t *testing.T) {
	t.Parallel()

	// The pattern target is also the value OAuth alias resolution returns for a
	// wildcard, so this one invariant covers both gates: the handler provider lookup
	// and the per-credential support check. If the catalog ever stops publishing the
	// target, a matching request dies as model_not_found or auth_not_found.
	cases := [][]config.OAuthModelAlias{
		{
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
		},
		{
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
			{Name: "gpt-5.6-luna", Alias: "my-friendly-name"},
		},
		{
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
			{Name: "gpt-5.6-luna", Alias: "my-friendly-name", Fork: true},
		},
		{
			{Name: "gpt-5.6-luna", Alias: "claude-haiku-4-5-*"},
			{Name: "gpt-5.6-luna", Alias: "one"},
			{Name: "gpt-5.6-luna", Alias: "two"},
		},
	}

	for i, aliases := range cases {
		published := applyOAuthModelAliasEntries(aliases, []*ModelInfo{{ID: "gpt-5.6-luna"}})
		ids := make([]string, 0, len(published))
		for _, model := range published {
			ids = append(ids, model.ID)
		}
		patterns := collectModelAliasPatterns(map[string][]config.OAuthModelAlias{"codex": aliases})
		if len(patterns) != 1 {
			t.Fatalf("case %d: pattern count = %d, want 1", i, len(patterns))
		}
		if !slices.Contains(ids, patterns[0].Target) {
			t.Errorf("case %d: pattern target %q is not published; catalog has %v", i, patterns[0].Target, ids)
		}
	}
}
