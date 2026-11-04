package responses

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

func TestResponsesRequestSelectionAndStreamIsolation(t *testing.T) {
	request := func(namespace string) []byte {
		return []byte(fmt.Sprintf(`{"tools":[{"type":"namespace","name":%q,"tools":[{"type":"function","name":"run"}]}]}`, namespace))
	}
	cases := []struct {
		original, translated []byte
		wantNamespace        string
		state                any
	}{
		{original: request("original"), translated: request("translated"), wantNamespace: "original"},
		{original: []byte(`{"broken":`), translated: request("fallback"), wantNamespace: "fallback"},
		{translated: request("separate"), wantNamespace: "separate"},
		{original: []byte(`invalid`), translated: []byte(`invalid`)},
	}
	start := []byte(`data: {"id":"r","created":1,"choices":[{"index":0,"delta":{"role":"assistant"}}]}`)
	tool := []byte(`data: {"id":"r","created":1,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c","function":{"name":"run","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`)
	// Interleave independent requests to catch accidental global cache reuse.
	for i := range cases {
		c := &cases[i]
		ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", c.original, c.translated, start, &c.state)
	}
	for i := range cases {
		c := &cases[i]
		events := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", c.original, c.translated, tool, &c.state)
		events = append(events, ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test", c.original, c.translated, []byte("data: [DONE]"), &c.state)...)
		all := bytes.Join(events, nil)
		if !bytes.Contains(all, []byte(`"name":"run"`)) {
			t.Fatalf("case %d lost tool identity", i)
		}
		if c.wantNamespace == "" {
			if bytes.Contains(all, []byte(`"namespace":`)) {
				t.Fatal("invalid requests acquired another stream's namespace")
			}
		} else if !bytes.Contains(all, []byte(fmt.Sprintf(`"namespace":%q`, c.wantNamespace))) {
			t.Fatalf("case %d lost namespace %q", i, c.wantNamespace)
		}
	}
}
