package claude

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
)

func TestBuildClaudeResponse(t *testing.T) {
	// Test basic response
	got := BuildClaudeResponse("Hello", nil, "model-1", usage.Detail{InputTokens: 10, OutputTokens: 20}, "end_turn")
	res := gjson.ParseBytes(got)
	
	if res.Get("content.0.text").String() != "Hello" {
		t.Errorf("expected content Hello, got %s", res.Get("content.0.text").String())
	}
	
	if res.Get("usage.input_tokens").Int() != 10 {
		t.Errorf("expected input tokens 10, got %d", res.Get("usage.input_tokens").Int())
	}
}

func TestBuildClaudeResponse_ToolUse(t *testing.T) {
	toolUses := []KiroToolUse{
		{
			ToolUseID: "call_1",
			Name:      "my_tool",
			Input:     map[string]interface{}{"arg": 1},
		},
	}
	
	got := BuildClaudeResponse("", toolUses, "model-1", usage.Detail{}, "")
	res := gjson.ParseBytes(got)
	
	content := res.Get("content").Array()
	// Should have ONLY tool_use block if content is empty
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	
	if content[0].Get("type").String() != "tool_use" {
		t.Errorf("expected tool_use block, got %s", content[0].Get("type").String())
	}
}

func TestExtractThinkingFromContent(t *testing.T) {
	content := "Before <thinking>thought</thinking> After"
	blocks := ExtractThinkingFromContent(content)
	
	if len(blocks) != 3 {
		t.Fatalf("expected 3 blocks, got %d", len(blocks))
	}
	
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "Before " {
		t.Errorf("first block mismatch: %v", blocks[0])
	}
	
	if blocks[1]["type"] != "thinking" || blocks[1]["thinking"] != "thought" {
		t.Errorf("second block mismatch: %v", blocks[1])
	}
	
	if blocks[2]["type"] != "text" || blocks[2]["text"] != " After" {
		t.Errorf("third block mismatch: %v", blocks[2])
	}
}

func TestGenerateThinkingSignature(t *testing.T) {
	s1 := generateThinkingSignature("test")
	s2 := generateThinkingSignature("test")
	if s1 == "" || s1 != s2 {
		t.Errorf("expected deterministic non-empty signature, got %s, %s", s1, s2)
	}
	if generateThinkingSignature("") != "" {
		t.Error("expected empty signature for empty content")
	}
}

func TestBuildClaudeResponse_Truncated(t *testing.T) {
	toolUses := []KiroToolUse{
		{
			ToolUseID: "c1",
			Name:      "f1",
			IsTruncated: true,
			TruncationInfo: &TruncationInfo{},
		},
	}
	got := BuildClaudeResponse("", toolUses, "model", usage.Detail{}, "tool_use")
	res := gjson.ParseBytes(got)
	
	content := res.Get("content").Array()
	if len(content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(content))
	}
	
	if content[0].Get("input._status").String() != "SOFT_LIMIT_REACHED" {
		t.Errorf("expected SOFT_LIMIT_REACHED status, got %v", content[0].Get("input._status").String())
	}
}

func TestExtractThinkingFromContent_Complex(t *testing.T) {
	// Missing closing tag
	content2 := "<thinking>Incomplete"
	blocks2 := ExtractThinkingFromContent(content2)
	if len(blocks2) != 1 || blocks2[0]["type"] != "thinking" {
		t.Errorf("expected 1 thinking block for missing closing tag, got %v", blocks2)
	}
	
	// Multiple thinking blocks
	content3 := "<thinking>T1</thinking> and <thinking>T2</thinking>"
	blocks3 := ExtractThinkingFromContent(content3)
	if len(blocks3) != 3 { // T1, " and ", T2
		t.Errorf("expected 3 blocks for multiple thinking, got %d", len(blocks3))
	}
}
