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
