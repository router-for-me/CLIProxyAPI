package openai

import (
	"bytes"
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
	outputResult := gjson.ParseBytes(normalizeCompactOutputJSON(outputJSON))
	diagnostic.outputCounts, diagnostic.compactOutputHasEvidence = compactEvidenceCountsAndHasEvidence(outputResult)
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

	tail := compactLatestTailWindow(input)
	if len(tail) == 0 {
		return "[]", false, nil
	}

	callIDs := make(map[string]struct{}, len(tail))
	for _, item := range tail {
		itemType := strings.TrimSpace(item.Get("type").String())
		if !isResponsesToolCallType(itemType) {
			continue
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID != "" {
			callIDs[callID] = struct{}{}
		}
	}

	items := make([]string, 0, len(tail))
	for _, item := range tail {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "message" && strings.TrimSpace(item.Get("role").String()) == "assistant" {
			items = append(items, item.Raw)
			continue
		}
		if isResponsesToolCallType(itemType) {
			if strings.TrimSpace(item.Get("call_id").String()) == "" {
				continue
			}
			items = append(items, item.Raw)
			continue
		}
		if isResponsesToolCallOutputType(itemType) {
			callID := strings.TrimSpace(item.Get("call_id").String())
			if _, ok := callIDs[callID]; ok {
				items = append(items, item.Raw)
			}
		}
	}
	if len(items) == 0 {
		return "[]", false, nil
	}

	return "[" + strings.Join(items, ",") + "]", true, nil
}

func compactLatestTailWindow(input gjson.Result) []gjson.Result {
	if !input.IsArray() {
		return nil
	}
	array := input.Array()
	tailStart := -1
	for i, item := range array {
		if isCompactTailMarker(item) {
			tailStart = i + 1
		}
	}
	if tailStart < 0 || tailStart >= len(array) {
		return nil
	}
	return array[tailStart:]
}

func isCompactTailMarker(item gjson.Result) bool {
	itemType := strings.TrimSpace(item.Get("type").String())
	if itemType == "compaction" || itemType == "compaction_summary" {
		return true
	}
	return (itemType == "" || itemType == "message") && strings.TrimSpace(item.Get("role").String()) == "user"
}

func compactResponseOutputJSON(root gjson.Result) ([]byte, string) {
	for _, path := range []string{"output", "response.output"} {
		output := root.Get(path)
		if output.Exists() && output.IsArray() {
			return []byte(output.Raw), path
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
	_, hasEvidence := compactEvidenceCountsAndHasEvidence(gjson.ParseBytes(normalizeCompactOutputJSON(raw)))
	return hasEvidence
}

func compactEvidenceInputCounts(rawJSON []byte) compactEvidenceCounts {
	return compactEvidenceCountsFromArray(gjson.GetBytes(rawJSON, "input"))
}

func compactEvidenceOutputCounts(rawJSON []byte) compactEvidenceCounts {
	return compactEvidenceCountsFromArray(gjson.ParseBytes(normalizeCompactOutputJSON(rawJSON)))
}

func compactEvidenceCountsFromArray(input gjson.Result) compactEvidenceCounts {
	counts, _ := compactEvidenceCountsAndHasEvidence(input)
	return counts
}

func compactEvidenceCountsAndHasEvidence(input gjson.Result) (compactEvidenceCounts, bool) {
	if !input.IsArray() {
		return compactEvidenceCounts{}, false
	}
	array := input.Array()
	counts := compactEvidenceCounts{itemCount: len(array)}
	hasEvidence := false
	for _, item := range array {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "message" && strings.TrimSpace(item.Get("role").String()) == "assistant" {
			counts.assistantMessageCount++
			hasEvidence = true
		}
		if isResponsesToolCallType(itemType) {
			counts.toolCallCount++
			if strings.TrimSpace(item.Get("call_id").String()) != "" {
				hasEvidence = true
			}
		}
		if isResponsesToolCallOutputType(itemType) {
			counts.toolCallOutputCount++
			if strings.TrimSpace(item.Get("call_id").String()) != "" {
				hasEvidence = true
			}
		}
	}
	return counts, hasEvidence
}

func normalizeCompactOutputJSON(raw []byte) []byte {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []byte("[]")
	}
	result := gjson.ParseBytes(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return trimmed
	}
	return []byte("[]")
}
