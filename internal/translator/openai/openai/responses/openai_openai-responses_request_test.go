package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_PreservesFastModeParams(t *testing.T) {
	input := []byte(`{
		"model": "gpt-5.4",
		"service_tier": "priority",
		"text": {
			"verbosity": "low"
		},
		"reasoning": {
			"effort": "low"
		},
		"input": "ping"
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", input, true)
	result := string(out)

	if got := gjson.Get(result, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q, body=%s", got, "priority", result)
	}
	if got := gjson.Get(result, "text.verbosity").String(); got != "low" {
		t.Fatalf("text.verbosity = %q, want %q, body=%s", got, "low", result)
	}
	if got := gjson.Get(result, "reasoning_effort").String(); got != "low" {
		t.Fatalf("reasoning_effort = %q, want %q, body=%s", got, "low", result)
	}
}
