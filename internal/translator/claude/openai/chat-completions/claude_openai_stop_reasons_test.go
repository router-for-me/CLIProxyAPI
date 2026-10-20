package chat_completions

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func chatStopReasonChunks(reason string) [][]byte {
	return [][]byte{
		[]byte(`data: {"type":"message_start","message":{"id":"msg_c","model":"claude-test","usage":{"input_tokens":5,"output_tokens":0}}}`),
		[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`),
		[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}`),
		[]byte(`data: {"type":"content_block_stop","index":0}`),
		[]byte(fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":3}}`, reason)),
		[]byte(`data: {"type":"message_stop"}`),
	}
}

func TestChatContextWindowExceededStreamEndsWithErrorWithoutSuccessChunk(t *testing.T) {
	var param any
	var outputs [][]byte
	for _, chunk := range chatStopReasonChunks("model_context_window_exceeded") {
		outputs = append(outputs, ConvertClaudeResponseToOpenAI(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}
	if len(outputs) == 0 {
		t.Fatal("no output")
	}
	last := gjson.ParseBytes(outputs[len(outputs)-1])
	if last.Get("error.code").String() != "context_length_exceeded" || last.Get("error.type").String() != "invalid_request_error" {
		t.Fatalf("last chunk = %s", last.Raw)
	}
	if last.Get("usage").Exists() {
		t.Fatalf("error chunk carries usage: %s", last.Raw)
	}
	for _, output := range outputs {
		chunk := gjson.ParseBytes(output)
		if reason := chunk.Get("choices.0.finish_reason").String(); reason != "" {
			t.Fatalf("success finish chunk emitted: %s", chunk.Raw)
		}
		if chunk.Get("usage").Exists() {
			t.Fatalf("usage chunk emitted: %s", chunk.Raw)
		}
	}
}

func TestChatContextWindowExceededNonStreamReturnsErrorBody(t *testing.T) {
	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chatStopReasonChunks("model_context_window_exceeded"), []byte("\n")), nil)
	if gjson.GetBytes(out, "error.code").String() != "context_length_exceeded" || gjson.GetBytes(out, "choices").Exists() || gjson.GetBytes(out, "usage").Exists() {
		t.Fatalf("body = %s", out)
	}
}

func TestChatRefusalKeepsContentFilterAndUsage(t *testing.T) {
	var param any
	var finish string
	for _, chunk := range chatStopReasonChunks("refusal") {
		for _, output := range ConvertClaudeResponseToOpenAI(context.Background(), "claude-test", nil, nil, chunk, &param) {
			if reason := gjson.GetBytes(output, "choices.0.finish_reason").String(); reason != "" {
				finish = reason
			}
		}
	}
	if finish != "content_filter" {
		t.Fatalf("stream finish_reason = %q", finish)
	}
	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chatStopReasonChunks("refusal"), []byte("\n")), nil)
	if gjson.GetBytes(out, "choices.0.finish_reason").String() != "content_filter" || gjson.GetBytes(out, "usage.completion_tokens").Int() != 3 {
		t.Fatalf("non-stream = %s", out)
	}
}

func TestChatMaxTokensUnchanged(t *testing.T) {
	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chatStopReasonChunks("max_tokens"), []byte("\n")), nil)
	if gjson.GetBytes(out, "choices.0.finish_reason").String() != "length" {
		t.Fatalf("non-stream = %s", out)
	}
}
