package executor

import (
	"bytes"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	claudeTokenCountingBeta          = "token-counting-2024-11-01"
	claudeFastModeBeta               = "fast-mode-2026-02-01"
	claudeOAuthBeta                  = "oauth-2025-04-20"
	claudeCodeBeta                   = "claude-code-20250219"
	claudeContext1MBeta              = "context-1m-2025-08-07"
	claudeMidConvSystemBeta          = "mid-conversation-system-2026-04-07"
	claudePerTurnControlBeta         = "per-turn-control-2026-07-01"
	claudePerTurnTimingBeta          = "timing-2026-09-09"
	claudeMidConvToolChangesBeta     = "mid-conversation-tool-changes-2026-07-01"
	claudeInlineToolsBeta            = "inline-tools-2026-09-15"
	claudeMidConvSystemClearAtBeta   = "mid-conversation-system-clear-at-2026-08-21"
	claudeDangerousToolUseBeta       = "dangerous-tool-use-2026-09-03"
	claudeAdvisorToolBeta            = "advisor-tool-2026-03-01"
	claudeAdvancedToolUseBeta        = "advanced-tool-use-2025-11-20"
	claudeEffortBeta                 = "effort-2025-11-24"
	claudeServerSideFallbackBeta     = "server-side-fallback-2026-06-01"
	claudeFallbackCreditBeta         = "fallback-credit-2026-06-01"
	claudeStructuredOutputsBeta      = "structured-outputs-2025-12-15"
	claudeThinkingDisplayUpdatesBeta = "thinking-display-updates-2026-08-18"
	claudeThinkingBindingBeta        = "thinking-binding-controls-2026-08-01"
	claudeThinkingResumptionBeta     = "thinking-resumption-2026-07-17"
	claudeExtendedCacheTTLBeta       = "extended-cache-ttl-2025-04-11"
	claudePromptCachingEvictBeta     = "prompt-caching-evict-2026-05-12"
	claudeCacheDiagnosisBeta         = "cache-diagnosis-2026-04-07"
	claudeRedactThinkingBeta         = "redact-thinking-2026-02-12"
	claudeAFKModeBeta                = "afk-mode-2026-01-31"
)

// claudeCodeCLIConstantBetas are the betas Claude Code sends on every
// /v1/messages request from the "cli" entrypoint, in wire order, excluding the
// leading claude-code-20250219.
//
// redact-thinking-2026-02-12 belongs here because cloaked requests always claim
// cc_entrypoint=cli; the "sdk-cli" entrypoint omits it. It is still dropped for
// requests that carry thinking.display, see claudeThinkingDisplaySet.
var claudeCodeCLIConstantBetas = []string{
	"interleaved-thinking-2025-05-14",
	claudeRedactThinkingBeta,
	"thinking-token-count-2026-05-13",
	"context-management-2025-06-27",
	"prompt-caching-scope-2026-01-05",
}

// claudeCodeTrailingBetas are caller-supplied betas that real Claude Code emits
// after effort-2025-11-24, in that relative order. They are forwarded when the
// caller asks for them and dropped otherwise.
var claudeCodeTrailingBetas = []string{
	claudeServerSideFallbackBeta,
	claudeFallbackCreditBeta,
	claudeStructuredOutputsBeta,
}

// claudeManagedBetaSet holds every beta the proxy itself assembles or gates.
// Caller betas outside this set are unknown to the pinned Claude Code profile —
// newer client releases ship betas past it — and are forwarded verbatim so
// their features keep working (#5738).
var claudeManagedBetaSet = func() map[string]bool {
	managed := []string{
		claudeTokenCountingBeta,
		claudeFastModeBeta,
		claudeOAuthBeta,
		claudeCodeBeta,
		claudeContext1MBeta,
		claudeMidConvSystemBeta,
		claudePerTurnControlBeta,
		claudePerTurnTimingBeta,
		claudeMidConvToolChangesBeta,
		claudeInlineToolsBeta,
		claudeMidConvSystemClearAtBeta,
		claudeDangerousToolUseBeta,
		claudeAdvisorToolBeta,
		claudeAdvancedToolUseBeta,
		claudeEffortBeta,
		claudeServerSideFallbackBeta,
		claudeFallbackCreditBeta,
		claudeStructuredOutputsBeta,
		claudeThinkingDisplayUpdatesBeta,
		claudeThinkingBindingBeta,
		claudeThinkingResumptionBeta,
		claudeExtendedCacheTTLBeta,
		claudePromptCachingEvictBeta,
		claudeCacheDiagnosisBeta,
		claudeRedactThinkingBeta,
		claudeAFKModeBeta,
	}
	managed = append(managed, claudeCodeCLIConstantBetas...)
	managed = append(managed, claudeCodeTrailingBetas...)
	set := make(map[string]bool, len(managed))
	for _, beta := range managed {
		set[beta] = true
	}
	return set
}()

func isManagedClaudeBeta(beta string) bool {
	return claudeManagedBetaSet[strings.TrimSpace(beta)]
}

// claudeCodeCLIBetas assembles the Anthropic-Beta baseline the way Claude Code
// 2.1.280 does: the list is per-request, not a fixed string. requested holds the
// betas the caller asked for, which decide the capability flags below.
//
// Verified against api.anthropic.com with native 2.1.258 captures on interactive,
// non-interactive, subagent, and multi-model paths (Sonnet, Opus, Fable, Haiku).
// Claude Code 2.1.280 (measured 2026-09-23, binary 80abbfe) inserts
// mid-conversation-tool-changes immediately after mid-conversation-system on the
// same non-legacy models. The betas named in issue #6054 are feature-gated in
// that binary, so they are emitted only for the model capability or body shape
// that actually sends them, in the same relative order:
//
//	 1 claude-code-20250219
//	 2 oauth-2025-04-20                  OAuth credentials only
//	 3 context-1m-2025-08-07             [1m] model variants only
//	 4 interleaved-thinking-2025-05-14
//	 5 redact-thinking-2026-02-12        cli entrypoint, no thinking.display
//	 6 thinking-token-count-2026-05-13
//	 7 context-management-2025-06-27
//	 8 prompt-caching-scope-2026-01-05
//	 9 mid-conversation-system-2026-04-07  models accepting a role=system turn
//	10 per-turn-control-2026-07-01        opus-5-5 and fable-5-1, or requested
//	11 timing-2026-09-09                  per-turn timing body, or requested
//	12 mid-conversation-tool-changes-2026-07-01  same models as mid-conversation-system
//	13 inline-tools-2026-09-15            inline tool_addition blocks, or requested
//	14 advisor-tool-2026-03-01            requests declaring advisor tools or requesting advisor beta
//	15 advanced-tool-use-2025-11-20       requests using tool search or another advanced tool-use feature
//	16 mid-conversation-system-clear-at-2026-08-21  messages with clear_at, or requested
//	17 dangerous-tool-use-2026-09-03      safeguards body, or requested
//	18 effort-2025-11-24                  effort-supporting models with active thinking
//	19 server-side-fallback-2026-06-01    requests with fallbacks or requested
//	20 fallback-credit-2026-06-01         requests with fallback tokens, fallbacks, or requested
//	21 structured-outputs-2025-12-15      structured output requests
//	22 thinking-binding-controls-2026-08-01  thinking.block_binding, or requested
//	23 thinking-display-updates-2026-08-18 requests with thinking.display=updates
//	24 thinking-resumption-2026-07-17     requested only; the 2.1.280 flag defaults off
//	25 fast-mode-2026-02-01               speed:fast requests only
//	26 afk-mode-2026-01-31                auto-mode sessions, forwarded when the caller sends it
//	27 extended-cache-ttl-2025-04-11      OAuth credentials (omitted on subagent & probe)
//	28 prompt-caching-evict-2026-05-12    evict_on_complete, or requested
//	29 cache-diagnosis-2026-04-07         requests with diagnostics only
//
// An empty body keeps the optimistic role=system default, matching the cloaking
// policy for unknown and future model IDs.
func claudeCodeCLIBetas(body []byte, requested map[string]bool, oauthToken bool) string {
	betas := make([]string, 0, len(claudeCodeCLIConstantBetas)+len(claudeCodeTrailingBetas)+10)
	betas = append(betas, claudeCodeBeta)
	if oauthToken {
		betas = append(betas, claudeOAuthBeta)
	}
	if requested[claudeContext1MBeta] {
		betas = append(betas, claudeContext1MBeta)
	}
	redactThinking := !claudeThinkingDisplaySet(body)
	for _, beta := range claudeCodeCLIConstantBetas {
		if beta == claudeRedactThinkingBeta && !redactThinking {
			continue
		}
		betas = append(betas, beta)
	}
	if !claudeUsesLegacySystemReminder(body) {
		betas = append(betas, claudeMidConvSystemBeta)
		if claudeIncludePerTurnControl(body, requested) {
			betas = append(betas, claudePerTurnControlBeta)
		}
		if claudeIncludePerTurnTiming(body, requested) {
			betas = append(betas, claudePerTurnTimingBeta)
		}
		betas = append(betas, claudeMidConvToolChangesBeta)
		if claudeIncludeInlineTools(body, requested) {
			betas = append(betas, claudeInlineToolsBeta)
		}
	} else {
		// Legacy models have no mid-conversation slot. A caller that still names
		// these betas keeps them, in the same relative order, ahead of effort.
		if claudeIncludePerTurnControl(body, requested) {
			betas = append(betas, claudePerTurnControlBeta)
		}
		if claudeIncludePerTurnTiming(body, requested) {
			betas = append(betas, claudePerTurnTimingBeta)
		}
	}
	if requested[claudeAdvisorToolBeta] || claudeBodyHasAdvisorTool(body) {
		betas = append(betas, claudeAdvisorToolBeta)
	}
	if requested[claudeAdvancedToolUseBeta] || claudeBodyUsesAdvancedToolUse(body) {
		betas = append(betas, claudeAdvancedToolUseBeta)
	}
	if !claudeUsesLegacySystemReminder(body) && claudeIncludeMidConvClearAt(body, requested) {
		betas = append(betas, claudeMidConvSystemClearAtBeta)
	}
	if requested[claudeDangerousToolUseBeta] || gjson.GetBytes(body, "safeguards").Exists() {
		betas = append(betas, claudeDangerousToolUseBeta)
	}
	if claudeRequestSupportsEffort(body, requested) {
		betas = append(betas, claudeEffortBeta)
	}
	isProbeOrHelper := helps.IsClaudeProbeOrHelperRequest(body)
	if !isProbeOrHelper && (requested[claudeServerSideFallbackBeta] || gjson.GetBytes(body, "fallbacks").Exists()) {
		betas = append(betas, claudeServerSideFallbackBeta)
	}
	shouldIncludeFallbackCredit := requested[claudeFallbackCreditBeta] ||
		gjson.GetBytes(body, "fallback_credit_token").Exists() ||
		(oauthToken && gjson.GetBytes(body, "fallbacks").Exists())
	if shouldIncludeFallbackCredit {
		betas = append(betas, claudeFallbackCreditBeta)
	}
	for _, beta := range claudeCodeTrailingBetas {
		if beta == claudeServerSideFallbackBeta || beta == claudeFallbackCreditBeta {
			continue
		}
		if requested[beta] {
			betas = append(betas, beta)
		}
	}
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if requested[claudeThinkingBindingBeta] || gjson.GetBytes(body, "thinking.block_binding").Exists() {
		betas = append(betas, claudeThinkingBindingBeta)
	}
	if !isProbeOrHelper && thinkingType != "disabled" && (requested[claudeThinkingDisplayUpdatesBeta] || claudeThinkingDisplayUpdates(body)) {
		betas = append(betas, claudeThinkingDisplayUpdatesBeta)
	}
	if requested[claudeThinkingResumptionBeta] {
		betas = append(betas, claudeThinkingResumptionBeta)
	}
	if claudeRequestUsesFastMode(body, requested) {
		betas = append(betas, claudeFastModeBeta)
	}
	if requested[claudeAFKModeBeta] {
		betas = append(betas, claudeAFKModeBeta)
	}
	if !isProbeOrHelper {
		includeExtended := (oauthToken && !helps.IsClaudeSubagentRequest(nil, body)) ||
			requested[claudeExtendedCacheTTLBeta] ||
			helps.ClaudePayloadHas1hTTL(body)
		if includeExtended {
			betas = append(betas, claudeExtendedCacheTTLBeta)
		}
	}
	if requested[claudePromptCachingEvictBeta] || bytes.Contains(body, []byte(`"evict_on_complete"`)) {
		betas = append(betas, claudePromptCachingEvictBeta)
	}
	if diagnostics := gjson.GetBytes(body, "diagnostics"); diagnostics.IsObject() {
		betas = append(betas, claudeCacheDiagnosisBeta)
	}
	return strings.Join(betas, ",")
}

func isClaudeHaikuModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "haiku")
}

func claudeCanonicalModel(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	return model
}

// claudeModelHasPerTurnEffort reports models whose 2.1.280 catalog capability
// per_turn_effort puts per-turn-control-2026-07-01 on every first-party request.
func claudeModelHasPerTurnEffort(model string) bool {
	model = claudeCanonicalModel(model)
	return strings.HasPrefix(model, "claude-opus-5-5") || strings.HasPrefix(model, "claude-fable-5-1")
}

// claudeModelHasPerTurnTiming reports models whose catalog lists per_turn_timing.
// Claude Code still withholds timing-2026-09-09 unless CLAUDE_CODE_PER_TURN_TIMING
// is set, so the beta follows the body or an explicit caller request.
func claudeModelHasPerTurnTiming(model string) bool {
	model = claudeCanonicalModel(model)
	return claudeModelHasPerTurnEffort(model) || strings.HasPrefix(model, "claude-mythos-5-1")
}

func claudeIncludePerTurnControl(body []byte, requested map[string]bool) bool {
	if requested[claudePerTurnControlBeta] {
		return true
	}
	return claudeModelHasPerTurnEffort(gjson.GetBytes(body, "model").String())
}

func claudeIncludePerTurnTiming(body []byte, requested map[string]bool) bool {
	if requested[claudePerTurnTimingBeta] {
		return true
	}
	if !claudeModelHasPerTurnTiming(gjson.GetBytes(body, "model").String()) {
		return false
	}
	if gjson.GetBytes(body, "output_config.timing").Exists() {
		return true
	}
	found := false
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("output_config.timing").Exists() {
			found = true
			return false
		}
		return true
	})
	return found
}

func claudeIncludeInlineTools(body []byte, requested map[string]bool) bool {
	if requested[claudeInlineToolsBeta] {
		return true
	}
	found := false
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("content").ForEach(func(_, block gjson.Result) bool {
			if strings.EqualFold(strings.TrimSpace(block.Get("type").String()), "tool_addition") && block.Get("tool.definition").Exists() {
				found = true
				return false
			}
			return true
		})
		return !found
	})
	return found
}

func claudeIncludeMidConvClearAt(body []byte, requested map[string]bool) bool {
	if requested[claudeMidConvSystemClearAtBeta] {
		return true
	}
	found := false
	gjson.GetBytes(body, "messages").ForEach(func(_, msg gjson.Result) bool {
		if msg.Get("clear_at").Exists() {
			found = true
			return false
		}
		return true
	})
	return found
}

func claudeRequestSupportsEffort(body []byte, requested map[string]bool) bool {
	if len(body) > 0 {
		if helps.IsClaudeProbeOrHelperRequest(body) {
			return false
		}
		model := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "model").String()))
		if isClaudeHaikuModel(model) {
			return false
		}
		thinkingType := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String()))
		if thinkingType == "disabled" {
			return false
		}
	}
	if requested[claudeEffortBeta] {
		return true
	}
	return true
}

func claudeThinkingDisplayUpdates(body []byte) bool {
	display := gjson.GetBytes(body, "thinking.display")
	return display.Type == gjson.String && strings.EqualFold(strings.TrimSpace(display.String()), "updates")
}

// claudeBodyUsesAdvancedToolUse reports whether the request needs
// advanced-tool-use-2025-11-20. Claude Code 2.1.258 adds the beta only while
// tool search is active, which puts a tool_search_tool_* server tool and
// defer_loading tools on the wire; plain tool declarations no longer carry it
// (measured 2026-09-02: 158 inline tools, no beta). Tool use examples
// (input_examples) and programmatic tool calling (allowed_callers) sit behind
// the same beta and are just as visible in the body, so callers using them keep
// working without requesting the beta explicitly.
func claudeBodyUsesAdvancedToolUse(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
		if strings.HasPrefix(toolType, "tool_search_tool_") {
			return true
		}
		if tool.Get("defer_loading").Bool() || tool.Get("input_examples").Exists() || tool.Get("allowed_callers").Exists() {
			return true
		}
	}
	return false
}

// claudeBodyHasAdvisorTool reports whether the request body declares an
// advisor server tool.
func claudeBodyHasAdvisorTool(body []byte) bool {
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	for _, tool := range tools.Array() {
		toolType := strings.ToLower(strings.TrimSpace(tool.Get("type").String()))
		if strings.HasPrefix(toolType, "advisor_") {
			return true
		}
	}
	return false
}

// claudeThinkingDisplaySet reports whether the request carries a thinking.display
// value. Claude Code 2.1.220 and redact-thinking-2026-02-12 are mutually
// exclusive by construction: the beta is only appended while thinking summaries
// are off, and the request builder removes it again whenever a display value is
// attached. Sending both makes Anthropic honour the redaction and return thinking
// blocks with an empty thinking field, so the caller's summary request would be
// answered with a signature and no text. Verified on api.anthropic.com with
// claude-opus-4-8: display=summarized yields thinking text only when the beta is
// absent, and a native 2.1.220 CLI run with showThinkingSummaries enabled sends
// display=summarized without the beta.
func claudeThinkingDisplaySet(body []byte) bool {
	display := gjson.GetBytes(body, "thinking.display")
	return display.Type == gjson.String && strings.TrimSpace(display.String()) != ""
}

// claudeRequestUsesFastMode reports whether the request selects the fast service
// tier. Anthropic rejects the body's speed field with "Extra inputs are not
// permitted" unless fast-mode-2026-02-01 is declared, so the beta has to follow
// the body. Deriving it here rather than at the call sites is deliberate: the
// streaming and non-streaming paths previously disagreed and streaming silently
// dropped the beta, turning every fast request into a 400.
func claudeRequestUsesFastMode(body []byte, requested map[string]bool) bool {
	if requested[claudeFastModeBeta] {
		return true
	}
	speed := gjson.GetBytes(body, "speed")
	return speed.Type == gjson.String && strings.EqualFold(strings.TrimSpace(speed.String()), "fast")
}

// claudeCountTokensBetas is the fixed profile Claude Code 2.1.220 sends to
// /v1/messages/count_tokens. It is far smaller than the inference baseline:
// redact-thinking, thinking-token-count, prompt-caching-scope, effort and every
// conditional beta are absent. Verified identical across 37 captured calls.
var claudeCountTokensBetas = []string{
	claudeCodeBeta,
	"interleaved-thinking-2025-05-14",
	"context-management-2025-06-27",
	claudeTokenCountingBeta,
}

func claudeCountTokensBetasForCredential(oauthToken bool) string {
	betas := make([]string, 0, len(claudeCountTokensBetas)+1)
	betas = append(betas, claudeCodeBeta)
	if oauthToken {
		betas = append(betas, claudeOAuthBeta)
	}
	betas = append(betas, claudeCountTokensBetas[1:]...)
	return strings.Join(betas, ",")
}

func withClaudeCountTokensOAuthBeta(betas string) string {
	parts := make([]string, 0, len(claudeCountTokensBetas)+1)
	seen := make(map[string]bool)
	for _, beta := range strings.Split(betas, ",") {
		if beta = strings.TrimSpace(beta); beta != "" && !seen[beta] {
			parts = append(parts, beta)
			seen[beta] = true
		}
	}
	if seen[claudeOAuthBeta] {
		return strings.Join(parts, ",")
	}
	insertAt := 0
	if len(parts) > 0 && parts[0] == claudeCodeBeta {
		insertAt = 1
	}
	parts = append(parts, "")
	copy(parts[insertAt+1:], parts[insertAt:])
	parts[insertAt] = claudeOAuthBeta
	return strings.Join(parts, ",")
}

// withClaudeOAuthCredentialBetas restores the credential-scoped betas that
// describe the selected upstream OAuth account rather than caller capability.
//
// A confirmed native client authenticates to CPA with whatever key the user
// configured and cannot know that CPA will select an OAuth credential upstream,
// so its header never carries the OAuth betas. Passing it through verbatim ships
// a Bearer request that declares neither oauth-2025-04-20 nor
// extended-cache-ttl-2025-04-11, which no real OAuth client ever does. Passthrough
// governs what the caller expressed; the credential is CPA's own choice and has to
// be described accurately.
//
// Betas already present are left exactly where the caller put them.
func withClaudeOAuthCredentialBetas(betas string, includeExtendedCacheTTL bool) string {
	parts := make([]string, 0, 16)
	seen := make(map[string]bool)
	for _, beta := range strings.Split(betas, ",") {
		if beta = strings.TrimSpace(beta); beta != "" && !seen[beta] {
			parts = append(parts, beta)
			seen[beta] = true
		}
	}
	if !seen[claudeOAuthBeta] {
		// Captured position 2, directly after claude-code-20250219.
		insertAt := 0
		if len(parts) > 0 && parts[0] == claudeCodeBeta {
			insertAt = 1
		}
		parts = append(parts, "")
		copy(parts[insertAt+1:], parts[insertAt:])
		parts[insertAt] = claudeOAuthBeta
	}
	if includeExtendedCacheTTL && !seen[claudeExtendedCacheTTLBeta] {
		parts = append(parts, claudeExtendedCacheTTLBeta)
	}
	return strings.Join(parts, ",")
}

func withoutClaudeBeta(betas, removeBeta string) string {
	parts := strings.Split(betas, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" && p != removeBeta {
			res = append(res, p)
		}
	}
	return strings.Join(res, ",")
}

func withClaudeExtendedCacheTTLBeta(betas string) string {
	parts := make([]string, 0, 16)
	seen := make(map[string]bool)
	for _, beta := range strings.Split(betas, ",") {
		if beta = strings.TrimSpace(beta); beta != "" && !seen[beta] {
			parts = append(parts, beta)
			seen[beta] = true
		}
	}
	if !seen[claudeExtendedCacheTTLBeta] {
		parts = append(parts, claudeExtendedCacheTTLBeta)
	}
	return strings.Join(parts, ",")
}

// withClaudeAdvisorToolBeta ensures advisor-tool-2026-03-01 is present when
// the body declares an advisor server tool, placed at the observed wire position
// before advanced-tool-use-2025-11-20 or effort-2025-11-24. Every beta that
// follows advisor on the wire, afk-mode-2026-01-31 included, is an insertion
// boundary so a caller-supplied trailer never ends up ahead of it.
func withClaudeAdvisorToolBeta(betas string) string {
	if strings.TrimSpace(betas) == "" {
		return claudeAdvisorToolBeta
	}
	parts := make([]string, 0, 16)
	seen := make(map[string]bool)
	for _, beta := range strings.Split(betas, ",") {
		if beta = strings.TrimSpace(beta); beta != "" && beta != claudeAdvisorToolBeta && !seen[beta] {
			parts = append(parts, beta)
			seen[beta] = true
		}
	}
	insertAt := len(parts)
	for index, beta := range parts {
		if beta == claudeAdvancedToolUseBeta ||
			beta == claudeEffortBeta ||
			beta == claudeServerSideFallbackBeta ||
			beta == claudeFallbackCreditBeta ||
			beta == claudeStructuredOutputsBeta ||
			beta == claudeFastModeBeta ||
			beta == claudeAFKModeBeta ||
			beta == claudeExtendedCacheTTLBeta ||
			beta == claudeCacheDiagnosisBeta {
			insertAt = index
			break
		}
	}
	parts = append(parts, "")
	copy(parts[insertAt+1:], parts[insertAt:])
	parts[insertAt] = claudeAdvisorToolBeta
	return strings.Join(parts, ",")
}

// claudeEntitlementError marks an upstream refusal that is a property of the
// request shape combined with the account's entitlements, not of the credential's
// health. The auth manager must neither rotate nor cool down on these.
type claudeEntitlementError struct {
	statusErr
}

func (claudeEntitlementError) IsRequestScoped() bool {
	return true
}

func (claudeEntitlementError) IsCredentialScoped() bool {
	return false
}

type claudeRateLimitError struct {
	statusErr
	credentialScoped bool
}

func (e claudeRateLimitError) IsCredentialScoped() bool {
	return e.credentialScoped
}

func (e claudeRateLimitError) IsRequestScoped() bool {
	return false
}

// classifyClaudeUpstreamError promotes upstream refusals that no other credential
// can satisfy into request-scoped errors.
//
// Anthropic answers a fast-mode request from an account without the matching
// usage credits with 429 rate_limit_error "Usage credits are required for fast
// mode". The generic pipeline reads 429 as quota exhaustion: it marks the
// credential Quota.Exceeded, applies an exponential cooldown and rotates to the
// next one, which returns the same 429. A single speed:"fast" request would walk
// the whole Claude pool and cool down every credential, all of which remain
// perfectly healthy for ordinary traffic. The refusal belongs to the request.
func classifyClaudeUpstreamError(statusCode int, headers http.Header, body []byte) error {
	return classifyClaudeUpstreamErrorWithCooling(statusCode, headers, body, false)
}

func classifyClaudeUpstreamErrorWithCooling(statusCode int, headers http.Header, body []byte, modelLevelCooling bool) error {
	var retryAfter *time.Duration
	if statusCode == http.StatusTooManyRequests || (statusCode >= 400 && statusCode < 600) {
		retryAfter = helps.ParseClaudeRateLimitReset(headers, time.Now())
	}
	err := statusErr{code: statusCode, msg: string(body), retryAfter: retryAfter}
	if statusCode == http.StatusTooManyRequests {
		if !modelLevelCooling && helps.ClaudeHeadersIndicateUnifiedRateLimitRejection(headers) {
			return claudeRateLimitError{statusErr: err, credentialScoped: true}
		}
		if claudeBodyIndicatesFastModeCredits(body) {
			return claudeEntitlementError{err}
		}
		// Ordinary model-level Claude 429 (not a unified 5h/7d rejection)
		return claudeRateLimitError{statusErr: err, credentialScoped: false}
	}
	return err
}

// claudeBodyIndicatesFastModeCredits matches Anthropic's fast-mode entitlement
// refusal without matching a genuine rate limit, which never mentions fast mode.
func claudeBodyIndicatesFastModeCredits(body []byte) bool {
	message := strings.ToLower(gjson.GetBytes(body, "error.message").String())
	if message == "" {
		message = strings.ToLower(string(body))
	}
	return strings.Contains(message, "fast request rejected") ||
		(strings.Contains(message, "fast") &&
			(strings.Contains(message, "usage credits") || strings.Contains(message, "credits are required")))
}

// claudeRequestedBetas collects every beta the caller asked for, from the
// Anthropic-Beta header and from betas lifted out of the request body.
func claudeRequestedBetas(incomingBetas string, extraBetas []string) map[string]bool {
	requested := make(map[string]bool)
	for _, beta := range strings.Split(incomingBetas, ",") {
		if beta = strings.TrimSpace(beta); beta != "" {
			requested[beta] = true
		}
	}
	for _, beta := range extraBetas {
		if beta = strings.TrimSpace(beta); beta != "" {
			requested[beta] = true
		}
	}
	return requested
}

// isAnthropicUpstreamURL reports whether a resolved request targets Anthropic's
// first-party API.
//
// Every rule that reconstructs Claude Code's identity must key on this rather
// than on the cloaked flag. Kimi rewrites base_url to api.kimi.com and custom
// gateways set their own host, yet both delegate to ClaudeExecutor and are
// therefore cloaked; a cloak-keyed rule silently rewrites their traffic too.
func isAnthropicUpstreamURL(u *url.URL) bool {
	return helps.IsAnthropicUpstreamURL(u)
}

// isAnthropicUpstreamBase reports whether a configured base URL targets Anthropic's
// first-party API. Used before the outgoing request exists.
func isAnthropicUpstreamBase(baseURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return isAnthropicUpstreamURL(parsed)
}

// extractAndRemoveBetas extracts the "betas" array from the body and removes it.
// Returns the extracted betas as a string slice and the modified body.
func extractAndRemoveBetas(body []byte) ([]string, []byte) {
	betasResult := gjson.GetBytes(body, "betas")
	if !betasResult.Exists() {
		return nil, body
	}
	var betas []string
	if betasResult.IsArray() {
		for _, item := range betasResult.Array() {
			if s := strings.TrimSpace(item.String()); s != "" {
				betas = append(betas, s)
			}
		}
	} else if s := strings.TrimSpace(betasResult.String()); s != "" {
		betas = append(betas, s)
	}
	body, _ = sjson.DeleteBytes(body, "betas")
	return betas, body
}

// disableThinkingIfToolChoiceForced checks if tool_choice forces tool use and disables thinking.
// Anthropic API does not allow thinking when tool_choice is set to "any" or a specific tool.
// See: https://docs.anthropic.com/en/docs/build-with-claude/extended-thinking#important-considerations
func disableThinkingIfToolChoiceForced(body []byte) []byte {
	toolChoiceType := gjson.GetBytes(body, "tool_choice.type").String()
	// "auto" is allowed with thinking, but "any" or "tool" (specific tool) are not
	if toolChoiceType == "any" || toolChoiceType == "tool" {
		// Remove thinking configuration entirely to avoid API error
		body, _ = sjson.DeleteBytes(body, "thinking")
		// Adaptive thinking may also set output_config.effort; remove it to avoid
		// leaking thinking controls when tool_choice forces tool use.
		body, _ = sjson.DeleteBytes(body, "output_config.effort")
		if oc := gjson.GetBytes(body, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "output_config")
		}
	}
	return body
}

// normalizeClaudeSamplingForUpstream keeps Anthropic message requests valid.
//
// Translated and cloaked callers keep the conservative normalization: their
// sampling knobs come from a protocol that was not written for Anthropic, and
// Anthropic rejects several combinations outright, so neither temperature nor
// top_p is worth forwarding.
//
// A confirmed native Claude Code client owns its own wire, exactly like
// cache_control placement. The measured structured Haiku helper sends
// "temperature":1 and claudeCodeHelperShapeStructured keys on it, so stripping
// it would emit a shape no native client ever produces. Keep what the caller
// sent and drop only what Anthropic actually rejects (verified live):
//   - thinking active: temperature must be 1, top_p must be >= 0.95, top_k unset
//   - otherwise: temperature and top_p cannot both be specified
func normalizeClaudeSamplingForUpstream(body []byte, nativeOwned bool) []byte {
	thinkingActive := false
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String())) {
	case "enabled", "adaptive", "auto":
		thinkingActive = true
	}

	if !nativeOwned {
		body, _ = sjson.DeleteBytes(body, "temperature")
		body, _ = sjson.DeleteBytes(body, "top_p")
		if thinkingActive {
			body, _ = sjson.DeleteBytes(body, "top_k")
		}
		return body
	}

	if thinkingActive {
		if temperature := gjson.GetBytes(body, "temperature"); temperature.Exists() && temperature.Num != 1 {
			body, _ = sjson.DeleteBytes(body, "temperature")
		}
		if topP := gjson.GetBytes(body, "top_p"); topP.Exists() && topP.Num < 0.95 {
			body, _ = sjson.DeleteBytes(body, "top_p")
		}
		body, _ = sjson.DeleteBytes(body, "top_k")
		return body
	}
	// Anthropic accepts either one but not both; temperature is the knob native
	// Claude Code actually sends, so top_p is the one that gives way.
	if gjson.GetBytes(body, "temperature").Exists() && gjson.GetBytes(body, "top_p").Exists() {
		body, _ = sjson.DeleteBytes(body, "top_p")
	}
	return body
}
