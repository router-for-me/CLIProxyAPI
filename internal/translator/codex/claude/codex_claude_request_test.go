package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodex_SystemMessageScenarios(t *testing.T) {
	tests := []struct {
		name             string
		inputJSON        string
		wantHasDeveloper bool
		wantTexts        []string
	}{
		{
			name: "No system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasDeveloper: false,
		},
		{
			name: "Empty string system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": "",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasDeveloper: false,
		},
		{
			name: "String system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": "Be helpful",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasDeveloper: true,
			wantTexts:        []string{"Be helpful"},
		},
		{
			name: "Array system field with filtered billing header",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": [
					{"type": "text", "text": "x-anthropic-billing-header: tenant-123"},
					{"type": "text", "text": "Block 1"},
					{"type": "text", "text": "Block 2"}
				],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasDeveloper: true,
			wantTexts:        []string{"Block 1", "Block 2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToCodex("test-model", []byte(tt.inputJSON), false)
			resultJSON := gjson.ParseBytes(result)
			inputs := resultJSON.Get("input").Array()

			hasDeveloper := len(inputs) > 0 && inputs[0].Get("role").String() == "developer"
			if hasDeveloper != tt.wantHasDeveloper {
				t.Fatalf("got hasDeveloper = %v, want %v. Output: %s", hasDeveloper, tt.wantHasDeveloper, resultJSON.Get("input").Raw)
			}

			if !tt.wantHasDeveloper {
				return
			}

			content := inputs[0].Get("content").Array()
			if len(content) != len(tt.wantTexts) {
				t.Fatalf("got %d system content items, want %d. Content: %s", len(content), len(tt.wantTexts), inputs[0].Get("content").Raw)
			}

			for i, wantText := range tt.wantTexts {
				if gotType := content[i].Get("type").String(); gotType != "input_text" {
					t.Fatalf("content[%d] type = %q, want %q", i, gotType, "input_text")
				}
				if gotText := content[i].Get("text").String(); gotText != wantText {
					t.Fatalf("content[%d] text = %q, want %q", i, gotText, wantText)
				}
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_ParallelToolCalls(t *testing.T) {
	tests := []struct {
		name                  string
		inputJSON             string
		wantParallelToolCalls bool
	}{
		{
			name: "Default to true when tool_choice.disable_parallel_tool_use is absent",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantParallelToolCalls: true,
		},
		{
			name: "Disable parallel tool calls when client opts out",
			inputJSON: `{
				"model": "claude-3-opus",
				"tool_choice": {"disable_parallel_tool_use": true},
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantParallelToolCalls: false,
		},
		{
			name: "Keep parallel tool calls enabled when client explicitly allows them",
			inputJSON: `{
				"model": "claude-3-opus",
				"tool_choice": {"disable_parallel_tool_use": false},
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantParallelToolCalls: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToCodex("test-model", []byte(tt.inputJSON), false)
			resultJSON := gjson.ParseBytes(result)

			if got := resultJSON.Get("parallel_tool_calls").Bool(); got != tt.wantParallelToolCalls {
				t.Fatalf("parallel_tool_calls = %v, want %v. Output: %s", got, tt.wantParallelToolCalls, string(result))
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_ThinkingSignatureToEncryptedContent(t *testing.T) {
	result := ConvertClaudeRequestToCodex("test-model", []byte(`{
		"model": "claude-3-opus",
		"messages": [{
			"role": "assistant",
			"content": [
				{"type": "thinking", "thinking": "Internal reasoning.", "signature": "sig_123"},
				{"type": "text", "text": "Visible answer."}
			]
		}]
	}`), false)
	resultJSON := gjson.ParseBytes(result)
	inputs := resultJSON.Get("input").Array()

	if len(inputs) != 2 {
		t.Fatalf("got %d input items, want 2. Output: %s", len(inputs), string(result))
	}

	reasoning := inputs[0]
	if got := reasoning.Get("type").String(); got != "reasoning" {
		t.Fatalf("input[0].type = %q, want %q. Output: %s", got, "reasoning", string(result))
	}
	if got := reasoning.Get("encrypted_content").String(); got != "sig_123" {
		t.Fatalf("encrypted_content = %q, want %q. Output: %s", got, "sig_123", string(result))
	}
	if got := reasoning.Get("summary.0.type").String(); got != "summary_text" {
		t.Fatalf("summary.0.type = %q, want %q. Output: %s", got, "summary_text", string(result))
	}
	if got := reasoning.Get("summary.0.text").String(); got != "Internal reasoning." {
		t.Fatalf("summary.0.text = %q, want %q. Output: %s", got, "Internal reasoning.", string(result))
	}

	message := inputs[1]
	if got := message.Get("type").String(); got != "message" {
		t.Fatalf("input[1].type = %q, want %q. Output: %s", got, "message", string(result))
	}
	if got := message.Get("role").String(); got != "assistant" {
		t.Fatalf("input[1].role = %q, want %q. Output: %s", got, "assistant", string(result))
	}
	if got := message.Get("content.0.type").String(); got != "output_text" {
		t.Fatalf("content.0.type = %q, want %q. Output: %s", got, "output_text", string(result))
	}
	if got := message.Get("content.0.text").String(); got != "Visible answer." {
		t.Fatalf("content.0.text = %q, want %q. Output: %s", got, "Visible answer.", string(result))
	}
}

func TestConvertClaudeRequestToCodex_ThinkingSignatureWithoutText(t *testing.T) {
	result := ConvertClaudeRequestToCodex("test-model", []byte(`{
		"model": "claude-3-opus",
		"messages": [{
			"role": "assistant",
			"content": [{"type": "thinking", "thinking": "", "signature": "sig_empty_text"}]
		}]
	}`), false)
	resultJSON := gjson.ParseBytes(result)
	inputs := resultJSON.Get("input").Array()

	if len(inputs) != 1 {
		t.Fatalf("got %d input items, want 1. Output: %s", len(inputs), string(result))
	}
	if got := inputs[0].Get("type").String(); got != "reasoning" {
		t.Fatalf("input[0].type = %q, want %q. Output: %s", got, "reasoning", string(result))
	}
	if got := inputs[0].Get("encrypted_content").String(); got != "sig_empty_text" {
		t.Fatalf("encrypted_content = %q, want %q. Output: %s", got, "sig_empty_text", string(result))
	}
	if got := len(inputs[0].Get("summary").Array()); got != 0 {
		t.Fatalf("summary length = %d, want 0. Output: %s", got, string(result))
	}
}
