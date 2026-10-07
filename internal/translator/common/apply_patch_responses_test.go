package common

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	"github.com/tidwall/gjson"
)

var patchResponsesRequest = []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)

func patchJSON(value any) string { raw, _ := json.Marshal(value); return string(raw) }
func patchItem(kind, id, call, name, args string) string {
	return fmt.Sprintf(`{"type":%s,"id":%s,"call_id":%s,"name":%s,"arguments":%s}`, patchJSON(kind), patchJSON(id), patchJSON(call), patchJSON(name), patchJSON(args))
}
func patchEvent(kind string, index int, item string) []byte {
	return []byte(fmt.Sprintf(`{"type":%s,"output_index":%d,"item":%s}`, patchJSON(kind), index, item))
}
func patchSend(t *testing.T, b *ApplyPatchResponsesBridge, event []byte) [][]byte {
	t.Helper()
	out, errTransform := b.Transform(event)
	if errTransform != nil {
		t.Fatalf("Transform(%s): %v", event, errTransform)
	}
	return out
}

// Raw source fragments must survive decoding without inventing a final snapshot.
func TestApplyPatchResponsesBridgeDeltaAndFourCompletions(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprintf("late=%v", late), func(t *testing.T) {
			b := NewApplyPatchResponsesBridge(patchResponsesRequest)
			name := "apply_patch"
			if late {
				name = ""
			}
			var events [][]byte
			events = append(events, patchSend(t, b, patchEvent("response.output_item.added", 0, patchItem("function_call", "", "", name, "")))...)
			args := `{"input":"*** Begin Patch\n+中文\uD83D\uDE00 \"\\\n*** End Patch\n"}`
			for _, part := range []string{args[:18], args[18:35], args[35:41], args[41:]} {
				events = append(events, patchSend(t, b, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%s}`, patchJSON(part))))...)
			}
			item := patchItem("function_call", "fc1", "c1", "apply_patch", args)
			events = append(events, patchSend(t, b, patchEvent("response.output_item.done", 0, item))...)
			events = append(events, patchSend(t, b, []byte(`{"type":"response.completed","response":{"id":"r1","output":[`+item+`]}}`))...)
			want, _ := applypatch.UnwrapInput(args)
			var delta strings.Builder
			counts := map[string]int{}
			lastSequence := int64(-1)
			for _, event := range events {
				root := gjson.ParseBytes(event)
				kind := root.Get("type").String()
				counts[kind]++
				seq := root.Get("sequence_number").Int()
				if seq <= lastSequence {
					t.Fatalf("sequence not increasing: %s", event)
				}
				lastSequence = seq
				switch kind {
				case "response.custom_tool_call_input.delta":
					delta.WriteString(root.Get("delta").String())
				case "response.custom_tool_call_input.done":
					if root.Get("input").String() != want || root.Get("item_id").String() != "fc1" || root.Get("call_id").String() != "c1" {
						t.Fatalf("input done: %s", event)
					}
				case "response.output_item.done":
					if root.Get("item.input").String() != want || root.Get("item.arguments").Exists() {
						t.Fatalf("item done: %s", event)
					}
				case "response.completed":
					if root.Get("response.output.0.input").String() != want {
						t.Fatalf("envelope: %s", event)
					}
				}
			}
			if delta.String() != want || counts["response.custom_tool_call_input.done"] != 1 || counts["response.output_item.done"] != 1 || counts["response.completed"] != 1 {
				t.Fatalf("counts=%v delta=%q want=%q", counts, delta.String(), want)
			}
			if errFinish := b.Finish(); errFinish != nil {
				t.Fatal(errFinish)
			}
			if more := patchSend(t, b, []byte(`{"type":"response.completed","response":{"output":[]}}`)); len(more) != 0 {
				t.Fatalf("duplicate terminal: %s", more)
			}
		})
	}
}

func TestApplyPatchResponsesBridgePassthrough(t *testing.T) {
	for _, req := range [][]byte{patchResponsesRequest, []byte(`{"tools":[{"type":"function","name":"apply_patch"}]}`), []byte(`{}`)} {
		b := NewApplyPatchResponsesBridge(req)
		for _, event := range [][]byte{
			[]byte(`{ "type":"response.output_item.added", "output_index":0,"item":{"type":"custom_tool_call","id":"native","name":"apply_patch","input":""}}`),
			[]byte(`{ "type":"response.custom_tool_call_input.delta", "item_id":"native","delta":"raw patch"}`),
			patchEvent("response.output_item.done", 1, patchItem("function_call", "ordinary", "ordinary", "lookup", `{"x":1}`)),
		} {
			out := patchSend(t, b, event)
			if len(out) != 1 || string(out[0]) != string(event) {
				t.Fatalf("changed native/ordinary: %s", out)
			}
		}
	}
}

func TestApplyPatchResponsesBridgeIdentityAndSnapshotEvidence(t *testing.T) {
	cases := []struct {
		name   string
		before [][]byte
		after  []byte
	}{
		{"index-item", [][]byte{patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "apply_patch", "")), patchEvent("response.output_item.added", 1, patchItem("function_call", "b", "cb", "lookup", ""))}, []byte(`{"type":"response.function_call_arguments.delta","output_index":1,"item_id":"a","delta":"{}"}`)},
		{"call-item", [][]byte{patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "apply_patch", ""))}, []byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"a","call_id":"other","arguments":"{\"input\":\"p\"}"}`)},
		{"type-before-name", [][]byte{patchEvent("response.output_item.added", 0, patchItem("message", "a", "ca", "", ""))}, patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "ca", "apply_patch", `{"input":"p"}`))},
		{"invalid-before-name", [][]byte{patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "", `{"input":"p","extra":1}`))}, patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "ca", "apply_patch", `{"input":"p"}`))},
		{"partial-snapshot", nil, patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "apply_patch", `{"input":"p`))},
		{"invalid-final-only", nil, []byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","name":"apply_patch","arguments":"{}"}]}}`)},
		{"old-patch-new-type", [][]byte{patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "apply_patch", ""))}, []byte(`{"type":"response.completed","response":{"output":[{"type":"message","id":"a","content":[]}]}}`)},
		{"pending-id-evidence", [][]byte{patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "", ""))}, patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "changed", "apply_patch", `{"input":"p"}`))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := NewApplyPatchResponsesBridge(patchResponsesRequest)
			for _, e := range tc.before {
				patchSend(t, b, e)
			}
			out, errTransform := b.Transform(tc.after)
			if errTransform == nil || len(out) != 1 || gjson.GetBytes(out[0], "type").String() != "response.failed" || b.ToolInputError() == nil {
				t.Fatalf("unsafe evidence accepted: %s %v", out, errTransform)
			}
			if more, errMore := b.Transform([]byte(`{"type":"response.completed","response":{"output":[]}}`)); len(more) != 0 || errMore != nil {
				t.Fatalf("post-failure output: %s %v", more, errMore)
			}
		})
	}
}

func TestApplyPatchResponsesBridgeStagesAndFinish(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	patchSend(t, b, patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "apply_patch", "")))
	if errFinish := b.Finish(); errFinish == nil {
		t.Fatal("incomplete call accepted")
	}
	b = NewApplyPatchResponsesBridge(patchResponsesRequest)
	for i := 0; i < 2; i++ {
		patchSend(t, b, patchEvent("response.output_item.added", i, patchItem("function_call", fmt.Sprint("a", i), fmt.Sprint("c", i), "apply_patch", "")))
		args := applypatch.WrapInput(fmt.Sprint("patch", i))
		out := patchSend(t, b, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.done","output_index":%d,"arguments":%s}`, i, patchJSON(args))))
		if len(out) != 1 {
			t.Fatalf("missing input.done: %s", out)
		}
		out = patchSend(t, b, patchEvent("response.output_item.done", i, patchItem("function_call", fmt.Sprint("a", i), fmt.Sprint("c", i), "apply_patch", args)))
		if len(out) != 1 || gjson.GetBytes(out[0], "item.input").String() != fmt.Sprint("patch", i) {
			t.Fatalf("duplicate arguments: %s", out)
		}
		if out = patchSend(t, b, patchEvent("response.output_item.done", i, patchItem("function_call", fmt.Sprint("a", i), fmt.Sprint("c", i), "apply_patch", args))); len(out) != 0 {
			t.Fatalf("duplicate done: %s", out)
		}
	}
	if errFinish := b.Finish(); errFinish != nil {
		t.Fatal(errFinish)
	}
}

func TestApplyPatchResponsesRequestHistoryAndWinners(t *testing.T) {
	for _, req := range []string{
		`{"tools":[{"type":"custom","name":"apply_patch"}],"input":[]}`,
		`{"tools":[{"type":"function","name":"apply_patch"}],"input":[]}`,
		`{"input":[]}`,
	} {
		root := gjson.Parse(req)
		_ = root
		raw := strings.Replace(req, `"input":[]`, `"input":[{"type":"custom_tool_call","call_id":"old","name":"apply_patch","input":"{\"input\":\"raw\"}"},{"type":"custom_tool_call_output","call_id":"old","output":"ok"},{"type":"function_call","call_id":"fn","name":"apply_patch","arguments":"{\"input\":\"existing\"}"}]`, 1)
		out, errNormalize := NormalizeApplyPatchResponsesRequest([]byte(raw))
		if errNormalize != nil {
			t.Fatal(errNormalize)
		}
		if gjson.GetBytes(out, "input.0.arguments").String() != applypatch.WrapInput(`{"input":"raw"}`) || gjson.GetBytes(out, "input.1.type").String() != "function_call_output" || gjson.GetBytes(out, "input.2.arguments").String() != `{"input":"existing"}` {
			t.Fatalf("history: %s", out)
		}
		if strings.Contains(req, `"type":"function"`) && gjson.GetBytes(out, "tools.0.parameters").Exists() {
			t.Fatalf("ordinary declaration changed: %s", out)
		}
	}
	for _, req := range []string{
		`{"tools":[{"type":"function","name":"apply_patch","description":"ordinary"}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`,
		`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"n__apply_patch","description":"ordinary"}]}`,
	} {
		out, errNormalize := NormalizeApplyPatchResponsesRequest([]byte(req))
		if errNormalize != nil {
			t.Fatal(errNormalize)
		}
		winners := NewApplyPatchResponsesBridge([]byte(req))
		item := patchItem("function_call", "a", "c", "apply_patch", `{"x":1}`)
		if strings.Contains(req, `"namespace"`) {
			item = patchItem("function_call", "a", "c", "n__apply_patch", `{"x":1}`)
		}
		got := patchSend(t, winners, patchEvent("response.output_item.done", 0, item))
		if len(got) != 1 || gjson.GetBytes(got[0], "item.type").String() != "function_call" {
			t.Fatalf("loser stole ordinary identity: %s normalized=%s", got, out)
		}
	}
}

func TestApplyPatchResponsesNamespaceMixedNonStream(t *testing.T) {
	req := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}]}`)
	b := NewApplyPatchResponsesBridge(req)
	raw := []byte(`{"id":"r","output":[` + patchItem("function_call", "a", "c", "n__apply_patch", applypatch.WrapInput("p")) + `,` + patchItem("function_call", "b", "d", "n__lookup", `{"x":1}`) + `]}`)
	out, errTransform := b.TransformNonStream(raw)
	if errTransform != nil {
		t.Fatal(errTransform)
	}
	if gjson.GetBytes(out, "output.0.type").String() != "custom_tool_call" || gjson.GetBytes(out, "output.0.name").String() != "apply_patch" || gjson.GetBytes(out, "output.0.namespace").String() != "n" || gjson.GetBytes(out, "output.1.name").String() != "lookup" || gjson.GetBytes(out, "output.1.namespace").String() != "n" || gjson.GetBytes(out, "output.1.arguments").String() != `{"x":1}` {
		t.Fatalf("namespace routing: %s", out)
	}
}

func TestApplyPatchResponsesContinuationMissingSnapshots(t *testing.T) {
	for _, source := range []string{"deltas", "arguments.done"} {
		t.Run(source, func(t *testing.T) {
			b := NewApplyPatchResponsesBridge(patchResponsesRequest)
			patchSend(t, b, patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "c", "apply_patch", "")))
			kind, field := "response.function_call_arguments.done", "arguments"
			if source == "deltas" {
				kind, field = "response.function_call_arguments.delta", "delta"
			}
			patchSend(t, b, []byte(fmt.Sprintf(`{"type":%s,"item_id":"a",%s:%s}`, patchJSON(kind), patchJSON(field), patchJSON(applypatch.WrapInput("p")))))
			out := patchSend(t, b, []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"apply_patch"}}`))
			if gjson.GetBytes(out[len(out)-1], "item.input").String() != "p" {
				t.Fatalf("missing snapshot: %s", out)
			}
			out = patchSend(t, b, []byte(`{"type":"response.completed","response":{"output":[]}}`))
			if gjson.GetBytes(out[len(out)-1], "response.output.0.input").String() != "p" {
				t.Fatalf("omitted completed item: %s", out)
			}
		})
	}
}

func TestApplyPatchResponsesContinuationNativeTerminalBytes(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	for _, e := range [][]byte{
		[]byte(`{ "type":"response.output_item.done", "output_index":0, "item":{"type":"custom_tool_call","id":"n","name":"apply_patch","input":"raw"}}`),
		[]byte(`{ "type":"response.completed", "sequence_number":71, "response": {"output":[{"type":"custom_tool_call","id":"n","name":"apply_patch","input":"raw"}]}}`),
	} {
		out := patchSend(t, b, e)
		if len(out) != 1 || string(out[0]) != string(e) {
			t.Fatalf("native bytes changed: %s", out)
		}
	}
}

func TestApplyPatchResponsesContinuationRootLateName(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	patchSend(t, b, []byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a"}}`))
	out := patchSend(t, b, []byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"a","call_id":"c","name":"apply_patch","arguments":"{\"input\":\"p\"}"}`))
	if len(out) == 0 || gjson.GetBytes(out[len(out)-1], "type").String() != "response.custom_tool_call_input.done" {
		t.Fatalf("late root name: %s", out)
	}
	if errFinish := b.Finish(); errFinish != nil {
		t.Fatal(errFinish)
	}
}

func TestApplyPatchResponsesContinuationNoInventedPreview(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	out := patchSend(t, b, patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "c", "apply_patch", applypatch.WrapInput("p"))))
	for _, e := range out {
		if gjson.GetBytes(e, "type").String() == "response.custom_tool_call_input.delta" {
			t.Fatalf("invented progress: %s", out)
		}
	}
}

func TestApplyPatchResponsesContinuationAllMatchedProvenance(t *testing.T) {
	// Every key can select patch provenance, including a record other than the first match.
	keys := []string{"output_index", "item_id", "call_id"}
	for _, patchKey := range keys {
		for _, firstKey := range keys {
			if firstKey == patchKey {
				continue
			}
			t.Run(patchKey+"-"+firstKey, func(t *testing.T) {
				b := NewApplyPatchResponsesBridge(patchResponsesRequest)
				patchSend(t, b, patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "lookup", "")))
				patchSend(t, b, patchEvent("response.output_item.added", 1, patchItem("function_call", "b", "cb", "apply_patch", "")))
				vals := map[string]string{"output_index": "0", "item_id": `"a"`, "call_id": `"ca"`}
				vals[patchKey] = map[string]string{"output_index": "1", "item_id": `"b"`, "call_id": `"cb"`}[patchKey]
				e := []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":%s,"item_id":%s,"call_id":%s,"delta":"{}"}`, vals["output_index"], vals["item_id"], vals["call_id"]))
				out, errTransform := b.Transform(e)
				if errTransform == nil || len(out) != 1 || gjson.GetBytes(out[0], "type").String() != "response.failed" {
					t.Fatalf("lost matched provenance: %s %v", out, errTransform)
				}
			})
		}
	}
	for _, discover := range []int{0, 1, 2} {
		t.Run(fmt.Sprint("pending-", discover), func(t *testing.T) {
			b := NewApplyPatchResponsesBridge(patchResponsesRequest)
			for i := 0; i < 3; i++ {
				patchSend(t, b, patchEvent("response.output_item.added", i, patchItem("function_call", fmt.Sprint("i", i), fmt.Sprint("c", i), "", "")))
			}
			patchSend(t, b, []byte(`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"i1","call_id":"c2","delta":""}`))
			out, errTransform := b.Transform(patchEvent("response.output_item.done", discover, patchItem("function_call", fmt.Sprint("i", discover), fmt.Sprint("c", discover), "apply_patch", applypatch.WrapInput("p"))))
			if errTransform == nil || len(out) != 1 {
				t.Fatalf("pending evidence lost: %s %v", out, errTransform)
			}
		})
	}
}

func TestApplyPatchResponsesContinuationCompletedWindow(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(fmt.Sprint(terminal), func(t *testing.T) {
			b := NewApplyPatchResponsesBridge(patchResponsesRequest)
			item := patchItem("function_call", "a", "c", "apply_patch", applypatch.WrapInput("p"))
			patchSend(t, b, patchEvent("response.output_item.done", 0, item))
			if terminal {
				patchSend(t, b, []byte(`{"type":"response.completed","response":{"output":[`+item+`]}}`))
			}
			out, errTransform := b.Transform(patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "c", "apply_patch", applypatch.WrapInput("different"))))
			if terminal {
				if len(out) != 0 || errTransform != nil {
					t.Fatalf("closed response: %s %v", out, errTransform)
				}
			} else if errTransform == nil {
				t.Fatalf("contradiction after item.done: %s", out)
			}
		})
	}
}

func TestApplyPatchResponsesContinuationOmittedMixedCompletedItems(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	patchSend(t, b, patchEvent("response.output_item.done", 0, patchItem("function_call", "p", "cp", "apply_patch", applypatch.WrapInput("p"))))
	ordinary := `{"type":"message","id":"m","content":[{"type":"output_text","text":"ok"}]}`
	patchSend(t, b, patchEvent("response.output_item.done", 1, ordinary))
	out := patchSend(t, b, []byte(`{"type":"response.completed","response":{"output":[]}}`))
	if gjson.GetBytes(out[len(out)-1], "response.output.0.input").String() != "p" || gjson.GetBytes(out[len(out)-1], "response.output.1.id").String() != "m" {
		t.Fatalf("completed output lost: %s", out)
	}
	b = NewApplyPatchResponsesBridge(patchResponsesRequest)
	patchSend(t, b, patchEvent("response.output_item.done", 0, patchItem("function_call", "p", "cp", "apply_patch", applypatch.WrapInput("p"))))
	out = patchSend(t, b, []byte(`{"type":"response.completed","response":{"output":[`+ordinary+`]}}`))
	if gjson.GetBytes(out[len(out)-1], "response.output.0.input").String() != "p" || gjson.GetBytes(out[len(out)-1], "response.output.1.id").String() != "m" {
		t.Fatalf("new final output collided: %s", out)
	}
}

func TestApplyPatchResponsesContinuationUnmatchedIdentityEvidence(t *testing.T) {
	for _, known := range []bool{false, true} {
		for _, key := range []string{`"item_id":"b"`, `"call_id":"cb"`, `"output_index":1`} {
			t.Run(fmt.Sprintf("known=%v/%s", known, key), func(t *testing.T) {
				b := NewApplyPatchResponsesBridge(patchResponsesRequest)
				if known {
					patchSend(t, b, patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "ca", "lookup", "")))
				}
				patchSend(t, b, []byte(`{"type":"response.output_item.added","output_index":1,"item_id":"a","call_id":"ca","item":{"type":"function_call","id":"b","call_id":"cb","name":""}}`))
				out, errTransform := b.Transform([]byte(`{"type":"response.function_call_arguments.done",` + key + `,"name":"apply_patch","arguments":"{\"input\":\"p\"}"}`))
				if errTransform == nil || len(out) != 1 || gjson.GetBytes(out[0], "type").String() != "response.failed" {
					t.Fatalf("unmatched-key evidence lost: %s %v", out, errTransform)
				}
			})
		}
	}
}

func TestApplyPatchResponsesContinuationMixedSequence(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	var events [][]byte
	for _, raw := range []string{
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":""}}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":1,"item":{"type":"function_call","id":"b","name":"lookup","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","sequence_number":3,"output_index":0,"item_id":"a","arguments":"{\"input\":\"p\"}"}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":1,"item":{"type":"function_call","id":"b","name":"lookup","arguments":"{\"x\":1}"}}`,
		`{"type":"response.completed","sequence_number":5,"response":{"output":[]}}`,
	} {
		events = append(events, patchSend(t, b, []byte(raw))...)
	}
	last := int64(-1)
	for _, event := range events {
		seq := gjson.GetBytes(event, "sequence_number").Int()
		if seq <= last {
			t.Fatalf("nonmonotonic mixed sequence: %s", events)
		}
		last = seq
	}
}

func TestApplyPatchResponsesContinuationOrdinaryRootNamePassthrough(t *testing.T) {
	req := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}]}`)
	b := NewApplyPatchResponsesBridge(req)
	raw := []byte(`{ "type":"response.function_call_arguments.done", "output_index":0,"name":"lookup","namespace":"n","arguments":"{}"}`)
	out := patchSend(t, b, raw)
	if len(out) != 1 || string(out[0]) != string(raw) {
		t.Fatalf("ordinary root name changed: %s", out)
	}
}

func TestApplyPatchResponsesContinuationMixedNativeBytes(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	patchSend(t, b, []byte(`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"a","name":"apply_patch","arguments":""}}`))
	native := []byte(`  { "type":"response.output_item.added", "sequence_number":2, "output_index":1,"item":{"type":"custom_tool_call","id":"n","name":"apply_patch","input":""}}  `)
	out := patchSend(t, b, native)
	if len(out) != 1 || string(out[0]) != string(native) {
		t.Fatalf("native custom became bridge-owned: %s", out)
	}
}
