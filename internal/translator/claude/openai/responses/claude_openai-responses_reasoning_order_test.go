package responses

import (
	"fmt"
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToClaude_KeepsLatestConsecutiveReasoning(t *testing.T) {
	firstRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-first")
	secondRaw, _ := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-second")
	thirdRaw, thirdSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-third")

	raw := responsesRequestFromItems(
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"prefix"}]}`,
		responsesReasoningItem(firstRaw, "first reasoning"),
		responsesReasoningItem(secondRaw, "second reasoning"),
		responsesReasoningItem(thirdRaw, "third reasoning"),
		responsesFunctionCallItem("call_latest", "latest_tool"),
		responsesFunctionCallOutputItem("call_latest", "done"),
	)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	wantTypes := []string{"text", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("text").String(); got != "prefix" {
		t.Fatalf("prefix text = %q, want prefix. Output: %s", got, out)
	}
	if got := content[1].Get("thinking").String(); got != "third reasoning" {
		t.Fatalf("thinking text = %q, want latest consecutive reasoning. Output: %s", got, out)
	}
	if got := content[1].Get("signature").String(); got != thirdSignature {
		t.Fatalf("thinking signature = %q, want latest signature %q. Output: %s", got, thirdSignature, out)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolCallsSeparateReasoningBlocks(t *testing.T) {
	firstRaw, firstSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-first")
	secondRaw, secondSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-second")

	raw := responsesRequestFromItems(
		responsesReasoningItem(firstRaw, "first reasoning"),
		responsesFunctionCallItem("call_first", "first_tool"),
		responsesReasoningItem(secondRaw, "second reasoning"),
		responsesFunctionCallItem("call_second", "second_tool"),
		responsesFunctionCallOutputItem("call_first", "first result"),
		responsesFunctionCallOutputItem("call_second", "second result"),
	)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	wantTypes := []string{"thinking", "tool_use", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("signature").String(); got != firstSignature {
		t.Fatalf("first thinking signature = %q, want %q", got, firstSignature)
	}
	if got := content[2].Get("signature").String(); got != secondSignature {
		t.Fatalf("second thinking signature = %q, want %q", got, secondSignature)
	}
	if got := content[1].Get("id").String(); got != "call_first" {
		t.Fatalf("first tool_use id = %q, want call_first", got)
	}
	if got := content[3].Get("id").String(); got != "call_second" {
		t.Fatalf("second tool_use id = %q, want call_second", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_NonThinkingBlocksSeparateReasoning(t *testing.T) {
	firstRaw, firstSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-first")
	secondRaw, secondSignature := testClaudeResponsesThinkingSignatureForModel(t, "claude-opus-5-second")
	const redactedData = "opaque-redacted-data"

	raw := responsesRequestFromItems(
		responsesReasoningItem(firstRaw, "first reasoning"),
		`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"visible separator"}]}`,
		responsesReasoningItem(secondRaw, "second reasoning"),
		`{"type":"reasoning","encrypted_content":"`+ClaudeResponsesRedactedThinkingPrefix+redactedData+`","summary":[]}`,
		responsesReasoningItem(firstRaw, "third reasoning"),
		responsesFunctionCallItem("call_separator", "separator_tool"),
		responsesFunctionCallOutputItem("call_separator", "done"),
	)

	out := ConvertOpenAIResponsesRequestToClaude("claude-test", raw, false)
	content := gjson.GetBytes(out, "messages.0.content").Array()
	wantTypes := []string{"thinking", "text", "thinking", "redacted_thinking", "thinking", "tool_use"}
	if len(content) != len(wantTypes) {
		t.Fatalf("assistant content count = %d, want %d. Output: %s", len(content), len(wantTypes), out)
	}
	for index, wantType := range wantTypes {
		if got := content[index].Get("type").String(); got != wantType {
			t.Fatalf("assistant content[%d].type = %q, want %q. Output: %s", index, got, wantType, out)
		}
	}
	if got := content[0].Get("signature").String(); got != firstSignature {
		t.Fatalf("first thinking signature = %q, want %q", got, firstSignature)
	}
	if got := content[2].Get("signature").String(); got != secondSignature {
		t.Fatalf("second thinking signature = %q, want %q", got, secondSignature)
	}
	if got := content[3].Get("data").String(); got != redactedData {
		t.Fatalf("redacted data = %q, want %q", got, redactedData)
	}
	if got := content[4].Get("signature").String(); got != firstSignature {
		t.Fatalf("third thinking signature = %q, want %q", got, firstSignature)
	}
	if got := content[5].Get("id").String(); got != "call_separator" {
		t.Fatalf("tool_use id = %q, want call_separator", got)
	}
}

func responsesReasoningItem(signature, text string) string {
	return fmt.Sprintf(`{"type":"reasoning","encrypted_content":%q,"summary":[{"type":"summary_text","text":%q}]}`, signature, text)
}

func responsesFunctionCallItem(callID, name string) string {
	return fmt.Sprintf(`{"type":"function_call","call_id":%q,"name":%q,"arguments":"{}"}`, callID, name)
}

func responsesFunctionCallOutputItem(callID, output string) string {
	return fmt.Sprintf(`{"type":"function_call_output","call_id":%q,"output":%q}`, callID, output)
}
