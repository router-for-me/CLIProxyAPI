package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"

	"github.com/tidwall/gjson"
)

func TestApplyPatchCallStateInterleavedCalls(t *testing.T) {
	first := ApplyPatchCallState{ItemID: "item_1", CallID: "call_1", Name: "apply_patch", Namespace: "tools", OutputIndex: 2}
	second := ApplyPatchCallState{ItemID: "item_2", CallID: "call_2", Name: "apply_patch", OutputIndex: 4}
	if delta, errPushArguments := first.PushArguments(`{"input":"first\uD83D`); errPushArguments != nil || delta != "first" {
		t.Fatalf("first push = %q, %v", delta, errPushArguments)
	}
	if delta, errPushArguments := second.PushArguments("{\"input\":\"second\xe4"); errPushArguments != nil || delta != "second" {
		t.Fatalf("second push = %q, %v", delta, errPushArguments)
	}
	if delta, errPushArguments := first.PushArguments(`\uDE00\n`); errPushArguments != nil || delta != "😀\n" {
		t.Fatalf("first continuation = %q, %v", delta, errPushArguments)
	}
	if delta, errPushArguments := second.PushArguments("\xb8\xad"); errPushArguments != nil || delta != "中" {
		t.Fatalf("second continuation = %q, %v", delta, errPushArguments)
	}
	tail, input, errFinishArguments := first.FinishArguments(applypatch.WrapInput("first😀\nlast"))
	if errFinishArguments != nil || tail != "last" || input != "first😀\nlast" {
		t.Fatalf("first finish = %q, %q, %v", tail, input, errFinishArguments)
	}
	tail, input, errFinishArguments = second.FinishArguments(applypatch.WrapInput("second中"))
	if errFinishArguments != nil || tail != "" || input != "second中" {
		t.Fatalf("second finish = %q, %q, %v", tail, input, errFinishArguments)
	}
	if first.ItemID != "item_1" || first.CallID != "call_1" || first.Namespace != "tools" || first.OutputIndex != 2 {
		t.Fatal("decoding changed call identity")
	}
}

func TestApplyPatchCallStateFailureIsolation(t *testing.T) {
	var bad, good ApplyPatchCallState
	if _, errPushArguments := bad.PushArguments(`{"input":null`); errPushArguments == nil {
		t.Fatal("bad call did not fail")
	}
	if delta, errPushArguments := good.PushArguments(`{"input":"good`); errPushArguments != nil || delta != "good" {
		t.Fatalf("good call push = %q, %v", delta, errPushArguments)
	}
	if tail, input, errFinishArguments := bad.FinishArguments(`{"input":"bad"}`); errFinishArguments == nil || tail != "" || input != "" {
		t.Fatalf("bad finish = %q, %q, %v", tail, input, errFinishArguments)
	}
	if tail, input, errFinishArguments := good.FinishArguments(`{"input":"good tail"}`); errFinishArguments != nil || tail != " tail" || input != "good tail" {
		t.Fatalf("good finish = %q, %q, %v", tail, input, errFinishArguments)
	}
	if tail, input, errFinishArguments := good.FinishArguments(`{"input":"good tail"}`); errFinishArguments != nil || tail != "" || input != "good tail" {
		t.Fatalf("duplicate good finish = %q, %q, %v", tail, input, errFinishArguments)
	}
}

func TestApplyPatchCallStateConcurrentIndependentCalls(t *testing.T) {
	start := make(chan struct{})
	results := make(chan error, 16)
	for i := 0; i < cap(results); i++ {
		go func(index int) {
			<-start
			state := ApplyPatchCallState{ItemID: fmt.Sprintf("item_%d", index), CallID: fmt.Sprintf("call_%d", index), OutputIndex: index}
			want := fmt.Sprintf("*** Begin Patch\n+  call %d 中文😀  \n*** End Patch\n", index)
			arguments := applypatch.WrapInput(want)
			var output strings.Builder
			for j := 0; j < len(arguments); j++ {
				delta, errPushArguments := state.PushArguments(arguments[j : j+1])
				if errPushArguments != nil {
					results <- errPushArguments
					return
				}
				output.WriteString(delta)
			}
			tail, input, errFinishArguments := state.FinishArguments(arguments)
			output.WriteString(tail)
			if errFinishArguments != nil {
				results <- errFinishArguments
				return
			}
			if output.String() != want || input != want {
				results <- fmt.Errorf("call %d: output %q, input %q", index, output.String(), input)
				return
			}
			payload := ApplyPatchInputDone(&state, input, index)
			if !json.Valid(payload) || gjson.GetBytes(payload, "call_id").String() != state.CallID {
				results <- fmt.Errorf("call %d: invalid done identity", index)
				return
			}
			results <- nil
		}(i)
	}
	close(start)
	for i := 0; i < cap(results); i++ {
		if errResult := <-results; errResult != nil {
			t.Error(errResult)
		}
	}
}

func TestApplyPatchEventsPayloads(t *testing.T) {
	state := ApplyPatchCallState{ItemID: "item_\"中", CallID: "call_\\1", Name: "apply_patch", Namespace: "tools", OutputIndex: 3}
	text := "*** Begin Patch\n+  中文 \\\"  \n*** End Patch\n"
	for _, test := range []struct {
		name     string
		payload  []byte
		kind     string
		textKey  string
		sequence int
	}{
		{name: "delta", payload: ApplyPatchInputDelta(&state, text, 11), kind: "response.custom_tool_call_input.delta", textKey: "delta", sequence: 11},
		{name: "done", payload: ApplyPatchInputDone(&state, text, 12), kind: "response.custom_tool_call_input.done", textKey: "input", sequence: 12},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !json.Valid(test.payload) {
				t.Fatalf("payload is not plain JSON: %q", test.payload)
			}
			root := gjson.ParseBytes(test.payload)
			for key, want := range map[string]string{"type": test.kind, "item_id": state.ItemID, "call_id": state.CallID, test.textKey: text} {
				if got := root.Get(key).String(); got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
			if root.Get("output_index").Int() != 3 || root.Get("sequence_number").Int() != int64(test.sequence) {
				t.Fatalf("invalid ordering metadata: %s", test.payload)
			}
		})
	}
	zero := ApplyPatchInputDelta(&ApplyPatchCallState{}, "", 0)
	for _, key := range []string{"item_id", "call_id", "output_index", "sequence_number", "delta"} {
		if !gjson.GetBytes(zero, key).Exists() {
			t.Fatalf("zero-valued payload omitted %s", key)
		}
	}
}

func TestApplyPatchFailureSanitizesClientError(t *testing.T) {
	secret := "*** Begin Patch\n+secret-token-value\n*** End Patch\n"
	original := errors.New("invalid input: " + secret)
	var state ApplyPatchErrorState
	if state.ToolInputError() != nil {
		t.Fatal("new state contains an error")
	}
	state.SetToolInputError(nil)
	state.SetToolInputError(original)
	if state.ToolInputError() != original {
		t.Fatal("original conversion error was not preserved")
	}
	payload := ApplyPatchFailure("resp_\"1", 17)
	if !json.Valid(payload) || strings.Contains(string(payload), "secret-token-value") || strings.Contains(string(payload), "Begin Patch") {
		t.Fatalf("invalid or unsanitized failure payload: %q", payload)
	}
	root := gjson.ParseBytes(payload)
	for key, want := range map[string]string{
		"type":                   "response.failed",
		"response.id":            "resp_\"1",
		"response.object":        "response",
		"response.status":        "failed",
		"response.error.code":    "invalid_tool_arguments",
		"response.error.message": "Invalid apply_patch tool arguments received from upstream.",
	} {
		if got := root.Get(key).String(); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
	if root.Get("sequence_number").Int() != 17 {
		t.Fatalf("sequence = %s", root.Get("sequence_number"))
	}
}
