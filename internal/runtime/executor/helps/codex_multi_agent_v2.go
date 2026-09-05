package helps

import (
	"context"
	"net/http"

	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	openaichatclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/chat-completions"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/openai/responses"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/codex/claude"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/gemini/claude"
	interactionsclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/interactions/claude"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// RewriteCodexSpawnAgentDescription optimizes spawn_agent definitions for
// official Codex clients when multi-agent v2 optimization is enabled.
func RewriteCodexSpawnAgentDescription(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) []byte {
	return multiagentv2.RewriteCodexSpawnAgentDescription(ctx, headers, payload, cfg)
}

// RewriteCodexMultiAgentV2Input converts official Codex multi-agent input into
// standard Responses API messages when multi-agent v2 optimization is enabled.
func RewriteCodexMultiAgentV2Input(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) []byte {
	return multiagentv2.RewriteCodexMultiAgentV2Input(ctx, headers, payload, cfg)
}

// RewriteCodexOrphanDelegationInput converts orphan Codex delegation outputs into
// standard user messages when orphan delegation compatibility is enabled and the
// request carries the X-Openai-Subagent: collab_spawn header.
func RewriteCodexOrphanDelegationInput(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) []byte {
	return multiagentv2.RewriteCodexOrphanDelegationInputForConfig(ctx, headers, payload, cfg)
}

// TranslateRequestWithCodexMultiAgentV2 normalizes official Codex multi-agent
// input before translating it to a non-Codex target protocol.
func TranslateRequestWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	return multiagentv2.TranslateRequestWithCodexMultiAgentV2(ctx, headers, cfg, from, to, model, payload, stream)
}

// TranslateRequestPairWithCodexMultiAgentV2 translates the untouched baseline
// payload and the working payload that later stages mutate in place. Executors
// normally assign the original payload to the request before translating, so both
// translations would rescan the same bytes and produce the same result. Built-in
// request translation is deterministic, so that case is translated once and
// duplicated when no plugin hooks are installed. Hooks retain two invocations
// because they may have request-scoped output or side effects. This removes a
// full extra pass over payloads that can reach tens of megabytes.
func TranslateRequestPairWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream bool) (original, working []byte) {
	original = TranslateRequestWithCodexMultiAgentV2(ctx, headers, cfg, from, to, model, originalPayload, stream)
	if sameByteSlice(originalPayload, requestPayload) && !sdktranslator.HasPluginHooks() {
		// The caller mutates the working copy, so it must not share the baseline array.
		return original, append([]byte(nil), original...)
	}
	return original, TranslateRequestWithCodexMultiAgentV2(ctx, headers, cfg, from, to, model, requestPayload, stream)
}

// sameByteSlice reports whether both slices describe the same bytes of the same
// backing array. It compares identity rather than content so the check stays
// constant time on large payloads.
func sameByteSlice(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	if len(a) == 0 {
		return true
	}
	return &a[0] == &b[0]
}

// TranslateRequestWithAPIKeyModelCompatibility applies compatibility-aware
// request translators when a configured API-key model enables compatibility mode.
func TranslateRequestWithAPIKeyModelCompatibility(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) []byte {
	if !isCompat {
		return TranslateRequestWithCodexMultiAgentV2(ctx, headers, cfg, from, to, model, payload, stream)
	}
	if from == sdktranslator.FormatOpenAIResponse {
		payload = RewriteCodexOrphanDelegationInput(ctx, headers, payload, cfg)
		if to != sdktranslator.FormatCodex && to != sdktranslator.FormatOpenAIResponse {
			payload = multiagentv2.RewriteCodexMultiAgentV2Input(ctx, headers, payload, cfg)
		}
	}

	var translated []byte
	switch {
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatCodex:
		translated = codexclaude.ConvertClaudeRequestToCodexWithCompat(model, payload, stream)
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatGemini:
		translated = geminiclaude.ConvertClaudeRequestToGeminiWithCompat(model, payload, stream)
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatInteractions:
		translated = interactionsclaude.ConvertClaudeRequestToInteractionsWithCompat(model, payload, stream)
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAI:
		translated = openaiclaude.ConvertClaudeRequestToOpenAIWithCompat(model, payload, stream)
	case from == sdktranslator.FormatOpenAI && to == sdktranslator.FormatClaude:
		translated = openaichatclaude.ConvertOpenAIRequestToClaudeWithCompat(model, payload, stream)
	case from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatClaude:
		translated = responsesclaude.ConvertOpenAIResponsesRequestToClaudeWithCompat(model, payload, stream)
	default:
		return TranslateRequestWithCodexMultiAgentV2(ctx, headers, cfg, from, to, model, payload, stream)
	}

	summaryConfig := thinking.ExtractSummaryConfig(payload, from.String())
	return thinking.ApplySummaryConfigForModel(translated, to.String(), model, summaryConfig)
}

// HasCodexMultiAgentV2NamespaceConflict reports whether the request defines
// the reserved optimized namespace, which must remain untouched.
func HasCodexMultiAgentV2NamespaceConflict(payload []byte) bool {
	return multiagentv2.HasCodexMultiAgentV2NamespaceConflict(payload)
}

// OptimizeCodexMultiAgentV2Request rewrites an eligible spawn_agent request and
// reports whether the collaboration namespace was renamed for upstream use.
func OptimizeCodexMultiAgentV2Request(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config) ([]byte, bool) {
	return multiagentv2.OptimizeCodexMultiAgentV2Request(ctx, headers, payload, cfg)
}

// OptimizeCodexMultiAgentV2RequestForAuth applies the standard Codex MultiAgentV2
// request optimization and, when the selected codex-api-key model has is-compat
// enabled, also converts agent_message items into portable message/user input.
func OptimizeCodexMultiAgentV2RequestForAuth(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config, auth *cliproxyauth.Auth, model string) ([]byte, bool) {
	updated, optimized := multiagentv2.OptimizeCodexMultiAgentV2Request(ctx, headers, payload, cfg)
	if cliproxyauth.CodexAPIKeyModelIsCompat(cfg, auth, model) {
		updated = multiagentv2.RewriteCodexMultiAgentV2Input(ctx, headers, updated, cfg)
	}
	return updated, optimized
}

// RestoreCodexMultiAgentV2Response restores optimized collaboration namespace
// values before an upstream response is translated and returned to the client.
func RestoreCodexMultiAgentV2Response(payload []byte, optimized bool) []byte {
	return multiagentv2.RestoreCodexMultiAgentV2Response(payload, optimized)
}
