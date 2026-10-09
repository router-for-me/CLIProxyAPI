package common

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// A known name does not make either missing downstream ID safe to publish.
func TestApplyPatchResponsesNamedLateIdentity(t *testing.T) {
	for _, first := range []string{"item", "call", "neither", "neither-item-first", "neither-call-first"} {
		for _, boundary := range []string{"item", "terminal"} {
			t.Run(first+"-"+boundary, func(t *testing.T) {
				b := NewApplyPatchResponsesBridge(patchResponsesRequest)
				id, call := "", ""
				if first == "item" {
					id = "a"
				}
				if first == "call" {
					call = "c"
				}
				pending := [][]byte{patchEvent("response.output_item.added", 0, patchItem("function_call", id, call, "apply_patch", ""))}
				for _, fragment := range []string{`{"input":"p`, `q"}`} {
					pending = append(pending, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":0,"delta":%s}`, patchJSON(fragment))))
				}
				pending = append(pending, []byte(`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"input\":\"pq\"}"}`))
				for _, event := range pending {
					if out := patchSend(t, b, event); len(out) != 0 {
						t.Fatalf("published unresolved identity: %s", out)
					}
				}
				if strings.HasPrefix(first, "neither-") {
					id, call = "a", ""
					if first == "neither-call-first" {
						id, call = "", "c"
					}
					if out := patchSend(t, b, patchEvent("response.output_item.added", 0, patchItem("function_call", id, call, "apply_patch", ""))); len(out) != 0 {
						t.Fatalf("one late ID is still unresolved: %s", out)
					}
				}
				item := patchItem("function_call", "a", "c", "apply_patch", `{"input":"pq"}`)
				var events [][]byte
				if boundary == "item" {
					events = append(events, patchSend(t, b, patchEvent("response.output_item.done", 0, item))...)
				}
				events = append(events, patchSend(t, b, []byte(`{"type":"response.completed","response":{"output":[`+item+`]}}`))...)
				counts := map[string]int{}
				var fragments []string
				for _, event := range events {
					root := gjson.ParseBytes(event)
					kind := root.Get("type").String()
					counts[kind]++
					identity := root
					idKey := "item_id"
					switch kind {
					case "response.output_item.added", "response.output_item.done":
						identity, idKey = root.Get("item"), "id"
						wantInput := ""
						if kind == "response.output_item.done" {
							wantInput = "pq"
						}
						if identity.Get("input").String() != wantInput {
							t.Fatalf("item input: %s", event)
						}
					case "response.completed":
						identity, idKey = root.Get("response.output.0"), "id"
						if identity.Get("input").String() != "pq" {
							t.Fatalf("final input: %s", event)
						}
					case "response.custom_tool_call_input.done":
						if root.Get("input").String() != "pq" {
							t.Fatalf("done input: %s", event)
						}
					}
					if kind != "response.completed" && (!root.Get("output_index").Exists() || root.Get("output_index").Int() != 0) {
						t.Fatalf("unstable index: %s", event)
					}
					if identity.Get(idKey).String() != "a" || identity.Get("call_id").String() != "c" {
						t.Fatalf("changed lifecycle identity: %s", event)
					}
					if kind == "response.custom_tool_call_input.delta" {
						fragments = append(fragments, root.Get("delta").String())
					}
				}
				if strings.Join(fragments, "|") != "p|q" {
					t.Fatalf("not real buffered fragments: %q", fragments)
				}
				for _, kind := range []string{"response.output_item.added", "response.custom_tool_call_input.done", "response.output_item.done", "response.completed"} {
					if counts[kind] != 1 {
						t.Fatalf("lifecycle counts: %v", counts)
					}
				}
			})
		}
	}
}

func TestApplyPatchResponsesNamedLateIdentityEvidence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after []byte
	}{
		{"seen-item-change", patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "", "apply_patch", "")), patchEvent("response.output_item.done", 0, patchItem("function_call", "changed", "c", "apply_patch", `{"input":"pq"}`))},
		{"seen-call-change", patchEvent("response.output_item.added", 0, patchItem("function_call", "", "c", "apply_patch", "")), patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "changed", "apply_patch", `{"input":"pq"}`))},
		{"partial-snapshot", patchEvent("response.output_item.added", 0, patchItem("function_call", "", "", "apply_patch", `{"input":"p`)), patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "c", "apply_patch", `{"input":"pq"}`))},
		{"invalid-snapshot", patchEvent("response.output_item.added", 0, patchItem("function_call", "a", "", "apply_patch", `{"input":"pq","extra":1}`)), patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "c", "apply_patch", `{"input":"pq"}`))},
		{"unresolved-terminal", patchEvent("response.output_item.done", 0, patchItem("function_call", "a", "", "apply_patch", `{"input":"pq"}`)), []byte(`{"type":"response.completed","response":{"output":[]}}`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := NewApplyPatchResponsesBridge(patchResponsesRequest)
			out, errTransform := b.Transform(tc.before)
			if errTransform == nil {
				if len(out) != 0 {
					t.Fatalf("unresolved output: %s", out)
				}
				out, errTransform = b.Transform(tc.after)
			}
			if errTransform == nil || len(out) != 1 || gjson.GetBytes(out[0], "type").String() != "response.failed" {
				t.Fatalf("lost evidence: %s %v", out, errTransform)
			}
			if out, errTransform = b.Transform(tc.after); len(out) != 0 || errTransform != nil {
				t.Fatalf("failure reopened: %s %v", out, errTransform)
			}
		})
	}
}

func TestApplyPatchResponsesNamedLateIdentityInterleaved(t *testing.T) {
	b := NewApplyPatchResponsesBridge(patchResponsesRequest)
	for i, item := range []string{patchItem("function_call", "a0", "", "apply_patch", ""), patchItem("function_call", "", "c1", "apply_patch", "")} {
		if out := patchSend(t, b, patchEvent("response.output_item.added", i, item)); len(out) != 0 {
			t.Fatalf("early added: %s", out)
		}
		if out := patchSend(t, b, []byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","output_index":%d,"delta":%s}`, i, patchJSON(`{"input":"`+fmt.Sprint(i)+`"}`)))); len(out) != 0 {
			t.Fatalf("early delta: %s", out)
		}
	}
	var events [][]byte
	for _, i := range []int{1, 0} {
		events = append(events, patchSend(t, b, patchEvent("response.output_item.done", i, patchItem("function_call", fmt.Sprint("a", i), fmt.Sprint("c", i), "apply_patch", `{"input":"`+fmt.Sprint(i)+`"}`)))...)
	}
	events = append(events, patchSend(t, b, []byte(`{"type":"response.completed","response":{"output":[]}}`))...)
	for _, event := range events {
		root := gjson.ParseBytes(event)
		if root.Get("type").String() == "response.completed" {
			for i, item := range root.Get("response.output").Array() {
				if item.Get("id").String() != fmt.Sprint("a", i) || item.Get("call_id").String() != fmt.Sprint("c", i) || item.Get("input").String() != fmt.Sprint(i) {
					t.Fatalf("interleaved final: %s", event)
				}
			}
			continue
		}
		i := int(root.Get("output_index").Int())
		identity, key := root, "item_id"
		if root.Get("item").Exists() {
			identity, key = root.Get("item"), "id"
		}
		if identity.Get(key).String() != fmt.Sprint("a", i) || identity.Get("call_id").String() != fmt.Sprint("c", i) {
			t.Fatalf("crossed calls: %s", event)
		}
	}
}
