package helps

import (
	"bytes"
	"fmt"
	"testing"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestApplyPatchResponsesHelperNativeSSEBytes(t *testing.T) {
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatCodex, request, request)
	for _, line := range [][]byte{
		[]byte("event: response.output_item.done"),
		[]byte(`data:   { "type":"response.output_item.done", "output_index":0, "item":{"type":"custom_tool_call","id":"a","name":"apply_patch","input":"raw"}}  `),
		[]byte(""),
		[]byte("event: response.completed"),
		[]byte(`data:  { "type":"response.completed", "sequence_number":8,"response":{"output":[{"type":"custom_tool_call","id":"a","name":"apply_patch","input":"raw"}]}} `),
	} {
		out, errStream := s.Stream(line)
		if errStream != nil {
			t.Fatal(errStream)
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			continue
		}
		if len(out) == 0 || !bytes.Equal(out[len(out)-1], line) {
			t.Fatalf("native framing changed: %q", out)
		}
	}
}

func TestApplyPatchResponsesHelperDispatcherKeysAndFinal(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}]}`)
	for _, key := range []string{`"output_index":0`, `"call_id":"c"`, `"item_id":"a"`} {
		for _, terminalOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", key, terminalOnly), func(t *testing.T) {
				s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, request, request)
				s.AddDispatcher("n", "n")
				_, errAdded := s.Transform([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"n","namespace":"n","arguments":""}}`))
				if errAdded != nil {
					t.Fatal(errAdded)
				}
				_, errDelta := s.Transform([]byte(`{"type":"response.function_call_arguments.delta",` + key + `,"delta":"{\"name\":\"apply_patch\",\"arguments\":{\"input\":\"p\"}}"}`))
				if errDelta != nil {
					t.Fatal(errDelta)
				}
				item := `{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","namespace":"n","arguments":"{\"input\":\"p\"}"}`
				var out [][]byte
				var errTransform error
				if terminalOnly {
					out, errTransform = s.Transform([]byte(`{"type":"response.completed","response":{"output":[` + item + `]}}`))
				} else {
					out, errTransform = s.Transform([]byte(`{"type":"response.output_item.done","output_index":0,"item":` + item + `}`))
				}
				if errTransform != nil {
					t.Fatal(errTransform)
				}
				found := false
				for _, e := range out {
					if gjson.GetBytes(e, "type").String() == "response.custom_tool_call_input.done" {
						found = true
					}
					if gjson.GetBytes(e, "type").String() == "response.custom_tool_call_input.delta" {
						t.Fatalf("fabricated dispatcher preview: %s", out)
					}
				}
				if !found {
					t.Fatalf("dispatcher bypass: %s", out)
				}
				// Completed input is valid independently of response closure.
				if errFinish := s.Bridge.Finish(); errFinish != nil {
					t.Fatal(errFinish)
				}
				if !terminalOnly && s.Finish() == nil {
					t.Fatal("completed dispatcher input substituted for source completion")
				}
			})
		}
	}
}

func TestApplyPatchResponsesHelperChatFunctionPreference(t *testing.T) {
	original := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","function":{"name":"apply_patch"}}]}`)
	declarations := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"apply_patch"}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAI, original, declarations)
	raw := []byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","name":"apply_patch","arguments":"ordinary"}}`)
	out, errTransform := s.Transform(raw)
	if errTransform != nil || len(out) != 1 || !bytes.Equal(out[0], raw) {
		t.Fatalf("Chat function preference: %s %v", out, errTransform)
	}
}

func TestApplyPatchResponsesHelperRequestChatPreference(t *testing.T) {
	original := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","function":{"name":"apply_patch","parameters":{"type":"object","properties":{"x":{"type":"integer"}}}}}]}`)
	body := []byte(`{"tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"apply_patch","parameters":{"type":"object","properties":{"x":{"type":"integer"}}}}]}`)
	out, errNormalize := NormalizeApplyPatchResponsesRequest(body, original)
	if errNormalize != nil || !gjson.GetBytes(out, "tools.0.parameters.properties.x").Exists() || gjson.GetBytes(out, "tools.0.parameters.properties.input").Exists() {
		t.Fatalf("request ordinary preference lost: %s %v", out, errNormalize)
	}
}

func TestApplyPatchResponsesHelperDispatcherOmittedArguments(t *testing.T) {
	req := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	for _, lateName := range []string{"n", "apply_patch"} {
		t.Run(lateName, func(t *testing.T) {
			s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, req, req)
			s.AddDispatcher("n", "n")
			for _, raw := range []string{
				`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"n","arguments":""}}`,
				`{"type":"response.function_call_arguments.delta","output_index":0,"item_id":"a","delta":"{\"name\":\"apply_patch\",\"arguments\":{\"input\":\"p\"}}"}`,
			} {
				if _, errTransform := s.Transform([]byte(raw)); errTransform != nil {
					t.Fatal(errTransform)
				}
			}
			out, errTransform := s.Transform([]byte(fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":%q,"namespace":"n"}}`, lateName)))
			if errTransform != nil || len(out) == 0 || gjson.GetBytes(out[len(out)-1], "item.input").String() != "p" {
				t.Fatalf("sourced dispatcher completion lost: %s %v", out, errTransform)
			}
		})
	}
}

func TestApplyPatchResponsesHelperClosedResponse(t *testing.T) {
	req := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, req, req)
	s.AddDispatcher("n", "n")
	if _, errTransform := s.Transform([]byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","id":"a","call_id":"c","namespace":"n","name":"apply_patch","arguments":"{\"input\":\"p\"}"}]}}`)); errTransform != nil {
		t.Fatal(errTransform)
	}
	out, errTransform := s.Transform([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","call_id":"changed","name":"n","arguments":""}}`))
	if len(out) != 0 || errTransform != nil {
		t.Fatalf("closed response mutated: %s %v", out, errTransform)
	}
	if errFinish := s.Finish(); errFinish != nil {
		t.Fatal(errFinish)
	}
}

func TestApplyPatchResponsesHelperTransportTerminal(t *testing.T) {
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	complete := []byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"apply_patch","arguments":"{\"input\":\"p\"}"}}`)
	for _, jsonTerminal := range []bool{false, true} {
		t.Run(fmt.Sprint(jsonTerminal), func(t *testing.T) {
			s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, request, request)
			if _, errStream := s.Stream(complete); errStream != nil {
				t.Fatal(errStream)
			}
			if jsonTerminal {
				if _, errStream := s.Stream([]byte(`data: {"type":"response.completed","response":{"output":[]}}`)); errStream != nil {
					t.Fatal(errStream)
				}
			}
			marker := []byte("data:   [DONE]  ")
			out, errStream := s.Stream(marker)
			if !jsonTerminal {
				if errStream == nil || len(out) != 1 || !bytes.Contains(out[0], []byte(`"type":"response.failed"`)) {
					t.Fatalf("premature source sentinel accepted: %q %v", out, errStream)
				}
			} else if errStream != nil || len(out) != 1 || !bytes.Equal(out[0], marker) {
				t.Fatalf("first legitimate source sentinel changed: %q %v", out, errStream)
			}
			for _, line := range [][]byte{complete, []byte("event: response.completed"), []byte(""), []byte(": keepalive"), marker} {
				out, errStream = s.Stream(line)
				if errStream != nil || len(out) != 0 {
					t.Fatalf("post-terminal output: %q %v", out, errStream)
				}
			}
			out, errTransform := s.Transform([]byte(`{"type":"response.completed","response":{"output":[]}}`))
			if errTransform != nil || len(out) != 0 {
				t.Fatalf("transport terminal reopened by JSON: %s %v", out, errTransform)
			}
		})
	}
}

func TestApplyPatchResponsesHelperFailedTransport(t *testing.T) {
	req := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, req, req)
	if _, errStream := s.Stream([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","name":"apply_patch","arguments":""}}`)); errStream != nil {
		t.Fatal(errStream)
	}
	out, errStream := s.Stream([]byte("data: [DONE]"))
	if errStream == nil || len(out) != 1 || !bytes.Contains(out[0], []byte(`"type":"response.failed"`)) {
		t.Fatalf("incomplete call did not fail before DONE: %s %v", out, errStream)
	}
	for _, line := range [][]byte{[]byte("data: [DONE]"), []byte(`data: {"type":"response.completed","response":{"output":[]}}`), []byte("event: response.completed")} {
		out, errStream = s.Stream(line)
		if len(out) != 0 || errStream != nil {
			t.Fatalf("failure repeated or success published after failure: %s %v", out, errStream)
		}
	}
}

func TestApplyPatchResponsesHelperInactiveTransportBytes(t *testing.T) {
	req := []byte(`{"tools":[{"type":"function","name":"apply_patch"}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, req, req)
	for _, line := range [][]byte{[]byte("data: [DONE]"), []byte(`data:  { "type":"response.completed", "response":{"output":[]}} `), []byte("event: response.completed"), []byte(""), []byte("data: [DONE]")} {
		out, errStream := s.Stream(line)
		if errStream != nil || len(out) != 1 || !bytes.Equal(out[0], line) {
			t.Fatalf("inactive bridge changed source bytes: %q %v", out, errStream)
		}
	}
}

func TestApplyPatchResponsesHelperRetainedDispatcherProvenance(t *testing.T) {
	req := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}]}`)
	patch := `{"name":"apply_patch","arguments":{"input":"p"}}`
	ordinary := `{"name":"lookup","arguments":{"input":"p"}}`
	for _, tc := range []struct {
		name       string
		delta      string
		wrappers   []string
		finalName  string
		shouldFail bool
	}{
		{"patch_then_ordinary", "", []string{patch, ordinary}, "n", true},
		{"ordinary_then_patch", "", []string{ordinary, patch}, "n", true},
		{"conflicting_inputs", "", []string{patch, `{"name":"apply_patch","arguments":{"input":"q"}}`}, "n", true},
		{"full_source_conflicts_with_snapshot", patch, []string{ordinary}, "n", true},
		{"full_source_conflicts_with_child", patch, nil, "lookup", true},
		{"ordinary_child_is_not_patch", "", []string{ordinary}, "n", false},
		{"ordinary_arguments_are_not_dispatcher_provenance", "", []string{`{"name":"lookup","arguments":{"name":"apply_patch","arguments":{"input":"not patch"}}}`}, "n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, req, req)
			s.AddDispatcher("n", "n")
			if _, errTransform := s.Transform([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","name":"n","arguments":""}}`)); errTransform != nil {
				t.Fatal(errTransform)
			}
			if tc.delta != "" {
				if _, errTransform := s.Transform([]byte(fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":"a","delta":%q}`, tc.delta))); errTransform != nil {
					t.Fatal(errTransform)
				}
			}
			for _, wrapper := range tc.wrappers {
				s.RememberDispatcherArguments([]byte(fmt.Sprintf(`{"type":"response.function_call_arguments.done","item_id":"a","arguments":%q}`, wrapper)))
				// The real restorer removes the wrapper before this event reaches Transform.
				if _, errTransform := s.Transform([]byte(fmt.Sprintf(`{"type":"response.function_call_arguments.done","item_id":"a","arguments":%q}`, gjson.Get(wrapper, "arguments").Raw))); errTransform != nil {
					t.Fatal(errTransform)
				}
			}
			final := fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","call_id":"late","name":%q,"namespace":"n"}}`, tc.finalName)
			out, errTransform := s.Transform([]byte(final))
			if tc.shouldFail {
				if errTransform == nil || len(out) != 1 || gjson.GetBytes(out[0], "type").String() != "response.failed" {
					t.Fatalf("dispatcher child evidence lost: %s %v", out, errTransform)
				}
			} else if errTransform != nil || len(out) == 0 || gjson.GetBytes(out[len(out)-1], "item.name").String() != "lookup" || gjson.GetBytes(out[len(out)-1], "item.namespace").String() != "n" || gjson.GetBytes(out[len(out)-1], "item.arguments").String() != gjson.Get(tc.wrappers[len(tc.wrappers)-1], "arguments").Raw || bytes.Contains(bytes.Join(out, nil), []byte(`custom_tool_call`)) {
				t.Fatalf("ordinary dispatcher child changed: %s %v", out, errTransform)
			}
		})
	}
}

func TestApplyPatchResponsesHelperDispatcherSnapshotsAllMatched(t *testing.T) {
	req := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	for discover := 0; discover < 3; discover++ {
		t.Run(fmt.Sprint(discover), func(t *testing.T) {
			s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, req, req)
			s.AddDispatcher("n", "n")
			for i := 0; i < 3; i++ {
				if _, errTransform := s.Transform([]byte(fmt.Sprintf(`{"type":"response.output_item.added","output_index":%d,"item":{"type":"function_call","id":"i%d","call_id":"c%d","name":"n","arguments":""}}`, i, i, i))); errTransform != nil {
					t.Fatal(errTransform)
				}
			}
			original := []byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"i1","call_id":"c2","arguments":"{\"name\":\"apply_patch\",\"arguments\":{\"input\":\"p\"}}"}`)
			s.RememberDispatcherArguments(original)
			for _, key := range []string{"item:i0", "item:i1", "item:i2"} {
				if len(s.byDispatcherKey[key].snapshots) != 1 || !bytes.Equal(s.byDispatcherKey[key].snapshots[0], original) {
					t.Fatalf("original source not retained on matched record %s", key)
				}
			}
			if _, errTransform := s.Transform([]byte(`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"i1","call_id":"c2","arguments":"{\"input\":\"p\"}"}`)); errTransform != nil {
				t.Fatal(errTransform)
			}
			out, errTransform := s.Transform([]byte(fmt.Sprintf(`{"type":"response.output_item.done","output_index":%d,"item":{"type":"function_call","id":"i%d","call_id":"c%d","name":"n"}}`, discover, discover, discover)))
			if errTransform == nil || len(out) != 1 || gjson.GetBytes(out[0], "type").String() != "response.failed" {
				t.Fatalf("conflicting all-key evidence lost on record %d: %s %v", discover, out, errTransform)
			}
		})
	}
}

func TestApplyPatchResponsesHelperOrdinaryProgress(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, request, request)
	s.AddDispatcher("n", "n")
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","name":"lookup","namespace":"n","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","item_id":"a","delta":"{\"x\":1}"}`,
		`{"type":"response.function_call_arguments.done","item_id":"a","arguments":"{\"x\":1}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"lookup","namespace":"n","arguments":"{\"x\":1}"}}`,
	} {
		s.RememberDispatcherEvent([]byte(raw))
		out, errTransform := s.Transform([]byte(raw))
		if errTransform != nil || len(out) != 1 || !bytes.Equal(out[0], []byte(raw)) {
			t.Fatalf("ordinary progress delayed or rewritten: %s %v", out, errTransform)
		}
	}
}

func TestApplyPatchResponsesHelperDispatcherLifecycle(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	for _, closeAt := range []string{"response", "sentinel", "upstream_failure", "local_failure"} {
		t.Run(closeAt, func(t *testing.T) {
			s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, request, request)
			s.AddDispatcher("n", "n")
			for _, raw := range []string{
				`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"a"}}`,
				`{"type":"response.function_call_arguments.done","item_id":"a","arguments":"{\"name\":\"apply_patch\",\"arguments\":{\"input\":\"p\"}}"}`,
			} {
				original := []byte(raw)
				s.RememberDispatcherEvent(original)
				out, errTransform := s.Transform(original)
				if errTransform != nil || len(out) != 0 || s.byDispatcherKey["item:a"].namespace != "" {
					t.Fatalf("wrapper prematurely acquired dispatcher provenance: %s %v", out, errTransform)
				}
				for i := range original {
					original[i] = 'x'
				}
			}
			out, errTransform := s.Transform([]byte(`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"a","call_id":"c","name":"n"}}`))
			if errTransform != nil || len(out) == 0 || gjson.GetBytes(out[len(out)-1], "item.input").String() != "p" {
				t.Fatalf("retained source not acquired: %s %v", out, errTransform)
			}
			call := s.byDispatcherKey["item:a"]
			if call == nil || !call.completed || call.namespace != "n" || call.index != 2 || s.byDispatcherKey["call:c"] != call || s.byDispatcherKey["index:2"] != call || len(call.snapshots) != 1 {
				t.Fatalf("completed aliases/source evidence expired: %+v", call)
			}
			if errFinish := s.Bridge.Finish(); errFinish != nil || s.Finish() == nil || s.byDispatcherKey["item:a"] != call {
				t.Fatalf("argument validation must not close completed provenance: %v", errFinish)
			}
			switch closeAt {
			case "response":
				out, errTransform = s.Transform([]byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","id":"a","call_id":"c","name":"n"}]}}`))
				if errTransform != nil || len(out) != 1 || gjson.GetBytes(out[0], "response.output.0.input").String() != "p" {
					t.Fatalf("sparse terminal lost completed child or replayed progress: %s %v", out, errTransform)
				}
			case "sentinel":
				out, errTransform = s.Stream([]byte("data: [DONE]"))
				if errTransform == nil || len(out) != 1 || !bytes.Contains(out[0], []byte(`"type":"response.failed"`)) {
					t.Fatalf("premature sentinel accepted: %s %v", out, errTransform)
				}
			case "upstream_failure":
				if _, errTransform = s.Transform([]byte(`{"type":"response.failed","response":{"output":[]}}`)); errTransform != nil {
					t.Fatal(errTransform)
				}
			case "local_failure":
				if _, errTransform = s.Transform([]byte(`{"type":"response.output_item.done","output_index":3,"item":{"type":"function_call","id":"a","name":"n"}}`)); errTransform == nil {
					t.Fatal("identity conflict did not fail")
				}
			}
			if len(s.byDispatcherKey) != 0 || len(s.records) != 0 || s.upstream != nil {
				t.Fatal("closed response retained dispatcher source/aliases")
			}
			out, errTransform = s.Transform([]byte(`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"a","name":"n"}}`))
			if errTransform != nil || len(out) != 0 {
				t.Fatalf("closed dispatcher reopened: %s %v", out, errTransform)
			}
		})
	}
}

func TestApplyPatchResponsesHelperLateChildNamespace(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, request, request)
	s.AddDispatcher("n", "n")
	var out [][]byte
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","name":"n","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","item_id":"a","arguments":"{\"name\":\"apply_patch\",\"arguments\":{\"input\":\"p\"}}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"apply_patch"}}`,
	} {
		s.RememberDispatcherEvent([]byte(raw))
		converted, errTransform := s.Transform([]byte(raw))
		if errTransform != nil {
			t.Fatal(errTransform)
		}
		out = append(out, converted...)
	}
	if len(out) == 0 || gjson.GetBytes(out[len(out)-1], "item.namespace").String() != "n" || gjson.GetBytes(out[len(out)-1], "item.input").String() != "p" {
		t.Fatalf("sourced late child lost its namespace/input: %s", out)
	}
}

func TestApplyPatchResponsesHelperTerminalSourceIdentity(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, request, request)
	s.AddDispatcher("n", "n")
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"a","name":"n","arguments":""}}`,
		`{"type":"response.function_call_arguments.done","item_id":"a","arguments":"{\"name\":\"apply_patch\",\"arguments\":{\"input\":\"p\"}}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"a","call_id":"c","name":"n"}}`,
	} {
		s.RememberDispatcherEvent([]byte(raw))
		if _, errTransform := s.Transform([]byte(raw)); errTransform != nil {
			t.Fatal(errTransform)
		}
	}
	// Provider filtering can remove an earlier item after the original snapshot is copied.
	s.RememberDispatcherEvent([]byte(`{"type":"response.completed","response":{"output":[{"type":"message","id":"removed"},{"type":"function_call","id":"a","call_id":"c","name":"n","namespace":"other"}]}}`))
	out, errTransform := s.Transform([]byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","id":"a","call_id":"c","name":"n","namespace":"n"}]}}`))
	if errTransform == nil || len(out) != 1 || gjson.GetBytes(out[0], "type").String() != "response.failed" {
		t.Fatalf("terminal source matched by position, losing contradictory namespace: %s %v", out, errTransform)
	}
}

func TestApplyPatchResponsesHelperIndexOnlyTerminal(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"}]}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, request, request)
	s.AddDispatcher("n", "n")
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call"}}`,
		`{"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"name\":\"apply_patch\",\"arguments\":{\"input\":\"p\"}}"}`,
		// IDs are real source evidence; the later sparse terminal still matches by index.
		`{"type":"response.function_call_arguments.done","output_index":0,"item_id":"a","call_id":"c","arguments":"{\"name\":\"apply_patch\",\"arguments\":{\"input\":\"p\"}}"}`,
	} {
		s.RememberDispatcherEvent([]byte(raw))
		if _, errTransform := s.Transform([]byte(raw)); errTransform != nil {
			t.Fatal(errTransform)
		}
	}
	terminal := []byte(`{"type":"response.completed","response":{"output":[{"type":"function_call","name":"n"}]}}`)
	s.RememberDispatcherEvent(terminal)
	out, errTransform := s.Transform(terminal)
	if errTransform != nil || len(out) == 0 || gjson.GetBytes(out[len(out)-1], "response.output.0.input").String() != "p" {
		t.Fatalf("index-only provenance bypassed: %s %v", out, errTransform)
	}
}

func TestApplyPatchResponsesHelperCompletedOrdinaryDelta(t *testing.T) {
	request := []byte(`{"tools":[{"type":"namespace","name":"n","tools":[{"type":"custom","name":"apply_patch"},{"type":"function","name":"lookup"}]}]}`)
	s := NewApplyPatchResponsesState(sdktranslator.FormatOpenAIResponse, request, request)
	s.AddDispatcher("n", "n")
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","name":"n"}}`,
		`{"type":"response.function_call_arguments.done","item_id":"a","arguments":"{\"name\":\"lookup\",\"arguments\":{\"x\":1}}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","call_id":"c","name":"n"}}`,
	} {
		s.RememberDispatcherEvent([]byte(raw))
		if _, errTransform := s.Transform([]byte(raw)); errTransform != nil {
			t.Fatal(errTransform)
		}
	}
	raw := []byte(`{"type":"response.function_call_arguments.delta","item_id":"a","delta":"ordinary"}`)
	s.RememberDispatcherEvent(raw)
	out, errTransform := s.Transform(raw)
	if errTransform != nil || len(out) != 1 || !bytes.Equal(out[0], raw) {
		t.Fatalf("ordinary post-completion progress acquired patch validation: %s %v", out, errTransform)
	}
}

func TestApplyPatchResponsesHelperSourceTerminalRequired(t *testing.T) {
	request := []byte(`{"tools":[{"type":"custom","name":"apply_patch"}]}`)
	for _, mode := range []string{"empty", "arguments", "item"} {
		t.Run(mode, func(t *testing.T) {
			s := NewApplyPatchResponsesState(sdktranslator.FormatCodex, request, request)
			if mode != "empty" {
				_, errTransform := s.Transform([]byte(`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"a","name":"apply_patch","arguments":""}}`))
				if errTransform != nil {
					t.Fatal(errTransform)
				}
				event := `{"type":"response.function_call_arguments.done","item_id":"a","arguments":"{\"input\":\"p\"}"}`
				if mode == "item" {
					event = `{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"a","name":"apply_patch","arguments":"{\"input\":\"p\"}"}}`
				}
				if _, errTransform = s.Transform([]byte(event)); errTransform != nil {
					t.Fatal(errTransform)
				}
			}
			out, errFinishStream := s.FinishStream()
			if errFinishStream == nil || len(out) != 1 || !bytes.Contains(out[0], []byte(`"type":"response.failed"`)) {
				t.Fatalf("source EOF silently accepted: %s %v", out, errFinishStream)
			}
			if out, errFinishStream = s.FinishStream(); len(out) != 0 || errFinishStream != nil {
				t.Fatalf("EOF failure repeated: %s %v", out, errFinishStream)
			}
		})
	}
}

func TestApplyPatchResponsesHelperInactiveEOFAndDONE(t *testing.T) {
	for _, request := range []string{`{}`, `{"tools":[{"type":"function","name":"apply_patch"}]}`} {
		s := NewApplyPatchResponsesState(sdktranslator.FormatCodex, []byte(request), []byte(request))
		if out, errFinishStream := s.FinishStream(); len(out) != 0 || errFinishStream != nil {
			t.Fatalf("inactive EOF changed: %s %v", out, errFinishStream)
		}
		line := []byte("data: [DONE]")
		if out, errStream := s.Stream(line); errStream != nil || len(out) != 1 || !bytes.Equal(out[0], line) {
			t.Fatalf("ordinary DONE changed: %s %v", out, errStream)
		}
	}
}
