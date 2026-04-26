package executor

import (
	"encoding/json"
	"testing"
)

func TestCursorCompletionJSONEscapesModelAndContent(t *testing.T) {
	t.Parallel()

	payload, err := cursorCompletionJSON("chatcmpl-test", 1700000000, `x","pwned":true,"y":"`, `hi "there"`)
	if err != nil {
		t.Fatalf("cursorCompletionJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v; payload=%s", err, payload)
	}
	if got["model"] != `x","pwned":true,"y":"` {
		t.Fatalf("model = %q", got["model"])
	}
	if _, ok := got["pwned"]; ok {
		t.Fatalf("payload allowed model to inject top-level field: %s", payload)
	}
}

func TestCursorChunkJSONEscapesModelAndFinishReason(t *testing.T) {
	t.Parallel()

	payload, err := cursorChunkJSON(
		"chatcmpl-test",
		1700000000,
		`x","pwned":true,"y":"`,
		json.RawMessage(`{"content":"ok"}`),
		`stop","pwned":true,"x":"`,
	)
	if err != nil {
		t.Fatalf("cursorChunkJSON: %v", err)
	}

	var got struct {
		Model   string `json:"model"`
		Pwned   bool   `json:"pwned"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal payload: %v; payload=%s", err, payload)
	}
	if got.Model != `x","pwned":true,"y":"` {
		t.Fatalf("model = %q", got.Model)
	}
	if got.Pwned {
		t.Fatalf("payload allowed model to inject top-level field: %s", payload)
	}
	if got.Choices[0].FinishReason != `stop","pwned":true,"x":"` {
		t.Fatalf("finish_reason = %q", got.Choices[0].FinishReason)
	}
}

func TestCursorToolCallDeltaJSONEscapesToolIdentifiers(t *testing.T) {
	t.Parallel()

	payload := cursorToolCallDeltaJSON(
		0,
		`call_1","pwned":true,"x":"`,
		`tool","pwned":true,"x":"`,
		`{"ok":true}`,
	)
	var got map[string]any
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("unmarshal payload: %v; payload=%s", err, payload)
	}
	if _, ok := got["pwned"]; ok {
		t.Fatalf("payload allowed tool metadata to inject top-level field: %s", payload)
	}
}
