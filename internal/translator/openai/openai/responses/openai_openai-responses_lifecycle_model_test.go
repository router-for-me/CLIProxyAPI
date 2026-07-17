package responses

import (
	"context"
	"testing"
)

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_LifecycleEventsIncludeRequiredResponseFields(t *testing.T) {
	t.Parallel()

	request := []byte(`{"model":"grok-4.5","stream":true}`)
	chunk := []byte(`data: {"id":"chatcmpl_grok","object":"chat.completion.chunk","created":1773896263,"choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`)

	var param any
	events := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(
		context.Background(),
		"grok-4.5",
		request,
		request,
		chunk,
		&param,
	)

	wantEvents := map[string]bool{
		"response.created":     false,
		"response.in_progress": false,
	}
	for _, chunk := range events {
		event, data := parseOpenAIResponsesSSEEvent(t, chunk)
		if _, ok := wantEvents[event]; !ok {
			continue
		}
		wantEvents[event] = true
		if got := data.Get("response.model").String(); got != "grok-4.5" {
			t.Fatalf("%s response.model = %q, want grok-4.5; event=%s", event, got, data.Raw)
		}
		output := data.Get("response.output")
		if !output.Exists() || !output.IsArray() {
			t.Fatalf("%s response.output must be an array; event=%s", event, data.Raw)
		}
		background := data.Get("response.background")
		if !background.Exists() || background.Bool() {
			t.Fatalf("%s response.background must be false; event=%s", event, data.Raw)
		}
		responseError := data.Get("response.error")
		if !responseError.Exists() || responseError.Raw != "null" {
			t.Fatalf("%s response.error must be null; event=%s", event, data.Raw)
		}
	}

	for event, seen := range wantEvents {
		if !seen {
			t.Fatalf("missing %s event", event)
		}
	}
}
