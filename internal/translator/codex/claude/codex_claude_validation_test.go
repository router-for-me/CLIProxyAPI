package claude

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestValidateClaudeRequestForCodexRejectsLossyControls(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "stop sequences",
			payload: `{"messages":[{"role":"user","content":"hello"}],"stop_sequences":["END"]}`,
		},
		{
			name:    "top k",
			payload: `{"messages":[{"role":"user","content":"hello"}],"top_k":10}`,
		},
		{
			name:    "unsupported structured output",
			payload: `{"messages":[{"role":"user","content":"hello"}],"output_config":{"format":{"type":"text"}}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateClaudeRequestForCodex([]byte(test.payload)); err == nil {
				t.Fatal("ValidateClaudeRequestForCodex() error = nil, want rejection")
			}
		})
	}
}

func TestValidateClaudeRequestForCodexRejectsDroppedContent(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{
			name:    "unknown message block",
			payload: `{"messages":[{"role":"user","content":[{"type":"audio","data":"ignored"}]}]}`,
		},
		{
			name:    "top level URL image",
			payload: `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}}]}]}`,
		},
		{
			name:    "tool result URL image",
			payload: `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"image","source":{"type":"url","url":"https://example.test/image.png"}}]}]}]}`,
		},
		{
			name:    "tool result document",
			payload: `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"document","source":{"type":"url","url":"https://example.test/document.pdf"}}]}]}]}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateClaudeRequestForCodex([]byte(test.payload)); err == nil {
				t.Fatal("ValidateClaudeRequestForCodex() error = nil, want rejection")
			}
		})
	}
}

func TestValidateClaudeRequestForCodexContextManagementNoOpBoundary(t *testing.T) {
	tests := []struct {
		name      string
		payload   []byte
		wantError bool
	}{
		{
			name:    "empty edits",
			payload: []byte(`{"messages":[{"role":"user","content":"hello"}],"context_management":{"edits":[]}}`),
		},
		{
			name:    "clear thinking keeps supplied turn",
			payload: []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"work","signature":""}]}],"context_management":{"edits":[{"type":"clear_thinking_20251015","keep":{"type":"thinking_turns","value":1}}]}}`),
		},
		{
			name:      "clear thinking would remove supplied turn",
			payload:   []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"work","signature":""}]}],"context_management":{"edits":[{"type":"clear_thinking_20251015"}]}}`),
			wantError: true,
		},
		{
			name:      "compaction has no mapping",
			payload:   []byte(`{"messages":[{"role":"user","content":"hello"}],"context_management":{"edits":[{"type":"compact_20260112"}]}}`),
			wantError: true,
		},
		{
			name:    "default clear tool policy keeps three results",
			payload: claudeToolResultHistory(t, 3),
		},
		{
			name:      "default clear tool policy would remove a result",
			payload:   claudeToolResultHistory(t, 4),
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateClaudeRequestForCodex(test.payload)
			if test.wantError && err == nil {
				t.Fatal("ValidateClaudeRequestForCodex() error = nil, want rejection")
			}
			if !test.wantError && err != nil {
				t.Fatalf("ValidateClaudeRequestForCodex() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateClaudeRequestForCodexPreservesCompatibilityInputs(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"system","content":[{"type":"text","text":"reminder"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"work","signature":""}]}],"temperature":0.7,"top_p":0.9,"metadata":{"user_id":"test"},"output_config":{"effort":"high"}}`)
	if err := ValidateClaudeRequestForCodex(payload); err != nil {
		t.Fatalf("ValidateClaudeRequestForCodex() error = %v, want nil", err)
	}
}

func TestValidateClaudeRequestForCodexRejectsMalformedEnvelope(t *testing.T) {
	for _, payload := range [][]byte{
		[]byte(`{"messages":`),
		[]byte(`[]`),
		[]byte(`{"messages":[]}`),
	} {
		if err := ValidateClaudeRequestForCodex(payload); err == nil {
			t.Fatalf("ValidateClaudeRequestForCodex(%s) error = nil, want rejection", payload)
		}
	}
}

func claudeToolResultHistory(t *testing.T, count int) []byte {
	t.Helper()
	messages := make([]map[string]any, 0, count*2)
	for index := 0; index < count; index++ {
		id := fmt.Sprintf("call_%d", index)
		messages = append(messages,
			map[string]any{
				"role": "assistant",
				"content": []map[string]any{{
					"type":  "tool_use",
					"id":    id,
					"name":  "lookup",
					"input": map[string]any{},
				}},
			},
			map[string]any{
				"role": "user",
				"content": []map[string]any{{
					"type":        "tool_result",
					"tool_use_id": id,
					"content":     "ok",
				}},
			},
		)
	}
	payload, err := json.Marshal(map[string]any{
		"messages": messages,
		"context_management": map[string]any{
			"edits": []map[string]any{{"type": "clear_tool_uses_20250919"}},
		},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return payload
}
