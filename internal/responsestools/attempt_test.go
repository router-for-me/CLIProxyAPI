package responsestools

import (
	"strings"
	"testing"
)

func bridgeAttemptPolicy() RoutePolicy {
	return RoutePolicy{
		ClientSearch:  ClientSearchBridge,
		CustomTools:   CustomToolsFunction,
		CustomGrammar: CustomGrammarDescribe,
		Schema:        SchemaPolicy{LocalRefs: LocalRefsPreserve},
	}
}

func TestPrepareBridgeEndToEnd(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}, {\"type\": \"custom\", \"name\": \"apply_patch\", \"description\": \"patch\"}], \"input\": []}")
	limiter := NewLimiter(DefaultLimits())
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Attempt == nil || !prepared.Attempt.Adapted() {
		t.Fatalf("expected adapted attempt")
	}
	if string(prepared.Body) == string(body) {
		t.Fatalf("expected rewritten body")
	}
	if strings.Contains(string(prepared.Body), "\"type\":\"custom\"") {
		t.Fatalf("custom declarations leaked: %s", prepared.Body)
	}
	prepared.Attempt.Close()
	if attempts, _ := limiter.Usage(); attempts != 0 {
		t.Fatalf("lease leaked")
	}
}

// A request payload larger than max-attempt-bytes is caller content, not
// bridge protocol state. It must be charged to the shared pool so long
// conversations keep working instead of failing with "attempt budget exceeded".
func TestPrepareRequestBodyLargerThanAttemptCeiling(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxAttemptBytes = 512
	limits.MaxStateBytes = 1 << 20
	limits.MaxActiveToolBytes = 4096

	padding := strings.Repeat("x", 4096)
	body := []byte(`{"tools":[{"type":"tool_search"}],"input":[{"role":"user","content":"` + padding + `"}]}`)
	if len(body) <= limits.MaxAttemptBytes {
		t.Fatalf("test body must exceed the ceiling, got %d", len(body))
	}

	limiter := NewLimiter(limits)
	prepared, err := Prepare(body, bridgeAttemptPolicy(), limits, limiter)
	if err != nil {
		t.Fatalf("oversized request body must still prepare: %v", err)
	}
	if prepared.Attempt == nil || !prepared.Attempt.Adapted() {
		t.Fatal("expected adapted attempt")
	}
	if len(prepared.Body) == 0 {
		t.Fatal("rewritten body must still be forwarded")
	}
	if !strings.Contains(string(prepared.Body), padding) {
		t.Fatal("rewritten body must preserve caller content")
	}
	prepared.Attempt.Close()
	if attempts, bytes := limiter.Usage(); attempts != 0 || bytes != 0 {
		t.Fatalf("lease leaked: attempts=%d bytes=%d", attempts, bytes)
	}
}

func TestPrepareDiscoveredNamespacedCustomKeepsOneWireAlias(t *testing.T) {
	body := []byte(`{"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"search_1","arguments":{"query":"patch"}},{"type":"tool_search_output","call_id":"search_1","tools":[{"type":"namespace","name":"fs","tools":[{"type":"custom","name":"patch","description":"patch"}]}]}]}`)
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	identity := ToolIdentity{Namespace: "fs", Name: "patch", Kind: ToolKindCustom}
	customAlias, ok := prepared.Attempt.bridge.Alias(identity)
	if !ok {
		t.Fatalf("custom bridge lost original identity: %v", prepared.Attempt.bridge.Aliases())
	}
	if got := prepared.Attempt.contract.AliasByID[identity]; got != customAlias {
		t.Fatalf("contract alias = %q, custom bridge alias = %q", got, customAlias)
	}
	value, ok := decodeValue(prepared.Body)
	if !ok {
		t.Fatalf("prepared body is invalid JSON: %s", prepared.Body)
	}
	root, _ := value.(map[string]any)
	tools, _ := root["tools"].([]any)
	foundDeclaration := false
	for _, rawTool := range tools {
		tool, _ := rawTool.(map[string]any)
		if stringField(tool, "name") == customAlias {
			foundDeclaration = true
			break
		}
	}
	if !foundDeclaration {
		t.Fatalf("active custom declaration alias %q is missing: %s", customAlias, prepared.Body)
	}
	input, _ := root["input"].([]any)
	if len(input) < 2 {
		t.Fatalf("prepared discovery history is missing: %s", prepared.Body)
	}
	var manifest string
	for _, rawItem := range input {
		item, _ := rawItem.(map[string]any)
		if stringField(item, "type") == "function_call_output" && stringField(item, "call_id") == "search_1" {
			manifest, _ = item["output"].(string)
			break
		}
	}
	if !strings.Contains(manifest, customAlias) {
		t.Fatalf("discovery manifest alias does not match declaration %q: %s", customAlias, manifest)
	}
}

func TestPrepareCustomAliasAvoidsEagerFunctionNames(t *testing.T) {
	collidingAlias := CustomAliasFor("", "patch")
	body := []byte(`{"tools":[{"type":"function","name":"` + collidingAlias + `","parameters":{"type":"object"}},{"type":"custom","name":"patch"}],"input":[]}`)
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	customAlias, ok := prepared.Attempt.bridge.Alias(ToolIdentity{Name: "patch", Kind: ToolKindCustom})
	if !ok {
		t.Fatal("custom alias missing")
	}
	if customAlias == collidingAlias {
		t.Fatalf("custom alias collides with eager function name %q", collidingAlias)
	}
	value, _ := decodeValue(prepared.Body)
	root, _ := value.(map[string]any)
	tools, _ := root["tools"].([]any)
	names := make(map[string]struct{}, len(tools))
	for _, rawTool := range tools {
		tool, _ := rawTool.(map[string]any)
		name := stringField(tool, "name")
		if _, exists := names[name]; exists {
			t.Fatalf("duplicate outbound function name %q", name)
		}
		names[name] = struct{}{}
	}
}

func TestPrepareAllowedToolsKeepsOrdinaryFunctionWithCustomDisplayName(t *testing.T) {
	body := []byte(`{"tools":[{"type":"function","name":"patch","parameters":{"type":"object"}},{"type":"custom","name":"patch"}],"tool_choice":{"type":"allowed_tools","tools":[{"type":"function","name":"patch"}]},"input":[]}`)
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	value, _ := decodeValue(prepared.Body)
	root, _ := value.(map[string]any)
	choice, _ := root["tool_choice"].(map[string]any)
	allowed, _ := choice["tools"].([]any)
	if len(allowed) != 1 {
		t.Fatalf("allowed_tools = %v, want one entry", allowed)
	}
	tool, _ := allowed[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "patch" {
		t.Fatalf("ordinary function choice was rewritten as custom: %v", tool)
	}
}

func TestPrepareCustomHistoryPrefersExplicitNamespace(t *testing.T) {
	body := []byte(`{"tools":[{"type":"custom","namespace":"A","name":"patch"}],"input":[{"type":"custom_tool_call","namespace":"B","name":"patch","call_id":"c1","input":"x"},{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}]}`)
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()

	value, _ := decodeValue(prepared.Body)
	root, _ := value.(map[string]any)
	input, _ := root["input"].([]any)
	call, _ := input[0].(map[string]any)
	wantAlias := CustomAliasFor("B", "patch")
	if call["type"] != "function_call" || call["name"] != wantAlias {
		t.Fatalf("namespaced custom history = %v, want alias %q", call, wantAlias)
	}
}

func TestPrepareRejectsAmbiguousCustomHistoryWithoutNamespace(t *testing.T) {
	body := []byte(`{
		"tools":[
			{"type":"custom","namespace":"A","name":"patch"},
			{"type":"custom","namespace":"B","name":"patch"}
		],
		"input":[
			{"type":"custom_tool_call","name":"patch","call_id":"c1","input":"x"},
			{"type":"custom_tool_call_output","call_id":"c1","output":"ok"}
		]
	}`)
	_, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil {
		t.Fatal("ambiguous custom history must not create a new unnamespaced identity")
	}
	if !IsRequestScopedError(err) {
		t.Fatalf("ambiguous custom history error must be request scoped: %v", err)
	}
}

func TestPreparePassthroughReturnsOriginalSlice(t *testing.T) {
	body := []byte("{\"model\": \"x\", \"input\": \"hello\"}")
	limiter := NewLimiter(DefaultLimits())
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Attempt != nil {
		t.Fatalf("unexpected attempt")
	}
	if string(prepared.Body) != string(body) {
		t.Fatalf("body must pass through untouched")
	}
	if attempts, _ := limiter.Usage(); attempts != 0 {
		t.Fatalf("passthrough must not reserve state")
	}
}

func TestPrepareDisabledRejects(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	policy := RoutePolicy{ClientSearch: ClientSearchDisabled}
	_, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil || !IsRequestScopedError(err) {
		t.Fatalf("expected request-scoped rejection, got %v", err)
	}
}

func TestPrepareRejectCustomTools(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	_, err := Prepare(body, RoutePolicy{CustomTools: CustomToolsReject}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("no custom present, must pass: %v", err)
	}
	customBody := []byte("{\"tools\": [{\"type\": \"custom\", \"name\": \"x\"}], \"input\": []}")
	_, err = Prepare(customBody, RoutePolicy{CustomTools: CustomToolsReject}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil {
		t.Fatalf("expected custom rejection")
	}
}

func TestAttemptResponseRestore(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	limiter := NewLimiter(DefaultLimits())
	prepared, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	resp := "{\"output\": [{\"type\": \"function_call\", \"id\": \"" + poisonedSearchCallID +
		"\", \"name\": \"tool_search\", \"call_id\": \"c1\", \"arguments\": " + "{\"query\":\"x\"}" + "}]}"
	restored, err := prepared.Attempt.RewriteResponse([]byte(resp))
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !strings.Contains(string(restored), "tool_search_call") {
		t.Fatalf("search call not restored: %s", restored)
	}
	if !strings.Contains(string(restored), repairedSearchCallID) {
		t.Fatalf("search call id not migrated: %s", restored)
	}
}

func TestAttemptWireAliasesOnlyIncludeActiveSearchBridge(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantSearch bool
	}{
		{
			name: "custom tools without client search",
			body: `{"tools":[{"type":"custom","name":"apply_patch","description":"patch"}],"input":[]}`,
		},
		{
			name:       "client search",
			body:       `{"tools":[{"type":"tool_search"}],"input":[]}`,
			wantSearch: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prepared, err := Prepare([]byte(tt.body), bridgeAttemptPolicy(), DefaultLimits(), NewLimiter(DefaultLimits()))
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			defer prepared.Attempt.Close()

			search, _ := prepared.Attempt.WireAliases()
			if gotSearch := len(search) > 0; gotSearch != tt.wantSearch {
				t.Fatalf("search aliases = %v, want active=%v", search, tt.wantSearch)
			}
		})
	}
}

func TestAttemptWireHistoryAliasesIncludeHistoryOnlyCustomTools(t *testing.T) {
	body := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call_1","name":"compile","input":"source"},{"type":"custom_tool_call_output","call_id":"call_1","output":"done"}]}`)
	prepared, err := Prepare(body, RoutePolicy{CustomTools: CustomToolsFunction}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare history-only custom request: %v", err)
	}
	defer prepared.Attempt.Close()

	_, custom := prepared.Attempt.WireAliases()
	if len(custom) != 0 {
		t.Fatalf("history-only custom tool must not require a current declaration, got active aliases %v", custom)
	}
	history := prepared.Attempt.WireHistoryAliases()
	if len(history) != 1 || !strings.HasPrefix(history[0], CustomAliasPrefix) {
		t.Fatalf("history aliases = %v, want one stable custom alias", history)
	}
}

func TestPrepareRetryRebuildsFromOriginal(t *testing.T) {
	body := []byte("{\"tools\": [{\"type\": \"tool_search\"}], \"input\": []}")
	limiter := NewLimiter(DefaultLimits())
	first, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	first.Attempt.Close()
	// A retry must rebuild from the original client contract, never by
	// re-wrapping an already normalized body.
	second, err := Prepare(body, bridgeAttemptPolicy(), DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	defer second.Attempt.Close()
	if string(first.Body) != string(second.Body) {
		t.Fatalf("retry produced different wire body")
	}
}

// The Codex desktop client declares request_environment_input with a
// self-referencing definition under a namespace tool. A portable-surface
// route must flatten the cyclic edges instead of forwarding them to an
// upstream that rejects recursive schemas.
func TestPrepareFlattenEndToEnd(t *testing.T) {
	recursive := map[string]any{"anyOf": []any{
		map[string]any{"type": "string"},
		map[string]any{"items": map[string]any{"$ref": "#/$defs/__schema0"}, "type": "array"},
		map[string]any{"additionalProperties": map[string]any{"$ref": "#/$defs/__schema0"}, "type": "object"},
	}}
	body := mustMarshal(map[string]any{
		"model": "muse-spark-1.3-contributor",
		"tools": []any{map[string]any{
			"type": "namespace", "name": "mcp__codex_app",
			"tools": []any{map[string]any{
				"type": "function", "name": "request_environment_input",
				"parameters": map[string]any{
					"$defs":      map[string]any{"__schema0": recursive},
					"type":       "object",
					"properties": map[string]any{"secret": map[string]any{"$ref": "#/$defs/__schema0"}},
				},
			}},
		}},
		"input": []any{},
	})
	policy := RoutePolicy{
		ClientSearch:  ClientSearchNative,
		CustomTools:   CustomToolsFunction,
		CustomGrammar: CustomGrammarDescribe,
		Schema:        SchemaPolicy{SearchRequired: SearchRequiredComplete, LocalRefs: LocalRefsFlatten},
	}
	limiter := NewLimiter(DefaultLimits())
	prepared, err := Prepare(body, policy, DefaultLimits(), limiter)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Attempt == nil || !prepared.Attempt.Adapted() {
		t.Fatalf("expected adapted attempt")
	}
	if strings.Contains(string(prepared.Body), "$ref") {
		t.Fatalf("cyclic references reached the wire body: %s", prepared.Body)
	}
	if !strings.Contains(string(prepared.Body), "$defs") {
		t.Fatalf("acyclic containers must be preserved: %s", prepared.Body)
	}
	prepared.Attempt.Close()
	if attempts, _ := limiter.Usage(); attempts != 0 {
		t.Fatalf("lease leaked")
	}
}
