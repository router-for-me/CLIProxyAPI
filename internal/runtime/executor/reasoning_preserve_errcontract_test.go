package executor

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

// TestConvertReasoningToThinkingContent_EmptyPayloadReturnsNilError ensures the
// early-return path for empty payload does not produce an error.
func TestConvertReasoningToThinkingContent_EmptyPayloadReturnsNilError(t *testing.T) {
	out, err := convertReasoningToThinkingContent(nil)
	if err != nil {
		t.Fatalf("nil payload: unexpected error %v", err)
	}
	if out != nil {
		t.Fatalf("nil payload: want nil output, got %v", out)
	}

	out, err = convertReasoningToThinkingContent([]byte{})
	if err != nil {
		t.Fatalf("empty slice payload: unexpected error %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("empty slice payload: want empty output, got %q", string(out))
	}
}

// TestConvertReasoningToThinkingContent_InvalidJSONReturnsPayloadUnchanged verifies
// that invalid JSON input is returned as-is without an error.
func TestConvertReasoningToThinkingContent_InvalidJSONReturnsPayloadUnchanged(t *testing.T) {
	invalid := []byte(`not json at all {{{`)
	out, err := convertReasoningToThinkingContent(invalid)
	if err != nil {
		t.Fatalf("invalid JSON: unexpected error %v", err)
	}
	if !bytes.Equal(out, invalid) {
		t.Fatalf("invalid JSON: output %q differs from input %q", string(out), string(invalid))
	}
}

// TestConvertReasoningToThinkingContent_NoMessagesFieldReturnsPayloadUnchanged
// verifies that a payload without a "messages" field is returned as-is.
func TestConvertReasoningToThinkingContent_NoMessagesFieldReturnsPayloadUnchanged(t *testing.T) {
	payload := []byte(`{"reasoning_effort":"high","model":"gpt-4"}`)
	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	if !bytes.Equal(out, payload) {
		t.Fatalf("output %q differs from input %q", string(out), string(payload))
	}
}

// TestConvertReasoningToThinkingContent_EmptyMessagesArrayNoOp verifies that an
// empty messages array is returned as-is without modification.
func TestConvertReasoningToThinkingContent_EmptyMessagesArrayNoOp(t *testing.T) {
	payload := []byte(`{"reasoning_effort":"high","messages":[]}`)
	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("unexpected error %v", err)
	}
	msgs := gjson.GetBytes(out, "messages")
	if !msgs.IsArray() || len(msgs.Array()) != 0 {
		t.Fatalf("messages should remain empty array, got %s", msgs.Raw)
	}
}

// TestConvertReasoningToThinkingContent_OriginalPayloadReturnedOnSuccess verifies
// the error-contract: when the conversion succeeds, the returned payload is NOT
// the original pointer (it has been rewritten), and no error is returned.
func TestConvertReasoningToThinkingContent_OriginalPayloadReturnedOnSuccess(t *testing.T) {
	payload := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"assistant","content":"result","reasoning_content":"my thought"}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() error = %v", err)
	}
	// The output should differ from the input (new content array was built).
	if bytes.Equal(out, payload) {
		t.Fatal("expected output to differ from input after conversion")
	}
	// Spot-check: thinking block should be present.
	if gjson.GetBytes(out, "messages.0.content.0.type").String() != "thinking" {
		t.Fatalf("expected thinking block in content.0, got %s",
			gjson.GetBytes(out, "messages.0.content.0").Raw)
	}
}

// TestConvertReasoningToThinkingContent_StringContentWrappedAsTextBlock verifies
// that an assistant message with string "content" and a reasoning_content is
// converted so that the string becomes a {"type":"text","text":"..."} block
// following the thinking block.
func TestConvertReasoningToThinkingContent_StringContentWrappedAsTextBlock(t *testing.T) {
	payload := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"assistant","content":"my response","reasoning_content":"deep thought"}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() error = %v", err)
	}

	contentArr := gjson.GetBytes(out, "messages.0.content").Array()
	if len(contentArr) != 2 {
		t.Fatalf("content should have 2 parts (thinking + text), got %d", len(contentArr))
	}

	if contentArr[0].Get("type").String() != "thinking" {
		t.Errorf("content[0].type = %q, want %q", contentArr[0].Get("type").String(), "thinking")
	}
	if contentArr[0].Get("thinking").String() != "deep thought" {
		t.Errorf("content[0].thinking = %q, want %q", contentArr[0].Get("thinking").String(), "deep thought")
	}

	if contentArr[1].Get("type").String() != "text" {
		t.Errorf("content[1].type = %q, want %q", contentArr[1].Get("type").String(), "text")
	}
	if contentArr[1].Get("text").String() != "my response" {
		t.Errorf("content[1].text = %q, want %q", contentArr[1].Get("text").String(), "my response")
	}
}

// TestConvertReasoningToThinkingContent_EmptyStringContentOnlyThinkingBlock verifies
// that when string content is empty, only the thinking block is emitted (no text block).
func TestConvertReasoningToThinkingContent_EmptyStringContentOnlyThinkingBlock(t *testing.T) {
	payload := []byte(`{
		"reasoning_effort":"high",
		"messages":[
			{"role":"assistant","content":"","reasoning_content":"thought only"}
		]
	}`)

	out, err := convertReasoningToThinkingContent(payload)
	if err != nil {
		t.Fatalf("convertReasoningToThinkingContent() error = %v", err)
	}

	contentArr := gjson.GetBytes(out, "messages.0.content").Array()
	if len(contentArr) != 1 {
		t.Fatalf("empty string content should yield only the thinking block, got %d parts", len(contentArr))
	}
	if contentArr[0].Get("type").String() != "thinking" {
		t.Errorf("content[0].type = %q, want %q", contentArr[0].Get("type").String(), "thinking")
	}
}