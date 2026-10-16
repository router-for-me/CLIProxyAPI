package thinking_test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/thinking/provider/claude"
	claudegemini "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/gemini"
	claudechat "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/chat-completions"
	clauderesponses "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	"github.com/tidwall/gjson"
)

// An OAuth Claude request carries no bound model definition, so the thinking
// pipeline reads the effort the translator already wrote. The translator must
// therefore keep every level the registry model supports: claude-opus-5-5 lists
// low, medium, high, xhigh and max.
func TestOpenAIChatToClaudeKeepsSupportedEffortLevels(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		t.Run(level, func(t *testing.T) {
			source := []byte(`{"model":"claude-opus-5-5","reasoning_effort":"` + level + `","messages":[{"role":"user","content":"hi"}]}`)
			translated := claudechat.ConvertOpenAIRequestToClaude("claude-opus-5-5", source, false)
			final, err := thinking.ApplyThinkingWithSourceAndSummary(translated, source, "claude-opus-5-5", "openai", "claude", "claude", thinking.SummaryConfig{})
			if err != nil {
				t.Fatalf("ApplyThinkingWithSourceAndSummary() error = %v", err)
			}
			if got := gjson.GetBytes(final, "output_config.effort").String(); got != level {
				t.Fatalf("output_config.effort = %q, want %q; body=%s", got, level, final)
			}
		})
	}
}

func TestOpenAIResponsesToClaudeKeepsXHigh(t *testing.T) {
	source := []byte(`{"model":"claude-opus-5-5","reasoning":{"effort":"xhigh"},"input":"hi"}`)
	translated := clauderesponses.ConvertOpenAIResponsesRequestToClaude("claude-opus-5-5", source, false)
	if got := gjson.GetBytes(translated, "output_config.effort").String(); got != "xhigh" {
		t.Fatalf("output_config.effort = %q, want xhigh; body=%s", got, translated)
	}
}

func TestGeminiToClaudeKeepsXHigh(t *testing.T) {
	source := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"generationConfig":{"thinkingConfig":{"thinkingLevel":"xhigh"}}}`)
	translated := claudegemini.ConvertGeminiRequestToClaude("claude-opus-5-5", source, false)
	if got := gjson.GetBytes(translated, "output_config.effort").String(); got != "xhigh" {
		t.Fatalf("output_config.effort = %q, want xhigh; body=%s", got, translated)
	}
}

// A model without xhigh still gets the strongest effort it has.
func TestMapToClaudeEffortHighIntentFallbacks(t *testing.T) {
	tests := []struct {
		level  string
		levels []string
		want   string
	}{
		{level: "xhigh", levels: []string{"low", "medium", "high", "xhigh", "max"}, want: "xhigh"},
		{level: "xhigh", levels: []string{"low", "medium", "high", "max"}, want: "max"},
		{level: "xhigh", levels: []string{"low", "medium", "high"}, want: "high"},
		{level: "max", levels: []string{"low", "medium", "high", "xhigh", "max"}, want: "max"},
		{level: "max", levels: []string{"low", "medium", "high", "xhigh"}, want: "xhigh"},
		{level: "max", levels: []string{"low", "medium", "high"}, want: "high"},
		{level: "minimal", levels: []string{"low", "medium", "high"}, want: "low"},
	}
	for _, tc := range tests {
		got, ok := thinking.MapToClaudeEffort(tc.level, tc.levels)
		if !ok || got != tc.want {
			t.Fatalf("MapToClaudeEffort(%q, %v) = %q, %v; want %q", tc.level, tc.levels, got, ok, tc.want)
		}
	}
}
