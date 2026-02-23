package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func parseSSEEvent(t *testing.T, chunk string) (string, gjson.Result) {
	t.Helper()

	lines := strings.Split(chunk, "\n")
	if len(lines) < 2 {
		t.Fatalf("unexpected SSE chunk: %q", chunk)
	}

	event := strings.TrimSpace(strings.TrimPrefix(lines[0], "event:"))
	dataLine := strings.TrimSpace(strings.TrimPrefix(lines[1], "data:"))
	if !gjson.Valid(dataLine) {
		t.Fatalf("invalid SSE data JSON: %q", dataLine)
	}
	return event, gjson.Parse(dataLine)
}

func TestConvertOpenAIChatCompletionsResponseToOpenAIResponses_CreatedHasModelAndCompletedHasUsage(t *testing.T) {
	in := `data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1700000000,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

	var param any
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "test-model", nil, nil, []byte(in), &param)

	gotCreated := false
	gotCompleted := false
	createdModel := ""
	for _, chunk := range out {
		ev, data := parseSSEEvent(t, chunk)
		switch ev {
		case "response.created":
			gotCreated = true
			createdModel = data.Get("response.model").String()
		case "response.completed":
			gotCompleted = true
			if !data.Get("response.usage.input_tokens").Exists() {
				t.Fatalf("response.completed missing usage.input_tokens: %s", data.Raw)
			}
			if !data.Get("response.usage.output_tokens").Exists() {
				t.Fatalf("response.completed missing usage.output_tokens: %s", data.Raw)
			}
		}
	}
	if !gotCreated {
		t.Fatalf("missing response.created event")
	}
	if createdModel != "test-model" {
		t.Fatalf("unexpected response.created model: got %q", createdModel)
	}
	if !gotCompleted {
		t.Fatalf("missing response.completed event")
	}
}
