package test

import (
	"context"
	"strings"
	"testing"

	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexToClaudeParallelFunctionCallsHaveValidLifecycle(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_parallel","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a","name":"Read"},"output_index":1}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_b","name":"Read"},"output_index":2}`),
		[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"file_path\":\"a\"}","output_index":1}`),
		[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"file_path\":\"a\"}","output_index":1}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{\"file_path\":\"a\"}"},"output_index":1}`),
		[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"file_path\":\"b\"}","output_index":2}`),
		[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"file_path\":\"b\"}","output_index":2}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{\"file_path\":\"b\"}"},"output_index":2}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{\"file_path\":\"a\"}"},{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{\"file_path\":\"b\"}"}]}}`),
	}

	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Read"}]}`)
	var state any
	open := make(map[int64]struct{})
	started := make(map[int64]struct{})
	toolIDs := make(map[int64]string)
	arguments := make(map[int64]string)
	var startIndices []int64
	var stopIndices []int64
	messageState := 0

	for _, chunk := range chunks {
		outputs := sdktranslator.TranslateStream(
			context.Background(),
			sdktranslator.FormatCodex,
			sdktranslator.FormatClaude,
			"gpt-5",
			originalRequest,
			nil,
			chunk,
			&state,
		)
		for _, output := range outputs {
			for _, line := range strings.Split(string(output), "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				event := gjson.Parse(strings.TrimPrefix(line, "data: "))
				if messageState == 2 {
					t.Fatalf("event emitted after message_stop: %s", event.Raw)
				}
				index := event.Get("index").Int()
				switch event.Get("type").String() {
				case "content_block_start":
					if messageState != 0 {
						t.Fatalf("content block started after message terminal events: %s", event.Raw)
					}
					if len(open) != 0 {
						t.Fatalf("content block start emitted while another block remains open: %v", open)
					}
					if _, exists := started[index]; exists {
						t.Fatalf("content block index %d was reused", index)
					}
					open[index] = struct{}{}
					started[index] = struct{}{}
					startIndices = append(startIndices, index)
					toolIDs[index] = event.Get("content_block.id").String()
				case "content_block_delta":
					if _, exists := open[index]; !exists {
						t.Fatalf("content block delta targets unopened index %d", index)
					}
					if event.Get("delta.type").String() == "input_json_delta" {
						arguments[index] += event.Get("delta.partial_json").String()
					}
				case "content_block_stop":
					if _, exists := open[index]; !exists {
						t.Fatalf("content block stop targets unopened index %d", index)
					}
					delete(open, index)
					stopIndices = append(stopIndices, index)
				case "message_delta":
					if len(open) != 0 {
						t.Fatalf("message_delta emitted while content blocks remain open: %v", open)
					}
					if messageState != 0 {
						t.Fatalf("duplicate or out-of-order message_delta: %s", event.Raw)
					}
					messageState = 1
				case "message_stop":
					if len(open) != 0 {
						t.Fatalf("message_stop emitted while content blocks remain open: %v", open)
					}
					if messageState != 1 {
						t.Fatalf("message_stop emitted before message_delta: %s", event.Raw)
					}
					messageState = 2
				}
			}
		}
	}

	if len(open) != 0 {
		t.Fatalf("content blocks remain open: %v", open)
	}
	if messageState != 2 {
		t.Fatalf("terminal message event state = %d, want message_delta followed by message_stop", messageState)
	}
	if len(startIndices) != 2 || startIndices[0] != 0 || startIndices[1] != 1 {
		t.Fatalf("start indices = %v, want [0 1]", startIndices)
	}
	if len(stopIndices) != 2 || stopIndices[0] != 0 || stopIndices[1] != 1 {
		t.Fatalf("stop indices = %v, want [0 1]", stopIndices)
	}
	if toolIDs[0] != "call_a" || toolIDs[1] != "call_b" {
		t.Fatalf("tool IDs = %v, want call_a and call_b", toolIDs)
	}
	if arguments[0] != `{"file_path":"a"}` || arguments[1] != `{"file_path":"b"}` {
		t.Fatalf("tool arguments = %v", arguments)
	}
}
