package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const geminiPatchRequest = `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","definition":"start: patch"}}]}]}`

func TestGeminiApplyPatchCompleteArgumentsLifecycle(t *testing.T) {
	var param any
	raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"functions__apply_patch","args":{"input":"  *** Begin Patch\n*** End Patch\n "}}}]}}]}`)
	if out := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.1-pro", []byte(geminiPatchRequest), nil, []byte(`{"responseId":"r"}`), &param); len(out) != 2 {
		t.Fatalf("initial events=%d", len(out))
	}
	out := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.1-pro", []byte(geminiPatchRequest), nil, raw, &param)
	out = append(out, ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.1-pro", []byte(geminiPatchRequest), nil, []byte(`[DONE]`), &param)...)
	var delta strings.Builder
	var done, item, final gjson.Result
	for _, chunk := range out {
		kind, ev := parseSSEEvent(t, chunk)
		switch kind {
		case "response.custom_tool_call_input.delta":
			delta.WriteString(ev.Get("delta").String())
		case "response.custom_tool_call_input.done":
			done = ev
		case "response.output_item.done":
			item = ev.Get("item")
		case "response.completed":
			final = ev.Get("response.output.0")
		}
	}
	want := "  *** Begin Patch\n*** End Patch\n "
	if delta.String() != want || done.Get("input").String() != want || item.Get("input").String() != want || final.Get("input").String() != want {
		t.Fatalf("inconsistent patch input: delta=%q done=%s item=%s final=%s", delta.String(), done.Raw, item.Raw, final.Raw)
	}
	if done.Get("call_id").String() != item.Get("call_id").String() || done.Get("item_id").String() != item.Get("id").String() || item.Get("namespace").String() != "functions" || final.Get("id").String() != item.Get("id").String() {
		t.Fatal("patch identity changed")
	}
}

func TestGeminiApplyPatchRejectInvalidCompleteArguments(t *testing.T) {
	for _, args := range []string{`{}`, `{"input":1}`, `{"input":"a","input":"b"}`, `{"input":"secret"`, `{"input":"x","extra":1}`, `{"input":"\ud800"}`} {
		raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"functions__apply_patch","args":` + args + `}}]},"finishReason":"STOP"}]}`)
		var nonStream any
		out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini", []byte(geminiPatchRequest), nil, raw, &nonStream)
		if len(out) != 0 {
			t.Fatalf("invalid input returned: %s", out)
		}
		if state, ok := nonStream.(interface{ ToolInputError() error }); !ok || state.ToolInputError() == nil {
			t.Fatal("missing non-stream error")
		}
		var param any
		chunks := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, raw, &param)
		failures := 0
		for _, chunk := range chunks {
			kind, ev := parseSSEEvent(t, chunk)
			if kind == "response.failed" {
				failures++
				if ev.Get("response.error.code").String() != "invalid_tool_arguments" {
					t.Fatal("wrong error")
				}
			}
			if kind == "response.completed" || kind == "response.custom_tool_call_input.done" {
				t.Fatalf("invalid success: %s", chunk)
			}
		}
		if failures != 1 {
			t.Fatalf("failures=%d", failures)
		}
		if more := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, []byte(`[DONE]`), &param); len(more) != 0 {
			t.Fatal("failure reopened")
		}
	}
}

func TestGeminiApplyPatchCompleteSnapshotMatrix(t *testing.T) {
	frame := func(name, id, args string) []byte {
		return []byte(`{"candidates":[{"content":{"parts":[{"partIndex":2,"functionCall":{"id":"` + id + `","name":"` + name + `","args":` + args + `}}]}}]}`)
	}
	cases := []struct {
		name   string
		inputs [][]byte
		fail   bool
	}{
		{"equivalent encoding after item done", [][]byte{frame("functions__apply_patch", "c1", `{"input":"a"}`), frame("functions__apply_patch", "c1", `{ "input" : "\u0061" }`)}, false},
		{"complete input cannot extend", [][]byte{frame("functions__apply_patch", "c1", `{"input":"a"}`), frame("functions__apply_patch", "c1", `{"input":"ab"}`)}, true},
		{"ID conflict after item done", [][]byte{frame("functions__apply_patch", "c1", `{"input":"a"}`), frame("functions__apply_patch", "c2", `{"input":"a"}`)}, true},
		{"illegal snapshot before name", [][]byte{frame("", "c1", `{"input":1}`), frame("functions__apply_patch", "c1", `{"input":"a"}`)}, true},
		{"truncated snapshot before name", [][]byte{frame("", "c1", `{"input":"a`), frame("functions__apply_patch", "c1", `{"input":"a"}`)}, true},
		{"ID conflict before name", [][]byte{frame("", "c1", `{"input":"a"}`), frame("", "c2", `{"input":"a"}`), frame("functions__apply_patch", "c1", `{"input":"a"}`)}, true},
		{"full snapshots before name cannot extend", [][]byte{frame("", "c1", `{"input":"a"}`), frame("functions__apply_patch", "c1", `{"input":"ab"}`)}, true},
		{"valid name arrives later", [][]byte{frame("", "c1", `{"input":"a"}`), frame("functions__apply_patch", "c1", `{"input":"a"}`)}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			var param any
			var events []gjson.Result
			for _, input := range test.inputs {
				for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, input, &param) {
					_, ev := parseSSEEvent(t, chunk)
					events = append(events, ev)
				}
			}
			failures, done := 0, 0
			for _, ev := range events {
				if ev.Get("type").String() == "response.failed" {
					failures++
				}
				if ev.Get("type").String() == "response.custom_tool_call_input.done" {
					done++
				}
			}
			if test.fail {
				if failures != 1 {
					t.Fatalf("failures=%d events=%v", failures, events)
				}
			} else {
				if failures != 0 || done != 1 {
					t.Fatalf("failures=%d done=%d", failures, done)
				}
				chunks := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, []byte(`[DONE]`), &param)
				_, ev := parseSSEEvent(t, chunks[len(chunks)-1])
				if ev.Get("response.output.#").Int() != 1 || ev.Get("response.output.0.input").String() != "a" {
					t.Fatalf("final: %s", ev.Raw)
				}
			}
			for _, input := range [][]byte{test.inputs[0], []byte(`[DONE]`)} {
				if chunks := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, input, &param); len(chunks) != 0 {
					t.Fatal("terminal reopened")
				}
			}
		})
	}
}

func TestGeminiApplyPatchWinningDeclarationAndNegativeCompatibility(t *testing.T) {
	cases := []struct{ name, request, upstream, args, wantType, wantInput string }{
		{"function wins", `{"tools":[{"type":"function","name":"apply_patch"}],"input":[{"type":"additional_tools","tools":[{"type":"custom","name":"apply_patch"}]}]}`, "apply_patch", `{"n":1}`, "function_call", ""},
		{"direct function wins namespace", `{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]},{"type":"function","name":"functions__apply_patch"}]}`, "functions__apply_patch", `{"n":1}`, "function_call", ""},
		{"other custom", `{"tools":[{"type":"custom","name":"exec"}]}`, "exec", `{"command":"ls"}`, "custom_tool_call", `{"command":"ls"}`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"` + test.upstream + `","args":` + test.args + `}}]},"finishReason":"STOP"}]}`)
			out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini", []byte(test.request), nil, raw, nil)
			item := gjson.GetBytes(out, "output.0")
			if item.Get("type").String() != test.wantType || item.Get("input").String() != test.wantInput {
				t.Fatalf("compatibility changed: %s", out)
			}
			var param any
			for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(test.request), nil, raw, &param) {
				kind, ev := parseSSEEvent(t, chunk)
				if kind == "response.custom_tool_call_input.delta" || kind == "response.failed" {
					t.Fatalf("patch behavior applied to non-patch: %s", chunk)
				}
				if kind == "response.completed" && ev.Get("response.output.0.type").String() != test.wantType {
					t.Fatal(ev.Raw)
				}
			}
		})
	}
}

func TestGeminiApplyPatchConflictingNameAndPartIdentity(t *testing.T) {
	for _, second := range []string{`{"partIndex":3,"functionCall":{"id":"c1","name":"functions__apply_patch","args":{"input":"a"}}}`, `{"partIndex":2,"functionCall":{"id":"c1","name":"other","args":{"input":"a"}}}`} {
		var param any
		first := []byte(`{"candidates":[{"content":{"parts":[{"partIndex":2,"functionCall":{"id":"c1","name":"functions__apply_patch","args":{"input":"a"}}}]}}]}`)
		ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, first, &param)
		chunks := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, []byte(`{"candidates":[{"content":{"parts":[`+second+`]}}]}`), &param)
		if len(chunks) != 1 {
			t.Fatalf("identity conflict ignored: %d events", len(chunks))
		}
		kind, _ := parseSSEEvent(t, chunks[0])
		if kind != "response.failed" {
			t.Fatal("missing identity failure")
		}
	}
}

func TestGeminiApplyPatchNonStreamConflictingCompleteCalls(t *testing.T) {
	var param any
	raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"c1","name":"functions__apply_patch","args":{"input":"a"}}},{"functionCall":{"id":"c1","name":"functions__apply_patch","args":{"input":"ab"}}}]}}]}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini", []byte(geminiPatchRequest), nil, raw, &param)
	if len(out) != 0 {
		t.Fatalf("conflicting source call IDs accepted: %s", out)
	}
	if state, ok := param.(interface{ ToolInputError() error }); !ok || state.ToolInputError() == nil {
		t.Fatal("missing error state")
	}
}

func TestGeminiApplyPatchHistoryPreservesWhitespaceAndToolPair(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]}],"input":[{"type":"custom_tool_call","call_id":"c1","namespace":"functions","name":"apply_patch","input":"  *** Begin Patch\n*** Add File: 中.txt\n+😀\n*** End Patch\n "},{"type":"custom_tool_call_output","call_id":"c1","output":"ok"},{"role":"user","type":"message","content":"continue"}]}`)
	out := ConvertOpenAIResponsesRequestToGemini("gemini-3.1-pro-preview", request, false)
	want := "  *** Begin Patch\n*** Add File: 中.txt\n+😀\n*** End Patch\n "
	var call, result gjson.Result
	gjson.GetBytes(out, "contents").ForEach(func(_, content gjson.Result) bool {
		content.Get("parts").ForEach(func(_, part gjson.Result) bool {
			if part.Get("functionCall").Exists() {
				call = part.Get("functionCall")
			}
			if part.Get("functionResponse").Exists() {
				result = part.Get("functionResponse")
			}
			return true
		})
		return true
	})
	if call.Get("args.input").String() != want || call.Get("name").String() != "functions__apply_patch" || result.Get("name").String() != "functions__apply_patch" {
		t.Fatalf("history changed: %s", out)
	}
}

func TestGeminiApplyPatchDoesNotRebindOrdinaryFunctionCalls(t *testing.T) {
	var param any
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"first"},{"type":"function","name":"second"}]}`)
	for _, name := range []string{"first", "second"} {
		chunks := ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", request, nil, []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"reused","name":"`+name+`","args":{"n":1}}}]}}]}`), &param)
		var item gjson.Result
		for _, chunk := range chunks {
			kind, ev := parseSSEEvent(t, chunk)
			if kind == "response.output_item.done" {
				item = ev.Get("item")
			}
		}
		if item.Get("name").String() != name || item.Get("type").String() != "function_call" {
			t.Fatalf("ordinary function changed: %s", item.Raw)
		}
	}
}

func TestGeminiApplyPatchDistinctUnkeyedCompleteCalls(t *testing.T) {
	var param any
	raw := []byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"functions__apply_patch","args":{"input":"a"}}},{"functionCall":{"name":"functions__apply_patch","args":{"input":"b"}}}]},"finishReason":"STOP"}]}`)
	inputs := map[string]string{}
	var final gjson.Result
	for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, raw, &param) {
		kind, ev := parseSSEEvent(t, chunk)
		switch kind {
		case "response.custom_tool_call_input.delta":
			inputs[ev.Get("item_id").String()] += ev.Get("delta").String()
		case "response.custom_tool_call_input.done":
			if inputs[ev.Get("item_id").String()] != ev.Get("input").String() {
				t.Fatal(ev.Raw)
			}
		case "response.completed":
			final = ev.Get("response")
		}
	}
	if final.Get("output.#").Int() != 2 || final.Get("output.0.input").String() != "a" || final.Get("output.1.input").String() != "b" || final.Get("output.0.id").String() == final.Get("output.1.id").String() {
		t.Fatalf("distinct calls merged: %s", final.Raw)
	}
}

func TestGeminiApplyPatchUnresolvableCallIdentityFailsClosed(t *testing.T) {
	for _, part := range []string{`{"functionCall":{"args":{"input":"secret"}}}`, `{"partIndex":2,"functionCall":{"id":"c1","args":{"input":"secret"}}}`} {
		var param any
		raw := []byte(`{"candidates":[{"content":{"parts":[` + part + `]},"finishReason":"STOP"}]}`)
		failures := 0
		for _, chunk := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini", []byte(geminiPatchRequest), nil, raw, &param) {
			kind, _ := parseSSEEvent(t, chunk)
			if kind == "response.failed" {
				failures++
			}
			if kind == "response.completed" || kind == "response.function_call_arguments.delta" {
				t.Fatalf("unresolvable identity accepted: %s", chunk)
			}
		}
		if failures != 1 {
			t.Fatalf("failures=%d", failures)
		}
		var nonStream any
		if out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini", []byte(geminiPatchRequest), nil, raw, &nonStream); len(out) != 0 {
			t.Fatalf("unresolvable non-stream accepted: %s", out)
		}
	}
}
