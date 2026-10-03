package translator

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCanonicalizeCodexToolArgumentsPreservesCustomText(t *testing.T) {
	for _, input := range []string{
		"*** Begin Patch\n*** Update File: x.py\n@@\n+timeout = 2500.0\n*** End Patch",
		"return 2500.0",
		`{"count":2500.0}`,
		"printf '%s' 2500.0",
	} {
		item := map[string]any{"type": "custom_tool_call", "name": "apply_patch", "input": input}
		for _, stream := range []bool{false, true} {
			var document any = map[string]any{"output": []any{item}}
			if stream {
				document = map[string]any{"type": "response.output_item.done", "item": item}
			}
			body, errMarshal := json.Marshal(document)
			if errMarshal != nil {
				t.Fatal(errMarshal)
			}
			if got := CanonicalizeCodexToolArguments(body, stream); !bytes.Equal(got, body) {
				t.Errorf("stream=%v custom text changed: %s", stream, got)
			}
		}
	}
}

func TestCanonicalizeCodexToolArgumentsCompleteEventsAgree(t *testing.T) {
	events := []string{
		`{"type":"response.output_item.done","item":{"type":"function_call","arguments":"{\"count\":2500.0}"}}`,
		`{"type":"response.function_call_arguments.done","arguments":"{\"count\":2500.0}"}`,
		`{"type":"response.completed","response":{"output":[{"type":"function_call","arguments":"{\"count\":2500.0}"}]}}`,
		`{"type":"response.incomplete","response":{"output":[{"type":"function_call","arguments":"{\"count\":2500.0}"}]}}`,
		`{"type":"response.failed","response":{"output":[{"type":"function_call","arguments":"{\"count\":2500.0}"}]}}`,
	}
	for _, event := range events {
		want := strings.Replace(event, "2500.0", "2500", 1)
		for _, envelope := range []string{"%s", "data: %s\n\n", "event: event-name\r\ndata: %s\r\n\r\n"} {
			body := []byte(strings.Replace(envelope, "%s", event, 1))
			expected := strings.Replace(envelope, "%s", want, 1)
			if got := CanonicalizeCodexToolArguments(body, true); string(got) != expected {
				t.Errorf("got %s; want %s", got, expected)
			}
		}
	}
}

func TestCanonicalizeCodexToolArgumentsPreservesIncompleteJSON(t *testing.T) {
	for _, arguments := range []string{`{"count":2500.0`, `{"count":2500.0e}`, `2500.0 trailing text`} {
		body, errMarshal := json.Marshal(map[string]any{
			"type": "response.output_item.done",
			"item": map[string]any{"type": "function_call", "arguments": arguments},
		})
		if errMarshal != nil {
			t.Fatal(errMarshal)
		}
		if got := CanonicalizeCodexToolArguments(body, true); !bytes.Equal(got, body) {
			t.Errorf("incomplete arguments changed: %s", got)
		}
	}
}

func TestCanonicalizeCodexToolArgumentsMultipleSSEEvents(t *testing.T) {
	body := []byte("event: response.created\ndata: {\"type\":\"response.created\"}\n\n" +
		"event: response.output_item.done\ndata: " +
		`{"type":"response.output_item.done","item":{"type":"function_call","arguments":"{\"count\":2500.0}"}}` + "\n\n")
	want := strings.Replace(string(body), "2500.0", "2500", 1)
	if got := CanonicalizeCodexToolArguments(body, true); string(got) != want {
		t.Fatalf("got %s; want %s", got, want)
	}
}

func TestCanonicalizeCodexToolArgumentsMixedSnapshotExactNumbers(t *testing.T) {
	body := []byte(`{"type":"response.completed","response":{"output":[{"type":"custom_tool_call","input":"+timeout = 2500.0"},{"type":"function_call","arguments":"{\"large\":123456789012345678901234567890.000,\"exponent\":2500.0e2,\"text\":\"2500.0\",\"ratio\":0.25}"}]}}`)
	want := strings.Replace(string(body), "123456789012345678901234567890.000", "123456789012345678901234567890", 1)
	if got := CanonicalizeCodexToolArguments(body, true); string(got) != want {
		t.Fatalf("got %s; want %s", got, want)
	}
}
