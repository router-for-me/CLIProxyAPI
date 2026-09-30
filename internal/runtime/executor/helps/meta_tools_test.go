package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeMetaWebSearchTools_StripsSearchContentTypesFromWebSearch(t *testing.T) {
	input := []byte(`{
		"model": "muse-spark-1.3",
		"tools": [
			{
				"type": "function",
				"name": "lookup",
				"parameters": {"type": "object"}
			},
			{
				"type": "web_search",
				"external_web_access": true,
				"search_content_types": ["text", "image"]
			},
			{
				"type": "web_search_preview",
				"search_content_types": ["text", "image"]
			},
			{
				"type": "namespace",
				"name": "search_group",
				"tools": [
					{
						"type": "web_search",
						"search_content_types": ["text"]
					}
				]
			}
		]
	}`)

	got := SanitizeMetaWebSearchTools(input)

	// web_search at index 1 must have search_content_types stripped
	tool1 := gjson.GetBytes(got, "tools.1")
	if tool1.Get("type").String() != "web_search" {
		t.Fatalf("tools.1 type = %q, want web_search", tool1.Get("type").String())
	}
	if tool1.Get("search_content_types").Exists() {
		t.Fatalf("tools.1 still has search_content_types: %s", tool1.Raw)
	}
	if !tool1.Get("external_web_access").Bool() {
		t.Fatalf("tools.1 external_web_access was lost: %s", tool1.Raw)
	}

	// web_search_preview at index 2 must retain search_content_types
	tool2 := gjson.GetBytes(got, "tools.2")
	if tool2.Get("type").String() != "web_search_preview" {
		t.Fatalf("tools.2 type = %q, want web_search_preview", tool2.Get("type").String())
	}
	if !tool2.Get("search_content_types").Exists() {
		t.Fatalf("tools.2 search_content_types missing: %s", tool2.Raw)
	}

	// nested web_search in namespace at index 3 must have search_content_types stripped
	nestedTool := gjson.GetBytes(got, "tools.3.tools.0")
	if nestedTool.Get("type").String() != "web_search" {
		t.Fatalf("tools.3.tools.0 type = %q, want web_search", nestedTool.Get("type").String())
	}
	if nestedTool.Get("search_content_types").Exists() {
		t.Fatalf("tools.3.tools.0 still has search_content_types: %s", nestedTool.Raw)
	}

	// function tool at index 0 must be unchanged
	tool0 := gjson.GetBytes(got, "tools.0")
	if tool0.Get("name").String() != "lookup" {
		t.Fatalf("tools.0 name = %q, want lookup", tool0.Get("name").String())
	}
}

func TestSanitizeMetaWebSearchTools_NoToolsOrEmpty(t *testing.T) {
	if got := SanitizeMetaWebSearchTools(nil); len(got) != 0 {
		t.Fatalf("expected empty output for nil input, got %s", got)
	}

	payloadWithoutTools := []byte(`{"model":"muse-spark-1.3","input":"hello"}`)
	if got := SanitizeMetaWebSearchTools(payloadWithoutTools); string(got) != string(payloadWithoutTools) {
		t.Fatalf("expected unmodified payload, got %s", got)
	}
}
