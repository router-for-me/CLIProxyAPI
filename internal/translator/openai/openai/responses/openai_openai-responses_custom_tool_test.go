package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const applyPatchPayload = "*** Begin Patch\n*** Add File: /tmp/x.txt\n+hi\n*** End Patch"

// request side: a custom (apply_patch) tool must be downgraded to a chat
// function tool carrying a single string `input` parameter (not dropped).
func TestDeepSeekRequest_CustomToolDowngraded(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro",
		"input":"edit a file",
		"tools":[
			{"type":"function","name":"shell","description":"run","parameters":{"type":"object","properties":{}}},
			{"type":"custom","name":"apply_patch","description":"Use the apply_patch tool to edit files. This is a FREEFORM tool, so do not wrap the patch in JSON."}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)
	tools := gjson.GetBytes(out, "tools")
	if !tools.IsArray() {
		t.Fatalf("tools missing: %s", out)
	}
	var ap gjson.Result
	tools.ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("function.name").String() == "apply_patch" {
			ap = tool
		}
		return true
	})
	if !ap.Exists() {
		t.Fatalf("apply_patch was dropped, got: %s", out)
	}
	if ap.Get("type").String() != "function" {
		t.Fatalf("apply_patch must be downgraded to function, got type %q", ap.Get("type").String())
	}
	if !ap.Get("function.parameters.properties.input").Exists() {
		t.Fatalf("downgraded apply_patch must carry an `input` string param, got: %s", ap.Raw)
	}
	if strings.Contains(ap.Get("function.description").String(), "do not wrap the patch in JSON") {
		t.Fatalf("contradictory freeform sentence must be stripped from description")
	}
}

// request side multi-turn: a historical custom_tool_call (bare input) must
// replay to the backend as an assistant tool_call with wrapped JSON arguments.
func TestDeepSeekRequest_CustomToolCallHistoryReplay(t *testing.T) {
	raw := []byte(`{
		"model":"deepseek-v4-pro",
		"tools":[{"type":"custom","name":"apply_patch","description":"x"}],
		"input":[
			{"type":"message","role":"user","content":[{"type":"input_text","text":"edit"}]},
			{"type":"custom_tool_call","call_id":"call_1","name":"apply_patch","input":"` + strings.ReplaceAll(applyPatchPayload, "\n", "\\n") + `"},
			{"type":"custom_tool_call_output","call_id":"call_1","output":"Success. Updated."}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("deepseek-v4-pro", raw, false)
	// find the assistant tool_call carrying apply_patch
	found := false
	gjson.GetBytes(out, "messages").ForEach(func(_, msg gjson.Result) bool {
		msg.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
			if tc.Get("function.name").String() == "apply_patch" {
				args := tc.Get("function.arguments").String()
				if gjson.Get(args, "input").String() == applyPatchPayload {
					found = true
				}
			}
			return true
		})
		return true
	})
	if !found {
		t.Fatalf("custom_tool_call history must replay as tool_call with {\"input\":patch} args, got: %s", out)
	}
}

// response side non-stream: a DeepSeek tool_call for apply_patch must surface
// as a custom_tool_call output item with bare input text (no JSON wrapper).
func TestDeepSeekResponse_NonStreamCustomToolCall(t *testing.T) {
	originalReq := []byte(`{"tools":[{"type":"custom","name":"apply_patch","description":"x"}]}`)
	// DeepSeek returns the downgraded function call with wrapped JSON args
	backendResp := []byte(`{
		"choices":[{"message":{"role":"assistant","tool_calls":[
			{"id":"call_9","type":"function","function":{"name":"apply_patch","arguments":"{\"input\":\"` + strings.ReplaceAll(applyPatchPayload, "\n", "\\n") + `\"}"}}
		]},"finish_reason":"tool_calls"}]
	}`)
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "deepseek-v4-pro", originalReq, originalReq, backendResp, nil)
	var item gjson.Result
	gjson.GetBytes(out, "output").ForEach(func(_, it gjson.Result) bool {
		if it.Get("name").String() == "apply_patch" {
			item = it
		}
		return true
	})
	if !item.Exists() {
		t.Fatalf("no apply_patch output item, got: %s", out)
	}
	if item.Get("type").String() != "custom_tool_call" {
		t.Fatalf("apply_patch must be custom_tool_call, got %q: %s", item.Get("type").String(), item.Raw)
	}
	if item.Get("input").String() != applyPatchPayload {
		t.Fatalf("input must be bare patch text, got %q", item.Get("input").String())
	}
	if item.Get("arguments").Exists() && item.Get("arguments").String() != "" {
		t.Fatalf("custom_tool_call must NOT carry JSON arguments, got %q", item.Get("arguments").String())
	}
}

// regression: a normal function tool call stays a function_call.
func TestDeepSeekResponse_NonStreamFunctionUnaffected(t *testing.T) {
	originalReq := []byte(`{"tools":[{"type":"function","name":"shell"}]}`)
	backendResp := []byte(`{
		"choices":[{"message":{"role":"assistant","tool_calls":[
			{"id":"call_5","type":"function","function":{"name":"shell","arguments":"{\"cmd\":\"ls\"}"}}
		]},"finish_reason":"tool_calls"}]
	}`)
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "deepseek-v4-pro", originalReq, originalReq, backendResp, nil)
	var item gjson.Result
	gjson.GetBytes(out, "output").ForEach(func(_, it gjson.Result) bool {
		if it.Get("name").String() == "shell" {
			item = it
		}
		return true
	})
	if item.Get("type").String() != "function_call" {
		t.Fatalf("shell must stay function_call, got %q", item.Get("type").String())
	}
}

// regression: when the request snapshot IS available and declares apply_patch as
// a *regular function* (not a freeform custom tool), the response must keep it as
// a function_call with its JSON arguments intact. The defensive apply_patch
// fallback must only fire when the request snapshot is unavailable, not whenever
// the name happens to be absent from the (correctly empty) custom set.
func TestDeepSeekResponse_NonStreamRegularApplyPatchNotForcedCustom(t *testing.T) {
	originalReq := []byte(`{"tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}]}`)
	backendResp := []byte(`{
		"choices":[{"message":{"role":"assistant","tool_calls":[
			{"id":"call_7","type":"function","function":{"name":"apply_patch","arguments":"{\"path\":\"/tmp/x\"}"}}
		]},"finish_reason":"tool_calls"}]
	}`)
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "deepseek-v4-pro", originalReq, originalReq, backendResp, nil)
	var item gjson.Result
	gjson.GetBytes(out, "output").ForEach(func(_, it gjson.Result) bool {
		if it.Get("name").String() == "apply_patch" {
			item = it
		}
		return true
	})
	if !item.Exists() {
		t.Fatalf("no apply_patch output item, got: %s", out)
	}
	if item.Get("type").String() != "function_call" {
		t.Fatalf("regular function apply_patch must stay function_call, got %q: %s", item.Get("type").String(), item.Raw)
	}
	if item.Get("arguments").String() != `{"path":"/tmp/x"}` {
		t.Fatalf("function_call must keep JSON arguments, got %q", item.Get("arguments").String())
	}
}
