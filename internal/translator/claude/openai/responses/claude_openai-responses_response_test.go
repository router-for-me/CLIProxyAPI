package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func parseClaudeResponsesSSEEvent(t *testing.T, chunk []byte) (string, gjson.Result) {
	t.Helper()

	var event string
	var data string
	for _, line := range strings.Split(string(chunk), "\n") {
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
			continue
		}
		if strings.HasPrefix(line, "data: ") {
			data = strings.TrimPrefix(line, "data: ")
		}
	}
	if data == "" {
		t.Fatalf("SSE chunk has no data line: %s", string(chunk))
	}

	return event, gjson.Parse(data)
}

func TestConvertClaudeResponseToOpenAIResponses_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_123"
	chunks := [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"internal "}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"reasoning"}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(`data: {"type":"message_stop"}`),
	}

	var param any
	var outputs [][]byte
	for _, chunk := range chunks {
		outputs = append(outputs, ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}

	var reasoningDone gjson.Result
	var completed gjson.Result
	for _, output := range outputs {
		event, data := parseClaudeResponsesSSEEvent(t, output)
		switch event {
		case "response.output_item.done":
			if data.Get("item.type").String() == "reasoning" {
				reasoningDone = data
			}
		case "response.completed":
			completed = data
		}
	}

	if !reasoningDone.Exists() {
		t.Fatal("expected reasoning output_item.done event")
	}
	if got := reasoningDone.Get("item.encrypted_content").String(); got != signature {
		t.Fatalf("reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := reasoningDone.Get("item.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("reasoning summary text = %q", got)
	}
	if got := completed.Get("response.output.0.encrypted_content").String(); got != signature {
		t.Fatalf("completed reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := completed.Get("response.output.0.summary.0.text").String(); got != "internal reasoning" {
		t.Fatalf("completed reasoning summary text = %q", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_ThinkingIncludesSignature(t *testing.T) {
	signature := "claude_sig_nonstream"
	raw := []byte(strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_nonstream","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"nonstream reasoning"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"` + signature + `"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", nil, nil, raw, nil)
	root := gjson.ParseBytes(out)

	if got := root.Get("output.0.encrypted_content").String(); got != signature {
		t.Fatalf("non-stream reasoning encrypted_content = %q, want %q", got, signature)
	}
	if got := root.Get("output.0.summary.0.text").String(); got != "nonstream reasoning" {
		t.Fatalf("non-stream reasoning summary text = %q", got)
	}
}
