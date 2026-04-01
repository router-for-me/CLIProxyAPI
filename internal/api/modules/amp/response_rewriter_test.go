package amp

import (
	"testing"
)

func TestRewriteModelInResponse_TopLevel(t *testing.T) {
	rw := &ResponseRewriter{originalModel: "gpt-5.2-codex"}

	input := []byte(`{"id":"resp_1","model":"gpt-5.3-codex","output":[]}`)
	result := rw.rewriteModelInResponse(input)

	expected := `{"id":"resp_1","model":"gpt-5.2-codex","output":[]}`
	if string(result) != expected {
		t.Errorf("expected %s, got %s", expected, string(result))
	}
}

func TestRewriteModelInResponse_ResponseModel(t *testing.T) {
	rw := &ResponseRewriter{originalModel: "gpt-5.2-codex"}

	input := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.3-codex","status":"completed"}}`)
	result := rw.rewriteModelInResponse(input)

	expected := `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2-codex","status":"completed"}}`
	if string(result) != expected {
		t.Errorf("expected %s, got %s", expected, string(result))
	}
}

func TestRewriteModelInResponse_ResponseCreated(t *testing.T) {
	rw := &ResponseRewriter{originalModel: "gpt-5.2-codex"}

	input := []byte(`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.3-codex","status":"in_progress"}}`)
	result := rw.rewriteModelInResponse(input)

	expected := `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.2-codex","status":"in_progress"}}`
	if string(result) != expected {
		t.Errorf("expected %s, got %s", expected, string(result))
	}
}

func TestRewriteModelInResponse_NoModelField(t *testing.T) {
	rw := &ResponseRewriter{originalModel: "gpt-5.2-codex"}

	input := []byte(`{"type":"response.output_item.added","item":{"id":"item_1","type":"message"}}`)
	result := rw.rewriteModelInResponse(input)

	if string(result) != string(input) {
		t.Errorf("expected no modification, got %s", string(result))
	}
}

func TestRewriteModelInResponse_EmptyOriginalModel(t *testing.T) {
	rw := &ResponseRewriter{originalModel: ""}

	input := []byte(`{"model":"gpt-5.3-codex"}`)
	result := rw.rewriteModelInResponse(input)

	if string(result) != string(input) {
		t.Errorf("expected no modification when originalModel is empty, got %s", string(result))
	}
}

func TestRewriteStreamChunk_SSEWithResponseModel(t *testing.T) {
	rw := &ResponseRewriter{originalModel: "gpt-5.2-codex"}

	chunk := []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.3-codex\",\"status\":\"completed\"}}\n\n")
	result := rw.rewriteStreamChunk(chunk)

	expected := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.2-codex\",\"status\":\"completed\"}}\n\n"
	if string(result) != expected {
		t.Errorf("expected %s, got %s", expected, string(result))
	}
}

func TestRewriteStreamChunk_MultipleEvents(t *testing.T) {
	rw := &ResponseRewriter{originalModel: "gpt-5.2-codex"}

	chunk := []byte("data: {\"type\":\"response.created\",\"response\":{\"model\":\"gpt-5.3-codex\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"item_1\"}}\n\n")
	result := rw.rewriteStreamChunk(chunk)

	if string(result) == string(chunk) {
		t.Error("expected response.model to be rewritten in SSE stream")
	}
	if !contains(result, []byte(`"model":"gpt-5.2-codex"`)) {
		t.Errorf("expected rewritten model in output, got %s", string(result))
	}
}

func TestRewriteStreamChunk_MessageModel(t *testing.T) {
	rw := &ResponseRewriter{originalModel: "claude-opus-4.5"}

	chunk := []byte("data: {\"message\":{\"model\":\"claude-sonnet-4\",\"role\":\"assistant\"}}\n\n")
	result := rw.rewriteStreamChunk(chunk)

	expected := "data: {\"message\":{\"model\":\"claude-opus-4.5\",\"role\":\"assistant\"}}\n\n"
	if string(result) != expected {
		t.Errorf("expected %s, got %s", expected, string(result))
	}
}

func TestRewriteStreamChunk_SuppressesThinkingContentBlockFrames(t *testing.T) {
	rw := &ResponseRewriter{suppressedContentBlock: make(map[int]struct{})}

	chunk := []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"abc\"}}\n\nevent: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\nevent: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"name\":\"bash\",\"input\":{}}}\n\n")
	result := rw.rewriteStreamChunk(chunk)

	if contains(result, []byte("\"thinking\"")) || contains(result, []byte("\"thinking_delta\"")) {
		t.Fatalf("expected thinking content_block frames to be suppressed, got %s", string(result))
	}
	if contains(result, []byte("content_block_stop")) {
		t.Fatalf("expected suppressed thinking content_block_stop to be removed, got %s", string(result))
	}
	if !contains(result, []byte("\"tool_use\"")) {
		t.Fatalf("expected tool_use content_block frame to remain, got %s", string(result))
	}
	if !contains(result, []byte("\"signature\":\"\"")) {
		t.Fatalf("expected tool_use content_block signature injection, got %s", string(result))
	}
}

func TestSanitizeAmpRequestBody_RemovesWhitespaceAndNonStringSignatures(t *testing.T) {
	input := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"drop-whitespace","signature":"   "},{"type":"thinking","thinking":"drop-number","signature":123},{"type":"thinking","thinking":"keep-valid","signature":"valid-signature"},{"type":"text","text":"keep-text"}]}]}`)
	result := SanitizeAmpRequestBody(input)

	if contains(result, []byte("drop-whitespace")) {
		t.Fatalf("expected whitespace-only signature block to be removed, got %s", string(result))
	}
	if contains(result, []byte("drop-number")) {
		t.Fatalf("expected non-string signature block to be removed, got %s", string(result))
	}
	if !contains(result, []byte("keep-valid")) {
		t.Fatalf("expected valid thinking block to remain, got %s", string(result))
	}
	if !contains(result, []byte("keep-text")) {
		t.Fatalf("expected non-thinking content to remain, got %s", string(result))
	}
}

func contains(data, substr []byte) bool {
	for i := 0; i <= len(data)-len(substr); i++ {
		if string(data[i:i+len(substr)]) == string(substr) {
			return true
		}
	}
	return false
}
