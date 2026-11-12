package executor

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func openaiResponsesReasoningSummaryIsEmpty(summary gjson.Result) bool {
	if !summary.Exists() || summary.Type == gjson.Null {
		return true
	}
	return summary.IsArray() && len(summary.Array()) == 0
}

func promoteOpenAIResponsesReasoningTextToSummary(itemRaw string, content gjson.Result) (string, error) {
	var b strings.Builder
	b.WriteByte('[')
	n := 0
	for _, part := range content.Array() {
		if strings.TrimSpace(part.Get("type").String()) != "reasoning_text" {
			continue
		}
		text := part.Get("text").String()
		if text == "" {
			continue
		}
		partJSON, err := sjson.Set(`{"type":"summary_text"}`, "text", text)
		if err != nil {
			return itemRaw, err
		}
		if n > 0 {
			b.WriteByte(',')
		}
		b.WriteString(partJSON)
		n++
	}
	b.WriteByte(']')
	if n == 0 {
		return itemRaw, nil
	}
	return sjson.SetRaw(itemRaw, "summary", b.String())
}

type metaSignaturePolicy int

const (
	// metaSignatureForeign: target is not Meta; a recognized Meta envelope is foreign.
	metaSignatureForeign metaSignaturePolicy = iota
	// metaSignatureNativeOnly: native Meta upstream (api.meta.ai); only unsuffixed envelopes replay.
	metaSignatureNativeOnly
	// metaSignatureAllowRouted: Meta-family model served by a router (e.g. OpenRouter meta/muse-*); native or router-suffixed envelopes replay verbatim.
	metaSignatureAllowRouted
)

func sanitizeOpenAIResponsesReasoningEncryptedContent(ctx context.Context, provider string, body []byte) []byte {
	return sanitizeOpenAIResponsesReasoningEncryptedContentWithOptions(ctx, provider, body, false, false, metaSignatureForeign, signature.SignatureProviderUnknown, false)
}

func sanitizeOpenAIResponsesReasoningEncryptedContentWithCompat(ctx context.Context, provider string, body []byte, isCompat bool) []byte {
	return sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModel(ctx, provider, body, isCompat, "")
}

// sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModel is the
// compat variant that knows its target model: a positively recognized Meta
// Muse envelope is preserved whenever the upstream actually serves a
// Meta-family model, whether or not the route is flagged compat (native
// OpenAI never serves muse-* models, but a router key such as an OpenRouter
// codex endpoint can serve meta/muse-* through the Codex executor without
// isCompat). Router-suffixed envelopes replay verbatim there; the native
// Meta executor keeps its own stricter native-only gate. For any other
// target the recognized Meta envelope is known-foreign and stripped like
// other providers'.
// resolveResponsesSignatureTargetModel returns the canonical model name to use
// for reasoning signature target checks, falling back to the client-requested
// model when an upstream alias mapping hides the Meta Muse family name.
func resolveResponsesSignatureTargetModel(baseModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	if signature.SignatureProviderFromModelName(baseModel) != signature.SignatureProviderUnknown {
		return baseModel
	}
	requested := thinking.ParseSuffix(helps.PayloadRequestedModel(opts, req.Model)).ModelName
	switch signature.SignatureProviderFromModelName(requested) {
	case signature.SignatureProviderMeta, signature.SignatureProviderGrok, signature.SignatureProviderGPT:
		return requested
	}
	return baseModel
}

func resolveMetaSignatureTargetModel(baseModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) string {
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	if signature.SignatureProviderFromModelName(targetModel) == signature.SignatureProviderMeta {
		return targetModel
	}
	return "muse-" + baseModel
}

func sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModel(ctx context.Context, provider string, body []byte, isCompat bool, targetModel string) []byte {
	return sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(ctx, provider, body, isCompat, targetModel, "")
}

func sanitizeOpenAIResponsesReasoningEncryptedContentWithCompatModelAndBaseURL(ctx context.Context, provider string, body []byte, isCompat bool, targetModel string, baseURL string) []byte {
	targetProvider := signature.SignatureProviderFromModelName(targetModel)
	metaPolicy := metaSignatureForeign
	if targetProvider == signature.SignatureProviderMeta {
		if strings.TrimSpace(baseURL) != "" && isNativeMetaUpstreamURL(baseURL) {
			metaPolicy = metaSignatureNativeOnly
		} else {
			metaPolicy = metaSignatureAllowRouted
		}
	}
	return sanitizeOpenAIResponsesReasoningEncryptedContentWithOptions(ctx, provider, body, isCompat, isCompat, metaPolicy, targetProvider, isOpenRouterUpstreamURL(baseURL))
}

func sanitizeOpenAIResponsesReasoningEncryptedContentKeepForeign(ctx context.Context, provider string, body []byte) []byte {
	return sanitizeOpenAIResponsesReasoningEncryptedContentWithOptions(ctx, provider, body, false, true, metaSignatureForeign, signature.SignatureProviderUnknown, false)
}

// sanitizeOpenAIResponsesReasoningEncryptedContentForMeta is the Meta-target
// variant and is deliberately native-only: only positively recognized Meta
// Muse envelopes without a router suffix may replay to api.meta.ai, whose
// suffix acceptance is unverified. GPT, other known-provider,
// unknown-format, and router-suffixed values are all stripped, so a wrong
// signature can never reach the Meta upstream through this path. A future
// Meta envelope revision will be stripped until the inspector learns it
// (fail-closed).
func sanitizeOpenAIResponsesReasoningEncryptedContentForMeta(ctx context.Context, provider string, body []byte) []byte {
	return sanitizeOpenAIResponsesReasoningEncryptedContentWithOptions(ctx, provider, body, false, true, metaSignatureNativeOnly, signature.SignatureProviderMeta, false)
}

// sanitizeOpenAIResponsesReasoningEncryptedContentForMetaBaseURL gates Meta
// reasoning signatures by whether baseURL is the native Meta upstream
// (api.meta.ai, which accepts only unsuffixed envelopes) or a third-party
// Responses provider configured under meta-api-key (which may emit and consume
// router-suffixed Meta Muse envelopes verbatim when targeting OpenRouter, or
// strip the OpenRouter suffix for other providers).
func sanitizeOpenAIResponsesReasoningEncryptedContentForMetaBaseURL(ctx context.Context, provider string, body []byte, baseURL string) []byte {
	if isNativeMetaUpstreamURL(baseURL) {
		return sanitizeOpenAIResponsesReasoningEncryptedContentForMeta(ctx, provider, body)
	}
	return sanitizeOpenAIResponsesReasoningEncryptedContentWithOptions(ctx, provider, body, false, true, metaSignatureAllowRouted, signature.SignatureProviderMeta, isOpenRouterUpstreamURL(baseURL))
}

func isNativeMetaUpstreamURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return true
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return true
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return host == "" || host == "api.meta.ai" || strings.HasSuffix(host, ".meta.ai")
}

func isOpenRouterUpstreamURL(rawURL string) bool {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	return host == "openrouter.ai" || strings.HasSuffix(host, ".openrouter.ai")
}

func stripRouterSuffix(rawSignature, suffix string) string {
	if suffix == "" || len(rawSignature) <= len(suffix)+1 {
		return rawSignature
	}
	return rawSignature[:len(rawSignature)-len(suffix)-1]
}

func sanitizeOpenAIResponsesReasoningEncryptedContentWithOptions(ctx context.Context, provider string, body []byte, isCompat bool, keepForeign bool, metaPolicy metaSignaturePolicy, targetProvider signature.SignatureProvider, allowRouterSuffix bool) []byte {
	inputResult := util.GetGJSONBytesNoCopy(body, "input")
	if !inputResult.Exists() || !inputResult.IsArray() {
		return body
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "openai responses upstream"
	}

	// Codex backend rejects store=true and does not persist items when store=false.
	// A reasoning item that still carries an id without usable encrypted_content is
	// treated as a store lookup and returns:
	//   Item with id '...' not found. Items are not persisted when `store` is set to false.
	// Strip those orphan ids unless the request explicitly opts into store=true.
	stripOrphanReasoningIDs := !gjson.GetBytes(body, "store").Bool()

	items := inputResult.Array()

	// rebuilt accumulates the edited "input" array as JSON array bytes. It
	// stays nil while no item needs editing so the common case (nothing to
	// sanitize) does no allocation or rebuilding. Edits are applied directly
	// to each item's own raw JSON rather than re-parsing the whole body,
	// keeping the cost proportional to the item being edited.
	var rebuilt []byte
	itemsWritten := 0
	keep := func(raw string) {
		if rebuilt == nil {
			return
		}
		if itemsWritten > 0 {
			rebuilt = append(rebuilt, ',')
		}
		rebuilt = append(rebuilt, raw...)
		itemsWritten++
	}
	startRebuild := func(index int) {
		if rebuilt != nil {
			return
		}
		// First item that needs editing: start the buffer and backfill
		// it with the raw JSON of every preceding item.
		rebuilt = make([]byte, 0, len(inputResult.Raw))
		rebuilt = append(rebuilt, '[')
		for i := range index {
			keep(items[i].Raw)
		}
	}

	for index, item := range items {
		if strings.TrimSpace(item.Get("type").String()) != "reasoning" {
			keep(item.Raw)
			continue
		}

		encryptedContent := item.Get("encrypted_content")
		itemID := strings.TrimSpace(item.Get("id").String())
		if itemID == "" {
			itemID = fmt.Sprintf("input[%d]", index)
		}

		nextItem := item.Raw
		changed := false

		// Official Codex schema sets maxItems: 0 on reasoning.content. Third-party
		// channels replay cleartext thinking there; promote it into summary when
		// summary is empty, then force content to [].
		// When isCompat is true, third-party Responses models (such as DeepSeek)
		// require original reasoning_text inside reasoning.content to be replayed.
		content := item.Get("content")
		if !isCompat && content.IsArray() && len(content.Array()) > 0 {
			if openaiResponsesReasoningSummaryIsEmpty(item.Get("summary")) {
				promoted, errPromote := promoteOpenAIResponsesReasoningTextToSummary(nextItem, content)
				if errPromote != nil {
					helps.LogWithRequestID(ctx).Debugf("%s: failed to promote reasoning_text into summary at input[%d]: %v", provider, index, errPromote)
				} else {
					nextItem = promoted
				}
			}
			cleared, errClear := sjson.SetRaw(nextItem, "content", "[]")
			if errClear != nil {
				helps.LogWithRequestID(ctx).Debugf("%s: failed to clear reasoning content at input[%d]: %v", provider, index, errClear)
			} else {
				nextItem = cleared
				changed = true
				helps.LogWithRequestID(ctx).Debugf("%s: cleared reasoning content at input[%d] item_id=%q", provider, index, itemID)
			}
		}

		if !encryptedContent.Exists() {
			if !isCompat && stripOrphanReasoningIDs && item.Get("id").Exists() {
				dropped, err := sjson.Delete(nextItem, "id")
				if err != nil {
					helps.LogWithRequestID(ctx).Debugf("%s: failed to drop orphan reasoning id at input[%d]: %v", provider, index, err)
				} else {
					nextItem = dropped
					changed = true
					helps.LogWithRequestID(ctx).Debugf("%s: dropped orphan reasoning id at input[%d] item_id=%q reason=missing encrypted_content with store disabled", provider, index, itemID)
				}
			}
			if !changed {
				keep(item.Raw)
				continue
			}
			startRebuild(index)
			keep(nextItem)
			continue
		}

		reason := ""
		rewrittenSignature := ""
		switch encryptedContent.Type {
		case gjson.String:
			rawSignature := encryptedContent.String()
			if rawSignature != strings.TrimSpace(rawSignature) {
				reason = "encrypted_content has leading or trailing whitespace"
			} else if metaPolicy != metaSignatureForeign {
				// Meta targets never reach the GPT-first gate: a GPT,
				// foreign-provider, or unknown-format blob must not replay to
				// Meta. Only a positively recognized Meta envelope survives,
				// and the native Meta upstream additionally refuses the
				// router suffix a router such as OpenRouter appends.
				if info, err := signature.InspectMetaReasoningSignature(rawSignature); err != nil {
					reason = err.Error()
				} else if info.RouterSuffix != "" {
					if metaPolicy == metaSignatureNativeOnly {
						reason = "router-suffixed Meta reasoning signature is not accepted by the native Meta upstream"
					} else if !allowRouterSuffix {
						rewrittenSignature = stripRouterSuffix(rawSignature, info.RouterSuffix)
					}
				}
			} else if targetProvider == signature.SignatureProviderUnknown || targetProvider == signature.SignatureProviderGPT {
				candidate := rawSignature
				hasGPTPrefix := false
				if prefixedProvider, unprefixed, ok := signature.SplitSignatureProviderPrefix(rawSignature); ok && prefixedProvider == signature.SignatureProviderGPT {
					candidate = unprefixed
					hasGPTPrefix = true
				}
				if gptInfo, err := signature.InspectGPTReasoningSignature(candidate); err == nil {
					if gptInfo.RouterSuffix != "" && !allowRouterSuffix {
						rewrittenSignature = stripRouterSuffix(candidate, gptInfo.RouterSuffix)
					} else if hasGPTPrefix {
						rewrittenSignature = candidate
					}
				} else if !(keepForeign && rawSignature != "" && signature.DetectSignatureProvider(rawSignature) == signature.SignatureProviderUnknown) {
					reason = err.Error()
				}
			} else {
				// Target model belongs to a known non-GPT, non-Meta family (such as
				// Grok or Gemini served through a Responses router like OpenRouter).
				// Only preserve signatures that match that target family (or
				// unknown-format blobs when keepForeign is enabled); foreign GPT or
				// other known-provider envelopes are stripped.
				keep := false
				if rawSignature != "" {
					switch targetProvider {
					case signature.SignatureProviderGrok:
						if grokInfo, errGrok := signature.InspectGrokEncryptedContent(rawSignature); errGrok == nil {
							keep = true
							if grokInfo.RouterSuffix != "" && !allowRouterSuffix {
								rewrittenSignature = stripRouterSuffix(rawSignature, grokInfo.RouterSuffix)
							}
						}
					case signature.SignatureProviderGemini, signature.SignatureProviderClaude, signature.SignatureProviderKimi, signature.SignatureProviderSWE:
						var normalized string
						normalized, keep = signature.CompatibleSignatureForProvider(targetProvider, rawSignature)
						if keep && normalized != "" && normalized != rawSignature {
							rewrittenSignature = normalized
						}
					}
					if !keep && keepForeign {
						keep = signature.DetectSignatureProvider(rawSignature) == signature.SignatureProviderUnknown
					}
				}
				if !keep {
					reason = fmt.Sprintf("reasoning encrypted_content is not compatible with target provider %s", targetProvider)
				}
			}
		case gjson.Null:
			reason = "encrypted_content is null"
		default:
			reason = fmt.Sprintf("encrypted_content must be a string, got %s", encryptedContent.Type.String())
		}
		if reason == "" {
			if rewrittenSignature != "" {
				if updatedItem, errSet := sjson.Set(nextItem, "encrypted_content", rewrittenSignature); errSet == nil {
					nextItem = updatedItem
					changed = true
					helps.LogWithRequestID(ctx).Debugf("%s: stripped router suffix from reasoning encrypted_content at input[%d] item_id=%q", provider, index, itemID)
				}
			}
			if !changed {
				keep(item.Raw)
				continue
			}
			startRebuild(index)
			keep(nextItem)
			continue
		}

		dropped, err := sjson.Delete(nextItem, "encrypted_content")
		if err != nil {
			helps.LogWithRequestID(ctx).Debugf("%s: failed to drop invalid reasoning encrypted_content at input[%d]: %v", provider, index, err)
			if !changed {
				keep(item.Raw)
				continue
			}
			startRebuild(index)
			keep(nextItem)
			continue
		}
		nextItem = dropped
		changed = true
		if !isCompat && stripOrphanReasoningIDs && item.Get("id").Exists() {
			if nextID, errID := sjson.Delete(nextItem, "id"); errID != nil {
				helps.LogWithRequestID(ctx).Debugf("%s: failed to drop reasoning id after invalid encrypted_content at input[%d]: %v", provider, index, errID)
			} else {
				nextItem = nextID
			}
		}

		startRebuild(index)
		keep(nextItem)

		helps.LogWithRequestID(ctx).Debugf("%s: dropped invalid reasoning encrypted_content at input[%d] item_id=%q reason=%s", provider, index, itemID, reason)
	}

	if rebuilt == nil {
		return body
	}
	rebuilt = append(rebuilt, ']')

	updated, err := sjson.SetRawBytes(body, "input", rebuilt)
	if err != nil {
		helps.LogWithRequestID(ctx).Debugf("%s: failed to rebuild input array while sanitizing reasoning encrypted_content: %v", provider, err)
		return body
	}
	return updated
}
