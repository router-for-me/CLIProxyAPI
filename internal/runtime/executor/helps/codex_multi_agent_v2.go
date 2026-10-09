package helps

import (
	"context"
	"net/http"

	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	openaichatclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/chat-completions"
	responsesclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/claude/openai/responses"
	codexclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/codex/claude"
	geminiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/claude"
	interactionsclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/interactions/claude"
	openaiclaude "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/claude"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
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
	return TranslateRequestWithCodexMultiAgentV2ForExecutor(ctx, headers, cfg, "", from, to, model, payload, stream)
}

// TranslateRequestWithCodexMultiAgentV2ForExecutor applies Codex client
// compatibility while respecting the actual target executor identity.
func TranslateRequestWithCodexMultiAgentV2ForExecutor(ctx context.Context, headers http.Header, cfg *config.Config, targetExecutor string, from, to sdktranslator.Format, model string, payload []byte, stream bool) []byte {
	if IsCodexUserAgent(headers) && !isCodexTargetExecutor(targetExecutor) {
		payload = NormalizeCodexToolIntegerTypes(payload, headers)
	}
	return multiagentv2.TranslateRequestWithCodexMultiAgentV2(ctx, headers, cfg, from, to, model, payload, stream)
}

// TranslateRequestEnvelopeWithCodexMultiAgentV2 normalizes official Codex
// multi-agent input while preserving the complete request envelope.
func TranslateRequestEnvelopeWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, req sdktranslator.RequestEnvelope) sdktranslator.RequestEnvelope {
	return multiagentv2.TranslateRequestEnvelopeWithCodexMultiAgentV2(ctx, headers, cfg, from, to, req)
}

// TranslateRequestPairWithCodexMultiAgentV2 translates the untouched baseline
// payload and the working payload that later stages mutate in place. Executors
// normally assign the original payload to the request before translating, so the
// same input is translated once, including plugin hooks, then copied into a
// separate working buffer. This avoids duplicate plugin side effects and a full
// extra pass over payloads that can reach tens of megabytes.
func TranslateRequestPairWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream bool) (original, working []byte) {
	req := sdktranslator.RequestEnvelope{Format: from, Model: model, Stream: stream}
	original, working, _ = TranslateRequestEnvelopePairWithCodexMultiAgentV2(ctx, headers, cfg, from, to, req, originalPayload, requestPayload)
	return original, working
}

// TranslateRequestEnvelopePairWithCodexMultiAgentV2 translates the baseline and
// working payload while preserving request-scoped metadata in req. The returned
// error is the one for the working payload, which is the payload sent upstream;
// a failure of the baseline alone never rejects a working payload that translated.
func TranslateRequestEnvelopePairWithCodexMultiAgentV2(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, req sdktranslator.RequestEnvelope, originalPayload, requestPayload []byte) (original, working []byte, err error) {
	originalReq := req
	originalReq.Body = originalPayload
	originalTranslated := TranslateRequestEnvelopeWithCodexMultiAgentV2(ctx, headers, cfg, from, to, originalReq)
	original = originalTranslated.Body
	if sameByteSlice(originalPayload, requestPayload) {
		// The caller mutates the working copy, so it must not share the baseline array.
		// A single translation serves both payloads, so its error is the working error.
		return original, append([]byte(nil), original...), originalTranslated.Err
	}
	workingReq := req
	workingReq.Body = requestPayload
	workingTranslated := TranslateRequestEnvelopeWithCodexMultiAgentV2(ctx, headers, cfg, from, to, workingReq)
	return original, workingTranslated.Body, workingTranslated.Err
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

// TranslateRequestPairWithAPIKeyModelCompatibility avoids translating identical
// inputs twice while retaining separate buffers and invoking plugins once per input.
func TranslateRequestPairWithAPIKeyModelCompatibility(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream, isCompat bool) (original, working []byte) {
	original, working, _ = translateRequestPairWithAPIKeyModelCompatibility(ctx, headers, cfg, from, to, model, originalPayload, requestPayload, stream, isCompat)
	return original, working
}

// TranslateRequestPairReturningError reports a content part the target cannot represent.
func TranslateRequestPairReturningError(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream, isCompat bool) ([]byte, []byte, error) {
	return translateRequestPairWithAPIKeyModelCompatibility(ctx, headers, cfg, from, to, model, originalPayload, requestPayload, stream, isCompat)
}

func translateRequestPairWithAPIKeyModelCompatibility(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream, isCompat bool) (original, working []byte, err error) {
	original, working, _, err = TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, headers, cfg, from, to, model, originalPayload, requestPayload, stream, isCompat)
	return original, working, err
}

// TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent returns the
// normalizer decision for the working payload, not for the baseline payload,
// and the working payload's error for a content part the target cannot
// represent. A baseline failure alone is ignored.
func TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, originalPayload, requestPayload []byte, stream, isCompat bool) (original, working []byte, updatesChanged bool, err error) {
	original, updatesChanged, err = TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, headers, cfg, from, to, model, originalPayload, stream, isCompat)
	if sameByteSlice(originalPayload, requestPayload) {
		// A single translation serves both payloads, so its error is the working error.
		return original, append([]byte(nil), original...), updatesChanged, err
	}
	// Only the working payload is sent upstream, so only its error counts.
	working, updatesChanged, err = TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx, headers, cfg, from, to, model, requestPayload, stream, isCompat)
	return original, working, updatesChanged, err
}

// TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent returns the
// plugin normalizer's request-scoped update decision for Responses targets.
func TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntent(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) ([]byte, bool, error) {
	return TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor(ctx, headers, cfg, "", from, to, model, payload, stream, isCompat)
}

// TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor
// translates a request while applying client schema compatibility for the
// actual target executor.
func TranslateRequestWithAPIKeyModelCompatibilityAndUpdateIntentForExecutor(ctx context.Context, headers http.Header, cfg *config.Config, targetExecutor string, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) ([]byte, bool, error) {
	if !isCompat || (to == sdktranslator.FormatCodex && from != sdktranslator.FormatClaude) {
		translated := sdktranslator.RequestEnvelope{Format: from, Model: model, Stream: stream, Body: payload}
		if IsCodexUserAgent(headers) && !isCodexTargetExecutor(targetExecutor) {
			translated.Body = NormalizeCodexToolIntegerTypes(translated.Body, headers)
		}
		translated = multiagentv2.TranslateRequestEnvelopeWithCodexMultiAgentV2(ctx, headers, cfg, from, to, translated)
		return translated.Body, translated.ConfigurationUpdatesChanged, translated.Err
	}
	body, err := translateRequestWithAPIKeyModelCompatibilityForExecutor(ctx, headers, cfg, targetExecutor, from, to, model, payload, stream, isCompat)
	return body, false, err
}

// TranslateRequestWithAPIKeyModelCompatibility applies compatibility-aware
// request translators when a configured API-key model enables compatibility mode.
func TranslateRequestWithAPIKeyModelCompatibility(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) []byte {
	return TranslateRequestWithAPIKeyModelCompatibilityForExecutor(ctx, headers, cfg, "", from, to, model, payload, stream, isCompat)
}

// TranslateRequestWithAPIKeyModelCompatibilityForExecutor applies compatibility-aware
// request translation while preserving the actual target executor's schema policy.
func TranslateRequestWithAPIKeyModelCompatibilityForExecutor(ctx context.Context, headers http.Header, cfg *config.Config, targetExecutor string, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) []byte {
	body, _ := translateRequestWithAPIKeyModelCompatibilityForExecutor(ctx, headers, cfg, targetExecutor, from, to, model, payload, stream, isCompat)
	return body
}

// TranslateRequestReturningError reports a content part the target cannot represent.
func TranslateRequestReturningError(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) ([]byte, error) {
	return translateRequestWithAPIKeyModelCompatibilityForExecutor(ctx, headers, cfg, "", from, to, model, payload, stream, isCompat)
}

func translateRequestWithAPIKeyModelCompatibilityForExecutor(ctx context.Context, headers http.Header, cfg *config.Config, targetExecutor string, from, to sdktranslator.Format, model string, payload []byte, stream, isCompat bool) ([]byte, error) {
	if IsCodexUserAgent(headers) && !isCodexTargetExecutor(targetExecutor) {
		payload = NormalizeCodexToolIntegerTypes(payload, headers)
	}
	if !isCompat {
		return translateRequestWithCodexMultiAgentV2Checked(ctx, headers, cfg, from, to, model, payload, stream)
	}
	if from == sdktranslator.FormatOpenAIResponse {
		payload = RewriteCodexOrphanDelegationInput(ctx, headers, payload, cfg)
		if to != sdktranslator.FormatCodex && to != sdktranslator.FormatOpenAIResponse {
			payload = multiagentv2.RewriteCodexMultiAgentV2Input(ctx, headers, payload, cfg, isCompat)
		}
	}

	var translated []byte
	var convertErr error
	switch {
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatCodex:
		translated, convertErr = codexclaude.ConvertClaudeRequestToCodexWithCompat(model, payload, stream)
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatGemini:
		translated, convertErr = geminiclaude.ConvertClaudeRequestToGeminiWithCompat(model, payload, stream)
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatInteractions:
		translated, convertErr = interactionsclaude.ConvertClaudeRequestToInteractionsWithCompat(model, payload, stream)
	case from == sdktranslator.FormatClaude && to == sdktranslator.FormatOpenAI:
		translated, convertErr = openaiclaude.ConvertClaudeRequestToOpenAIWithCompat(model, payload, stream)
	case from == sdktranslator.FormatOpenAI && to == sdktranslator.FormatClaude:
		translated, convertErr = openaichatclaude.ConvertOpenAIRequestToClaudeWithCompat(model, payload, stream)
	case from == sdktranslator.FormatOpenAIResponse && to == sdktranslator.FormatClaude:
		translated, convertErr = responsesclaude.ConvertOpenAIResponsesRequestToClaudeWithCompat(model, payload, stream)
	default:
		return translateRequestWithCodexMultiAgentV2Checked(ctx, headers, cfg, from, to, model, payload, stream)
	}

	summaryConfig := thinking.ExtractTranslatedSummaryConfig(payload, from.String(), to.String())
	translated = thinking.ApplySummaryConfigForModel(translated, to.String(), model, summaryConfig)
	return sdktranslator.NormalizeRequest(ctx, from, to, model, translated, stream), convertErr
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
// request optimization and uses the execution attempt's resolved compatibility
// flag to convert agent_message items into portable message/user input,
// proactively stripping author, recipient, and internal passthrough metadata.
func OptimizeCodexMultiAgentV2RequestForAuth(ctx context.Context, headers http.Header, payload []byte, cfg *config.Config, auth *cliproxyauth.Auth, isCompat bool) ([]byte, bool) {
	if auth != nil && auth.AuthKind() == cliproxyauth.AuthKindAPIKey {
		cfg = cfg.ForAPIKey()
	}
	payload = multiagentv2.RewriteCodexOrphanDelegationInputForConfig(ctx, headers, payload, cfg)
	updated, optimized := multiagentv2.OptimizeCodexMultiAgentV2Request(ctx, headers, payload, cfg)
	if isCompat {
		updated = multiagentv2.RewriteCodexMultiAgentV2Input(ctx, headers, updated, cfg, true)
	}
	return updated, optimized
}

// RestoreCodexMultiAgentV2Response restores optimized collaboration namespace
// values before an upstream response is translated and returned to the client.
func RestoreCodexMultiAgentV2Response(payload []byte, optimized bool) []byte {
	return multiagentv2.RestoreCodexMultiAgentV2Response(payload, optimized)
}

// translateRequestWithCodexMultiAgentV2Checked is the multi-agent translation that
// keeps the envelope failure instead of dropping it with the body.
func translateRequestWithCodexMultiAgentV2Checked(ctx context.Context, headers http.Header, cfg *config.Config, from, to sdktranslator.Format, model string, payload []byte, stream bool) ([]byte, error) {
	translated := multiagentv2.TranslateRequestEnvelopeWithCodexMultiAgentV2(ctx, headers, cfg, from, to, sdktranslator.RequestEnvelope{Format: from, Model: model, Stream: stream, Body: payload})
	return translated.Body, translated.Err
}
