package responsestools

import (
	"encoding/json"
	"fmt"
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
	body := []byte(`{
		"tools": [{"type": "tool_search", "execution": "server"}],
		"input": [{"type": "tool_search_call", "execution": "server", "call_id": "c1", "arguments": {"q": "x"}}]
	}`)
	contract := ParseContract(body)
	value, ok := decodeValue(body)
	if !ok {
		t.Fatal("decode request")
	}
	if _, err := RewriteRequest(value, policy, contract, DefaultLimits()); err == nil {
		t.Fatal("bridge route must reject server-executed search instead of passing it through")
	}
}

func TestSearchBridgeRejectsUncorrelatedHistory(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "missing call id",
			body: `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","arguments":{"query":"x"}}]}`,
		},
		{
			name: "non-object arguments",
			body: `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"c1","arguments":"{\"query\":\"x\"}"}]}`,
		},
		{
			name: "invalid arguments",
			body: `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"c1","arguments":"{"}]}`,
		},
		{
			name: "output without call",
			body: `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_output","call_id":"c1","tools":[]}]}`,
		},
		{
			name: "duplicate call id",
			body: `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"c1","arguments":{}},{"type":"tool_search_call","call_id":"c1","arguments":{}}]}`,
		},
		{
			name: "duplicate output",
			body: `{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"c1","arguments":{}},{"type":"tool_search_output","call_id":"c1","tools":[]},{"type":"tool_search_output","call_id":"c1","tools":[]}]}`,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(test.body)
			contract := ParseContract(raw)
			value, ok := decodeValue(raw)
			if !ok {
				t.Fatal("decode request")
			}
			_, err := RewriteRequest(value, RoutePolicy{ClientSearch: ClientSearchBridge}, contract, DefaultLimits())
			if err == nil {
				t.Fatal("expected uncorrelated search history rejection")
			}
		})
	}
}

func TestPrepareRejectsConflictingDiscoveryDeclarationsInOneRound(t *testing.T) {
	body := []byte(`{
		"tools":[{"type":"tool_search"}],
		"input":[
			{"type":"tool_search_call","call_id":"c1","arguments":{}},
			{"type":"tool_search_output","call_id":"c1","tools":[
				{"type":"function","name":"repeat","description":"first"},
				{"type":"function","name":"repeat","description":"second"}
			]}
		]
	}`)
	_, err := Prepare(body, RoutePolicy{ClientSearch: ClientSearchBridge}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil {
		t.Fatal("expected conflicting declarations in the same discovery round to be rejected")
	}
	if got := responseStatus(err); got != 422 {
		t.Fatalf("status = %d, want 422: %v", got, err)
	}
}

func TestInjectDiscoveredToolsKeepsOlderDiscoveryRoundsAtomic(t *testing.T) {
	search := map[string]any{"type": "function", "name": ToolSearchName, "parameters": map[string]any{"type": "object"}}
	root := map[string]any{"tools": []any{search}, "input": []any{}}
	latest := ToolIdentity{Name: "latest", Kind: ToolKindFunction}
	oldOne := ToolIdentity{Name: "old_one", Kind: ToolKindFunction}
	oldTwo := ToolIdentity{Name: "old_two", Kind: ToolKindFunction}
	declaration := func(name string) json.RawMessage {
		value, err := json.Marshal(map[string]any{"type": "function", "name": name, "parameters": map[string]any{"type": "object"}})
		if err != nil {
			t.Fatalf("marshal declaration: %v", err)
		}
		return value
	}
	contract := NewToolContract()
	contract.LatestRound = 2
	contract.Declarations[latest] = declaration(latest.Name)
	contract.Declarations[oldOne] = declaration(oldOne.Name)
	contract.Declarations[oldTwo] = declaration(oldTwo.Name)
	for identity, round := range map[ToolIdentity]int{latest: 2, oldOne: 1, oldTwo: 1} {
		contract.Discovered[identity] = struct{}{}
		contract.DiscoveredRound[identity] = round
		contract.AliasByID[identity] = identity.Name
		contract.IDByAlias[identity.Name] = identity
	}
	baseTools := []any{search, map[string]any{"type": "function", "name": latest.Name, "parameters": map[string]any{"type": "object"}}}
	oneOldTools := append(append([]any(nil), baseTools...), map[string]any{"type": "function", "name": oldOne.Name, "parameters": map[string]any{"type": "object"}})
	allOldTools := append(append([]any(nil), oneOldTools...), map[string]any{"type": "function", "name": oldTwo.Name, "parameters": map[string]any{"type": "object"}})
	oneOldRoot := map[string]any{"tools": oneOldTools, "input": []any{}}
	allOldRoot := map[string]any{"tools": allOldTools, "input": []any{}}
	oneOldSize, err := ActiveToolArraysBytes(oneOldRoot)
	if err != nil {
		t.Fatalf("measure one old declaration: %v", err)
	}
	allOldSize, err := ActiveToolArraysBytes(allOldRoot)
	if err != nil {
		t.Fatalf("measure both old declarations: %v", err)
	}
	if oneOldSize >= allOldSize {
		t.Fatalf("test setup does not distinguish partial from full round: one=%d all=%d", oneOldSize, allOldSize)
	}
	limits := DefaultLimits()
	limits.MaxActiveToolBytes = oneOldSize
	if _, err := injectDiscoveredTools(root, contract, limits); err != nil {
		t.Fatalf("inject: %v", err)
	}
	tools := root["tools"].([]any)
	names := make(map[string]bool, len(tools))
	for _, rawTool := range tools {
		tool, ok := rawTool.(map[string]any)
		if ok {
			names[stringField(tool, "name")] = true
		}
	}
	if !names[latest.Name] {
		t.Fatal("latest discovery round was not activated")
	}
	if names[oldOne.Name] || names[oldTwo.Name] {
		t.Fatalf("older discovery round was only partially activated: %v", names)
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

func TestToolChoiceUsesAliasForDiscoveredNamespacedFunction(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type":"tool_search"},
			{"type":"function","name":"eager"}
		],
		"tool_choice": {
			"type":"allowed_tools",
			"tools":[
				{"type":"function","namespace":"fs","name":"read"},
				{"type":"function","name":"eager"}
			]
		},
		"input":[
			{"type":"tool_search_call","call_id":"search_1","arguments":{"query":"read"}},
			{"type":"tool_search_output","call_id":"search_1","tools":[
				{"type":"namespace","name":"fs","tools":[
					{"type":"function","name":"read","parameters":{"type":"object"}}
				]}
			]}
		]
	}`)
	contract := ParseContract(body)
	if contract == nil {
		t.Fatal("expected parsed contract")
	}
	discovered := ToolIdentity{Namespace: "fs", Name: "read", Kind: ToolKindFunction}
	alias, ok := contract.AliasByID[discovered]
	if !ok {
		t.Fatal("expected alias for discovered function")
	}
	value, ok := decodeValue(body)
	if !ok {
		t.Fatal("decode request")
	}
	if _, err := RewriteRequest(value, RoutePolicy{ClientSearch: ClientSearchBridge}, contract, DefaultLimits()); err != nil {
		t.Fatalf("rewrite request: %v", err)
	}
	root, _ := value.(map[string]any)
	choice, _ := root["tool_choice"].(map[string]any)
	allowed, _ := choice["tools"].([]any)
	if len(allowed) != 2 {
		t.Fatalf("allowed_tools = %v, want two entries", allowed)
	}
	discoveredChoice, _ := allowed[0].(map[string]any)
	if discoveredChoice["name"] != alias {
		t.Fatalf("discovered choice name = %#v, want alias %q", discoveredChoice["name"], alias)
	}
	if _, exists := discoveredChoice["namespace"]; exists {
		t.Fatalf("discovered choice retained namespace after aliasing: %v", discoveredChoice)
	}
	eagerChoice, _ := allowed[1].(map[string]any)
	if eagerChoice["name"] != "eager" {
		t.Fatalf("eager choice changed: %v", eagerChoice)
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
		callID := fmt.Sprintf("call_%02d", round)
		rounds = append(rounds,
			map[string]any{"type": "tool_search_call", "call_id": callID, "arguments": map[string]any{"query": "q"}},
			map[string]any{"type": "tool_search_output", "call_id": callID, "tools": []any{
				map[string]any{"type": "function", "name": fmt.Sprintf("tool_round_%02d", round), "parameters": map[string]any{"type": "object"}},
			}},
		)
	}
	rounds = append(rounds,
		map[string]any{"type": "tool_search_call", "call_id": "call_final", "arguments": map[string]any{"query": "q"}},
		map[string]any{"type": "tool_search_output", "call_id": "call_final", "tools": []any{
			map[string]any{"type": "function", "name": "final_tool", "parameters": map[string]any{"type": "object"}},
		}},
	)
	body, _ := json.Marshal(map[string]any{
		"tools": []any{map[string]any{"type": "tool_search"}},
		"input": rounds,
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
	activeNames := make(map[string]struct{})
	for _, name := range toolNames(root) {
		activeNames[name] = struct{}{}
	}
	for round := 1; round <= 50; round++ {
		name := fmt.Sprintf("tool_round_%02d", round)
		if _, exists := activeNames[name]; !exists {
			t.Fatalf("discovery tool from round %d was not activated: %v", round, toolNames(root))
		}
	}
	input := root["input"].([]any)
	if len(input) != 103 {
		t.Fatalf("history length = %d, want all 50 rounds plus the final round and user message", len(input))
	}
	callIDs := make(map[string]struct{}, 51)
	outputIDs := make(map[string]struct{}, 51)
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		switch stringField(item, "type") {
		case "function_call":
			callIDs[stringField(item, "call_id")] = struct{}{}
		case "function_call_output":
			outputIDs[stringField(item, "call_id")] = struct{}{}
		}
	}
	if len(callIDs) != 51 || len(outputIDs) != 51 {
		t.Fatalf("history calls/outputs = %d/%d, want 51/51", len(callIDs), len(outputIDs))
	}
	for callID := range callIDs {
		if _, exists := outputIDs[callID]; !exists {
			t.Fatalf("search history call %q lost its output", callID)
		}
	}
}
