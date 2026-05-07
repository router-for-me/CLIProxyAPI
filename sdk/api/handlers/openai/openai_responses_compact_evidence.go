package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	compactEvidenceFailNone                 = "none"
	compactEvidenceFailInvalidSameTurnInput = "invalid_same_turn_evidence"
)

type compactEvidenceDiagnostic struct {
	compactOutputHasEvidence         bool
	sameTurnEvidenceHit              bool
	compactResponseEvidenceAugmented bool
	failReason                       string
	inputCounts                      compactEvidenceCounts
	outputCounts                     compactEvidenceCounts
}

type compactEvidenceCounts struct {
	itemCount             int
	assistantMessageCount int
	toolCallCount         int
	toolCallOutputCount   int
}

func compactResponseWithSameTurnEvidence(requestJSON, responseJSON []byte) ([]byte, compactEvidenceDiagnostic, error) {
	diagnostic := compactEvidenceDiagnostic{
		failReason:  compactEvidenceFailNone,
		inputCounts: compactEvidenceInputCounts(requestJSON),
	}

	outputJSON, outputPath := compactResponseOutputJSON(gjson.ParseBytes(responseJSON))
	diagnostic.compactOutputHasEvidence = compactOutputHasAssistantOrToolEvidence(outputJSON)
	diagnostic.outputCounts = compactEvidenceOutputCounts(outputJSON)
	if diagnostic.compactOutputHasEvidence {
		return responseJSON, diagnostic, nil
	}

	evidenceJSON, evidenceHit, errEvidence := compactSameTurnEvidenceJSON(requestJSON)
	diagnostic.sameTurnEvidenceHit = evidenceHit
	if errEvidence != nil {
		diagnostic.failReason = compactEvidenceFailInvalidSameTurnInput
		return nil, diagnostic, errEvidence
	}
	if !evidenceHit {
		return responseJSON, diagnostic, nil
	}

	mergedOutput, errMerge := mergeJSONArrayRaw(string(outputJSON), evidenceJSON)
	if errMerge != nil {
		diagnostic.failReason = compactEvidenceFailInvalidSameTurnInput
		return nil, diagnostic, fmt.Errorf("merge compact evidence: %w", errMerge)
	}
	updated, errSet := compactSetResponseOutputJSON(responseJSON, outputPath, []byte(mergedOutput))
	if errSet != nil {
		diagnostic.failReason = compactEvidenceFailInvalidSameTurnInput
		return nil, diagnostic, fmt.Errorf("set compact response output: %w", errSet)
	}

	diagnostic.compactResponseEvidenceAugmented = true
	diagnostic.outputCounts = compactEvidenceOutputCounts([]byte(mergedOutput))
	return updated, diagnostic, nil
}

func logCompactEvidenceDiagnostic(diagnostic compactEvidenceDiagnostic) {
	fields := log.Fields{
		"compact_input_item_count":            diagnostic.inputCounts.itemCount,
		"compact_input_assistant_count":       diagnostic.inputCounts.assistantMessageCount,
		"compact_input_tool_call_count":       diagnostic.inputCounts.toolCallCount,
		"compact_input_tool_output_count":     diagnostic.inputCounts.toolCallOutputCount,
		"compact_output_has_evidence":         diagnostic.compactOutputHasEvidence,
		"compact_same_turn_evidence_hit":      diagnostic.sameTurnEvidenceHit,
		"compact_response_evidence_augmented": diagnostic.compactResponseEvidenceAugmented,
		"compact_fail_reason":                 diagnostic.failReason,
		"compact_output_item_count":           diagnostic.outputCounts.itemCount,
		"compact_output_assistant_count":      diagnostic.outputCounts.assistantMessageCount,
		"compact_output_tool_call_count":      diagnostic.outputCounts.toolCallCount,
		"compact_output_tool_output_count":    diagnostic.outputCounts.toolCallOutputCount,
	}
	if diagnostic.failReason != compactEvidenceFailNone {
		log.WithFields(fields).Warn("openai responses compact evidence diagnostic")
		return
	}
	log.WithFields(fields).Info("openai responses compact evidence diagnostic")
}

func compactSameTurnEvidenceJSON(rawJSON []byte) (string, bool, error) {
	input := gjson.GetBytes(rawJSON, "input")
	if !input.IsArray() {
		return "[]", false, nil
	}

	items := make([]string, 0, len(input.Array()))
	for _, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "message" && strings.TrimSpace(item.Get("role").String()) == "assistant" {
			items = append(items, item.Raw)
			continue
		}
		if isResponsesToolCallType(itemType) || isResponsesToolCallOutputType(itemType) {
			items = append(items, item.Raw)
		}
	}
	if len(items) == 0 {
		return "[]", false, nil
	}

	evidenceJSON := "[" + strings.Join(items, ",") + "]"
	if errValidate := validateCompactToolOutputs(evidenceJSON); errValidate != nil {
		return "", false, errValidate
	}
	return evidenceJSON, true, nil
}

func validateCompactToolOutputs(rawArray string) error {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		return nil
	}

	var items []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &items); errUnmarshal != nil {
		return errUnmarshal
	}

	callIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if !isResponsesToolCallType(itemType) {
			continue
		}
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID != "" {
			callIDs[callID] = struct{}{}
		}
	}

	for _, item := range items {
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		if !isResponsesToolCallOutputType(itemType) {
			continue
		}
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID == "" {
			return fmt.Errorf("tool output missing matching call")
		}
		if _, ok := callIDs[callID]; !ok {
			return fmt.Errorf("tool output missing matching call")
		}
	}
	return nil
}

func compactResponseOutputJSON(root gjson.Result) ([]byte, string) {
	for _, path := range []string{"output", "response.output"} {
		output := root.Get(path)
		if output.Exists() && output.IsArray() {
			return bytes.Clone([]byte(output.Raw)), path
		}
	}
	return []byte("[]"), "output"
}

func compactSetResponseOutputJSON(payload []byte, outputPath string, outputJSON []byte) ([]byte, error) {
	outputPath = strings.TrimSpace(outputPath)
	if outputPath == "" {
		outputPath = "output"
	}
	return sjson.SetRawBytes(payload, outputPath, outputJSON)
}

func compactOutputHasAssistantOrToolEvidence(raw []byte) bool {
	result := gjson.ParseBytes(normalizeCompactOutputJSON(raw))
	if !result.IsArray() {
		return false
	}
	for _, item := range result.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if isResponsesToolCallType(itemType) && strings.TrimSpace(item.Get("call_id").String()) != "" {
			return true
		}
		if itemType == "message" && strings.TrimSpace(item.Get("role").String()) == "assistant" {
			return true
		}
	}
	return false
}

func compactEvidenceInputCounts(rawJSON []byte) compactEvidenceCounts {
	return compactEvidenceCountsFromArray(gjson.GetBytes(rawJSON, "input"))
}

func compactEvidenceOutputCounts(rawJSON []byte) compactEvidenceCounts {
	return compactEvidenceCountsFromArray(gjson.ParseBytes(normalizeCompactOutputJSON(rawJSON)))
}

func compactEvidenceCountsFromArray(input gjson.Result) compactEvidenceCounts {
	if !input.IsArray() {
		return compactEvidenceCounts{}
	}
	counts := compactEvidenceCounts{itemCount: len(input.Array())}
	for _, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "message" && strings.TrimSpace(item.Get("role").String()) == "assistant" {
			counts.assistantMessageCount++
		}
		if isResponsesToolCallType(itemType) {
			counts.toolCallCount++
		}
		if isResponsesToolCallOutputType(itemType) {
			counts.toolCallOutputCount++
		}
	}
	return counts
}

func normalizeCompactOutputJSON(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("[]")
	}
	result := gjson.ParseBytes(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return bytes.Clone(trimmed)
	}
	return []byte("[]")
}
