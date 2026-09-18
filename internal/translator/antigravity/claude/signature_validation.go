// Claude thinking signature validation wrappers for Antigravity bypass mode.
package claude

import (
	"encoding/base64"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	maxBypassSignatureLen = signature.MaxClaudeThinkingSignatureLen

	// Gemini carrier envelopes exist only on the Claude-facing wire. The request
	// translator validates and unwraps them before writing native Gemini parts.
	geminiClaudeCarrierPrefix     = "cpa-gemini-carrier-v1:"
	geminiClaudeCarrierNext       = "next"
	geminiClaudeCarrierPrevious   = "previous"
	geminiClaudeCarrierStandalone = "standalone"
	geminiClaudeCarrierText       = "text"
	geminiClaudeCarrierFunction   = "function"
	geminiClaudeCarrierAny        = "any"
)

type claudeSignatureTree = signature.ClaudeSignatureTree

func encodeGeminiClaudeCarrierSignature(rawSignature, direction, targetKind string) string {
	rawSignature = strings.TrimSpace(rawSignature)
	if rawSignature == "" {
		return ""
	}
	return geminiClaudeCarrierPrefix + direction + ":" + targetKind + ":" + base64.RawStdEncoding.EncodeToString([]byte(rawSignature))
}

func decodeGeminiClaudeCarrierSignature(rawSignature string) (signatureValue, direction, targetKind string, marked, ok bool) {
	rawSignature = strings.TrimSpace(rawSignature)
	if !strings.HasPrefix(rawSignature, geminiClaudeCarrierPrefix) {
		return rawSignature, "", "", false, true
	}
	marked = true
	if len(rawSignature) > (signature.MaxGeminiThoughtSignatureLen*4/3)+1024 {
		return "", "", "", true, false
	}
	fields := strings.SplitN(strings.TrimPrefix(rawSignature, geminiClaudeCarrierPrefix), ":", 3)
	if len(fields) != 3 {
		return "", "", "", true, false
	}
	direction, targetKind = fields[0], fields[1]
	switch direction {
	case geminiClaudeCarrierNext, geminiClaudeCarrierPrevious, geminiClaudeCarrierStandalone:
	default:
		return "", "", "", true, false
	}
	switch targetKind {
	case geminiClaudeCarrierText, geminiClaudeCarrierFunction, geminiClaudeCarrierAny:
	default:
		return "", "", "", true, false
	}
	decoded, errDecode := base64.RawStdEncoding.DecodeString(fields[2])
	if errDecode != nil || len(decoded) == 0 || strings.HasPrefix(string(decoded), geminiClaudeCarrierPrefix) {
		return "", "", "", true, false
	}
	blockKind := signature.SignatureBlockKindGeminiModelPart
	if targetKind == geminiClaudeCarrierFunction {
		blockKind = signature.SignatureBlockKindGeminiFunctionCall
	}
	normalized, compatible := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, string(decoded), blockKind)
	if !compatible || signature.IsGeminiThoughtSignatureBypass(signature.SignaturePayloadWithoutProviderPrefix(normalized)) {
		return "", "", "", true, false
	}
	return normalized, direction, targetKind, true, true
}

func geminiClaudeSemanticTargetKind(block gjson.Result) string {
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

func geminiClaudeCarrierMatchesAdjacent(blocks []gjson.Result, index int, direction, targetKind string) bool {
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

func geminiClaudePrecomputeCarrierContext(blocks []gjson.Result) geminiClaudeCarrierContext {
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
	messages := gjson.GetBytes(payload, "messages")
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
			messageJSON, _ = sjson.SetRawBytes(messageJSON, "content", translatorcommon.JoinRawArray(contentItems))
			changed = true
		}
		messageItems = append(messageItems, messageJSON)
	}
	if !changed {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "messages", translatorcommon.JoinRawArray(messageItems))
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
