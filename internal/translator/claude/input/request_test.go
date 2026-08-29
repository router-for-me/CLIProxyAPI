package input

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestParsePreservesClaudeRequestAndResolvedModel(t *testing.T) {
	raw := []byte(`{"model":"client/claude","system":[{"type":"text","text":"system"}],"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"sig"},{"type":"tool_use","id":"call-1","name":"run","input":{"command":"true"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":[{"type":"text","text":"ok"}]}]}],"tools":[{"name":"run","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"run"},"thinking":{"type":"enabled","budget_tokens":1024},"output_config":{"effort":"high"}}`)

	request := Parse("resolved-model", raw)

	if !bytes.Equal(request.Raw(), raw) {
		t.Fatalf("Raw() changed request bytes: %s", request.Raw())
	}
	if got := request.ResolvedModel(); got != "resolved-model" {
		t.Fatalf("ResolvedModel() = %q, want resolved-model", got)
	}
	if got := request.SourceModel(); got != "client/claude" {
		t.Fatalf("SourceModel() = %q, want client/claude", got)
	}
	if got := request.ModelOrSource(); got != "resolved-model" {
		t.Fatalf("ModelOrSource() = %q, want resolved-model", got)
	}
	if got := request.System().Get("0.text").String(); got != "system" {
		t.Fatalf("System() text = %q, want system", got)
	}
	if got := len(request.Messages()); got != 2 {
		t.Fatalf("len(Messages()) = %d, want 2", got)
	}
	if got := request.Tools().Get("0.name").String(); got != "run" {
		t.Fatalf("Tools() name = %q, want run", got)
	}
	if got := request.ToolChoice().Get("type").String(); got != "tool" {
		t.Fatalf("ToolChoice() type = %q, want tool", got)
	}
	if got := request.Thinking().Get("type").String(); got != "enabled" {
		t.Fatalf("Thinking() type = %q, want enabled", got)
	}
	if got := request.OutputConfig().Get("effort").String(); got != "high" {
		t.Fatalf("OutputConfig() effort = %q, want high", got)
	}
}

func TestParseUsesSourceModelOnlyWhenRequested(t *testing.T) {
	request := Parse("", []byte(`{"model":"source-model","messages":[]}`))

	if got := request.ResolvedModel(); got != "" {
		t.Fatalf("ResolvedModel() = %q, want empty", got)
	}
	if got := request.ModelOrSource(); got != "source-model" {
		t.Fatalf("ModelOrSource() = %q, want source-model", got)
	}
}

func TestMessageBlocksExposeClaudeGenericIdentity(t *testing.T) {
	request := Parse("model", []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"call-1","name":"run","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"ok"}]}]}`))
	messages := request.Messages()

	if got := MessageRole(messages[0]); got != "assistant" {
		t.Fatalf("MessageRole() = %q, want assistant", got)
	}
	assistantBlocks := MessageBlocks(messages[0])
	if got := len(assistantBlocks); got != 2 {
		t.Fatalf("assistant block count = %d, want 2", got)
	}
	if got := assistantBlocks[1].Type(); got != "tool_use" {
		t.Fatalf("tool block type = %q, want tool_use", got)
	}
	if got := assistantBlocks[1].ToolUseID(); got != "call-1" {
		t.Fatalf("ToolUseID() = %q, want call-1", got)
	}
	resultBlocks := MessageBlocks(messages[1])
	if got := resultBlocks[0].ToolResultID(); got != "call-1" {
		t.Fatalf("ToolResultID() = %q, want call-1", got)
	}
}

func TestValidateChecksOnlyClaudeGenericShape(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "valid", raw: `{"messages":[{"role":"user","content":"hi"}]}`},
		{name: "invalid JSON", raw: `{`, wantErr: true},
		{name: "trailing JSON", raw: `{"messages":[]} {"messages":[]}`, wantErr: true},
		{name: "non-object", raw: `[]`, wantErr: true},
		{name: "messages not array", raw: `{"messages":{}}`, wantErr: true},
		{name: "message not object", raw: `{"messages":["hi"]}`, wantErr: true},
		{name: "content wrong shape", raw: `{"messages":[{"role":"user","content":1}]}`, wantErr: true},
		{name: "block not object", raw: `{"messages":[{"role":"user","content":["hi"]}]}`, wantErr: true},
		{name: "block missing type", raw: `{"messages":[{"role":"user","content":[{"text":"hi"}]}]}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Parse("model", []byte(tt.raw)).Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestClaudeBlockConstructorsPreserveGenericFields(t *testing.T) {
	toolUse := ToolUseBlock("call-1", "run", []byte(`{"command":"true"}`))
	if got := string(toolUse); got != `{"type":"tool_use","id":"call-1","name":"run","input":{"command":"true"}}` {
		t.Fatalf("ToolUseBlock() = %s", got)
	}
	if got := gjson.GetBytes(toolUse, "id").String(); got != "call-1" {
		t.Fatalf("tool_use id = %q, want call-1", got)
	}

	toolResult := ToolResultBlock("call-1", []byte(`[{"type":"text","text":"ok"}]`))
	if got := string(toolResult); got != `{"type":"tool_result","tool_use_id":"call-1","content":[{"type":"text","text":"ok"}]}` {
		t.Fatalf("ToolResultBlock() = %s", got)
	}

	thinking := ThinkingBlock("reason", "signature")
	if got := string(thinking); got != `{"type":"thinking","thinking":"reason","signature":"signature"}` {
		t.Fatalf("ThinkingBlock() = %s", got)
	}

	unsignedThinking := ThinkingBlock("reason", "")
	if gjson.GetBytes(unsignedThinking, "signature").Exists() {
		t.Fatalf("unsigned ThinkingBlock() included signature: %s", unsignedThinking)
	}

	redacted := RedactedThinkingBlock("opaque")
	if got := string(redacted); got != `{"type":"redacted_thinking","data":"opaque"}` {
		t.Fatalf("RedactedThinkingBlock() = %s", got)
	}
}
