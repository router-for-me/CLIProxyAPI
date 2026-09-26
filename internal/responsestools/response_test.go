package responsestools

import (
	"testing"
)

func TestRestoreSearchResponseItem(t *testing.T) {
	contract := ParseContract([]byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}"))
	item := map[string]any{"type": "function_call", "name": "tool_search", "call_id": "c1", "arguments": "{\"query\":\"x\"}"}
	if !rewriteResponseItem(item, contract, nil) {
		t.Fatalf("expected restore")
	}
	if err := ValidateToolSearchCall(item); err != nil {
		t.Fatalf("invalid restored shape: %v", err)
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
