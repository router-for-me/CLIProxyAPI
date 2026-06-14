package common

import (
	"bytes"
	"testing"
)

func TestWrapGeminiCLIResponse(t *testing.T) {
	tests := []struct {
		name  string
		input []byte
		want  string
	}{
		{
			name:  "wraps simple response",
			input: []byte(`{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`),
			want:  `{"response":{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}}`,
		},
		{
			name:  "wraps empty object",
			input: []byte(`{}`),
			want:  `{"response":{}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := WrapGeminiCLIResponse(tt.input)
			if !bytes.Equal(got, []byte(tt.want)) {
				t.Errorf("WrapGeminiCLIResponse() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestGeminiTokenCountJSON(t *testing.T) {
	tests := []struct {
		name  string
		count int64
		want  string
	}{
		{name: "zero tokens", count: 0, want: `{"totalTokens":0,"promptTokensDetails":[{"modality":"TEXT","tokenCount":0}]}`},
		{name: "positive tokens", count: 42, want: `{"totalTokens":42,"promptTokensDetails":[{"modality":"TEXT","tokenCount":42}]}`},
		{name: "large count", count: 100000, want: `{"totalTokens":100000,"promptTokensDetails":[{"modality":"TEXT","tokenCount":100000}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GeminiTokenCountJSON(tt.count)
			if !bytes.Equal(got, []byte(tt.want)) {
				t.Errorf("GeminiTokenCountJSON() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestClaudeInputTokensJSON(t *testing.T) {
	tests := []struct {
		name  string
		count int64
		want  string
	}{
		{name: "zero tokens", count: 0, want: `{"input_tokens":0}`},
		{name: "positive tokens", count: 7, want: `{"input_tokens":7}`},
		{name: "large count", count: 99999, want: `{"input_tokens":99999}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClaudeInputTokensJSON(tt.count)
			if !bytes.Equal(got, []byte(tt.want)) {
				t.Errorf("ClaudeInputTokensJSON() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestSSEEventData(t *testing.T) {
	got := SSEEventData("completion", []byte(`{"text":"hello"}`))
	want := "event: completion\ndata: {\"text\":\"hello\"}"
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("SSEEventData() = %q, want %q", got, want)
	}
}

func TestAppendSSEEventString(t *testing.T) {
	tests := []struct {
		name             string
		event            string
		payload          string
		trailingNewlines int
		want             string
	}{
		{name: "no trailing newlines", event: "message", payload: `{"key":"val"}`, trailingNewlines: 0, want: "event: message\ndata: {\"key\":\"val\"}"},
		{name: "one trailing newline", event: "message", payload: `{"key":"val"}`, trailingNewlines: 1, want: "event: message\ndata: {\"key\":\"val\"}\n"},
		{name: "two trailing newlines", event: "done", payload: `[DONE]`, trailingNewlines: 2, want: "event: done\ndata: [DONE]\n\n"},
		{name: "empty payload", event: "ping", payload: "", trailingNewlines: 1, want: "event: ping\ndata: \n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf []byte
			got := AppendSSEEventString(buf, tt.event, tt.payload, tt.trailingNewlines)
			if !bytes.Equal(got, []byte(tt.want)) {
				t.Errorf("AppendSSEEventString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppendSSEEventBytes(t *testing.T) {
	tests := []struct {
		name             string
		event            string
		payload          []byte
		trailingNewlines int
		want             string
	}{
		{name: "no trailing newlines", event: "completion", payload: []byte(`{"text":"hi"}`), trailingNewlines: 0, want: "event: completion\ndata: {\"text\":\"hi\"}"},
		{name: "two trailing newlines", event: "error", payload: []byte(`{"error":"bad"}`), trailingNewlines: 2, want: "event: error\ndata: {\"error\":\"bad\"}\n\n"},
		{name: "nil payload", event: "ping", payload: nil, trailingNewlines: 1, want: "event: ping\ndata: \n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf []byte
			got := AppendSSEEventBytes(buf, tt.event, tt.payload, tt.trailingNewlines)
			if !bytes.Equal(got, []byte(tt.want)) {
				t.Errorf("AppendSSEEventBytes() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppendSSEEventString_PrependsToExisting(t *testing.T) {
	existing := []byte("prefix")
	got := AppendSSEEventString(existing, "msg", "data", 0)
	want := "prefixevent: msg\ndata: data"
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("AppendSSEEventString with prefix = %q, want %q", got, want)
	}
}

func TestAppendSSEEventBytes_PrependsToExisting(t *testing.T) {
	existing := []byte("prefix")
	got := AppendSSEEventBytes(existing, "msg", []byte("data"), 0)
	want := "prefixevent: msg\ndata: data"
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("AppendSSEEventBytes with prefix = %q, want %q", got, want)
	}
}
