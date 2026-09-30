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

// Both halves of a custom round trip change type on the wire, so both ids have
// to move with them. Migrating only the call would leave the output naming an
// item the upstream never declared.
func TestCustomFunctionHistoryMigratesCallAndOutputIDs(t *testing.T) {
	body := []byte(`{
		"tools": [{"type": "custom", "name": "patcher", "description": "d"}],
		"input": [
			{"type": "custom_tool_call", "id": "ctc_call_1", "call_id": "c1", "name": "patcher", "input": "do it"},
			{"type": "custom_tool_call_output", "id": "ctco_out_1", "call_id": "c1", "output": "done"}
		]
	}`)
	prepared, err := Prepare(body, RoutePolicy{CustomTools: CustomToolsFunction},
		DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	value, ok := decodeValue(prepared.Body)
	if !ok {
		t.Fatalf("adapted body is not valid JSON")
	}
	input, _ := value.(map[string]any)["input"].([]any)
	call, _ := input[0].(map[string]any)
	if call["type"] != "function_call" || call["id"] != "fc_call_1" || call["call_id"] != "c1" {
		t.Fatalf("custom call = %v", call)
	}
	if _, exists := call["input"]; exists {
		t.Fatalf("the custom input must move into arguments: %v", call)
	}
	output, _ := input[1].(map[string]any)
	if output["type"] != "function_call_output" || output["id"] != "fco_out_1" || output["output"] != "done" {
		t.Fatalf("custom output = %v", output)
	}
}

// A history item that omits its optional id keeps it omitted: the proxy has no
// evidence for an identity the client never sent.
func TestCustomFunctionHistoryKeepsMissingOptionalID(t *testing.T) {
	body := []byte(`{
		"tools": [{"type": "custom", "name": "patcher", "description": "d"}],
		"input": [
			{"type": "custom_tool_call", "call_id": "c1", "name": "patcher", "input": "do it"},
			{"type": "custom_tool_call_output", "call_id": "c1", "output": "done"}
		]
	}`)
	prepared, err := Prepare(body, RoutePolicy{CustomTools: CustomToolsFunction},
		DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	value, _ := decodeValue(prepared.Body)
	input, _ := value.(map[string]any)["input"].([]any)
	for index, rawItem := range input {
		if item, _ := rawItem.(map[string]any); item["id"] != nil {
			t.Fatalf("input[%d] gained an id: %v", index, item)
		}
	}
}

// A history that references a custom tool declared in an earlier turn still
// converts, and its id moves with it.
func TestCustomFunctionHistoryOnlyMigratesIDs(t *testing.T) {
	body := []byte(`{
		"tools": [],
		"input": [
			{"type": "custom_tool_call", "id": "ctc_history_1", "call_id": "c1", "name": "patcher", "input": "do it"},
			{"type": "custom_tool_call_output", "id": "ctco_history_1", "call_id": "c1", "output": "done"}
		]
	}`)
	prepared, err := Prepare(body, RoutePolicy{CustomTools: CustomToolsFunction},
		DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	value, _ := decodeValue(prepared.Body)
	input, _ := value.(map[string]any)["input"].([]any)
	ids := []string{"fc_history_1", "fco_history_1"}
	for index, expected := range ids {
		item, _ := input[index].(map[string]any)
		if item["id"] != expected {
			t.Fatalf("input[%d].id = %v, want %q", index, item["id"], expected)
		}
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
	item := map[string]any{
		"type": "function_call", "id": "fc_fixture_1", "name": alias,
		"arguments": `{"input":"x"}`, "call_id": "c",
	}
	if !bridge.RestoreCustomResponseItem(item) {
		t.Fatalf("restore failed")
	}
	if item["type"] != "custom_tool_call" || item["input"] != "x" || item["name"] != "patch" {
		t.Fatalf("wrong restore: %v", item)
	}
	if item["id"] != "ctc_fixture_1" {
		t.Fatalf("id = %v, want the custom namespace", item["id"])
	}
}

// The checked entry point is the production one: it either converts the whole
// item or leaves it exactly as the upstream sent it.
func TestRestoreCustomResponseItemCheckedMigratesID(t *testing.T) {
	bridge := BuildCustomBridge(map[string]any{
		"tools": []any{map[string]any{"type": "custom", "name": "patch"}},
	}, CustomGrammarReject)
	alias, _ := bridge.Alias(ToolIdentity{Namespace: "", Name: "patch", Kind: ToolKindCustom})

	restored := map[string]any{
		"type": "function_call", "id": "fc_fixture_2", "name": alias,
		"arguments": `{"input":"y"}`, "call_id": "c",
	}
	changed, err := bridge.RestoreCustomResponseItemChecked(restored)
	if err != nil || !changed {
		t.Fatalf("RestoreCustomResponseItemChecked: changed=%v err=%v", changed, err)
	}
	if restored["type"] != "custom_tool_call" || restored["id"] != "ctc_fixture_2" {
		t.Fatalf("restore = %v", restored)
	}

	// A malformed payload and a missing id are both upstream contract
	// violations, and neither may leave a half-converted item behind.
	broken := map[string]any{
		"type": "function_call", "id": "fc_fixture_3", "name": alias,
		"arguments": `{"input":"y","extra":1}`, "call_id": "c",
	}
	before := mustMarshal(broken)
	if _, err := bridge.RestoreCustomResponseItemChecked(broken); err == nil {
		t.Fatalf("expected malformed custom arguments to fail")
	}
	if string(mustMarshal(broken)) != string(before) {
		t.Fatalf("item was partially rewritten: %s", mustMarshal(broken))
	}

	idless := map[string]any{
		"type": "function_call", "name": alias, "arguments": `{"input":"y"}`, "call_id": "c",
	}
	before = mustMarshal(idless)
	if _, err := bridge.RestoreCustomResponseItemChecked(idless); err == nil {
		t.Fatalf("expected a bridged item without id to fail")
	}
	if string(mustMarshal(idless)) != string(before) {
		t.Fatalf("item was partially rewritten: %s", mustMarshal(idless))
	}
}

func mustMarshal(value any) []byte {
	out, err := jsonMarshal(value)
	if err != nil {
		panic(err)
	}
	return out
}
