package responsestools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The ids below come from a real failing request: a bridged turn minted a
// tool_search_call that kept its upstream function_call id, the client stored
// it verbatim, and every later native-protocol turn was rejected with
// "Invalid 'input[28].id': ... Expected an ID that begins with 'tsc'".
const (
	poisonedSearchCallID = "fc_call_function_olq4gx3ow6cs_1"
	repairedSearchCallID = "tsc_call_function_olq4gx3ow6cs_1"
	poisonedSearchOutID  = "tso_01a0f0e8-db00-7053-9c6d-3626da9b1ea1"
	repairedSearchOutID  = "fco_01a0f0e8-db00-7053-9c6d-3626da9b1ea1"
	healthySearchOutID   = "tso_01a0f0e8-dd11-7113-8f5c-9a1d3a4e5b60"
)

func TestConvertedItemIDSupportedTransitions(t *testing.T) {
	cases := []struct {
		name     string
		fromType string
		toType   string
		id       string
		want     string
	}{
		{"function call to custom call", "function_call", "custom_tool_call", "fc_x", "ctc_x"},
		{"custom call to function call", "custom_tool_call", "function_call", "ctc_x", "fc_x"},
		{"function call to search call", "function_call", "tool_search_call", poisonedSearchCallID, repairedSearchCallID},
		{"search call to function call", "tool_search_call", "function_call", repairedSearchCallID, poisonedSearchCallID},
		{"function output to custom output", "function_call_output", "custom_tool_call_output", "fco_y", "ctco_y"},
		{"custom output to function output", "custom_tool_call_output", "function_call_output", "ctco_y", "fco_y"},
		{"function output to search output", "function_call_output", "tool_search_output", "fco_z", "tso_z"},
		{"search output to function output", "tool_search_output", "function_call_output", poisonedSearchOutID, repairedSearchOutID},
		{"same type keeps the id", "function_call", "function_call", "fc_x", "fc_x"},
		{"same type keeps an unknown id", "function_call", "function_call", "call_upstream_owned", "call_upstream_owned"},
		{"target prefix is idempotent", "function_call", "tool_search_call", repairedSearchCallID, repairedSearchCallID},
		{"empty id stays empty", "custom_tool_call", "function_call", "", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ConvertedItemID(testCase.fromType, testCase.toType, testCase.id)
			if err != nil {
				t.Fatalf("ConvertedItemID(%q, %q, %q): %v", testCase.fromType, testCase.toType, testCase.id, err)
			}
			if got != testCase.want {
				t.Fatalf("ConvertedItemID(%q, %q, %q) = %q, want %q",
					testCase.fromType, testCase.toType, testCase.id, got, testCase.want)
			}
		})
	}
}

func TestConvertedItemIDRejectsUnsupportedTransitions(t *testing.T) {
	cases := []struct {
		name     string
		fromType string
		toType   string
		id       string
	}{
		{"call to output", "function_call", "function_call_output", "fc_x"},
		{"search call from custom call", "custom_tool_call", "tool_search_call", "ctc_x"},
		{"custom call from search call", "tool_search_call", "custom_tool_call", "tsc_x"},
		{"non tool items never convert", "message", "reasoning", "msg_x"},
		{"unknown target has no namespace", "function_call", "computer_call", "fc_x"},
		{"id namespace contradicts the source type", "function_call", "custom_tool_call", "fco_x"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got, err := ConvertedItemID(testCase.fromType, testCase.toType, testCase.id); err == nil {
				t.Fatalf("ConvertedItemID(%q, %q, %q) = %q, want an error",
					testCase.fromType, testCase.toType, testCase.id, got)
			}
		})
	}
}

// Message and reasoning items keep whatever identity the upstream gave them.
// The proxy has no reason to re-identify them, and guessing would rewrite state
// it does not own.
func TestConvertedItemIDLeavesNonToolItemsAlone(t *testing.T) {
	for _, itemType := range []string{"message", "reasoning", "computer_call", ""} {
		if prefix := ItemIDPrefixForType(itemType); prefix != "" {
			t.Fatalf("ItemIDPrefixForType(%q) = %q, want no namespace", itemType, prefix)
		}
	}
}

func TestConvertedItemIDUnknownIsDeterministic(t *testing.T) {
	sum := sha256.Sum256([]byte("function_call\x00item_1"))
	want := "tsc_proxy_" + hex.EncodeToString(sum[:])
	got, err := ConvertedItemID("function_call", "tool_search_call", "item_1")
	if err != nil {
		t.Fatalf("ConvertedItemID: %v", err)
	}
	if got != want {
		t.Fatalf("ConvertedItemID = %q, want %q", got, want)
	}
	if len(got) != len("tsc_proxy_")+64 {
		t.Fatalf("digest was truncated: %q", got)
	}
	if again, err := ConvertedItemID("function_call", "tool_search_call", "item_1"); err != nil || again != want {
		t.Fatalf("digest is not stable: %q, %v", again, err)
	}
	// A minted id already carries the target namespace, so replaying it must
	// not hash it a second time.
	if stable, err := ConvertedItemID("function_call", "tool_search_call", got); err != nil || stable != want {
		t.Fatalf("minted id is not idempotent: %q, %v", stable, err)
	}
	// The digest covers the source type, so the same value under another source
	// identity is a different id.
	other, err := ConvertedItemID("custom_tool_call", "function_call", "item_1")
	if err != nil {
		t.Fatalf("ConvertedItemID: %v", err)
	}
	if other == want {
		t.Fatalf("digest ignored the source type")
	}
}

func TestReidentifyItemMovesTypeAndIDTogether(t *testing.T) {
	item := map[string]any{
		"type":     "custom_tool_call",
		"id":       "ctc_x",
		"call_id":  "call_1",
		"input":    "payload",
		"sequence": json.Number("3"),
	}
	changed, err := ReidentifyItem(item, "function_call")
	if err != nil || !changed {
		t.Fatalf("ReidentifyItem: changed=%v err=%v", changed, err)
	}
	if item["type"] != "function_call" || item["id"] != "fc_x" {
		t.Fatalf("item = %v", item)
	}
	if item["call_id"] != "call_1" || item["input"] != "payload" || item["sequence"] != json.Number("3") {
		t.Fatalf("unrelated fields changed: %v", item)
	}
}

// An input item may legitimately omit its optional id, and the conversion must
// not invent one.
func TestReidentifyItemDoesNotAddAMissingID(t *testing.T) {
	item := map[string]any{"type": "custom_tool_call_output", "call_id": "call_1"}
	changed, err := ReidentifyItem(item, "function_call_output")
	if err != nil || !changed {
		t.Fatalf("ReidentifyItem: changed=%v err=%v", changed, err)
	}
	if _, exists := item["id"]; exists {
		t.Fatalf("a missing id must stay missing: %v", item)
	}
}

func TestReidentifyItemDoesNotPartiallyMutateOnError(t *testing.T) {
	item := map[string]any{"type": "function_call", "id": "fc_x", "call_id": "call_1"}
	changed, err := ReidentifyItem(item, "message")
	if err == nil {
		t.Fatalf("expected an unsupported conversion to fail")
	}
	if changed {
		t.Fatalf("failed conversion reported a change")
	}
	if item["type"] != "function_call" || item["id"] != "fc_x" {
		t.Fatalf("item was partially rewritten: %v", item)
	}

	broken := map[string]any{"type": "function_call", "id": json.Number("7")}
	if _, err := ReidentifyItem(broken, "custom_tool_call"); err == nil {
		t.Fatalf("expected a non-string id to fail")
	}
	if broken["type"] != "function_call" {
		t.Fatalf("item was partially rewritten: %v", broken)
	}
	if changed, err := ReidentifyItem(nil, "function_call"); changed || err != nil {
		t.Fatalf("ReidentifyItem(nil) = %v, %v", changed, err)
	}
}

func TestItemIDRegistryRejectsDistinctOwners(t *testing.T) {
	var registry ItemIDRegistry
	first := ItemIDOwner{WireID: "fc_a", WireType: "function_call", CallID: "call_a"}
	if err := registry.Register("tsc_a", first); err != nil {
		t.Fatalf("register: %v", err)
	}
	// The added, done, and completed events of one item repeat the same owner.
	if err := registry.Register("tsc_a", first); err != nil {
		t.Fatalf("repeat registration: %v", err)
	}
	if err := registry.Register("tsc_a", ItemIDOwner{WireID: "fc_b", WireType: "function_call"}); !errors.Is(err, errItemIDConflict) {
		t.Fatalf("want a wire id conflict, got %v", err)
	}
	if err := registry.Register("tsc_a", ItemIDOwner{WireID: "fc_a", WireType: "custom_tool_call"}); !errors.Is(err, errItemIDConflict) {
		t.Fatalf("want a wire type conflict, got %v", err)
	}
	// A client id the upstream already owns is a conflict too: one id would
	// name two different items.
	if err := registry.Register("tsc_a", ItemIDOwner{WireID: "tsc_a", WireType: "tool_search_call"}); !errors.Is(err, errItemIDConflict) {
		t.Fatalf("want a native collision, got %v", err)
	}
	if err := registry.Register("tsc_b", ItemIDOwner{WireID: "fc_c", WireType: "function_call"}); err != nil {
		t.Fatalf("unrelated id: %v", err)
	}
	if err := registry.Register("", ItemIDOwner{WireID: "fc_d"}); err != nil {
		t.Fatalf("empty client id: %v", err)
	}
}

func TestItemIDRegistryAllowsLateCallID(t *testing.T) {
	var registry ItemIDRegistry
	if err := registry.Register("ctc_x", ItemIDOwner{WireID: "fc_x", WireType: "function_call"}); err != nil {
		t.Fatalf("register: %v", err)
	}
	// The added event carries no call_id yet; the terminal item supplies it.
	if err := registry.Register("ctc_x", ItemIDOwner{
		WireID: "fc_x", WireType: "function_call", CallID: "call_x",
	}); err != nil {
		t.Fatalf("late call id: %v", err)
	}
	if err := registry.Register("ctc_x", ItemIDOwner{WireID: "fc_x", WireType: "function_call"}); err != nil {
		t.Fatalf("repeat without call id: %v", err)
	}
	if err := registry.Register("ctc_x", ItemIDOwner{
		WireID: "fc_x", WireType: "function_call", CallID: "call_other",
	}); !errors.Is(err, errItemIDConflict) {
		t.Fatalf("want a call id conflict, got %v", err)
	}
	if registry.owners["ctc_x"].CallID != "call_x" {
		t.Fatalf("stored call id = %q", registry.owners["ctc_x"].CallID)
	}
}

func TestItemIDRegistryErrorNamesNoID(t *testing.T) {
	if strings.Contains(errItemIDConflict.Error(), "fc_") {
		t.Fatalf("conflict message leaks an id: %v", errItemIDConflict)
	}
}

func TestRewriteResponseItemMovesSearchIDIntoNamespace(t *testing.T) {
	item := map[string]any{
		"type":      "function_call",
		"id":        poisonedSearchCallID,
		"name":      "tool_search",
		"call_id":   "call_function_olq4gx3ow6cs_1",
		"arguments": `{"query":"x"}`,
	}
	if !rewriteResponseItem(item, bridgedSearchTestContract(), nil) {
		t.Fatalf("expected restore")
	}
	if err := ValidateToolSearchCall(item); err != nil {
		t.Fatalf("invalid restored shape: %v", err)
	}
	if item["id"] != repairedSearchCallID {
		t.Fatalf("id = %v, want %q", item["id"], repairedSearchCallID)
	}
}

// A bridged item that arrives without a usable id cannot be given one: any id
// the proxy invented would be an identity the client stores and replays.
func TestRewriteResponseItemRejectsBridgingAnItemWithoutID(t *testing.T) {
	for _, id := range []any{nil, "", json.Number("7")} {
		item := map[string]any{
			"type":      "function_call",
			"name":      "tool_search",
			"call_id":   "call_function_olq4gx3ow6cs_1",
			"arguments": `{"query":"x"}`,
		}
		if id != nil {
			item["id"] = id
		}
		_, err := rewriteResponseItemChecked(item, bridgedSearchTestContract(), nil)
		assertUpstreamContractError(t, err)
		if item["type"] != "function_call" {
			t.Fatalf("item was rewritten despite the failure: %v", item)
		}
	}
}

// Restoring a name must not turn an ordinary function call into a custom one.
// Doing so would hand the client a protocol the upstream never produced.
func TestRestoreFunctionIdentityRejectsUnbridgedKindChange(t *testing.T) {
	contract := NewToolContract()
	identity := ToolIdentity{Name: "run", Kind: ToolKindCustom}
	contract.IDByAlias["run"] = identity
	item := map[string]any{"type": "function_call", "id": "fc_x", "name": "run", "arguments": "{}"}
	_, err := RestoreFunctionCallIdentityChecked(item, contract)
	assertUpstreamContractError(t, err)
	if item["type"] != "function_call" || item["id"] != "fc_x" {
		t.Fatalf("item was rewritten despite the failure: %v", item)
	}
	// The same call is fine once the item already carries the custom type: name
	// restoration never changes the tool protocol category.
	item["type"] = "custom_tool_call"
	changed, err := RestoreFunctionCallIdentityChecked(item, contract)
	if err != nil || !changed {
		t.Fatalf("RestoreFunctionCallIdentityChecked: changed=%v err=%v", changed, err)
	}
	if item["type"] != "custom_tool_call" || item["id"] != "fc_x" {
		t.Fatalf("name restore changed the protocol identity: %v", item)
	}
}

func TestStreamSearchIDStaysConsistentAcrossEvents(t *testing.T) {
	feed := NewStreamFeed(bridgedSearchTestContract(), nil, &Lease{}, DefaultLimits())
	wireItem := map[string]any{
		"type": "function_call", "id": poisonedSearchCallID, "name": "tool_search",
		"call_id": "call_function_olq4gx3ow6cs_1", "output_index": json.Number("0"),
	}
	events := []map[string]any{
		{"type": eventOutputItemAdded, "output_index": json.Number("0"), "item": wireItem},
		{"type": eventFunctionCallArgsDelta, "item_id": poisonedSearchCallID, "output_index": json.Number("0"), "delta": `{"query":`},
		{"type": eventFunctionCallArgsDelta, "item_id": poisonedSearchCallID, "output_index": json.Number("0"), "delta": `"x"}`},
		{"type": eventFunctionCallArgsDone, "item_id": poisonedSearchCallID, "output_index": json.Number("0"), "arguments": `{"query":"x"}`},
		{"type": eventOutputItemDone, "output_index": json.Number("0"), "item": wireItem},
		{"type": eventResponseCompleted, "response": map[string]any{
			"id": "resp_1",
			"output": []any{map[string]any{
				"type": "function_call", "id": poisonedSearchCallID, "name": "tool_search",
				"call_id": "call_function_olq4gx3ow6cs_1", "arguments": `{"query":"x"}`,
			}},
		}},
	}
	seen := 0
	for _, event := range events {
		emitted, err := feed.Feed(frameJSON(t, event))
		if err != nil {
			t.Fatalf("feed %v: %v", event["type"], err)
		}
		for _, frame := range emitted {
			value, ok := decodeValue(frame)
			if !ok {
				t.Fatalf("emitted frame is not valid JSON: %s", frame)
			}
			payload, _ := value.(map[string]any)
			if payload == nil {
				continue
			}
			// Every client-visible reference to the bridged item must name the
			// same id, in the namespace tool_search_call requires.
			ids := map[string]string{}
			if item, okItem := payload["item"].(map[string]any); okItem {
				if id, isText := item["id"].(string); isText {
					ids["item.id"] = id
				}
			}
			if id, isText := payload["item_id"].(string); isText {
				ids["item_id"] = id
			}
			if response, okResponse := payload["response"].(map[string]any); okResponse {
				if output, isList := response["output"].([]any); isList {
					for index, rawItem := range output {
						item, isItem := rawItem.(map[string]any)
						if !isItem {
							continue
						}
						if id, isText := item["id"].(string); isText {
							ids[fmt.Sprintf("response.output[%d].id", index)] = id
						}
					}
				}
			}
			for pointer, id := range ids {
				if id != repairedSearchCallID {
					t.Fatalf("%s %s id = %q, want %q", payload["type"], pointer, id, repairedSearchCallID)
				}
				seen++
			}
		}
	}
	if _, err := feed.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if seen < 3 {
		t.Fatalf("expected the added, done, and completed frames to carry the id, saw %d", seen)
	}
}

func TestRewriteRequestMovesSearchIDsBackToWireNamespaces(t *testing.T) {
	root := rewriteTestRequest(t, `{
		"tools": [{"type": "tool_search"}],
		"input": [
			{"type": "tool_search_call", "id": "`+repairedSearchCallID+`", "call_id": "c1", "arguments": {"query": "x"}, "execution": "client"},
			{"type": "tool_search_output", "id": "`+poisonedSearchOutID+`", "call_id": "c1", "execution": "client",
			 "tools": [{"type": "function", "name": "exec_command"}]}
		]
	}`, RoutePolicy{ClientSearch: ClientSearchBridge})
	input, _ := root["input"].([]any)
	if len(input) != 2 {
		t.Fatalf("expected 2 items, got %d", len(input))
	}
	call, _ := input[0].(map[string]any)
	if call["type"] != "function_call" || call["id"] != poisonedSearchCallID {
		t.Fatalf("search call not restored for the wire: %v", call)
	}
	output, _ := input[1].(map[string]any)
	if output["type"] != "function_call_output" || output["id"] != repairedSearchOutID {
		t.Fatalf("search output not restored for the wire: %v", output)
	}
}

// A bridged request conversion must fail loudly on an id shape it cannot
// migrate instead of forwarding a half-converted item.
func TestRewriteRequestRejectsAnUnmigratableItemID(t *testing.T) {
	body := []byte(`{
		"tools": [{"type": "tool_search"}],
		"input": [
			{"type": "tool_search_call", "id": 7, "call_id": "c1", "arguments": {"query": "x"}, "execution": "client"},
			{"type": "tool_search_output", "id": "` + poisonedSearchOutID + `", "call_id": "c1", "execution": "client",
			 "tools": [{"type": "function", "name": "exec_command"}]}
		]
	}`)
	prepared, err := Prepare(body, RoutePolicy{ClientSearch: ClientSearchBridge}, DefaultLimits(), NewLimiter(DefaultLimits()))
	if err == nil {
		t.Fatalf("expected a non-string id to be rejected, got %s", prepared.Body)
	}
	var compat *ToolCompatibilityError
	if !errors.As(err, &compat) || compat.StatusCode() != 422 || compat.Reason != ReasonHistoryLink {
		t.Fatalf("want 422 history_link, got %v", err)
	}
}

func assertUpstreamContractError(t *testing.T, err error) {
	t.Helper()
	var compat *ToolCompatibilityError
	if !errors.As(err, &compat) || compat.StatusCode() != 502 || compat.Reason != ReasonUpstreamContract {
		t.Fatalf("want 502 upstream_contract, got %v", err)
	}
}
