// Claude thinking signature validation wrappers for Antigravity bypass mode.
package claude

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
)

const (
	maxBypassSignatureLen = signature.MaxClaudeThinkingSignatureLen

	// Gemini carrier envelopes exist only on the Claude-facing wire. The request
	// translator validates and unwraps them before writing native Gemini parts.
	geminiClaudeCarrierPrefix     = translatorcommon.GeminiClaudeCarrierPrefix
	geminiClaudeCarrierNext       = translatorcommon.GeminiClaudeCarrierNext
	geminiClaudeCarrierPrevious   = translatorcommon.GeminiClaudeCarrierPrevious
	geminiClaudeCarrierStandalone = translatorcommon.GeminiClaudeCarrierStandalone
	geminiClaudeCarrierText       = translatorcommon.GeminiClaudeCarrierText
	geminiClaudeCarrierFunction   = translatorcommon.GeminiClaudeCarrierFunction
	geminiClaudeCarrierAny        = translatorcommon.GeminiClaudeCarrierAny
)

type claudeSignatureTree = signature.ClaudeSignatureTree

func encodeGeminiClaudeCarrierSignature(rawSignature, direction, targetKind string) string {
	return translatorcommon.EncodeGeminiClaudeCarrierSignature(rawSignature, direction, targetKind)
}

func decodeGeminiClaudeCarrierSignature(rawSignature string) (signatureValue, direction, targetKind string, marked, ok bool) {
	return translatorcommon.DecodeGeminiClaudeCarrierSignature(rawSignature)
}

func geminiClaudeSemanticTargetKind(block ojson.Result) string {
	switch block.Get("type").String() {
	case "text":
		return geminiClaudeCarrierText
	case "tool_use":
		return geminiClaudeCarrierFunction
	case "thinking":
		if strings.TrimSpace(block.Get("thinking").String()) != "" {
			return geminiClaudeCarrierText
		}
	}
	return ""
}

func geminiClaudeCarrierMatchesAdjacent(blocks []ojson.Result, index int, direction, targetKind string) bool {
	step := 1
	if direction == geminiClaudeCarrierPrevious {
		step = -1
	}
	for adjacent := index + step; adjacent >= 0 && adjacent < len(blocks); adjacent += step {
		if kind := geminiClaudeSemanticTargetKind(blocks[adjacent]); kind != "" {
			return targetKind == geminiClaudeCarrierAny || targetKind == kind
		}
		if blocks[adjacent].Get("type").String() != "thinking" || strings.TrimSpace(blocks[adjacent].Get("thinking").String()) != "" {
			return false
		}
	}
	return false
}

func geminiClaudeIsValidNonEmptyThinking(rawSignature string, nextSemanticKind string) bool {
	if rawSignature == "" {
		return false
	}
	innerSig, direction, targetKind, marked, okCarrier := decodeGeminiClaudeCarrierSignature(rawSignature)
	blockKind := signature.SignatureBlockKindGeminiModelPart
	if marked && targetKind == geminiClaudeCarrierFunction {
		blockKind = signature.SignatureBlockKindGeminiFunctionCall
	}
	if okCarrier {
		if _, ok := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, innerSig, blockKind); !ok {
			return false
		}
		if marked {
			if direction == geminiClaudeCarrierPrevious {
				return false
			}
			if direction == geminiClaudeCarrierStandalone && targetKind == geminiClaudeCarrierFunction {
				return false
			}
			if direction == geminiClaudeCarrierNext {
				if nextSemanticKind == "" || (targetKind != geminiClaudeCarrierAny && targetKind != nextSemanticKind) {
					return false
				}
			}
		}
		return true
	}
	_, ok := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, rawSignature, signature.SignatureBlockKindGeminiModelPart)
	return ok
}

type geminiClaudeCarrierContext struct {
	nextSemanticKind           []string
	hasTrailingPreviousCarrier []bool
}

func geminiClaudePrecomputeCarrierContext(blocks []ojson.Result) geminiClaudeCarrierContext {
	n := len(blocks)
	ctx := geminiClaudeCarrierContext{
		nextSemanticKind:           make([]string, n),
		hasTrailingPreviousCarrier: make([]bool, n),
	}
	if n == 0 {
		return ctx
	}

	activePreviousCarrierTargetKind := ""
	activePreviousCarrierValid := false
	latestSemanticIndex := -1
	currentNextSemanticKind := ""

	for i := n - 1; i >= 0; i-- {
		block := blocks[i]
		blockType := block.Get("type").String()
		ctx.nextSemanticKind[i] = currentNextSemanticKind
		switch blockType {
		case "thinking":
			thinkingText := strings.TrimSpace(block.Get("thinking").String())
			rawSig := strings.TrimSpace(block.Get("signature").String())
			if thinkingText == "" {
				innerSig, direction, targetKind, marked, okCarrier := decodeGeminiClaudeCarrierSignature(rawSig)
				if okCarrier && marked && direction == geminiClaudeCarrierPrevious {
					blockKind := signature.SignatureBlockKindGeminiModelPart
					if targetKind == geminiClaudeCarrierFunction {
						blockKind = signature.SignatureBlockKindGeminiFunctionCall
					}
					if _, ok := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, innerSig, blockKind); ok {
						activePreviousCarrierTargetKind = targetKind
						activePreviousCarrierValid = true
						continue
					}
				}
				activePreviousCarrierTargetKind = ""
				activePreviousCarrierValid = false
				continue
			}

			if rawSig != "" && !geminiClaudeIsValidNonEmptyThinking(rawSig, currentNextSemanticKind) {
				activePreviousCarrierTargetKind = ""
				activePreviousCarrierValid = false
				latestSemanticIndex = -1
				currentNextSemanticKind = ""
				continue
			}

			currentNextSemanticKind = geminiClaudeCarrierText
			if activePreviousCarrierValid && (activePreviousCarrierTargetKind == geminiClaudeCarrierAny || activePreviousCarrierTargetKind == geminiClaudeCarrierText) {
				ctx.hasTrailingPreviousCarrier[i] = true
			} else if latestSemanticIndex != -1 && ctx.hasTrailingPreviousCarrier[latestSemanticIndex] {
				ctx.hasTrailingPreviousCarrier[i] = true
			}
			activePreviousCarrierTargetKind = ""
			activePreviousCarrierValid = false
			latestSemanticIndex = i

		case "text", "tool_use":
			semanticKind := geminiClaudeCarrierText
			if blockType == "tool_use" {
				semanticKind = geminiClaudeCarrierFunction
			}
			currentNextSemanticKind = semanticKind
			if activePreviousCarrierValid && (activePreviousCarrierTargetKind == geminiClaudeCarrierAny || activePreviousCarrierTargetKind == semanticKind) {
				ctx.hasTrailingPreviousCarrier[i] = true
			}
			activePreviousCarrierTargetKind = ""
			activePreviousCarrierValid = false
			latestSemanticIndex = i

		default:
			activePreviousCarrierTargetKind = ""
			activePreviousCarrierValid = false
			latestSemanticIndex = -1
			currentNextSemanticKind = ""
		}
	}
	return ctx
}

// StripEmptySignatureThinkingBlocks removes thinking blocks whose signatures
// are empty or not valid Claude thinking signatures. These usually come from
// proxy-generated responses where no real Claude signature exists.
func StripEmptySignatureThinkingBlocks(payload []byte) []byte {
	return signature.StripInvalidClaudeThinkingBlocks(payload, signature.ClaudeSignatureValidationOptions{PrefixOnly: true})
}

// StripInvalidGeminiSignatureThinkingBlocks preserves only thinking carriers
// whose signatures can be replayed to Gemini. Claude Code uses these carriers
// to return provider-native signatures from prior translated responses.
func StripInvalidGeminiSignatureThinkingBlocks(payload []byte) []byte {
	messages := ojson.GetBytesNoCopy(payload, "messages")
	if !messages.IsArray() {
		return payload
	}
	changed := false
	messageItems := make([][]byte, 0, len(messages.Array()))
	for _, message := range messages.Array() {
		messageJSON := []byte(message.Raw)
		content := message.Get("content")
		if !content.IsArray() {
			messageItems = append(messageItems, messageJSON)
			continue
		}
		contentChanged := false
		assistantMessage := strings.EqualFold(message.Get("role").String(), "assistant")
		contentBlocks := content.Array()
		carrierCtx := geminiClaudePrecomputeCarrierContext(contentBlocks)
		contentItems := make([][]byte, 0, len(contentBlocks))
		pendingCarrierTargetKind := ""
		currentPrevSemanticKind := ""
		for blockIndex, block := range contentBlocks {
			blockType := block.Get("type").String()
			if blockType == "thinking" {
				rawSignature := strings.TrimSpace(block.Get("signature").String())
				thinkingText := strings.TrimSpace(block.Get("thinking").String())
				if assistantMessage && rawSignature == "" && thinkingText != "" && (pendingCarrierTargetKind == geminiClaudeCarrierAny || pendingCarrierTargetKind == geminiClaudeCarrierText || carrierCtx.hasTrailingPreviousCarrier[blockIndex]) {
					pendingCarrierTargetKind = ""
					currentPrevSemanticKind = geminiClaudeCarrierText
					contentItems = append(contentItems, []byte(block.Raw))
					continue
				}
				innerSignature, direction, targetKind, marked, okCarrier := decodeGeminiClaudeCarrierSignature(rawSignature)
				blockKind := signature.SignatureBlockKindGeminiModelPart
				if marked && targetKind == geminiClaudeCarrierFunction {
					blockKind = signature.SignatureBlockKindGeminiFunctionCall
				}
				invalidMarkedPlacement := false
				if marked {
					switch direction {
					case geminiClaudeCarrierNext:
						nextKind := carrierCtx.nextSemanticKind[blockIndex]
						invalidMarkedPlacement = nextKind == "" || (targetKind != geminiClaudeCarrierAny && targetKind != nextKind)
					case geminiClaudeCarrierPrevious:
						invalidMarkedPlacement = currentPrevSemanticKind == "" || (targetKind != geminiClaudeCarrierAny && targetKind != currentPrevSemanticKind)
					case geminiClaudeCarrierStandalone:
						invalidMarkedPlacement = thinkingText != "" && targetKind == geminiClaudeCarrierFunction
					}
					if thinkingText != "" && direction == geminiClaudeCarrierPrevious {
						invalidMarkedPlacement = true
					}
				}
				if !okCarrier || !assistantMessage || invalidMarkedPlacement {
					pendingCarrierTargetKind = ""
					if thinkingText != "" {
						currentPrevSemanticKind = ""
					}
					contentChanged = true
					continue
				}
				if !marked {
					innerSignature = rawSignature
				}
				if _, ok := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, innerSignature, blockKind); !ok {
					pendingCarrierTargetKind = ""
					if thinkingText != "" {
						currentPrevSemanticKind = ""
					}
					contentChanged = true
					continue
				}
				if marked && direction == geminiClaudeCarrierNext {
					pendingCarrierTargetKind = targetKind
				} else {
					pendingCarrierTargetKind = ""
				}
				if thinkingText != "" {
					currentPrevSemanticKind = geminiClaudeCarrierText
				}
			} else {
				pendingCarrierTargetKind = ""
				if blockType == "tool_use" {
					currentPrevSemanticKind = geminiClaudeCarrierFunction
				} else if blockType == "text" {
					currentPrevSemanticKind = geminiClaudeCarrierText
				} else {
					currentPrevSemanticKind = ""
				}
			}
			contentItems = append(contentItems, []byte(block.Raw))
		}
		if contentChanged {
			messageJSON, _ = ojson.SetRawBytes(messageJSON, "content", translatorcommon.JoinRawArray(contentItems))
			changed = true
		}
		messageItems = append(messageItems, messageJSON)
	}
	if !changed {
		return payload
	}
	updated, errSet := ojson.SetRawBytes(payload, "messages", translatorcommon.JoinRawArray(messageItems))
	if errSet != nil {
		return payload
	}
	return updated
}

func StripInvalidBypassSignatureThinkingBlocks(payload []byte) []byte {
	return signature.StripInvalidClaudeThinkingBlocks(payload, claudeBypassSignatureValidationOptions())
}

func ValidateClaudeBypassSignatures(inputRawJSON []byte) error {
	return signature.ValidateClaudeThinkingSignatures(inputRawJSON, claudeBypassSignatureValidationOptions())
}

func normalizeClaudeBypassSignature(rawSignature string) (string, error) {
	return signature.NormalizeClaudeThinkingSignature(rawSignature, claudeBypassSignatureValidationOptions())
}

func inspectDoubleLayerSignature(sig string) (*claudeSignatureTree, error) {
	return signature.InspectClaudeDoubleLayerSignature(sig)
}

func inspectSingleLayerSignature(sig string) (*claudeSignatureTree, error) {
	return signature.InspectClaudeSingleLayerSignature(sig)
}

func inspectClaudeSignaturePayload(payload []byte, encodingLayers int) (*claudeSignatureTree, error) {
	return signature.InspectClaudeSignaturePayload(payload, encodingLayers)
}

func claudeBypassSignatureValidationOptions() signature.ClaudeSignatureValidationOptions {
	return signature.ClaudeSignatureValidationOptions{Strict: cache.SignatureBypassStrictMode()}
}
