package helps

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestPrepareV1CompactionPayloadPreservesTriggerAndConversation(t *testing.T) {
	payload := []byte(`{"model":"custom-model","stream":false,"previous_response_id":"resp_previous","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"keep this"}]},{"type":"compaction_trigger","id":"trigger-1","opaque":{"keep":true}}],"tools":[{"type":"function","name":"tool"}],"tool_choice":"auto","parallel_tool_calls":true,"text":{"format":{"type":"json_object"}},"response_format":{"type":"json_object"},"context_management":[{"type":"compact"}],"temperature":0.2}`)
	prepared := PrepareV1CompactionPayload(payload)
	if !gjson.ValidBytes(prepared) {
		t.Fatalf("prepared payload is invalid JSON: %s", prepared)
	}
	input := gjson.GetBytes(prepared, "input").Array()
	originalInput := gjson.GetBytes(payload, "input").Array()
	if len(input) != 3 || input[0].Raw != originalInput[0].Raw || input[2].Raw != originalInput[1].Raw {
		t.Fatalf("conversation order or content changed: %s", prepared)
	}
	if input[2].Get("type").String() != "compaction_trigger" || input[2].Get("id").String() != "trigger-1" || input[2].Get("opaque.keep").Bool() != true {
		t.Fatalf("trigger was changed, removed, or no longer final: %s", prepared)
	}
	if input[1].Get("role").String() != "user" || !strings.Contains(input[1].Get("content.0.text").String(), "summary") {
		t.Fatalf("summary instruction was not appended: %s", prepared)
	}
	if gjson.GetBytes(prepared, "stream").Bool() != true || gjson.GetBytes(prepared, "previous_response_id").String() != "resp_previous" || gjson.GetBytes(prepared, "temperature").Float() != 0.2 {
		t.Fatalf("required request state was not preserved: %s", prepared)
	}
	for _, field := range []string{"tools", "tool_choice", "parallel_tool_calls", "text", "response_format", "context_management"} {
		if gjson.GetBytes(prepared, field).Exists() {
			t.Errorf("incompatible field %q was retained", field)
		}
	}
}

func TestPrepareV1CompactionPayloadConvertsStringInput(t *testing.T) {
	prepared := PrepareV1CompactionPayload([]byte(`{"input":"previous plain input"}`))
	input := gjson.GetBytes(prepared, "input").Array()
	if len(input) != 2 || input[0].Get("content.0.text").String() != "previous plain input" {
		t.Fatalf("string input was not retained before the prompt: %s", prepared)
	}
}

func TestConvertResponsesCompactionResponsePreservesOutputAndUsage(t *testing.T) {
	firstOutput := `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"private reasoning"}]}`
	messageOutput := `{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"exclude this"},{"type":"output_text","text":"  Preserve the current task state.  ","annotations":[]}]}`
	response := []byte(`{"id":"resp_1","object":"response","status":"completed","model":"provider-model","output":[` + firstOutput + `,` + messageOutput + `],"usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26,"custom":{"kept":true}},"metadata":{"kept":true}}`)
	converted, err := ConvertResponsesCompactionResponse(response, "requested-model", "provider-a:https://api.example", []string{"key-a", "key-b"})
	if err != nil {
		t.Fatalf("convert summary response: %v", err)
	}
	if gjson.GetBytes(converted, "object").String() != "response" || gjson.GetBytes(converted, "status").String() != "completed" || gjson.GetBytes(converted, "model").String() != "provider-model" {
		t.Fatalf("response envelope or upstream model changed unexpectedly: %s", converted)
	}
	if gjson.GetBytes(converted, "output.0").Raw != firstOutput || gjson.GetBytes(converted, "output.1").Raw != messageOutput {
		t.Fatalf("original output was not preserved: %s", converted)
	}
	if gjson.GetBytes(converted, "usage").Raw != gjson.GetBytes(response, "usage").Raw || !gjson.GetBytes(converted, "metadata.kept").Bool() {
		t.Fatalf("usage or metadata was lost: %s", converted)
	}
	items := gjson.GetBytes(converted, "output").Array()
	if len(items) != 3 || items[2].Get("type").String() != "compaction" {
		t.Fatalf("expected exactly one appended compaction item: %s", converted)
	}
	capsule := items[2].Get("encrypted_content").String()
	if !strings.HasPrefix(capsule, responsesV1CompactionCapsulePrefix) || strings.Contains(capsule, "Preserve the current task state") {
		t.Fatalf("summary was not stored as an opaque capsule: %s", items[2].Raw)
	}
}

func TestConvertResponsesCompactionResponsePreservesNativeCompaction(t *testing.T) {
	native := `{"id":"cmp_native","type":"compaction","encrypted_content":"upstream-opaque","future":{"keep":true}}`
	response := []byte(`{"id":"resp_native","object":"response.compaction","status":"completed","output":[{"type":"reasoning","id":"rs_native"},` + native + `],"usage":{"total_tokens":12}}`)
	converted, err := ConvertResponsesCompactionResponse(response, "model", "scope", nil)
	if err != nil {
		t.Fatalf("preserve native compaction: %v", err)
	}
	if gjson.GetBytes(converted, "output.1").Raw != native || gjson.GetBytes(converted, "output.0.id").String() != "rs_native" {
		t.Fatalf("native output changed: %s", converted)
	}
	if gjson.GetBytes(converted, "usage.total_tokens").Int() != 12 || gjson.GetBytes(converted, "object").String() != "response" {
		t.Fatalf("native response metadata was not preserved or normalized: %s", converted)
	}
}

func TestConvertResponsesCompactionResponseRejectsIncompleteAndInvalidResults(t *testing.T) {
	validOutput := `[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"summary"}]}]`
	for _, test := range []struct {
		name     string
		response string
	}{
		{name: "invalid json", response: `{`},
		{name: "missing status", response: `{"output":` + validOutput + `}`},
		{name: "incomplete response", response: `{"status":"incomplete","output":` + validOutput + `}`},
		{name: "upstream error", response: `{"status":"completed","error":{"code":"failed"},"output":` + validOutput + `}`},
		{name: "no output array", response: `{"status":"completed","output":{}}`},
		{name: "reasoning only", response: `{"status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"not a summary"}]}]}`},
		{name: "refusal only", response: `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"refusal","refusal":"declined"}]}]}`},
		{name: "tool output only", response: `{"status":"completed","output":[{"type":"function_call","name":"tool"}]}`},
		{name: "blank summary", response: `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"  "}]}]}`},
		{name: "numeric summary", response: `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":123}]}]}`},
		{name: "boolean summary", response: `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":true}]}]}`},
		{name: "object summary", response: `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":{"bad":"state"}}]}]}`},
		{name: "array summary", response: `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":["bad"]}]}]}`},
		{name: "incomplete summary item", response: `{"status":"completed","output":[{"type":"message","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"partial"}]}]}`},
		{name: "invalid native item", response: `{"status":"completed","output":[{"type":"compaction","encrypted_content":""}]}`},
		{name: "duplicate native items", response: `{"status":"completed","output":[{"type":"compaction","encrypted_content":"one"},{"type":"compaction","encrypted_content":"two"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ConvertResponsesCompactionResponse([]byte(test.response), "model", "scope", []string{"key"}); err == nil {
				t.Fatalf("accepted invalid response: %s", test.response)
			}
		})
	}
}

func TestExpandResponsesCompactionCapsulesPreservesInputAndCredentialRotation(t *testing.T) {
	response := []byte(`{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"carry this summary"}]}]}`)
	converted, err := ConvertResponsesCompactionResponse(response, "model", "provider-a:https://api.example", []string{"old-key", "current-key"})
	if err != nil {
		t.Fatalf("create capsule: %v", err)
	}
	capsule := gjson.GetBytes(converted, "output.1.encrypted_content").String()
	if strings.Contains(capsule, "carry this summary") {
		t.Fatal("capsule exposed summary text")
	}
	request := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"before"}]},{"type":"compaction","encrypted_content":"` + capsule + `"},{"type":"compaction_trigger","id":"trigger","extra":true},{"type":"message","role":"user","content":[{"type":"input_text","text":"after"}]}]}`)
	expanded, err := ExpandResponsesCompactionCapsules(request, "provider-a:https://api.example", []string{"current-key", "old-key"})
	if err != nil {
		t.Fatalf("expand capsule with rotated keyring: %v", err)
	}
	items := gjson.GetBytes(expanded, "input").Array()
	if len(items) != 4 || items[0].Get("content.0.text").String() != "before" || items[3].Get("content.0.text").String() != "after" {
		t.Fatalf("input items or ordering changed: %s", expanded)
	}
	if items[1].Get("type").String() != "message" || items[1].Get("role").String() != "developer" || !strings.Contains(items[1].Get("content.0.text").String(), "carry this summary") {
		t.Fatalf("capsule was not expanded into developer context: %s", expanded)
	}
	if items[2].Raw != gjson.GetBytes(request, "input.2").Raw || items[2].Get("type").String() != "compaction_trigger" {
		t.Fatalf("compaction trigger changed during replay: %s", expanded)
	}
	unchanged, err := ExpandResponsesCompactionCapsules(request, "provider-a:https://api.example", []string{"current-key", "old-key"})
	if err != nil || gjson.GetBytes(unchanged, "input.1.role").String() != "developer" {
		t.Fatalf("capsule did not survive a subsequent stateless replay: %s, %v", unchanged, err)
	}
}

func TestExpandResponsesCompactionCapsulesRejectsCorruptionScopeAndUnknownKey(t *testing.T) {
	capsule, err := sealResponsesV1CompactionCapsule("summary", "provider-a:https://api.example", []string{"key-a"})
	if err != nil {
		t.Fatalf("seal capsule: %v", err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(capsule, responsesV1CompactionCapsulePrefix))
	if err != nil {
		t.Fatalf("decode test capsule: %v", err)
	}
	decoded[len(decoded)-1] ^= 0x01
	corrupted := responsesV1CompactionCapsulePrefix + base64.RawURLEncoding.EncodeToString(decoded)
	for _, test := range []struct {
		name    string
		capsule string
		scope   string
		keys    []string
	}{
		{name: "corrupted", capsule: corrupted, scope: "provider-a:https://api.example", keys: []string{"key-a"}},
		{name: "foreign scope", capsule: capsule, scope: "provider-b:https://api.example", keys: []string{"key-a"}},
		{name: "unknown key", capsule: capsule, scope: "provider-a:https://api.example", keys: []string{"key-b"}},
		{name: "malformed encoding", capsule: responsesV1CompactionCapsulePrefix + "!", scope: "provider-a:https://api.example", keys: []string{"key-a"}},
		{name: "unsupported capsule version", capsule: "cpa-responses-v1-compaction-v2.opaque", scope: "provider-a:https://api.example", keys: []string{"key-a"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"input": []any{map[string]any{"type": "compaction", "encrypted_content": test.capsule}}})
			if _, errExpand := ExpandResponsesCompactionCapsules(payload, test.scope, test.keys); errExpand == nil {
				t.Fatal("accepted an invalid owned capsule")
			}
		})
	}
}

func TestExpandResponsesCompactionCapsulesPreservesNativeAndForeignItems(t *testing.T) {
	payload := []byte(`{"input":[{"type":"compaction","encrypted_content":"native-upstream-state"},{"type":"compaction","encrypted_content":"other-format:opaque"},{"type":"compaction_trigger","id":"trigger"}]}`)
	expanded, err := ExpandResponsesCompactionCapsules(payload, "provider", []string{"key"})
	if err != nil {
		t.Fatalf("preserve native items: %v", err)
	}
	if string(expanded) != string(payload) {
		t.Fatalf("native or foreign data changed: %s", expanded)
	}
}

func TestBuildResponsesCompactionStreamHasConsecutiveEventsAndPreservesItems(t *testing.T) {
	response := []byte(`{"id":"resp_1","object":"response","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"summary"}]},{"type":"compaction","id":"cmp_1","encrypted_content":"opaque","future":{"kept":true}}],"usage":{"input_tokens":7,"output_tokens":3,"total_tokens":10}}`)
	frames := BuildResponsesCompactionStream(response)
	wantTypes := []string{"response.created", "response.in_progress", "response.output_item.added", "response.output_item.done", "response.output_item.added", "response.output_item.done", "response.completed"}
	if len(frames) != len(wantTypes) {
		t.Fatalf("got %d events, want %d", len(frames), len(wantTypes))
	}
	var final []byte
	responseItems := gjson.GetBytes(response, "output").Array()
	for index, frame := range frames {
		var data string
		for _, line := range strings.Split(string(frame), "\n") {
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" {
			t.Fatalf("event %d has no data: %q", index, frame)
		}
		if got := gjson.Get(data, "type").String(); got != wantTypes[index] {
			t.Fatalf("event %d type = %q, want %q", index, got, wantTypes[index])
		}
		if got := gjson.Get(data, "sequence_number").Int(); got != int64(index) {
			t.Fatalf("event %d sequence = %d", index, got)
		}
		if index == len(frames)-1 {
			final = []byte(data)
		}
		if index == 3 || index == 5 {
			outputIndex := (index - 3) / 2
			if got := gjson.Get(data, "item").Raw; got != responseItems[outputIndex].Raw {
				t.Fatalf("done event %d changed output item: %s", index, data)
			}
		}
	}
	if gjson.GetBytes(final, "response.output").Raw != gjson.GetBytes(response, "output").Raw || gjson.GetBytes(final, "response.usage").Raw != gjson.GetBytes(response, "usage").Raw {
		t.Fatalf("terminal response lost output or usage: %s", final)
	}
}

func TestCompletedV1ResponsesBodyRejectsDuplicateTerminal(t *testing.T) {
	first := `data: {"type":"response.completed","response":{"id":"resp_first","status":"completed","output":[]}}`
	second := `data: {"type":"response.completed","response":{"id":"resp_second","status":"completed","output":[]}}`
	for _, ending := range []string{"\n\n", ""} {
		t.Run(fmt.Sprintf("ending=%q", ending), func(t *testing.T) {
			response, err := CompletedV1ResponsesBody([]byte(first+"\n\n"+second+ending), nil)
			if err == nil || len(response) != 0 {
				t.Fatalf("duplicate terminal returned response %s, error %v", response, err)
			}
			if scoped, ok := err.(interface{ IsRequestScoped() bool }); !ok || !scoped.IsRequestScoped() {
				t.Fatalf("error %T is not request-scoped", err)
			}
		})
	}
}

func TestCompletedV1ResponsesBodyPreservesUsage(t *testing.T) {
	const earlier = `{"input_tokens":17,"output_tokens":9,"total_tokens":26,"provider_usage":{"kept":true}}`
	const terminal = `{"input_tokens":3,"output_tokens":2,"total_tokens":5,"provider_usage":{"terminal":true}}`
	for _, prefix := range []string{
		`{"type":"response.in_progress","usage":` + earlier + `}`,
		`{"type":"response.in_progress","response":{"usage":` + earlier + `}}`,
	} {
		for _, tc := range []struct{ name, field, want string }{
			{name: "omitted", want: earlier},
			{name: "null", field: `,"usage":null`, want: earlier},
			{name: "terminal takes precedence", field: `,"usage":` + terminal, want: terminal},
		} {
			t.Run(prefix+"/"+tc.name, func(t *testing.T) {
				body := "data: " + prefix + "\n\ndata: " + `{"type":"response.completed","response":{"status":"completed","output":[]` + tc.field + `}}` + "\n\n"
				response, err := CompletedV1ResponsesBody([]byte(body), nil)
				if err != nil {
					t.Fatal(err)
				}
				if got := gjson.GetBytes(response, "usage").Raw; got != tc.want {
					t.Fatalf("response usage = %s, want %s", got, tc.want)
				}
			})
		}
	}
}
