package responses

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	sigcompat "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	geminiResponsesCarrierPrefix     = "cpa-gemini-responses-carrier-v1:"
	geminiResponsesCarrierNext       = "next"
	geminiResponsesCarrierPrevious   = "previous"
	geminiResponsesCarrierStandalone = "standalone"
	geminiResponsesCarrierText       = "text"
	geminiResponsesCarrierFunction   = "function"
	geminiResponsesCarrierAny        = "any"

	geminiResponsesCarrierDirectionField = "_cpa_reasoning_direction"
	geminiResponsesCarrierTargetField    = "_cpa_reasoning_target"
	geminiResponsesCarrierSignatureField = "_cpa_reasoning_signature"
	geminiResponsesCarrierSummaryField   = "_cpa_reasoning_summary"
)

func encodeGeminiResponsesCarrier(rawSignature, direction, targetKind string) string {
	rawSignature = strings.TrimSpace(rawSignature)
	if rawSignature == "" {
		return ""
	}
	return geminiResponsesCarrierPrefix + direction + ":" + targetKind + ":" + base64.RawStdEncoding.EncodeToString([]byte(rawSignature))
}

func decodeGeminiResponsesCarrier(rawSignature string) (signatureValue, direction, targetKind string, marked, ok bool) {
	rawSignature = strings.TrimSpace(rawSignature)
	if !strings.HasPrefix(rawSignature, geminiResponsesCarrierPrefix) {
		return rawSignature, "", "", false, true
	}
	marked = true
	if len(rawSignature) > (sigcompat.MaxGeminiThoughtSignatureLen*4/3)+1024 {
		return "", "", "", true, false
	}
	fields := strings.SplitN(strings.TrimPrefix(rawSignature, geminiResponsesCarrierPrefix), ":", 3)
	if len(fields) != 3 {
		return "", "", "", true, false
	}
	direction, targetKind = fields[0], fields[1]
	switch direction {
	case geminiResponsesCarrierNext, geminiResponsesCarrierPrevious, geminiResponsesCarrierStandalone:
	default:
		return "", "", "", true, false
	}
	switch targetKind {
	case geminiResponsesCarrierText, geminiResponsesCarrierFunction, geminiResponsesCarrierAny:
	default:
		return "", "", "", true, false
	}
	decoded, errDecode := base64.RawStdEncoding.DecodeString(fields[2])
	if errDecode != nil || len(decoded) == 0 || strings.HasPrefix(string(decoded), geminiResponsesCarrierPrefix) {
		return "", "", "", true, false
	}
	return string(decoded), direction, targetKind, true, true
}

func compatibleGeminiResponsesCarrierSignature(rawSignature, targetKind string) (string, bool) {
	blockKind := sigcompat.SignatureBlockKindGeminiModelPart
	if targetKind == geminiResponsesCarrierFunction {
		blockKind = sigcompat.SignatureBlockKindGeminiFunctionCall
	}
	normalized, compatible := sigcompat.CompatibleSignatureForProviderBlock(sigcompat.SignatureProviderGemini, rawSignature, blockKind)
	if !compatible || sigcompat.IsGeminiThoughtSignatureBypass(sigcompat.SignaturePayloadWithoutProviderPrefix(normalized)) {
		return "", false
	}
	return normalized, true
}

func geminiResponsesCarrierSemanticTarget(item gjson.Result) string {
	switch item.Get("type").String() {
	case "function_call":
		return geminiResponsesCarrierFunction
	case "reasoning":
		if strings.TrimSpace(item.Get("summary.0.text").String()) != "" {
			return geminiResponsesCarrierText
		}
	}
	if _, ok := openAIResponsesAssistantVisibleText(item); ok {
		return geminiResponsesCarrierText
	}
	return ""
}

func geminiResponsesCarrierMatchesAdjacent(items []gjson.Result, index int, direction, targetKind string) bool {
	step := 1
	if direction == geminiResponsesCarrierPrevious {
		step = -1
	}
	for adjacent := index + step; adjacent >= 0 && adjacent < len(items); adjacent += step {
		if kind := geminiResponsesCarrierSemanticTarget(items[adjacent]); kind != "" {
			return targetKind == geminiResponsesCarrierAny || targetKind == kind
		}
		if !isOpenAIResponsesDetachedCarrier(items[adjacent]) {
			return false
		}
	}
	return false
}

func stripGeminiResponsesCarrierMetadata(itemJSON []byte) []byte {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(itemJSON, &fields); err != nil {
		return itemJSON
	}
	delete(fields, geminiResponsesCarrierDirectionField)
	delete(fields, geminiResponsesCarrierTargetField)
	delete(fields, geminiResponsesCarrierSignatureField)
	delete(fields, geminiResponsesCarrierSummaryField)
	stripped, errMarshal := json.Marshal(fields)
	if errMarshal != nil {
		return itemJSON
	}
	return stripped
}

func normalizeGeminiResponsesCarriers(items []gjson.Result) ([]gjson.Result, bool) {
	normalized := make([]gjson.Result, 0, len(items))
	hasValidCarrier := false
	for itemIndex, originalItem := range items {
		itemJSON := stripGeminiResponsesCarrierMetadata([]byte(originalItem.Raw))
		item := gjson.ParseBytes(itemJSON)
		if item.Get("type").String() != "reasoning" {
			normalized = append(normalized, item)
			continue
		}
		rawSignature := strings.TrimSpace(item.Get("encrypted_content").String())
		signature, direction, targetKind, marked, ok := decodeGeminiResponsesCarrier(rawSignature)
		if !marked {
			if rawSignature != "" {
				_, hasCompatibleRawCarrier := compatibleGeminiResponsesCarrierSignature(rawSignature, geminiResponsesCarrierAny)
				hasValidCarrier = hasValidCarrier || hasCompatibleRawCarrier
			}
			normalized = append(normalized, item)
			continue
		}
		if ok {
			signature, ok = compatibleGeminiResponsesCarrierSignature(signature, targetKind)
		}
		if ok && direction != geminiResponsesCarrierStandalone {
			ok = geminiResponsesCarrierMatchesAdjacent(items, itemIndex, direction, targetKind)
		}
		isDetached := isOpenAIResponsesDetachedCarrier(item)
		hasSummary := strings.TrimSpace(item.Get("summary.0.text").String()) != ""
		validSummaryCarrier := hasSummary && ((direction == geminiResponsesCarrierStandalone && (targetKind == geminiResponsesCarrierText || targetKind == geminiResponsesCarrierAny)) || direction == geminiResponsesCarrierNext)
		if !ok || (!isDetached && !validSummaryCarrier) {
			if strings.TrimSpace(item.Get("summary.0.text").String()) == "" {
				continue
			}
			itemJSON, _ = sjson.DeleteBytes(itemJSON, "encrypted_content")
			normalized = append(normalized, gjson.ParseBytes(itemJSON))
			continue
		}
		hasValidCarrier = true
		itemJSON, _ = sjson.SetBytes(itemJSON, "encrypted_content", signature)
		itemJSON, _ = sjson.SetBytes(itemJSON, geminiResponsesCarrierDirectionField, direction)
		itemJSON, _ = sjson.SetBytes(itemJSON, geminiResponsesCarrierTargetField, targetKind)
		normalized = append(normalized, gjson.ParseBytes(itemJSON))
	}
	return normalized, hasValidCarrier
}

func geminiResponsesCarrierDirection(item gjson.Result) string {
	return item.Get(geminiResponsesCarrierDirectionField).String()
}

func geminiResponsesCarrierTarget(item gjson.Result) string {
	return item.Get(geminiResponsesCarrierTargetField).String()
}

func isOpenAIResponsesDetachedCarrier(item gjson.Result) bool {
	return item.Get("type").String() == "reasoning" && strings.TrimSpace(item.Get("encrypted_content").String()) != "" && strings.TrimSpace(item.Get("summary.0.text").String()) == ""
}
