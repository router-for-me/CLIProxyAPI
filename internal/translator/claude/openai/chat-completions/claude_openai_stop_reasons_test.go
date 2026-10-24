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

func TestChatContextWindowExceededStreamMapsToLengthAndKeepsTextAndUsage(t *testing.T) {
	var param any
	var outputs [][]byte
	for _, chunk := range chatStopReasonChunks("model_context_window_exceeded") {
		outputs = append(outputs, ConvertClaudeResponseToOpenAI(context.Background(), "claude-test", nil, nil, chunk, &param)...)
	}
	if len(outputs) == 0 {
		t.Fatal("no output")
	}
	var text string
	var finish string
	for _, output := range outputs {
		chunk := gjson.ParseBytes(output)
		if chunk.Get("error").Exists() {
			t.Fatalf("unexpected error chunk: %s", chunk.Raw)
		}
		text += chunk.Get("choices.0.delta.content").String()
		if reason := chunk.Get("choices.0.finish_reason").String(); reason != "" {
			finish = reason
		}
	}
	if text != "partial" {
		t.Fatalf("stream text = %q, want %q", text, "partial")
	}
	if finish != "length" {
		t.Fatalf("stream finish_reason = %q, want length", finish)
	}
	last := gjson.ParseBytes(outputs[len(outputs)-1])
	if last.Get("usage.completion_tokens").Int() != 3 {
		t.Fatalf("trailing usage chunk = %s", last.Raw)
	}
}

func TestChatContextWindowExceededNonStreamMapsToLengthAndKeepsTextAndUsage(t *testing.T) {
	out := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chatStopReasonChunks("model_context_window_exceeded"), []byte("\n")), nil)
	if gjson.GetBytes(out, "error").Exists() {
		t.Fatalf("unexpected error body = %s", out)
	}
	if got := gjson.GetBytes(out, "choices.0.finish_reason").String(); got != "length" {
		t.Fatalf("finish_reason = %q, want length: %s", got, out)
	}
	if got := gjson.GetBytes(out, "choices.0.message.content").String(); got != "partial" {
		t.Fatalf("content = %q, want %q: %s", got, "partial", out)
	}
	if got := gjson.GetBytes(out, "usage.completion_tokens").Int(); got != 3 {
		t.Fatalf("completion_tokens = %d, want 3: %s", got, out)
	}
}

func TestChatTruncatedToolCallKeepsLengthFinishReason(t *testing.T) {
	for _, reason := range []string{"max_tokens", "model_context_window_exceeded"} {
		t.Run(reason, func(t *testing.T) {
			chunks := [][]byte{
				[]byte(`data: {"type":"message_start","message":{"id":"msg_tc","model":"claude-test","usage":{"input_tokens":5,"output_tokens":0}}}`),
				[]byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}}`),
				[]byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"ls -l"}}`),
				[]byte(`data: {"type":"content_block_stop","index":0}`),
				[]byte(fmt.Sprintf(`data: {"type":"message_delta","delta":{"stop_reason":%q},"usage":{"output_tokens":4}}`, reason)),
				[]byte(`data: {"type":"message_stop"}`),
			}

			var param any
			var streamFinish string
			var streamArgs string
			var streamCompletionTokens int64
			for _, chunk := range chunks {
				for _, output := range ConvertClaudeResponseToOpenAI(context.Background(), "claude-test", nil, nil, chunk, &param) {
					parsed := gjson.ParseBytes(output)
					if args := parsed.Get("choices.0.delta.tool_calls.0.function.arguments"); args.Exists() {
						streamArgs = args.String()
					}
					if fr := parsed.Get("choices.0.finish_reason").String(); fr != "" {
						streamFinish = fr
					}
					if tok := parsed.Get("usage.completion_tokens"); tok.Exists() {
						streamCompletionTokens = tok.Int()
					}
				}
			}
			if streamFinish != "length" {
				t.Fatalf("stream finish_reason = %q, want length", streamFinish)
			}
			if streamArgs != `{"command":"ls -l` {
				t.Fatalf("stream tool call arguments = %q", streamArgs)
			}
			if streamCompletionTokens != 4 {
				t.Fatalf("stream completion_tokens = %d, want 4", streamCompletionTokens)
			}

			nonStream := ConvertClaudeResponseToOpenAINonStream(context.Background(), "claude-test", nil, nil, bytes.Join(chunks, []byte("\n")), nil)
			if got := gjson.GetBytes(nonStream, "choices.0.finish_reason").String(); got != "length" {
				t.Fatalf("non-stream finish_reason = %q, want length: %s", got, nonStream)
			}
			if got := gjson.GetBytes(nonStream, "choices.0.message.tool_calls.0.function.arguments").String(); got != `{"command":"ls -l` {
				t.Fatalf("non-stream tool call arguments = %q: %s", got, nonStream)
			}
			if got := gjson.GetBytes(nonStream, "usage.completion_tokens").Int(); got != 4 {
				t.Fatalf("non-stream completion_tokens = %d, want 4: %s", got, nonStream)
			}
		})
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
