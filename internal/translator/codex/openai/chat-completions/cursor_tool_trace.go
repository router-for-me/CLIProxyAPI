package chat_completions

import (
	"fmt"
	"os"
	"sort"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func cursorToolTraceEnabled() bool {
	value := strings.TrimSpace(os.Getenv("CLIPROXY_CURSOR_TOOL_TRACE"))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}

func traceOpenAIChatRequest(original []byte, converted []byte) {
	if !cursorToolTraceEnabled() {
		return
	}

	fields := log.Fields{
		"orig_tools":               countArray(original, "tools"),
		"orig_functions":           countArray(original, "functions"),
		"orig_tool_choice":         summarizeJSONField(original, "tool_choice"),
		"orig_function_call":       summarizeJSONField(original, "function_call"),
		"orig_messages":            countArray(original, "messages"),
		"orig_last_role":           lastMessageRole(original),
		"orig_last_has_tool_calls": lastMessageHasArray(original, "tool_calls"),
		"orig_last_has_function":   lastMessageHasObject(original, "function_call"),
		"orig_last_tool_call_id":   lastToolCallID(original),
		"converted_tools":          countArray(converted, "tools"),
		"converted_tool_choice":    summarizeJSONField(converted, "tool_choice"),
		"converted_input":          countArray(converted, "input"),
	}
	log.Infof("cursor tool trace: chat request %s", formatTraceFields(fields))
}

func traceOpenAIChatResponse(dataType string, useLegacy bool, rawJSON []byte, out [][]byte) {
	if !cursorToolTraceEnabled() {
		return
	}
	if dataType != "response.output_item.added" &&
		dataType != "response.function_call_arguments.delta" &&
		dataType != "response.output_text.delta" &&
		dataType != "response.completed" {
		return
	}

	fields := log.Fields{
		"event":      dataType,
		"legacy":     useLegacy,
		"out_chunks": len(out),
	}
	switch dataType {
	case "response.output_item.added":
		fields["item_type"] = gjson.GetBytes(rawJSON, "item.type").String()
		fields["item_name"] = gjson.GetBytes(rawJSON, "item.name").String()
	case "response.function_call_arguments.delta":
		fields["arguments_delta"] = true
	case "response.output_text.delta":
		fields["text_delta"] = true
	case "response.completed":
		fields["response_status"] = gjson.GetBytes(rawJSON, "response.status").String()
		fields["output_types"] = responseOutputTypes(rawJSON)
	}
	log.Infof("cursor tool trace: codex response %s", formatTraceFields(fields))
}

func formatTraceFields(fields log.Fields) string {
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", key, fields[key]))
	}
	return strings.Join(parts, " ")
}

func countArray(raw []byte, path string) int {
	result := gjson.GetBytes(raw, path)
	if !result.IsArray() {
		return 0
	}
	return len(result.Array())
}

func summarizeJSONField(raw []byte, path string) string {
	result := gjson.GetBytes(raw, path)
	if !result.Exists() {
		return "<missing>"
	}
	if result.Type == gjson.String {
		return result.String()
	}
	if result.IsObject() {
		fieldType := result.Get("type").String()
		name := result.Get("name").String()
		if name == "" {
			name = result.Get("function.name").String()
		}
		if fieldType != "" || name != "" {
			return strings.TrimSpace(fieldType + ":" + name)
		}
		return "<object>"
	}
	return result.Type.String()
}

func lastMessageRole(raw []byte) string {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return ""
	}
	items := messages.Array()
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1].Get("role").String()
}

func lastMessageHasArray(raw []byte, path string) bool {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return false
	}
	items := messages.Array()
	if len(items) == 0 {
		return false
	}
	return items[len(items)-1].Get(path).IsArray()
}

func lastMessageHasObject(raw []byte, path string) bool {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return false
	}
	items := messages.Array()
	if len(items) == 0 {
		return false
	}
	return items[len(items)-1].Get(path).IsObject()
}

func lastToolCallID(raw []byte) string {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return ""
	}
	items := messages.Array()
	for i := len(items) - 1; i >= 0; i-- {
		role := items[i].Get("role").String()
		if role == "tool" {
			return lengthBucket(items[i].Get("tool_call_id").String())
		}
	}
	return ""
}

func lengthBucket(value string) string {
	if value == "" {
		return ""
	}
	return "len=" + stringInt(len(value))
}

func stringInt(value int) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buf := make([]byte, 0, 8)
	for value > 0 {
		buf = append(buf, digits[value%10])
		value /= 10
	}
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
}

func responseOutputTypes(raw []byte) []string {
	output := gjson.GetBytes(raw, "response.output")
	if !output.IsArray() {
		return nil
	}
	items := output.Array()
	types := make([]string, 0, len(items))
	for _, item := range items {
		types = append(types, item.Get("type").String())
	}
	return types
}
