package thinking_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/antigravity"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/interactions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/kimi"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/xai"
)

func invarianceBody(provider, mode string) []byte {
	level := mode
	switch provider {
	case "openai", "kimi":
		switch mode {
		case "budget":
			level = "high"
		case "adaptive":
			level = "auto"
		case "disabled":
			level = "none"
		}
		return []byte(`{"reasoning_effort":"` + level + `"}`)
	case "codex", "xai":
		switch mode {
		case "budget":
			level = "high"
		case "adaptive":
			level = "auto"
		case "disabled":
			level = "none"
		}
		return []byte(`{"reasoning":{"effort":"` + level + `"}}`)
	case "claude":
		switch mode {
		case "budget":
			return []byte(`{"thinking":{"type":"enabled","budget_tokens":4096}}`)
		case "adaptive":
			return []byte(`{"thinking":{"type":"adaptive"}}`)
		case "disabled":
			return []byte(`{"thinking":{"type":"disabled"}}`)
		default:
			return []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"` + level + `"}}`)
		}
	case "gemini", "antigravity":
		switch mode {
		case "budget":
			return []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":4096}}}`)
		case "adaptive":
			return []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":-1}}}`)
		case "disabled":
			return []byte(`{"generationConfig":{"thinkingConfig":{"thinkingBudget":0}}}`)
		default:
			return []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"` + level + `"}}}`)
		}
	case "interactions":
		switch mode {
		case "budget":
			return []byte(`{"generation_config":{"thinking_budget":4096}}`)
		case "adaptive":
			return []byte(`{"generation_config":{"thinking_level":"auto"}}`)
		case "disabled":
			return []byte(`{"generation_config":{"thinking_level":"none"}}`)
		default:
			return []byte(`{"generation_config":{"thinking_level":"` + level + `"}}`)
		}
	default:
		panic("unsupported registered provider " + provider)
	}
}

func applyInvarianceRow(provider, mode string, userDefined, levels, hybrid bool) ([]byte, error) {
	support := &registry.ThinkingSupport{}
	if levels {
		support.Levels = []string{"low", "medium", "high"}
	}
	if hybrid || !levels {
		support.Min, support.Max = 1024, 32768
	}
	info := &registry.ModelInfo{ID: "invariance-model", Type: provider, UserDefined: userDefined, Thinking: support}
	body := invarianceBody(provider, mode)
	return thinking.ApplyThinkingWithModelInfo(body, body, info.ID, provider, provider, provider, info)
}

func TestDeclaredLevelsDoNotAffectNonCompatibilityProviders(t *testing.T) {
	modes := []string{"low", "medium", "high", "xhigh", "max", "budget", "adaptive", "disabled"}
	for _, provider := range thinking.RegisteredProvidersForTest() {
		for _, mode := range modes {
			for _, hybrid := range []bool{false, true} {
				name := fmt.Sprintf("provider=%s/mode=%s/hybrid=%t", provider, mode, hybrid)
				t.Run(name, func(t *testing.T) {
					actual, actualErr := applyInvarianceRow(provider, mode, true, true, hybrid)
					expected, expectedErr := applyInvarianceRow(provider, mode, true, false, hybrid)
					if !bytes.Equal(actual, expected) || fmt.Sprint(actualErr) != fmt.Sprint(expectedErr) {
						t.Fatalf("declared levels changed non-compatibility output\nactual:   %s\nactual error: %v\nexpected: %s\nexpected error: %v", actual, actualErr, expected, expectedErr)
					}
				})
			}
		}
	}
}

func TestCompatibilityResolverIsInvariantForEveryNonCompatibilityProvider(t *testing.T) {
	configs := []thinking.ThinkingConfig{
		{Mode: thinking.ModeLevel, Level: thinking.LevelLow},
		{Mode: thinking.ModeLevel, Level: thinking.LevelMedium},
		{Mode: thinking.ModeLevel, Level: thinking.LevelHigh},
		{Mode: thinking.ModeLevel, Level: thinking.LevelXHigh},
		{Mode: thinking.ModeLevel, Level: thinking.LevelMax},
		{Mode: thinking.ModeBudget, Budget: 4096},
		{Mode: thinking.ModeAuto, Budget: -1},
		{Mode: thinking.ModeNone},
	}
	for _, provider := range thinking.RegisteredProvidersForTest() {
		for _, userDefined := range []bool{false, true} {
			for _, hybrid := range []bool{false, true} {
				for _, config := range configs {
					name := fmt.Sprintf("provider=%s/mode=%s/user=%t/hybrid=%t", provider, config.Mode, userDefined, hybrid)
					t.Run(name, func(t *testing.T) {
						support := &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
						if hybrid {
							support.Min, support.Max = 1024, 32768
						}
						info := &registry.ModelInfo{Type: provider, UserDefined: userDefined, Thinking: support}
						actual, matched := thinking.ResolveOpenAICompatibilityConfigForTest(config, info)
						if matched || actual != config {
							t.Fatalf("compatibility resolver changed non-compatibility config\nactual: %#v\nexpected: %#v\nmatched: %t", actual, config, matched)
						}
					})
				}
			}
		}
	}
}

func TestOpenAICompatibilityDeclaredLevelClampMatrix(t *testing.T) {
	tests := []struct {
		name      string
		request   string
		supported []string
		want      string
	}{
		{name: "below lowest", request: "minimal", supported: []string{"low", "medium", "xhigh"}, want: "low"},
		{name: "exact low", request: "low", supported: []string{"low", "medium", "xhigh"}, want: "low"},
		{name: "exact medium", request: "medium", supported: []string{"low", "medium", "xhigh"}, want: "medium"},
		{name: "tie prefers lower", request: "high", supported: []string{"low", "medium", "xhigh"}, want: "medium"},
		{name: "exact xhigh", request: "xhigh", supported: []string{"low", "medium", "xhigh"}, want: "xhigh"},
		{name: "max counterpart", request: "max", supported: []string{"low", "medium", "xhigh"}, want: "xhigh"},
		{name: "xhigh counterpart", request: "xhigh", supported: []string{"high", "max"}, want: "max"},
		{name: "asymmetric nearest higher", request: "medium", supported: []string{"minimal", "high"}, want: "high"},
		{name: "max only from high", request: "high", supported: []string{"max"}, want: "max"},
		{name: "max only from low", request: "low", supported: []string{"max"}, want: "max"},
		{name: "minimal only from xhigh", request: "xhigh", supported: []string{"minimal"}, want: "minimal"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &registry.ModelInfo{ID: "compat-model", Type: "openai-compatibility", UserDefined: true, Thinking: &registry.ThinkingSupport{Levels: tc.supported}}
			body := []byte(`{"reasoning_effort":"` + tc.request + `"}`)
			out, err := thinking.ApplyThinkingWithModelInfo(body, body, info.ID, "openai", "openai", "compat", info)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
			}
			want := []byte(`{"reasoning_effort":"` + tc.want + `"}`)
			if !bytes.Equal(out, want) {
				t.Fatalf("wire output = %s, want %s", out, want)
			}
		})
	}
}
