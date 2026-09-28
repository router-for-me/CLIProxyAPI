package responsestools

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeTestContract(t *testing.T, body string) *ToolContract {
	t.Helper()
	contract := ParseContract([]byte(body))
	if contract == nil {
		t.Fatalf("expected contract, got nil for %s", body)
	}
	return contract
}

func TestParseContractSearchOnly(t *testing.T) {
	contract := decodeTestContract(t, `{"tools": [{"type": "tool_search", "description": "search"}], "input": [{"type": "message", "role": "user", "content": "hi"}]}`)
	if !contract.ClientSearch {
		t.Fatalf("expected client search")
	}
	if contract.ServerSearch {
		t.Fatalf("unexpected server search")
	}
	if contract.SearchAlias != ToolSearchName {
		t.Fatalf("unexpected search alias %q", contract.SearchAlias)
	}
}

func TestParseContractChoosesUnusedSearchAliasWhenFallbackNameExists(t *testing.T) {
	contract := decodeTestContract(t, `{
		"tools": [
			{"type":"function","name":"tool_search"},
			{"type":"function","name":"cts_tool_search"},
			{"type":"tool_search"}
		],
		"input":[]
	}`)
	if contract.SearchAlias != "cts_tool_search_1" {
		t.Fatalf("search alias = %q, want cts_tool_search_1", contract.SearchAlias)
	}
	for identity, alias := range contract.AliasByID {
		if alias == contract.SearchAlias {
			t.Fatalf("search alias collides with declared identity %v", identity)
		}
	}
}

func TestParseContractServerSearchKeptSeparate(t *testing.T) {
	contract := decodeTestContract(t, `{"tools": [{"type": "tool_search", "execution": "server"}], "input": []}`)
	if !contract.ServerSearch {
		t.Fatalf("expected server search")
	}
	if contract.ClientSearch {
		t.Fatalf("server execution must not count as client search")
	}
}

func TestParseContractMixedEagerDeferred(t *testing.T) {
	contract := decodeTestContract(t, `{
		"tools": [
			{"type": "function", "name": "eager_tool"},
			{"type": "tool_search"},
			{"type": "namespace", "name": "win", "tools": [
				{"type": "function", "name": "deferred_one", "defer_loading": true},
				{"type": "custom", "name": "patcher", "defer_loading": true}
			]}
		],
		"input": []
	}`)
	if !contract.ClientSearch {
		t.Fatalf("expected client search")
	}
	deferred := ToolIdentity{Namespace: "win", Name: "deferred_one", Kind: ToolKindFunction}
	if !contract.Deferred[deferred] {
		t.Fatalf("expected deferred flag for %v", deferred)
	}
	custom := ToolIdentity{Namespace: "win", Name: "patcher", Kind: ToolKindCustom}
	if !contract.Deferred[custom] {
		t.Fatalf("custom kind must survive as custom, got identities %v", contract.Identities)
	}
	if _, exists := contract.TopLevel["eager_tool"]; !exists {
		t.Fatalf("expected eager tool in top level")
	}
}

func TestParseContractNestedNamespace(t *testing.T) {
	contract := decodeTestContract(t, `{
		"tools": [
			{"type": "tool_search"},
			{"type": "namespace", "name": "outer", "tools": [
				{"type": "namespace", "name": "inner", "tools": [
					{"type": "function", "name": "deep", "defer_loading": true}
				]}
			]}
		],
		"input": []
	}`)
	identity := ToolIdentity{Namespace: "outer__inner", Name: "deep", Kind: ToolKindFunction}
	found := false
	for _, candidate := range contract.Identities {
		if candidate == identity {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected nested identity %v in %v", identity, contract.Identities)
	}
}

func TestParseContractAdditionalToolsAndDiscovery(t *testing.T) {
	contract := decodeTestContract(t, `{
		"tools": [{"type": "tool_search"}],
		"input": [
			{"type": "additional_tools", "tools": [{"type": "function", "name": "extra", "defer_loading": true}]},
			{"type": "tool_search_call", "call_id": "call_1", "arguments": {"query": "x"}},
			{"type": "tool_search_output", "call_id": "call_1", "tools": [{"type": "function", "name": "found", "parameters": {"type": "object"}}]}
		]
	}`)
	if contract.LatestRound == 0 {
		t.Fatalf("expected discovery round")
	}
	identity := ToolIdentity{Namespace: "", Name: "found", Kind: ToolKindFunction}
	if _, exists := contract.Discovered[identity]; !exists {
		t.Fatalf("expected discovered identity")
	}
	raw, exists := contract.Declarations[identity]
	if !exists || len(raw) == 0 {
		t.Fatalf("expected discovered declaration bytes")
	}
}

func TestParseContractSkipsUnrelatedBodies(t *testing.T) {
	for _, body := range []string{
		`{"model": "x", "input": "hello"}`,
		`not json`,
		``,
		`[]`,
	} {
		if contract := ParseContract([]byte(body)); contract != nil {
			t.Fatalf("expected nil contract for %q", body)
		}
	}
}

func TestParseContractPreservesIntegerPrecision(t *testing.T) {
	contract := decodeTestContract(t, `{"tools": [{"type": "tool_search"}], "input": [{"type": "tool_search_output", "call_id": "c", "tools": [{"type": "function", "name": "big", "x": 9007199254740993}]}]}`)
	identity := ToolIdentity{Namespace: "", Name: "big", Kind: ToolKindFunction}
	raw := contract.Declarations[identity]
	var decoded map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if number, ok := decoded["x"].(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("integer precision lost: %v", decoded["x"])
	}
}

func TestResolveAmbiguityNeverGuesses(t *testing.T) {
	contract := decodeTestContract(t, `{
		"tools": [{"type": "tool_search"}],
		"input": [{"type": "tool_search_output", "call_id": "c", "tools": [
			{"type": "namespace", "name": "a", "tools": [{"type": "function", "name": "dup"}]},
			{"type": "namespace", "name": "b", "tools": [{"type": "function", "name": "dup"}]}
		]}]
	}`)
	if _, ok := contract.Resolve("dup"); ok {
		t.Fatalf("ambiguous local name must not resolve")
	}
}

func TestResolveSearchAliasAndEagerNeverRestore(t *testing.T) {
	contract := decodeTestContract(t, `{"tools": [{"type": "function", "name": "eager"}, {"type": "tool_search"}], "input": []}`)
	if _, ok := contract.Resolve(ToolSearchName); ok {
		t.Fatalf("search alias must not resolve")
	}
	if _, ok := contract.Resolve("eager"); ok {
		t.Fatalf("eager tools must not restore")
	}
}

func TestUnknownFieldsSurviveRewrite(t *testing.T) {
	policy := RoutePolicy{ClientSearch: ClientSearchBridge}
	body := `{"tools": [{"type": "tool_search", "weird": [1, 2, {"x": "y"}]}], "input": [], "custom_field": {"nested": true}}`
	contract := ParseContract([]byte(body))
	value, _ := decodeValue([]byte(body))
	if _, err := RewriteRequest(value, policy, contract, DefaultLimits()); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	root := value.(map[string]any)
	tools := root["tools"].([]any)
	found := false
	for _, rawTool := range tools {
		if tool, ok := rawTool.(map[string]any); ok && tool["name"] == ToolSearchName {
			if weird, okWeird := tool["weird"].([]any); !okWeird || len(weird) != 3 {
				t.Fatalf("unknown tool field lost: %v", tool)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("search function missing")
	}
	if custom, ok := root["custom_field"].(map[string]any); !ok || custom["nested"] != true {
		t.Fatalf("unknown root field lost")
	}
}
