package responsestools

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRepairInputItemIDsPreservesOtherBytes(t *testing.T) {
	body := []byte("{\n  \"metadata\":{\"n\":9007199254740993,\"s\":\"\\u4e2d\",\"f\":25000.0},\n  \"input\":[{\"type\":\"tool_search_call\",\"id\":\"fc_x\",\"call_id\":\"c1\",\"arguments\":{}}]\n}")
	want := strings.Replace(string(body), `"id":"fc_x"`, `"id":"tsc_x"`, 1)
	out, changed, err := RepairInputItemIDs(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if string(out) != want {
		t.Fatalf("non-ID bytes changed:\n got %s\nwant %s", out, want)
	}
	if strings.Contains(string(body), `"id":"tsc_x"`) {
		t.Fatal("mutated caller body")
	}
}

// A key written with a JSON escape is the same key. The repair has to find it
// without rewriting the key itself.
func TestRepairInputItemIDsPreservesEscapedStructuralKeys(t *testing.T) {
	body := []byte(`{"in\u0070ut":[{"ty\u0070e":"tool_search_call","i\u0064":"fc_x","call_id":"c1"}],"previous_\u0072esponse_id":""}`)
	want := strings.Replace(string(body), `"i\u0064":"fc_x"`, `"i\u0064":"tsc_x"`, 1)
	out, changed, err := RepairInputItemIDs(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if string(out) != want {
		t.Fatalf("key escaping was not preserved:\n got %s\nwant %s", out, want)
	}
}

func TestRepairInputItemIDsHealthyIsByteIdentical(t *testing.T) {
	healthy := []byte(`{
		"model": "gpt-6.1-sol",
		"tools": [{"type": "tool_search", "execution": "client"}],
		"input": [
			{"type": "tool_search_call", "id": "` + repairedSearchCallID + `", "call_id": "c1", "arguments": {}, "execution": "client"},
			{"type": "tool_search_output", "id": "` + healthySearchOutID + `", "call_id": "c1", "execution": "client", "tools": []}
		]
	}`)
	out, changed, err := RepairInputItemIDs(healthy)
	if err != nil || changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if string(out) != string(healthy) {
		t.Fatalf("healthy request was rewritten")
	}
	// A second pass must be a no-op, so a body can cross the repair more than
	// once without drifting.
	again, changedAgain, err := RepairInputItemIDs(out)
	if err != nil || changedAgain || string(again) != string(healthy) {
		t.Fatalf("repair is not idempotent: changed=%v err=%v", changedAgain, err)
	}
}

// History that no longer declares any tool is still history the client will
// replay, so it is repaired without waiting for ParseContract to notice.
func TestRepairInputItemIDsWithoutDeclarations(t *testing.T) {
	body := []byte(`{"input":[{"type":"function_call","id":"ctc_x","call_id":"c1","name":"run","arguments":"{}"}]}`)
	out, changed, err := RepairInputItemIDs(body)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if !strings.Contains(string(out), `"id":"fc_x"`) {
		t.Fatalf("custom id was not migrated for the function item: %s", out)
	}
}

func TestRepairInputItemIDsRejectsOpaquePollution(t *testing.T) {
	for _, body := range []string{
		`{"previous_response_id":"resp_1","input":[{"type":"tool_search_call","id":"fc_x","call_id":"c1","arguments":{}}]}`,
		`{"previous_item_id":"msg_1","input":[{"type":"tool_search_call","id":"fc_x","call_id":"c1","arguments":{}}]}`,
		`{"input":[{"type":"item_reference","id":"msg_1"},{"type":"tool_search_call","id":"fc_x","call_id":"c1","arguments":{}}]}`,
	} {
		out, _, err := RepairInputItemIDs([]byte(body))
		var compat *ToolCompatibilityError
		if !errors.As(err, &compat) || compat.StatusCode() != 422 || compat.Reason != ReasonOpaqueHistory {
			t.Fatalf("want opaque history 422, got %v", out)
		}
	}
}

// Opaque history the repair does not need to touch stays on the pass-through
// path: the client already knows how to continue that conversation.
func TestRepairInputItemIDsKeepsHealthyOpaqueHistory(t *testing.T) {
	body := []byte(`{"previous_response_id":"resp_1","input":[{"type":"tool_search_call","id":"` +
		repairedSearchCallID + `","call_id":"c1","arguments":{}}]}`)
	out, changed, err := RepairInputItemIDs([]byte(body))
	if err != nil || changed || string(out) != string(body) {
		t.Fatalf("healthy opaque history must pass through: changed=%v err=%v", changed, err)
	}
}

func TestRepairInputItemIDsRejectsTargetCollision(t *testing.T) {
	body := []byte(`{"input":[{"type":"tool_search_call","id":"fc_x","call_id":"c1","arguments":{}},{"type":"tool_search_call","id":"tsc_x","call_id":"c2","arguments":{}}]}`)
	_, _, err := RepairInputItemIDs([]byte(body))
	var compat *ToolCompatibilityError
	if !errors.As(err, &compat) || compat.StatusCode() != 422 || compat.Reason != ReasonAmbiguousIdentity {
		t.Fatalf("want collision 422, got %v", err)
	}

	// The same owner replayed twice is one identity, not a collision.
	replay := []byte(`{"input":[{"type":"tool_search_call","id":"fc_x","call_id":"c1","arguments":{}},{"type":"tool_search_call","id":"fc_x","call_id":"c1","arguments":{}}]}`)
	out, changed, err := RepairInputItemIDs(replay)
	if err != nil || !changed {
		t.Fatalf("identical replay: changed=%v err=%v", changed, err)
	}
	if strings.Count(string(out), `"id":"tsc_x"`) != 2 {
		t.Fatalf("replay was not migrated consistently: %s", out)
	}
}

// Two different items must not converge on one id, whether they arrive from
// two legacy namespaces or from a migration and a native id.
func TestRepairInputItemIDsRejectsConvergingSources(t *testing.T) {
	bodies := []string{
		`{"input":[{"type":"function_call","id":"ctc_x","call_id":"c1","name":"a","arguments":"{}"},{"type":"function_call","id":"fc_x","call_id":"c2","name":"b","arguments":"{}"}]}`,
		`{"input":[{"type":"function_call_output","id":"ctco_x","call_id":"c1","output":"a"},{"type":"function_call_output","id":"fco_x","call_id":"c2","output":"b"}]}`,
	}
	for _, body := range bodies {
		_, _, err := RepairInputItemIDs([]byte(body))
		var compat *ToolCompatibilityError
		if !errors.As(err, &compat) || compat.Reason != ReasonAmbiguousIdentity {
			t.Fatalf("want an ambiguous identity 422, got %v", err)
		}
	}
}

func TestRepairInputItemIDsDuplicateStructuralKeys(t *testing.T) {
	bodies := []string{
		`{"input":[{"type":"tool_search_call","id":"fc_x","call_id":"c1"}],"input":[{"type":"tool_search_call","id":"fc_y","call_id":"c2"}]}`,
		`{"input":[{"type":"tool_search_call","id":"fc_x","id":"fc_y","call_id":"c1"}]}`,
		`{"input":[{"type":"function_call","type":"tool_search_call","id":"fc_x","call_id":"c1"}]}`,
	}
	for _, body := range bodies {
		_, _, err := RepairInputItemIDs([]byte(body))
		var compat *ToolCompatibilityError
		if !errors.As(err, &compat) || compat.StatusCode() != 400 || compat.Reason != ReasonJSONSyntax {
			t.Fatalf("%s: want 400 json_syntax, got %v", body, err)
		}
	}
	// Without a migration candidate the body keeps its existing validation.
	healthy := []byte(`{"input":[{"type":"message","id":"msg_1","id":"msg_1","content":[]}]}`)
	if _, changed, err := RepairInputItemIDs(healthy); err != nil || changed {
		t.Fatalf("a request with nothing to repair must not gain a new validator: changed=%v err=%v", changed, err)
	}
	// Business text is the client's own content, not request structure.
	nested := []byte(`{"input":[{"type":"tool_search_call","id":"fc_x","call_id":"c1","arguments":"{\"id\":\"id\"}"}]}`)
	if _, changed, err := RepairInputItemIDs(nested); err != nil || !changed {
		t.Fatalf("business text must not be scanned: changed=%v err=%v", changed, err)
	}
}

func TestRepairInputItemIDsLeavesUnownedItems(t *testing.T) {
	body := []byte(`{
		"input": [
			{"type": "tool_search_call", "id": "fc_server", "call_id": "c0", "execution": "server", "arguments": {}},
			{"type": "tool_search_call", "id": "item_unknown", "call_id": "c1", "arguments": {}},
			{"type": "tool_search_call", "call_id": "c2", "arguments": {}},
			{"type": "message", "id": "fc_lookalike", "role": "user", "content": []},
			{"type": "reasoning", "id": "msg_lookalike", "summary": []},
			{"type": "function_call", "id": 7, "call_id": "c3", "name": "run", "arguments": "{}"},
			{"type": "function_call", "id": "", "call_id": "c4", "name": "run", "arguments": "{}"},
			{"type": "function_call", "id": "ctc_polluted", "call_id": "c5", "name": "run", "arguments": "{}"},
			{"type": "custom_tool_call", "id": "ctc_healthy", "call_id": "c6", "name": "patch", "input": "x"}
		]
	}`)
	out, changed, err := RepairInputItemIDs(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !changed {
		t.Fatalf("the custom history item should still migrate")
	}
	value, ok := decodeValue(out)
	if !ok {
		t.Fatalf("repaired body is not valid JSON")
	}
	root, _ := value.(map[string]any)
	input, _ := root["input"].([]any)
	want := []string{
		"fc_server", "item_unknown", "", "fc_lookalike", "msg_lookalike", "", "", "fc_polluted", "ctc_healthy",
	}
	for index, expected := range want {
		item, _ := input[index].(map[string]any)
		rawID, exists := item["id"]
		if !exists {
			if expected != "" {
				t.Fatalf("input[%d] lost its id", index)
			}
			continue
		}
		if number, isNumber := rawID.(json.Number); isNumber {
			// A non-string id is left exactly as the client sent it.
			if number.String() != "7" {
				t.Fatalf("input[%d].id = %v, want the original literal", index, number)
			}
			continue
		}
		if rawID != expected {
			t.Fatalf("input[%d].id = %v, want %q", index, rawID, expected)
		}
	}
}

func TestRepairInputItemIDsSkipsNonObjectInput(t *testing.T) {
	for _, body := range []string{
		`{"input":"just text with fc_x inside"}`,
		`{"model":"gpt-6.1-sol"}`,
		`[{"type":"tool_search_call","id":"fc_x","call_id":"c1"}]`,
		`not json at all fc_`,
	} {
		out, changed, err := RepairInputItemIDs([]byte(body))
		if err != nil || changed || string(out) != body {
			t.Fatalf("body %q: changed=%v err=%v", body, changed, err)
		}
	}
}

func TestPrepareRepairsReplayedSearchCallOnNativeRoute(t *testing.T) {
	body := []byte(`{
		"model": "gpt-6.1-sol",
		"tools": [{"type": "tool_search", "execution": "client"}],
		"input": [
			{"type": "tool_search_call", "id": "` + poisonedSearchCallID + `", "call_id": "call_function_olq4gx3ow6cs_1",
			 "arguments": {"query": "x"}, "execution": "client"},
			{"type": "tool_search_output", "id": "` + poisonedSearchOutID + `", "call_id": "call_function_olq4gx3ow6cs_1",
			 "execution": "client", "tools": [{"type": "function", "name": "exec_command"}]},
			{"type": "function_call", "id": "fc_call_function_olq4gx3ow6cs_2", "call_id": "call_function_olq4gx3ow6cs_2",
			 "name": "exec_command", "arguments": "{}"}
		]
	}`)
	prepared, err := Prepare(body, RoutePolicy{ClientSearch: ClientSearchNative}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Attempt != nil {
		t.Fatalf("native routes must stay on the pass-through path")
	}
	if string(prepared.Body) == string(body) {
		t.Fatalf("replayed ids were forwarded unchanged")
	}
	value, ok := decodeValue(prepared.Body)
	if !ok {
		t.Fatalf("repaired body is not valid JSON")
	}
	root, _ := value.(map[string]any)
	input, _ := root["input"].([]any)
	want := []string{repairedSearchCallID, poisonedSearchOutID, "fc_call_function_olq4gx3ow6cs_2"}
	for index, expected := range want {
		item, _ := input[index].(map[string]any)
		if item["id"] != expected {
			t.Fatalf("input[%d].id = %v, want %q", index, item["id"], expected)
		}
	}
	// Repair moves ids only: the protocol items themselves stay native.
	call, _ := input[0].(map[string]any)
	if call["type"] != "tool_search_call" || call["execution"] != "client" {
		t.Fatalf("native tool_search items must pass through untouched: %v", call)
	}
}

func TestPrepareKeepsHealthyRequestByteIdentical(t *testing.T) {
	body := []byte(`{
		"tools": [{"type": "tool_search", "execution": "client"}],
		"input": [
			{"type": "tool_search_call", "id": "` + repairedSearchCallID + `", "call_id": "c1", "arguments": {}, "execution": "client"},
			{"type": "tool_search_output", "id": "` + healthySearchOutID + `", "call_id": "c1", "execution": "client", "tools": []}
		]
	}`)
	prepared, err := Prepare(body, RoutePolicy{ClientSearch: ClientSearchNative}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if prepared.Attempt != nil || string(prepared.Body) != string(body) {
		t.Fatalf("healthy request must be forwarded as-is")
	}
}

// Repair is not a search-bridge feature: a route that adapts custom tools must
// still get its replayed history fixed, and the adapted body must keep the
// migrated id.
func TestPrepareRepairsHistoryWhenCustomAdaptationEngages(t *testing.T) {
	body := []byte(`{
		"tools": [
			{"type": "tool_search", "execution": "client"},
			{"type": "custom", "name": "apply_patch", "description": "patch"}
		],
		"input": [
			{"type": "tool_search_call", "id": "` + poisonedSearchCallID + `", "call_id": "s1", "arguments": {"query": "x"}, "execution": "client"},
			{"type": "tool_search_output", "id": "` + poisonedSearchOutID + `", "call_id": "s1", "execution": "client",
			 "tools": [{"type": "function", "name": "exec_command"}]},
			{"type": "custom_tool_call", "id": "ctc_patch_1", "call_id": "p1", "name": "apply_patch", "input": "exact"}
		]
	}`)
	prepared, err := Prepare(body, RoutePolicy{ClientSearch: ClientSearchBridge, CustomTools: CustomToolsFunction},
		DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	value, ok := decodeValue(prepared.Body)
	if !ok {
		t.Fatalf("adapted body is not valid JSON")
	}
	root, _ := value.(map[string]any)
	input, _ := root["input"].([]any)
	search, _ := input[0].(map[string]any)
	if search["id"] != poisonedSearchCallID {
		t.Fatalf("the wire search call lost its function namespace: %v", search)
	}
	custom, _ := input[2].(map[string]any)
	if custom["id"] != "fc_patch_1" {
		t.Fatalf("custom history id = %v, want the function namespace", custom["id"])
	}
}

// The repaired id is what the client will replay next turn, so a body that is
// adapted anyway must not lose it.
func TestPrepareRepairsHistoryWhenSchemaAdaptationEngages(t *testing.T) {
	body := []byte(`{
		"tools": [{"type": "tool_search", "execution": "client",
			"parameters": {"type": "object", "properties": {"query": {"type": "string"}}}}],
		"input": [
			{"type": "tool_search_call", "id": "` + poisonedSearchCallID + `", "call_id": "s1", "arguments": {"query": "x"}, "execution": "client"},
			{"type": "tool_search_output", "id": "` + poisonedSearchOutID + `", "call_id": "s1", "execution": "client",
			 "tools": [{"type": "function", "name": "exec_command"}]}
		]
	}`)
	policy := RoutePolicy{ClientSearch: ClientSearchNative}
	policy.Schema.LocalRefs = LocalRefsInline
	prepared, err := Prepare(body, policy, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer prepared.Attempt.Close()
	if !strings.Contains(string(prepared.Body), repairedSearchCallID) {
		t.Fatalf("repaired id did not survive adaptation: %s", prepared.Body)
	}
}

// A migration that would collide with a later adapter conversion is refused
// before the request is sent, and the attempt lease is released with it.
func TestPrepareInputMigrationCollisionReleasesLease(t *testing.T) {
	limiter := NewLimiter(DefaultLimits())
	body := []byte(`{
		"tools": [
			{"type": "tool_search", "execution": "client"},
			{"type": "custom", "name": "apply_patch", "description": "patch"}
		],
		"input": [
			{"type": "tool_search_call", "id": "fc_shared", "call_id": "s1", "arguments": {"query": "x"}, "execution": "client"},
			{"type": "tool_search_output", "id": "` + poisonedSearchOutID + `", "call_id": "s1", "execution": "client",
			 "tools": [{"type": "function", "name": "exec_command"}]},
			{"type": "custom_tool_call", "id": "fc_shared", "call_id": "p1", "name": "apply_patch", "input": "exact"}
		]
	}`)
	_, err := Prepare(body, RoutePolicy{ClientSearch: ClientSearchBridge, CustomTools: CustomToolsFunction},
		DefaultLimits(), limiter)
	var compat *ToolCompatibilityError
	if !errors.As(err, &compat) || compat.Reason != ReasonAmbiguousIdentity {
		t.Fatalf("want an ambiguous identity rejection, got %v", err)
	}
	attempts, bytesUsed := limiter.Usage()
	if attempts != 0 || bytesUsed != 0 {
		t.Fatalf("lease leaked: attempts=%d bytes=%d", attempts, bytesUsed)
	}
}
