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
