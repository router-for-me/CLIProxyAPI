package responses

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const patchRequest = `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"start: patch"}}]}]}`
const patchText = "  *** Begin Patch\n*** Add File: 中.txt\n+😀\n*** End Patch\n "

func patchJSON(v any) []byte             { b, _ := json.Marshal(v); return b }
func patchArguments(input string) string { return string(patchJSON(map[string]string{"input": input})) }
func patchStep(event string, step any) []byte {
	return patchJSON(map[string]any{"event_type": event, "index": 2, "step": step})
}
func patchEvents(chunks [][]byte) []gjson.Result {
	var events []gjson.Result
	for _, chunk := range chunks {
		for _, line := range strings.Split(string(chunk), "\n") {
			if strings.HasPrefix(line, "data: ") && gjson.Valid(strings.TrimPrefix(line, "data: ")) {
				events = append(events, gjson.Parse(strings.TrimPrefix(line, "data: ")))
			}
		}
	}
	return events
}
func patchSend(param *any, raw []byte) []gjson.Result {
	return patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", []byte(patchRequest), nil, raw, param))
}

func TestInteractionsApplyPatchDeclarationAndHistory(t *testing.T) {
	request := gjson.Parse(patchRequest).Value().(map[string]any)
	request["input"] = []any{map[string]any{"type": "custom_tool_call", "name": "apply_patch", "namespace": "functions", "call_id": "c1", "input": patchText}, map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": "ok"}}
	body1, errConvert1 := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", patchJSON(request), false)
	if errConvert1 != nil {
		t.Fatal(errConvert1)
	}
	out := gjson.ParseBytes(body1)
	if !strings.Contains(out.Get("tools.0.description").String(), "*** Begin Patch") || out.Get("tools.0.parameters.additionalProperties").Bool() || !out.Get("tools.0.parameters.additionalProperties").Exists() {
		t.Fatalf("missing patch contract: %s", out.Raw)
	}
	if out.Get("input.0.arguments.input").String() != patchText || out.Get("input.1.name").String() != "functions__apply_patch" {
		t.Fatalf("history changed: %s", out.Raw)
	}
}

func TestInteractionsApplyPatchEverySplitPreviewAndCompletion(t *testing.T) {
	args := strings.NewReplacer("中", `\u4e2d`, "😀", `\ud83d\ude00`).Replace(patchArguments(patchText))
	for split := 0; split <= len(args); split++ {
		var param any
		var events []gjson.Result
		events = append(events, patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{}}))...)
		for _, fragment := range []string{args[:split], args[split:]} {
			events = append(events, patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": fragment}}))...)
		}
		var preview strings.Builder
		for _, ev := range events {
			if ev.Get("type").String() == "response.custom_tool_call_input.delta" {
				preview.WriteString(ev.Get("delta").String())
			}
		}
		if preview.String() != patchText {
			t.Fatalf("split %d: no decoded preview before stop: %q", split, preview.String())
		}
		events = append(events, patchSend(&param, patchStep("step.stop", nil))...)
		events = append(events, patchSend(&param, []byte(`{"event_type":"interaction.completed","interaction":{"id":"r"}}`))...)
		assertPatchLifecycle(t, events, patchText)
	}
}

func assertPatchLifecycle(t *testing.T, events []gjson.Result, want string) {
	t.Helper()
	var delta strings.Builder
	counts := map[string]int{}
	lastSeq := int64(0)
	for _, ev := range events {
		kind := ev.Get("type").String()
		counts[kind]++
		if seq := ev.Get("sequence_number").Int(); seq <= lastSeq {
			t.Fatalf("sequence: %s", ev.Raw)
		} else {
			lastSeq = seq
		}
		switch kind {
		case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
			if ev.Get("item_id").String() != "item_2" || ev.Get("call_id").String() != "call_2" || ev.Get("output_index").Int() != 2 {
				t.Fatalf("event identity: %s", ev.Raw)
			}
			if kind == "response.custom_tool_call_input.delta" {
				delta.WriteString(ev.Get("delta").String())
			} else if ev.Get("input").String() != want {
				t.Fatalf("done input: %s", ev.Raw)
			}
		case "response.output_item.added", "response.output_item.done":
			if ev.Get("item.id").String() != "item_2" || ev.Get("item.call_id").String() != "call_2" || ev.Get("item.namespace").String() != "functions" || ev.Get("item.name").String() != "apply_patch" {
				t.Fatalf("item identity: %s", ev.Raw)
			}
			if kind == "response.output_item.done" && ev.Get("item.input").String() != want {
				t.Fatalf("item input: %s", ev.Raw)
			}
		case "response.completed":
			if ev.Get("response.output.0.input").String() != want || ev.Get("response.output.0.id").String() != "item_2" || ev.Get("response.output.0.call_id").String() != "call_2" {
				t.Fatalf("final input: %s", ev.Raw)
			}
		}
	}
	if delta.String() != want || counts["response.output_item.added"] != 1 || counts["response.custom_tool_call_input.done"] != 1 || counts["response.output_item.done"] != 1 || counts["response.completed"] != 1 || counts["response.function_call_arguments.delta"] != 0 {
		t.Fatalf("lifecycle counts=%v delta=%q", counts, delta.String())
	}
}

func TestInteractionsApplyPatchInvalidNonStream(t *testing.T) {
	for _, args := range []string{`{}`, `{"input":1}`, `{"input":"a","input":"b"}`, `{"input":"secret"`, `{"input":"x","extra":1}`} {
		var param any
		raw := patchJSON(map[string]any{"id": "r", "steps": []any{map[string]any{"type": "function_call", "name": "functions__apply_patch", "arguments": args}}})
		out := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin/swe-2", []byte(patchRequest), nil, raw, &param)
		if len(out) != 0 {
			t.Fatalf("invalid arguments returned output: %s", out)
		}
		if state, ok := param.(interface{ ToolInputError() error }); !ok || state.ToolInputError() == nil {
			t.Fatal("missing error state")
		}
	}
}

func TestInteractionsApplyPatchSnapshotMatrix(t *testing.T) {
	start := func(name, id, args string) []byte {
		step := map[string]any{"type": "function_call", "id": id, "call_id": "call_2"}
		if name != "" {
			step["name"] = name
		}
		if args != "" {
			step["arguments"] = args
		}
		return patchStep("step.start", step)
	}
	delta := func(args string) []byte {
		return patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": args}})
	}
	stop := func(args string) []byte {
		return patchStep("step.stop", map[string]any{"name": "functions__apply_patch", "arguments": args})
	}
	final := func(args string) []byte {
		return patchJSON(map[string]any{"event_type": "interaction.completed", "interaction": map[string]any{"id": "r", "steps": []any{map[string]any{"index": 2, "type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": args}}}})
	}
	cases := []struct {
		name   string
		inputs [][]byte
		fail   bool
		want   string
	}{
		{"partial prefix completed by stop", [][]byte{start("functions__apply_patch", "item_2", ""), delta(`{"input":"a`), stop(`{"input":"ab"}`), final(`{"input":"ab"}`)}, false, "ab"},
		{"equivalent full snapshot", [][]byte{start("functions__apply_patch", "item_2", `{"input":"a"}`), stop(`{ "input" : "\u0061" }`), final(`{"input":"a"}`)}, false, "a"},
		{"full source cannot extend", [][]byte{start("functions__apply_patch", "item_2", ""), delta(`{"input":"a"}`), stop(`{"input":"ab"}`)}, true, ""},
		{"complete snapshot cannot extend", [][]byte{start("functions__apply_patch", "item_2", `{"input":"a"}`), stop(`{"input":"ab"}`)}, true, ""},
		{"invalid snapshot before name retained", [][]byte{start("", "item_2", `{"input":1}`), start("functions__apply_patch", "item_2", `{"input":"a"}`)}, true, ""},
		{"truncated snapshot before name retained", [][]byte{start("", "item_2", `{"input":"a`), start("functions__apply_patch", "item_2", `{"input":"a"}`)}, true, ""},
		{"conflicting ID before name retained", [][]byte{start("", "item_2", ""), start("", "wrong", ""), start("functions__apply_patch", "item_2", `{"input":"a"}`)}, true, ""},
		{"unknown-name fragments replayed", [][]byte{start("", "item_2", ""), delta(`{"input":"a`), start("functions__apply_patch", "item_2", ""), delta(`b"}`), stop(`{"input":"ab"}`), final(`{"input":"ab"}`)}, false, "ab"},
		{"completed item conflicts at final", [][]byte{start("functions__apply_patch", "item_2", `{"input":"a"}`), stop(`{"input":"a"}`), final(`{"input":"ab"}`)}, true, ""},
		{"completed item conflicts at repeated stop", [][]byte{start("functions__apply_patch", "item_2", `{"input":"a"}`), stop(`{"input":"a"}`), stop(`{"input":"ab"}`)}, true, ""},
		{"completed item rejects later delta", [][]byte{start("functions__apply_patch", "item_2", `{"input":"a"}`), stop(`{"input":"a"}`), delta(`{"input":"ab"}`)}, true, ""},
		{"truncated delta cannot complete without snapshot", [][]byte{start("functions__apply_patch", "item_2", ""), delta(`{"input":"a`), patchStep("step.stop", nil)}, true, ""},
		{"illegal delta remains failed", [][]byte{start("functions__apply_patch", "item_2", ""), delta(`{"input":1}`), stop(`{"input":"a"}`)}, true, ""},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var param any
			var events []gjson.Result
			for _, input := range test.inputs {
				events = append(events, patchSend(&param, input)...)
			}
			failures := 0
			for _, ev := range events {
				if ev.Get("type").String() == "response.failed" {
					failures++
					if ev.Get("response.error.code").String() != "invalid_tool_arguments" {
						t.Fatal(ev.Raw)
					}
				}
			}
			if test.fail {
				if failures != 1 {
					t.Fatalf("failures=%d events=%v", failures, events)
				}
				if state, ok := param.(interface{ ToolInputError() error }); !ok || state.ToolInputError() == nil {
					t.Fatal("missing error")
				}
			} else {
				if failures != 0 {
					t.Fatalf("unexpected failure: %v", events)
				}
				assertPatchLifecycle(t, events, test.want)
			}
			for _, input := range [][]byte{[]byte(`{"event_type":"interaction.completed"}`), []byte(`{"event_type":"interaction.failed"}`), []byte(`[DONE]`), patchStep("step.start", map[string]any{"type": "model_output"}), delta("late")} {
				if more := patchSend(&param, input); len(more) != 0 {
					t.Fatalf("terminal reopened: %v", more)
				}
			}
		})
	}
}

func TestInteractionsApplyPatchWinningDeclarationAndNegativeCompatibility(t *testing.T) {
	cases := []struct {
		name, request, upstream, args, wantType, wantInput string
		patch                                              bool
	}{
		{"top function beats additional custom", `{"tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object","properties":{"n":{"type":"number"}}}}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "apply_patch", `{"n":1}`, "function_call", "", false},
		{"direct function beats namespace custom", `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"functions__apply_patch"}]}`, "functions__apply_patch", `{"n":1}`, "function_call", "", false},
		{"other custom remains lenient", `{"tools":[{"type":"custom","name":"exec"}]}`, "exec", `{"command":"ls"}`, "custom_tool_call", `{"command":"ls"}`, false},
		{"sanitization collision keeps qualified Interactions name", `{"tools":[{"type":"namespace","name":"a.b","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"a_b__apply_patch"}]}`, "a.b__apply_patch", `{"input":"p"}`, "custom_tool_call", "p", true},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			request := []byte(test.request)
			body2, errConvert2 := ConvertOpenAIResponsesRequestToInteractions("devin/swe-2", request, false)
			if errConvert2 != nil {
				t.Fatal(errConvert2)
			}
			translated := gjson.ParseBytes(body2)
			declaration := translated.Get("tools.0")
			if strings.Contains(declaration.Get("description").String(), "*** Begin Patch") != test.patch {
				t.Fatalf("wrong contract: %s", translated.Raw)
			}
			var param any
			send := func(raw []byte) []gjson.Result {
				return patchEvents(ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, raw, &param))
			}
			send(patchStep("step.start", map[string]any{"type": "function_call", "id": "c", "name": test.upstream}))
			send(patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": test.args}}))
			send(patchStep("step.stop", nil))
			events := send([]byte(`{"event_type":"interaction.completed"}`))
			item := events[len(events)-1].Get("response.output.0")
			if item.Get("type").String() != test.wantType || item.Get("input").String() != test.wantInput {
				t.Fatalf("compatibility changed: %s", item.Raw)
			}
			if test.wantType == "function_call" && item.Get("arguments").String() != test.args {
				t.Fatalf("function arguments changed: %s", item.Raw)
			}
		})
	}
}

func TestInteractionsApplyPatchMalformedEnvelopeAndTerminalFailure(t *testing.T) {
	var param any
	patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch"}))
	events := patchSend(&param, []byte(`{"event_type":"step.stop","index":2,"step":{"arguments":{"input":"secret"}}`))
	if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
		t.Fatalf("malformed complete snapshot accepted: %v", events)
	}
	if more := patchSend(&param, []byte(`{"event_type":"interaction.completed"}`)); len(more) != 0 {
		t.Fatal("failure reopened")
	}
	var nonStream any
	out := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin", []byte(patchRequest), nil, []byte(`{"id":"r","steps":[{"type":"function_call","name":"functions__apply_patch","arguments":{"input":"secret"}}]`), &nonStream)
	if len(out) != 0 {
		t.Fatalf("malformed non-stream accepted: %s", out)
	}
}

func TestInteractionsApplyPatchDeltaBeforeStartDoesNotLeakArguments(t *testing.T) {
	var param any
	delta := patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": `{"input":"a`}})
	if events := patchSend(&param, delta); len(events) != 0 {
		t.Fatalf("raw arguments emitted before tool identity: %v", events)
	}
	var events []gjson.Result
	events = append(events, patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch"}))...)
	events = append(events, patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": `b"}`}}))...)
	events = append(events, patchSend(&param, patchStep("step.stop", nil))...)
	events = append(events, patchSend(&param, []byte(`{"event_type":"interaction.completed"}`))...)
	assertPatchLifecycle(t, events, "ab")
}

func TestInteractionsApplyPatchInitialSnapshotDoesNotFakePreview(t *testing.T) {
	var param any
	events := patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
	for _, ev := range events {
		if ev.Get("type").String() == "response.custom_tool_call_input.delta" {
			t.Fatal("initial complete snapshot presented as early generation")
		}
	}
}

func TestInteractionsApplyPatchEarlyPreviewBeforeJSONCompletion(t *testing.T) {
	var param any
	patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch"}))
	events := patchSend(&param, patchJSON(map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": `{"input":"*** Begin Patch\n`}}))
	if len(events) != 1 || events[0].Get("delta").String() != "*** Begin Patch\n" || events[0].Get("type").String() != "response.custom_tool_call_input.delta" {
		t.Fatalf("no early preview: %v", events)
	}
}

func TestInteractionsApplyPatchLateIdentityAtStopAndCompletedItemTypeConflict(t *testing.T) {
	var param any
	patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "arguments": map[string]any{"input": "p"}}))
	if ev := patchSend(&param, patchStep("step.stop", nil)); len(ev) != 0 {
		t.Fatalf("unknown tool completed with raw arguments: %v", ev)
	}
	events := patchSend(&param, patchStep("step.stop", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "p"}}))
	if len(events) != 4 {
		t.Fatalf("late identity did not complete: %v", events)
	}
	events = patchSend(&param, patchStep("step.start", map[string]any{"type": "model_output", "id": "item_2"}))
	if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
		t.Fatalf("completed item type conflict ignored: %v", events)
	}
}

func TestInteractionsApplyPatchDoesNotChangeOtherFunctionNames(t *testing.T) {
	var param any
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"read_file"}]}`)
	chunks := ConvertInteractionsResponseToOpenAIResponses(context.Background(), "devin/swe-2", request, nil, patchStep("step.start", map[string]any{"type": "function_call", "id": "c", "name": "external_read_file"}), &param)
	events := patchEvents(chunks)
	if len(events) != 1 || events[0].Get("item.name").String() != "external_read_file" {
		t.Fatalf("borrowed Antigravity name alias: %v", events)
	}
}

func TestInteractionsApplyPatchInterleavedCallsRemainIndependent(t *testing.T) {
	var param any
	var events []gjson.Result
	send := func(index int, event string, value any) {
		key := "step"
		if event == "step.delta" {
			key = "delta"
		}
		events = append(events, patchSend(&param, patchJSON(map[string]any{"event_type": event, "index": index, key: value}))...)
	}
	for index, name := range []string{"c1", "c2"} {
		send(index, "step.start", map[string]any{"type": "function_call", "id": name, "call_id": "call_" + name, "name": "functions__apply_patch"})
	}
	send(0, "step.delta", map[string]any{"type": "arguments_delta", "arguments": `{"input":"a`})
	send(1, "step.delta", map[string]any{"type": "arguments_delta", "arguments": `{"input":"b`})
	send(0, "step.delta", map[string]any{"type": "arguments_delta", "arguments": `1"}`})
	send(1, "step.delta", map[string]any{"type": "arguments_delta", "arguments": `2"}`})
	send(1, "step.stop", nil)
	send(0, "step.stop", nil)
	events = append(events, patchSend(&param, []byte(`{"event_type":"interaction.completed"}`))...)
	inputs := map[string]string{}
	for _, ev := range events {
		if ev.Get("type").String() == "response.custom_tool_call_input.delta" {
			id := ev.Get("item_id").String()
			inputs[id] += ev.Get("delta").String()
			if ev.Get("call_id").String() != "call_"+id {
				t.Fatal(ev.Raw)
			}
		}
		if ev.Get("type").String() == "response.custom_tool_call_input.done" && ev.Get("input").String() != inputs[ev.Get("item_id").String()] {
			t.Fatal(ev.Raw)
		}
	}
	final := events[len(events)-1]
	if inputs["c1"] != "a1" || inputs["c2"] != "b2" || final.Get("response.output.0.input").String() != "a1" || final.Get("response.output.1.input").String() != "b2" {
		t.Fatalf("cross-call contamination: inputs=%v final=%s", inputs, final.Raw)
	}
}

func TestInteractionsApplyPatchUpstreamFailureClosesEveryEvent(t *testing.T) {
	var param any
	patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch"}))
	events := patchSend(&param, []byte(`{"event_type":"interaction.failed","error":{"message":"upstream failed"}}`))
	if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
		t.Fatal(events)
	}
	for _, raw := range [][]byte{[]byte(`{"event_type":"interaction.completed"}`), []byte(`{"event_type":"interaction.failed"}`), []byte(`[DONE]`), patchStep("step.stop", nil)} {
		if more := patchSend(&param, raw); len(more) != 0 {
			t.Fatalf("failed response reopened: %v", more)
		}
	}
}

func TestInteractionsApplyPatchUnresolvedIdentityCannotComplete(t *testing.T) {
	var param any
	patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "c1", "arguments": map[string]any{"input": "secret"}}))
	events := patchSend(&param, []byte(`{"event_type":"interaction.completed"}`))
	if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
		t.Fatalf("unresolved identity accepted: %v", events)
	}
}

func TestInteractionsApplyPatchCompletedItemRejectsEmptySnapshot(t *testing.T) {
	var param any
	patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{"input": "a"}}))
	patchSend(&param, patchStep("step.stop", nil))
	events := patchSend(&param, patchStep("step.start", map[string]any{"type": "function_call", "id": "item_2", "call_id": "call_2", "name": "functions__apply_patch", "arguments": map[string]any{}}))
	if len(events) != 1 || events[0].Get("type").String() != "response.failed" {
		t.Fatalf("complete empty snapshot was treated as initial placeholder: %v", events)
	}
}

func TestInteractionsApplyPatchNonStreamRejectsUnresolvedIdentity(t *testing.T) {
	var param any
	raw := []byte(`{"steps":[{"type":"function_call","id":"c1","arguments":{"input":"secret"}}]}`)
	if out := ConvertInteractionsResponseToOpenAIResponsesNonStream(context.Background(), "devin", []byte(patchRequest), nil, raw, &param); len(out) != 0 {
		t.Fatalf("unresolved identity accepted: %s", out)
	}
	if state, ok := param.(interface{ ToolInputError() error }); !ok || state.ToolInputError() == nil {
		t.Fatal("missing error state")
	}
}
