package responses

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

func traceOpenAIResponsesRequest(original []byte, converted []byte) {
	if !cursorToolTraceEnabled() {
		return
	}

	fields := log.Fields{
		"orig_model":                 summarizeJSONField(original, "model"),
		"orig_reasoning_effort":      summarizeReasoningEffort(original),
		"orig_tools":                 countArray(original, "tools"),
		"orig_tool_choice":           summarizeJSONField(original, "tool_choice"),
		"orig_input":                 countArray(original, "input"),
		"orig_last_input_type":       lastInputType(original),
		"orig_last_call_id":          lastInputCallID(original),
		"converted_model":            summarizeJSONField(converted, "model"),
		"converted_reasoning_effort": summarizeReasoningEffort(converted),
		"converted_tools":            countArray(converted, "tools"),
		"converted_tool_choice":      summarizeJSONField(converted, "tool_choice"),
		"converted_input":            countArray(converted, "input"),
		"converted_last_call_id":     lastInputCallID(converted),
	}
	log.Infof("cursor tool trace: responses request %s", formatTraceFields(fields))
}

func traceOpenAIResponsesResponse(rawJSON []byte) {
	if !cursorToolTraceEnabled() {
		return
	}

	dataType := gjson.GetBytes(rawJSON, "type").String()
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
		"event": dataType,
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
	log.Infof("cursor tool trace: responses response %s", formatTraceFields(fields))
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

func summarizeReasoningEffort(raw []byte) string {
	if effort := gjson.GetBytes(raw, "reasoning.effort"); effort.Exists() {
		return summarizeJSONField(raw, "reasoning.effort")
	}
	return summarizeJSONField(raw, "reasoning_effort")
}

func lastInputType(raw []byte) string {
	input := gjson.GetBytes(raw, "input")
	if !input.IsArray() {
		return ""
	}
	items := input.Array()
	if len(items) == 0 {
		return ""
	}
	return items[len(items)-1].Get("type").String()
}

func lastInputCallID(raw []byte) string {
	input := gjson.GetBytes(raw, "input")
	if !input.IsArray() {
		return ""
	}
	items := input.Array()
	for i := len(items) - 1; i >= 0; i-- {
		if callID := items[i].Get("call_id").String(); callID != "" {
			return lengthBucket(callID)
		}
	}
	return ""
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
		"glob_pattern",
		"globPattern",
		"filePattern",
		"cwd",
		"target_directory",
		"targetDirectory",
		"workspace_path",
		"workspacePath",
		"relative_workspace_path",
		"relativeWorkspacePath",
		"regex",
		"include_pattern",
		"includePattern",
		"exclude_pattern",
		"excludePattern",
		"environment",
		"cloud_base_branch",
		"subagent_type",
		"run_in_background",
		"readonly",
		"resume",
		"model",
		"description",
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
