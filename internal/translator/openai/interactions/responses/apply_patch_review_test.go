package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestInteractionsApplyPatchOrdinaryFunctionStopSnapshotDeltas(t *testing.T) {
	requests := []struct {
		name, request, upstream string
	}{
		{"ordinary function", `{"tools":[{"type":"function","name":"lookup"}]}`, "lookup"},
		{"ordinary function with patch bridge", `{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}`, "lookup"},
		{"function winner named apply_patch", `{"tools":[{"type":"function","name":"apply_patch"}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "apply_patch"},
	}
	for _, request := range requests {
		for _, identityAt := range []string{"start", "after delta", "stop", "delta before start"} {
			t.Run(request.name+"/"+identityAt, func(t *testing.T) {
				var param any
				var events []gjson.Result
				send := func(raw []byte) []gjson.Result {
					more := patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", []byte(request.request), nil, raw, &param))
					events = append(events, more...)
					return more
				}
				step := map[string]any{"type": "function_call", "arguments": map[string]any{}}
				identity := func() {
					step["id"], step["call_id"], step["name"] = "item_2", "call_2", request.upstream
				}
				if identityAt == "start" {
					identity()
				}
				if identityAt != "delta before start" {
					send(patchStep("step.start", step))
				}
				args := `{"x":1}`
				deltas := send(patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": args}}))
				if identityAt != "start" && len(deltas) != 0 {
					t.Fatalf("arguments emitted before announcement: %v", deltas)
				}
				identity()
				if identityAt == "after delta" || identityAt == "delta before start" {
					send(patchStep("step.start", step))
				}
				step["arguments"] = map[string]any{"x": 1}
				send(patchStep("step.stop", step))
				if more := send(patchStep("step.stop", step)); len(more) != 0 {
					t.Fatalf("repeated stop replayed arguments: %v", more)
				}
				send([]byte(`{"event_type":"interaction.completed"}`))
				var delta strings.Builder
				counts := map[string]int{}
				for _, event := range events {
					kind := event.Get("type").String()
					counts[kind]++
					switch kind {
					case "response.function_call_arguments.delta":
						if counts["response.output_item.added"] != 1 || event.Get("item_id").String() != "item_2" {
							t.Fatalf("delta precedes real announcement: %s", event.Raw)
						}
						delta.WriteString(event.Get("delta").String())
					case "response.function_call_arguments.done":
						if event.Get("arguments").String() != args {
							t.Fatalf("done arguments changed: %s", event.Raw)
						}
					case "response.output_item.done":
						if event.Get("item.arguments").String() != args || event.Get("item.type").String() != "function_call" || event.Get("item.name").String() != request.upstream {
							t.Fatalf("ordinary function changed: %s", event.Raw)
						}
					case "response.completed":
						if event.Get("response.output.0.arguments").String() != args {
							t.Fatalf("final arguments changed: %s", event.Raw)
						}
					}
				}
				if delta.String() != args || counts["response.function_call_arguments.delta"] != 1 || counts["response.function_call_arguments.done"] != 1 || counts["response.output_item.done"] != 1 || counts["response.completed"] != 1 || counts["response.failed"] != 0 {
					t.Fatalf("arguments replayed or lost: delta=%q counts=%v", delta.String(), counts)
				}
			})
		}
	}
}

func assertInteractionsPatchReviewFailure(t *testing.T, param *any, events []gjson.Result) {
	t.Helper()
	if len(events) != 1 || events[0].Get("type").String() != "response.failed" || events[0].Get("response.error.code").String() != "invalid_tool_arguments" {
		t.Fatalf("expected only sanitized failure: %v", events)
	}
	if strings.Contains(events[0].Raw, "secret") || strings.Contains(events[0].Raw, `\"input\"`) {
		t.Fatalf("raw arguments leaked: %s", events[0].Raw)
	}
	if state, ok := (*param).(interface{ ToolInputError() error }); !ok || state.ToolInputError() == nil {
		t.Fatal("missing tool input error")
	}
	for _, raw := range [][]byte{[]byte(`{"event_type":"interaction.completed"}`), []byte(`{"event_type":"interaction.failed"}`), []byte(`[DONE]`), patchStep("step.stop", nil)} {
		if more := patchSend(param, raw); len(more) != 0 {
			t.Fatalf("failed response reopened: %v", more)
		}
	}
}

func TestInteractionsApplyPatchCompletedItemStopAndFinalTypeConflicts(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
		for _, key := range []string{"index", "id", "call_id"} {
			for _, itemType := range []string{"model_output", "thought", "custom_tool_call"} {
				t.Run(entry+"/"+key+"/"+itemType, func(t *testing.T) {
					var param any
					patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
					completed := patchSend(&param, patchStep("step.stop", nil))
					if len(completed) != 3 || completed[len(completed)-1].Get("type").String() != "response.output_item.done" {
						t.Fatalf("fixture did not complete patch item: %v", completed)
					}
					step := map[string]any{"type": itemType}
					event := map[string]any{"event_type": entry, "step": step}
					if key == "index" {
						step["index"] = 2
						event["index"] = 2
					} else if key == "id" {
						step["id"] = "item_2"
					} else {
						step["call_id"] = "call_2"
					}
					if entry == "interaction.completed" {
						event["interaction"] = map[string]any{"steps": []any{step}}
					} else if entry == "finish" {
						event["steps"] = []any{step}
					}
					assertInteractionsPatchReviewFailure(t, &param, patchSend(&param, patchJSON(event)))
				})
			}
		}
	}
}

func TestInteractionsApplyPatchFinalOnlyIdentityAndEnvelope(t *testing.T) {
	for _, entry := range []string{"interaction.completed", "finish"} {
		for _, location := range []string{"interaction", "root"} {
			for _, evidence := range []string{"unnamed", "malformed named", "valid named"} {
				t.Run(entry+"/"+location+"/"+evidence, func(t *testing.T) {
					step := map[string]any{"index": 2, "type": "function_call", "id": "item_2", "call_id": "call_2", "arguments": map[string]any{"input": "secret"}}
					if evidence != "unnamed" {
						step["name"] = "functions__apply_patch"
					}
					event := map[string]any{"event_type": entry}
					path := "steps"
					if location == "interaction" {
						event["interaction"] = map[string]any{"id": "r", "steps": []any{step}}
						path = "interaction.steps"
					} else {
						event["steps"] = []any{step}
					}
					raw := patchJSON(event)
					if evidence == "malformed named" {
						raw = raw[:len(raw)-1]
						if gjson.ValidBytes(raw) || gjson.GetBytes(raw, path+".0.name").String() != "functions__apply_patch" {
							t.Fatal("fixture must retain a recoverable patch step in incomplete outer JSON")
						}
					}
					var param any
					events := patchSend(&param, raw)
					if evidence == "valid named" {
						assertPatchLifecycle(t, events, "secret")
					} else {
						assertInteractionsPatchReviewFailure(t, &param, events)
					}
				})
			}
		}
	}
}

func TestInteractionsApplyPatchFinalOnlyOtherToolsKeepLegacyBehavior(t *testing.T) {
	for _, name := range []string{"lookup", "exec"} {
		for _, malformed := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/valid", true: "/malformed"}[malformed], func(t *testing.T) {
				request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"},{"type":"custom","name":"exec"}]}`)
				raw := patchJSON(map[string]any{"event_type": "interaction.completed", "steps": []any{map[string]any{"type": "function_call", "name": name, "arguments": map[string]any{"x": 1}}}})
				if malformed {
					raw = raw[:len(raw)-1]
				}
				var param any
				events := patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, raw, &param))
				if len(events) != 1 || events[0].Get("type").String() != "response.completed" || len(events[0].Get("response.output").Array()) != 0 {
					t.Fatalf("legacy final-only ordinary/custom behavior changed: %v", events)
				}
				if state := param.(interface{ ToolInputError() error }); state.ToolInputError() != nil {
					t.Fatalf("ordinary/custom arguments received patch validation: %v", state.ToolInputError())
				}
			})
		}
	}
}

func TestInteractionsApplyPatchFinalSnapshotsPreserveOtherOutputsAndCalls(t *testing.T) {
	var param any
	request := []byte(`{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"lookup"},{"type":"custom","name":"exec"}]}`)
	send := func(index int, entry, key string, value any) []gjson.Result {
		return patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, patchJSON(map[string]any{"event_type": entry, "index": index, key: value}), &param))
	}
	snapshots := []any{
		map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}},
		map[string]any{"type": "function_call", "id": "ordinary", "name": "lookup", "arguments": map[string]any{"x": 1}},
		map[string]any{"type": "function_call", "id": "custom", "name": "exec", "arguments": map[string]any{"command": "ls"}},
		map[string]any{"type": "model_output", "id": "message", "content": "hello"},
	}
	for index, step := range snapshots {
		send(index, "step.start", "step", step)
		if index == 3 {
			send(index, "step.delta", "delta", map[string]any{"type": "text", "text": "hello"})
		}
		send(index, "step.stop", "step", step)
	}
	// Array positions are not explicit call indexes when the supplied IDs identify other steps.
	finalSteps := []any{snapshots[3], snapshots[2], snapshots[1], snapshots[0]}
	raw := patchJSON(map[string]any{"event_type": "interaction.completed", "interaction": map[string]any{"steps": finalSteps}})
	events := patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, raw, &param))
	if len(events) != 1 || events[0].Get("type").String() != "response.completed" {
		t.Fatalf("legitimate reordered snapshots failed: %v", events)
	}
	output := events[0].Get("response.output")
	if len(output.Array()) != 4 || output.Get("0.input").String() != "p" || output.Get("1.arguments").String() != `{"x":1}` || output.Get("2.input").String() != `{"command":"ls"}` || output.Get("3.content.0.text").String() != "hello" {
		t.Fatalf("other output/call lost or rebound: %s", output.Raw)
	}
}

func TestInteractionsApplyPatchFinalOnlyCallDoesNotRebindUnrelatedItem(t *testing.T) {
	var param any
	request := []byte(`{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"lookup"}]}`)
	send := func(raw []byte) []gjson.Result {
		return patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, raw, &param))
	}
	send([]byte(`{"event_type":"step.start","index":0,"step":{"type":"function_call","id":"ordinary","name":"lookup","arguments":{"x":1}}}`))
	send([]byte(`{"event_type":"step.stop","index":0}`))
	events := send([]byte(`{"event_type":"interaction.completed","steps":[{"type":"function_call","id":"patch","call_id":"patch_call","name":"functions__apply_patch","arguments":{"input":"p"}}]}`))
	final := events[len(events)-1]
	if final.Get("type").String() != "response.completed" || final.Get("response.output.0.id").String() != "ordinary" || final.Get("response.output.0.arguments").String() != `{"x":1}` || final.Get("response.output.1.id").String() != "patch" || final.Get("response.output.1.input").String() != "p" {
		t.Fatalf("array position rebound unrelated call: %v", events)
	}
}

func TestInteractionsApplyPatchExistingCallAcceptsOmittedSnapshotType(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed"} {
		t.Run(entry, func(t *testing.T) {
			var param any
			patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
			patchSend(&param, patchStep("step.stop", nil))
			step := map[string]any{"id": "item_2", "arguments": map[string]any{"input": "p"}}
			raw := patchStep(entry, step)
			if entry == "interaction.completed" {
				raw = patchJSON(map[string]any{"event_type": entry, "steps": []any{step}})
			}
			events := patchSend(&param, raw)
			if entry != "interaction.completed" {
				if len(events) != 0 {
					t.Fatalf("equivalent snapshot replayed lifecycle: %v", events)
				}
				events = patchSend(&param, []byte(`{"event_type":"interaction.completed"}`))
			}
			if len(events) != 1 || events[0].Get("type").String() != "response.completed" || events[0].Get("response.output.0.input").String() != "p" {
				t.Fatalf("omitted type erased existing patch identity: %v", events)
			}
		})
	}
}

func TestInteractionsApplyPatchMalformedPreNameEnvelopeRetainsSuppliedID(t *testing.T) {
	var param any
	if events := patchSend(&param, []byte(`{"event_type":"step.start","step":{"type":"function_call","id":"item_2","call_id":"call_2","arguments":{"input":"secret"}}`)); len(events) != 0 {
		t.Fatalf("unnamed malformed evidence leaked: %v", events)
	}
	events := patchSend(&param, []byte(`{"event_type":"interaction.completed","steps":[{"type":"function_call","id":"item_2","call_id":"call_2","name":"functions__apply_patch","arguments":{"input":"secret"}}]}`))
	assertInteractionsPatchReviewFailure(t, &param, events)
}

func TestInteractionsApplyPatchFinalOnlyUnnamedUnkeyedFunctionFails(t *testing.T) {
	var param any
	events := patchSend(&param, []byte(`{"event_type":"interaction.completed","steps":[{"type":"function_call","arguments":{"input":"secret"}}]}`))
	assertInteractionsPatchReviewFailure(t, &param, events)
}

func TestInteractionsApplyPatchOrdinaryLateIdentityReplaysOnlyBufferedPrefix(t *testing.T) {
	var param any
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}`)
	var events []gjson.Result
	send := func(raw []byte) {
		events = append(events, patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, raw, &param))...)
	}
	send([]byte(`{"event_type":"step.start","index":2,"step":{"type":"function_call","arguments":{}}}`))
	send([]byte(`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"x\":"}}`))
	send([]byte(`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"item_2","name":"lookup"}}`))
	send([]byte(`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"1}"}}`))
	send([]byte(`{"event_type":"step.stop","index":2,"step":{"type":"function_call","id":"item_2","name":"lookup","arguments":{"x":1}}}`))
	send([]byte(`{"event_type":"interaction.completed"}`))
	var delta strings.Builder
	for _, event := range events {
		if event.Get("type").String() == "response.function_call_arguments.delta" {
			delta.WriteString(event.Get("delta").String())
		}
		if event.Get("type").String() == "response.function_call_arguments.done" && event.Get("arguments").String() != `{"x":1}` {
			t.Fatal(event.Raw)
		}
	}
	if delta.String() != `{"x":1}` || events[len(events)-1].Get("response.output.0.arguments").String() != delta.String() {
		t.Fatalf("late-identity prefix replayed or lost: delta=%q events=%v", delta.String(), events)
	}
}

func patchReviewSnapshot(entry string, index int, step map[string]any) []byte {
	event := map[string]any{"event_type": entry}
	if entry == "step.start" || entry == "step.stop" {
		event["step"] = step
		if index >= 0 {
			event["index"] = index
		}
	} else {
		if index >= 0 {
			step["index"] = index
		}
		if entry == "finish" {
			event["steps"] = []any{step}
		} else {
			event["interaction"] = map[string]any{"steps": []any{step}}
		}
	}
	return patchJSON(event)
}

func TestInteractionsApplyPatchConflictingIndexAndIDs(t *testing.T) {
	identities := []struct {
		name, itemID, callID string
		index                int
	}{
		{"both IDs", "item_2", "call_2", 3},
		{"item ID only", "item_2", "", 3},
		{"call ID only", "", "call_2", 3},
		{"matching item wrong call", "item_2", "wrong_call", 3},
		{"wrong item matching call", "wrong_item", "call_2", 3},
		{"item and other patch call", "item_2", "call_4", 3},
		{"other patch item and call", "item_4", "call_2", 3},
		{"index of other patch", "item_2", "call_2", 4},
		{"split IDs without index", "item_2", "call_4", -1},
		{"split IDs at real index", "item_2", "call_4", 2},
	}
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
		for _, phase := range []string{"active item", "completed item"} {
			for _, identity := range identities {
				for _, snapshot := range []string{"model_output", "thought", "custom_tool_call", "function same input", "function changed input", "omitted type"} {
					t.Run(entry+"/"+phase+"/"+identity.name+"/"+snapshot, func(t *testing.T) {
						var param any
						patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch"}))
						patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": patchArguments("p")}}))
						patchSend(&param, patchReviewSnapshot("step.start", 4, map[string]any{"type": "function_call", "id": "item_4", "call_id": "call_4", "name": "functions__apply_patch", "arguments": map[string]any{"input": "q"}}))
						if phase == "completed item" {
							completed := patchSend(&param, patchStep("step.stop", nil))
							if len(completed) != 2 || completed[1].Get("type").String() != "response.output_item.done" {
								t.Fatalf("fixture did not complete real source input: %v", completed)
							}
						}
						step := map[string]any{}
						if identity.itemID != "" {
							step["id"] = identity.itemID
						}
						if identity.callID != "" {
							step["call_id"] = identity.callID
						}
						switch snapshot {
						case "function same input", "function changed input", "omitted type":
							step["name"] = "functions__apply_patch"
							input := "p"
							if snapshot == "function changed input" {
								input = "secret"
							}
							step["arguments"] = map[string]any{"input": input}
							if snapshot != "omitted type" {
								step["type"] = "function_call"
							}
						default:
							step["type"] = snapshot
						}
						assertInteractionsPatchReviewFailure(t, &param, patchSend(&param, patchReviewSnapshot(entry, identity.index, step)))
						state := param.(*interactionsToResponsesStreamState)
						if len(state.FunctionCalls) != 2 || state.FunctionCalls[2].PatchCall.OutputIndex != 2 {
							t.Fatalf("conflicting identity created or rebound a patch call: %+v", state.FunctionCalls)
						}
					})
				}
			}
		}
	}
}

func TestInteractionsApplyPatchRootAndStepIndexesMustAgree(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop"} {
		for _, indexes := range [][2]int{{2, 3}, {3, 2}} {
			t.Run(entry+"/"+string(patchJSON(indexes)), func(t *testing.T) {
				var param any
				patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
				patchSend(&param, patchStep("step.stop", nil))
				step := map[string]any{"index": indexes[1], "type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}
				assertInteractionsPatchReviewFailure(t, &param, patchSend(&param, patchReviewSnapshot(entry, indexes[0], step)))
			})
		}
	}
}

func TestInteractionsApplyPatchPreNameIndexConflictRetained(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
		for _, key := range []string{"id", "call_id", "both"} {
			for _, snapshotType := range []string{"model_output", "function_call", "omitted"} {
				for _, resolvedAt := range []string{"original index", "changed index", "IDs only", "original index without IDs", "changed index without IDs"} {
					t.Run(entry+"/"+key+"/"+snapshotType+"/"+resolvedAt, func(t *testing.T) {
						var param any
						patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "arguments": map[string]any{"input": "p"}}))
						conflict := map[string]any{"index": 3}
						if key != "call_id" {
							conflict["id"] = "item_2"
						}
						if key != "id" {
							conflict["call_id"] = "call_2"
						}
						if snapshotType != "omitted" {
							conflict["type"] = snapshotType
						}
						resolved := map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}
						index := -1
						if strings.HasPrefix(resolvedAt, "original index") {
							index = 2
						} else if strings.HasPrefix(resolvedAt, "changed index") {
							index = 3
						}
						if strings.HasSuffix(resolvedAt, "without IDs") {
							delete(resolved, "id")
							delete(resolved, "call_id")
						}
						var events []gjson.Result
						if entry == "step.start" || entry == "step.stop" {
							// Unknown names must not make identity contradictions disappear later.
							patchSend(&param, patchReviewSnapshot(entry, 3, conflict))
							events = patchSend(&param, patchReviewSnapshot("interaction.completed", index, resolved))
						} else {
							if index >= 0 {
								resolved["index"] = index
							}
							events = patchSend(&param, patchJSON(map[string]any{"event_type": entry, "steps": []any{conflict, resolved}}))
						}
						assertInteractionsPatchReviewFailure(t, &param, events)
						for _, call := range param.(*interactionsToResponsesStreamState).FunctionCalls {
							if call.PatchCall != nil || call.Added {
								t.Fatal("late identity announced a patch after contradictory evidence")
							}
						}
					})
				}
			}
		}
	}
}

func TestInteractionsApplyPatchConsistentSnapshotIdentityMatrix(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
		for _, key := range []string{"index and IDs", "IDs only", "item only", "call only"} {
			for _, snapshotType := range []string{"function_call", "omitted"} {
				t.Run(entry+"/"+key+"/"+snapshotType, func(t *testing.T) {
					var param any
					var events []gjson.Result
					events = append(events, patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))...)
					events = append(events, patchSend(&param, patchStep("step.stop", nil))...)
					step := map[string]any{"arguments": map[string]any{"input": "p"}}
					index := -1
					if key == "index and IDs" {
						index = 2
					}
					if key != "call only" {
						step["id"] = "item_2"
					}
					if key != "item only" {
						step["call_id"] = "call_2"
					}
					if snapshotType != "omitted" {
						step["type"] = snapshotType
					}
					events = append(events, patchSend(&param, patchReviewSnapshot(entry, index, step))...)
					if entry == "step.start" || entry == "step.stop" {
						events = append(events, patchSend(&param, []byte(`{"event_type":"interaction.completed"}`))...)
					}
					assertPatchLifecycle(t, events, "p")
					if state := param.(*interactionsToResponsesStreamState); len(state.FunctionCalls) != 1 || state.ToolInputError() != nil {
						t.Fatalf("consistent snapshot changed identity: %+v", state)
					}
				})
			}
		}
	}
}

func TestInteractionsApplyPatchUnmatchedNewIDsRemainIndependent(t *testing.T) {
	for _, entry := range []string{"step.start", "interaction.completed", "finish"} {
		for _, index := range []int{-1, 3} {
			t.Run(entry+"/"+string(patchJSON(index)), func(t *testing.T) {
				var param any
				patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
				patchSend(&param, patchStep("step.stop", nil))
				step := map[string]any{"type": "function_call", "id": "new_item", "call_id": "new_call", "name": "functions__apply_patch", "arguments": map[string]any{"input": "q"}}
				events := patchSend(&param, patchReviewSnapshot(entry, index, step))
				if entry == "step.start" {
					events = append(events, patchSend(&param, []byte(`{"event_type":"interaction.completed"}`))...)
				}
				final := events[len(events)-1]
				if final.Get("type").String() != "response.completed" || len(final.Get("response.output").Array()) != 2 {
					t.Fatalf("unmatched new IDs were rejected: %v", events)
				}
				inputs := map[string]string{}
				for _, item := range final.Get("response.output").Array() {
					inputs[item.Get("call_id").String()] = item.Get("input").String()
				}
				if inputs["call_2"] != "p" || inputs["new_call"] != "q" || param.(*interactionsToResponsesStreamState).ToolInputError() != nil {
					t.Fatalf("new call rebound existing patch: %v", events)
				}
			})
		}
	}
}

func TestInteractionsApplyPatchOrdinaryExplicitIndexKeepsLegacyBehavior(t *testing.T) {
	var param any
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}`)
	send := func(raw []byte) []gjson.Result {
		return patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, raw, &param))
	}
	for _, index := range []int{2, 3} {
		step := map[string]any{"type": "function_call", "id": "ordinary", "call_id": "ordinary_call", "name": "lookup", "arguments": map[string]any{"x": index}}
		send(patchReviewSnapshot("step.start", index, step))
		send(patchReviewSnapshot("step.stop", index, step))
	}
	events := send([]byte(`{"event_type":"interaction.completed"}`))
	if len(events) != 1 || events[0].Get("type").String() != "response.completed" || events[0].Get("response.output.0.arguments").String() != `{"x":2}` || events[0].Get("response.output.1.arguments").String() != `{"x":3}` || param.(*interactionsToResponsesStreamState).ToolInputError() != nil {
		t.Fatalf("ordinary explicit-index behavior received patch validation: %v", events)
	}
}

func TestInteractionsApplyPatchThreeEventChangedIndexReproduction(t *testing.T) {
	for _, snapshot := range []string{
		`{"index":3,"type":"model_output","id":"item_2","call_id":"call_2"}`,
		`{"index":3,"type":"function_call","id":"item_2","call_id":"call_2","name":"functions__apply_patch","arguments":{"input":"secret"}}`,
	} {
		t.Run(snapshot, func(t *testing.T) {
			var param any
			patchSend(&param, []byte(`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"item_2","call_id":"call_2","name":"functions__apply_patch","arguments":{"input":"p"}}}`))
			patchSend(&param, []byte(`{"event_type":"step.stop","index":2}`))
			assertInteractionsPatchReviewFailure(t, &param, patchSend(&param, []byte(`{"event_type":"interaction.completed","steps":[`+snapshot+`]}`)))
			if len(param.(*interactionsToResponsesStreamState).FunctionCalls) != 1 {
				t.Fatal("terminal snapshot created a second call with the same IDs")
			}
		})
	}
}

func TestInteractionsApplyPatchIDsCannotSelectUnrelatedOrdinaryIndex(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
		for _, snapshotType := range []string{"model_output", "function_call"} {
			t.Run(entry+"/"+snapshotType, func(t *testing.T) {
				var param any
				request := []byte(`{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"lookup"}]}`)
				send := func(raw []byte) []gjson.Result {
					return patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, raw, &param))
				}
				send(patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
				send(patchStep("step.stop", nil))
				send(patchReviewSnapshot("step.start", 3, map[string]any{"type": "function_call", "id": "ordinary", "call_id": "ordinary_call", "name": "lookup", "arguments": map[string]any{"x": 1}}))
				send(patchReviewSnapshot("step.stop", 3, nil))
				step := map[string]any{"type": snapshotType, "id": "item_2", "call_id": "call_2", "name": "lookup", "arguments": map[string]any{"x": 2}}
				assertInteractionsPatchReviewFailure(t, &param, send(patchReviewSnapshot(entry, 3, step)))
				if call := param.(*interactionsToResponsesStreamState).FunctionCalls[3]; call.ID != "ordinary" || call.Arguments.String() != `{"x":1}` || call.PatchCall != nil {
					t.Fatalf("conflicting patch IDs mutated an unrelated ordinary call: %+v", call)
				}
			})
		}
	}
}

func TestInteractionsApplyPatchNestedChangedIndexAndCompletedSource(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
		for _, index := range []int{-1, 2, 3} {
			t.Run(entry+"/"+string(patchJSON(index)), func(t *testing.T) {
				var param any
				patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch"}))
				patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": patchArguments("p")}}))
				patchSend(&param, patchStep("step.stop", nil))
				step := map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "secret"}}
				if index >= 0 {
					step["index"] = index
				}
				assertInteractionsPatchReviewFailure(t, &param, patchSend(&param, patchReviewSnapshot(entry, -1, step)))
				if len(param.(*interactionsToResponsesStreamState).FunctionCalls) != 1 {
					t.Fatal("function snapshot bypassed completed real source comparison")
				}
			})
		}
	}
}
