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

func TestBuildGeminiFunctionDeclarations_ClientToolSearchShim(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "function", "name": "exec_command", "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}},
			{"type": "tool_search", "execution": "client", "description": "Search deferred tools.", "parameters": {"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "number"}}, "required": ["query"], "additionalProperties": false}},
			{"type": "web_search"}
		]
	}`
	declarations, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))

	if len(declarations) != 2 {
		t.Fatalf("expected exec_command and tool_search declarations, got %d: %s", len(declarations), declarations)
	}
	shim := gjson.ParseBytes(declarations[1])
	if got := shim.Get("name").String(); got != ResponsesToolSearchFunctionName {
		t.Fatalf("shim name = %q, want %q", got, ResponsesToolSearchFunctionName)
	}
	if got := shim.Get("description").String(); got != "Search deferred tools." {
		t.Fatalf("shim description = %q, want original description", got)
	}
	if got := shim.Get("parametersJsonSchema.required.0").String(); got != "query" {
		t.Fatalf("shim parameters not preserved: %s", shim.Raw)
	}
	if got := forwardMap[ResponsesToolSearchFunctionName]; got != ResponsesToolSearchFunctionName {
		t.Fatalf("forward map for shim = %q", got)
	}
	identity, ok := reverseMap[ResponsesToolSearchFunctionName]
	if !ok || !identity.ToolSearch || identity.Custom || identity.Namespace != "" {
		t.Fatalf("shim identity = %+v, want ToolSearch", identity)
	}
	if identity := reverseMap["exec_command"]; identity.ToolSearch {
		t.Fatalf("regular function must not be marked ToolSearch: %+v", identity)
	}
}

func TestBuildGeminiFunctionDeclarations_ToolSearchShimDefaultsAndSkips(t *testing.T) {
	t.Run("default parameters", func(t *testing.T) {
		raw := `{"tools": [{"type": "tool_search", "execution": "client"}]}`
		declarations, _, _ := BuildGeminiFunctionDeclarations(gjson.Parse(raw))
		if len(declarations) != 1 {
			t.Fatalf("expected one shim declaration, got %d", len(declarations))
		}
		if got := gjson.GetBytes(declarations[0], "parametersJsonSchema.properties.query.type").String(); got != "string" {
			t.Fatalf("default parameters missing query: %s", declarations[0])
		}
	})
	t.Run("server executed search is not emulated", func(t *testing.T) {
		raw := `{"tools": [{"type": "tool_search"}, {"type": "tool_search", "execution": "server"}]}`
		declarations, _, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))
		if len(declarations) != 0 || len(reverseMap) != 0 {
			t.Fatalf("server-executed tool_search must be dropped, got %s", declarations)
		}
	})
	t.Run("existing function keeps the shim name", func(t *testing.T) {
		raw := `{"tools": [{"type": "function", "name": "tool_search", "description": "user tool"}, {"type": "tool_search", "execution": "client"}]}`
		declarations, _, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))
		if len(declarations) != 1 {
			t.Fatalf("expected the user function only, got %d declarations", len(declarations))
		}
		if identity := reverseMap["tool_search"]; identity.ToolSearch {
			t.Fatalf("user function must win over the shim: %+v", identity)
		}
	})
	t.Run("additional_tools declaration", func(t *testing.T) {
		raw := `{"input": [{"type": "additional_tools", "tools": [{"type": "tool_search", "execution": "client"}]}]}`
		_, _, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))
		if !reverseMap[ResponsesToolSearchFunctionName].ToolSearch {
			t.Fatalf("tool_search inside additional_tools must be exposed: %+v", reverseMap)
		}
	})
}

func TestBuildGeminiFunctionDeclarations_CollectsToolSearchOutputTools(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "namespace", "name": "mcp__cua_repl", "tools": [{"type": "function", "name": "js", "description": "current"}]}
		],
		"input": [
			{"type": "tool_search_call", "execution": "client", "call_id": "search_1", "status": "completed", "arguments": {"query": "fixture"}},
			{"type": "tool_search_output", "execution": "client", "call_id": "search_1", "status": "completed", "tools": [
				{"type": "namespace", "name": "fixture_tools", "description": "fixture", "tools": [
					{"type": "function", "name": "ping", "defer_loading": true, "parameters": {"type": "object", "properties": {}, "additionalProperties": false}}
				]},
				{"type": "namespace", "name": "mcp__cua_repl", "tools": [{"type": "function", "name": "js", "description": "discovered"}]}
			]},
			{"type": "tool_search_output", "execution": "client", "call_id": "search_2", "status": "completed", "tools": [
				{"type": "namespace", "name": "fixture_tools", "tools": [{"type": "function", "name": "ping", "description": "again"}]}
			]}
		]
	}`
	declarations, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))

	names := make([]string, 0, len(declarations))
	for _, declaration := range declarations {
		names = append(names, gjson.GetBytes(declaration, "name").String())
	}
	if len(names) != 2 || names[0] != "mcp__cua_repl__js" || names[1] != "fixture_tools__ping" {
		t.Fatalf("declarations = %v, want current js then discovered ping exactly once", names)
	}
	if got := gjson.GetBytes(declarations[0], "description").String(); got != "current" {
		t.Fatalf("top-level declaration must win over a discovered duplicate, got %q", got)
	}
	if got := forwardMap["fixture_tools__ping"]; got != "fixture_tools__ping" {
		t.Fatalf("forward map for discovered tool = %q", got)
	}
	identity := reverseMap["fixture_tools__ping"]
	if identity.Name != "ping" || identity.Namespace != "fixture_tools" || identity.ToolSearch {
		t.Fatalf("discovered identity = %+v, want ping in fixture_tools", identity)
	}
}

func TestFindResponsesClientToolSearch(t *testing.T) {
	if _, ok := FindResponsesClientToolSearch(gjson.Parse(`{"tools": [{"type": "function", "name": "a"}]}`)); ok {
		t.Fatalf("no tool_search declared, but one was found")
	}
	if _, ok := FindResponsesClientToolSearch(gjson.Parse(`{"input": [{"type": "tool_search_output", "tools": [{"type": "tool_search", "execution": "client"}]}]}`)); ok {
		t.Fatalf("tool_search_output tools must not count as a declaration")
	}
	tool, ok := FindResponsesClientToolSearch(gjson.Parse(`{"tools": [{"type": "tool_search", "execution": "client", "description": "d"}]}`))
	if !ok || tool.Get("description").String() != "d" {
		t.Fatalf("expected the client tool_search declaration, got ok=%v tool=%s", ok, tool.Raw)
	}
}
