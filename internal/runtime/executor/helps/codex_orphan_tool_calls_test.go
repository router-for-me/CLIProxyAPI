package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestDropOrphanResponsesToolCalls(t *testing.T) {
	body := []byte(`{"model":"m","input":[` +
		`{"type":"function_call","call_id":"fc_orphan","name":"a","arguments":"{}"},` +
		`{"type":"custom_tool_call","call_id":"ct_orphan","name":"b","input":"x"},` +
		`{"type":"function_call","call_id":"fc_ok","name":"a","arguments":"{}"},` +
		`{"type":"custom_tool_call","call_id":"ct_ok","name":"b","input":"x"},` +
		`{"type":"function_call_output","call_id":"fc_ok","output":"1"},` +
		`{"type":"custom_tool_call_output","call_id":"ct_ok","output":"2"},` +
		`{"type":"function_call_output","call_id":"out_first","output":"3"},` +
		`{"type":"function_call","call_id":"out_first","name":"a","arguments":"{}"},` +
		`{"type":"message","role":"user","content":"hi"}]}`)

	got := DropOrphanResponsesToolCalls(body)

	var ids []string
	for _, item := range gjson.GetBytes(got, "input").Array() {
		ids = append(ids, item.Get("type").String()+":"+item.Get("call_id").String())
	}
	want := []string{
		"function_call:fc_ok", "custom_tool_call:ct_ok",
		"function_call_output:fc_ok", "custom_tool_call_output:ct_ok",
		"function_call_output:out_first", "function_call:out_first", "message:",
	}
	if len(ids) != len(want) {
		t.Fatalf("items = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("items = %v, want %v", ids, want)
		}
	}
	if gjson.GetBytes(got, "model").String() != "m" {
		t.Fatalf("model not preserved: %s", got)
	}
}

func TestDropOrphanResponsesToolCallsUnchanged(t *testing.T) {
	for _, body := range []string{
		`{"input":"text"}`,
		`{"model":"m"}`,
		`{"input":[{"type":"function_call","call_id":"c","name":"a"},{"type":"function_call_output","call_id":"c","output":"1"}]}`,
		`{"input":[{"type":"function_call","name":"a"}]}`,
	} {
		if got := string(DropOrphanResponsesToolCalls([]byte(body))); got != body {
			t.Fatalf("body changed: %s -> %s", body, got)
		}
	}
}
