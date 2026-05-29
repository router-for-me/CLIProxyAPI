package signature

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type ClaudeMessagesSignatureSanitizeOptions struct {
	TargetProvider                SignatureProvider
	TargetModel                   string
	DropEmptyMessages             bool
	DropToolSignatures            bool
	DropEmptyThinkingPlaceholders bool
}

type SignatureSanitizeReport struct {
	TargetProvider     SignatureProvider
	Preserved          int
	DroppedBlocks      int
	DroppedSignatures  int
	ReplacedSignatures int
	Decisions          []SignatureCompatibilityDecision
}

// SanitizeClaudeMessagesSignaturesForModel removes or preserves Claude
// /v1/messages signed history according to the provider family implied by
// targetModel.
func SanitizeClaudeMessagesSignaturesForModel(payload []byte, targetModel string) ([]byte, SignatureSanitizeReport) {
	return SanitizeClaudeMessagesSignaturesForTarget(payload, ClaudeMessagesSignatureSanitizeOptions{
		TargetProvider:    SignatureProviderFromModelName(targetModel),
		TargetModel:       targetModel,
		DropEmptyMessages: true,
	})
}

// SanitizeClaudeMessagesForClaudeUpstream prepares a Claude /v1/messages body
// for native Claude upstreams. Invalid thinking blocks are dropped, valid
// thinking signatures are normalized to Claude provider-native E-form, and
// tool_use blocks keep only their tool-call payload.
func SanitizeClaudeMessagesForClaudeUpstream(payload []byte, targetModel string) ([]byte, SignatureSanitizeReport) {
	return SanitizeClaudeMessagesSignaturesForTarget(payload, ClaudeMessagesSignatureSanitizeOptions{
		TargetProvider:                SignatureProviderClaude,
		TargetModel:                   targetModel,
		DropEmptyMessages:             true,
		DropToolSignatures:            true,
		DropEmptyThinkingPlaceholders: true,
	})
}

// SanitizeClaudeMessagesSignaturesForTarget applies provider-aware signature
// compatibility rules to Claude /v1/messages history. Compatible thinking
// signatures are preserved. Incompatible thinking blocks are removed so a user
// can continue a conversation after switching between Claude, GPT/Codex,
// and Gemini models.
func SanitizeClaudeMessagesSignaturesForTarget(payload []byte, opts ClaudeMessagesSignatureSanitizeOptions) ([]byte, SignatureSanitizeReport) {
	targetProvider := normalizeSignatureTargetProvider(opts.TargetProvider)
	if targetProvider == SignatureProviderUnknown && opts.TargetModel != "" {
		targetProvider = SignatureProviderFromModelName(opts.TargetModel)
	}
	report := SignatureSanitizeReport{TargetProvider: targetProvider}

	messages := gjson.GetBytes(payload, "messages")
	if !messages.IsArray() {
		return payload, report
	}

	messageResults := messages.Array()
	keptMessages := make([]string, 0, len(messageResults))
	modified := false

	for i, message := range messageResults {
		content := message.Get("content")
		if !content.IsArray() {
			keptMessages = append(keptMessages, message.Raw)
			continue
		}

		contentResults := content.Array()
		keptParts := make([]string, 0, len(contentResults))
		messageModified := false

		for j, part := range contentResults {
			partType := part.Get("type").String()
			if partType == "tool_use" {
				if opts.DropToolSignatures {
					updatedPart, changed := stripClaudeToolUseSignatureFields(part)
					if changed {
						messageModified = true
						report.DroppedSignatures++
					}
					keptParts = append(keptParts, updatedPart)
					continue
				}
				updatedPart, changed, decisions := sanitizeClaudeToolUseSignature(part, targetProvider, i, j)
				report.Decisions = append(report.Decisions, decisions...)
				if changed {
					messageModified = true
				}
				for _, decision := range decisions {
					switch decision.Action {
					case SignatureActionPreserve:
						report.Preserved++
					case SignatureActionReplaceWithGeminiBypass:
						report.ReplacedSignatures++
					default:
						report.DroppedSignatures++
					}
				}
				keptParts = append(keptParts, updatedPart)
				continue
			}

			if partType != "thinking" {
				keptParts = append(keptParts, part.Raw)
				continue
			}

			if targetProvider == SignatureProviderClaude && isEmptyClaudeThinkingPlaceholder(part) && !opts.DropEmptyThinkingPlaceholders {
				keptParts = append(keptParts, part.Raw)
				continue
			}

			rawSignature := part.Get("signature").String()
			decision := DecideSignatureCompatibility(targetProvider, rawSignature, SignatureBlockKindClaudeThinking)
			decision.Reason = fmt.Sprintf("messages[%d].content[%d]: %s", i, j, decision.Reason)
			report.Decisions = append(report.Decisions, decision)

			switch decision.Action {
			case SignatureActionPreserve:
				report.Preserved++
				if decision.NormalizedSignature != "" && decision.NormalizedSignature != rawSignature {
					updated, _ := sjson.Set(part.Raw, "signature", decision.NormalizedSignature)
					keptParts = append(keptParts, updated)
					messageModified = true
					continue
				}
				keptParts = append(keptParts, part.Raw)
			case SignatureActionReplaceWithGeminiBypass:
				report.ReplacedSignatures++
				updated, _ := sjson.Set(part.Raw, "signature", decision.ReplacementSignature)
				keptParts = append(keptParts, updated)
				messageModified = true
			case SignatureActionDropSignature:
				report.DroppedSignatures++
				updated, _ := sjson.Delete(part.Raw, "signature")
				keptParts = append(keptParts, updated)
				messageModified = true
			default:
				report.DroppedBlocks++
				messageModified = true
			}
		}

		if messageModified {
			modified = true
			if len(keptParts) == 0 && opts.DropEmptyMessages {
				continue
			}
			updated, _ := sjson.SetRaw(message.Raw, "content", "["+strings.Join(keptParts, ",")+"]")
			keptMessages = append(keptMessages, updated)
			continue
		}

		keptMessages = append(keptMessages, message.Raw)
	}

	if !modified {
		return payload, report
	}
	output, _ := sjson.SetRawBytes(payload, "messages", []byte("["+strings.Join(keptMessages, ",")+"]"))
	return output, report
}

func stripClaudeToolUseSignatureFields(part gjson.Result) (string, bool) {
	updated := part.Raw
	changed := false
	for _, sigPath := range claudeToolUseProvenancePaths() {
		if !gjson.Get(updated, sigPath).Exists() {
			continue
		}
		updated, _ = sjson.Delete(updated, sigPath)
		changed = true
	}
	if cleaned, ok := deleteEmptyJSONObjectPath(updated, "extra_content.google"); ok {
		updated = cleaned
		changed = true
	}
	if cleaned, ok := deleteEmptyJSONObjectPath(updated, "extra_content"); ok {
		updated = cleaned
		changed = true
	}
	return updated, changed
}

func sanitizeClaudeToolUseSignature(part gjson.Result, targetProvider SignatureProvider, messageIdx, partIdx int) (string, bool, []SignatureCompatibilityDecision) {
	updated := part.Raw
	changed := false
	var decisions []SignatureCompatibilityDecision

	for _, sigPath := range claudeToolUseSignaturePaths() {
		sigResult := part.Get(sigPath)
		if !sigResult.Exists() {
			continue
		}

		blockKind := SignatureBlockKindGeminiFunctionCall
		if targetProvider == SignatureProviderClaude {
			blockKind = SignatureBlockKindClaudeThinking
		} else if targetProvider == SignatureProviderGPT {
			blockKind = SignatureBlockKindGPTReasoning
		}
		decision := DecideSignatureCompatibility(targetProvider, sigResult.String(), blockKind)
		decision.Reason = fmt.Sprintf("messages[%d].content[%d].%s: %s", messageIdx, partIdx, sigPath, decision.Reason)
		decisions = append(decisions, decision)

		switch decision.Action {
		case SignatureActionPreserve:
			if decision.NormalizedSignature != "" && decision.NormalizedSignature != sigResult.String() {
				updated, _ = sjson.Set(updated, sigPath, decision.NormalizedSignature)
				changed = true
			}
		case SignatureActionReplaceWithGeminiBypass:
			updated, _ = sjson.Set(updated, sigPath, decision.ReplacementSignature)
			changed = true
		default:
			updated, _ = sjson.Delete(updated, sigPath)
			changed = true
		}
	}

	if cleaned, ok := deleteEmptyJSONObjectPath(updated, "extra_content.google"); ok {
		updated = cleaned
		changed = true
	}
	if cleaned, ok := deleteEmptyJSONObjectPath(updated, "extra_content"); ok {
		updated = cleaned
		changed = true
	}

	return updated, changed, decisions
}

func claudeToolUseSignaturePaths() []string {
	return []string{
		"signature",
		"thoughtSignature",
		"thought_signature",
		"extra_content.google.thought_signature",
	}
}

func claudeToolUseProvenancePaths() []string {
	return append(claudeToolUseSignaturePaths(), "model")
}

func deleteEmptyJSONObjectPath(raw, path string) (string, bool) {
	result := gjson.Get(raw, path)
	if !result.Exists() || !result.IsObject() || len(result.Map()) != 0 {
		return raw, false
	}
	updated, err := sjson.Delete(raw, path)
	if err != nil {
		return raw, false
	}
	return updated, true
}
