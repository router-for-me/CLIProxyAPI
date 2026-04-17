package claude

import (
	"bytes"
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func firstSSEDataLine(chunk []byte) []byte {
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			return bytes.TrimSpace(line[len("data:"):])
		}
	}
	return nil
}

func TestConvertCodexResponseToClaude_AcceptsFullSSEFrameForResponseCreated(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToClaude(
		ctx,
		"gpt-5.3-codex",
		nil,
		nil,
		[]byte("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_123\",\"model\":\"gpt-5.3-codex\"}}\n\n"),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	if !bytes.Contains(out[0], []byte("event: message_start\n")) {
		t.Fatalf("expected message_start event, got %q", string(out[0]))
	}

	payload := firstSSEDataLine(out[0])
	if got := gjson.GetBytes(payload, "type").String(); got != "message_start" {
		t.Fatalf("type = %q, want %q", got, "message_start")
	}
	if got := gjson.GetBytes(payload, "message.id").String(); got != "resp_123" {
		t.Fatalf("message.id = %q, want %q", got, "resp_123")
	}
	if got := gjson.GetBytes(payload, "message.model").String(); got != "gpt-5.3-codex" {
		t.Fatalf("message.model = %q, want %q", got, "gpt-5.3-codex")
	}
}

func TestConvertCodexResponseToClaude_KeepsDataPrefixedInputWorking(t *testing.T) {
	ctx := context.Background()
	var param any

	out := ConvertCodexResponseToClaude(
		ctx,
		"gpt-5.3-codex",
		nil,
		nil,
		[]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_456\",\"model\":\"gpt-5.3-codex\"}}"),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}

	payload := firstSSEDataLine(out[0])
	if got := gjson.GetBytes(payload, "message.id").String(); got != "resp_456" {
		t.Fatalf("message.id = %q, want %q", got, "resp_456")
	}
}

func TestConvertCodexResponseToClaude_AcceptsFullSSEFrameForTextDelta(t *testing.T) {
	ctx := context.Background()
	var param any

	_ = ConvertCodexResponseToClaude(
		ctx,
		"gpt-5.3-codex",
		nil,
		nil,
		[]byte("data: {\"type\":\"response.content_part.added\"}"),
		&param,
	)
	out := ConvertCodexResponseToClaude(
		ctx,
		"gpt-5.3-codex",
		nil,
		nil,
		[]byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"OK\"}\n\n"),
		&param,
	)
	if len(out) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(out))
	}
	if !bytes.Contains(out[0], []byte("event: content_block_delta\n")) {
		t.Fatalf("expected content_block_delta event, got %q", string(out[0]))
	}

	payload := firstSSEDataLine(out[0])
	if got := gjson.GetBytes(payload, "delta.type").String(); got != "text_delta" {
		t.Fatalf("delta.type = %q, want %q", got, "text_delta")
	}
	if got := gjson.GetBytes(payload, "delta.text").String(); got != "OK" {
		t.Fatalf("delta.text = %q, want %q", got, "OK")
	}
}

func TestConvertCodexResponseToClaude_IgnoresDoneAndInvalidFrames(t *testing.T) {
	ctx := context.Background()
	var param any

	tests := [][]byte{
		[]byte("data: [DONE]"),
		[]byte("event: response.created\n\n"),
		[]byte(""),
	}
	for _, raw := range tests {
		if out := ConvertCodexResponseToClaude(ctx, "gpt-5.3-codex", nil, nil, raw, &param); len(out) != 0 {
			t.Fatalf("expected no output for %q, got %d chunks", string(raw), len(out))
		}
	}
}
