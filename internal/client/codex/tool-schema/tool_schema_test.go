package toolschema

import (
	"net/http"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestNormalizeCodexToolIntegerTypes(t *testing.T) {
	input := []byte(`{
		"tools": [
			{
				"type": "function",
				"name": "exec_command",
				"parameters": {
					"type": "object",
					"properties": {
						"cmd": {"type": "string"},
						"yield_time_ms": {"type": "number"},
						"max_output_tokens": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "write_stdin",
				"parameters": {
					"type": "object",
					"properties": {
						"session_id": {"type": "number"},
						"yield_time_ms": {"type": "number"},
						"max_output_tokens": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "sleep",
				"parameters": {
					"type": "object",
					"properties": {
						"duration_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "wait_agent",
				"parameters": {
					"type": "object",
					"properties": {
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "wait",
				"parameters": {
					"type": "object",
					"properties": {
						"yield_time_ms": {"type": "number"},
						"max_tokens": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "tool_search",
				"parameters": {
					"type": "object",
					"properties": {
						"limit": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "test_sync_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"sleep_before_ms": {"type": "number"},
						"sleep_after_ms": {"type": "number"},
						"participants": {"type": "number"},
						"timeout_ms": {"type": "number"}
					}
				}
			},
			{
				"type": "function",
				"name": "unrelated_tool",
				"parameters": {
					"type": "object",
					"properties": {
						"timeout_ms": {"type": "number"}
					}
				}
			}
		],
		"input": [
			{
				"type": "additional_tools",
				"tools": [
					{
						"type": "function",
						"name": "functions__exec_command",
						"parameters": {
							"type": "object",
							"properties": {
								"yield_time_ms": {"type": ["number", "null"]}
							}
						}
					}
				]
			}
		]
	}`)

	t.Run("non-codex user agent preserves numbers", func(t *testing.T) {
		headers := http.Header{"User-Agent": []string{"curl/8.7.1"}}
		out := NormalizeCodexToolIntegerTypes(input, headers)
		if got := ojson.GetBytesNoCopy(out, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("expected number for non-codex, got: %s", got)
		}
	})

	t.Run("nil or empty headers leaves payload untouched", func(t *testing.T) {
		outNil := NormalizeCodexToolIntegerTypes(input, nil)
		if got := ojson.GetBytesNoCopy(outNil, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("expected nil headers to preserve number, got: %s", got)
		}
		outEmpty := NormalizeCodexToolIntegerTypes(input, http.Header{})
		if got := ojson.GetBytesNoCopy(outEmpty, "tools.0.parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Fatalf("expected empty headers to preserve number, got: %s", got)
		}
	})

	t.Run("codex user agent normalizes specified fields", func(t *testing.T) {
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0 (Mac OS 26.5.2; arm64)"}}
		out := NormalizeCodexToolIntegerTypes(input, headers)

		toolMap := make(map[string]ojson.Result)
		for _, tool := range ojson.GetBytesNoCopy(out, "tools").Array() {
			toolMap[tool.Get("name").String()] = tool
		}

		expected := map[string][]string{
			"exec_command":   {"yield_time_ms", "max_output_tokens", "timeout_ms"},
			"write_stdin":    {"session_id", "yield_time_ms", "max_output_tokens"},
			"sleep":          {"duration_ms"},
			"wait_agent":     {"timeout_ms"},
			"wait":           {"yield_time_ms", "max_tokens"},
			"tool_search":    {"limit"},
			"test_sync_tool": {"sleep_before_ms", "sleep_after_ms", "participants", "timeout_ms"},
		}

		for toolName, fields := range expected {
			tool, ok := toolMap[toolName]
			if !ok {
				t.Fatalf("missing tool: %s", toolName)
			}
			for _, field := range fields {
				if got := tool.Get("parameters.properties." + field + ".type").String(); got != "integer" {
					t.Errorf("tool %s field %s type = %q, want integer", toolName, field, got)
				}
			}
		}

		// Check non-target field cmd in exec_command
		if got := toolMap["exec_command"].Get("parameters.properties.cmd.type").String(); got != "string" {
			t.Errorf("exec_command cmd type = %q, want string", got)
		}

		// Check unrelated tool retains number
		if got := toolMap["unrelated_tool"].Get("parameters.properties.timeout_ms.type").String(); got != "number" {
			t.Errorf("unrelated_tool timeout_ms type = %q, want number", got)
		}

		// Check additional_tools under input
		addToolType := ojson.GetBytesNoCopy(out, "input.0.tools.0.parameters.properties.yield_time_ms.type").Array()
		if len(addToolType) != 2 || addToolType[0].String() != "integer" || addToolType[1].String() != "null" {
			t.Errorf("additional_tools yield_time_ms type = %v, want [integer null]", addToolType)
		}
	})

	t.Run("third-party mcp tools are not modified", func(t *testing.T) {
		mcpInput := []byte(`{
			"tools": [
				{
					"type": "function",
					"name": "mcp__server__sleep",
					"parameters": {
						"type": "object",
						"properties": {
							"duration_ms": {"type": "number"}
						}
					}
				},
				{
					"type": "function",
					"name": "mcp__server__exec_command",
					"parameters": {
						"type": "object",
						"properties": {
							"yield_time_ms": {"type": "number"}
						}
					}
				},
				{
					"type": "function",
					"name": "functions__sleep",
					"parameters": {
						"type": "object",
						"properties": {
							"duration_ms": {"type": "number"}
						}
					}
				},
				{
					"type": "function",
					"name": "collab__exec_command",
					"parameters": {
						"type": "object",
						"properties": {
							"yield_time_ms": {"type": "number"}
						}
					}
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
		out := NormalizeCodexToolIntegerTypes(mcpInput, headers)
		toolMap := make(map[string]ojson.Result)
		for _, tool := range ojson.GetBytesNoCopy(out, "tools").Array() {
			toolMap[tool.Get("name").String()] = tool
		}
		// mcp__server__sleep must remain number
		if got := toolMap["mcp__server__sleep"].Get("parameters.properties.duration_ms.type").String(); got != "number" {
			t.Errorf("mcp__server__sleep duration_ms type = %q, want number", got)
		}
		// mcp__server__exec_command must remain number
		if got := toolMap["mcp__server__exec_command"].Get("parameters.properties.yield_time_ms.type").String(); got != "number" {
			t.Errorf("mcp__server__exec_command yield_time_ms type = %q, want number", got)
		}
		// functions__sleep must be normalized to integer
		if got := toolMap["functions__sleep"].Get("parameters.properties.duration_ms.type").String(); got != "integer" {
			t.Errorf("functions__sleep duration_ms type = %q, want integer", got)
		}
		// collab__exec_command must be normalized to integer
		if got := toolMap["collab__exec_command"].Get("parameters.properties.yield_time_ms.type").String(); got != "integer" {
			t.Errorf("collab__exec_command yield_time_ms type = %q, want integer", got)
		}
	})

	t.Run("array type with number and integer deduplicates to single integer", func(t *testing.T) {
		dupInput := []byte(`{
			"tools": [
				{
					"type": "function",
					"name": "sleep",
					"parameters": {
						"type": "object",
						"properties": {
							"duration_ms": {"type": ["number", "integer", "null"]}
						}
					}
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
		out := NormalizeCodexToolIntegerTypes(dupInput, headers)
		arr := ojson.GetBytesNoCopy(out, "tools.0.parameters.properties.duration_ms.type").Array()
		if len(arr) != 2 || arr[0].String() != "integer" || arr[1].String() != "null" {
			t.Errorf("deduplicated type = %v, want [integer null]", arr)
		}
	})

	t.Run("claude input_schema format supported", func(t *testing.T) {
		claudeInput := []byte(`{
			"tools": [
				{
					"name": "exec_command",
					"input_schema": {
						"type": "object",
						"properties": {
							"yield_time_ms": {"type": "number"},
							"timeout_ms": {"type": "number"}
						}
					}
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}
		out := NormalizeCodexToolIntegerTypes(claudeInput, headers)
		if got := ojson.GetBytesNoCopy(out, "tools.0.input_schema.properties.yield_time_ms.type").String(); got != "integer" {
			t.Errorf("claude tool yield_time_ms type = %q, want integer", got)
		}
		if got := ojson.GetBytesNoCopy(out, "tools.0.input_schema.properties.timeout_ms.type").String(); got != "integer" {
			t.Errorf("claude tool timeout_ms type = %q, want integer", got)
		}
	})

	t.Run("gemini function_declarations format supported", func(t *testing.T) {
		geminiInput := []byte(`{
			"tools": [
				{
					"function_declarations": [
						{
							"name": "sleep",
							"parameters": {
								"type": "object",
								"properties": {
									"duration_ms": {"type": "number"}
								}
							}
						}
					]
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-desktop/0.159.0"}}
		out := NormalizeCodexToolIntegerTypes(geminiInput, headers)
		if got := ojson.GetBytesNoCopy(out, "tools.0.function_declarations.0.parameters.properties.duration_ms.type").String(); got != "integer" {
			t.Errorf("gemini tool duration_ms type = %q, want integer", got)
		}
	})

	t.Run("gemini parametersJsonSchema format supported directly", func(t *testing.T) {
		geminiInput := []byte(`{
			"tools": [
				{
					"functionDeclarations": [
						{
							"name": "exec_command",
							"parametersJsonSchema": {
								"type": "object",
								"properties": {
									"yield_time_ms": {"type": "number"}
								}
							}
						}
					]
				}
			]
		}`)
		headers := http.Header{"User-Agent": []string{"codex-desktop/0.159.0"}}
		out := NormalizeCodexToolIntegerTypes(geminiInput, headers)
		if got := ojson.GetBytesNoCopy(out, "tools.0.functionDeclarations.0.parametersJsonSchema.properties.yield_time_ms.type").String(); got != "integer" {
			t.Errorf("gemini parametersJsonSchema yield_time_ms type = %q, want integer", got)
		}
	})
}

func TestNormalizeCodexToolSchemasDocCrossMethod(t *testing.T) {
	raw := []byte(`{
		"model": "gpt-5.4(high)",
		"tools": [{
			"type": "function",
			"name": "choose_action",
			"parameters": {
				"type": "object",
				"properties": {
					"action": {
						"type": "string",
						"oneOf": [
							{"const": "a", "description": "A"},
							{"const": "b", "description": "B"},
							{"const": "c", "description": "C"},
							{"const": "d", "description": "D"},
							{"const": "e", "description": "E"},
							{"const": "f", "description": "F"},
							{"const": "g", "description": "G"},
							{"const": "h", "description": "H"}
						]
					},
					"field": {
						"type": "string",
						"pattern": "^[^\\p{Cc}]+$"
					}
				}
			}
		}]
	}`)

	doc := ojson.NewDoc(raw)
	if errSet := doc.Set("model", "gpt-5.4"); errSet != nil {
		t.Fatalf("doc.Set(model) error = %v", errSet)
	}
	if !NormalizeCodexToolSchemasDoc(doc) {
		t.Fatal("expected NormalizeCodexToolSchemasDoc to report changes")
	}
	got := doc.Bytes()

	if gotModel := ojson.GetBytesNoCopy(got, "model").String(); gotModel != "gpt-5.4" {
		t.Fatalf("model = %q, want gpt-5.4", gotModel)
	}
	actionProp := ojson.GetBytesNoCopy(got, "tools.0.parameters.properties.action")
	if actionProp.Get("oneOf").Exists() {
		t.Fatalf("expected action.oneOf to be removed: %s", actionProp.Raw)
	}
	if enums := actionProp.Get("enum").Array(); len(enums) != 8 {
		t.Fatalf("action.enum len = %d, want 8", len(enums))
	}
	fieldProp := ojson.GetBytesNoCopy(got, "tools.0.parameters.properties.field")
	if fieldProp.Get("pattern").Exists() {
		t.Fatalf("expected field.pattern to be removed: %s", fieldProp.Raw)
	}
}
