package claude

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToClaudeNonStream_SkipsToolCallsWithEmptyName(t *testing.T) {
	originalRequest := []byte(`{"tools":[{"name":"Read"}]}`)
	rawJSON := []byte(`{
		"id":"msg_1",
		"model":"gpt-4.1",
		"choices":[{
			"finish_reason":"tool_calls",
			"message":{
				"tool_calls":[
					{"id":"call_1","function":{"name":"","arguments":"{\"foo\":1}"}},
					{"id":"call_2","function":{"name":"Read","arguments":"{\"path\":\"a.txt\"}"}}
				]
			}
		}]
	}`)

	out := ConvertOpenAIResponseToClaudeNonStream(context.Background(), "", originalRequest, nil, rawJSON, nil)
	parsed := gjson.ParseBytes(out)

	content := parsed.Get("content")
	if !content.Exists() || len(content.Array()) != 1 {
		t.Fatalf("expected exactly 1 content block, got %s", content.Raw)
	}
	if got := content.Get("0.type").String(); got != "tool_use" {
		t.Fatalf("expected tool_use block, got %q", got)
	}
	if got := content.Get("0.name").String(); got != "Read" {
		t.Fatalf("expected tool name Read, got %q", got)
	}
}

func TestConvertOpenAIResponseToClaude_StreamSkipsToolCallsWithEmptyName(t *testing.T) {
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Read"}]}`)
	chunk := []byte(`data: {
		"id":"chatcmpl_1",
		"model":"gpt-4.1",
		"created":1,
		"choices":[{
			"delta":{
				"tool_calls":[
					{"index":0,"id":"call_1","function":{"name":"","arguments":"{\"foo\":1}"}},
					{"index":1,"id":"call_2","function":{"name":"Read","arguments":"{\"path\":\"a.txt\"}"}}
				]
			}
		}]
	}`)

	var param any
	chunks := ConvertOpenAIResponseToClaude(context.Background(), "", originalRequest, nil, chunk, &param)
	if len(chunks) == 0 {
		t.Fatalf("expected streaming chunks")
	}

	foundRead := false
	for _, item := range chunks {
		t.Logf("chunk: %s", item)
		parsed := parseSSEData(item)
		if parsed.Get("type").String() != "content_block_start" {
			continue
		}
		if parsed.Get("content_block.type").String() != "tool_use" {
			continue
		}
		name := parsed.Get("content_block.name").String()
		if name == "" {
			t.Fatalf("unexpected empty tool name in chunk: %s", parsed.Raw)
		}
		if name == "Read" {
			foundRead = true
		}
	}

	if !foundRead {
		t.Fatalf("expected tool_use chunk for Read")
	}
}

func parseSSEData(chunk []byte) gjson.Result {
	const dataPrefix = "data: "
	for _, line := range bytes.Split(bytes.TrimSpace(chunk), []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte(dataPrefix)) {
			return gjson.ParseBytes(bytes.TrimPrefix(line, []byte(dataPrefix)))
		}
	}
	return gjson.Result{}
}
