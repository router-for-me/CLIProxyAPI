package executor

import (
	"strings"
	"testing"
)

// claudeCode2321MainBetas is the Anthropic-Beta value native Claude Code 2.1.321
// sent for an interactive cli main-thread turn on Fable 5.2, Fable 5.1, Opus 5.5
// and Sonnet 5.5 (captured against api.anthropic.com on 2026-11-09 with OAuth,
// in an auto-mode session, hence afk-mode).
const claudeCode2321MainBetas = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14," +
	"context-management-2025-06-27,prompt-caching-scope-2026-01-05,per-turn-control-2026-07-01," +
	"mid-conversation-tool-changes-2026-07-01,inline-tools-2026-09-15,advisor-tool-2026-03-01," +
	"thinking-token-count-2026-05-13,advanced-tool-use-2025-11-20,mid-conversation-system-clear-at-2026-08-21," +
	"effort-2025-11-24,dangerous-tool-use-2026-09-03,thinking-binding-controls-2026-08-01," +
	"thinking-display-updates-2026-08-18,afk-mode-2026-01-31,extended-cache-ttl-2025-04-11," +
	"cache-diagnosis-2026-04-07,message-threads-2026-08-12"

// claudeCode2321MainBody keeps only the fields of the captured main-thread body
// that gate betas: a role=system turn with an inline tool_addition, a deferred
// tool, the advisor tool, safeguards, thread, diagnostics, and a 1h cache TTL.
func claudeCode2321MainBody(model, display string) []byte {
	return []byte(`{"model":"` + model + `","max_tokens":128000,` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"say ok"}]},` +
		`{"role":"system","content":[{"type":"text","text":"tools changed"},` +
		`{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"mcp__docs__guide","input_schema":{"type":"object"}}}}]}],` +
		`"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},` +
		`{"type":"text","text":"prompt","cache_control":{"type":"ephemeral","ttl":"1h","scope":"global"}}],` +
		`"tools":[{"name":"Read","input_schema":{"type":"object"}},` +
		`{"name":"DeferredToolPlaceholder","input_schema":{"type":"object"},"defer_loading":true},` +
		`{"type":"advisor_20260301","name":"advisor","model":"claude-opus-5-5","defer_loading":true}],` +
		`"thinking":{"type":"adaptive","display":"` + display + `"},` +
		`"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":"all"}]},` +
		`"safeguards":{"type":"dangerous_tool_use"},"output_config":{"effort":"medium"},` +
		`"thread":{"type":"create"},"diagnostics":{"previous_message_id":null},` +
		`"metadata":{"user_id":"user"},"stream":true}`)
}

func TestClaudeCodeCLIBetas_MatchesClaudeCode2321Captures(t *testing.T) {
	// Haiku 5.5 moves claude-code-20250219 behind prompt-caching-scope.
	haiku55 := strings.Replace(claudeCode2321MainBetas,
		"claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,",
		"oauth-2025-04-20,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,claude-code-20250219,", 1)
	// The sdk-cli entrypoint sends thinking.display=omitted.
	sdkCLI := withoutClaudeBeta(claudeCode2321MainBetas, claudeThinkingDisplayUpdatesBeta)

	tests := []struct {
		name   string
		body   []byte
		native string
	}{
		{name: "fable-5-2 cli", body: claudeCode2321MainBody("claude-fable-5-2", "updates"), native: claudeCode2321MainBetas},
		{name: "fable-5-1 cli", body: claudeCode2321MainBody("claude-fable-5-1", "updates"), native: claudeCode2321MainBetas},
		{name: "opus-5-5 cli", body: claudeCode2321MainBody("claude-opus-5-5", "updates"), native: claudeCode2321MainBetas},
		{name: "sonnet-5-5 cli", body: claudeCode2321MainBody("claude-sonnet-5-5", "updates"), native: claudeCode2321MainBetas},
		{name: "haiku-5-5 cli", body: claudeCode2321MainBody("claude-haiku-5-5", "updates"), native: haiku55},
		{name: "fable-5-2 sdk-cli", body: claudeCode2321MainBody("claude-fable-5-2", "omitted"), native: sdkCLI},
		{
			name:   "haiku-4-5 quota probe",
			body:   []byte(`{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"quota"}],"metadata":{"user_id":"user"}}`),
			native: "oauth-2025-04-20,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,prompt-caching-scope-2026-01-05",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requested := claudeRequestedBetas(tt.native, nil)
			if got := claudeCodeCLIBetas(tt.body, requested, true); got != tt.native {
				t.Fatalf("with native betas requested:\n got: %s\nwant: %s", got, tt.native)
			}
			// A caller that sends no betas gets the same list minus afk-mode,
			// which Claude Code only sends from auto-mode sessions.
			want := withoutClaudeBeta(tt.native, claudeAFKModeBeta)
			if got := claudeCodeCLIBetas(tt.body, nil, true); got != want {
				t.Fatalf("with no betas requested:\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

// The 2.1.321 capture of the prompt "say ok" carried cc_version=2.1.321.20d.
func TestComputeFingerprintMatchesClaudeCode2321Capture(t *testing.T) {
	if got := computeFingerprint("say ok", "2.1.321"); got != "20d" {
		t.Fatalf("computeFingerprint() = %q, want 20d", got)
	}
}
