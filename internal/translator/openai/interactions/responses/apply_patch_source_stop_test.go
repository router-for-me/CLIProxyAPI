package responses

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Source completion is a barrier even when the winning name or either ID is late.
func TestInteractionsApplyPatchSourceStopFragments(t *testing.T) {
	for _, first := range []string{"item", "call", "neither", "ready"} {
		for _, lateName := range []bool{false, true} {
			for _, input := range []string{"partial", "complete"} {
				for _, discovery := range []string{"terminal", "identity", "coincident-snapshot"} {
					t.Run(fmt.Sprintf("%s/late-name=%v/%s/%s", first, lateName, input, discovery), func(t *testing.T) {
						var param any
						step := map[string]any{"type": "function_call"}
						if first == "item" || first == "ready" {
							step["id"] = "item_2"
						}
						if first == "call" || first == "ready" {
							step["call_id"] = "call_2"
						}
						if !lateName {
							step["name"] = "functions__apply_patch"
						}
						before, after, final := `{"input":"p`, `q"}`, "pq"
						if input == "complete" {
							before, after, final = `{"input":"p"}`, " \t\n", "p"
						}
						var events []gjson.Result
						events = append(events, patchSend(&param, patchStep("step.start", step))...)
						events = append(events, patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": before}}))...)
						events = append(events, patchSend(&param, patchStep("step.stop", nil))...)
						state := param.(*interactionsToResponsesStreamState)
						if (first != "ready" || lateName) && len(events) != 0 {
							t.Fatalf("unresolved call published: %v", events)
						}
						resolved := map[string]any{"index": 2, "type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": final}, "provider_secret": "RAW_SECRET"}
						post := map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": after}}
						if discovery == "coincident-snapshot" {
							post["step"] = resolved
						}
						events = append(events, patchSend(&param, patchJSON(post))...)
						if !lateName || discovery == "coincident-snapshot" {
							if state.ToolInputError() == nil {
								t.Fatalf("post-stop fragment accepted before identity readiness: %v", events)
							}
							if first != "ready" || lateName {
								if !strings.Contains(state.ToolInputError().Error(), "after source stop") {
									t.Fatalf("snapshot/replay erased source ordering: %v", state.ToolInputError())
								}
							}
						} else {
							call := state.FunctionCalls[2]
							if state.ToolInputError() != nil || call.PendingError == nil || !strings.Contains(call.PendingError.Error(), "after source stop") {
								t.Fatalf("unnamed call lost pending source violation: %+v", call)
							}
							if len(call.ArgumentFragments) != 1 || call.ArgumentFragments[0] != before {
								t.Fatalf("buffered a fragment after source stop: %q", call.ArgumentFragments)
							}
						}
						if discovery == "identity" {
							events = append(events, patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "step": resolved, "delta": map[string]any{"type": "arguments_delta", "arguments": ""}}))...)
						}
						events = append(events, patchSend(&param, patchJSON(map[string]any{"event_type": "interaction.completed", "steps": []any{resolved}}))...)
						failures := 0
						for _, event := range events {
							if (first != "ready" || lateName) && event.Get("type").String() != "response.failed" {
								t.Fatalf("unresolved stopped call published success: %s", event.Raw)
							}
							if event.Get("type").String() == "response.failed" {
								failures++
								if event.Get("response.error.code").String() != "invalid_tool_arguments" {
									t.Fatalf("unsanitized failure: %s", event.Raw)
								}
							}
							if event.Get("type").String() == "response.completed" || strings.Contains(event.Raw, "RAW_SECRET") {
								t.Fatalf("source violation completed/leaked: %s", event.Raw)
							}
						}
						if failures != 1 || state.ToolInputError() == nil {
							t.Fatalf("failure count=%d events=%v", failures, events)
						}
						if more := patchSend(&param, []byte(`[DONE]`)); len(more) != 0 {
							t.Fatalf("failure reopened: %v", more)
						}
					})
				}
			}
		}
	}
}

func TestInteractionsApplyPatchSourceStopLateIdentityReplay(t *testing.T) {
	for _, first := range []string{"item", "call", "neither"} {
		for _, lateName := range []bool{false, true} {
			for _, boundary := range []string{"before-stop", "identity-after-stop", "terminal-snapshot"} {
				t.Run(fmt.Sprintf("%s/late-name=%v/%s", first, lateName, boundary), func(t *testing.T) {
					var param any
					step := map[string]any{"type": "function_call"}
					if first == "item" {
						step["id"] = "item_2"
					}
					if first == "call" {
						step["call_id"] = "call_2"
					}
					if !lateName {
						step["name"] = "functions__apply_patch"
					}
					if events := patchSend(&param, patchStep("step.start", step)); len(events) != 0 {
						t.Fatalf("provisional identity: %v", events)
					}
					args := patchArguments(patchText)
					for _, fragment := range []string{args[:15], args[15:]} {
						if events := patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": fragment}})); len(events) != 0 {
							t.Fatalf("unresolved delta: %v", events)
						}
					}
					if boundary != "before-stop" {
						if events := patchSend(&param, patchStep("step.stop", nil)); len(events) != 0 {
							t.Fatalf("stop guessed an identity: %v", events)
						}
					}
					resolved := map[string]any{"index": 2, "type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch"}
					var events []gjson.Result
					if boundary != "terminal-snapshot" {
						events = patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "step": resolved, "delta": map[string]any{"type": "arguments_delta", "arguments": ""}}))
						wantCount := 3
						if boundary == "identity-after-stop" {
							wantCount = 5
							if !param.(*interactionsToResponsesStreamState).FunctionCalls[2].ItemDoneEmitted {
								t.Fatal("identity-only update did not publish pending completion")
							}
						}
						if len(events) != wantCount || param.(*interactionsToResponsesStreamState).Terminal {
							t.Fatalf("replay waited for response terminal: %v", events)
						}
						if boundary == "before-stop" {
							events = append(events, patchSend(&param, patchStep("step.stop", nil))...)
						}
					}
					resolved["arguments"] = map[string]any{"input": patchText}
					events = append(events, patchSend(&param, patchJSON(map[string]any{"event_type": "interaction.completed", "steps": []any{resolved}}))...)
					assertPatchLifecycle(t, events, patchText)
					var fragments []string
					for _, event := range events {
						if event.Get("type").String() == "response.custom_tool_call_input.delta" {
							fragments = append(fragments, event.Get("delta").String())
						}
					}
					if len(fragments) != 2 || fragments[0] != patchText[:5] || fragments[1] != patchText[5:] {
						t.Fatalf("snapshot replaced real source fragments: %q", fragments)
					}
				})
			}
		}
	}
}

// An arguments delta can establish a candidate before its first named start.
func TestInteractionsApplyPatchSourceStopBeforeStart(t *testing.T) {
	for _, before := range []string{`{"input":"p`, `{"input":"pq"}`} {
		t.Run(before, func(t *testing.T) {
			var param any
			patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": before}}))
			patchSend(&param, patchStep("step.stop", nil))
			post := `q"}`
			if strings.HasSuffix(before, "}") {
				post = " "
			}
			patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": post}}))
			if call := param.(*interactionsToResponsesStreamState).FunctionCalls[2]; call.PendingError == nil {
				t.Fatal("stop before named start lost source evidence")
			}
			events := patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "pq"}}))
			if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
				t.Fatalf("late start repaired a stopped call: %v", events)
			}
		})
	}
}

func TestInteractionsApplyPatchSourceStopOrdinaryFunction(t *testing.T) {
	var param any
	for _, raw := range []string{
		`{"event_type":"step.start","index":2,"step":{"type":"function_call","id":"ordinary","call_id":"ordinary_call"}}`,
		`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":"{\"path\":\"a"}}`,
		`{"event_type":"step.stop","index":2}`,
		`{"event_type":"step.delta","index":2,"delta":{"type":"arguments_delta","arguments":".txt\"}"}}`,
	} {
		if events := patchSend(&param, []byte(raw)); len(events) != 0 {
			t.Fatalf("unnamed ordinary call announced: %v", events)
		}
	}
	events := patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "ordinary", "call_id": "ordinary_call", "name": "external_read_file"}))
	events = append(events, patchSend(&param, patchStep("step.stop", nil))...)
	events = append(events, patchSend(&param, []byte(`{"event_type":"interaction.completed"}`))...)
	state := param.(*interactionsToResponsesStreamState)
	if state.ToolInputError() != nil {
		t.Fatalf("patch-only source violation broke ordinary function: %v", state.ToolInputError())
	}
	counts := map[string]int{}
	for _, event := range events {
		kind := event.Get("type").String()
		counts[kind]++
		if strings.Contains(kind, "custom_tool_call") || kind == "response.failed" {
			t.Fatalf("ordinary function bridged: %s", event.Raw)
		}
		if kind == "response.function_call_arguments.delta" && event.Get("delta").String() != `{"path":"a.txt"}` {
			t.Fatalf("ordinary buffered fragments changed: %s", event.Raw)
		}
	}
	for _, kind := range []string{"response.output_item.added", "response.function_call_arguments.delta", "response.function_call_arguments.done", "response.output_item.done", "response.completed"} {
		if counts[kind] != 1 {
			t.Fatalf("ordinary lifecycle changed: %v", counts)
		}
	}
}
