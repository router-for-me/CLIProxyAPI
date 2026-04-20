package executor

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// MergeResponsesTranscript rebuilds a full Responses API request from the current
// request and a stored snapshot, following the same merge rules used by the
// websocket path:
//
//	mergedInput = snapshot.Input + snapshot.Output + currentRequest.input
//
// It also:
//   - Removes previous_response_id (the transcript is now self-contained)
//   - Backfills model and instructions from the snapshot if missing in the current request
//   - Preserves stream flag from the current request
//
// This is only used in the chat_fallback path when previous_response_id is present.
func MergeResponsesTranscript(currentRequest []byte, snapshot ResponsesSnapshot) ([]byte, error) {
	if len(currentRequest) == 0 {
		return nil, fmt.Errorf("merge responses transcript: current request is empty")
	}

	// 1. Extract the previous input from snapshot.
	prevInput := strings.TrimSpace(string(snapshot.Input))
	if prevInput == "" {
		prevInput = "[]"
	}

	// 2. Normalize the previous output as a JSON array.
	prevOutput := normalizeJSONArrayRaw(snapshot.Output)

	// 3. Extract the current request's input.
	currentInputRaw := gjson.GetBytes(currentRequest, "input")
	currentInput := "[]"
	if currentInputRaw.Exists() && currentInputRaw.IsArray() {
		currentInput = currentInputRaw.Raw
	}

	// 4. Merge: prevInput + prevOutput + currentInput
	merged, err := mergeJSONArrayRaw(prevInput, prevOutput)
	if err != nil {
		return nil, fmt.Errorf("merge responses transcript: failed to merge prev input+output: %w", err)
	}
	merged, err = mergeJSONArrayRaw(merged, currentInput)
	if err != nil {
		return nil, fmt.Errorf("merge responses transcript: failed to merge with current input: %w", err)
	}

	// 5. Remove previous_response_id and type.
	normalized, errDel := sjson.DeleteBytes(currentRequest, "previous_response_id")
	if errDel != nil {
		normalized = currentRequest
	}
	normalized, _ = sjson.DeleteBytes(normalized, "type")

	// 6. Backfill model from snapshot if missing.
	if !gjson.GetBytes(normalized, "model").Exists() && snapshot.Model != "" {
		normalized, _ = sjson.SetBytes(normalized, "model", snapshot.Model)
	}

	// 7. Backfill instructions from snapshot if missing.
	if !gjson.GetBytes(normalized, "instructions").Exists() && snapshot.Instructions != "" {
		normalized, _ = sjson.SetBytes(normalized, "instructions", snapshot.Instructions)
	}

	// 8. Replace input with the merged result.
	normalized, errSet := sjson.SetRawBytes(normalized, "input", []byte(merged))
	if errSet != nil {
		return nil, fmt.Errorf("merge responses transcript: failed to set merged input: %w", errSet)
	}

	return normalized, nil
}

// mergeJSONArrayRaw concatenates two JSON arrays represented as raw strings.
// Empty or blank inputs are treated as "[]".
func mergeJSONArrayRaw(existingRaw, appendRaw string) (string, error) {
	existingRaw = strings.TrimSpace(existingRaw)
	appendRaw = strings.TrimSpace(appendRaw)
	if existingRaw == "" {
		existingRaw = "[]"
	}
	if appendRaw == "" {
		appendRaw = "[]"
	}

	var existing []json.RawMessage
	if err := json.Unmarshal([]byte(existingRaw), &existing); err != nil {
		return "", err
	}
	var appendItems []json.RawMessage
	if err := json.Unmarshal([]byte(appendRaw), &appendItems); err != nil {
		return "", err
	}

	merged := append(existing, appendItems...)
	out, err := json.Marshal(merged)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// normalizeJSONArrayRaw parses raw bytes as a JSON array string.
// Returns "[]" if the input is empty or not a valid JSON array.
func normalizeJSONArrayRaw(raw []byte) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return "[]"
	}
	result := gjson.Parse(trimmed)
	if result.Type == gjson.JSON && result.IsArray() {
		return trimmed
	}
	return "[]"
}
