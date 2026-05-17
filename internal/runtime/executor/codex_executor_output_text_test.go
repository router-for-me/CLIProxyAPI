package executor

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// TestCodexResponseHasOutputText covers the helper used by the codex executor
// to decide whether the response.completed event already contains assistant
// text content (in which case we leave it alone), or whether we need to
// synthesize a message item from accumulated streaming deltas.
func TestCodexResponseHasOutputText(t *testing.T) {
	cases := []struct {
		name string
		line []byte
		want bool
	}{
		{
			name: "missing output array",
			line: []byte(`{"type":"response.completed","response":{}}`),
			want: false,
		},
		{
			name: "output present but no message item",
			line: []byte(`{"type":"response.completed","response":{"output":[{"type":"reasoning","summary":[]}]}}`),
			want: false,
		},
		{
			name: "message item with empty text",
			line: []byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":""}]}]}}`),
			want: false,
		},
		{
			name: "message item with text",
			line: []byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`),
			want: true,
		},
		{
			name: "message item with non-text content only",
			line: []byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"type":"input_image","image_url":"x"}]}]}}`),
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := codexResponseHasOutputText(tc.line)
			if got != tc.want {
				t.Fatalf("codexResponseHasOutputText(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// TestInjectCodexOutputText verifies that the helper appends a synthesized
// message item with output_text content to the response.output array.
func TestInjectCodexOutputText(t *testing.T) {
	t.Run("appends to existing empty output", func(t *testing.T) {
		line := []byte(`{"type":"response.completed","response":{"id":"resp_1","output":[]}}`)
		out := injectCodexOutputText(line, "Hi there!")

		got := gjson.GetBytes(out, "response.output.0.type").String()
		if got != "message" {
			t.Fatalf("expected first output item type=message, got %q", got)
		}
		text := gjson.GetBytes(out, "response.output.0.content.0.text").String()
		if text != "Hi there!" {
			t.Fatalf("expected text=%q, got %q", "Hi there!", text)
		}
		ctype := gjson.GetBytes(out, "response.output.0.content.0.type").String()
		if ctype != "output_text" {
			t.Fatalf("expected content type=output_text, got %q", ctype)
		}
	})

	t.Run("preserves existing reasoning items", func(t *testing.T) {
		line := []byte(`{"type":"response.completed","response":{"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking..."}]}]}}`)
		out := injectCodexOutputText(line, "answer")

		// The reasoning item must still be at index 0.
		first := gjson.GetBytes(out, "response.output.0.type").String()
		if first != "reasoning" {
			t.Fatalf("expected reasoning at index 0, got %q", first)
		}
		// The synthesized message must be appended at index 1.
		second := gjson.GetBytes(out, "response.output.1.type").String()
		if second != "message" {
			t.Fatalf("expected message at index 1, got %q", second)
		}
		text := gjson.GetBytes(out, "response.output.1.content.0.text").String()
		if text != "answer" {
			t.Fatalf("expected text=%q, got %q", "answer", text)
		}
	})

	t.Run("creates output array when missing", func(t *testing.T) {
		line := []byte(`{"type":"response.completed","response":{"id":"resp_1"}}`)
		out := injectCodexOutputText(line, "hello")

		text := gjson.GetBytes(out, "response.output.0.content.0.text").String()
		if text != "hello" {
			t.Fatalf("expected text=%q, got %q", "hello", text)
		}
	})

	t.Run("escapes special characters", func(t *testing.T) {
		line := []byte(`{"type":"response.completed","response":{"output":[]}}`)
		text := "line1\nline2 \"quoted\" \\backslash"
		out := injectCodexOutputText(line, text)

		got := gjson.GetBytes(out, "response.output.0.content.0.text").String()
		if got != text {
			t.Fatalf("expected text=%q, got %q", text, got)
		}
	})
}

// TestCodexExecutor_AccumulatesDeltasAndInjects exercises the accumulator
// loop directly by reproducing what the executor does with a real-shape
// gpt-5.x SSE body where response.completed has no output_text content.
//
// We don't construct a full executor + http server here — the loop logic is
// the bug surface, so this test focuses on the inputs/outputs of that loop.
func TestCodexExecutor_AccumulatesDeltasAndInjects(t *testing.T) {
	// Simulated SSE body from a gpt-5.x reasoning model: deltas carry the
	// text, and response.completed has only reasoning + an empty message.
	sse := []byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"created_at\":1700000000,\"model\":\"gpt-5.4\"}}\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\"}\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"!\"}\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\" There.\"}\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[{\"type\":\"reasoning\",\"summary\":[]}],\"usage\":{\"output_tokens\":3,\"input_tokens\":5,\"total_tokens\":8}}}\n")

	// Replicate the executor's loop locally so the test exercises the same
	// accumulator/injection path. (Calling the executor end-to-end would
	// require a full HTTP fixture and is overkill for the regression we're
	// guarding against.)
	var accumulatedText strings.Builder
	var completedLine []byte
	dataPrefix := []byte("data:")
	for _, line := range bytesSplitLines(sse) {
		if !startsWith(line, dataPrefix) {
			continue
		}
		line = trimSpace(line[5:])
		eventType := gjson.GetBytes(line, "type").String()
		if eventType == "response.output_text.delta" {
			if delta := gjson.GetBytes(line, "delta"); delta.Exists() {
				accumulatedText.WriteString(delta.String())
			}
			continue
		}
		if eventType == "response.completed" {
			completedLine = line
		}
	}

	if completedLine == nil {
		t.Fatal("expected to find response.completed event")
	}
	if got := accumulatedText.String(); got != "Hi! There." {
		t.Fatalf("expected accumulated text %q, got %q", "Hi! There.", got)
	}
	if codexResponseHasOutputText(completedLine) {
		t.Fatal("expected response.completed to have no output_text before injection")
	}

	completedLine = injectCodexOutputText(completedLine, accumulatedText.String())

	if !codexResponseHasOutputText(completedLine) {
		t.Fatal("expected response.completed to have output_text after injection")
	}

	got := gjson.GetBytes(completedLine, "response.output.1.content.0.text").String()
	if got != "Hi! There." {
		t.Fatalf("expected injected text %q, got %q", "Hi! There.", got)
	}
}

// Tiny helpers to keep the test self-contained without pulling in additional
// imports beyond what the executor file already uses.
func bytesSplitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

func startsWith(b, prefix []byte) bool {
	if len(b) < len(prefix) {
		return false
	}
	for i := range prefix {
		if b[i] != prefix[i] {
			return false
		}
	}
	return true
}

func trimSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end && (b[start] == ' ' || b[start] == '\t' || b[start] == '\r' || b[start] == '\n') {
		start++
	}
	for end > start && (b[end-1] == ' ' || b[end-1] == '\t' || b[end-1] == '\r' || b[end-1] == '\n') {
		end--
	}
	return b[start:end]
}
