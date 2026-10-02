package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func patchTypeReviewIdentity(key string) (int, map[string]any) {
	index := -1
	step := map[string]any{}
	if key == "index only" || key == "index and IDs" {
		index = 2
	}
	if key == "IDs only" || key == "index and IDs" || key == "item only" {
		step["id"] = "item_2"
	}
	if key == "IDs only" || key == "index and IDs" || key == "call only" {
		step["call_id"] = "call_2"
	}
	return index, step
}

func patchTypeReviewEntries(entry string) []string {
	if entry == "step.start" || entry == "step.stop" {
		return []string{"step.start", "step.stop", "interaction.completed", "finish"}
	}
	return []string{entry}
}

func patchTypeReviewFinal(entry string, steps ...any) []byte {
	return patchJSON(map[string]any{"event_type": entry, "steps": steps})
}

func TestInteractionsApplyPatchPreNameConsistentTypeConflict(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
		for _, itemType := range []string{"model_output", "thought", "custom_tool_call"} {
			for _, evidenceKey := range []string{"index and IDs", "index only", "IDs only"} {
				for _, resolvedKey := range []string{"index and IDs", "index only", "IDs only", "item only", "call only"} {
					for _, resolvedEntry := range patchTypeReviewEntries(entry) {
						t.Run(strings.Join([]string{entry, itemType, evidenceKey, resolvedEntry, resolvedKey}, "/"), func(t *testing.T) {
							var param any
							if events := patchSend(&param, []byte(`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"item_2","call_id":"call_2","arguments":{"input":"p"}}}`)); len(events) != 0 {
								t.Fatalf("unnamed call announced: %v", events)
							}
							index, conflict := patchTypeReviewIdentity(evidenceKey)
							conflict["type"] = itemType
							resolvedIndex, resolved := patchTypeReviewIdentity(resolvedKey)
							resolved["type"], resolved["name"] = "function_call", "functions__apply_patch"
							resolved["arguments"] = map[string]any{"input": "p"}
							var events []gjson.Result
							if entry == "step.start" || entry == "step.stop" {
								for _, event := range patchSend(&param, patchReviewSnapshot(entry, index, conflict)) {
									if event.Get("item.type").String() == "custom_tool_call" || strings.HasPrefix(event.Get("type").String(), "response.custom_tool_call_input.") || event.Get("type").String() == "response.failed" {
										t.Fatalf("unresolved type evidence prematurely treated as patch: %s", event.Raw)
									}
								}
								events = patchSend(&param, patchReviewSnapshot(resolvedEntry, resolvedIndex, resolved))
							} else {
								if index >= 0 {
									conflict["index"] = index
								}
								if resolvedIndex >= 0 {
									resolved["index"] = resolvedIndex
								}
								// Both snapshots belong to the same call in this single terminal event.
								events = patchSend(&param, patchTypeReviewFinal(entry, conflict, resolved))
							}
							assertInteractionsPatchReviewFailure(t, &param, events)
							state := param.(*interactionsToResponsesStreamState)
							call := state.FunctionCalls[2]
							if len(state.FunctionCalls) != 1 || len(state.PendingIdentityErrors) != 0 || call.PendingError == nil || !strings.Contains(call.PendingError.Error(), "type") || call.PatchCall != nil || call.Added {
								t.Fatalf("type conflict lost or hidden by identity failure: %+v", state)
							}
						})
					}
				}
			}
		}
	}
}

func TestInteractionsApplyPatchPreNameOmittedType(t *testing.T) {
	for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
		for _, resolvedKey := range []string{"index and IDs", "index only", "IDs only", "item only", "call only"} {
			for _, resolvedEntry := range patchTypeReviewEntries(entry) {
				t.Run(entry+"/"+resolvedEntry+"/"+resolvedKey, func(t *testing.T) {
					var param any
					var events []gjson.Result
					events = append(events, patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "arguments": map[string]any{"input": "p"}}))...)
					_, snapshot := patchTypeReviewIdentity("index and IDs")
					index, resolved := patchTypeReviewIdentity(resolvedKey)
					resolved["type"], resolved["name"] = "function_call", "functions__apply_patch"
					resolved["arguments"] = map[string]any{"input": "p"}
					if entry == "step.start" || entry == "step.stop" {
						events = append(events, patchSend(&param, patchReviewSnapshot(entry, 2, snapshot))...)
						events = append(events, patchSend(&param, patchReviewSnapshot(resolvedEntry, index, resolved))...)
						if resolvedEntry == "step.start" || resolvedEntry == "step.stop" {
							events = append(events, patchSend(&param, []byte(`{"event_type":"interaction.completed"}`))...)
						}
					} else {
						snapshot["index"] = 2
						if index >= 0 {
							resolved["index"] = index
						}
						events = append(events, patchSend(&param, patchTypeReviewFinal(entry, snapshot, resolved))...)
					}
					assertPatchLifecycle(t, events, "p")
					if state := param.(*interactionsToResponsesStreamState); state.ToolInputError() != nil || state.FunctionCalls[2].PendingError != nil || len(state.FunctionCalls) != 1 {
						t.Fatalf("omitted type introduced a conflict: %+v", state)
					}
				})
			}
		}
	}
}

func TestInteractionsApplyPatchPreNameTypeOrdinaryCompatibility(t *testing.T) {
	requests := []struct{ name, request, upstream string }{
		{"ordinary", `{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}`, "lookup"},
		{"function winner", `{"tools":[{"type":"function","name":"apply_patch"},{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "apply_patch"},
	}
	for _, request := range requests {
		for _, entry := range []string{"step.start", "step.stop", "interaction.completed", "finish"} {
			for _, itemType := range []string{"model_output", "thought", "custom_tool_call", "omitted"} {
				t.Run(request.name+"/"+entry+"/"+itemType, func(t *testing.T) {
					var param any
					send := func(raw []byte) []gjson.Result {
						return patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", []byte(request.request), nil, raw, &param))
					}
					send(patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "arguments": map[string]any{"x": 1}}))
					conflict := map[string]any{"index": 2, "id": "item_2", "call_id": "call_2"}
					if itemType != "omitted" {
						conflict["type"] = itemType
					}
					resolved := map[string]any{"index": 2, "type": "function_call", "id": "item_2", "call_id": "call_2", "name": request.upstream, "arguments": map[string]any{"x": 1}}
					var events []gjson.Result
					if entry == "step.start" || entry == "step.stop" {
						events = append(events, send(patchReviewSnapshot(entry, 2, conflict))...)
						events = append(events, send(patchReviewSnapshot("step.start", 2, resolved))...)
						events = append(events, send(patchStep("step.stop", nil))...)
						events = append(events, send([]byte(`{"event_type":"interaction.completed"}`))...)
					} else {
						events = send(patchTypeReviewFinal(entry, conflict, resolved))
					}
					for _, event := range events {
						if event.Get("type").String() == "response.failed" || strings.HasPrefix(event.Get("type").String(), "response.custom_tool_call_input.") || event.Get("item.type").String() == "custom_tool_call" {
							t.Fatalf("ordinary winner received patch behavior: %s", event.Raw)
						}
					}
					final := events[len(events)-1]
					if final.Get("type").String() != "response.completed" || final.Get("response.output.0.type").String() != "function_call" || final.Get("response.output.0.name").String() != request.upstream || final.Get("response.output.0.arguments").String() != `{"x":1}` || param.(interface{ ToolInputError() error }).ToolInputError() != nil {
						t.Fatalf("ordinary late identity changed: %v", events)
					}
				})
			}
		}
	}
}

func TestInteractionsApplyPatchPreNameUnrelatedModelOutput(t *testing.T) {
	for _, entry := range []string{"interaction.completed", "finish"} {
		t.Run(entry, func(t *testing.T) {
			var param any
			patchSend(&param, patchReviewSnapshot("step.start", 0, map[string]any{"type": "model_output", "id": "message"}))
			patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 0, "delta": map[string]any{"type": "text", "text": "hello"}}))
			patchSend(&param, patchReviewSnapshot("step.stop", 0, nil))
			patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "arguments": map[string]any{"input": "p"}}))
			events := patchSend(&param, patchTypeReviewFinal(entry, map[string]any{"type": "model_output", "id": "message", "content": "hello"}, map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
			final := events[len(events)-1]
			if final.Get("type").String() != "response.completed" || final.Get("response.output.#").Int() != 2 || final.Get("response.output.0.content.0.text").String() != "hello" || final.Get("response.output.1.input").String() != "p" || param.(*interactionsToResponsesStreamState).ToolInputError() != nil {
				t.Fatalf("unrelated message became a type conflict: %v", events)
			}
		})
	}
}

func TestInteractionsApplyPatchPreNameNoBridgeLegacy(t *testing.T) {
	requests := []struct{ name, request, upstream string }{
		{"ordinary only", `{"tools":[{"type":"function","name":"lookup"}]}`, "lookup"},
		{"function winner without bridge", `{"tools":[{"type":"function","name":"apply_patch"}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "apply_patch"},
	}
	for _, request := range requests {
		for _, entry := range []string{"step.start", "interaction.completed", "finish"} {
			t.Run(request.name+"/"+entry, func(t *testing.T) {
				var param any
				send := func(raw []byte) []gjson.Result {
					return patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", []byte(request.request), nil, raw, &param))
				}
				send(patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "arguments": map[string]any{"x": 1}}))
				message := map[string]any{"index": 2, "type": "model_output", "id": "item_2", "call_id": "call_2"}
				resolved := map[string]any{"index": 2, "type": "function_call", "id": "item_2", "call_id": "call_2", "name": request.upstream, "arguments": map[string]any{"x": 1}}
				if entry == "step.start" {
					events := send(patchReviewSnapshot(entry, 2, message))
					if len(events) != 2 || events[0].Get("item.type").String() != "message" || events[1].Get("type").String() != "response.content_part.added" {
						t.Fatalf("ordinary-only repeated start changed: %v", events)
					}
					send(patchReviewSnapshot("step.start", 2, resolved))
					send(patchStep("step.stop", nil))
					events = send([]byte(`{"event_type":"interaction.completed"}`))
					if len(events) != 1 || events[0].Get("response.output.0.name").String() != request.upstream || events[0].Get("response.output.0.arguments").String() != `{"x":1}` {
						t.Fatalf("ordinary-only function changed: %v", events)
					}
				} else {
					events := send(patchTypeReviewFinal(entry, message, resolved))
					state := param.(*interactionsToResponsesStreamState)
					if len(events) != 1 || events[0].Get("type").String() != "response.completed" || state.FunctionCalls[2].RawName != "" || state.FunctionCalls[2].Added {
						t.Fatalf("non-patch final filtering changed: %v", events)
					}
				}
				if state := param.(*interactionsToResponsesStreamState); state.ToolInputError() != nil || interactionsHasPatchBridge(state) {
					t.Fatalf("ordinary winner became a patch: %+v", state)
				}
			})
		}
	}
}
