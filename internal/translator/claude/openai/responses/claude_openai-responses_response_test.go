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

func TestConvertClaudeResponseToOpenAIResponses_CreatedHasModelAndCompletedHasUsage(t *testing.T) {
	in := []string{
		`data: {"type":"message_start","message":{"id":"msg_1"}}`,
		`data: {"type":"message_stop"}`,
	}

	var param any
	var out []string
	for _, line := range in {
		out = append(out, ConvertClaudeResponseToOpenAIResponses(context.Background(), "test-model", nil, nil, []byte(line), &param)...)
	}

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
