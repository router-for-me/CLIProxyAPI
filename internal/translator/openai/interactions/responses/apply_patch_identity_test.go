package responses

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestInteractionsApplyPatchNamedLateIdentity(t *testing.T) {
	for _, first := range []string{"item", "call", "neither", "neither-item-first", "neither-call-first"} {
		for _, boundary := range []string{"stop", "terminal"} {
			t.Run(first+"-"+boundary, func(t *testing.T) {
				var param any
				step := map[string]any{"type": "function_call", "name": "functions__apply_patch"}
				if first == "item" {
					step["id"] = "item_2"
				}
				if first == "call" {
					step["call_id"] = "call_2"
				}
				if events := patchSend(&param, patchStep("step.start", step)); len(events) != 0 {
					t.Fatalf("published provisional IDs: %v", events)
				}
				args := patchArguments(patchText)
				for _, fragment := range []string{args[:15], args[15:]} {
					if events := patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": fragment}})); len(events) != 0 {
						t.Fatalf("published unresolved delta: %v", events)
					}
				}
				if strings.HasPrefix(first, "neither-") {
					if first == "neither-item-first" {
						step["id"] = "item_2"
					} else {
						step["call_id"] = "call_2"
					}
					if events := patchSend(&param, patchStep("step.start", step)); len(events) != 0 {
						t.Fatalf("one late ID is still unresolved: %v", events)
					}
				}
				step["id"], step["call_id"] = "item_2", "call_2"
				step["arguments"] = map[string]any{"input": patchText}
				var events []gjson.Result
				if boundary == "stop" {
					events = append(events, patchSend(&param, patchStep("step.stop", step))...)
				}
				step["index"] = 2
				events = append(events, patchSend(&param, patchJSON(map[string]any{"event_type": "interaction.completed", "interaction": map[string]any{"steps": []any{step}}}))...)
				assertPatchLifecycle(t, events, patchText)
				var fragments []string
				for _, event := range events {
					if event.Get("type").String() == "response.custom_tool_call_input.delta" {
						fragments = append(fragments, event.Get("delta").String())
					}
				}
				if len(fragments) != 2 || fragments[0] != patchText[:5] || fragments[1] != patchText[5:] {
					t.Fatalf("not the real buffered fragments: %q", fragments)
				}
			})
		}
	}
}

// The first supplied call ID is not a change to an upstream-established ID.
func TestInteractionsApplyPatchFirstLateIDAccepted(t *testing.T) {
	var param any
	patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "name": "functions__apply_patch"}))
	patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": `{"input":"p`}}))
	events := patchSend(&param, patchStep("step.stop", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
	if state := param.(*interactionsToResponsesStreamState); state.ToolInputError() != nil {
		t.Fatalf("first real ID rejected: %v events=%v", state.ToolInputError(), events)
	}
}

func TestInteractionsApplyPatchNamedLateIdentityEvidence(t *testing.T) {
	for _, tc := range []struct{ name, start, after string }{
		{"item-change", `{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"item_2","name":"functions__apply_patch"}}`, `{"event_type":"step.stop","index":2,"step":{"id":"changed","call_id":"call_2"}}`},
		{"call-change", `{"event_type":"step.start","index":2,"step":{"type":"function_call","call_id":"call_2","name":"functions__apply_patch"}}`, `{"event_type":"step.stop","index":2,"step":{"id":"item_2","call_id":"changed"}}`},
		{"repeated-start-type", `{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"item_2","name":"functions__apply_patch"}}`, `{"event_type":"step.start","index":2,"step":{"type":"model_output","id":"item_2"}}`},
		{"terminal-type", `{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"item_2","name":"functions__apply_patch"}}`, `{"event_type":"interaction.completed","steps":[{"index":2,"type":"model_output","id":"item_2"}]}`},
		{"partial-snapshot", `{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"item_2","name":"functions__apply_patch"}}`, `{"event_type":"step.stop","index":2,"step":{"arguments":"{\"input\":\"p"}}`},
		{"invalid-snapshot", `{"event_type":"step.start","index":2,"step":{"type":"function_call","call_id":"call_2","name":"functions__apply_patch"}}`, `{"event_type":"step.stop","index":2,"step":{"arguments":{"input":"p","extra":1}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var param any
			if events := patchSend(&param, []byte(tc.start)); len(events) != 0 {
				t.Fatalf("early output: %v", events)
			}
			events := patchSend(&param, []byte(tc.after))
			if len(events) != 1 || events[0].Get("type").String() != "response.failed" || param.(*interactionsToResponsesStreamState).ToolInputError() == nil {
				t.Fatalf("lost evidence: %v", events)
			}
			if events = patchSend(&param, []byte(`{"event_type":"interaction.completed"}`)); len(events) != 0 {
				t.Fatalf("failure reopened: %v", events)
			}
		})
	}
}

// Skipped/invalid snapshots must retain conflicting unmatched aliases as well as indexes.
func TestInteractionsApplyPatchUnresolvedAllKeys(t *testing.T) {
	for _, partial := range []bool{false, true} {
		for _, discover := range []string{"index", "step-index", "item", "call"} {
			t.Run(discover+map[bool]string{false: "-snapshot", true: "-partial"}[partial], func(t *testing.T) {
				var param any
				patchSend(&param, []byte(`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"a"}}`))
				raw := `{"event_type":"step.start","index":3,"step":{"index":4,"type":"model_output","id":"a","call_id":"late"}}`
				if partial {
					raw = raw[:len(raw)-1]
				}
				patchSend(&param, []byte(raw))
				root := map[string]any{"event_type": "step.start", "step": map[string]any{"type": "function_call", "name": "functions__apply_patch"}}
				step := root["step"].(map[string]any)
				switch discover {
				case "index":
					root["index"] = 3
				case "step-index":
					step["index"] = 4
				case "item":
					step["id"] = "a"
				case "call":
					step["call_id"] = "late"
				}
				events := patchSend(&param, patchJSON(root))
				if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
					t.Fatalf("alias erased evidence: %v", events)
				}
			})
		}
	}
}

func TestInteractionsApplyPatchTerminalIdentityFallback(t *testing.T) {
	for _, first := range []string{"item", "call", "neither"} {
		for _, terminal := range []string{"interaction.completed", "finish", "done"} {
			t.Run(first+"-"+terminal, func(t *testing.T) {
				var param any
				step := map[string]any{"type": "function_call", "name": "functions__apply_patch"}
				id := "item_2"
				if first == "item" {
					step["id"] = id
				}
				if first == "call" {
					id = "call_2"
					step["call_id"] = id
				}
				if events := patchSend(&param, patchStep("step.start", step)); len(events) != 0 {
					t.Fatalf("early fallback: %v", events)
				}
				if events := patchSend(&param, []byte(`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"input\":\"pq\"}"}}`)); len(events) != 0 {
					t.Fatalf("early delta: %v", events)
				}
				if events := patchSend(&param, patchStep("step.stop", nil)); len(events) != 0 {
					t.Fatalf("step stop is not a response terminal: %v", events)
				}
				events := patchSend(&param, patchJSON(map[string]any{"event_type": terminal}))
				counts := map[string]int{}
				for _, event := range events {
					kind := event.Get("type").String()
					identity, key := event, "item_id"
					switch kind {
					case "response.output_item.added", "response.output_item.done":
						identity, key = event.Get("item"), "id"
					case "response.completed":
						identity, key = event.Get("response.output.0"), "id"
					}
					counts[kind]++
					if identity.Get(key).String() != id || identity.Get("call_id").String() != id {
						t.Fatalf("fallback identity changed: %s", event.Raw)
					}
				}
				for _, kind := range []string{"response.output_item.added", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done", "response.output_item.done"} {
					if counts[kind] != 1 {
						t.Fatalf("fallback lifecycle: %v", counts)
					}
				}
				if events = patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "changed", "call_id": "changed", "name": "functions__apply_patch"})); len(events) != 0 {
					t.Fatalf("terminal identity reopened: %v", events)
				}
			})
		}
	}
}

func TestInteractionsApplyPatchNamedLateIdentityInterleaved(t *testing.T) {
	var param any
	for _, raw := range []string{
		`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"a2","name":"functions__apply_patch"}}`,
		`{"event_type":"step.start","index":3,"step":{"type":"function_call","call_id":"c3","name":"functions__apply_patch"}}`,
		`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"input\":\"two\"}"}}`,
		`{"event_type":"step.delta","index":3,"delta":{"type":"arguments_delta","arguments":"{\"input\":\"three\"}"}}`,
		`{"event_type":"step.stop","index":2}`,
		`{"event_type":"step.stop","index":3}`,
	} {
		if events := patchSend(&param, []byte(raw)); len(events) != 0 {
			t.Fatalf("early interleaved event: %v", events)
		}
	}
	events := patchSend(&param, []byte(`{"event_type":"interaction.completed","steps":[{"index":3,"type":"function_call","id":"a3","call_id":"c3","name":"functions__apply_patch","arguments":{"input":"three"}},{"index":2,"type":"function_call","id":"a2","call_id":"c2","name":"functions__apply_patch","arguments":{"input":"two"}}]}`))
	counts := map[int]map[string]int{}
	for _, event := range events {
		kind := event.Get("type").String()
		if kind == "response.completed" {
			for position, item := range event.Get("response.output").Array() {
				index := position + 2
				if item.Get("id").String() != map[int]string{2: "a2", 3: "a3"}[index] || item.Get("call_id").String() != map[int]string{2: "c2", 3: "c3"}[index] || item.Get("input").String() != map[int]string{2: "two", 3: "three"}[index] {
					t.Fatalf("final crossed calls: %s", event.Raw)
				}
			}
			continue
		}
		index := int(event.Get("output_index").Int())
		if counts[index] == nil {
			counts[index] = map[string]int{}
		}
		counts[index][kind]++
		identity, key := event, "item_id"
		if event.Get("item").Exists() {
			identity, key = event.Get("item"), "id"
		}
		if identity.Get(key).String() != map[int]string{2: "a2", 3: "a3"}[index] || identity.Get("call_id").String() != map[int]string{2: "c2", 3: "c3"}[index] {
			t.Fatalf("crossed identities: %s", event.Raw)
		}
		if kind == "response.custom_tool_call_input.delta" && event.Get("delta").String() != map[int]string{2: "two", 3: "three"}[index] {
			t.Fatalf("crossed fragments: %s", event.Raw)
		}
	}
	for _, index := range []int{2, 3} {
		for _, kind := range []string{"response.output_item.added", "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done", "response.output_item.done"} {
			if counts[index][kind] != 1 {
				t.Fatalf("interleaved lifecycle: %v", counts)
			}
		}
	}
}
