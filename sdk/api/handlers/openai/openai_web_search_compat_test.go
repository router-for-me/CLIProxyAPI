package openai

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeChatWebSearchRequestOptions(t *testing.T) {
	input := []byte(`{
		"model":"grok-4.5",
		"messages":[{"role":"user","content":"Search current news"}],
		"web_search_options":{
			"search_context_size":"high",
			"user_location":{"type":"approximate","approximate":{"city":"Shanghai","country":"CN"}}
		}
	}`)

	out := normalizeChatWebSearchRequest(input)
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools length = %d, want 1: %s", len(tools), string(out))
	}
	tool := tools[0]
	if got := tool.Get("type").String(); got != "web_search" {
		t.Fatalf("tool type = %q, want web_search: %s", got, string(out))
	}
	if got := tool.Get("search_context_size").String(); got != "high" {
		t.Fatalf("search_context_size = %q, want high: %s", got, string(out))
	}
	if got := tool.Get("user_location.city").String(); got != "Shanghai" {
		t.Fatalf("user_location.city = %q, want Shanghai: %s", got, string(out))
	}
	if tool.Get("user_location.approximate").Exists() {
		t.Fatalf("Chat user_location should be flattened for Responses: %s", string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice").String(); got != "required" {
		t.Fatalf("tool_choice = %q, want required: %s", got, string(out))
	}
}

func TestNormalizeChatWebSearchRequestAvoidsDuplicateAndNormalizesAliases(t *testing.T) {
	input := []byte(`{
		"messages":[{"role":"user","content":"Search"}],
		"tools":[{"type":"web_search_preview_2025_03_11"}],
		"tool_choice":{"type":"web_search_2025_08_26"},
		"web_search_options":{"search_context_size":"low"}
	}`)

	out := normalizeChatWebSearchRequest(input)
	tools := gjson.GetBytes(out, "tools").Array()
	if len(tools) != 1 {
		t.Fatalf("tools length = %d, want 1: %s", len(tools), string(out))
	}
	if got := tools[0].Get("type").String(); got != "web_search" {
		t.Fatalf("tool type = %q, want web_search: %s", got, string(out))
	}
	if got := gjson.GetBytes(out, "tool_choice.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.type = %q, want web_search: %s", got, string(out))
	}
}

func TestAddChatWebSearchAnnotations(t *testing.T) {
	request := []byte(`{"tools":[{"type":"web_search"}]}`)
	response := []byte(`{"choices":[{"message":{"role":"assistant","content":"你好 [[1]](https://example.com/source) and [Second](https://example.org/two)"}}]}`)

	out := addChatWebSearchAnnotations(request, response)
	annotations := gjson.GetBytes(out, "choices.0.message.annotations").Array()
	if len(annotations) != 2 {
		t.Fatalf("annotations length = %d, want 2: %s", len(annotations), string(out))
	}
	first := annotations[0].Get("url_citation")
	if got := first.Get("url").String(); got != "https://example.com/source" {
		t.Fatalf("first URL = %q: %s", got, string(out))
	}
	if got := first.Get("title").String(); got != "1" {
		t.Fatalf("first title = %q, want 1: %s", got, string(out))
	}
	if got := first.Get("start_index").Int(); got != 3 {
		t.Fatalf("first start_index = %d, want 3: %s", got, string(out))
	}
	wantEnd := int64(3 + len([]rune(`[[1]](https://example.com/source)`)))
	if got := first.Get("end_index").Int(); got != wantEnd {
		t.Fatalf("first end_index = %d, want %d: %s", got, wantEnd, string(out))
	}
	if got := annotations[1].Get("url_citation.title").String(); got != "Second" {
		t.Fatalf("second title = %q, want Second: %s", got, string(out))
	}
}

func TestAddChatWebSearchAnnotationsLeavesNonSearchAndExistingAnnotationsAlone(t *testing.T) {
	response := []byte(`{"choices":[{"message":{"content":"[Link](https://example.com)"}}]}`)
	if out := addChatWebSearchAnnotations([]byte(`{"messages":[]}`), response); !bytes.Equal(out, response) {
		t.Fatalf("non-search response changed: %s", string(out))
	}

	withAnnotations := []byte(`{"choices":[{"message":{"content":"[Link](https://example.com)","annotations":[]}}]}`)
	if out := addChatWebSearchAnnotations([]byte(`{"tools":[{"type":"web_search"}]}`), withAnnotations); !bytes.Equal(out, withAnnotations) {
		t.Fatalf("existing annotations changed: %s", string(out))
	}
}
