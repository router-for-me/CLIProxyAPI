package common

import (
	"testing"
)

func TestDeriveClaudeUserID_SameConversationIsStable(t *testing.T) {
	raw := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`)
	first := DeriveClaudeUserID(raw)
	second := DeriveClaudeUserID(raw)
	if first == "" {
		t.Fatal("expected non-empty user_id")
	}
	if first != second {
		t.Fatalf("same conversation produced different user_id: %q vs %q", first, second)
	}
}

func TestDeriveClaudeUserID_PreservesCallerSuppliedMetadataUserID(t *testing.T) {
	testCases := []struct {
		name     string
		rawJSON  string
		expected string
	}{
		{
			name:     "plain string",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"caller-123"},"messages":[{"role":"user","content":"hello"}]}`,
			expected: "caller-123",
		},
		{
			name:     "whitespace preserved",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"  caller-spaces  "},"messages":[{"role":"user","content":"hello"}]}`,
			expected: "  caller-spaces  ",
		},
		{
			name:     "special characters",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"foo\"bar\nbaz\\qux"},"messages":[{"role":"user","content":"hello"}]}`,
			expected: "foo\"bar\nbaz\\qux",
		},
		{
			name:     "claude code json string",
			rawJSON:  `{"model":"claude-test","metadata":{"user_id":"{\"device_id\":\"dev-1\",\"session_id\":\"sess-1\"}"},"messages":[{"role":"user","content":"hello"}]}`,
			expected: `{"device_id":"dev-1","session_id":"sess-1"}`,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DeriveClaudeUserID([]byte(tc.rawJSON)); got != tc.expected {
				t.Fatalf("caller-supplied metadata.user_id not preserved, got %q want %q", got, tc.expected)
			}
		})
	}
}

func TestDeriveClaudeUserID_PreservesOpenAIUserField(t *testing.T) {
	raw := []byte(`{"model":"claude-test","user":"openai-user-456","messages":[{"role":"user","content":"hello"}]}`)
	if got := DeriveClaudeUserID(raw); got != "openai-user-456" {
		t.Fatalf("caller-supplied user not preserved, got %q", got)
	}
}

func TestDeriveClaudeUserID_MetadataUserIDTakesPriorityOverUserField(t *testing.T) {
	raw := []byte(`{"model":"claude-test","metadata":{"user_id":"meta-user-1"},"user":"openai-user-2","messages":[{"role":"user","content":"hello"}]}`)
	if got := DeriveClaudeUserID(raw); got != "meta-user-1" {
		t.Fatalf("metadata.user_id should take priority over user field, got %q", got)
	}
}

func TestDeriveClaudeUserID_CaseInsensitiveUserRole(t *testing.T) {
	rawA := []byte(`{"model":"claude-test","messages":[{"role":"User","content":"message A"}]}`)
	rawB := []byte(`{"model":"claude-test","messages":[{"role":"USER","content":"message B"}]}`)
	idA := DeriveClaudeUserID(rawA)
	idB := DeriveClaudeUserID(rawB)
	if idA == "" || idB == "" || idA == "unknown" || idB == "unknown" {
		t.Fatalf("expected valid derived user_id for uppercase User role, got idA=%q idB=%q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("different messages with User role produced same user_id: %q", idA)
	}
}

func TestDeriveClaudeUserID_IgnoresNonStringMetadataUserIDOrUser(t *testing.T) {
	raw := []byte(`{"model":"claude-test","metadata":{"user_id":12345},"user":true,"messages":[{"role":"user","content":"hello"}]}`)
	got := DeriveClaudeUserID(raw)
	if got == "" || got == "12345" || got == "true" {
		t.Fatalf("non-string user_id should be ignored and derived, got %q", got)
	}
}

func TestDeriveClaudeUserID_DifferentSessionsAreDifferent(t *testing.T) {
	a := []byte(`{"model":"claude-test","prompt_cache_key":"session-a","messages":[{"role":"user","content":"hello"}]}`)
	b := []byte(`{"model":"claude-test","prompt_cache_key":"session-b","messages":[{"role":"user","content":"hello"}]}`)
	idA := DeriveClaudeUserID(a)
	idB := DeriveClaudeUserID(b)
	if idA == idB {
		t.Fatalf("different prompt_cache_key produced same user_id: %q", idA)
	}
}

func TestDeriveClaudeUserID_SessionIDVariants(t *testing.T) {
	a := []byte(`{"model":"claude-test","session_id":"sess-a","messages":[{"role":"user","content":"hello"}]}`)
	b := []byte(`{"model":"claude-test","sessionId":"sess-b","messages":[{"role":"user","content":"hello"}]}`)
	idA := DeriveClaudeUserID(a)
	idB := DeriveClaudeUserID(b)
	if idA == "" || idB == "" {
		t.Fatal("expected non-empty user_id for session_id/sessionId")
	}
	if idA == idB {
		t.Fatalf("different session ids produced same user_id: %q", idA)
	}
}

func TestDeriveClaudeUserID_ConversationIDVariants(t *testing.T) {
	cObj := []byte(`{"model":"claude-test","conversation":{"id":"conv-1"},"messages":[{"role":"user","content":"hello"}]}`)
	cStr := []byte(`{"model":"claude-test","conversation":"conv-2","messages":[{"role":"user","content":"hello"}]}`)
	cFlat := []byte(`{"model":"claude-test","conversation_id":"conv-3","messages":[{"role":"user","content":"hello"}]}`)

	idObj := DeriveClaudeUserID(cObj)
	idStr := DeriveClaudeUserID(cStr)
	idFlat := DeriveClaudeUserID(cFlat)

	if idObj == "" || idStr == "" || idFlat == "" {
		t.Fatal("expected non-empty user_id for conversation variants")
	}
	if idObj == idStr || idObj == idFlat || idStr == idFlat {
		t.Fatalf("different conversation ids produced identical user_ids: obj=%q str=%q flat=%q", idObj, idStr, idFlat)
	}
}

func TestDeriveClaudeUserID_TurnGrowthKeepsSameUserID(t *testing.T) {
	first := []byte(`{"model":"claude-test","prompt_cache_key":"session-1","messages":[{"role":"user","content":"hello"}]}`)
	second := []byte(`{"model":"claude-test","prompt_cache_key":"session-1","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"follow up"}]}`)
	idFirst := DeriveClaudeUserID(first)
	idSecond := DeriveClaudeUserID(second)
	if idFirst != idSecond {
		t.Fatalf("conversation turn growth changed user_id: %q vs %q", idFirst, idSecond)
	}
}

func TestDeriveClaudeUserID_TurnGrowthWithoutSessionKeyKeepsSameUserID(t *testing.T) {
	first := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"first prompt"}]}`)
	second := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"first prompt"},{"role":"assistant","content":"hi"},{"role":"user","content":"second prompt"}]}`)
	idFirst := DeriveClaudeUserID(first)
	idSecond := DeriveClaudeUserID(second)
	if idFirst == "" || idFirst == "unknown" {
		t.Fatalf("expected valid derived user_id, got %q", idFirst)
	}
	if idFirst != idSecond {
		t.Fatalf("conversation turn growth without session key changed user_id: %q vs %q", idFirst, idSecond)
	}
}

func TestDeriveClaudeUserID_GeminiTurnGrowthWithoutSessionKeyKeepsSameUserID(t *testing.T) {
	first := []byte(`{"contents":[{"role":"user","parts":[{"text":"first gemini prompt"}]}]}`)
	second := []byte(`{"contents":[{"role":"user","parts":[{"text":"first gemini prompt"}]},{"role":"model","parts":[{"text":"answer"}]},{"role":"user","parts":[{"text":"second prompt"}]}]}`)
	idFirst := DeriveClaudeUserID(first)
	idSecond := DeriveClaudeUserID(second)
	if idFirst == "" || idFirst == "unknown" {
		t.Fatalf("expected valid derived user_id, got %q", idFirst)
	}
	if idFirst != idSecond {
		t.Fatalf("gemini turn growth without session key changed user_id: %q vs %q", idFirst, idSecond)
	}
}

func TestDeriveClaudeUserID_FirstMessageFallback(t *testing.T) {
	rawA := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"message A"}]}`)
	rawB := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"message B"}]}`)
	idA := DeriveClaudeUserID(rawA)
	idB := DeriveClaudeUserID(rawB)
	if idA == "" || idB == "" || idA == "unknown" || idB == "unknown" {
		t.Fatalf("expected valid derived user_id, got idA=%q idB=%q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("different first messages produced same user_id: %q", idA)
	}
}

func TestDeriveClaudeUserID_ResponsesInputString(t *testing.T) {
	rawA := []byte(`{"model":"claude-test","input":"hello world A"}`)
	rawB := []byte(`{"model":"claude-test","input":"hello world B"}`)
	idA := DeriveClaudeUserID(rawA)
	idB := DeriveClaudeUserID(rawB)
	if idA == "" || idB == "" || idA == "unknown" || idB == "unknown" {
		t.Fatalf("expected valid derived user_id for input string, got idA=%q idB=%q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("different input strings produced same user_id: %q", idA)
	}
}

func TestDeriveClaudeUserID_ResponsesInputArraySkipsSystemLevelItems(t *testing.T) {
	rawA := []byte(`{
		"model": "claude-test",
		"input": [
			{"type": "message", "role": "system", "content": "system prompt"},
			{"type": "message", "role": "developer", "content": "dev prompt"},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "user message A"}]}
		]
	}`)
	rawB := []byte(`{
		"model": "claude-test",
		"input": [
			{"type": "message", "role": "system", "content": "system prompt"},
			{"type": "message", "role": "developer", "content": "dev prompt"},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "user message B"}]}
		]
	}`)
	idA := DeriveClaudeUserID(rawA)
	idB := DeriveClaudeUserID(rawB)
	if idA == "" || idB == "" || idA == "unknown" || idB == "unknown" {
		t.Fatalf("expected valid derived user_id, got idA=%q idB=%q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("different user messages with same system prompt produced identical user_id: %q", idA)
	}
}

func TestDeriveClaudeUserID_GeminiContentsDefaultRole(t *testing.T) {
	rawA := []byte(`{"contents":[{"parts":[{"text":"gemini message A"}]}]}`)
	rawB := []byte(`{"contents":[{"parts":[{"text":"gemini message B"}]}]}`)
	idA := DeriveClaudeUserID(rawA)
	idB := DeriveClaudeUserID(rawB)
	if idA == "" || idB == "" || idA == "unknown" || idB == "unknown" {
		t.Fatalf("expected valid derived user_id for gemini without explicit role, got idA=%q idB=%q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("different gemini messages produced same user_id: %q", idA)
	}
}

func TestDeriveClaudeUserID_GeminiContentsMultipleTextParts(t *testing.T) {
	rawA := []byte(`{"contents":[{"role":"user","parts":[{"text":"Prefix"},{"text":"Question A"}]}]}`)
	rawB := []byte(`{"contents":[{"role":"user","parts":[{"text":"Prefix"},{"text":"Question B"}]}]}`)
	idA := DeriveClaudeUserID(rawA)
	idB := DeriveClaudeUserID(rawB)
	if idA == "" || idB == "" || idA == "unknown" || idB == "unknown" {
		t.Fatalf("expected valid derived user_id for gemini multiple parts, got idA=%q idB=%q", idA, idB)
	}
	if idA == idB {
		t.Fatalf("different second parts produced same user_id: %q", idA)
	}
}

func TestDeriveClaudeUserID_GeminiContentsSkipsThoughtParts(t *testing.T) {
	raw := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [
					{"thought": true, "text": "internal thought"},
					{"text": "visible content"}
				]
			}
		]
	}`)
	rawOnlyVisible := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [
					{"text": "visible content"}
				]
			}
		]
	}`)
	id1 := DeriveClaudeUserID(raw)
	id2 := DeriveClaudeUserID(rawOnlyVisible)
	if id1 != id2 {
		t.Fatalf("thought part changed derived user_id: %q vs %q", id1, id2)
	}
}

func TestDeriveClaudeUserID_GeminiSystemInstruction(t *testing.T) {
	rawCamel := []byte(`{"systemInstruction":{"parts":[{"text":"system rule A"}]}}`)
	rawSnake := []byte(`{"system_instruction":{"parts":[{"text":"system rule B"}]}}`)
	idCamel := DeriveClaudeUserID(rawCamel)
	idSnake := DeriveClaudeUserID(rawSnake)
	if idCamel == "" || idSnake == "" || idCamel == "unknown" || idSnake == "unknown" {
		t.Fatalf("expected valid derived user_id for systemInstruction, got camel=%q snake=%q", idCamel, idSnake)
	}
	if idCamel == idSnake {
		t.Fatalf("different system instructions produced same user_id: %q", idCamel)
	}
}
