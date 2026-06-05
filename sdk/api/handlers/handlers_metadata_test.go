package handlers

import (
	"testing"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataIncludesExecutionSessionWithoutIdempotencyKey(t *testing.T) {
	ctx := WithExecutionSessionID(context.Background(), "session-1")

	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-1" {
		t.Fatalf("ExecutionSessionMetadataKey = %v, want %q", got, "session-1")
	}
	if _, ok := meta[idempotencyKeyMetadataKey]; ok {
		t.Fatalf("unexpected idempotency key in metadata: %v", meta[idempotencyKeyMetadataKey])
	}
}

func TestSetReasoningEffortMetadataUsesSuffixOverBody(t *testing.T) {
	meta := make(map[string]any)

	setReasoningEffortMetadata(meta, "openai", "gpt-5.4(high)", []byte(`{"reasoning_effort":"low"}`))

	if got := meta[coreexecutor.ReasoningEffortMetadataKey]; got != "high" {
		t.Fatalf("ReasoningEffortMetadataKey = %v, want %q", got, "high")
	}
}

func TestSetReasoningEffortMetadataSupportsOpenAIResponses(t *testing.T) {
	meta := make(map[string]any)

	setReasoningEffortMetadata(meta, "openai-response", "gpt-5.4", []byte(`{"reasoning":{"effort":"medium"}}`))

	if got := meta[coreexecutor.ReasoningEffortMetadataKey]; got != "medium" {
		t.Fatalf("ReasoningEffortMetadataKey = %v, want %q", got, "medium")
	}
}

func TestSetRequestShapeMetadataCountsChatMessagesAndToolCalls(t *testing.T) {
	meta := make(map[string]any)

	setRequestShapeMetadata(meta, []byte(`{
		"messages": [
			{"role":"user","content":"secret prompt"},
			{"role":"assistant","tool_calls":[{"id":"call_1"},{"id":"call_2"}]},
			{"role":"tool","tool_call_id":"call_1","content":"result"}
		],
		"tools": [{"type":"function"},{"type":"function"},{"type":"function"}]
	}`))

	if got := meta[coreexecutor.MessageCountMetadataKey]; got != 3 {
		t.Fatalf("MessageCountMetadataKey = %v, want 3", got)
	}
	if got := meta[coreexecutor.ToolCountMetadataKey]; got != 3 {
		t.Fatalf("ToolCountMetadataKey = %v, want 3", got)
	}
}

func TestSetRequestShapeMetadataCountsResponsesInputAndDeclaredTools(t *testing.T) {
	meta := make(map[string]any)

	setRequestShapeMetadata(meta, []byte(`{
		"input": [
			{"type":"message","role":"user","content":"hello"},
			{"type":"message","role":"assistant","content":"world"}
		],
		"tools": [{"type":"function"},{"type":"web_search"}]
	}`))

	if got := meta[coreexecutor.MessageCountMetadataKey]; got != 2 {
		t.Fatalf("MessageCountMetadataKey = %v, want 2", got)
	}
	if got := meta[coreexecutor.ToolCountMetadataKey]; got != 2 {
		t.Fatalf("ToolCountMetadataKey = %v, want 2", got)
	}
}
