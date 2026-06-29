package executor

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func sanitizeCodexHTTPFallbackPayload(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}
	out := bytes.Clone(payload)
	if strings.TrimSpace(gjson.GetBytes(out, "type").String()) == "response.create" {
		out, _ = sjson.DeleteBytes(out, "type")
	}
	out, _ = sjson.DeleteBytes(out, "generate")
	out = sanitizeCodexHTTPFallbackInput(out)
	return out
}

func sanitizeCodexHTTPFallbackInput(payload []byte) []byte {
	input := gjson.GetBytes(payload, "input")
	if !input.IsArray() {
		return payload
	}
	sanitizedInput := sanitizeCodexHTTPFallbackInputArray([]byte(input.Raw))
	if string(sanitizedInput) == input.Raw {
		return payload
	}
	out, err := sjson.SetRawBytes(payload, "input", sanitizedInput)
	if err != nil {
		return payload
	}
	return out
}

func sanitizeCodexHTTPFallbackInputArray(raw []byte) []byte {
	if len(raw) == 0 {
		return raw
	}

	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return stripCodexHTTPFallbackInputActions(raw)
	}

	sanitized := make([]json.RawMessage, 0, len(items))
	callPresent := make(map[string]map[string]struct{}, len(items))
	outputPresent := make(map[string]map[string]struct{}, len(items))
	for _, item := range items {
		item = stripCodexHTTPFallbackItemAction(item)
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		if callID == "" {
			sanitized = append(sanitized, item)
			continue
		}
		if isCodexHTTPFallbackToolCallType(itemType) {
			recordCodexHTTPFallbackToolItemPresence(callPresent, callID, itemType)
		}
		if isCodexHTTPFallbackToolOutputType(itemType) && !codexHTTPFallbackToolOutputDoesNotRequireLocalCall(itemType, item) {
			recordCodexHTTPFallbackToolItemPresence(outputPresent, callID, itemType)
		}
		sanitized = append(sanitized, item)
	}

	filtered := make([]json.RawMessage, 0, len(sanitized))
	for _, item := range sanitized {
		itemType := strings.TrimSpace(gjson.GetBytes(item, "type").String())
		callID := strings.TrimSpace(gjson.GetBytes(item, "call_id").String())
		switch {
		case isCodexHTTPFallbackToolOutputType(itemType):
			if codexHTTPFallbackToolOutputDoesNotRequireLocalCall(itemType, item) {
				filtered = append(filtered, item)
				continue
			}
			if callID == "" || !codexHTTPFallbackToolOutputHasMatchingCall(itemType, callID, callPresent) {
				continue
			}
		case isCodexHTTPFallbackToolCallType(itemType):
			if callID != "" && !codexHTTPFallbackToolCallHasMatchingOutput(itemType, callID, outputPresent) {
				continue
			}
		}
		filtered = append(filtered, item)
	}

	out, err := json.Marshal(filtered)
	if err != nil {
		return raw
	}
	return out
}

func stripCodexHTTPFallbackInputActions(raw []byte) []byte {
	items := gjson.ParseBytes(raw)
	if !items.IsArray() {
		return raw
	}
	out := bytes.Clone(raw)
	for i, item := range items.Array() {
		if !item.Get("action").Exists() {
			continue
		}
		updated, err := sjson.DeleteBytes(out, strconv.Itoa(i)+".action")
		if err == nil {
			out = updated
		}
	}
	return out
}

func stripCodexHTTPFallbackItemAction(item json.RawMessage) json.RawMessage {
	if len(item) == 0 || !gjson.GetBytes(item, "action").Exists() {
		return item
	}
	updated, err := sjson.DeleteBytes(item, "action")
	if err != nil {
		return item
	}
	return updated
}

func isCodexHTTPFallbackToolCallType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call", "custom_tool_call", "tool_search_call", "local_shell_call":
		return true
	default:
		return false
	}
}

func isCodexHTTPFallbackToolOutputType(itemType string) bool {
	switch strings.TrimSpace(itemType) {
	case "function_call_output", "custom_tool_call_output", "tool_search_output":
		return true
	default:
		return false
	}
}

func recordCodexHTTPFallbackToolItemPresence(present map[string]map[string]struct{}, callID string, itemType string) {
	callID = strings.TrimSpace(callID)
	itemType = strings.TrimSpace(itemType)
	if callID == "" || itemType == "" {
		return
	}
	types := present[callID]
	if types == nil {
		types = make(map[string]struct{}, 1)
		present[callID] = types
	}
	types[itemType] = struct{}{}
}

func codexHTTPFallbackToolOutputDoesNotRequireLocalCall(itemType string, item []byte) bool {
	return strings.TrimSpace(itemType) == "tool_search_output" &&
		strings.EqualFold(strings.TrimSpace(gjson.GetBytes(item, "execution").String()), "server")
}

func codexHTTPFallbackToolOutputHasMatchingCall(outputType string, callID string, callPresent map[string]map[string]struct{}) bool {
	for callType := range callPresent[strings.TrimSpace(callID)] {
		if codexHTTPFallbackToolOutputMatchesCall(outputType, callType) {
			return true
		}
	}
	return false
}

func codexHTTPFallbackToolCallHasMatchingOutput(callType string, callID string, outputPresent map[string]map[string]struct{}) bool {
	for outputType := range outputPresent[strings.TrimSpace(callID)] {
		if codexHTTPFallbackToolOutputMatchesCall(outputType, callType) {
			return true
		}
	}
	return false
}

func codexHTTPFallbackToolOutputMatchesCall(outputType string, callType string) bool {
	switch strings.TrimSpace(outputType) {
	case "function_call_output":
		switch strings.TrimSpace(callType) {
		case "function_call", "local_shell_call":
			return true
		default:
			return false
		}
	case "custom_tool_call_output":
		return strings.TrimSpace(callType) == "custom_tool_call"
	case "tool_search_output":
		return strings.TrimSpace(callType) == "tool_search_call"
	default:
		return false
	}
}
