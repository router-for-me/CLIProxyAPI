package helps

import (
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func TestIsInteractionsTokenEvent(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{"created", `{"event_type":"interaction.created","interaction":{"id":"i1"}}`, false},
		{"step start", `{"event_type":"step.start","index":0,"step":{"type":"model_output"}}`, false},
		{"empty text delta", `{"event_type":"step.delta","delta":{"type":"text","text":""}}`, false},
		{"text delta", `{"event_type":"step.delta","delta":{"type":"text","text":"hi"}}`, true},
		{"text delta with data prefix", `data: {"event_type":"step.delta","delta":{"type":"text","text":"hi"}}`, true},
		{"thought summary", `{"event_type":"step.delta","delta":{"type":"thought_summary","content":{"type":"text","text":"hmm"}}}`, true},
		{"arguments", `{"event_type":"step.delta","delta":{"type":"arguments_delta","arguments":"{\"a\""}}`, true},
		{"thought signature only", `{"event_type":"step.delta","delta":{"type":"thought_signature","signature":"abc"}}`, false},
		{"completed", `{"event_type":"interaction.completed","interaction":{"id":"i1"}}`, true},
		{"gemini candidate is not an interactions event", `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsInteractionsTokenEvent([]byte(tt.payload)); got != tt.want {
				t.Fatalf("IsInteractionsTokenEvent(%s) = %v, want %v", tt.payload, got, tt.want)
			}
		})
	}
}

// Frames built by translator/common.SSEEventData start with an "event:" line.
func TestObservePluginExecutorStreamTTFT_EventPrefixedFrames(t *testing.T) {
	tests := []struct {
		protocol string
		meta     string
		token    string
	}{
		{
			protocol: "openai-response",
			meta:     "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\n",
			token:    "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n",
		},
		{
			protocol: "interactions",
			meta:     "event: interaction.created\ndata: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"i1\"}}\n\nevent: step.start\ndata: {\"event_type\":\"step.start\",\"index\":0}\n\n",
			token:    "event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"hi\"}}\n\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.protocol, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				reporter := NewUsageReporter(context.Background(), "plugin", "m", nil)
				reporter.StartResponseTTFT()
				time.Sleep(100 * time.Millisecond)
				ObservePluginExecutorStreamTTFT(tt.protocol, reporter, []byte(tt.meta))
				if reporter.IsTTFTSet() {
					t.Fatalf("lifecycle frames must not set TTFT")
				}
				time.Sleep(time.Second)
				ObservePluginExecutorStreamTTFT(tt.protocol, reporter, []byte(tt.token))
				if !reporter.IsTTFTSet() {
					t.Fatalf("token frame did not set TTFT")
				}
				if got := reporter.ttftDuration(); got != 1100*time.Millisecond {
					t.Fatalf("TTFT = %v, want 1.1s", got)
				}
			})
		})
	}
}

// One chunk can carry a lifecycle frame and a token frame together.
func TestObservePluginExecutorStreamTTFT_SeveralFramesInOneChunk(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "plugin", "m", nil)
	reporter.StartResponseTTFT()
	chunk := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"
	ObservePluginExecutorStreamTTFT("codex", reporter, []byte(chunk))
	if !reporter.IsTTFTSet() {
		t.Fatalf("token frame after a lifecycle frame in the same chunk did not set TTFT")
	}
}

// A bare JSON event spread over several lines is not SSE and must be
// classified as one object.
func TestObservePluginExecutorStreamTTFT_PrettyPrintedBareJSON(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "plugin", "m", nil)
	reporter.StartResponseTTFT()
	payload := "{\n  \"type\": \"response.output_text.delta\",\n  \"delta\": \"hi\"\n}"
	ObservePluginExecutorStreamTTFT("openai-response", reporter, []byte(payload))
	if !reporter.IsTTFTSet() {
		t.Fatalf("pretty-printed bare JSON delta did not set TTFT")
	}
}

func TestSplitSSEEventData(t *testing.T) {
	got := splitSSEEventData([]byte("event: a\ndata: {\"x\":1}\n\n: comment\r\ndata: {\"y\":\r\ndata: 2}\r\n\r\n"))
	want := []string{`{"x":1}`, "{\"y\":\n2}"}
	if len(got) != len(want) {
		t.Fatalf("got %d events %q, want %d", len(got), got, len(want))
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("event %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The served model often arrives only on the terminal event, after TTFT is set.
func TestObservePluginExecutorStreamTTFT_KeepsModelAfterTTFT(t *testing.T) {
	reporter := NewUsageReporter(context.Background(), "plugin", "m", nil)
	reporter.StartResponseTTFT()
	ObservePluginExecutorStreamTTFT("openai-response", reporter, []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n"))
	ObservePluginExecutorStreamTTFT("openai-response", reporter, []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"model\":\"served-model\"}}\n\n"))
	reporter.responseModelMu.RLock()
	got := reporter.responseModel
	reporter.responseModelMu.RUnlock()
	if got != "served-model" {
		t.Fatalf("response model = %q, want served-model", got)
	}
}
