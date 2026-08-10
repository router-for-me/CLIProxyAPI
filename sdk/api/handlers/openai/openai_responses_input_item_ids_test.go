package openai

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeResponsesInputItemIDsRewritesObservedLocalIDs(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":[{"type":"function_call","id":"item_call123","call_id":"call_1","name":"wait"},{"type":"function_call_output","id":"fco_1","call_id":"call_1","output":"done"},{"type":"message","id":"item_msg456","role":"assistant","content":[]}]}`)

	got := normalizeResponsesInputItemIDs(payload)

	checks := map[string]string{
		"input.0.id":      "fc_call123",
		"input.0.call_id": "call_1",
		"input.1.id":      "fco_1",
		"input.1.call_id": "call_1",
		"input.2.id":      "msg_msg456",
		"input.2.role":    "assistant",
	}
	for path, want := range checks {
		if actual := gjson.GetBytes(got, path).String(); actual != want {
			t.Fatalf("%s = %q, want %q; payload=%s", path, actual, want, got)
		}
	}
}

func TestNormalizeResponsesInputItemIDsPreservesValidAndUnknownItems(t *testing.T) {
	payload := []byte(`{"input":[{"type":"function_call","id":"fc_valid","call_id":"call_1"},{"type":"message","id":"msg_valid","role":"assistant"},{"type":"custom_tool_call","id":"item_custom","call_id":"call_2"}]}`)

	got := normalizeResponsesInputItemIDs(payload)

	if !bytes.Equal(got, payload) {
		t.Fatalf("payload changed unexpectedly: %s", got)
	}
}
