package responsestools

import (
	"encoding/json"
	"strings"
	"testing"
)

func streamTestFeed(t *testing.T) *StreamFeed {
	t.Helper()
	contract := ParseContract([]byte(`{
		"tools": [{"type": "tool_search"}],
		"input": [{"type": "tool_search_output", "call_id": "c0", "tools": [
			{"type": "custom", "name": "apply_patch", "description": "patch"}
		]}]
	}`))
	if contract == nil {
		t.Fatalf("no contract")
	}
	contract.SearchBridged = true
	bridge := BuildCustomBridge(map[string]any{
		"tools": []any{map[string]any{"type": "custom", "name": "apply_patch"}},
	}, CustomGrammarReject)
	customAlias, _ := bridge.Alias(ToolIdentity{Namespace: "", Name: "apply_patch", Kind: ToolKindCustom})
	_ = customAlias
	return NewStreamFeed(contract, bridge, &Lease{}, DefaultLimits())
}

func bridgedSearchTestContract() *ToolContract {
	contract := ParseContract([]byte(`{"tools":[{"type":"tool_search"}],"input":[]}`))
	if contract != nil {
		contract.SearchBridged = true
	}
	return contract
}

func frameJSON(t *testing.T, payload map[string]any) []byte {
	t.Helper()
	out, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func TestStreamCustomInterleaved(t *testing.T) {
	feed := streamTestFeed(t)
	bridge := feed.bridge
	alias, _ := bridge.Alias(ToolIdentity{Namespace: "", Name: "apply_patch", Kind: ToolKindCustom})
	events := []map[string]any{
		{"type": eventOutputItemAdded, "item": map[string]any{"type": "function_call", "id": "item_1", "name": alias, "call_id": "call_1", "output_index": 0}},
		{"type": eventFunctionCallArgsDelta, "item_id": "item_1", "output_index": 0, "call_id": "call_1", "delta": `{"input":"hel`},
		{"type": eventFunctionCallArgsDelta, "item_id": "item_1", "output_index": 0, "call_id": "call_1", "delta": `lo"}`},
		{"type": eventFunctionCallArgsDone, "item_id": "item_1", "output_index": 0, "call_id": "call_1", "arguments": `{"input":"hello"}`},
		{"type": eventOutputItemDone, "item": map[string]any{"type": "function_call", "id": "item_1", "name": alias, "call_id": "call_1", "output_index": 0, "arguments": `{"input":"hello"}`}},
	}
	var emitted [][]byte
	for _, event := range events {
		out, err := feed.Feed(frameJSON(t, event))
		if err != nil {
			t.Fatalf("feed: %v", err)
		}
		emitted = append(emitted, out...)
	}
	// added restores one custom frame; buffered deltas emit nothing; args.done
	// emits custom delta+done; item.done emits the restored custom item.
	if len(emitted) != 4 {
		t.Fatalf("expected 4 emitted frames, got %d: %s", len(emitted), emitted)
	}
	var added, delta, done, item map[string]any
	if err := json.Unmarshal(emitted[0], &added); err != nil {
		t.Fatalf("added: %v", err)
	}
	if err := json.Unmarshal(emitted[1], &delta); err != nil {
		t.Fatalf("delta: %v", err)
	}
	if err := json.Unmarshal(emitted[2], &done); err != nil {
		t.Fatalf("done: %v", err)
	}
	if err := json.Unmarshal(emitted[3], &item); err != nil {
		t.Fatalf("item: %v", err)
	}
	addedItem, _ := added["item"].(map[string]any)
	if addedItem["type"] != "custom_tool_call" || addedItem["input"] != "" {
		t.Fatalf("wrong added frame: %v", added)
	}
	if delta["type"] != eventCustomInputDelta || delta["delta"] != "hello" {
		t.Fatalf("wrong custom delta: %v", delta)
	}
	if done["type"] != eventCustomInputDone || done["input"] != "hello" {
		t.Fatalf("wrong custom done: %v", done)
	}
	restored, _ := item["item"].(map[string]any)
	if restored["type"] != "custom_tool_call" || restored["input"] != "hello" || restored["name"] != "apply_patch" {
		t.Fatalf("wrong restored item: %v", restored)
	}
}

func TestStreamSearchNoFabricatedDelta(t *testing.T) {
	feed := streamTestFeed(t)
	events := []map[string]any{
		{"type": eventOutputItemAdded, "item": map[string]any{"type": "function_call", "id": "item_s", "name": ToolSearchName, "call_id": "cs_1", "output_index": 1}},
		{"type": eventFunctionCallArgsDelta, "item_id": "item_s", "delta": `{"query":"x`},
		{"type": eventFunctionCallArgsDelta, "item_id": "item_s", "delta": `"}`},
		{"type": eventFunctionCallArgsDone, "item_id": "item_s", "arguments": `{"query":"x"}`},
		{"type": eventOutputItemDone, "item": map[string]any{"type": "function_call", "id": "item_s", "name": ToolSearchName, "call_id": "cs_1", "arguments": `{"query":"x"}`}},
	}
	var emitted [][]byte
	for _, event := range events {
		out, err := feed.Feed(frameJSON(t, event))
		if err != nil {
			t.Fatalf("feed: %v", err)
		}
		emitted = append(emitted, out...)
	}
	// added restores one search frame; deltas and args.done buffer silently;
	// item.done emits the restored search call. No fabricated arguments.delta.
	if len(emitted) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(emitted))
	}
	for _, frame := range emitted {
		if strings.Contains(string(frame), "tool_search_arguments") {
			t.Fatalf("fabricated search delta: %s", frame)
		}
	}
	var last map[string]any
	if err := json.Unmarshal(emitted[1], &last); err != nil {
		t.Fatalf("last: %v", err)
	}
	item, _ := last["item"].(map[string]any)
	if item["type"] != "tool_search_call" || item["execution"] != "client" {
		t.Fatalf("search not restored: %v", item)
	}
	if args, ok := item["arguments"].(map[string]any); !ok || args["query"] != "x" {
		t.Fatalf("search args wrong: %v", item)
	}
}

// Two bridged items are interleaved on purpose: with a single item a stream
// that derived the client identity from the already rewritten type would still
// look consistent.
func TestStreamBridgedIDsAcrossInterleavedEvents(t *testing.T) {
	feed := streamTestFeed(t)
	alias, _ := feed.bridge.Alias(ToolIdentity{Namespace: "", Name: "apply_patch", Kind: ToolKindCustom})
	events := []map[string]any{
		{"type": eventOutputItemAdded, "output_index": 0, "item": map[string]any{
			"type": "function_call", "id": "fc_s", "name": ToolSearchName, "call_id": "call_s", "output_index": 0}},
		{"type": eventOutputItemAdded, "output_index": 1, "item": map[string]any{
			"type": "function_call", "id": "fc_c", "name": alias, "call_id": "call_c", "output_index": 1}},
		{"type": eventFunctionCallArgsDelta, "item_id": "fc_s", "call_id": "call_s", "output_index": 0, "delta": `{"query":`},
		{"type": eventFunctionCallArgsDelta, "item_id": "fc_c", "call_id": "call_c", "output_index": 1, "delta": `{"input":"hel`},
		{"type": eventFunctionCallArgsDelta, "item_id": "fc_c", "call_id": "call_c", "output_index": 1, "delta": `lo"}`},
		{"type": eventFunctionCallArgsDelta, "item_id": "fc_s", "call_id": "call_s", "output_index": 0, "delta": `"x"}`},
		{"type": eventFunctionCallArgsDone, "item_id": "fc_c", "call_id": "call_c", "output_index": 1, "arguments": `{"input":"hello"}`},
		{"type": eventFunctionCallArgsDone, "item_id": "fc_s", "call_id": "call_s", "output_index": 0, "arguments": `{"query":"x"}`},
		{"type": eventOutputItemDone, "output_index": 1, "item": map[string]any{
			"type": "function_call", "id": "fc_c", "name": alias, "call_id": "call_c", "output_index": 1, "arguments": `{"input":"hello"}`}},
		{"type": eventOutputItemDone, "output_index": 0, "item": map[string]any{
			"type": "function_call", "id": "fc_s", "name": ToolSearchName, "call_id": "call_s", "output_index": 0, "arguments": `{"query":"x"}`}},
		{"type": eventResponseCompleted, "response": map[string]any{
			"id": "resp_1",
			"output": []any{
				map[string]any{"type": "function_call", "id": "fc_s", "name": ToolSearchName,
					"call_id": "call_s", "output_index": 0, "arguments": `{"query":"x"}`},
				map[string]any{"type": "function_call", "id": "fc_c", "name": alias,
					"call_id": "call_c", "output_index": 1, "arguments": `{"input":"hello"}`},
			},
		}},
	}
	want := map[string]streamIdentity{
		"tsc_s": {callID: "call_s", outputIndex: 0},
		"ctc_c": {callID: "call_c", outputIndex: 1},
	}
	seen := map[string]int{}
	sequences := make([]float64, 0, 8)
	for _, event := range events {
		emitted, err := feed.Feed(frameJSON(t, event))
		if err != nil {
			t.Fatalf("feed %v: %v", event["type"], err)
		}
		for _, frame := range emitted {
			payload := decodeStreamPayload(t, frame)
			if number, ok := payload["sequence_number"]; ok {
				sequences = append(sequences, number.(float64))
			}
			collectStreamItemIdentities(t, payload, want, seen)
		}
	}
	if _, err := feed.Finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	// The search item has no delta events of its own, so it is named three
	// times; the custom item is named by the added frame, its input delta and
	// done events, the terminal item, and the completed output.
	for clientID, minimum := range map[string]int{"tsc_s": 3, "ctc_c": 5} {
		if seen[clientID] < minimum {
			t.Fatalf("%s was referenced %d times, want at least %d: added, delta, done and completed must all agree",
				clientID, seen[clientID], minimum)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("expected exactly the two client identities, saw %v", seen)
	}
	for index := 1; index < len(sequences); index++ {
		if sequences[index] <= sequences[index-1] {
			t.Fatalf("sequence numbers are not increasing: %v", sequences)
		}
	}
	// The upstream ids stay the tracking keys, so the consistency checks keep
	// comparing what the upstream actually sent.
	if got := feed.SortedCallIDs(); len(got) != 2 || got[0] != "fc_c" || got[1] != "fc_s" {
		t.Fatalf("wire tracking keys = %v", got)
	}
}

type streamIdentity struct {
	callID      string
	outputIndex int
}

func decodeStreamPayload(t *testing.T, frame []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(frame, &payload); err != nil {
		t.Fatalf("emitted frame is not valid JSON: %s", frame)
	}
	return payload
}

// collectStreamItemIdentities records every client-visible reference to a
// tracked item in a frame, so the test asserts the whole frame rather than one
// field.
func collectStreamItemIdentities(t *testing.T, payload map[string]any, want map[string]streamIdentity, seen map[string]int) {
	t.Helper()
	check := func(id any, callID any, outputIndex any) {
		clientID, isText := id.(string)
		if !isText {
			return
		}
		expected, tracked := want[clientID]
		if !tracked {
			t.Fatalf("frame references unknown client id %q: %v", clientID, payload)
		}
		seen[clientID]++
		if callID != nil && callID != expected.callID {
			t.Fatalf("%s carried call_id %v, want %q", clientID, callID, expected.callID)
		}
		if outputIndex != nil {
			index, isNumber := outputIndex.(float64)
			if !isNumber || int(index) != expected.outputIndex {
				t.Fatalf("%s carried output_index %v, want %d", clientID, outputIndex, expected.outputIndex)
			}
		}
	}
	if item, ok := payload["item"].(map[string]any); ok {
		check(item["id"], item["call_id"], item["output_index"])
	}
	check(payload["item_id"], payload["call_id"], payload["output_index"])
	if response, ok := payload["response"].(map[string]any); ok {
		if output, isList := response["output"].([]any); isList {
			for _, rawItem := range output {
				if item, isItem := rawItem.(map[string]any); isItem {
					check(item["id"], item["call_id"], item["output_index"])
				}
			}
		}
	}
}

// Two different upstream items must not reach the client under one id.
func TestStreamRejectsClientIDCollision(t *testing.T) {
	feed := streamTestFeed(t)
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded, "output_index": 0,
		"item": map[string]any{"type": "function_call", "id": "fc_x", "name": ToolSearchName,
			"call_id": "call_1", "output_index": 0},
	})); err != nil {
		t.Fatalf("first item: %v", err)
	}
	emitted, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded, "output_index": 1,
		"item": map[string]any{"type": "function_call", "id": "tsc_x", "name": ToolSearchName,
			"call_id": "call_2", "output_index": 1},
	}))
	assertUpstreamContractError(t, err)
	if len(emitted) != 0 {
		t.Fatalf("a colliding item must not be announced: %s", emitted)
	}
	if len(feed.calls) != 1 {
		t.Fatalf("the rejected item was tracked anyway: %d", len(feed.calls))
	}
}

// A terminal response can complete an item the stream never announced. The
// completion path must reuse the same mapping rather than derive a second one.
func TestStreamCompletedOnlyUsesSameIDMapping(t *testing.T) {
	feed := streamTestFeed(t)
	emitted, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventResponseCompleted,
		"response": map[string]any{
			"id":   "resp_1",
			"type": "response",
			"output": []any{
				map[string]any{"type": "function_call", "id": "fc_s", "name": ToolSearchName,
					"call_id": "call_s", "arguments": `{"query":"x"}`},
			},
		},
	}))
	// The item was never tracked, so the terminal array is restored as an ordinary
	// passthrough instead of inventing a client identity for it.
	if err != nil {
		t.Fatalf("completed without a tracked item: %v", err)
	}
	if len(emitted) == 0 {
		t.Fatalf("expected the completed frame to be emitted")
	}
	if !strings.Contains(string(emitted[len(emitted)-1]), `"id":"tsc_s"`) {
		t.Fatalf("terminal search item was not migrated: %s", emitted[len(emitted)-1])
	}
}

// The client identity is retained state, so it is charged to the lease before
// the frame that announces it is emitted.
func TestStreamClientIDStateRespectsLease(t *testing.T) {
	limits := DefaultLimits()
	limiter := NewLimiter(limits)
	lease, err := limiter.Acquire(0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Close()
	feed := NewStreamFeed(bridgedSearchTestContract(), nil, lease, limits)
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": poisonedSearchCallID, "name": ToolSearchName},
	})); err != nil {
		t.Fatalf("added: %v", err)
	}
	tracked := feed.calls[poisonedSearchCallID]
	if tracked == nil || tracked.clientID != repairedSearchCallID {
		t.Fatalf("tracked = %+v", tracked)
	}
	if got := lease.Bytes(); got != trackedCallStateBytes(tracked) {
		t.Fatalf("lease holds %d bytes, want the full tracked state %d", got, trackedCallStateBytes(tracked))
	}
	lease.Close()
	if attempts, bytesUsed := limiter.Usage(); attempts != 0 || bytesUsed != 0 {
		t.Fatalf("close left attempts=%d bytes=%d", attempts, bytesUsed)
	}
}

// Without a custom bridge there is no evidence that a function call is really
// a custom call, so the proxy refuses instead of announcing one protocol and
// streaming another.
func TestStreamRejectsUnbridgedCustomIdentity(t *testing.T) {
	contract := NewToolContract()
	identity := ToolIdentity{Name: "apply_patch", Kind: ToolKindCustom}
	contract.IDByAlias[identity.Name] = identity
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	emitted, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": "fc_x", "name": "apply_patch", "arguments": "{}"},
	}))
	assertUpstreamContractError(t, err)
	if len(emitted) != 0 {
		t.Fatalf("a mixed protocol was announced: %s", emitted)
	}
}

func TestStreamRejectsTerminalItemIdentityChanges(t *testing.T) {
	for _, test := range []struct {
		name     string
		terminal func(string) map[string]any
	}{
		{
			name: "name",
			terminal: func(alias string) map[string]any {
				return map[string]any{"type": "function_call", "id": "item_1", "name": alias + "_changed", "call_id": "call_1", "arguments": `{"input":"ok"}`}
			},
		},
		{
			name: "type",
			terminal: func(alias string) map[string]any {
				return map[string]any{"type": "custom_tool_call", "id": "item_1", "name": alias, "call_id": "call_1", "arguments": `{"input":"ok"}`}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			feed := streamTestFeed(t)
			alias, _ := feed.bridge.Alias(ToolIdentity{Name: "apply_patch", Kind: ToolKindCustom})
			if _, err := feed.Feed(frameJSON(t, map[string]any{
				"type": eventOutputItemAdded,
				"item": map[string]any{"type": "function_call", "id": "item_1", "name": alias, "call_id": "call_1"},
			})); err != nil {
				t.Fatalf("add item: %v", err)
			}
			if _, err := feed.Feed(frameJSON(t, map[string]any{
				"type": eventOutputItemDone,
				"item": test.terminal(alias),
			})); err == nil {
				t.Fatal("terminal item changed its tracked name or type without rejection")
			}
		})
	}
}

func TestStreamCopiesTrackedOutputIndexToGeneratedItemDone(t *testing.T) {
	contract := bridgedSearchTestContract()
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "search_1", "output_index": 7},
	})); err != nil {
		t.Fatalf("add item: %v", err)
	}
	output, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemDone,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "search_1", "arguments": `{"query":"x"}`},
	}))
	if err != nil {
		t.Fatalf("complete item: %v", err)
	}
	if len(output) != 1 {
		t.Fatalf("output events = %d, want one: %s", len(output), output)
	}
	var event map[string]any
	if err := json.Unmarshal(output[0], &event); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got := event["output_index"]; got != float64(7) {
		t.Fatalf("output_index = %v, want 7", got)
	}
}

func TestStreamRestoresDiscoveredFunctionIdentity(t *testing.T) {
	contract := ParseContract([]byte(`{
		"tools": [{"type": "tool_search"}],
		"input": [{"type": "tool_search_output", "call_id": "search_1", "tools": [
			{"type": "function", "name": "read", "namespace": "fs", "parameters": {"type": "object"}}
		]}]
	}`))
	if contract == nil {
		t.Fatalf("no contract")
	}
	alias := contract.AliasByID[ToolIdentity{Namespace: "fs", Name: "read", Kind: ToolKindFunction}]
	if alias == "" {
		t.Fatalf("no discovered alias: %v", contract.AliasByID)
	}
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	added, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": alias, "call_id": "call_1", "arguments": ""},
	}))
	if err != nil {
		t.Fatalf("added: %v", err)
	}
	var addedEvent map[string]any
	if err := json.Unmarshal(added[0], &addedEvent); err != nil {
		t.Fatalf("decode added: %v", err)
	}
	addedItem, _ := addedEvent["item"].(map[string]any)
	if addedItem["name"] != "read" || addedItem["namespace"] != "fs" {
		t.Fatalf("added identity not restored: %v", addedItem)
	}

	done, err := feed.Feed(frameJSON(t, map[string]any{
		"type":         eventOutputItemDone,
		"output_index": 0,
		"item": map[string]any{
			"type": "function_call", "id": "item_1", "name": alias, "call_id": "call_1", "arguments": "{}",
		},
	}))
	if err != nil {
		t.Fatalf("done: %v", err)
	}
	var doneEvent map[string]any
	if err := json.Unmarshal(done[0], &doneEvent); err != nil {
		t.Fatalf("decode done: %v", err)
	}
	doneItem, _ := doneEvent["item"].(map[string]any)
	if doneItem["name"] != "read" || doneItem["namespace"] != "fs" {
		t.Fatalf("done identity not restored: %v", doneItem)
	}

	completed, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventResponseCompleted,
		"response": map[string]any{
			"id": "resp_1", "status": "completed",
			"output": []any{map[string]any{
				"type": "function_call", "id": "item_1", "name": alias, "call_id": "call_1", "arguments": "{}",
			}},
		},
	}))
	if err != nil {
		t.Fatalf("completed: %v", err)
	}
	var completedEvent map[string]any
	if err := json.Unmarshal(completed[0], &completedEvent); err != nil {
		t.Fatalf("decode completed: %v", err)
	}
	response, _ := completedEvent["response"].(map[string]any)
	output, _ := response["output"].([]any)
	completedItem, _ := output[0].(map[string]any)
	if completedItem["name"] != "read" || completedItem["namespace"] != "fs" {
		t.Fatalf("completed output identity not restored: %v", completedItem)
	}

	finalOnlyFeed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	finalOnly, err := finalOnlyFeed.Feed(frameJSON(t, map[string]any{
		"type": eventResponseCompleted,
		"response": map[string]any{
			"id": "resp_final_only", "status": "completed",
			"output": []any{map[string]any{
				"type": "function_call", "id": "item_final_only", "name": alias, "call_id": "call_final_only", "arguments": "{}",
			}},
		},
	}))
	if err != nil {
		t.Fatalf("final-only completed: %v", err)
	}
	var finalOnlyEvent map[string]any
	if err := json.Unmarshal(finalOnly[0], &finalOnlyEvent); err != nil {
		t.Fatalf("decode final-only completed: %v", err)
	}
	finalResponse, _ := finalOnlyEvent["response"].(map[string]any)
	finalOutput, _ := finalResponse["output"].([]any)
	finalItem, _ := finalOutput[0].(map[string]any)
	if finalItem["name"] != "read" || finalItem["namespace"] != "fs" {
		t.Fatalf("final-only output identity not restored: %v", finalItem)
	}
}

func TestStreamRestoresEventPrefixedSSEFrame(t *testing.T) {
	contract := ParseContract([]byte(`{
		"tools": [{"type": "tool_search"}],
		"input": [{"type": "tool_search_output", "call_id": "search_1", "tools": [
			{"type": "function", "name": "read", "namespace": "fs", "parameters": {"type": "object"}}
		]}]
	}`))
	if contract == nil {
		t.Fatalf("no contract")
	}
	alias := contract.AliasByID[ToolIdentity{Namespace: "fs", Name: "read", Kind: ToolKindFunction}]
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())

	output, err := feed.Feed([]byte("event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"name\":\"" + alias + "\",\"call_id\":\"call_1\",\"arguments\":\"\"}}\r\n\r\n"))
	if err != nil {
		t.Fatalf("feed event-prefixed frame: %v", err)
	}
	if len(output) != 1 {
		t.Fatalf("output events = %d, want 1", len(output))
	}
	var event map[string]any
	if err := json.Unmarshal(output[0], &event); err != nil {
		t.Fatalf("decode output event %q: %v", output[0], err)
	}
	item, _ := event["item"].(map[string]any)
	if item["name"] != "read" || item["namespace"] != "fs" {
		t.Fatalf("event-prefixed function identity not restored: %v", item)
	}
}

func TestStreamRejectsEventTypeMismatch(t *testing.T) {
	feed := streamTestFeed(t)
	_, err := feed.Feed([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"name\":\"tool_search\",\"call_id\":\"call_1\",\"arguments\":\"{}\"}}\n\n"))
	if err == nil {
		t.Fatal("expected mismatched SSE event and JSON type to fail")
	}
	if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != 502 {
		t.Fatalf("expected typed 502, got %v", err)
	}
}

func TestStreamRejectsArgumentsDoneThatConflictWithDeltas(t *testing.T) {
	contract := bridgedSearchTestContract()
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	added := map[string]any{
		"type":         eventOutputItemAdded,
		"output_index": 0,
		"item":         map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "call_1", "arguments": ""},
	}
	if _, err := feed.Feed(frameJSON(t, added)); err != nil {
		t.Fatalf("added: %v", err)
	}
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventFunctionCallArgsDelta, "item_id": "item_1", "output_index": 0, "delta": `{"query":"first"}`,
	})); err != nil {
		t.Fatalf("delta: %v", err)
	}
	_, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventFunctionCallArgsDone, "item_id": "item_1", "output_index": 0, "arguments": `{"query":"different"}`,
	}))
	if err == nil {
		t.Fatal("conflicting arguments.done was accepted")
	}
}

func TestStreamAcceptsCompleteItemAfterPartialArgumentDeltas(t *testing.T) {
	contract := bridgedSearchTestContract()
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded, "output_index": 4,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "call_1", "arguments": ""},
	})); err != nil {
		t.Fatalf("added: %v", err)
	}
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventFunctionCallArgsDelta, "item_id": "item_1", "output_index": 4, "delta": `{"query":`,
	})); err != nil {
		t.Fatalf("delta: %v", err)
	}
	output, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemDone, "output_index": 4,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "call_1", "arguments": `{"query":"x"}`},
	}))
	if err != nil {
		t.Fatalf("item.done should complete a partial delta sequence: %v", err)
	}
	if len(output) != 1 {
		t.Fatalf("output events = %d, want one completed call", len(output))
	}
}

func TestStreamRejectsCompletedArgumentsThatConflictWithDoneItem(t *testing.T) {
	contract := bridgedSearchTestContract()
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded, "output_index": 0,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "call_1", "arguments": ""},
	})); err != nil {
		t.Fatalf("added: %v", err)
	}
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventFunctionCallArgsDone, "item_id": "item_1", "output_index": 0, "arguments": `{"query":"first"}`,
	})); err != nil {
		t.Fatalf("arguments.done: %v", err)
	}
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemDone, "output_index": 0,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "call_1", "arguments": `{"query":"first"}`},
	})); err != nil {
		t.Fatalf("item.done: %v", err)
	}
	_, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventResponseCompleted,
		"response": map[string]any{
			"output": []any{map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "call_1", "arguments": `{"query":"second"}`}},
		},
	}))
	if err == nil {
		t.Fatal("response.completed with conflicting arguments was accepted")
	}
}

func TestStreamRejectsCompletedArgumentsConflictBeforeItemDone(t *testing.T) {
	contract := bridgedSearchTestContract()
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "call_1", "arguments": ""},
	})); err != nil {
		t.Fatalf("added: %v", err)
	}
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventFunctionCallArgsDone, "item_id": "item_1", "arguments": `{"query":"first"}`,
	})); err != nil {
		t.Fatalf("arguments.done: %v", err)
	}
	_, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventResponseCompleted,
		"response": map[string]any{
			"output": []any{map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "call_1", "arguments": `{"query":"second"}`}},
		},
	}))
	if err == nil {
		t.Fatal("response.completed changed arguments before output_item.done")
	}
}

func TestStreamCustomEventsCarryOutputIndexAndSequenceNumbers(t *testing.T) {
	contract := ParseContract([]byte(`{"tools":[{"type":"custom","name":"apply_patch"}],"input":[]}`))
	if contract == nil {
		t.Fatal("no custom contract")
	}
	bridge := BuildCustomBridge(map[string]any{
		"tools": []any{map[string]any{"type": "custom", "name": "apply_patch"}},
	}, CustomGrammarReject)
	alias, ok := bridge.Alias(ToolIdentity{Name: "apply_patch", Kind: ToolKindCustom})
	if !ok {
		t.Fatal("custom alias missing")
	}
	feed := NewStreamFeed(contract, bridge, &Lease{}, DefaultLimits())
	added, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded, "output_index": 7,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": alias, "call_id": "call_1", "arguments": ""},
	}))
	if err != nil {
		t.Fatalf("added: %v", err)
	}
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventFunctionCallArgsDelta, "item_id": "item_1", "output_index": 7, "delta": `{"input":"hello"}`,
	})); err != nil {
		t.Fatalf("delta: %v", err)
	}
	customOutput, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventFunctionCallArgsDone, "item_id": "item_1", "output_index": 7, "arguments": `{"input":"hello"}`,
	}))
	if err != nil {
		t.Fatalf("arguments.done: %v", err)
	}
	output := append(added, customOutput...)
	if len(output) != 3 {
		t.Fatalf("custom events = %d, want added, delta, and done", len(output))
	}
	for index, raw := range output {
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		if event["output_index"] != float64(7) {
			t.Fatalf("event %d output_index = %v, want 7", index, event["output_index"])
		}
		if event["sequence_number"] != float64(index) {
			t.Fatalf("event %d sequence_number = %v, want %d", index, event["sequence_number"], index)
		}
	}
}

func TestStreamMalformedCustomArgumentsAreUpstreamErrors(t *testing.T) {
	body := map[string]any{"tools": []any{map[string]any{"type": "custom", "name": "apply_patch"}}, "input": []any{}}
	contract := ParseContract(frameJSON(t, body))
	bridge := BuildCustomBridge(body, CustomGrammarReject)
	alias, ok := bridge.Alias(ToolIdentity{Name: "apply_patch", Kind: ToolKindCustom})
	if !ok {
		t.Fatal("custom alias missing")
	}
	feed := NewStreamFeed(contract, bridge, &Lease{}, DefaultLimits())
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": "item_1", "name": alias, "call_id": "call_1"},
	})); err != nil {
		t.Fatalf("added: %v", err)
	}
	_, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventFunctionCallArgsDone, "item_id": "item_1", "arguments": `{"input":1}`,
	}))
	if err == nil {
		t.Fatal("malformed custom function arguments were accepted")
	}
	if status, ok := err.(interface{ StatusCode() int }); !ok || status.StatusCode() != 502 {
		t.Fatalf("status = %v, want 502: %v", status, err)
	}
}

func TestStreamCombinesEventAndDataSplitAcrossChunks(t *testing.T) {
	contract := ParseContract([]byte(`{
		"tools":[{"type":"tool_search"}],
		"input":[{"type":"tool_search_output","call_id":"search_1","tools":[
			{"type":"function","name":"read","namespace":"fs","parameters":{"type":"object"}}
		]}]
	}`))
	if contract == nil {
		t.Fatal("no contract")
	}
	alias := contract.AliasByID[ToolIdentity{Namespace: "fs", Name: "read", Kind: ToolKindFunction}]
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	first, err := feed.Feed([]byte("event: response.output_item.added\r\n"))
	if err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if len(first) != 0 {
		t.Fatalf("event-only prefix emitted before data: %q", first)
	}
	second, err := feed.Feed([]byte("data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"name\":\"" + alias + "\",\"call_id\":\"call_1\",\"arguments\":\"\"}}\r\n\r\n"))
	if err != nil {
		t.Fatalf("second chunk: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("output events = %d, want 1", len(second))
	}
	var event map[string]any
	if err := json.Unmarshal(second[0], &event); err != nil {
		t.Fatalf("decode output event: %v", err)
	}
	item, _ := event["item"].(map[string]any)
	if item["name"] != "read" || item["namespace"] != "fs" {
		t.Fatalf("split SSE event identity not restored: %v", item)
	}
}

func TestStreamParsesEventPrefixedSSEOneByteAtATime(t *testing.T) {
	contract := ParseContract([]byte(`{
		"tools":[{"type":"tool_search"}],
		"input":[{"type":"tool_search_output","call_id":"search_1","tools":[
			{"type":"function","name":"read","namespace":"fs","parameters":{"type":"object"}}
		]}]
	}`))
	if contract == nil {
		t.Fatal("no contract")
	}
	alias := contract.AliasByID[ToolIdentity{Namespace: "fs", Name: "read", Kind: ToolKindFunction}]
	feed := NewStreamFeed(contract, nil, &Lease{}, DefaultLimits())
	frame := []byte("event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\"output_index\":2,\"item\":{\"type\":\"function_call\",\"id\":\"item_1\",\"name\":\"" + alias + "\",\"call_id\":\"call_1\",\"arguments\":\"\"}}\r\n\r\n")
	var output [][]byte
	for _, value := range frame {
		events, err := feed.Feed([]byte{value})
		if err != nil {
			t.Fatalf("feed byte %q: %v", value, err)
		}
		output = append(output, events...)
	}
	if len(output) != 1 {
		t.Fatalf("output events = %d, want 1", len(output))
	}
	var event map[string]any
	if err := json.Unmarshal(output[0], &event); err != nil {
		t.Fatalf("decode output event: %v", err)
	}
	item, _ := event["item"].(map[string]any)
	if item["name"] != "read" || item["namespace"] != "fs" {
		t.Fatalf("byte-split SSE identity not restored: %v", item)
	}
}

func TestStreamFinishRejectsIncompleteSSEFrame(t *testing.T) {
	feed := streamTestFeed(t)
	if output, err := feed.Feed([]byte("event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\"")); err != nil {
		t.Fatalf("feed partial frame: %v", err)
	} else if len(output) != 0 {
		t.Fatalf("partial frame emitted early: %q", output)
	}
	if _, err := feed.Finish(); err == nil {
		t.Fatal("incomplete SSE frame at EOF was silently accepted")
	}
}

func TestStreamParsesCommentAndDataInSameSSEFrame(t *testing.T) {
	feed := streamTestFeed(t)
	out, err := feed.Feed([]byte(": heartbeat\nevent: response.created\ndata: {\"type\":\"response.created\"}\n\n"))
	if err != nil {
		t.Fatalf("feed SSE frame: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("output events = %d, want 1: %q", len(out), out)
	}
	var event map[string]any
	if err := json.Unmarshal(out[0], &event); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if event["type"] != "response.created" || event["sequence_number"] != float64(0) {
		t.Fatalf("comment prevented the data event from being processed: %v", event)
	}
}

func TestStreamAcceptsTerminalCRLFSplitAfterCarriageReturn(t *testing.T) {
	feed := streamTestFeed(t)
	frame := "event: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\r\n\r\n"
	var out [][]byte
	for index := range frame {
		events, err := feed.Feed([]byte{frame[index]})
		if err != nil {
			t.Fatalf("feed byte %d (%q): %v", index, frame[index], err)
		}
		out = append(out, events...)
	}
	if len(out) != 1 {
		t.Fatalf("output events = %d, want 1: %q", len(out), out)
	}
	var event map[string]any
	if err := json.Unmarshal(out[0], &event); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if event["type"] != "response.completed" || event["sequence_number"] != float64(0) {
		t.Fatalf("terminal CRLF frame was not normalized: %v", event)
	}
}

func TestStreamOrdinaryArgumentsDoneUsesUnifiedSequence(t *testing.T) {
	feed := streamTestFeed(t)
	added, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": "ordinary_1", "name": "ordinary", "call_id": "call_1"},
	}))
	if err != nil {
		t.Fatalf("item added: %v", err)
	}
	done, err := feed.Feed(frameJSON(t, map[string]any{
		"type":            eventFunctionCallArgsDone,
		"item_id":         "ordinary_1",
		"arguments":       `{"value":"x"}`,
		"sequence_number": 900,
	}))
	if err != nil {
		t.Fatalf("arguments done: %v", err)
	}
	if len(added) != 1 || len(done) != 1 {
		t.Fatalf("added/done events = %d/%d, want 1/1", len(added), len(done))
	}
	for index, raw := range [][]byte{added[0], done[0]} {
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		if event["sequence_number"] != float64(index) {
			t.Fatalf("event %d sequence_number = %v, want %d", index, event["sequence_number"], index)
		}
	}
}

func TestStreamCustomItemDoneEmitsBufferedInputEvents(t *testing.T) {
	feed := streamTestFeed(t)
	alias, ok := feed.bridge.Alias(ToolIdentity{Name: "apply_patch", Kind: ToolKindCustom})
	if !ok {
		t.Fatal("custom alias missing")
	}
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": "custom_1", "name": alias, "call_id": "call_1"},
	})); err != nil {
		t.Fatalf("item added: %v", err)
	}
	if _, err := feed.Feed(frameJSON(t, map[string]any{
		"type":    eventFunctionCallArgsDelta,
		"item_id": "custom_1",
		"delta":   `{"input":"patch"}`,
	})); err != nil {
		t.Fatalf("arguments delta: %v", err)
	}
	out, err := feed.Feed(frameJSON(t, map[string]any{
		"type": eventOutputItemDone,
		"item": map[string]any{
			"type": "function_call", "id": "custom_1", "name": alias, "call_id": "call_1",
		},
	}))
	if err != nil {
		t.Fatalf("item done: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("item.done output events = %d, want custom input.delta, input.done, and item.done: %q", len(out), out)
	}
	for index, raw := range out {
		var event map[string]any
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode event %d: %v", index, err)
		}
		if event["sequence_number"] != float64(index+1) {
			t.Fatalf("event %d sequence_number = %v, want %d", index, event["sequence_number"], index+1)
		}
	}
}

func TestStreamDoesNotEmitCompletedBeforeTrailingSSEValidation(t *testing.T) {
	feed := streamTestFeed(t)
	chunk := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"output\":[]}}\n\ndata: {\"type\":")
	output, err := feed.Feed(chunk)
	if err == nil {
		t.Fatal("trailing incomplete SSE data after completion was accepted")
	}
	if len(output) != 0 {
		t.Fatalf("completed event escaped before trailing frame validation: %q", output)
	}
}

func TestStreamOrdinaryPassesThrough(t *testing.T) {
	feed := streamTestFeed(t)
	event := map[string]any{"type": eventFunctionCallArgsDelta, "item_id": "unknown", "delta": "hi"}
	out, err := feed.Feed(frameJSON(t, event))
	if err != nil {
		t.Fatalf("feed: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("ordinary frame must pass through")
	}
}

func TestStreamItemIDMismatchRejected(t *testing.T) {
	feed := streamTestFeed(t)
	added := map[string]any{"type": eventOutputItemAdded, "item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "a", "output_index": 0}}
	if _, err := feed.Feed(frameJSON(t, added)); err != nil {
		t.Fatalf("added: %v", err)
	}
	done := map[string]any{"type": eventOutputItemDone, "item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "different", "output_index": 0, "arguments": `{"query":"x"}`}}
	if _, err := feed.Feed(frameJSON(t, done)); err == nil {
		t.Fatalf("expected call_id mismatch rejection")
	}
}

func TestStreamEOFWithUnfinishedRejected(t *testing.T) {
	feed := streamTestFeed(t)
	added := map[string]any{"type": eventOutputItemAdded, "item": map[string]any{"type": "function_call", "id": "item_1", "name": ToolSearchName, "call_id": "a"}}
	if _, err := feed.Feed(frameJSON(t, added)); err != nil {
		t.Fatalf("added: %v", err)
	}
	if _, err := feed.Finish(); err == nil {
		t.Fatalf("expected EOF rejection for unfinished call")
	}
}

func TestStreamFailedIsTerminalNotExecutable(t *testing.T) {
	feed := streamTestFeed(t)
	failed := map[string]any{"type": eventResponseFailed, "response": map[string]any{"status": "failed"}}
	out, err := feed.Feed(frameJSON(t, failed))
	if err != nil {
		t.Fatalf("failed: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("failed frame must pass through once")
	}
	if _, err := feed.Finish(); err != nil {
		t.Fatalf("finish after failed: %v", err)
	}
}

func TestStreamFeedChargesTrackedItemStateBeforeInsert(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxAttemptBytes = 2 * (trackedCallStateOverhead + len("item_a") + len("ordinary"))
	limiter := NewLimiter(limits)
	lease, err := limiter.Acquire(0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Close()
	feed := NewStreamFeed(nil, nil, lease, limits)

	add := func(id string) error {
		_, errAdd := feed.Feed(frameJSON(t, map[string]any{
			"type": eventOutputItemAdded,
			"item": map[string]any{"type": "function_call", "id": id, "name": "ordinary"},
		}))
		return errAdd
	}
	if err := add("item_a"); err != nil {
		t.Fatalf("first item: %v", err)
	}
	reserved := lease.Bytes()
	if reserved == 0 {
		t.Fatal("first tracked item did not reserve state bytes")
	}
	err = add("item_b")
	if err == nil {
		t.Fatal("second tracked item exceeded the per-attempt state budget without an error")
	}
	if len(feed.calls) != 1 {
		t.Fatalf("tracked item count = %d, want the rejected item to remain untracked", len(feed.calls))
	}
	if lease.Bytes() != reserved {
		t.Fatalf("rejected item changed lease bytes from %d to %d", reserved, lease.Bytes())
	}
}

func TestStreamFeedChargesLateCallIDBeforeRetainingIt(t *testing.T) {
	contract := bridgedSearchTestContract()
	added := frameJSON(t, map[string]any{
		"type": eventOutputItemAdded,
		"item": map[string]any{"type": "function_call", "id": "item_x", "name": ToolSearchName},
	})
	// Measure the retained state of one tracked item first, so the budget
	// below leaves room for exactly that item and nothing more: the late
	// call_id is what must be refused.
	probeLimits := DefaultLimits()
	probeLease, err := NewLimiter(probeLimits).Acquire(0)
	if err != nil {
		t.Fatalf("probe acquire: %v", err)
	}
	defer probeLease.Close()
	probe := NewStreamFeed(contract, nil, probeLease, probeLimits)
	if _, err := probe.Feed(added); err != nil {
		t.Fatalf("probe added: %v", err)
	}

	limits := DefaultLimits()
	limits.MaxAttemptBytes = trackedCallStateBytes(probe.calls["item_x"]) + 1
	limiter := NewLimiter(limits)
	lease, err := limiter.Acquire(0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer lease.Close()
	feed := NewStreamFeed(contract, nil, lease, limits)
	if _, err := feed.Feed(added); err != nil {
		t.Fatalf("added: %v", err)
	}
	reserved := lease.Bytes()
	err = func() error {
		_, errDelta := feed.Feed(frameJSON(t, map[string]any{
			"type": eventFunctionCallArgsDelta, "item_id": "item_x", "call_id": "c1", "delta": "",
		}))
		return errDelta
	}()
	if err == nil {
		t.Fatal("late call_id exceeded the per-attempt state budget without an error")
	}
	if got := feed.calls["item_x"].callID; got != "" {
		t.Fatalf("rejected call_id was retained: %q", got)
	}
	if lease.Bytes() != reserved {
		t.Fatalf("rejected call_id changed lease bytes from %d to %d", reserved, lease.Bytes())
	}
}
