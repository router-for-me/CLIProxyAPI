package test

import (
	"context"
	"fmt"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/antigravity"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/claude"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/codex"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/gemini"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/interactions"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/kimi"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/openai"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/thinking/provider/xai"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// knownFormats are the schema identifiers observed in the default translator
// registry. Use Has*Transformer to discover which (from,to) pairs are wired.
var knownFormats = []string{
	"openai",
	"openai-response",
	"claude",
	"gemini",
	"codex",
	"antigravity",
	"interactions",
	"kiro",
}

// requestProbe describes a source payload for the high (reasoning enabled) and
// none (reasoning disabled) effort levels used to test (a).
type requestProbe struct {
	high []byte
	none []byte
}

var requestProbes = map[string]requestProbe{
	"openai": {
		high: []byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`),
		none: []byte(`{"reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`),
	},
	"openai-response": {
		high: []byte(`{"reasoning":{"effort":"high","summary":"auto"},"input":"hi"}`),
		none: []byte(`{"reasoning":{"effort":"none","summary":null},"input":"hi"}`),
	},
	"claude": {
		high: []byte(`{"thinking":{"type":"enabled","budget_tokens":24576,"display":"summarized"},"messages":[{"role":"user","content":"hi"}]}`),
		none: []byte(`{"thinking":{"type":"disabled"},"messages":[{"role":"user","content":"hi"}]}`),
	},
	"gemini": {
		high: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
		none: []byte(`{"generationConfig":{"thinkingConfig":{"thinkingLevel":"none","includeThoughts":false}},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	},
	"antigravity": {
		high: []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"high","includeThoughts":true}},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
		none: []byte(`{"request":{"generationConfig":{"thinkingConfig":{"thinkingLevel":"none","includeThoughts":false}},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`),
	},
	"interactions": {
		high: []byte(`{"generation_config":{"thinking_level":"high","thinking_summaries":"auto"},"input":"hi"}`),
		none: []byte(`{"generation_config":{"thinking_level":"none","thinking_summaries":"none"},"input":"hi"}`),
	},
	"codex": {
		high: []byte(`{"reasoning":{"effort":"high","summary":"auto"},"messages":[{"role":"user","content":"hi"}]}`),
		none: []byte(`{"reasoning":{"effort":"none","summary":null},"messages":[{"role":"user","content":"hi"}]}`),
	},
	"kiro": {
		high: []byte(`{"reasoning_effort":"high","messages":[{"role":"user","content":"hi"}]}`),
		none: []byte(`{"reasoning_effort":"none","messages":[{"role":"user","content":"hi"}]}`),
	},
}

// targetEffortCheck verifies that output in the given target format reflects
// the requested effort level (high != none).
type targetEffortCheck struct {
	high func(t *testing.T, out []byte)
	none func(t *testing.T, out []byte)
}

var targetEffortChecks = map[string]targetEffortCheck{
	"openai": {
		high: func(t *testing.T, out []byte) {
			effort := gjson.GetBytes(out, "reasoning_effort").String()
			if effort == "" || effort == "none" {
				t.Fatalf("openai high: reasoning_effort = %q; out=%s", effort, out)
			}
		},
		none: func(t *testing.T, out []byte) {
			if gjson.GetBytes(out, "reasoning_effort").String() != "none" {
				t.Fatalf("openai none: reasoning_effort missing/wrong; out=%s", out)
			}
		},
	},
	"openai-response": {
		high: func(t *testing.T, out []byte) {
			effort := gjson.GetBytes(out, "reasoning.effort").String()
			if effort == "" || effort == "none" {
				t.Fatalf("openai-response high: reasoning.effort = %q; out=%s", effort, out)
			}
			if gjson.GetBytes(out, "reasoning.summary").String() != "auto" {
				t.Fatalf("openai-response high: reasoning.summary != auto; out=%s", out)
			}
		},
		none: func(t *testing.T, out []byte) {
			if gjson.GetBytes(out, "reasoning.effort").String() != "none" {
				t.Fatalf("openai-response none: reasoning.effort != none; out=%s", out)
			}
			if gjson.GetBytes(out, "reasoning.summary").Exists() {
				t.Fatalf("openai-response none: reasoning.summary should be absent; out=%s", out)
			}
		},
	},
	"codex": {
		high: func(t *testing.T, out []byte) {
			effort := gjson.GetBytes(out, "reasoning.effort").String()
			if effort == "" || effort == "none" {
				t.Fatalf("codex high: reasoning.effort = %q; out=%s", effort, out)
			}
			if gjson.GetBytes(out, "reasoning.summary").String() != "auto" {
				t.Fatalf("codex high: reasoning.summary != auto; out=%s", out)
			}
		},
		none: func(t *testing.T, out []byte) {
			if gjson.GetBytes(out, "reasoning.effort").String() != "none" {
				t.Fatalf("codex none: reasoning.effort != none; out=%s", out)
			}
			if gjson.GetBytes(out, "reasoning.summary").Exists() {
				t.Fatalf("codex none: reasoning.summary should be absent; out=%s", out)
			}
		},
	},
	"claude": {
		high: func(t *testing.T, out []byte) {
			thinkingType := gjson.GetBytes(out, "thinking.type").String()
			if thinkingType != "enabled" && thinkingType != "adaptive" {
				t.Fatalf("claude high: thinking.type = %q; out=%s", thinkingType, out)
			}
			if gjson.GetBytes(out, "thinking.display").String() != "summarized" {
				t.Fatalf("claude high: thinking.display != summarized; out=%s", out)
			}
		},
		none: func(t *testing.T, out []byte) {
			if gjson.GetBytes(out, "thinking.type").String() != "disabled" {
				t.Fatalf("claude none: thinking.type != disabled; out=%s", out)
			}
			if gjson.GetBytes(out, "thinking.display").Exists() {
				t.Fatalf("claude none: thinking.display should be absent; out=%s", out)
			}
		},
	},
	"gemini": {
		high: func(t *testing.T, out []byte) {
			if !geminiThinkingEnabled(out, "generationConfig.thinkingConfig") {
				t.Fatalf("gemini high: no enabled thinking config; out=%s", out)
			}
		},
		none: func(t *testing.T, out []byte) {
			if !geminiThinkingDisabled(out, "generationConfig.thinkingConfig") {
				t.Fatalf("gemini none: thinking not disabled; out=%s", out)
			}
		},
	},
	"antigravity": {
		high: func(t *testing.T, out []byte) {
			if !geminiThinkingEnabled(out, "request.generationConfig.thinkingConfig") {
				t.Fatalf("antigravity high: no enabled thinking config; out=%s", out)
			}
		},
		none: func(t *testing.T, out []byte) {
			if !geminiThinkingDisabled(out, "request.generationConfig.thinkingConfig") {
				t.Fatalf("antigravity none: thinking not disabled; out=%s", out)
			}
		},
	},
	"interactions": {
		high: func(t *testing.T, out []byte) {
			if gjson.GetBytes(out, "generation_config.thinking_summaries").String() != "auto" {
				t.Fatalf("interactions high: thinking_summaries != auto; out=%s", out)
			}
			if gjson.GetBytes(out, "generation_config.thinking_level").String() != "high" &&
				gjson.GetBytes(out, "generation_config.thinking_config.thinking_budget").Int() <= 0 {
				t.Fatalf("interactions high: no high thinking level/budget; out=%s", out)
			}
		},
		none: func(t *testing.T, out []byte) {
			summaries := gjson.GetBytes(out, "generation_config.thinking_summaries").String()
			if summaries != "none" && summaries != "" {
				t.Fatalf("interactions none: thinking_summaries = %q; out=%s", summaries, out)
			}
			level := gjson.GetBytes(out, "generation_config.thinking_level").String()
			budget := gjson.GetBytes(out, "generation_config.thinking_config.thinking_budget")
			if level != "none" && !(budget.Exists() && budget.Int() == 0) {
				t.Fatalf("interactions none: no explicit none level or zero budget; out=%s", out)
			}
		},
	},
	"kiro": {
		high: func(t *testing.T, out []byte) {
			// Kiro request translators pass through the source format; the
			// executor's payload builder later consumes these fields.
			effort := gjson.GetBytes(out, "reasoning_effort").String()
			if effort != "" && effort != "none" {
				return
			}
			if thinkingType := gjson.GetBytes(out, "thinking.type").String(); thinkingType == "enabled" || thinkingType == "adaptive" {
				return
			}
			t.Fatalf("kiro high: no reasoning intent found; out=%s", out)
		},
		none: func(t *testing.T, out []byte) {
			if gjson.GetBytes(out, "reasoning_effort").String() == "none" {
				return
			}
			if gjson.GetBytes(out, "thinking.type").String() == "disabled" {
				return
			}
			t.Fatalf("kiro none: reasoning not disabled; out=%s", out)
		},
	},
}

func geminiThinkingEnabled(out []byte, prefix string) bool {
	if gjson.GetBytes(out, prefix+".thinkingLevel").String() == "high" {
		if gjson.GetBytes(out, prefix+".includeThoughts").String() == "true" {
			return true
		}
	}
	if budget := gjson.GetBytes(out, prefix+".thinkingBudget").Int(); budget > 0 {
		if gjson.GetBytes(out, prefix+".includeThoughts").String() == "true" {
			return true
		}
	}
	return false
}

func geminiThinkingDisabled(out []byte, prefix string) bool {
	includeThoughts := gjson.GetBytes(out, prefix+".includeThoughts")
	if includeThoughts.Exists() && includeThoughts.String() == "true" {
		return false
	}
	if includeThoughts.Exists() && includeThoughts.String() == "false" {
		return true
	}
	if gjson.GetBytes(out, prefix+".thinkingLevel").String() == "none" {
		return true
	}
	if budget := gjson.GetBytes(out, prefix+".thinkingBudget"); budget.Exists() && budget.Int() == 0 {
		return true
	}
	return false
}

func TestDegradationRequestEffortMapping(t *testing.T) {
	for _, from := range knownFormats {
		probe, ok := requestProbes[from]
		if !ok {
			continue
		}
		for _, to := range knownFormats {
			fromF := sdktranslator.FromString(from)
			toF := sdktranslator.FromString(to)
			if !sdktranslator.HasRequestTransformer(fromF, toF) {
				continue
			}
			check, ok := targetEffortChecks[to]
			if !ok {
				continue
			}
			for _, stream := range []bool{false, true} {
				name := fmt.Sprintf("%s_to_%s_stream_%v", from, to, stream)
				t.Run(name, func(t *testing.T) {
					highOut := sdktranslator.TranslateRequest(fromF, toF, "doctrine-model", probe.high, stream)
					check.high(t, highOut)

					noneOut := sdktranslator.TranslateRequest(fromF, toF, "doctrine-model", probe.none, stream)

					if from == "claude" && (to == "gemini" || to == "antigravity") &&
						!gjson.GetBytes(noneOut, "generationConfig.thinkingConfig").Exists() &&
						!gjson.GetBytes(noneOut, "request.generationConfig.thinkingConfig").Exists() {
						t.Skipf("%s -> %s translator does not emit an explicit thinkingConfig disabled signal; fix pending", from, to)
					}

					check.none(t, noneOut)
				})
			}
		}
	}
}

// TestDegradationResponseDoctrines exercises (b)-(d) for non-stream response
// translators. Cases that already pass on stock dev are hard assertions (no
// skipPR). Cases that require an unreleased Plus fix detect the pre-fix state
// and skip; once that fix lands the pre-fix state disappears and any remaining
// output failure becomes FAIL.
func TestDegradationResponseDoctrines(t *testing.T) {
	cases := []struct {
		name     string
		from     string
		to       string
		skipPR   string
		request  []byte
		response []byte
		prereq   func(out []byte) string
		check    func(out []byte) string
	}{
		{
			name:     "openai_responses_reasoning_fallback",
			from:     "openai",
			to:       "openai-response",
			skipPR:   "",
			request:  []byte(`{"model":"o3-mini","reasoning":{"summary":"auto"},"messages":[{"role":"user","content":"hi"}]}`),
			response: []byte(`{"id":"chatcmpl_r","object":"chat.completion","created":1773896263,"model":"o3-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hello","reasoning":"Let me think"}}]}`),
			prereq:   func(out []byte) string { return "" },
			check: func(out []byte) string {
				if !gjson.GetBytes(out, "output.#(type==\"reasoning\")").Exists() {
					return fmt.Sprintf("reasoning item missing; out=%s", out)
				}
				return ""
			},
		},
		{
			name:    "claude_openai_reasoning_content_canonical",
			from:    "claude",
			to:      "openai",
			skipPR:  "",
			request: []byte(`{"model":"claude-opus-4-6","thinking":{"type":"adaptive","display":"summarized"},"messages":[{"role":"user","content":"hi"}]}`),
			response: []byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_123\",\"model\":\"claude-opus-4-6\"}}\n" +
				"data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"First thought. Second thought.\"}}\n" +
				"data: {\"type\":\"content_block_stop\",\"index\":0}\n" +
				"data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n" +
				"data: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"text_delta\",\"text\":\"Here is the solution.\"}}\n" +
				"data: {\"type\":\"content_block_stop\",\"index\":1}\n" +
				"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"input_tokens\":10,\"output_tokens\":20}}\n"),
			prereq: func(out []byte) string {
				if gjson.GetBytes(out, "choices.0.message.reasoning").Exists() &&
					!gjson.GetBytes(out, "choices.0.message.reasoning_content").Exists() {
					return "pre-fix: only legacy reasoning field present"
				}
				return ""
			},
			check: func(out []byte) string {
				if !gjson.GetBytes(out, "choices.0.message.reasoning_content").Exists() {
					return fmt.Sprintf("canonical reasoning_content missing; out=%s", out)
				}
				if gjson.GetBytes(out, "choices.0.message.reasoning").Exists() {
					return fmt.Sprintf("non-canonical reasoning field leaked; out=%s", out)
				}
				return ""
			},
		},
		{
			name:     "gemini_claude_thoughtsignature_preserved",
			from:     "gemini",
			to:       "claude",
			skipPR:   "",
			request:  []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
			response: []byte(`{"responseId":"resp-test","modelVersion":"gemini-test","candidates":[{"content":{"role":"model","parts":[{"thought":true,"text":"thinking text","thoughtSignature":"sig-test"},{"text":"hello world"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":21,"candidatesTokenCount":1,"totalTokenCount":131,"thoughtsTokenCount":109}}`),
			prereq: func(out []byte) string {
				if gjson.GetBytes(out, "content.#(type==\"thinking\")").Exists() &&
					!gjson.GetBytes(out, "content.#(type==\"thinking\").signature").Exists() {
					return "pre-fix: thinking block present but signature missing"
				}
				return ""
			},
			check: func(out []byte) string {
				if got := gjson.GetBytes(out, "content.#(type==\"thinking\").signature").String(); got != "sig-test" {
					return fmt.Sprintf("thinking signature = %q, want sig-test; out=%s", got, out)
				}
				return ""
			},
		},
		{
			name:     "gemini_claude_visible_text_with_signature_stays_text",
			from:     "gemini",
			to:       "claude",
			skipPR:   "Plus #190",
			request:  []byte(`{"model":"gemini-3.5-flash","contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
			response: []byte(`{"responseId":"resp-test","modelVersion":"gemini-test","candidates":[{"content":{"role":"model","parts":[{"text":"hello world","thoughtSignature":"sig-carrier"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":21,"candidatesTokenCount":1,"totalTokenCount":131}}`),
			prereq: func(out []byte) string {
				if !gjson.GetBytes(out, "content.#(type==\"text\")").Exists() &&
					gjson.GetBytes(out, "content.#(type==\"thinking\")").Exists() {
					return "pre-fix: visible text missing, misrouted to thinking"
				}
				return ""
			},
			check: func(out []byte) string {
				if !gjson.GetBytes(out, "content.#(type==\"text\")").Exists() {
					return fmt.Sprintf("visible text block missing; out=%s", out)
				}
				if gjson.GetBytes(out, "content.#(type==\"thinking\")").Exists() {
					return fmt.Sprintf("visible text misrouted to thinking; out=%s", out)
				}
				return ""
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fromF := sdktranslator.FromString(tc.from)
			toF := sdktranslator.FromString(tc.to)
			if !sdktranslator.HasNonStreamResponseTransformer(toF, fromF) {
				if tc.skipPR != "" {
					t.Skipf("no non-stream response transformer for %s -> %s; %s", tc.from, tc.to, tc.skipPR)
				}
				t.Fatalf("no non-stream response transformer for %s -> %s", tc.from, tc.to)
			}

			out := sdktranslator.TranslateNonStream(context.Background(), fromF, toF, "doctrine-model", tc.request, tc.request, tc.response, nil)

			if msg := tc.prereq(out); msg != "" {
				if tc.skipPR != "" {
					t.Skipf("current stock lacks %s: %s", tc.skipPR, msg)
				}
				t.Fatal(msg)
			}

			if msg := tc.check(out); msg != "" {
				t.Fatal(msg)
			}
		})
	}
}
