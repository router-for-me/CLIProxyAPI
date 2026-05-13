package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeKimiToolMessageLinks_UsesCallIDFallback(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"list_directory:1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]},
			{"role":"tool","call_id":"list_directory:1","content":"[]"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	if got != "list_directory:1" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "list_directory:1")
	}
}

func TestNormalizeKimiToolMessageLinks_InferSinglePendingID(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_123","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","content":"file-content"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	if got != "call_123" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_123")
	}
}

func TestNormalizeKimiToolMessageLinks_AmbiguousMissingIDIsNotInferred(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[
				{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}},
				{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{}"}}
			]},
			{"role":"tool","content":"result-without-id"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	if gjson.GetBytes(out, "messages.1.tool_call_id").Exists() {
		t.Fatalf("messages.1.tool_call_id should be absent for ambiguous case, got %q", gjson.GetBytes(out, "messages.1.tool_call_id").String())
	}
}

func TestNormalizeKimiToolMessageLinks_PreservesExistingToolCallID(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","call_id":"different-id","content":"result"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.tool_call_id").String()
	if got != "call_1" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_1")
	}
}

func TestNormalizeKimiToolMessageLinks_InheritsPreviousReasoningForAssistantToolCalls(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":"plan","reasoning_content":"previous reasoning"},
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.1.reasoning_content").String()
	if got != "previous reasoning" {
		t.Fatalf("messages.1.reasoning_content = %q, want %q", got, "previous reasoning")
	}
}

func TestNormalizeKimiToolMessageLinks_InsertsFallbackReasoningWhenMissing(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	reasoning := gjson.GetBytes(out, "messages.0.reasoning_content")
	if !reasoning.Exists() {
		t.Fatalf("messages.0.reasoning_content should exist")
	}
	if reasoning.String() != "[reasoning unavailable]" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", reasoning.String(), "[reasoning unavailable]")
	}
}

func TestNormalizeKimiToolMessageLinks_UsesContentAsReasoningFallback(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":[{"type":"text","text":"first line"},{"type":"text","text":"second line"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.0.reasoning_content").String()
	if got != "first line\nsecond line" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "first line\nsecond line")
	}
}

func TestNormalizeKimiToolMessageLinks_ReplacesEmptyReasoningContent(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":"assistant summary","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}],"reasoning_content":""}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.0.reasoning_content").String()
	if got != "assistant summary" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "assistant summary")
	}
}

func TestNormalizeKimiToolMessageLinks_PreservesExistingAssistantReasoning(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}],"reasoning_content":"keep me"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	got := gjson.GetBytes(out, "messages.0.reasoning_content").String()
	if got != "keep me" {
		t.Fatalf("messages.0.reasoning_content = %q, want %q", got, "keep me")
	}
}

func TestNormalizeKimiToolMessageLinks_RepairsIDsAndReasoningTogether(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}],"reasoning_content":"r1"},
			{"role":"tool","call_id":"call_1","content":"[]"},
			{"role":"assistant","tool_calls":[{"id":"call_2","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
			{"role":"tool","call_id":"call_2","content":"file"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	if got := gjson.GetBytes(out, "messages.1.tool_call_id").String(); got != "call_1" {
		t.Fatalf("messages.1.tool_call_id = %q, want %q", got, "call_1")
	}
	if got := gjson.GetBytes(out, "messages.3.tool_call_id").String(); got != "call_2" {
		t.Fatalf("messages.3.tool_call_id = %q, want %q", got, "call_2")
	}
	if got := gjson.GetBytes(out, "messages.2.reasoning_content").String(); got != "r1" {
		t.Fatalf("messages.2.reasoning_content = %q, want %q", got, "r1")
	}
}

func TestNormalizeKimiToolMessageLinks_DropsEmptyAssistantWithoutToolLink(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"user","content":"start"},
			{"role":"assistant","content":""},
			{"role":"assistant","content":"   "},
			{"role":"assistant","content":"","tool_calls":null},
			{"role":"assistant","content":[{"type":"text","text":"  "}]},
			{"role":"assistant"},
			{"role":"assistant","content":"keep"},
			{"role":"user","content":"next"}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 3 {
		t.Fatalf("messages length = %d, want 3, raw = %s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}
	if got := messages[0].Get("content").String(); got != "start" {
		t.Fatalf("messages.0.content = %q, want %q", got, "start")
	}
	if got := messages[1].Get("content").String(); got != "keep" {
		t.Fatalf("messages.1.content = %q, want %q", got, "keep")
	}
	if got := messages[2].Get("content").String(); got != "next" {
		t.Fatalf("messages.2.content = %q, want %q", got, "next")
	}
}

func TestNormalizeKimiToolMessageLinks_PreservesAssistantWithToolLinkOrReasoning(t *testing.T) {
	body := []byte(`{
		"messages":[
			{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_directory","arguments":"{}"}}]},
			{"role":"assistant","content":"","function_call":{"name":"legacy_call","arguments":"{}"}},
			{"role":"assistant","content":"","reasoning_content":"thought"},
			{"role":"assistant","content":[{"type":"text","text":" visible "}]}
		]
	}`)

	out, err := normalizeKimiToolMessageLinks(body)
	if err != nil {
		t.Fatalf("normalizeKimiToolMessageLinks() error = %v", err)
	}

	messages := gjson.GetBytes(out, "messages").Array()
	if len(messages) != 4 {
		t.Fatalf("messages length = %d, want 4, raw = %s", len(messages), gjson.GetBytes(out, "messages").Raw)
	}
	if !messages[0].Get("tool_calls").Exists() {
		t.Fatalf("messages.0.tool_calls should exist")
	}
	if !messages[1].Get("function_call").Exists() {
		t.Fatalf("messages.1.function_call should exist")
	}
	if got := messages[2].Get("reasoning_content").String(); got != "thought" {
		t.Fatalf("messages.2.reasoning_content = %q, want %q", got, "thought")
	}
	if got := messages[3].Get("content.0.text").String(); got != " visible " {
		t.Fatalf("messages.3.content.0.text = %q, want %q", got, " visible ")
	}
}
