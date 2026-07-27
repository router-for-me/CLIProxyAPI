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
		contentItems := make([][]byte, 0, len(contentBlocks))
		pendingCarrierTargetKind := ""
		for blockIndex, block := range contentBlocks {
			if block.Get("type").String() == "thinking" {
				rawSignature := strings.TrimSpace(block.Get("signature").String())
				thinkingText := strings.TrimSpace(block.Get("thinking").String())
				if rawSignature == "" && thinkingText != "" && (pendingCarrierTargetKind == geminiClaudeCarrierAny || pendingCarrierTargetKind == geminiClaudeCarrierText) {
					pendingCarrierTargetKind = ""
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
					case geminiClaudeCarrierNext, geminiClaudeCarrierPrevious:
						invalidMarkedPlacement = !geminiClaudeCarrierMatchesAdjacent(contentBlocks, blockIndex, direction, targetKind)
					case geminiClaudeCarrierStandalone:
						invalidMarkedPlacement = thinkingText != "" && targetKind == geminiClaudeCarrierFunction
					}
					if thinkingText != "" && direction == geminiClaudeCarrierPrevious {
						invalidMarkedPlacement = true
					}
				}
				if !okCarrier || !assistantMessage || invalidMarkedPlacement {
					pendingCarrierTargetKind = ""
					contentChanged = true
					continue
				}
				if !marked {
					innerSignature = rawSignature
				}
				if _, ok := signature.CompatibleSignatureForProviderBlock(signature.SignatureProviderGemini, innerSignature, blockKind); !ok {
					pendingCarrierTargetKind = ""
					contentChanged = true
					continue
				}
				if marked && direction == geminiClaudeCarrierNext {
					pendingCarrierTargetKind = targetKind
				} else {
					pendingCarrierTargetKind = ""
				}
			} else {
				pendingCarrierTargetKind = ""
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
