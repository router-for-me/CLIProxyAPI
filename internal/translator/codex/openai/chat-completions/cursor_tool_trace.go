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
		"orig_last_tool_output":    lastToolOutputSummary(original),
		"orig_last_tool_class":     lastToolOutputClass(original),
		"orig_last_tool_failure":   lastToolOutputFailureHint(original),
		"converted_tools":          countArray(converted, "tools"),
		"converted_tool_choice":    summarizeJSONField(converted, "tool_choice"),
		"converted_input":          countArray(converted, "input"),
		"converted_last_types":     lastInputTypes(converted, 6),
		"converted_last_output":    lastFunctionOutputSummary(converted),
		"converted_output_matched": lastFunctionOutputHasMatchingCall(converted),
	}
	log.Infof("cursor tool trace: chat request %s", formatTraceFields(fields))
}

func traceOpenAIChatResponse(dataType string, useLegacy bool, rawJSON []byte, out [][]byte) {
	if !cursorToolTraceEnabled() {
		return
	}
	if dataType != "response.output_item.added" &&
		dataType != "response.output_item.done" &&
		dataType != "response.function_call_arguments.delta" &&
		dataType != "response.custom_tool_call_input.delta" &&
		dataType != "response.custom_tool_call_input.done" &&
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
	case "response.output_item.added", "response.output_item.done":
		fields["item_type"] = gjson.GetBytes(rawJSON, "item.type").String()
		fields["item_name"] = gjson.GetBytes(rawJSON, "item.name").String()
		fields["item_input"] = lengthBucket(gjson.GetBytes(rawJSON, "item.input").String())
		fields["item_arguments"] = lengthBucket(gjson.GetBytes(rawJSON, "item.arguments").String())
		fields["item_arg_hint"] = summarizeToolArguments(rawJSON)
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		fields["arguments_delta"] = true
	case "response.custom_tool_call_input.done":
		fields["input_done"] = lengthBucket(gjson.GetBytes(rawJSON, "input").String())
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

func lastToolOutputSummary(raw []byte) string {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return ""
	}
	items := messages.Array()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Get("role").String() != "tool" {
			continue
		}
		return resultLengthSummary(items[i].Get("content"))
	}
	return ""
}

func lastToolOutputClass(raw []byte) string {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return ""
	}
	items := messages.Array()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Get("role").String() != "tool" {
			continue
		}
		return classifyToolOutput(items[i].Get("content"))
	}
	return ""
}

func lastToolOutputFailureHint(raw []byte) string {
	messages := gjson.GetBytes(raw, "messages")
	if !messages.IsArray() {
		return ""
	}
	items := messages.Array()
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Get("role").String() != "tool" {
			continue
		}
		content := items[i].Get("content")
		class := classifyToolOutput(content)
		if class == "" || class == "ok" {
			return ""
		}
		return compactTraceText(class+":"+toolOutputText(content), 240)
	}
	return ""
}

func lastInputTypes(raw []byte, limit int) []string {
	input := gjson.GetBytes(raw, "input")
	if !input.IsArray() {
		return nil
	}
	items := input.Array()
	if len(items) == 0 {
		return nil
	}
	start := len(items) - limit
	if start < 0 {
		start = 0
	}
	types := make([]string, 0, len(items)-start)
	for _, item := range items[start:] {
		itemType := item.Get("type").String()
		if itemType == "" {
			itemType = "<missing>"
		}
		types = append(types, itemType)
	}
	return types
}

func lastFunctionOutputSummary(raw []byte) string {
	input := gjson.GetBytes(raw, "input")
	if !input.IsArray() {
		return ""
	}
	items := input.Array()
	for i := len(items) - 1; i >= 0; i-- {
		itemType := items[i].Get("type").String()
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			continue
		}
		return resultLengthSummary(items[i].Get("output"))
	}
	return ""
}

func lastFunctionOutputHasMatchingCall(raw []byte) bool {
	input := gjson.GetBytes(raw, "input")
	if !input.IsArray() {
		return false
	}
	items := input.Array()
	outputCallID := ""
	for i := len(items) - 1; i >= 0; i-- {
		itemType := items[i].Get("type").String()
		if itemType != "function_call_output" && itemType != "custom_tool_call_output" {
			continue
		}
		outputCallID = items[i].Get("call_id").String()
		break
	}
	if outputCallID == "" {
		return false
	}
	for i := len(items) - 1; i >= 0; i-- {
		itemType := items[i].Get("type").String()
		if itemType != "function_call" && itemType != "custom_tool_call" {
			continue
		}
		if items[i].Get("call_id").String() == outputCallID {
			return true
		}
	}
	return false
}

func resultLengthSummary(result gjson.Result) string {
	if !result.Exists() {
		return "<missing>"
	}
	if result.IsArray() {
		return "array_len=" + stringInt(len(result.Array())) + ",raw_" + lengthBucket(result.Raw)
	}
	if result.IsObject() {
		return "object,raw_" + lengthBucket(result.Raw)
	}
	return lengthBucket(result.String())
}

func classifyToolOutput(result gjson.Result) string {
	if !result.Exists() {
		return "missing"
	}
	text := toolOutputText(result)
	if text == "" {
		return "empty"
	}
	if len(text) > 4096 {
		text = text[:4096]
	}
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "enoent") ||
		strings.Contains(lower, "not found") ||
		strings.Contains(lower, "no such file") ||
		strings.Contains(lower, "does not exist") ||
		strings.Contains(lower, "cannot find"):
		return "not-found"
	case strings.Contains(lower, "no files") ||
		strings.Contains(lower, "no matches") ||
		strings.Contains(lower, "found 0"):
		return "empty-glob"
	case strings.Contains(lower, "permission") ||
		strings.Contains(lower, "access denied") ||
		strings.Contains(lower, "eperm") ||
		strings.Contains(lower, "eacces"):
		return "permission"
	case strings.Contains(lower, "outside") && strings.Contains(lower, "workspace"):
		return "outside-workspace"
	case strings.Contains(lower, "error") ||
		strings.Contains(lower, "failed"):
		return "error"
	default:
		return "ok"
	}
}

func toolOutputText(result gjson.Result) string {
	if !result.Exists() {
		return ""
	}
	if result.IsArray() {
		parts := make([]string, 0)
		for _, item := range result.Array() {
			if text := item.Get("text").String(); text != "" {
				parts = append(parts, text)
				continue
			}
			if text := item.Get("content").String(); text != "" {
				parts = append(parts, text)
				continue
			}
			if item.Raw != "" {
				parts = append(parts, item.Raw)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, " ")
		}
	}
	if result.IsObject() {
		for _, key := range []string{"error", "message", "text", "content", "output"} {
			if text := result.Get(key).String(); text != "" {
				return text
			}
		}
	}
	text := result.String()
	if text == "" {
		text = result.Raw
	}
	return text
}

func summarizeToolArguments(rawJSON []byte) string {
	item := gjson.GetBytes(rawJSON, "item")
	arguments := item.Get("arguments").String()
	if arguments == "" {
		arguments = item.Get("input").String()
	}
	if arguments == "" {
		return ""
	}
	args := gjson.Parse(arguments)
	if !args.IsObject() {
		return lengthBucket(arguments)
	}
	values := make([]string, 0)
	for _, key := range []string{
		"path",
		"file_path",
		"filePath",
		"filepath",
		"file",
		"query",
		"pattern",
		"glob",
		"cwd",
		"relative_workspace_path",
	} {
		value := args.Get(key)
		if !value.Exists() {
			continue
		}
		values = append(values, key+"="+compactTraceText(value.String(), 160))
	}
	if command := args.Get("command").String(); command != "" {
		values = append(values, "command_"+lengthBucket(command))
	}
	if len(values) > 0 {
		return strings.Join(values, ",")
	}
	keys := make([]string, 0, len(args.Map()))
	for key := range args.Map() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		return "<object>"
	}
	return "keys=" + strings.Join(keys, ",")
}

func compactTraceText(value string, maxRunes int) string {
	value = strings.Join(strings.Fields(value), "_")
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxRunes {
		value = string(runes[:maxRunes]) + "..."
	}
	return value
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
