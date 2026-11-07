package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func staticModelWithLimits(t *testing.T, channel string) *ModelInfo {
	t.Helper()
	for _, model := range registry.GetStaticModelDefinitionsByChannel(channel) {
		if model != nil && model.ContextLength > 0 && model.MaxCompletionTokens > 0 {
			return model
		}
	}
	t.Fatalf("no static %s model with limits", channel)
	return nil
}

func TestBuildConfigModelsInheritsStaticLimits(t *testing.T) {
	upstream := staticModelWithLimits(t, "claude")

	for _, alias := range []string{upstream.ID, "team-claude"} {
		got := buildClaudeConfigModels(&config.ClaudeKey{
			Models: []config.ClaudeModel{{Name: upstream.ID, Alias: alias}},
		})
		if len(got) != 1 {
			t.Fatalf("alias %q: got %d models, want 1", alias, len(got))
		}
		if got[0].ContextLength != upstream.ContextLength {
			t.Errorf("alias %q: ContextLength = %d, want %d", alias, got[0].ContextLength, upstream.ContextLength)
		}
		if got[0].MaxCompletionTokens != upstream.MaxCompletionTokens {
			t.Errorf("alias %q: MaxCompletionTokens = %d, want %d", alias, got[0].MaxCompletionTokens, upstream.MaxCompletionTokens)
		}
		if got[0].MaxContextLength != 0 {
			t.Errorf("alias %q: MaxContextLength = %d, want 0 (inherited limits are not overrides)", alias, got[0].MaxContextLength)
		}
	}
}

func TestBuildConfigModelsInheritsStaticLimitsPerChannel(t *testing.T) {
	upstream := staticModelWithLimits(t, "codex")
	got := buildCodexConfigModels(&config.CodexKey{
		Models: []config.CodexModel{{Name: upstream.ID, Alias: "team-codex"}},
	})
	if len(got) != 1 || got[0].ContextLength != upstream.ContextLength || got[0].MaxCompletionTokens != upstream.MaxCompletionTokens {
		t.Fatalf("codex alias limits = %+v, want context %d and output %d", got, upstream.ContextLength, upstream.MaxCompletionTokens)
	}
}

func TestBuildConfigModelsConfiguredMaxContextLengthWins(t *testing.T) {
	upstream := staticModelWithLimits(t, "claude")
	const want = 123456
	got := buildClaudeConfigModels(&config.ClaudeKey{
		Models: []config.ClaudeModel{{Name: upstream.ID, Alias: "team-claude", MaxContextLength: want}},
	})[0]
	if got.ContextLength != want || got.MaxContextLength != want {
		t.Fatalf("ContextLength/MaxContextLength = %d/%d, want %d", got.ContextLength, got.MaxContextLength, want)
	}
	if got.MaxCompletionTokens != upstream.MaxCompletionTokens {
		t.Fatalf("MaxCompletionTokens = %d, want %d", got.MaxCompletionTokens, upstream.MaxCompletionTokens)
	}
}

func TestBuildConfigModelsUnknownUpstreamKeepsZeroLimits(t *testing.T) {
	got := buildClaudeConfigModels(&config.ClaudeKey{
		Models: []config.ClaudeModel{{Name: "not-a-catalog-model", Alias: "custom"}},
	})[0]
	if got.ContextLength != 0 || got.MaxCompletionTokens != 0 {
		t.Fatalf("limits = %d/%d, want 0/0", got.ContextLength, got.MaxCompletionTokens)
	}
}

func TestBuildConfigModelsInheritsGeminiTokenLimits(t *testing.T) {
	for _, channel := range []string{"gemini", "vertex"} {
		var upstream *ModelInfo
		for _, model := range registry.GetStaticModelDefinitionsByChannel(channel) {
			if model != nil && model.ContextLength <= 0 && model.MaxCompletionTokens <= 0 && model.InputTokenLimit > 0 && model.OutputTokenLimit > 0 {
				upstream = model
				break
			}
		}
		if upstream == nil {
			t.Fatalf("no static %s model with only inputTokenLimit/outputTokenLimit", channel)
		}
		var got []*ModelInfo
		if channel == "gemini" {
			got = buildGeminiConfigModels(&config.GeminiKey{Models: []config.GeminiModel{{Name: upstream.ID, Alias: "team-gemini"}}})
		} else {
			got = buildVertexCompatConfigModels(&config.VertexCompatKey{Models: []config.VertexCompatModel{{Name: upstream.ID, Alias: "team-gemini"}}})
		}
		if len(got) != 1 || got[0].ContextLength != upstream.InputTokenLimit || got[0].MaxCompletionTokens != upstream.OutputTokenLimit {
			t.Fatalf("%s alias of %s: limits = %+v, want context %d and output %d", channel, upstream.ID, got, upstream.InputTokenLimit, upstream.OutputTokenLimit)
		}
	}
}
