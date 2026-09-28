package responsestools

import (
	"strings"
	"testing"
)

func TestCustomFunctionRoundTrip(t *testing.T) {
	body := `{
		"tools": [{"type": "custom", "name": "apply_patch", "description": "Apply a patch."}],
		"input": []
	}`
	value, _ := decodeValue([]byte(body))
	bridge, changed, err := RewriteCustomDeclarations(value, CustomGrammarReject)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if !changed {
		t.Fatalf("expected change")
	}
	root := value.(map[string]any)
	tools := root["tools"].([]any)
	wrapped := tools[0].(map[string]any)
	if wrapped["type"] != "function" {
		t.Fatalf("not wrapped: %v", wrapped)
	}
	alias := wrapped["name"].(string)
	if !strings.HasPrefix(alias, CustomAliasPrefix) {
		t.Fatalf("unexpected alias %q", alias)
	}
	// History round trip with exact whitespace, unicode, CRLF, and JSON-looking text.
	original := "line1\nline2\r\n  unicode: \u4e2d\u6587 \\ backslash {\"json\": true}"
	history := map[string]any{
		"type": "custom_tool_call", "name": "apply_patch", "call_id": "c1", "input": original,
	}
	root["input"] = []any{history}
	value2, _ := decodeValue(mustMarshal(root))
	_, _, err = RewriteCustomDeclarations(value2, CustomGrammarReject)
	if err != nil {
		t.Fatalf("history wrap: %v", err)
	}
	_ = bridge
}

func TestCustomStrictUnpackRejectsMalformed(t *testing.T) {
	for _, bad := range []string{
		``,
		`{"input": "a"} {"input": "b"}`,
		`{"input": "a", "extra": 1}`,
		`{"input": 42}`,
		`{"other": "x"}`,
		`{}`,
		`not json`,
	} {
		if _, err := UnpackCustomArguments(bad); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
	if input, err := UnpackCustomArguments(`{"input":"ok"}`); err != nil || input != "ok" {
		t.Fatalf("valid unpack failed: %v %q", err, input)
	}
}

func TestCustomHistoryOnlyFunctionBridge(t *testing.T) {
	value, _ := decodeValue([]byte(`{"tools": [], "input": [
		{"type": "custom_tool_call", "call_id": "c1", "name": "patcher", "input": "do it"},
		{"type": "custom_tool_call_output", "call_id": "c1", "output": "done"}
	]}`))
	changed, err := ConvertCustomHistory(value)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if !changed {
		t.Fatalf("expected change")
	}
	root := value.(map[string]any)
	input := root["input"].([]any)
	if input[0].(map[string]any)["type"] != "function_call" {
		t.Fatalf("call not converted: %v", input[0])
	}
	if input[1].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("output not converted: %v", input[1])
	}
}

func TestCustomHistoryRejectsNonString(t *testing.T) {
	value, _ := decodeValue([]byte(`{"input": [{"type": "custom_tool_call", "call_id": "c", "name": "x", "input": 42}]}`))
	if _, err := ConvertCustomHistory(value); err == nil {
		t.Fatalf("expected rejection for non-string input")
	}
}

func TestStripForcedCallRejected(t *testing.T) {
	value, _ := decodeValue([]byte(`{
		"tools": [{"type": "custom", "name": "patcher"}],
		"tool_choice": {"type": "function", "name": "patcher"},
		"input": []
	}`))
	if _, err := StripCustomDeclarations(value); err == nil {
		t.Fatalf("expected forced-call rejection")
	}
}

func TestCustomAliasDeterministic(t *testing.T) {
	first := CustomAliasFor("ns", "tool")
	second := CustomAliasFor("ns", "tool")
	if first != second {
		t.Fatalf("alias not deterministic")
	}
	if CustomAliasFor("ns", "tool") == CustomAliasFor("", "tool") {
		t.Fatalf("namespace must affect alias")
	}
}

func TestNestedCustomFlattensWithNamespace(t *testing.T) {
	value, _ := decodeValue([]byte(`{
		"tools": [{"type": "namespace", "name": "mcp", "tools": [{"type": "custom", "name": "patch", "description": "d"}]}],
		"input": []
	}`))
	bridge, changed, err := RewriteCustomDeclarations(value, CustomGrammarReject)
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	if !changed {
		t.Fatalf("expected change")
	}
	identity, ok := bridge.Resolve(bridge.aliasByID[ToolIdentity{Namespace: "mcp", Name: "patch", Kind: ToolKindCustom}])
	if !ok || identity.Name != "patch" || identity.Namespace != "mcp" {
		t.Fatalf("reverse namespace lost: %v %v", identity, ok)
	}
}

func TestCustomOutputArrayPreserved(t *testing.T) {
	bridge := BuildCustomBridge(map[string]any{
		"tools": []any{map[string]any{"type": "custom", "name": "patch"}},
	}, CustomGrammarReject)
	alias, _ := bridge.Alias(ToolIdentity{Namespace: "", Name: "patch", Kind: ToolKindCustom})
	item := map[string]any{"type": "function_call", "name": alias, "arguments": `{"input":"x"}`, "call_id": "c"}
	if !bridge.RestoreCustomResponseItem(item) {
		t.Fatalf("restore failed")
	}
	if item["type"] != "custom_tool_call" || item["input"] != "x" || item["name"] != "patch" {
		t.Fatalf("wrong restore: %v", item)
	}
}

func mustMarshal(value any) []byte {
	out, err := jsonMarshal(value)
	if err != nil {
		panic(err)
	}
	return out
}
