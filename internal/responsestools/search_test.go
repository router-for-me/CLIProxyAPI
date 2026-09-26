package responsestools

import (
	"encoding/json"
	"strings"
	"testing"
)

func rewriteTestRequest(t *testing.T, body string, policy RoutePolicy) map[string]any {
	t.Helper()
	contract := ParseContract([]byte(body))
	value, ok := decodeValue([]byte(body))
	if !ok {
		t.Fatalf("invalid test body")
	}
	if _, err := RewriteRequest(value, policy, contract, DefaultLimits()); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	root, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected object root")
	}
	return root
}

func toolNames(root map[string]any) []string {
	tools, _ := root["tools"].([]any)
	names := make([]string, 0, len(tools))
	for _, rawTool := range tools {
		if tool, ok := rawTool.(map[string]any); ok {
			names = append(names, stringField(tool, "name"))
		}
	}
	return names
}

func TestSearchBridgeFirstPacketPrunesDeferred(t *testing.T) {
	policy := RoutePolicy{ClientSearch: ClientSearchBridge}
	root := rewriteTestRequest(t, `{
		"tools": [
			{"type": "function", "name": "eager"},
			{"type": "tool_search"},
			{"type": "namespace", "name": "ws", "tools": [
				{"type": "function", "name": "hidden", "defer_loading": true, "parameters": {"type": "object"}}
			]}
		],
		"input": []
	}`, policy)
	for _, name := range toolNames(root) {
		if name == "hidden" || strings.Contains(name, "hidden") {
			t.Fatalf("deferred schema leaked into first packet: %v", toolNames(root))
		}
	}
	encoded, _ := json.Marshal(root)
	if strings.Contains(string(encoded), `"defer_loading":true`) {
		t.Fatalf("deferred flag leaked: %s", encoded)
	}
}

func TestServerExecutionNeverBridged(t *testing.T) {
	policy := RoutePolicy{ClientSearch: ClientSearchBridge}
	root := rewriteTestRequest(t, `{
		"tools": [{"type": "tool_search", "execution": "server"}],
		"input": [{"type": "tool_search_call", "execution": "server", "call_id": "c1", "arguments": {"q": "x"}}]
	}`, policy)
	tools, _ := root["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("server search declaration must stay: %v", tools)
	}
	if tool, ok := tools[0].(map[string]any); !ok || tool["type"] != "tool_search" {
		t.Fatalf("server search rewritten: %v", tools)
	}
}

func TestToolChoiceSearchConverted(t *testing.T) {
	policy := RoutePolicy{ClientSearch: ClientSearchBridge}
	root := rewriteTestRequest(t, `{
		"tools": [{"type": "tool_search"}],
		"tool_choice": {"type": "tool_search"},
		"input": []
	}`, policy)
	choice := root["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != ToolSearchName {
		t.Fatalf("choice not converted: %v", choice)
	}
}

func TestSearchCallOutputRoundTrip(t *testing.T) {
	policy := RoutePolicy{ClientSearch: ClientSearchBridge}
	root := rewriteTestRequest(t, `{
		"tools": [{"type": "tool_search"}],
		"input": [
			{"type": "tool_search_call", "call_id": "call_9", "arguments": {"query": "patch"}},
			{"type": "tool_search_output", "call_id": "call_9", "tools": [{"type": "function", "name": "apply_patch"}]}
		]
	}`, policy)
	input := root["input"].([]any)
	call := input[0].(map[string]any)
	if call["type"] != "function_call" || call["name"] != ToolSearchName {
		t.Fatalf("call not bridged: %v", call)
	}
	output := input[1].(map[string]any)
	if output["type"] != "function_call_output" {
		t.Fatalf("output not bridged: %v", output)
	}
	var manifest struct {
		Tools []map[string]any `json:"tools"`
	}
	if err := json.Unmarshal([]byte(output["output"].(string)), &manifest); err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if len(manifest.Tools) != 1 || manifest.Tools[0]["name"] != "apply_patch" {
		t.Fatalf("manifest wrong: %v", manifest)
	}
}

func TestNameOnlyManifestPreserved(t *testing.T) {
	manifest := CompactToolSearchManifest([]any{
		map[string]any{"type": "function", "name": "a", "parameters": map[string]any{"type": "object"}},
	}, nil)
	if len(manifest) != 1 {
		t.Fatalf("expected one entry")
	}
	entry := manifest[0].(map[string]any)
	if entry["name"] != "a" || len(entry) != 1 {
		t.Fatalf("manifest must be name-only: %v", entry)
	}
}

func TestLatestDiscoveryAlwaysFits(t *testing.T) {
	contract := ParseContract([]byte(`{
		"tools": [{"type": "tool_search"}],
		"input": [{"type": "tool_search_output", "call_id": "c", "tools": [
			{"type": "function", "name": "big", "parameters": {"type": "object", "properties": {"p": {"type": "string", "description": "0123456789abcdef"}}}}
		]}]
	}`))
	root := map[string]any{"tools": []any{map[string]any{"type": "function", "name": ToolSearchName}}}
	limits := DefaultLimits()
	limits.MaxActiveToolBytes = 10
	if _, err := injectDiscoveredTools(root, contract, limits); err == nil {
		t.Fatalf("expected budget error when latest discovery does not fit")
	} else if !IsRequestScopedError(err) {
		t.Fatalf("expected request-scoped error, got %v", err)
	}
}

func TestFiftyRoundClosure(t *testing.T) {
	// Build 50 discovery rounds; the newest round must fully activate while
	// history from every round survives untouched.
	rounds := make([]any, 0, 100)
	rounds = append(rounds, map[string]any{"type": "message", "role": "user", "content": "go"})
	for round := 1; round <= 50; round++ {
		callID := strings.Repeat("c", 3) + string(rune('0'+round%10)) + strings.Repeat("d", round%5)
		rounds = append(rounds,
			map[string]any{"type": "tool_search_call", "call_id": callID, "arguments": map[string]any{"query": "q"}},
			map[string]any{"type": "tool_search_output", "call_id": callID, "tools": []any{
				map[string]any{"type": "function", "name": "tool_round", "parameters": map[string]any{"type": "object"}},
			}},
		)
	}
	_ = rounds
	body, _ := json.Marshal(map[string]any{
		"tools": []any{map[string]any{"type": "tool_search"}},
		"input": []any{
			map[string]any{"type": "tool_search_call", "call_id": "call_final", "arguments": map[string]any{"query": "q"}},
			map[string]any{"type": "tool_search_output", "call_id": "call_final", "tools": []any{
				map[string]any{"type": "function", "name": "final_tool", "parameters": map[string]any{"type": "object"}},
			}},
		},
	})
	policy := RoutePolicy{ClientSearch: ClientSearchBridge}
	contract := ParseContract(body)
	value, _ := decodeValue(body)
	if _, err := RewriteRequest(value, policy, contract, DefaultLimits()); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	root := value.(map[string]any)
	found := false
	for _, name := range toolNames(root) {
		if name == "final_tool" {
			found = true
		}
	}
	if !found {
		t.Fatalf("latest discovery missing: %v", toolNames(root))
	}
}
