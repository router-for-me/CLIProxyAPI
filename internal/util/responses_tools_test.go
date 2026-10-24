package util

import (
	"reflect"
	"strings"
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

func TestBuildGeminiApplyPatchDeclaration(t *testing.T) {
	root := gjson.Parse(`{"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`)
	declarations, _, reverse := BuildGeminiFunctionDeclarations(root)
	if len(declarations) != 1 {
		t.Fatalf("declarations: %d", len(declarations))
	}
	declaration := gjson.ParseBytes(declarations[0])
	if !strings.Contains(declaration.Get("description").String(), "*** Begin Patch") || !strings.Contains(declaration.Get("description").String(), "start: patch") {
		t.Fatal("patch format is missing")
	}
	if declaration.Get("parametersJsonSchema.properties.input.type").String() != "string" || !declaration.Get("parametersJsonSchema.additionalProperties").Exists() || declaration.Get("parametersJsonSchema.additionalProperties").Bool() {
		t.Fatal("input schema is missing")
	}
	if !reverse[declaration.Get("name").String()].Custom {
		t.Fatal("custom client identity was lost")
	}
}

func TestBuildGeminiApplyPatchIdentityUsesWinningOriginalDeclaration(t *testing.T) {
	root := gjson.Parse(`{"tools":[{"type":"namespace","name":"a.b","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"a_b__apply_patch","parameters":{"type":"object","properties":{"n":{"type":"number"}}}},{"type":"function","name":"apply_patch"}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	declarations, forward, reverse := BuildGeminiFunctionDeclarations(root)
	if len(declarations) != 3 {
		t.Fatalf("declarations=%d", len(declarations))
	}
	if forward["a.b__apply_patch"] == forward["a_b__apply_patch"] {
		t.Fatal("sanitized collision was not separated")
	}
	for _, raw := range declarations {
		decl := gjson.ParseBytes(raw)
		identity := reverse[decl.Get("name").String()]
		patch := identity.Custom && identity.Namespace == "a.b"
		if identity.ApplyPatch != patch || strings.Contains(decl.Get("description").String(), "*** Begin Patch") != patch {
			t.Fatalf("wrong winning identity=%+v declaration=%s", identity, raw)
		}
	}
	if reverse[forward["apply_patch"]].Custom || reverse[forward["apply_patch"]].ApplyPatch {
		t.Fatal("ordinary function became custom patch")
	}
}

func TestBuildGeminiFunctionDeclarations_ClientToolSearchShimAndDeferredFiltering(t *testing.T) {
	raw := `{
		"tools": [
			{"type": "function", "name": "exec_command", "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}},
			{"type": "tool_search", "execution": "client", "description": "Search deferred tools.", "parameters": {"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}}, "required": ["query"], "additionalProperties": false}},
			{"type": "function", "name": "deferred_fn", "defer_loading": true, "parameters": {"type": "object", "properties": {}}},
			{"type": "web_search"}
		]
	}`
	declarations, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))

	if len(declarations) != 2 {
		t.Fatalf("expected exec_command and tool_search declarations (with deferred_fn hidden), got %d: %s", len(declarations), declarations)
	}
	shim := gjson.ParseBytes(declarations[1])
	if got := shim.Get("name").String(); got != "tool_search" {
		t.Fatalf("shim name = %q, want tool_search", got)
	}
	if got := shim.Get("description").String(); got != "Search deferred tools." {
		t.Fatalf("shim description = %q, want original description", got)
	}
	if got := shim.Get("parametersJsonSchema.required.0").String(); got != "query" {
		t.Fatalf("shim parameters not preserved: %s", shim.Raw)
	}
	if got := forwardMap["tool_search"]; got != "tool_search" {
		t.Fatalf("forward map for shim = %q, want tool_search", got)
	}
	if _, ok := reverseMap["tool_search"]; !ok {
		t.Fatalf("expected tool_search in reverseMap")
	}
	if !reverseMap["tool_search"].ToolSearch || reverseMap["tool_search"].Custom || reverseMap["tool_search"].Namespace != "" {
		t.Fatalf("unexpected shim identity: %+v", reverseMap["tool_search"])
	}
	if reverseMap["exec_command"].ToolSearch {
		t.Fatalf("regular function must not be marked ToolSearch: %+v", reverseMap["exec_command"])
	}
}

func TestBuildGeminiFunctionDeclarations_ToolSearchShimDefaultsAndPrecedence(t *testing.T) {
	t.Run("default parameters when omitted and custom schema preserved when provided", func(t *testing.T) {
		defaultDecls, _, _ := BuildGeminiFunctionDeclarations(gjson.Parse(`{"tools": [{"type": "tool_search", "execution": "client"}]}`))
		if len(defaultDecls) != 1 {
			t.Fatalf("expected one shim declaration, got %d", len(defaultDecls))
		}
		if got := gjson.GetBytes(defaultDecls[0], "parametersJsonSchema.properties.query.type").String(); got != "string" {
			t.Fatalf("default parameters missing query: %s", defaultDecls[0])
		}
		if !gjson.GetBytes(defaultDecls[0], `parametersJsonSchema.required.#(=="query")`).Exists() {
			t.Fatalf("default parameters missing required query: %s", defaultDecls[0])
		}

		customGoalRaw := `{"tools": [{"type": "tool_search", "execution": "client", "description": "Custom goal search", "parameters": {"type": "object", "properties": {"goal": {"type": "string"}}, "required": ["goal"], "additionalProperties": false}}]}`
		customDecls, _, _ := BuildGeminiFunctionDeclarations(gjson.Parse(customGoalRaw))
		if len(customDecls) != 1 {
			t.Fatalf("expected one shim declaration, got %d", len(customDecls))
		}
		if got := gjson.GetBytes(customDecls[0], "parametersJsonSchema.properties.goal.type").String(); got != "string" {
			t.Fatalf("custom goal property not preserved: %s", customDecls[0])
		}
		if gjson.GetBytes(customDecls[0], "parametersJsonSchema.properties.query").Exists() {
			t.Fatalf("custom schema must not be mutated to inject query: %s", customDecls[0])
		}
		if gjson.GetBytes(customDecls[0], `parametersJsonSchema.required.#(=="query")`).Exists() {
			t.Fatalf("custom required array must not inject query: %s", customDecls[0])
		}
	})
	t.Run("server executed search is not emulated", func(t *testing.T) {
		raw := `{"tools": [{"type": "tool_search"}, {"type": "tool_search", "execution": "server"}]}`
		declarations, _, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))
		if len(declarations) != 0 || len(reverseMap) != 0 {
			t.Fatalf("server-executed tool_search must be dropped, got %s", declarations)
		}
	})
	t.Run("user function and client tool_search coexist in both orders", func(t *testing.T) {
		for _, userFnName := range []string{"tool_search", "__cpa_client_tool_search__"} {
			for _, tc := range []struct {
				name string
				raw  string
			}{
				{
					name: userFnName + "/user function first",
					raw:  `{"tools": [{"type": "function", "name": "` + userFnName + `", "description": "user tool"}, {"type": "tool_search", "execution": "client", "description": "client search"}, {"type": "function", "name": "deferred_fn", "defer_loading": true}]}`,
				},
				{
					name: userFnName + "/client tool_search first",
					raw:  `{"tools": [{"type": "tool_search", "execution": "client", "description": "client search"}, {"type": "function", "name": "` + userFnName + `", "description": "user tool"}, {"type": "function", "name": "deferred_fn", "defer_loading": true}]}`,
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					declarations, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(tc.raw))
					if len(declarations) != 2 {
						t.Fatalf("expected both user %s and client tool_search shim (with deferred_fn hidden), got %d declarations: %s", userFnName, len(declarations), declarations)
					}
					userGeminiName := forwardMap[userFnName]
					shimGeminiName := MapResponsesClientToolSearchName(forwardMap)
					if userGeminiName != userFnName {
						t.Fatalf("user function Gemini name = %q, want %s", userGeminiName, userFnName)
					}
					if shimGeminiName == "" || shimGeminiName == userGeminiName {
						t.Fatalf("shim Gemini name = %q, must be non-empty and distinct from %q", shimGeminiName, userGeminiName)
					}
					if reverseMap[userGeminiName].ToolSearch || reverseMap[userGeminiName].Name != userFnName {
						t.Fatalf("user function identity = %+v, want non-ToolSearch function named %s", reverseMap[userGeminiName], userFnName)
					}
					if !reverseMap[shimGeminiName].ToolSearch {
						t.Fatalf("shim identity = %+v, want ToolSearch=true", reverseMap[shimGeminiName])
					}
				})
			}
		}
	})
	t.Run("sanitized name collision with tool.search keeps both", func(t *testing.T) {
		raw := `{"tools": [{"type": "function", "name": "tool.search", "description": "dotted user tool"}, {"type": "tool_search", "execution": "client", "description": "client search"}]}`
		declarations, forwardMap, reverseMap := BuildGeminiFunctionDeclarations(gjson.Parse(raw))
		if len(declarations) != 2 {
			t.Fatalf("expected 2 declarations, got %d: %s", len(declarations), declarations)
		}
		dottedGeminiName := forwardMap["tool.search"]
		shimGeminiName := MapResponsesClientToolSearchName(forwardMap)
		if shimGeminiName != "tool_search" {
			t.Fatalf("shim Gemini name = %q, want tool_search", shimGeminiName)
		}
		if dottedGeminiName == "" || dottedGeminiName == shimGeminiName {
			t.Fatalf("dotted user tool Gemini name = %q, must be distinct from %q", dottedGeminiName, shimGeminiName)
		}
		if reverseMap[dottedGeminiName].ToolSearch || reverseMap[dottedGeminiName].Name != "tool.search" {
			t.Fatalf("dotted tool identity = %+v", reverseMap[dottedGeminiName])
		}
		if !reverseMap[shimGeminiName].ToolSearch {
			t.Fatalf("shim identity = %+v", reverseMap[shimGeminiName])
		}
	})
	t.Run("shared CollectResponsesToolDescriptors keeps deferred tools for non-search callers", func(t *testing.T) {
		raw := `{"tools": [{"type": "tool_search", "execution": "client"}, {"type": "function", "name": "deferred_fn", "defer_loading": true}, {"type": "function", "name": "eager_fn"}]}`
		descriptors := CollectResponsesToolDescriptors(gjson.Parse(raw))
		if len(descriptors) != 2 || descriptors[0].Name != "deferred_fn" || descriptors[1].Name != "eager_fn" {
			t.Fatalf("CollectResponsesToolDescriptors = %+v, want [deferred_fn eager_fn]", descriptors)
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
			{"type": "namespace", "name": "mcp__cua_repl", "tools": [{"type": "function", "name": "js", "description": "current"}]},
			{"type": "tool_search", "execution": "client"},
			{"type": "function", "name": "deferred_fn", "defer_loading": true, "description": "top-level deferred"}
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
	if len(names) != 3 || names[0] != "mcp__cua_repl__js" || names[1] != "tool_search" || names[2] != "fixture_tools__ping" {
		t.Fatalf("declarations = %v, want [mcp__cua_repl__js tool_search fixture_tools__ping]", names)
	}
	if got := gjson.GetBytes(declarations[0], "description").String(); got != "current" {
		t.Fatalf("top-level declaration must win over a discovered duplicate, got %q", got)
	}
	if got := forwardMap["fixture_tools__ping"]; got != "fixture_tools__ping" {
		t.Fatalf("forward map for discovered tool = %q", got)
	}
	identity := reverseMap["fixture_tools__ping"]
	if identity.Name != "ping" || identity.Namespace != "fixture_tools" {
		t.Fatalf("discovered identity = %+v, want ping in fixture_tools", identity)
	}
}

func TestResponsesToolReverseIdentityMap_MatchesFullDeclarations(t *testing.T) {
	minimalRaw := []byte(`{
		"tools": [
			{"type": "function", "name": "read.file", "parameters": {"type": "object"}},
			{"type": "custom", "name": "apply_patch", "description": "Apply a patch", "format": {"type": "grammar", "syntax": "lark", "definition": "start: /.+/"}},
			{"type": "custom", "name": "notes"},
			{"type": "tool_search", "execution": "client"},
			{"type": "namespace", "name": "mcp", "tools": [{"type": "function", "name": "search", "parameters": {"type": "object"}}]}
		]
	}`)
	heavyRaw := []byte(`{
		"tools": [
			{"type": "function", "name": "read.file", "parameters": {"$defs": {"item": {"type": "object", "properties": {"id": {"type": "string", "pattern": "^[^\\x00]*$", "examples": ["/tmp/a"]}}}}, "type": "object", "properties": {"path": {"type": "string", "pattern": "^[^\\x00]*$", "examples": ["/tmp/a"]}, "items": {"type": "array", "items": {"$ref": "#/$defs/item"}}, "mode": {"anyOf": [{"type": "string", "const": "fast"}, {"type": "null"}]}}, "additionalProperties": false}},
			{"type": "custom", "name": "apply_patch", "description": "Apply a patch", "format": {"type": "grammar", "syntax": "lark", "definition": "start: /.+/"}},
			{"type": "custom", "name": "notes"},
			{"type": "tool_search", "execution": "client", "parameters": {"type": "object", "properties": {"query": {"type": ["string", "null"], "examples": ["mcp"]}, "limit": {"type": "number", "minimum": 1, "maximum": 50}}, "required": ["query"], "additionalProperties": false}},
			{"type": "namespace", "name": "mcp", "tools": [{"type": "function", "name": "search", "parameters": {"$defs": {"filter": {"type": "object", "properties": {"tag": {"type": ["string", "null"]}}}}, "type": "object", "properties": {"q": {"type": ["string", "null"]}, "filter": {"$ref": "#/$defs/filter"}}, "additionalProperties": false}}]}
		]
	}`)
	_, _, full := BuildGeminiFunctionDeclarations(gjson.ParseBytes(heavyRaw))
	fast := ResponsesToolReverseIdentityMap(heavyRaw)
	if len(full) == 0 || !reflect.DeepEqual(full, fast) {
		t.Fatalf("reverse identity map differs from full declarations:\nfull=%#v\nfast=%#v", full, fast)
	}

	minimalAllocs := testing.AllocsPerRun(10, func() {
		_ = ResponsesToolReverseIdentityMap(minimalRaw)
	})
	heavyAllocs := testing.AllocsPerRun(10, func() {
		_ = ResponsesToolReverseIdentityMap(heavyRaw)
	})
	if heavyAllocs > minimalAllocs+10 {
		t.Fatalf("ResponsesToolReverseIdentityMap performed schema cleanup allocations: minimalAllocs=%.1f heavyAllocs=%.1f", minimalAllocs, heavyAllocs)
	}
}
