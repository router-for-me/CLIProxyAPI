package responses

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_ForwardsServiceTier(t *testing.T) {
	input := []byte(`{
		"model":"gpt-5.4",
		"service_tier":"priority",
		"input":"hello"
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", input, true)

	if got := gjson.GetBytes(out, "service_tier").String(); got != "priority" {
		t.Fatalf("service_tier = %q, want %q", got, "priority")
	}
}

func TestConvertOpenAIResponsesRequestToOpenAIChatCompletions_IgnoresEmptyServiceTier(t *testing.T) {
	input := []byte(`{
		"model":"gpt-5.4",
		"service_tier":"   ",
		"input":"hello"
	}`)

	out := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("gpt-5.4", input, true)

	if gjson.GetBytes(out, "service_tier").Exists() {
		t.Fatalf("service_tier should not be set, payload=%s", string(out))
	}
}
