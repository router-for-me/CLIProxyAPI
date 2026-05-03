package responses

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func responseEventPayload(t *testing.T, chunk []byte) []byte {
	t.Helper()
	prefix := []byte("data: ")
	idx := bytes.Index(chunk, prefix)
	if idx < 0 {
		t.Fatalf("missing data payload in chunk: %s", chunk)
	}
	return bytes.TrimSpace(chunk[idx+len(prefix):])
}

func TestConvertClaudeResponseToOpenAIResponses_TextDoneUsesCurrentBlockText(t *testing.T) {
	var param any
	ctx := context.Background()
	emptyRequest := []byte(`{}`)

	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"message_start","message":{"id":"msg_456","usage":{"input_tokens":1,"output_tokens":0}}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"first"}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_stop","index":0}`), &param)

	startSecond := ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"second"}}`), &param)
	out := ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_stop","index":2}`), &param)

	if len(startSecond) != 2 {
		t.Fatalf("second content_block_start emitted %d events, want 2", len(startSecond))
	}
	secondItemAdded := responseEventPayload(t, startSecond[0])
	if got := gjson.GetBytes(secondItemAdded, "output_index").Int(); got != 2 {
		t.Fatalf("second response.output_item.added output_index = %d, want 2", got)
	}
	if got := gjson.GetBytes(secondItemAdded, "item.id").String(); got != "msg_msg_456_2" {
		t.Fatalf("second response.output_item.added item.id = %q, want msg_msg_456_2", got)
	}

	if len(out) != 3 {
		t.Fatalf("second content_block_stop emitted %d events, want 3", len(out))
	}
	outputTextDone := responseEventPayload(t, out[0])
	if got := gjson.GetBytes(outputTextDone, "text").String(); got != "second" {
		t.Fatalf("second response.output_text.done text = %q, want second", got)
	}
	if got := gjson.GetBytes(outputTextDone, "output_index").Int(); got != 2 {
		t.Fatalf("second response.output_text.done output_index = %d, want 2", got)
	}

	contentPartDone := responseEventPayload(t, out[1])
	if got := gjson.GetBytes(contentPartDone, "part.text").String(); got != "second" {
		t.Fatalf("second response.content_part.done part.text = %q, want second", got)
	}

	outputItemDone := responseEventPayload(t, out[2])
	if got := gjson.GetBytes(outputItemDone, "item.content.0.text").String(); got != "second" {
		t.Fatalf("second response.output_item.done item.content.0.text = %q, want second", got)
	}
	if got := gjson.GetBytes(outputItemDone, "output_index").Int(); got != 2 {
		t.Fatalf("second response.output_item.done output_index = %d, want 2", got)
	}

	completed := ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"message_stop"}`), &param)
	if len(completed) != 1 {
		t.Fatalf("message_stop emitted %d events, want 1", len(completed))
	}
	completedPayload := responseEventPayload(t, completed[0])
	outputs := gjson.GetBytes(completedPayload, "response.output").Array()
	if len(outputs) != 2 {
		t.Fatalf("response.completed output length = %d, want 2: %s", len(outputs), completedPayload)
	}
	if got := outputs[0].Get("id").String(); got != "msg_msg_456_0" {
		t.Fatalf("response.completed output.0.id = %q, want msg_msg_456_0", got)
	}
	if got := outputs[0].Get("content.0.text").String(); got != "first" {
		t.Fatalf("response.completed output.0 text = %q, want first", got)
	}
	if got := outputs[1].Get("id").String(); got != "msg_msg_456_2" {
		t.Fatalf("response.completed output.1.id = %q, want msg_msg_456_2", got)
	}
	if got := outputs[1].Get("content.0.text").String(); got != "second" {
		t.Fatalf("response.completed output.1 text = %q, want second", got)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_TextDoneCarriesAccumulatedText(t *testing.T) {
	var param any
	ctx := context.Background()
	emptyRequest := []byte(`{}`)

	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"message_start","message":{"id":"msg_123","usage":{"input_tokens":1,"output_tokens":0}}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"안녕"}}`), &param)
	_ = ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"?"}}`), &param)

	out := ConvertClaudeResponseToOpenAIResponses(ctx, "claude-sonnet", emptyRequest, emptyRequest, []byte(`data: {"type":"content_block_stop","index":0}`), &param)
	if len(out) != 3 {
		t.Fatalf("content_block_stop emitted %d events, want 3", len(out))
	}

	outputTextDone := responseEventPayload(t, out[0])
	if got := gjson.GetBytes(outputTextDone, "text").String(); got != "안녕?" {
		t.Fatalf("response.output_text.done text = %q, want %q", got, "안녕?")
	}

	contentPartDone := responseEventPayload(t, out[1])
	if got := gjson.GetBytes(contentPartDone, "part.text").String(); got != "안녕?" {
		t.Fatalf("response.content_part.done part.text = %q, want %q", got, "안녕?")
	}

	outputItemDone := responseEventPayload(t, out[2])
	if got := gjson.GetBytes(outputItemDone, "item.content.0.text").String(); got != "안녕?" {
		t.Fatalf("response.output_item.done item.content.0.text = %q, want %q", got, "안녕?")
	}
}
