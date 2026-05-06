package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_SkipsUnnamedTools(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
		"tools":[
			{"type":"web_search_preview"},
			{"type":"function","name":"search_files","description":"Search files","parameters":{"type":"object","properties":{"q":{"type":"string"}}}},
			{"type":"function","function":{"name":"nested_tool","description":"Nested","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}}
		]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, true)
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("tools length = %d, want 2: %s", len(tools), out)
	}
	if got := tools[0].Get("name").String(); got != "search_files" {
		t.Fatalf("tools.0.name = %q, want search_files", got)
	}
	if got := tools[0].Get("input_schema.properties.q.type").String(); got != "string" {
		t.Fatalf("tools.0 input schema q type = %q, want string", got)
	}
	if got := tools[1].Get("name").String(); got != "nested_tool" {
		t.Fatalf("tools.1.name = %q, want nested_tool", got)
	}
	if gjson.GetBytes(out, `tools.#(name="")`).Exists() {
		t.Fatalf("output should not contain unnamed tools: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ExtractsEmbeddedDataImageFromInputText(t *testing.T) {
	imageData := strings.Repeat("A", 512)
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{
			"role":"user",
			"content":[{
				"type":"input_text",
				"text":"before\n![screenshot](data:image/png;base64,` + imageData + `)\nafter"
			}]
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, true)
	content := gjson.GetBytes(out, "messages.0.content")
	if !content.IsArray() {
		t.Fatalf("message content should be an array after extracting an embedded image: %s", out)
	}
	parts := content.Array()
	if len(parts) != 3 {
		t.Fatalf("content parts length = %d, want 3: %s", len(parts), out)
	}
	if got := parts[0].Get("type").String(); got != "text" {
		t.Fatalf("content.0.type = %q, want text", got)
	}
	if got := parts[0].Get("text").String(); got != "before\n" {
		t.Fatalf("content.0.text = %q, want prefix text", got)
	}
	if got := parts[1].Get("type").String(); got != "image" {
		t.Fatalf("content.1.type = %q, want image", got)
	}
	if got := parts[1].Get("source.type").String(); got != "base64" {
		t.Fatalf("content.1.source.type = %q, want base64", got)
	}
	if got := parts[1].Get("source.media_type").String(); got != "image/png" {
		t.Fatalf("content.1.source.media_type = %q, want image/png", got)
	}
	if got := parts[1].Get("source.data").String(); got != imageData {
		t.Fatalf("content.1.source.data length = %d, want %d", len(got), len(imageData))
	}
	if got := parts[2].Get("text").String(); got != "\nafter" {
		t.Fatalf("content.2.text = %q, want suffix text", got)
	}
	for _, part := range parts {
		if strings.Contains(part.Get("text").String(), "data:image") {
			t.Fatalf("text part should not retain embedded data image URL: %s", out)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_LeavesShortDataImageTextLiteral(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{
			"role":"user",
			"content":[{
				"type":"input_text",
				"text":"Document this literal: data:image/png;base64,AAAA"
			}]
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, true)
	content := gjson.GetBytes(out, "messages.0.content")
	if content.Type != gjson.String {
		t.Fatalf("short literal data URL should remain legacy string content: %s", out)
	}
	if !strings.Contains(content.String(), "data:image/png;base64,AAAA") {
		t.Fatalf("short literal data URL missing from text content: %s", out)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ExtractsEmbeddedDataImageFromStringContent(t *testing.T) {
	imageData := strings.Repeat("A", 512)
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{
			"role":"user",
			"content":"before data:image/jpeg;base64,` + imageData + ` after"
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, true)
	content := gjson.GetBytes(out, "messages.0.content")
	if !content.IsArray() {
		t.Fatalf("string content should become an array after extracting an embedded image: %s", out)
	}
	parts := content.Array()
	if len(parts) != 3 {
		t.Fatalf("content parts length = %d, want 3: %s", len(parts), out)
	}
	if got := parts[1].Get("source.media_type").String(); got != "image/jpeg" {
		t.Fatalf("content.1.source.media_type = %q, want image/jpeg", got)
	}
	if got := parts[1].Get("source.data").String(); got != imageData {
		t.Fatalf("content.1.source.data length = %d, want %d", len(got), len(imageData))
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ExtractsEmbeddedDataImageFromFunctionCallOutput(t *testing.T) {
	imageData := strings.Repeat("A", 512)
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{
			"type":"function_call_output",
			"call_id":"call_123",
			"output":"before\n![screenshot](data:image/png;base64,` + imageData + `)\nafter"
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, true)
	toolResult := gjson.GetBytes(out, "messages.0.content.0")
	if got := toolResult.Get("type").String(); got != "tool_result" {
		t.Fatalf("messages.0.content.0.type = %q, want tool_result: %s", got, out)
	}
	if got := toolResult.Get("tool_use_id").String(); got != "call_123" {
		t.Fatalf("tool_result.tool_use_id = %q, want call_123", got)
	}
	content := toolResult.Get("content")
	if !content.IsArray() {
		t.Fatalf("tool_result content should be an array after extracting an embedded image: %s", out)
	}
	parts := content.Array()
	if len(parts) != 3 {
		t.Fatalf("tool_result content parts length = %d, want 3: %s", len(parts), out)
	}
	if got := parts[0].Get("type").String(); got != "text" {
		t.Fatalf("tool_result.content.0.type = %q, want text", got)
	}
	if got := parts[0].Get("text").String(); got != "before\n" {
		t.Fatalf("tool_result.content.0.text = %q, want prefix text", got)
	}
	if got := parts[1].Get("type").String(); got != "image" {
		t.Fatalf("tool_result.content.1.type = %q, want image", got)
	}
	if got := parts[1].Get("source.type").String(); got != "base64" {
		t.Fatalf("tool_result.content.1.source.type = %q, want base64", got)
	}
	if got := parts[1].Get("source.media_type").String(); got != "image/png" {
		t.Fatalf("tool_result.content.1.source.media_type = %q, want image/png", got)
	}
	if got := parts[1].Get("source.data").String(); got != imageData {
		t.Fatalf("tool_result.content.1.source.data length = %d, want %d", len(got), len(imageData))
	}
	if got := parts[2].Get("text").String(); got != "\nafter" {
		t.Fatalf("tool_result.content.2.text = %q, want suffix text", got)
	}
	for _, part := range parts {
		if strings.Contains(part.Get("text").String(), "data:image") {
			t.Fatalf("text part should not retain embedded data image URL: %s", out)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_LeavesShortDataImageFunctionCallOutputLiteral(t *testing.T) {
	input := []byte(`{
		"model":"claude-opus-4-7",
		"input":[{
			"type":"function_call_output",
			"call_id":"call_123",
			"output":"Document this literal: data:image/png;base64,AAAA"
		}]
	}`)

	out := ConvertOpenAIResponsesRequestToClaude("claude-opus-4-7", input, true)
	content := gjson.GetBytes(out, "messages.0.content.0.content")
	if content.Type != gjson.String {
		t.Fatalf("short literal data URL should remain string tool_result content: %s", out)
	}
	if !strings.Contains(content.String(), "data:image/png;base64,AAAA") {
		t.Fatalf("short literal data URL missing from tool_result content: %s", out)
	}
}
