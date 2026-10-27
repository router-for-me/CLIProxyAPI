package interactions

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

func TestGeminiInteractionsStreamSSEPayloadLines(t *testing.T) {
	const body = `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":4,"totalTokenCount":15}}`
	const wantEvents = "interaction.created,interaction.status_update,step.start,step.delta,step.stop,interaction.completed,done"
	for _, test := range []struct {
		name string
		line string
	}{
		{"bare", body},
		{"data with space", "data: " + body},
		{"data without space", "data:" + body},
		{"surrounding whitespace", "  data: " + body + "  "},
	} {
		t.Run(test.name, func(t *testing.T) {
			var param any
			out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.8-flash", nil, nil, []byte(test.line), &param)
			out = append(out, ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.8-flash", nil, nil, []byte("data: [DONE]"), &param)...)
			if got := string(eventTypes(out)); got != wantEvents {
				t.Fatalf("events = %s, want %s", got, wantEvents)
			}
			if got := gjson.GetBytes(findNthEventPayload(out, "step.delta", 0), "delta.text").String(); got != "OK" {
				t.Fatalf("text = %q, want OK", got)
			}
			if got := gjson.GetBytes(findCompletedPayload(out), "interaction.usage.total_tokens").Int(); got != 15 {
				t.Fatalf("total tokens = %d, want 15", got)
			}
		})
	}
}

func TestGeminiInteractionsStreamIgnoresSSEControlLines(t *testing.T) {
	for _, line := range []string{"", "  ", "data:", "data:  ", ": keepalive", "event: message", "id: 1", "retry: 1000"} {
		t.Run(line, func(t *testing.T) {
			var param any
			out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.8-flash", nil, nil, []byte(line), &param)
			if len(out) != 0 || param != nil {
				t.Fatalf("control line produced %q and state %#v", out, param)
			}
		})
	}
}

func TestGeminiInteractionsStreamPrefixedDoneCompletesOnce(t *testing.T) {
	var param any
	out := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.8-flash", nil, nil, []byte(`{"candidates":[{"finishReason":"STOP"}]}`), &param)
	out = append(out, ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.8-flash", nil, nil, []byte("data: [DONE]"), &param)...)
	if got, want := string(eventTypes(out)), "interaction.created,interaction.status_update,interaction.completed,done"; got != want {
		t.Fatalf("events = %s, want %s", got, want)
	}
	if got := ConvertGeminiResponseToInteractionsStream(context.Background(), "gemini-3.8-flash", nil, nil, []byte("[DONE]"), &param); len(got) != 0 {
		t.Fatalf("duplicate terminator emitted %q", got)
	}
}
