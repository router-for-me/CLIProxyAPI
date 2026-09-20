package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

type codexClaudeContentBlock struct {
	Index     int64
	Type      string
	ID        string
	Name      string
	Text      string
	Arguments string
}

func translateCodexClaudeChunks(t *testing.T, chunks [][]byte) [][]byte {
	t.Helper()

	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Read"}]}`)
	var state any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertCodexResponseToClaude(context.Background(), "gpt-5", originalRequest, nil, chunk, &state)...)
	}
	return outputs
}

func assertCodexClaudeContentBlockLifecycle(t *testing.T, outputs [][]byte) []*codexClaudeContentBlock {
	t.Helper()

	open := make(map[int64]*codexClaudeContentBlock)
	started := make(map[int64]struct{})
	blocks := make([]*codexClaudeContentBlock, 0)
	messageState := 0
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
				block := &codexClaudeContentBlock{
					Index: index,
					Type:  event.Get("content_block.type").String(),
					ID:    event.Get("content_block.id").String(),
					Name:  event.Get("content_block.name").String(),
				}
				open[index] = block
				started[index] = struct{}{}
				blocks = append(blocks, block)
			case "content_block_delta":
				block := open[index]
				if block == nil {
					t.Fatalf("content block delta targets unopened index %d", index)
				}
				switch event.Get("delta.type").String() {
				case "input_json_delta":
					block.Arguments += event.Get("delta.partial_json").String()
				case "text_delta":
					block.Text += event.Get("delta.text").String()
				}
			case "content_block_stop":
				if open[index] == nil {
					t.Fatalf("content block stop targets unopened index %d", index)
				}
				delete(open, index)
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
	if len(open) != 0 {
		t.Fatalf("content blocks remain open: %v", open)
	}
	return blocks
}

func assertParallelCodexClaudeToolCalls(t *testing.T, blocks []*codexClaudeContentBlock) {
	t.Helper()

	if len(blocks) != 2 {
		t.Fatalf("content block count = %d, want 2", len(blocks))
	}
	expectedIDs := []string{"call_a", "call_b"}
	expectedArguments := []string{`{"file_path":"a"}`, `{"file_path":"b"}`}
	for index, block := range blocks {
		if block.Index != int64(index) {
			t.Fatalf("block %d index = %d, want %d", index, block.Index, index)
		}
		if block.Type != "tool_use" || block.Name != "Read" {
			t.Fatalf("block %d = %#v, want Read tool_use", index, block)
		}
		if block.ID != expectedIDs[index] {
			t.Fatalf("block %d ID = %q, want %q", index, block.ID, expectedIDs[index])
		}
		if block.Arguments != expectedArguments[index] {
			t.Fatalf("block %d arguments = %q, want %q", index, block.Arguments, expectedArguments[index])
		}
	}
}

func TestConvertCodexResponseToClaude_StreamSerializesInterleavedNamedFunctionCalls(t *testing.T) {
	tests := []struct {
		name   string
		chunks [][]byte
	}{
		{
			name: "first call finishes first",
			chunks: [][]byte{
				[]byte(`data: {"type":"response.created","response":{"id":"resp_parallel","model":"gpt-5"}}`),
				[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a","name":"Read"},"output_index":1}`),
				[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_b","name":"Read"},"output_index":2}`),
				[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"file_path\":\"a\"}","output_index":1}`),
				[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"file_path\":\"b\"}","output_index":2}`),
				[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"file_path\":\"a\"}","output_index":1}`),
				[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{\"file_path\":\"a\"}"},"output_index":1}`),
				[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"file_path\":\"b\"}","output_index":2}`),
				[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{\"file_path\":\"b\"}"},"output_index":2}`),
			},
		},
		{
			name: "second call finishes first",
			chunks: [][]byte{
				[]byte(`data: {"type":"response.created","response":{"id":"resp_parallel","model":"gpt-5"}}`),
				[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a","name":"Read"},"output_index":1}`),
				[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_b","name":"Read"},"output_index":2}`),
				[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"file_path\":\"b\"}","output_index":2}`),
				[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"file_path\":\"b\"}","output_index":2}`),
				[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{\"file_path\":\"b\"}"},"output_index":2}`),
				[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"file_path\":\"a\"}","output_index":1}`),
				[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"file_path\":\"a\"}","output_index":1}`),
				[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{\"file_path\":\"a\"}"},"output_index":1}`),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blocks := assertCodexClaudeContentBlockLifecycle(t, translateCodexClaudeChunks(t, test.chunks))
			assertParallelCodexClaudeToolCalls(t, blocks)
		})
	}
}

func TestConvertCodexResponseToClaude_StreamDefersOtherContentUntilFunctionCallsClose(t *testing.T) {
	tests := []struct {
		name         string
		functionCall []byte
		firstBlock   string
		secondBlock  string
	}{
		{
			name:         "named active call",
			functionCall: []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a","name":"Read"},"output_index":0}`),
			firstBlock:   "tool_use",
			secondBlock:  "text",
		},
		{
			name:         "unnamed pending call",
			functionCall: []byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a"},"output_index":0}`),
			firstBlock:   "text",
			secondBlock:  "tool_use",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			chunks := [][]byte{
				[]byte(`data: {"type":"response.created","response":{"id":"resp_mixed","model":"gpt-5"}}`),
				test.functionCall,
				[]byte(`data: {"type":"response.output_item.added","item":{"type":"message","status":"in_progress"},"output_index":1}`),
				[]byte(`data: {"type":"response.content_part.added","part":{"type":"output_text"},"content_index":0,"output_index":1}`),
				[]byte(`data: {"type":"response.output_text.delta","delta":"done","output_index":1}`),
				[]byte(`data: {"type":"response.content_part.done","part":{"type":"output_text"},"content_index":0,"output_index":1}`),
				[]byte(`data: {"type":"response.output_item.done","item":{"type":"message","status":"completed"},"output_index":1}`),
				[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"file_path\":\"a\"}","output_index":0}`),
				[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{\"file_path\":\"a\"}"},"output_index":0}`),
				[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
			}

			blocks := assertCodexClaudeContentBlockLifecycle(t, translateCodexClaudeChunks(t, chunks))
			if len(blocks) != 2 {
				t.Fatalf("content block count = %d, want 2", len(blocks))
			}
			if blocks[0].Index != 0 || blocks[0].Type != test.firstBlock {
				t.Fatalf("unexpected first block: %#v", blocks[0])
			}
			if blocks[1].Index != 1 || blocks[1].Type != test.secondBlock {
				t.Fatalf("unexpected second block: %#v", blocks[1])
			}
			for _, block := range blocks {
				switch block.Type {
				case "tool_use":
					if block.Arguments != `{"file_path":"a"}` {
						t.Fatalf("unexpected tool block: %#v", block)
					}
				case "text":
					if block.Text != "done" {
						t.Fatalf("unexpected text block: %#v", block)
					}
				}
			}
		})
	}
}

func TestConvertCodexResponseToClaude_StreamDeferredTextClosesBeforeThinkingStarts(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_mixed","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a","name":"Read"},"output_index":0}`),
		[]byte(`data: {"type":"response.content_part.added","part":{"type":"output_text"},"content_index":0,"output_index":1}`),
		[]byte(`data: {"type":"response.output_text.delta","delta":"answer","output_index":1}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"reasoning","encrypted_content":"enc_initial"},"output_index":2}`),
		[]byte(`data: {"type":"response.reasoning_summary_part.added","output_index":2}`),
		[]byte(`data: {"type":"response.reasoning_summary_text.delta","delta":"thought","output_index":2}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"reasoning","encrypted_content":"enc_final"},"output_index":2}`),
		[]byte(`data: {"type":"response.function_call_arguments.done","arguments":"{\"file_path\":\"a\"}","output_index":0}`),
		[]byte(`data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{\"file_path\":\"a\"}"},"output_index":0}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1}}}`),
	}

	blocks := assertCodexClaudeContentBlockLifecycle(t, translateCodexClaudeChunks(t, chunks))
	if len(blocks) != 3 {
		t.Fatalf("content block count = %d, want 3", len(blocks))
	}
	if blocks[0].Index != 0 || blocks[0].Type != "tool_use" || blocks[0].Arguments != `{"file_path":"a"}` {
		t.Fatalf("unexpected tool block: %#v", blocks[0])
	}
	if blocks[1].Index != 1 || blocks[1].Type != "text" || blocks[1].Text != "answer" {
		t.Fatalf("unexpected text block: %#v", blocks[1])
	}
	if blocks[2].Index != 2 || blocks[2].Type != "thinking" {
		t.Fatalf("unexpected thinking block: %#v", blocks[2])
	}
}

func TestConvertCodexResponseToClaude_StreamTerminalMatchesFunctionCallsByOutputIndex(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"type":"response.created","response":{"id":"resp_parallel","model":"gpt-5"}}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"Read"},"output_index":0}`),
		[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","name":"Read"},"output_index":1}`),
		[]byte(`data: {"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"function_call","name":"Read","arguments":"{\"file_path\":\"a\"}"},{"type":"function_call","name":"Read","arguments":"{\"file_path\":\"b\"}"}]}}`),
	}

	blocks := assertCodexClaudeContentBlockLifecycle(t, translateCodexClaudeChunks(t, chunks))
	if len(blocks) != 2 {
		t.Fatalf("content block count = %d, want 2", len(blocks))
	}
	if blocks[0].Index != 0 || blocks[0].Arguments != `{"file_path":"a"}` {
		t.Fatalf("unexpected first function call: %#v", blocks[0])
	}
	if blocks[1].Index != 1 || blocks[1].Arguments != `{"file_path":"b"}` {
		t.Fatalf("unexpected second function call: %#v", blocks[1])
	}
}

func TestConvertCodexResponseToClaude_StreamTerminalHydratesInterleavedFunctionCalls(t *testing.T) {
	for _, terminalType := range []string{"response.completed", "response.incomplete"} {
		t.Run(terminalType, func(t *testing.T) {
			terminal := `data: {"type":"` + terminalType + `","response":{"usage":{"input_tokens":1,"output_tokens":1},"output":[{"type":"function_call","call_id":"call_a","name":"Read","arguments":"{\"file_path\":\"a\"}"},{"type":"function_call","call_id":"call_b","name":"Read","arguments":"{\"file_path\":\"b\"}"}]}}`
			chunks := [][]byte{
				[]byte(`data: {"type":"response.created","response":{"id":"resp_parallel","model":"gpt-5"}}`),
				[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_a","name":"Read"},"output_index":0}`),
				[]byte(`data: {"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_b","name":"Read"},"output_index":1}`),
				[]byte(`data: {"type":"response.function_call_arguments.delta","delta":"{\"file_path\":","output_index":0}`),
				[]byte(terminal),
			}

			blocks := assertCodexClaudeContentBlockLifecycle(t, translateCodexClaudeChunks(t, chunks))
			assertParallelCodexClaudeToolCalls(t, blocks)
		})
	}
}
