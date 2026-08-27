package util

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestCollectResponsesToolDescriptors_PriorityAndNamespace(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "function", "name": "top_fn", "description": "top function"}
		],
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "namespace",
						"name": "ns1",
						"tools": [
							{"type": "function", "name": "child_fn", "description": "child function"},
							{"type": "custom", "name": "child_custom", "description": "child custom"}
						]
					},
					{"type": "custom", "name": "direct_custom"}
				]
			}
		]
	}`

	root := gjson.Parse(raw)
	descriptors := CollectResponsesToolDescriptors(root)
	if len(descriptors) != 4 {
		t.Fatalf("expected 4 descriptors, got %d", len(descriptors))
	}

	decls, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(root)
	if len(decls) != 4 {
		t.Fatalf("expected 4 declarations, got %d", len(decls))
	}

	if forwardMap["ns1__child_fn"] != "ns1__child_fn" {
		t.Fatalf("forwardMap['ns1__child_fn'] = %q, want ns1__child_fn", forwardMap["ns1__child_fn"])
	}

	childCustomIdentity := reverseMap["ns1__child_custom"]
	if childCustomIdentity.Name != "child_custom" || childCustomIdentity.Namespace != "ns1" || !childCustomIdentity.Custom {
		t.Fatalf("unexpected reverseMap for ns1__child_custom: %+v", childCustomIdentity)
	}

	topFnIdentity := reverseMap["top_fn"]
	if topFnIdentity.Name != "top_fn" || topFnIdentity.Namespace != "" || topFnIdentity.Custom {
		t.Fatalf("unexpected reverseMap for top_fn: %+v", topFnIdentity)
	}
}

func TestResponsesToolWinners_TopLevelBeatsAdditionalTools(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "function", "name": "shared_fn", "description": "top level"}
		],
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{"type": "function", "name": "shared_fn", "description": "additional"}
				]
			}
		]
	}`

	root := gjson.Parse(raw)
	winners := CollectResponsesToolWinners(root)
	winner := winners["shared_fn"]
	if winner.SourcePriority != 0 {
		t.Fatalf("winner priority = %d, want 0", winner.SourcePriority)
	}
	if winner.Tool.Get("description").String() != "top level" {
		t.Fatalf("winner description = %q, want 'top level'", winner.Tool.Get("description").String())
	}
}

func TestResponsesToolWinners_DirectBeatsNamespaceChild(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "namespace", "name": "n", "tools": [{"type": "function", "name": "x", "description": "namespace child"}]},
			{"type": "custom", "name": "n__x", "description": "direct"}
		]
	}`

	root := gjson.Parse(raw)
	winners := CollectResponsesToolWinners(root)
	winner := winners["n__x"]
	if !winner.Direct {
		t.Fatalf("winner direct = %v, want true", winner.Direct)
	}
	if winner.ToolType != "custom" {
		t.Fatalf("winner toolType = %q, want custom", winner.ToolType)
	}
}

func TestConvertResponsesToolChoiceToGemini(t *testing.T) {
	tests := []struct {
		name       string
		choiceJSON string
		forwardMap map[string]string
		wantMode   string
		wantNames  []string
	}{
		{
			name:       "auto string",
			choiceJSON: `"auto"`,
			wantMode:   "AUTO",
		},
		{
			name:       "none string",
			choiceJSON: `"none"`,
			wantMode:   "NONE",
		},
		{
			name:       "required string",
			choiceJSON: `"required"`,
			wantMode:   "ANY",
		},
		{
			name:       "function object with namespace",
			choiceJSON: `{"type": "function", "name": "my_fn", "namespace": "my_ns"}`,
			forwardMap: map[string]string{"my_ns__my_fn": "my_ns__my_fn"},
			wantMode:   "ANY",
			wantNames:  []string{"my_ns__my_fn"},
		},
		{
			name:       "custom object",
			choiceJSON: `{"type": "custom", "name": "exec", "namespace": "functions"}`,
			forwardMap: map[string]string{"functions__exec": "functions__exec"},
			wantMode:   "ANY",
			wantNames:  []string{"functions__exec"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			choice := gjson.Parse(tt.choiceJSON)
			out, ok := ConvertResponsesToolChoiceToGemini(choice, tt.forwardMap)
			if !ok {
				t.Fatalf("ConvertResponsesToolChoiceToGemini returned false")
			}
			mode := gjson.GetBytes(out, "mode").String()
			if mode != tt.wantMode {
				t.Fatalf("mode = %q, want %q", mode, tt.wantMode)
			}
			if len(tt.wantNames) > 0 {
				names := gjson.GetBytes(out, "allowedFunctionNames").Array()
				if len(names) != len(tt.wantNames) {
					t.Fatalf("allowedFunctionNames count = %d, want %d", len(names), len(tt.wantNames))
				}
				for i, want := range tt.wantNames {
					if names[i].String() != want {
						t.Fatalf("allowedFunctionNames[%d] = %q, want %q", i, names[i].String(), want)
					}
				}
			}
		})
	}
}

func TestUnwrapResponsesCustomToolInput(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: `{"input":"pwd"}`, want: "pwd"},
		{input: `{"input":{"cmd":"ls"}}`, want: `{"cmd":"ls"}`},
		{input: `"direct text"`, want: "direct text"},
		{input: `{}`, want: ""},
		{input: ``, want: ""},
	}

	for _, tt := range tests {
		got := UnwrapResponsesCustomToolInput(tt.input)
		if got != tt.want {
			t.Errorf("UnwrapResponsesCustomToolInput(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestBuildGeminiFunctionDeclarations_DisambiguationAndLongNames(t *testing.T) {
	// Two tools that genuinely collide after sanitization (e.g. "read/file" vs "read_file"), and one > 64 chars
	raw := `{
		"tools": [
			{"type": "function", "name": "read/file", "description": "tool with slash"},
			{"type": "function", "name": "read_file", "description": "tool with underscore"},
			{"type": "custom", "name": "mcp__very_very_very_very_very_very_long_namespace_name__very_very_very_long_custom_tool_name_that_exceeds_sixty_four_chars"}
		]
	}`

	root := gjson.Parse(raw)
	decls, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(root)
	if len(decls) != 3 {
		t.Fatalf("expected 3 decls, got %d", len(decls))
	}

	name1 := forwardMap["read/file"]
	name2 := forwardMap["read_file"]
	if name1 == name2 {
		t.Fatalf("colliding tools mapped to identical name: %q", name1)
	}

	identity1 := reverseMap[name1]
	if identity1.Name != "read/file" {
		t.Fatalf("reverseMap[%q].Name = %q, want read/file", name1, identity1.Name)
	}
	identity2 := reverseMap[name2]
	if identity2.Name != "read_file" {
		t.Fatalf("reverseMap[%q].Name = %q, want read_file", name2, identity2.Name)
	}

	longName := forwardMap["mcp__very_very_very_very_very_very_long_namespace_name__very_very_very_long_custom_tool_name_that_exceeds_sixty_four_chars"]
	if len(longName) > 64 {
		t.Fatalf("long tool name length = %d > 64: %q", len(longName), longName)
	}

	identityLong := reverseMap[longName]
	if !identityLong.Custom || identityLong.Name != "mcp__very_very_very_very_very_very_long_namespace_name__very_very_very_long_custom_tool_name_that_exceeds_sixty_four_chars" {
		t.Fatalf("unexpected reverse identity for long name: %+v", identityLong)
	}
}
