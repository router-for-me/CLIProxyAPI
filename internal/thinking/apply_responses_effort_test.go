package thinking_test

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/codex"
)

func TestApplyThinkingNativeResponsesValidatesEffort(t *testing.T) {
	for _, model := range []string{"gpt-6-sol", "gpt-6.1-sol"} {
		for _, format := range []string{"openai-response", "codex"} {
			for _, tc := range []struct {
				name   string
				suffix string
				body   string
				want   string
			}{
				{
					name: "unsupported baseline uses minimum effort",
					body: `{"reasoning":{"effort":"none","summary":"auto","other":7},"input":[{"role":"user","content":"hi"}]}`,
					want: `{"reasoning":{"effort":"low","summary":"auto","other":7},"input":[{"role":"user","content":"hi"}]}`,
				},
				{
					name: "every update is validated without flattening the baseline",
					body: `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"none","other":7}},{"role":"user","content":"first"},{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"assistant","content":"reply"},{"type":"configuration_update","reasoning":{"effort":"none"}}]}`,
					want: `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low","other":7}},{"role":"user","content":"first"},{"type":"configuration_update","reasoning":{"effort":"high"}},{"role":"assistant","content":"reply"},{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
				},
				{
					name: "update does not invent a baseline",
					body: `{"input":[{"type":"configuration_update","reasoning":{"effort":"none"}},{"role":"user","content":"hi"}]}`,
					want: `{"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"hi"}]}`,
				},
				{
					name:   "suffix overrides baseline but updates are still validated",
					suffix: "(high)",
					body:   `{"reasoning":{"effort":"invalid","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}}]}`,
					want:   `{"reasoning":{"effort":"high","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
				},
				{
					name:   "invalid suffix does not bypass effort validation",
					suffix: "(invalid)",
					body:   `{"reasoning":{"effort":"none"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}}]}`,
					want:   `{"reasoning":{"effort":"low"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}}]}`,
				},
				{
					name: "auto uses the existing model fallback",
					body: `{"reasoning":{"effort":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"auto"}}]}`,
					want: `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"medium"}}]}`,
				},
				{
					name: "supported efforts remain byte identical",
					body: `{ "reasoning": {"effort":"max", "summary":"auto"}, "input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"type":"configuration_update","reasoning":{"effort":"medium"}},{"type":"configuration_update","reasoning":{"effort":"high"}},{"type":"configuration_update","reasoning":{"effort":"xhigh"}},{"type":"configuration_update","reasoning":{"effort":"max"}}] }`,
				},
				{
					name: "missing effort remains absent",
					body: `{"reasoning":{"summary":"auto"},"input":[{"type":"configuration_update","tools":[]},{"role":"user","content":"hi"}]}`,
				},
			} {
				t.Run(model+"/"+format+"/"+tc.name, func(t *testing.T) {
					var info *registry.ModelInfo
					for _, candidate := range registry.GetCodexPlusModels() {
						if candidate.ID == model {
							info = candidate
							break
						}
					}
					if info == nil || !info.SupportConfigurationUpdate {
						t.Fatalf("missing native Responses model definition for %s", model)
					}
					got, errApply := thinking.ApplyThinkingWithModelInfo([]byte(tc.body), nil, model+tc.suffix, format, "codex", "codex", info)
					if errApply != nil {
						t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
					}
					want := tc.want
					if want == "" {
						want = tc.body
					}
					if !bytes.Equal(got, []byte(want)) {
						t.Fatalf("body = %s, want %s", got, want)
					}
				})
			}
		}
	}
}

func TestApplyThinkingNativeResponsesRespectsModelCapabilities(t *testing.T) {
	const body = `{"reasoning":{"effort":"none","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"none"}}]}`
	for _, tc := range []struct {
		name        string
		thinking    *registry.ThinkingSupport
		userDefined bool
		want        string
	}{
		{
			name:     "none is a supported level",
			thinking: &registry.ThinkingSupport{Levels: []string{"none", "low", "high"}},
			want:     body,
		},
		{
			name:     "model permits zero effort",
			thinking: &registry.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "high"}},
			want:     body,
		},
		{
			name: "missing thinking metadata remains opaque",
			want: body,
		},
		{
			name:     "missing levels remain opaque",
			thinking: &registry.ThinkingSupport{},
			want:     body,
		},
		{
			name:        "user defined capabilities remain opaque",
			thinking:    &registry.ThinkingSupport{Levels: []string{"low", "high"}},
			userDefined: true,
			want:        body,
		},
		{
			name:     "configured model uses its lowest level rather than a hardcoded low",
			thinking: &registry.ThinkingSupport{Levels: []string{"high", "medium"}},
			want:     `{"reasoning":{"effort":"medium","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"medium"}}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &registry.ModelInfo{ID: "opaque-route", Type: "codex", SupportConfigurationUpdate: true, Thinking: tc.thinking, UserDefined: tc.userDefined}
			got, errApply := thinking.ApplyThinkingWithModelInfo([]byte(body), nil, info.ID, "openai-response", "codex", "codex", info)
			if errApply != nil {
				t.Fatalf("ApplyThinkingWithModelInfo() error = %v", errApply)
			}
			if string(got) != tc.want {
				t.Fatalf("body = %s, want %s", got, tc.want)
			}
		})
	}
}
