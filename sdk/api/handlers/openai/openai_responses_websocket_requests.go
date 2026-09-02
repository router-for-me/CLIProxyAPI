package openai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func normalizeResponsesWebsocketRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	return normalizeResponsesWebsocketRequestWithMode(rawJSON, lastRequest, lastResponseOutput, true, true)
}

func normalizeResponsesWebsocketRequestWithMode(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, allowIncrementalInputWithPreviousResponseID bool, allowCompactionReplayBypass bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	return normalizeResponsesWebsocketRequestWithLastResponseID(rawJSON, lastRequest, lastResponseOutput, "", allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass)
}

func normalizeResponsesWebsocketRequestWithLastResponseID(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, lastResponseID string, allowIncrementalInputWithPreviousResponseID bool, allowCompactionReplayBypass bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	return normalizeResponsesWebsocketRequestWithIncrementalState(rawJSON, lastRequest, lastResponseOutput, lastResponseID, nil, allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass)
}

func normalizeResponsesWebsocketRequestWithIncrementalState(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, lastResponseID string, lastResponsePendingToolCallIDs []string, allowIncrementalInputWithPreviousResponseID bool, allowCompactionReplayBypass bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	switch requestType {
	case wsRequestTypeCreate:
		// log.Infof("responses websocket: response.create request")
		if len(lastRequest) == 0 {
			return normalizeResponseCreateRequest(rawJSON)
		}
		return normalizeResponseSubsequentRequest(rawJSON, lastRequest, lastResponseOutput, lastResponseID, lastResponsePendingToolCallIDs, allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass)
	case wsRequestTypeAppend:
		// log.Infof("responses websocket: response.append request")
		return normalizeResponseSubsequentRequest(rawJSON, lastRequest, lastResponseOutput, lastResponseID, lastResponsePendingToolCallIDs, allowIncrementalInputWithPreviousResponseID, allowCompactionReplayBypass)
	default:
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("unsupported websocket request type: %s", requestType),
		}
	}
}

func normalizeResponseCreateRequest(rawJSON []byte) ([]byte, []byte, *interfaces.ErrorMessage) {
	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	if !gjson.GetBytes(normalized, "input").Exists() {
		normalized, _ = sjson.SetRawBytes(normalized, "input", []byte("[]"))
	}

	modelName := strings.TrimSpace(gjson.GetBytes(normalized, "model").String())
	if modelName == "" {
		return nil, nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("missing model in response.create request"),
		}
	}
	return normalized, bytes.Clone(normalized), nil
}

func normalizeResponseSubsequentRequest(rawJSON []byte, lastRequest []byte, lastResponseOutput []byte, lastResponseID string, lastResponsePendingToolCallIDs []string, allowIncrementalInputWithPreviousResponseID bool, allowCompactionReplayBypass bool) ([]byte, []byte, *interfaces.ErrorMessage) {
	if len(lastRequest) == 0 {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request received before response.create"),
		}
	}

	nextInput := gjson.GetBytes(rawJSON, "input")
	if !nextInput.Exists() || !nextInput.IsArray() {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("websocket request requires array field: input"),
		}
	}

	// Compaction can cause clients to replace local websocket history with a new
	// compact transcript on the next `response.create`. When the input already
	// contains historical model output items, treating it as an incremental append
	// duplicates stale turn-state and can leave late orphaned function_call items.
	if shouldReplaceWebsocketTranscript(rawJSON, nextInput) {
		normalized := normalizeResponseTranscriptReplacement(rawJSON, lastRequest)
		return normalized, bytes.Clone(normalized), nil
	}

	// Websocket v2 mode uses response.create with previous_response_id + incremental input.
	// Do not expand it into a full input transcript; upstream expects the incremental payload.
	if allowIncrementalInputWithPreviousResponseID {
		prev := strings.TrimSpace(gjson.GetBytes(rawJSON, "previous_response_id").String())
		if prev == "" {
			if !inputSatisfiesPendingToolCalls(nextInput, lastResponsePendingToolCallIDs) {
				normalized := normalizeResponseTranscriptReplacement(rawJSON, lastRequest)
				return normalized, bytes.Clone(normalized), nil
			}
			prev = strings.TrimSpace(lastResponseID)
		}
		if prev != "" {
			normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
			if errDelete != nil {
				normalized = bytes.Clone(rawJSON)
			}
			normalized, _ = sjson.SetBytes(normalized, "previous_response_id", prev)
			if !gjson.GetBytes(normalized, "model").Exists() {
				modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
				if modelName != "" {
					normalized, _ = sjson.SetBytes(normalized, "model", modelName)
				}
			}
			if !gjson.GetBytes(normalized, "instructions").Exists() {
				instructions := gjson.GetBytes(lastRequest, "instructions")
				if instructions.Exists() {
					normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
				}
			}
			normalized, _ = sjson.SetBytes(normalized, "stream", true)
			return normalized, bytes.Clone(normalized), nil
		}
	}

	// When the client sends a compact replay for a downstream that can consume it
	// directly, the input already carries the canonical history. In that case,
	// skip merging with stale lastRequest/lastResponseOutput to avoid breaking
	// function_call / function_call_output pairings.
	// See: https://github.com/router-for-me/CLIProxyAPI/issues/2207
	var mergedInput []byte
	if allowCompactionReplayBypass && inputContainsFullTranscript(nextInput) {
		log.Infof("responses websocket: full transcript detected, skipping stale merge (input items=%d)", len(nextInput.Array()))
		mergedInput = []byte(nextInput.Raw)
	} else {
		appendInputRaw := nextInput.Raw
		if inputContainsFullTranscript(nextInput) {
			appendInputRaw = inputWithoutCompactionItems(nextInput)
		}

		var errMerge error
		mergedInput, errMerge = mergeResponsesWebsocketInput(lastRequest, lastResponseOutput, appendInputRaw)
		if errMerge != nil {
			return nil, lastRequest, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      errMerge,
			}
		}
	}

	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	if !gjson.GetBytes(normalized, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	var errSet error
	normalized, errSet = sjson.SetRawBytes(normalized, "input", mergedInput)
	if errSet != nil {
		return nil, lastRequest, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("failed to merge websocket input: %w", errSet),
		}
	}
	return normalized, normalized, nil
}

func shouldReplaceWebsocketTranscript(rawJSON []byte, nextInput gjson.Result) bool {
	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	if requestType != wsRequestTypeCreate && requestType != wsRequestTypeAppend {
		return false
	}
	previousResponseID := gjson.GetBytes(rawJSON, "previous_response_id")
	if strings.TrimSpace(previousResponseID.String()) != "" {
		return false
	}
	if !nextInput.Exists() || !nextInput.IsArray() {
		return false
	}
	if requestType == wsRequestTypeCreate && !previousResponseID.Exists() && inputHasCodexLocalCompactionSummary(nextInput) {
		return true
	}

	for _, item := range nextInput.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call", "custom_tool_call":
			return true
		case "message":
			if strings.TrimSpace(item.Get("role").String()) == "assistant" {
				return true
			}
		}
	}

	return false
}

func inputHasCodexLocalCompactionSummary(input gjson.Result) bool {
	if !input.IsArray() {
		return false
	}

	hasSummary := false
	for index, item := range input.Array() {
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType == "additional_tools" {
			tools := item.Get("tools")
			if index != 0 || strings.TrimSpace(item.Get("role").String()) != "developer" || !tools.IsArray() {
				return false
			}
			for _, tool := range tools.Array() {
				if !tool.IsObject() || strings.TrimSpace(tool.Get("type").String()) == "" {
					return false
				}
			}
			continue
		}
		if itemType != "" && itemType != "message" {
			return false
		}

		role := strings.TrimSpace(item.Get("role").String())
		if role != "user" && role != "developer" {
			return false
		}
		if role == "user" && strings.HasPrefix(codexLocalCompactionMessageText(item), codexLocalCompactionSummaryPrefix+"\n") {
			hasSummary = true
		}
	}
	return hasSummary
}

func codexLocalCompactionMessageText(message gjson.Result) string {
	content := message.Get("content")
	if content.Type == gjson.String {
		return content.String()
	}
	if !content.IsArray() {
		return ""
	}

	var text strings.Builder
	for _, part := range content.Array() {
		if strings.TrimSpace(part.Get("type").String()) == "input_text" {
			text.WriteString(part.Get("text").String())
		}
	}
	return text.String()
}

func inputSatisfiesPendingToolCalls(input gjson.Result, pendingCallIDs []string) bool {
	if len(pendingCallIDs) == 0 {
		return true
	}
	if !input.IsArray() {
		return false
	}
	outputs := make(map[string]struct{}, len(pendingCallIDs))
	for _, item := range input.Array() {
		switch strings.TrimSpace(item.Get("type").String()) {
		case "function_call_output", "custom_tool_call_output":
			callID := strings.TrimSpace(item.Get("call_id").String())
			if callID != "" {
				outputs[callID] = struct{}{}
			}
		}
	}
	for _, callID := range pendingCallIDs {
		callID = strings.TrimSpace(callID)
		if callID == "" {
			continue
		}
		if _, ok := outputs[callID]; !ok {
			return false
		}
	}
	return true
}

func normalizeResponseTranscriptReplacement(rawJSON []byte, lastRequest []byte) []byte {
	normalized, errDelete := sjson.DeleteBytes(rawJSON, "type")
	if errDelete != nil {
		normalized = bytes.Clone(rawJSON)
	}
	normalized, _ = sjson.DeleteBytes(normalized, "previous_response_id")
	if !gjson.GetBytes(normalized, "model").Exists() {
		modelName := strings.TrimSpace(gjson.GetBytes(lastRequest, "model").String())
		if modelName != "" {
			normalized, _ = sjson.SetBytes(normalized, "model", modelName)
		}
	}
	if !gjson.GetBytes(normalized, "instructions").Exists() {
		instructions := gjson.GetBytes(lastRequest, "instructions")
		if instructions.Exists() {
			normalized, _ = sjson.SetRawBytes(normalized, "instructions", []byte(instructions.Raw))
		}
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	return bytes.Clone(normalized)
}

type responsesWebsocketInputItem struct {
	raw      json.RawMessage
	itemType string
	id       string
	callID   string
}

type responsesWebsocketMergeInputItem struct {
	// raw may reference a caller-owned request buffer. Merge items must remain
	// local to mergeResponsesWebsocketInput, which copies every item into the
	// owned output buffer before returning.
	raw      string
	itemType string
	id       string
	callID   string
}

func mergeResponsesWebsocketInput(lastRequest []byte, lastResponseOutput []byte, appendRaw string) ([]byte, error) {
	previousInput, errPrevious := responsesWebsocketPreviousInputNoCopy(lastRequest)
	if errPrevious != nil {
		return nil, fmt.Errorf("invalid previous request input: %w", errPrevious)
	}
	items, errExisting := appendResponsesWebsocketMergeInputResult(nil, previousInput)
	if errExisting != nil {
		return nil, fmt.Errorf("invalid previous request input: %w", errExisting)
	}

	trimmedResponse := bytes.TrimSpace(lastResponseOutput)
	if len(trimmedResponse) > 0 && trimmedResponse[0] == '[' && json.Valid(trimmedResponse) {
		responseInput := util.ParseGJSONBytesNoCopy(trimmedResponse)
		if inputContainsFullTranscript(responseInput) {
			items = slices.DeleteFunc(items, func(item responsesWebsocketMergeInputItem) bool {
				return item.itemType == "compaction_trigger"
			})
		}
		var errResponse error
		items, errResponse = appendResponsesWebsocketMergeInputResult(items, responseInput)
		if errResponse != nil {
			return nil, fmt.Errorf("invalid previous response output: %w", errResponse)
		}
	}

	items, errAppend := appendResponsesWebsocketMergeInputItems(items, appendRaw)
	if errAppend != nil {
		return nil, fmt.Errorf("invalid request input: %w", errAppend)
	}

	items = dedupeResponsesWebsocketMergeFunctionCalls(items)
	items = dedupeResponsesWebsocketMergeInputItems(items)
	return marshalResponsesWebsocketMergeInputItems(items), nil
}

func responsesWebsocketPreviousInputNoCopy(lastRequest []byte) (gjson.Result, error) {
	if !json.Valid(lastRequest) {
		return gjson.Result{}, responsesWebsocketPreviousInputDecodeError(lastRequest)
	}

	root := util.ParseGJSONBytesNoCopy(lastRequest)
	if root.Type == gjson.Null {
		return gjson.Parse("[]"), nil
	}
	if !root.IsObject() {
		return gjson.Result{}, responsesWebsocketPreviousInputDecodeError(lastRequest)
	}

	var input gjson.Result
	inputFound := false
	invalidInput := false
	root.ForEach(func(key, value gjson.Result) bool {
		if !strings.EqualFold(key.String(), "input") {
			return true
		}
		// encoding/json processes matching duplicate fields in source order,
		// retains the last value, and still reports a type error from any
		// incompatible duplicate. Preserve those semantics without copying the
		// selected array out of the caller-owned request buffer.
		inputFound = true
		input = value
		if value.Type != gjson.Null && !value.IsArray() {
			invalidInput = true
		}
		return true
	})
	if invalidInput {
		return gjson.Result{}, responsesWebsocketPreviousInputDecodeError(lastRequest)
	}
	if !inputFound || input.Type == gjson.Null {
		return gjson.Parse("[]"), nil
	}
	return input, nil
}

func responsesWebsocketPreviousInputDecodeError(lastRequest []byte) error {
	var previousRequest struct {
		Input []json.RawMessage `json:"input"`
	}
	return json.Unmarshal(lastRequest, &previousRequest)
}

func appendResponsesWebsocketMergeInputItems(items []responsesWebsocketMergeInputItem, rawArray string) ([]responsesWebsocketMergeInputItem, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		rawArray = "[]"
	}
	parsed := gjson.Parse(rawArray)
	if gjson.Valid(rawArray) {
		return appendResponsesWebsocketMergeInputResult(items, parsed)
	}

	var rawItems []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &rawItems); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return items, nil
}

func appendResponsesWebsocketMergeInputResult(items []responsesWebsocketMergeInputItem, input gjson.Result) ([]responsesWebsocketMergeInputItem, error) {
	if input.Type == gjson.Null {
		return items, nil
	}
	if !input.IsArray() {
		var rawItems []json.RawMessage
		if errUnmarshal := json.Unmarshal([]byte(input.Raw), &rawItems); errUnmarshal != nil {
			return nil, errUnmarshal
		}
		return items, nil
	}

	rawItems := input.Array()
	items = slices.Grow(items, len(rawItems))
	for _, rawItem := range rawItems {
		item := responsesWebsocketMergeInputItem{raw: rawItem.Raw}
		if rawItem.IsObject() {
			rawItem.ForEach(func(key, value gjson.Result) bool {
				metadataKey := key.String()
				switch {
				case strings.EqualFold(metadataKey, "type"):
					item.itemType = strings.TrimSpace(value.String())
				case strings.EqualFold(metadataKey, "id"):
					item.id = strings.TrimSpace(value.String())
				case strings.EqualFold(metadataKey, "call_id"):
					item.callID = strings.TrimSpace(value.String())
				}
				return true
			})
		}
		items = append(items, item)
	}
	return items, nil
}

func dedupeResponsesWebsocketMergeFunctionCalls(items []responsesWebsocketMergeInputItem) []responsesWebsocketMergeInputItem {
	seenCallIDs := make(map[string]struct{}, len(items))
	filtered := items[:0]
	for _, item := range items {
		if isResponsesToolCallType(item.itemType) && item.callID != "" {
			if _, ok := seenCallIDs[item.callID]; ok {
				continue
			}
			seenCallIDs[item.callID] = struct{}{}
		}
		filtered = append(filtered, item)
	}
	clear(items[len(filtered):])
	return filtered
}

func dedupeResponsesWebsocketMergeInputItems(items []responsesWebsocketMergeInputItem) []responsesWebsocketMergeInputItem {
	referencedCallIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		if isResponsesToolCallOutputType(item.itemType) && item.callID != "" {
			referencedCallIDs[item.callID] = struct{}{}
		}
	}

	keepIndexByID := make(map[string]int, len(items))
	keepReferencedByID := make(map[string]bool, len(items))
	for index, item := range items {
		if item.id == "" {
			continue
		}
		_, referenced := referencedCallIDs[item.callID]
		referenced = referenced && item.callID != ""
		if _, seen := keepIndexByID[item.id]; !seen {
			keepIndexByID[item.id] = index
			keepReferencedByID[item.id] = referenced
			continue
		}
		if referenced || !keepReferencedByID[item.id] {
			keepIndexByID[item.id] = index
			keepReferencedByID[item.id] = referenced
		}
	}

	filtered := items[:0]
	for index, item := range items {
		if item.id != "" && keepIndexByID[item.id] != index {
			continue
		}
		filtered = append(filtered, item)
	}
	clear(items[len(filtered):])
	return filtered
}

func marshalResponsesWebsocketMergeInputItems(items []responsesWebsocketMergeInputItem) []byte {
	outputLength := 2
	if len(items) > 1 {
		outputLength += len(items) - 1
	}
	for _, item := range items {
		outputLength += len(item.raw)
	}

	// This allocation establishes ownership of the merged transcript and is the
	// only large allocation the merge path retains after it returns.
	out := make([]byte, 0, outputLength)
	out = append(out, '[')
	for index, item := range items {
		if index > 0 {
			out = append(out, ',')
		}
		out = append(out, item.raw...)
	}
	out = append(out, ']')
	return out
}

func parseResponsesWebsocketInputItems(rawArray string) ([]responsesWebsocketInputItem, error) {
	return appendResponsesWebsocketInputItems(nil, rawArray)
}

func appendResponsesWebsocketInputItems(items []responsesWebsocketInputItem, rawArray string) ([]responsesWebsocketInputItem, error) {
	rawArray = strings.TrimSpace(rawArray)
	if rawArray == "" {
		rawArray = "[]"
	}
	var rawItems []json.RawMessage
	if errUnmarshal := json.Unmarshal([]byte(rawArray), &rawItems); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return appendResponsesWebsocketRawInputItems(items, rawItems)
}

func appendResponsesWebsocketRawInputItems(items []responsesWebsocketInputItem, rawItems []json.RawMessage) ([]responsesWebsocketInputItem, error) {
	for _, rawItem := range rawItems {
		item, errItem := parseResponsesWebsocketInputItem(rawItem)
		if errItem != nil {
			return nil, errItem
		}
		items = append(items, item)
	}
	return items, nil
}

func parseResponsesWebsocketInputItem(rawItem json.RawMessage) (responsesWebsocketInputItem, error) {
	item := responsesWebsocketInputItem{raw: rawItem}
	trimmed := bytes.TrimSpace(rawItem)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return item, nil
	}
	var metadata struct {
		Type   json.RawMessage `json:"type"`
		ID     json.RawMessage `json:"id"`
		CallID json.RawMessage `json:"call_id"`
	}
	if errUnmarshal := json.Unmarshal(trimmed, &metadata); errUnmarshal != nil {
		return responsesWebsocketInputItem{}, errUnmarshal
	}
	item.itemType = responsesWebsocketMetadataString(metadata.Type)
	item.id = responsesWebsocketMetadataString(metadata.ID)
	item.callID = responsesWebsocketMetadataString(metadata.CallID)
	return item, nil
}

func responsesWebsocketMetadataString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	if raw[0] == '"' {
		var value string
		if errUnmarshal := json.Unmarshal(raw, &value); errUnmarshal == nil {
			return strings.TrimSpace(value)
		}
	}
	return strings.TrimSpace(string(raw))
}

func marshalResponsesWebsocketInputItems(items []responsesWebsocketInputItem) (string, error) {
	rawItems := make([]json.RawMessage, len(items))
	for index := range items {
		rawItems[index] = items[index].raw
	}
	out, errMarshal := json.Marshal(rawItems)
	if errMarshal != nil {
		return "", errMarshal
	}
	return string(out), nil
}

func dedupeResponsesWebsocketFunctionCalls(items []responsesWebsocketInputItem) []responsesWebsocketInputItem {
	seenCallIDs := make(map[string]struct{}, len(items))
	filtered := items[:0]
	for _, item := range items {
		if isResponsesToolCallType(item.itemType) && item.callID != "" {
			if _, ok := seenCallIDs[item.callID]; ok {
				continue
			}
			seenCallIDs[item.callID] = struct{}{}
		}
		filtered = append(filtered, item)
	}
	clear(items[len(filtered):])
	return filtered
}

func dedupeResponsesWebsocketInputItems(items []responsesWebsocketInputItem) []responsesWebsocketInputItem {
	// Collect the call_ids that are still referenced by tool-call output
	// items. When several input items share the same id, the one we keep must
	// preserve any call_id that has a matching output; otherwise the upstream
	// rejects the request with "No tool call found for function call output".
	referencedCallIDs := make(map[string]struct{}, len(items))
	for _, item := range items {
		switch item.itemType {
		case "function_call_output", "custom_tool_call_output":
			if item.callID != "" {
				referencedCallIDs[item.callID] = struct{}{}
			}
		}
	}

	// For each id, choose the index to keep. The default is the last
	// occurrence (matching the original dedupe behavior), but we never replace
	// an item whose call_id still has a matching output with one that does not.
	keepIndexByID := make(map[string]int, len(items))
	keepReferencedByID := make(map[string]bool, len(items))
	for index, item := range items {
		if item.id == "" {
			continue
		}
		_, referenced := referencedCallIDs[item.callID]
		referenced = referenced && item.callID != ""
		if _, seen := keepIndexByID[item.id]; !seen {
			keepIndexByID[item.id] = index
			keepReferencedByID[item.id] = referenced
			continue
		}
		if referenced || !keepReferencedByID[item.id] {
			keepIndexByID[item.id] = index
			keepReferencedByID[item.id] = referenced
		}
	}

	filtered := items[:0]
	for index, item := range items {
		if item.id != "" && keepIndexByID[item.id] != index {
			continue
		}
		filtered = append(filtered, item)
	}
	clear(items[len(filtered):])
	return filtered
}

func dedupeResponsesWebsocketInputItemsByID(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() || !input.IsArray() {
		return payload
	}
	dedupedInput, errDedupe := dedupeInputItemsByID(input.Raw)
	if errDedupe != nil || dedupedInput == input.Raw {
		return payload
	}
	updated, errSet := sjson.SetRawBytes(payload, "input", []byte(dedupedInput))
	if errSet != nil {
		return payload
	}
	return updated
}

func dedupeInputItemsByID(rawArray string) (string, error) {
	items, errParse := parseResponsesWebsocketInputItems(rawArray)
	if errParse != nil {
		return "", errParse
	}
	return marshalResponsesWebsocketInputItems(dedupeResponsesWebsocketInputItems(items))
}

func normalizeResponsesWebsocketPassthroughRequest(rawJSON []byte, modelName string) ([]byte, *interfaces.ErrorMessage) {
	if !json.Valid(rawJSON) {
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("invalid websocket request JSON"),
		}
	}

	requestType := strings.TrimSpace(gjson.GetBytes(rawJSON, "type").String())
	switch requestType {
	case wsRequestTypeCreate, wsRequestTypeAppend:
	default:
		return nil, &interfaces.ErrorMessage{
			StatusCode: http.StatusBadRequest,
			Error:      fmt.Errorf("unsupported websocket request type: %s", requestType),
		}
	}

	normalized := bytes.Clone(rawJSON)
	if strings.TrimSpace(gjson.GetBytes(normalized, "model").String()) == "" {
		modelName = strings.TrimSpace(modelName)
		if modelName == "" {
			return nil, &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      fmt.Errorf("missing model in response.create request"),
			}
		}
		normalized, _ = sjson.SetBytes(normalized, "model", modelName)
	}
	normalized, _ = sjson.SetBytes(normalized, "stream", true)
	return normalized, nil
}
