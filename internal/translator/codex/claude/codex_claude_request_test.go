package claude

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodex_DeferLoading_InitialRequest(t *testing.T) {
	input := []byte(`{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]}
		],
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search for tools",
				"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}
			},
			{
				"name": "Read",
				"description": "Read a file from the filesystem.",
				"input_schema": {"type": "object", "properties": {"file_path": {"type": "string"}}},
				"defer_loading": true
			}
		]
	}`)

	output := ConvertClaudeRequestToCodex("test-model", input, false)

	if !gjson.Valid(string(output)) {
		t.Fatal("output is not valid JSON")
	}

	toolsResult := gjson.GetBytes(output, "tools")
	if !toolsResult.Exists() || !toolsResult.IsArray() {
		t.Fatal("tools array is missing")
	}

	tools := toolsResult.Array()
	if len(tools) != 1 {
		t.Errorf("expected 1 tool in output, got %d", len(tools))
	}

	if gjson.GetBytes(output, "tools.0.name").String() != "ToolSearch" {
		t.Errorf("expected tools.0.name to be ToolSearch, got %s", gjson.GetBytes(output, "tools.0.name").String())
	}

	for _, tool := range tools {
		if tool.Get("name").String() == "Read" {
			t.Error("Read tool should not appear in output tools (deferred, not loaded)")
		}
	}
}

func TestConvertClaudeRequestToCodex_DeferLoading_WithToolReference(t *testing.T) {
	input := []byte(`{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "tu_1", "name": "ToolSearch", "input": {"query": "select:Read"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu_1", "content": [{"type": "tool_reference", "tool_name": "Read"}]}]}
		],
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search for tools",
				"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}
			},
			{
				"name": "Read",
				"description": "Reads a file from the local filesystem.",
				"input_schema": {"type": "object", "properties": {"file_path": {"type": "string", "description": "The absolute path to the file"}}},
				"defer_loading": true
			}
		]
	}`)

	output := ConvertClaudeRequestToCodex("test-model", input, false)

	if !gjson.Valid(string(output)) {
		t.Fatal("output is not valid JSON")
	}

	// Both tools should be present after loading Read via tool_reference.
	tools := gjson.GetBytes(output, "tools").Array()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools in output (ToolSearch + Read), got %d", len(tools))
	}

	// Locate the function_call_output message in the input array.
	var toolOutputMsg gjson.Result
	for _, item := range gjson.GetBytes(output, "input").Array() {
		if item.Get("type").String() == "function_call_output" {
			toolOutputMsg = item
			break
		}
	}
	if !toolOutputMsg.Exists() {
		t.Fatal("function_call_output message not found in output input array")
	}

	// output field must not contain the raw tool_reference JSON.
	if strings.Contains(toolOutputMsg.Get("output").Raw, "tool_reference") {
		t.Error("output should not contain raw tool_reference JSON")
	}

	if toolOutputMsg.Get("output.0.type").String() != "input_text" {
		t.Errorf("expected output.0.type to be input_text, got %s", toolOutputMsg.Get("output.0.type").String())
	}

	text := toolOutputMsg.Get("output.0.text").String()
	if !strings.HasPrefix(text, "Tool 'Read' is now available.") {
		t.Errorf("expected text to start with \"Tool 'Read' is now available.\", got: %s", text)
	}
	if !strings.Contains(text, "Description:") {
		t.Error("expected text to contain 'Description:'")
	}
	if !strings.Contains(text, "Parameters:") {
		t.Error("expected text to contain 'Parameters:'")
	}
}

func TestConvertClaudeRequestToCodex_DeferLoading_MultipleTools(t *testing.T) {
	input := []byte(`{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "tu_1", "name": "ToolSearch", "input": {"query": "select:Read"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu_1", "content": [{"type": "tool_reference", "tool_name": "Read"}]}]}
		],
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search for tools",
				"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}
			},
			{
				"name": "Read",
				"description": "Read a file",
				"input_schema": {"type": "object", "properties": {"file_path": {"type": "string"}}},
				"defer_loading": true
			},
			{
				"name": "Bash",
				"description": "Run a bash command",
				"input_schema": {"type": "object", "properties": {"command": {"type": "string"}}},
				"defer_loading": true
			}
		]
	}`)

	output := ConvertClaudeRequestToCodex("test-model", input, false)

	if !gjson.Valid(string(output)) {
		t.Fatal("output is not valid JSON")
	}

	tools := gjson.GetBytes(output, "tools").Array()
	if len(tools) != 2 {
		t.Errorf("expected 2 tools (ToolSearch + Read), got %d", len(tools))
	}

	toolNames := map[string]bool{}
	for _, tool := range tools {
		toolNames[tool.Get("name").String()] = true
	}

	if !toolNames["ToolSearch"] {
		t.Error("ToolSearch should be in output tools")
	}
	if !toolNames["Read"] {
		t.Error("Read should be in output tools (loaded via tool_reference)")
	}
	if toolNames["Bash"] {
		t.Error("Bash should not be in output tools (deferred, not loaded)")
	}
}

// #5 — 同一 deferred 工具被 tool_reference 两次：工具只出现一次，两条 tool_result 均注入 schema 文本。
func TestConvertClaudeRequestToCodex_DeferLoading_DuplicateToolReference(t *testing.T) {
	input := []byte(`{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "tu_1", "name": "ToolSearch", "input": {"query": "select:Read"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu_1", "content": [{"type": "tool_reference", "tool_name": "Read"}]}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "tu_2", "name": "ToolSearch", "input": {"query": "select:Read"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu_2", "content": [{"type": "tool_reference", "tool_name": "Read"}]}]}
		],
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search for tools",
				"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}
			},
			{
				"name": "Read",
				"description": "Read a file",
				"input_schema": {"type": "object", "properties": {"file_path": {"type": "string"}}},
				"defer_loading": true
			}
		]
	}`)

	output := ConvertClaudeRequestToCodex("test-model", input, false)

	if !gjson.Valid(string(output)) {
		t.Fatal("output is not valid JSON")
	}

	// Read should appear exactly once in tools (not duplicated).
	outputTools := gjson.GetBytes(output, "tools").Array()
	if len(outputTools) != 2 {
		t.Errorf("expected 2 tools (ToolSearch + Read), got %d", len(outputTools))
	}
	readCount := 0
	for _, tool := range outputTools {
		if tool.Get("name").String() == "Read" {
			readCount++
		}
	}
	if readCount != 1 {
		t.Errorf("expected Read to appear exactly once in output tools, got %d", readCount)
	}

	// Both function_call_output messages must have schema text injected.
	var toolOutputMsgs []gjson.Result
	for _, item := range gjson.GetBytes(output, "input").Array() {
		if item.Get("type").String() == "function_call_output" {
			toolOutputMsgs = append(toolOutputMsgs, item)
		}
	}
	if len(toolOutputMsgs) != 2 {
		t.Fatalf("expected 2 function_call_output messages, got %d", len(toolOutputMsgs))
	}
	for i, msg := range toolOutputMsgs {
		text := msg.Get("output.0.text").String()
		if !strings.HasPrefix(text, "Tool 'Read' is now available.") {
			t.Errorf("function_call_output[%d]: expected text to start with \"Tool 'Read' is now available.\", got: %s", i, text)
		}
	}
}

// #6 — tool_reference 引用不在 tools 数组中的工具名：防御路径，只注入通知文本，无 schema 段，不 panic。
func TestConvertClaudeRequestToCodex_DeferLoading_UnknownToolReference(t *testing.T) {
	input := []byte(`{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "tu_1", "name": "ToolSearch", "input": {"query": "select:UnknownTool"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu_1", "content": [{"type": "tool_reference", "tool_name": "UnknownTool"}]}]}
		],
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search for tools",
				"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}
			}
		]
	}`)

	output := ConvertClaudeRequestToCodex("test-model", input, false)

	if !gjson.Valid(string(output)) {
		t.Fatal("output is not valid JSON")
	}

	var toolOutputMsg gjson.Result
	for _, item := range gjson.GetBytes(output, "input").Array() {
		if item.Get("type").String() == "function_call_output" {
			toolOutputMsg = item
			break
		}
	}
	if !toolOutputMsg.Exists() {
		t.Fatal("function_call_output message not found")
	}

	if toolOutputMsg.Get("output.0.type").String() != "input_text" {
		t.Errorf("expected output.0.type to be input_text, got %s", toolOutputMsg.Get("output.0.type").String())
	}

	text := toolOutputMsg.Get("output.0.text").String()
	const wantText = "Tool 'UnknownTool' is now available."
	if text != wantText {
		t.Errorf("expected output.0.text to be %q, got %q", wantText, text)
	}
	if strings.Contains(text, "Description:") {
		t.Error("expected no 'Description:' section for unknown tool (not in toolSchemaMap)")
	}
	if strings.Contains(text, "Parameters:") {
		t.Error("expected no 'Parameters:' section for unknown tool (not in toolSchemaMap)")
	}

	// UnknownTool is not in the input tools array, so it must not appear in output tools.
	outputTools := gjson.GetBytes(output, "tools").Array()
	if len(outputTools) != 1 {
		t.Errorf("expected 1 tool (ToolSearch only), got %d", len(outputTools))
	}
	if gjson.GetBytes(output, "tools.0.name").String() != "ToolSearch" {
		t.Errorf("expected tools.0.name to be ToolSearch, got %s", gjson.GetBytes(output, "tools.0.name").String())
	}
}

// #7 — 全部工具均为 deferred 且无 tool_reference：输出 tools 为空数组，结构合法。
func TestConvertClaudeRequestToCodex_DeferLoading_AllDeferredNoReference(t *testing.T) {
	input := []byte(`{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]}
		],
		"tools": [
			{
				"name": "Bash",
				"description": "Run a bash command",
				"input_schema": {"type": "object", "properties": {"command": {"type": "string"}}},
				"defer_loading": true
			},
			{
				"name": "Read",
				"description": "Read a file",
				"input_schema": {"type": "object", "properties": {"file_path": {"type": "string"}}},
				"defer_loading": true
			}
		]
	}`)

	output := ConvertClaudeRequestToCodex("test-model", input, false)

	if !gjson.Valid(string(output)) {
		t.Fatal("output is not valid JSON")
	}

	toolsResult := gjson.GetBytes(output, "tools")
	if !toolsResult.IsArray() {
		t.Fatal("tools field must be an array (not null) even when empty")
	}
	if len(toolsResult.Array()) != 0 {
		t.Errorf("expected empty tools array, got %d tools", len(toolsResult.Array()))
	}
	if gjson.GetBytes(output, "tool_choice").String() != "auto" {
		t.Errorf("expected tool_choice to be auto, got %s", gjson.GetBytes(output, "tool_choice").String())
	}
	if !gjson.GetBytes(output, "parallel_tool_calls").Bool() {
		t.Error("expected parallel_tool_calls to be true")
	}
}

// #8 — tool_reference 与 text 混合在同一 tool_result 的 content：两个内容块按顺序正确转换，索引不错位。
func TestConvertClaudeRequestToCodex_DeferLoading_MixedContentInToolResult(t *testing.T) {
	input := []byte(`{
		"model": "test-model",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "hello"}]},
			{"role": "assistant", "content": [{"type": "tool_use", "id": "tu_1", "name": "ToolSearch", "input": {"query": "select:Read"}}]},
			{"role": "user", "content": [{"type": "tool_result", "tool_use_id": "tu_1", "content": [
				{"type": "text", "text": "search done"},
				{"type": "tool_reference", "tool_name": "Read"}
			]}]}
		],
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search for tools",
				"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}
			},
			{
				"name": "Read",
				"description": "Reads a file from the local filesystem.",
				"input_schema": {"type": "object", "properties": {"file_path": {"type": "string", "description": "The absolute path"}}},
				"defer_loading": true
			}
		]
	}`)

	output := ConvertClaudeRequestToCodex("test-model", input, false)

	if !gjson.Valid(string(output)) {
		t.Fatal("output is not valid JSON")
	}

	var toolOutputMsg gjson.Result
	for _, item := range gjson.GetBytes(output, "input").Array() {
		if item.Get("type").String() == "function_call_output" {
			toolOutputMsg = item
			break
		}
	}
	if !toolOutputMsg.Exists() {
		t.Fatal("function_call_output message not found")
	}

	outputArr := toolOutputMsg.Get("output").Array()
	if len(outputArr) != 2 {
		t.Fatalf("expected output array length 2 (text + tool_reference), got %d", len(outputArr))
	}

	// Index 0: text block.
	if outputArr[0].Get("type").String() != "input_text" {
		t.Errorf("expected output[0].type to be input_text, got %s", outputArr[0].Get("type").String())
	}
	if outputArr[0].Get("text").String() != "search done" {
		t.Errorf("expected output[0].text to be 'search done', got %q", outputArr[0].Get("text").String())
	}

	// Index 1: tool_reference → schema text block.
	if outputArr[1].Get("type").String() != "input_text" {
		t.Errorf("expected output[1].type to be input_text, got %s", outputArr[1].Get("type").String())
	}
	refText := outputArr[1].Get("text").String()
	if !strings.HasPrefix(refText, "Tool 'Read' is now available.") {
		t.Errorf("expected output[1].text to start with \"Tool 'Read' is now available.\", got: %s", refText)
	}
	if !strings.Contains(refText, "Description:") {
		t.Error("expected output[1].text to contain 'Description:'")
	}
	if !strings.Contains(refText, "Parameters:") {
		t.Error("expected output[1].text to contain 'Parameters:'")
	}

	// Read must be present in output tools (loaded via this tool_reference).
	toolNames := map[string]bool{}
	for _, tool := range gjson.GetBytes(output, "tools").Array() {
		toolNames[tool.Get("name").String()] = true
	}
	if !toolNames["Read"] {
		t.Error("Read should be in output tools after tool_reference")
	}
}
