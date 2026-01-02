package executor

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAppendToolsAsContentForCounting(t *testing.T) {
	tests := []struct {
		name                  string
		input                 string
		wantToolsInContent    bool
		wantToolsFieldRemoved bool
	}{
		{
			name: "payload with tools - should append to contents",
			input: `{
				"request": {
					"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
					"tools": [{"functionDeclarations": [{"name": "test_tool", "description": "A test tool"}]}]
				}
			}`,
			wantToolsInContent:    true,
			wantToolsFieldRemoved: true,
		},
		{
			name: "payload without tools - should return unchanged",
			input: `{
				"request": {
					"contents": [{"role": "user", "parts": [{"text": "hello"}]}]
				}
			}`,
			wantToolsInContent:    false,
			wantToolsFieldRemoved: false,
		},
		{
			name: "payload with empty tools array - should return unchanged",
			input: `{
				"request": {
					"contents": [{"role": "user", "parts": [{"text": "hello"}]}],
					"tools": []
				}
			}`,
			wantToolsInContent:    false,
			wantToolsFieldRemoved: false,
		},
		{
			name: "payload with invalid contents type - should return unchanged",
			input: `{
				"request": {
					"contents": "not-an-array",
					"tools": [{"functionDeclarations": [{"name": "test_tool"}]}]
				}
			}`,
			wantToolsInContent:    false,
			wantToolsFieldRemoved: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appendToolsAsContentForCounting([]byte(tt.input))

			// Check if tools field is removed
			toolsField := gjson.GetBytes(result, "request.tools")
			if tt.wantToolsFieldRemoved && toolsField.Exists() {
				t.Errorf("expected request.tools to be removed, but it still exists")
			}
			if !tt.wantToolsFieldRemoved && !toolsField.Exists() && gjson.GetBytes([]byte(tt.input), "request.tools").Exists() {
				t.Errorf("expected request.tools to remain, but it was removed")
			}

			// Check if tools are appended to contents
			contents := gjson.GetBytes(result, "request.contents")
			if !contents.Exists() {
				t.Fatalf("expected request.contents to exist")
			}

			contentsArray := contents.Array()
			if tt.wantToolsInContent {
				// Should have original content items + tools
				inputContents := gjson.GetBytes([]byte(tt.input), "request.contents").Array()
				expectedLen := len(inputContents) + 1
				if len(contentsArray) != expectedLen {
					t.Errorf("expected %d content items, got %d", expectedLen, len(contentsArray))
				}

				// Last content item should contain tool definitions
				lastContent := contentsArray[len(contentsArray)-1]
				lastText := lastContent.Get("parts.0.text").String()
				if lastText == "" {
					t.Errorf("expected last content to have text with tools, got empty")
				}
				if !gjson.Valid(lastText[len(toolDefinitionsPrefix):]) {
					t.Errorf("expected tools JSON in last content text")
				}
			} else {
				// Should have only original content - compare raw JSON to ensure unchanged
				inputContentsRaw := gjson.GetBytes([]byte(tt.input), "request.contents").Raw
				if contents.Raw != inputContentsRaw {
					t.Errorf("expected contents to be unchanged, but was modified. got: %s, want: %s", contents.Raw, inputContentsRaw)
				}
			}
		})
	}
}

func TestAppendToolsAsContentForCounting_PreservesOriginalContent(t *testing.T) {
	input := `{
		"request": {
			"contents": [
				{"role": "user", "parts": [{"text": "first message"}]},
				{"role": "model", "parts": [{"text": "response"}]},
				{"role": "user", "parts": [{"text": "second message"}]}
			],
			"tools": [{"functionDeclarations": [{"name": "my_tool"}]}]
		}
	}`

	result := appendToolsAsContentForCounting([]byte(input))

	contents := gjson.GetBytes(result, "request.contents").Array()
	if len(contents) != 4 {
		t.Fatalf("expected 4 content items (3 original + 1 tools), got %d", len(contents))
	}

	// Verify original contents are preserved by comparing key fields
	// Note: Raw JSON comparison doesn't work due to field reordering during serialization
	inputContents := gjson.GetBytes([]byte(input), "request.contents").Array()
	for i, expected := range inputContents {
		if contents[i].Get("role").String() != expected.Get("role").String() {
			t.Errorf("content at index %d role not preserved. got %s, want %s", i, contents[i].Get("role").String(), expected.Get("role").String())
		}
		if contents[i].Get("parts.0.text").String() != expected.Get("parts.0.text").String() {
			t.Errorf("content at index %d text not preserved. got %s, want %s", i, contents[i].Get("parts.0.text").String(), expected.Get("parts.0.text").String())
		}
	}

	// Verify tools are in the last content
	lastText := contents[3].Get("parts.0.text").String()
	if len(lastText) < 20 {
		t.Errorf("expected tools content, got: %s", lastText)
	}
}

func TestAppendToolsAsContentForCounting_ValidJSON(t *testing.T) {
	input := `{
		"request": {
			"contents": [{"role": "user", "parts": [{"text": "test"}]}],
			"tools": [{"functionDeclarations": [{"name": "tool1"}, {"name": "tool2"}]}]
		}
	}`

	result := appendToolsAsContentForCounting([]byte(input))

	// Result should be valid JSON
	var parsed map[string]any
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}

	// Should not have request.tools
	if gjson.GetBytes(result, "request.tools").Exists() {
		t.Errorf("request.tools should be removed")
	}
}
