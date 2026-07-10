package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestStripCodexInputNamespace(t *testing.T) {
	body := []byte(`{"input":[` +
		`{"type":"message","role":"user","content":[]},` +
		`{"type":"function_call","name":"exec","namespace":"exec_command","call_id":"c1","arguments":"{}"},` +
		`{"type":"custom_tool_call","name":"apply_patch","namespace":"apply_patch","call_id":"c2","input":"x"}` +
		`]}`)

	out := stripCodexInputNamespace(body)

	if gjson.GetBytes(out, "input.1.namespace").Exists() {
		t.Fatalf("namespace should be stripped from input[1]")
	}
	if gjson.GetBytes(out, "input.2.namespace").Exists() {
		t.Fatalf("namespace should be stripped from input[2]")
	}
	// Sibling fields on the same items must be preserved.
	if got := gjson.GetBytes(out, "input.1.call_id").String(); got != "c1" {
		t.Fatalf("call_id preserved: got %q", got)
	}
	if got := gjson.GetBytes(out, "input.1.name").String(); got != "exec" {
		t.Fatalf("name preserved: got %q", got)
	}
	if got := gjson.GetBytes(out, "input.2.type").String(); got != "custom_tool_call" {
		t.Fatalf("type preserved: got %q", got)
	}
	// Items without a namespace are untouched.
	if got := gjson.GetBytes(out, "input.0.type").String(); got != "message" {
		t.Fatalf("message item intact: got %q", got)
	}
}

func TestStripCodexInputNamespace_NoInput(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5"}`)
	if out := stripCodexInputNamespace(body); string(out) != string(body) {
		t.Fatalf("body without input array should be unchanged")
	}
}
