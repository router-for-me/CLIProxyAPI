package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponseToClaude_StreamSkipsNullToolNameDelta(t *testing.T) {
	ctx := context.Background()
	originalRequest := []byte(`{"stream":true,"tools":[{"name":"Bash","input_schema":{"type":"object"}}]}`)
	var param any

	chunks := [][]byte{
		[]byte(`data: {"id":"chatcmpl-1","model":"test-model","created":1,"choices":[{"delta":{"role":"assistant"}}]}`),
		[]byte(`data: {"id":"chatcmpl-1","model":"test-model","created":1,"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"Bash","arguments":"{\"command\":"}}]}}]}`),
		[]byte(`data: {"id":"chatcmpl-1","model":"test-model","created":1,"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":null,"arguments":"\"pwd\"}"}}]}}]}`),
		[]byte(`data: {"id":"chatcmpl-1","model":"test-model","created":1,"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		[]byte(`data: [DONE]`),
	}

	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertOpenAIResponseToClaude(ctx, "", originalRequest, nil, chunk, &param)...)
	}

	toolStartCount := 0
	foundInputDelta := false
	foundToolStopReason := false
	for _, out := range outputs {
		for _, line := range strings.Split(string(out), "\n") {
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := gjson.Parse(strings.TrimPrefix(line, "data: "))
			switch data.Get("type").String() {
			case "content_block_start":
				if data.Get("content_block.type").String() != "tool_use" {
					continue
				}
				toolStartCount++
				if got := data.Get("content_block.name").String(); got != "Bash" {
					t.Fatalf("expected only non-empty tool name Bash, got %q in %s", got, line)
				}
			case "content_block_delta":
				if data.Get("delta.type").String() == "input_json_delta" {
					foundInputDelta = true
					if got := data.Get("delta.partial_json").String(); got != `{"command":"pwd"}` {
						t.Fatalf("expected accumulated arguments to be preserved, got %q", got)
					}
				}
			case "message_delta":
				if data.Get("delta.stop_reason").String() == "tool_use" {
					foundToolStopReason = true
				}
			}
		}
	}

	if toolStartCount != 1 {
		t.Fatalf("expected exactly one tool_use content_block_start, got %d", toolStartCount)
	}
	if !foundInputDelta {
		t.Fatal("expected input_json_delta with accumulated arguments")
	}
	if !foundToolStopReason {
		t.Fatal("expected message_delta stop_reason tool_use")
	}
}
