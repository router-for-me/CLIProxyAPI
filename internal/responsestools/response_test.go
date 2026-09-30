package responsestools

import (
	"errors"
	"testing"
)

func TestRestoreSearchResponseItem(t *testing.T) {
	contract := ParseContract([]byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}"))
	contract.SearchBridged = true
	item := map[string]any{
		"type": "function_call", "id": poisonedSearchCallID,
		"name": "tool_search", "call_id": "c1", "arguments": "{\"query\":\"x\"}",
	}
	if !rewriteResponseItem(item, contract, nil) {
		t.Fatalf("expected restore")
	}
	if err := ValidateToolSearchCall(item); err != nil {
		t.Fatalf("invalid restored shape: %v", err)
	}
	if item["id"] != repairedSearchCallID {
		t.Fatalf("id = %v, want %q", item["id"], repairedSearchCallID)
	}
}

func TestRestoreNamespacedIdentity(t *testing.T) {
	contract := ParseContract([]byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": [{\"type\": \"tool_search_output\", \"call_id\": \"c\", \"tools\": [{\"type\": \"namespace\", \"name\": \"mcp\", \"tools\": [{\"type\": \"function\", \"name\": \"read\"}]}]}]}"))
	if contract == nil {
		t.Fatalf("no contract")
	}
	alias := contract.AliasByID[ToolIdentity{Namespace: "mcp", Name: "read", Kind: ToolKindFunction}]
	if alias == "" {
		t.Fatalf("no alias, ids: %v", contract.AliasByID)
	}
	item := map[string]any{"type": "function_call", "name": alias, "call_id": "c2", "arguments": "{}"}
	if !RestoreFunctionCallIdentity(item, contract) {
		t.Fatalf("expected restore")
	}
	if item["name"] != "read" || item["namespace"] != "mcp" {
		t.Fatalf("wrong identity: %v", item)
	}
}

func TestBusinessJSONNeverScanned(t *testing.T) {
	value, _ := decodeValue([]byte("{\"output\": [{\"type\": \"message\", \"content\": \"call the tool_search function named tool_search\"}]}"))
	if RewriteResponseBody(value, nil, nil) {
		t.Fatalf("business text must never rewrite")
	}
}

// Every bridged item leaves in the namespace its client-visible type requires,
// and the client stores that id verbatim for the next turn.
func TestRewriteResponseMigratesIDsAndRejectsCollisions(t *testing.T) {
	contract := ParseContract([]byte(`{"tools": [
		{"type": "tool_search"},
		{"type": "custom", "name": "patcher", "description": "d"}
	], "input": []}`))
	if contract == nil {
		t.Fatalf("no contract")
	}
	contract.SearchBridged = true
	bridge := BuildCustomBridge(map[string]any{
		"tools": []any{map[string]any{"type": "custom", "name": "patcher"}},
	}, CustomGrammarReject)
	alias, _ := bridge.Alias(ToolIdentity{Namespace: "", Name: "patcher", Kind: ToolKindCustom})

	value, _ := decodeValue([]byte(`{"output": [
		{"type": "function_call", "id": "` + poisonedSearchCallID + `", "name": "tool_search", "call_id": "s1", "arguments": "{\"query\":\"x\"}"},
		{"type": "function_call", "id": "fc_patch_1", "name": "` + alias + `", "call_id": "p1", "arguments": "{\"input\":\"patch\"}"},
		{"type": "message", "id": "msg_1", "role": "assistant", "content": []},
		{"type": "function_call", "id": "item_unknown", "name": "exec_command", "call_id": "e1", "arguments": "{}"}
	]}`))
	changed, err := RewriteResponseBodyChecked(value, contract, bridge)
	if err != nil || !changed {
		t.Fatalf("rewrite: changed=%v err=%v", changed, err)
	}
	items, _ := value.(map[string]any)["output"].([]any)
	search, _ := items[0].(map[string]any)
	if search["type"] != "tool_search_call" || search["id"] != repairedSearchCallID {
		t.Fatalf("search item = %v", search)
	}
	custom, _ := items[1].(map[string]any)
	if custom["type"] != "custom_tool_call" || custom["id"] != "ctc_patch_1" {
		t.Fatalf("custom item = %v", custom)
	}
	// An item the rewrite does not own keeps its identity, but it still
	// occupies its id in the response.
	message, _ := items[2].(map[string]any)
	if message["id"] != "msg_1" {
		t.Fatalf("message item changed: %v", message)
	}
	ordinary, _ := items[3].(map[string]any)
	if ordinary["id"] != "item_unknown" || ordinary["type"] != "function_call" {
		t.Fatalf("ordinary item changed: %v", ordinary)
	}

	// Two bridged items that would land on one client id are refused instead
	// of being handed out twice.
	colliding, _ := decodeValue([]byte(`{"output": [
		{"type": "function_call", "id": "fc_shared", "name": "tool_search", "call_id": "s1", "arguments": "{}"},
		{"type": "function_call", "id": "tsc_shared", "name": "` + alias + `", "call_id": "p1", "arguments": "{\"input\":\"x\"}"}
	]}`))
	_, err = RewriteResponseBodyChecked(colliding, contract, bridge)
	var compat *ToolCompatibilityError
	if !errors.As(err, &compat) || compat.StatusCode() != 502 || compat.Reason != ReasonUpstreamContract {
		t.Fatalf("want 502 upstream_contract, got %v", err)
	}
}
