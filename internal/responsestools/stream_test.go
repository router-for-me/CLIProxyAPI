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
	bridge := BuildCustomBridge(map[string]any{
		"tools": []any{map[string]any{"type": "custom", "name": "apply_patch"}},
	}, CustomGrammarReject)
	customAlias, _ := bridge.Alias(ToolIdentity{Namespace: "", Name: "apply_patch", Kind: ToolKindCustom})
	_ = customAlias
	return NewStreamFeed(contract, bridge, &Lease{}, DefaultLimits())
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
