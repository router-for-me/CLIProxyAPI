package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	"github.com/tidwall/gjson"
)

func TestApplyThinkingWithModelInfoMapsCrossFamilyHighIntent(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		supported []string
		want      string
	}{
		{name: "xhigh stays xhigh", source: "xhigh", supported: []string{"high", "max", "xhigh"}, want: "xhigh"},
		{name: "xhigh prefers max", source: "xhigh", supported: []string{"high", "max"}, want: "max"},
		{name: "xhigh falls back to high", source: "xhigh", supported: []string{"high"}, want: "high"},
		{name: "max stays max", source: "max", supported: []string{"high", "xhigh", "max"}, want: "max"},
		{name: "max prefers xhigh", source: "max", supported: []string{"high", "xhigh"}, want: "xhigh"},
		{name: "max falls back to high", source: "max", supported: []string{"high"}, want: "high"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelInfo := &registry.ModelInfo{
				ID:       "claude-upstream",
				Type:     "claude",
				Thinking: &registry.ThinkingSupport{Levels: tc.supported},
			}
			body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`)
			source := []byte(`{"reasoning_effort":"` + tc.source + `"}`)
			out, err := thinking.ApplyThinkingWithModelInfo(body, source, "claude-upstream", "openai", "claude", "claude", modelInfo)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
			}
			if got := gjson.GetBytes(out, "output_config.effort").String(); got != tc.want {
				t.Fatalf("output effort = %q, want %q; body=%s", got, tc.want, out)
			}
		})
	}
}

func TestApplyThinkingWithModelInfoMapsOpenAICompatibilityHighIntent(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "compat-upstream",
		Type:     "openai-compatibility",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"reasoning_effort":"high"}`)
	source := []byte(`{"reasoning_effort":"xhigh"}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, "compat-upstream", "openai", "openai", "compat-provider", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "max" {
		t.Fatalf("reasoning_effort = %q, want max; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoMapsResponsesToCodexHighIntent(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "codex-upstream",
		Type:     "codex",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "xhigh"}},
	}
	body := []byte(`{"reasoning":{"effort":"high"}}`)
	source := []byte(`{"reasoning":{"effort":"max"}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, "codex-upstream", "openai-response", "codex", "codex", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "xhigh" {
		t.Fatalf("reasoning.effort = %q, want xhigh; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoKeepsSameFamilyValidationStrict(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "openai-upstream",
		Type:     "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}},
	}
	body := []byte(`{"reasoning_effort":"xhigh"}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, body, "openai-upstream", "openai", "openai", "openai", modelInfo)
	if err == nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = nil, want unsupported xhigh error; body=%s", out)
	}
}

func TestApplyThinkingWithModelInfoAppliesEnabledSummaryOnlyClaudeVisibility(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "private-claude",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
	}
	out, err := thinking.ApplyThinkingWithModelInfo(
		[]byte(`{"model":"private-claude","max_tokens":32000}`),
		[]byte(`{"reasoning":{"summary":"auto"}}`),
		"private-claude", "openai-response", "claude", "claude", modelInfo,
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
		t.Fatalf("thinking.type = %q, want adaptive; body=%s", got, out)
	}
	if got := gjson.GetBytes(out, "thinking.display").String(); got != "summarized" {
		t.Fatalf("thinking.display = %q, want summarized; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoAndSummaryDropsInferredClaudeModeWhenSummaryRemoved(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "private-manual-claude",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Min: 1024, Max: 16000},
	}
	out, err := thinking.ApplyThinkingWithModelInfoAndSummary(
		[]byte(`{"model":"private-manual-claude","max_tokens":32000,"thinking":{"type":"adaptive"}}`),
		[]byte(`{"reasoning":{"summary":"auto"}}`),
		"private-manual-claude", "openai-response", "claude", "claude", modelInfo,
		thinking.SummaryConfig{},
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfoAndSummary() error = %v", err)
	}
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("removed summary retained globally inferred adaptive thinking: %s", out)
	}
}

func TestApplyThinkingWithModelInfoDoesNotActivateClaudeForDisabledSummary(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "private-claude",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high"}},
	}
	out, err := thinking.ApplyThinkingWithModelInfo(
		[]byte(`{"model":"private-claude","max_tokens":32000}`),
		[]byte(`{"reasoning":{"summary":null}}`),
		"private-claude", "openai-response", "claude", "claude", modelInfo,
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if gjson.GetBytes(out, "thinking").Exists() {
		t.Fatalf("disabled summary activated Claude thinking: %s", out)
	}
}

func TestApplyThinkingWithModelInfoSummaryOnlyDoesNotInventOpenAIEffort(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "private-openai",
		Type:     "openai",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	out, err := thinking.ApplyThinkingWithModelInfo(
		[]byte(`{"model":"private-openai","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"private-openai","reasoning":{"summary":"auto"},"input":"hi"}`),
		"private-openai", "openai-response", "openai", "openai", modelInfo,
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v; body=%s", err, out)
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("summary-only request invented reasoning_effort: %s", out)
	}
}

func TestApplyThinkingWithSummaryKeepsOpenAIChatSuffixNone(t *testing.T) {
	out, err := thinking.ApplyThinkingWithSummary(
		[]byte(`{"model":"private-openai","messages":[{"role":"user","content":"hi"}]}`),
		"private-openai(none)", "openai-response", "openai", "openai",
		thinking.SummaryConfig{Mode: thinking.SummaryEnabled, Detail: "auto"},
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithSummary() error = %v; body=%s", err, out)
	}
	if got := gjson.GetBytes(out, "reasoning_effort").String(); got != "none" {
		t.Fatalf("reasoning_effort = %q, want none; body=%s", got, out)
	}
}

func TestApplyThinkingWithModelInfoUsesOpenRouterVisibility(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "openrouter-model",
		Type:     "openai-compatibility",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	out, err := thinking.ApplyThinkingWithModelInfo(
		[]byte(`{"model":"openrouter-model","messages":[{"role":"user","content":"hi"}]}`),
		[]byte(`{"model":"openrouter-model","reasoning":{"summary":"auto"},"input":"hi"}`),
		"openrouter-model", "openai-response", "openai", "openrouter", modelInfo,
	)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v; body=%s", err, out)
	}
	if exclude := gjson.GetBytes(out, "reasoning.exclude"); !exclude.Exists() || exclude.Bool() {
		t.Fatalf("OpenRouter summary visibility not enabled: %s", out)
	}
	if gjson.GetBytes(out, "reasoning_effort").Exists() {
		t.Fatalf("OpenRouter summary visibility invented reasoning_effort: %s", out)
	}
}

func TestApplyConfigurationUpdateCrossProtocol(t *testing.T) {
	const source = `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"},{"type":"configuration_update","reasoning":{"effort":"high"}}]}`
	tests := []struct {
		name      string
		body      string
		model     string
		format    string
		typeName  string
		supported bool
		wantPath  string
		want      string
	}{
		{
			name: "unsupported Responses source becomes OpenAI Chat effort", body: `{"messages":[{"role":"user","content":"ok"}],"reasoning_effort":"medium","other":true}`,
			model: "private-chat", format: "openai", typeName: "openai", wantPath: "reasoning_effort", want: "high",
		},
		{
			name: "supported updates cannot be sent to OpenAI Chat", body: `{"messages":[{"role":"user","content":"ok"}],"reasoning_effort":"medium","other":true}`,
			model: "private-chat", format: "openai", typeName: "openai", supported: true, wantPath: "reasoning_effort", want: "high",
		},
		{
			name: "Responses source becomes Claude effort", body: `{"max_tokens":4096,"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},"other":true}`,
			model: "private-claude", format: "claude", typeName: "claude", wantPath: "output_config.effort", want: "high",
		},
		{
			name: "model suffix overrides source updates", body: `{"messages":[{"role":"user","content":"ok"}],"reasoning_effort":"medium","other":true}`,
			model: "private-chat(low)", format: "openai", typeName: "openai", wantPath: "reasoning_effort", want: "low",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info := &registry.ModelInfo{
				ID: "private", Type: tc.typeName, SupportConfigurationUpdate: tc.supported,
				Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}},
			}
			out, err := thinking.ApplyThinkingWithModelInfo([]byte(tc.body), []byte(source), tc.model, "openai-response", tc.format, tc.format, info)
			if err != nil {
				t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
			}
			if got := gjson.GetBytes(out, tc.wantPath).String(); got != tc.want {
				t.Errorf("%s = %q, want %q; body=%s", tc.wantPath, got, tc.want, out)
			}
			if !gjson.GetBytes(out, "other").Bool() {
				t.Errorf("non-thinking field lost: %s", out)
			}
		})
	}
}

func TestApplyConfigurationUpdateSourceEntry(t *testing.T) {
	const source = `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`
	const target = `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"medium"}},{"role":"user","content":"ok"}]}`
	tests := []struct {
		name      string
		model     string
		body      string
		source    string
		format    string
		want      string
		wantInput string
		wantSame  bool
	}{
		{
			name: "registry capability preserves native Responses without suffix", model: "gpt-6-astra", body: source, source: source,
			want: "xhigh", wantInput: `[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]`, wantSame: true,
		},
		{
			name: "unknown gpt-6 model defaults to unsupported", model: "gpt-6-unknown-routed", body: source, source: source,
			want: "low", wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name: "source effort controls translated target", model: "gpt-6-unknown-routed", body: target, source: source,
			want: "low", wantInput: `[{"role":"user","content":"ok"}]`,
		},
		{
			name: "unknown model source update applies to OpenAI Chat", model: "gpt-6-unknown-routed", body: `{"reasoning_effort":"xhigh","messages":[{"role":"user","content":"ok"}]}`, source: source, format: "openai",
			want: "low",
		},
		{
			name: "nonarray input remains unchanged", model: "gpt-6-unknown-routed", body: `{"reasoning":{"summary":"auto"},"input":{"type":"configuration_update"}}`, source: source,
			want: "low", wantInput: `{"type":"configuration_update"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			format := tc.format
			if format == "" {
				format = "codex"
			}
			out, err := thinking.ApplyThinkingWithSourceAndSummary([]byte(tc.body), []byte(tc.source), tc.model, "openai-response", format, format, thinking.ExtractSummaryConfig([]byte(tc.source), "openai-response"))
			if err != nil {
				t.Fatalf("ApplyThinkingWithSourceAndSummary() error = %v", err)
			}
			path := "reasoning.effort"
			if format == "openai" {
				path = "reasoning_effort"
			}
			if got := gjson.GetBytes(out, path).String(); got != tc.want {
				t.Errorf("%s = %q, want %q; body=%s", path, got, tc.want, out)
			}
			if tc.wantInput != "" && gjson.GetBytes(out, "input").Raw != tc.wantInput {
				t.Errorf("input = %s, want %s; body=%s", gjson.GetBytes(out, "input").Raw, tc.wantInput, out)
			}
			if tc.wantSame && string(out) != tc.body {
				t.Errorf("native body changed: got %s, want %s", out, tc.body)
			}
		})
	}
}

func TestApplyConfigurationUpdateInvalidTarget(t *testing.T) {
	const invalidTarget = `{"reasoning":{"effort":"xhigh"},"input":[`
	const updateSource = `{"reasoning":{"effort":"medium"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`
	tests := []struct {
		name       string
		model      string
		source     string
		bound      bool
		normalized bool
		want       string
	}{
		{
			name: "bound model does not rebuild invalid target from source update", model: "private-codex", source: updateSource, bound: true,
			want: invalidTarget,
		},
		{
			name: "unbound model does not rebuild invalid target from source update", model: "gpt-6-unknown-routed", source: updateSource,
			want: invalidTarget,
		},
		{
			name: "normalized updates cannot rebuild invalid target", model: "private-codex", source: updateSource, bound: true, normalized: true,
			want: invalidTarget,
		},
		{
			name: "suffix alone still rebuilds invalid target", model: "private-codex(high)", bound: true,
			want: `{"reasoning":{"effort":"high"}}`,
		},
		{
			name: "suffix takes priority over source update for invalid target", model: "private-codex(high)", source: updateSource, bound: true, normalized: true,
			want: `{"reasoning":{"effort":"high"}}`,
		},
	}
	info := &registry.ModelInfo{ID: "private-codex", Type: "codex", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "medium", "high", "xhigh"}}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(invalidTarget)
			var out []byte
			var err error
			if tc.bound {
				out, err = thinking.ApplyThinkingWithModelInfoAndSummary(body, []byte(tc.source), tc.model, "codex", "codex", "codex", info, thinking.SummaryConfig{}, tc.normalized)
			} else {
				out, err = thinking.ApplyThinkingWithSourceAndSummary(body, []byte(tc.source), tc.model, "codex", "codex", "codex", thinking.SummaryConfig{}, tc.normalized)
			}
			if err != nil {
				t.Fatalf("ApplyThinking() error = %v", err)
			}
			if string(out) != tc.want {
				t.Fatalf("body = %s, want %s", out, tc.want)
			}
		})
	}
}

func TestApplyConfigurationUpdateBoundModelWithoutThinking(t *testing.T) {
	const body = `{"reasoning":{"effort":"xhigh","summary":"auto"},"input":[{"type":"configuration_update","reasoning":{"effort":"low"}},{"role":"user","content":"ok"}]}`
	out, err := thinking.ApplyThinkingWithModelInfoAndSummary([]byte(body), []byte(body), "gpt-6-astra", "codex", "codex", "codex", nil, thinking.ExtractSummaryConfig([]byte(body), "codex"))
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfoAndSummary() error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "low" {
		t.Errorf("unresolved binding used static support: effort=%q; body=%s", got, out)
	}
	if len(gjson.GetBytes(out, "input").Array()) != 1 {
		t.Errorf("unresolved binding retained an update: %s", out)
	}

	info := &registry.ModelInfo{ID: "custom", UserDefined: true}
	out, err = thinking.ApplyThinkingWithModelInfoAndSummary([]byte(body), []byte(body), "custom", "codex", "codex", "codex", info, thinking.ExtractSummaryConfig([]byte(body), "codex"))
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfoAndSummary(user-defined) error = %v", err)
	}
	if got := gjson.GetBytes(out, "reasoning.effort").String(); got != "low" {
		t.Errorf("user-defined model lost source effort: effort=%q; body=%s", got, out)
	}
	if len(gjson.GetBytes(out, "input").Array()) != 1 {
		t.Errorf("user-defined model retained an update: %s", out)
	}

	info.SupportConfigurationUpdate = true
	for _, tc := range []struct {
		name  string
		model string
		want  string
	}{
		{name: "native no suffix", model: "custom", want: "xhigh"},
		{name: "native suffix", model: "custom(high)", want: "high"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, errApply := thinking.ApplyThinkingWithModelInfoAndSummary([]byte(body), []byte(body), tc.model, "codex", "codex", "codex", info, thinking.ExtractSummaryConfig([]byte(body), "codex"))
			if errApply != nil {
				t.Fatalf("ApplyThinkingWithModelInfoAndSummary() error = %v", errApply)
			}
			if got := gjson.GetBytes(out, "reasoning.effort").String(); got != tc.want {
				t.Errorf("effort = %q, want %q; body=%s", got, tc.want, out)
			}
			if got := gjson.GetBytes(out, "input.0.reasoning.effort").String(); got != "low" {
				t.Errorf("input update effort changed to %q: %s", got, out)
			}
			if got := gjson.GetBytes(out, "reasoning.summary").String(); got != "auto" {
				t.Errorf("summary = %q, want auto; body=%s", got, out)
			}
		})
	}
}

func TestApplyThinkingWithModelInfoUsesOriginalResponsesEffort(t *testing.T) {
	modelInfo := &registry.ModelInfo{
		ID:       "claude-upstream",
		Type:     "claude",
		Thinking: &registry.ThinkingSupport{Levels: []string{"high", "max"}},
	}
	body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`)
	source := []byte(`{"reasoning":{"effort":"xhigh"}}`)
	out, err := thinking.ApplyThinkingWithModelInfo(body, source, "claude-upstream", "openai-response", "claude", "claude", modelInfo)
	if err != nil {
		t.Fatalf("ApplyThinkingWithModelInfo() error = %v", err)
	}
	if got := gjson.GetBytes(out, "output_config.effort").String(); got != "max" {
		t.Fatalf("output effort = %q, want max; body=%s", got, out)
	}
}
