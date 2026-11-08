package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// This file covers same-format plugin executor stream normalization beyond
// the single exact-frame case: arbitrary chunk boundaries, the same-format
// passthrough fast path, response-hook interaction, and terminal seeding.

// collectDeframerOutput feeds chunks (split at the given cut points when
// non-empty) through a deframer and returns the emitted payloads plus the
// final flush result.
func collectDeframerOutput(t *testing.T, input string, cuts []int) ([]string, string) {
	t.Helper()
	deframer := &executorSSEDeframer{}
	var got []string
	prev := 0
	boundaries := append(append([]int{}, cuts...), len(input))
	for _, end := range boundaries {
		if end < prev || end > len(input) {
			t.Fatalf("invalid cut point %d for %q", end, input)
		}
		for _, chunk := range deframer.feed([]byte(input[prev:end])) {
			got = append(got, string(chunk))
		}
		prev = end
	}
	return got, string(deframer.flush())
}

func assertStringsEqual(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d chunks %q, want %d %q", label, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s chunk %d = %q, want %q", label, i, got[i], want[i])
		}
	}
}

func TestExecutorSSEDeframerBoundaries(t *testing.T) {
	frame1 := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"
	frame2 := "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"
	frameDone := "data: [DONE]\n\n"
	full := frame1 + frame2 + frameDone
	wantEvents := []string{frame1, frame2, frameDone}

	t.Run("whole chunk", func(t *testing.T) {
		got, tail := collectDeframerOutput(t, full, nil)
		assertStringsEqual(t, "whole", got, wantEvents)
		if tail != "" {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("arbitrary splits", func(t *testing.T) {
		// Split inside the data prefix, mid-JSON, inside the blank-line
		// boundary, and several bytes at a time.
		var cuts []int
		for i := 3; i < len(full); i += 7 {
			cuts = append(cuts, i)
		}
		got, tail := collectDeframerOutput(t, full, cuts)
		assertStringsEqual(t, "splits", got, wantEvents)
		if tail != "" {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("coalesced done in same chunk", func(t *testing.T) {
		deframer := &executorSSEDeframer{}
		got := deframer.feed([]byte(frame1 + frameDone))
		want := []string{frame1, frameDone}
		if len(got) != len(want) {
			t.Fatalf("feed returned %d chunks %q, want %d %q", len(got), got, len(want), want)
		}
		for i := range want {
			if string(got[i]) != want[i] {
				t.Errorf("chunk %d = %q, want %q", i, got[i], want[i])
			}
		}
		if tail := deframer.flush(); len(tail) != 0 {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("crlf split across chunks", func(t *testing.T) {
		crlf := "data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\r\n\r\n" + "data: [DONE]\r\n\r\n"
		// Cut between the CR and LF of the blank line terminating event one.
		cut := strings.Index(crlf, "}\r\n\r") + len("}\r\n\r")
		got, tail := collectDeframerOutput(t, crlf, []int{cut})
		want := []string{
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\r\n\r\n",
			"data: [DONE]\r\n\r\n",
		}
		assertStringsEqual(t, "crlf", got, want)
		if tail != "" {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("mixed line endings", func(t *testing.T) {
		mixed := "data: {\"a\":1}\r\n\n" + "data: {\"b\":2}\n\r\n"
		got, tail := collectDeframerOutput(t, mixed, nil)
		want := []string{"data: {\"a\":1}\r\n\n", "data: {\"b\":2}\n\r\n"}
		assertStringsEqual(t, "mixed", got, want)
		if tail != "" {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("event line framing", func(t *testing.T) {
		input := "event: message\ndata: {\"a\":1}\n\nevent: stop\ndata: {\"b\":2}\n\n"
		got, tail := collectDeframerOutput(t, input, nil)
		want := []string{
			"event: message\ndata: {\"a\":1}\n\n",
			"event: stop\ndata: {\"b\":2}\n\n",
		}
		assertStringsEqual(t, "event", got, want)
		if tail != "" {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("comment only events dropped", func(t *testing.T) {
		input := ": ping\n\n" + frame1 + ": ping\n: pong\n\n" + frameDone
		got, tail := collectDeframerOutput(t, input, nil)
		assertStringsEqual(t, "comments", got, []string{frame1, frameDone})
		if tail != "" {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("leading blank lines dropped after an sse start", func(t *testing.T) {
		// Once a stream is in SSE mode (here: the first chunk begins an event
		// that is still incomplete when the blanks arrive), unmatched blank
		// lines are stale event boundaries and are dropped.
		deframer := &executorSSEDeframer{}
		if got := deframer.feed([]byte("data: {\"choices\":")); len(got) != 0 {
			t.Fatalf("partial event emitted %q, want buffered", got)
		}
		var got []string
		for _, chunk := range deframer.feed([]byte("[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" + "\n\r\n" + frameDone + "\n")) {
			got = append(got, string(chunk))
		}
		assertStringsEqual(t, "blanks", got, []string{frame1, frameDone})
		if tail := deframer.flush(); len(tail) != 0 {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("leading newline bare payload stays whole", func(t *testing.T) {
		// A leading newline alone does not make a chunk SSE: bare JSON that
		// opens with a newline must stay one untouched payload, never split
		// per line.
		input := "\n{\n\"choices\":[]\n}\n"
		got, tail := collectDeframerOutput(t, input, nil)
		assertStringsEqual(t, "bare leading newline", got, []string{input})
		if tail != "" {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("blank line between events across chunks", func(t *testing.T) {
		// A blank line that arrives in a later chunk, between two SSE
		// events, is framing: the following event (itself split mid-JSON)
		// must still be reassembled instead of leaking through with its
		// SSE prefix intact.
		deframer := &executorSSEDeframer{}
		first := deframer.feed([]byte("data: {\"choices\":[]}\n\n"))
		if len(first) != 1 || string(first[0]) != "data: {\"choices\":[]}\n\n" {
			t.Fatalf("first feed = %q, want the first event", first)
		}
		if got := deframer.feed([]byte("\ndata:{\"choices\":")); len(got) != 0 {
			t.Fatalf("inter-event blank + partial emitted %q, want buffered", got)
		}
		last := deframer.feed([]byte("[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"))
		if len(last) != 1 || string(last[0]) != "data:{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" {
			t.Fatalf("last feed = %q, want one reassembled event", last)
		}
		if tail := deframer.flush(); len(tail) != 0 {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("blank line between events across chunks crlf", func(t *testing.T) {
		deframer := &executorSSEDeframer{}
		_ = deframer.feed([]byte("data: {\"choices\":[]}\r\n\r\n"))
		if got := deframer.feed([]byte("\r\ndata: {\"choices\":")); len(got) != 0 {
			t.Fatalf("crlf inter-event blank + partial emitted %q, want buffered", got)
		}
		last := deframer.feed([]byte("[{\"delta\":{},\"finish_reason\":\"stop\"}]}\r\n\r\n"))
		if len(last) != 1 || string(last[0]) != "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\r\n\r\n" {
			t.Fatalf("last feed = %q, want one reassembled event", last)
		}
		if tail := deframer.flush(); len(tail) != 0 {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("bare line after sse event keeps next event framed", func(t *testing.T) {
		deframer := &executorSSEDeframer{}
		_ = deframer.feed([]byte("data: {\"choices\":[]}\n\n"))
		var got []string
		for _, chunk := range deframer.feed([]byte("{\"bare\":true}\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")) {
			got = append(got, string(chunk))
		}
		assertStringsEqual(t, "sse-then-bare-then-sse", got, []string{
			"{\"bare\":true}\n",
			"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n",
		})
		if tail := deframer.flush(); len(tail) != 0 {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("data prefix without space is sse", func(t *testing.T) {
		// The space after "data:" is optional. A no-space event split across
		// chunks must be buffered and reassembled, not released early as a
		// bare payload.
		deframer := &executorSSEDeframer{}
		if got := deframer.feed([]byte("data:{\"choices\":")); len(got) != 0 {
			t.Fatalf("no-space partial event emitted %q, want buffered", got)
		}
		got := deframer.feed([]byte("[]}\n\n"))
		if len(got) != 1 || string(got[0]) != "data:{\"choices\":[]}\n\n" {
			t.Fatalf("feed = %q, want one reassembled event", got)
		}
		if tail := deframer.flush(); len(tail) != 0 {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("bare lines in one chunk stay one payload", func(t *testing.T) {
		// Bare payloads keep their original chunk boundary: lines arriving in
		// a single chunk are delivered as that chunk, not split per line.
		input := "{\"a\":1}\n{\"b\":2}\n"
		got, tail := collectDeframerOutput(t, input, nil)
		assertStringsEqual(t, "bare", got, []string{"{\"a\":1}\n{\"b\":2}\n"})
		if tail != "" {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("bare line then event in separate chunks", func(t *testing.T) {
		deframer := &executorSSEDeframer{}
		var got []string
		for _, chunk := range deframer.feed([]byte("{\"a\":1}\n")) {
			got = append(got, string(chunk))
		}
		for _, chunk := range deframer.feed([]byte(frame1)) {
			got = append(got, string(chunk))
		}
		assertStringsEqual(t, "bare+event", got, []string{"{\"a\":1}\n", frame1})
	})

	t.Run("event then bare line", func(t *testing.T) {
		input := frame1 + "{\"b\":2}\n"
		got, _ := collectDeframerOutput(t, input, nil)
		assertStringsEqual(t, "event+bare", got, []string{frame1, "{\"b\":2}\n"})
	})

	t.Run("bare line after sse event across chunks", func(t *testing.T) {
		deframer := &executorSSEDeframer{}
		_ = deframer.feed([]byte(frame1))
		got := deframer.feed([]byte("{\"b\":2}\n"))
		if len(got) != 1 || string(got[0]) != "{\"b\":2}\n" {
			t.Fatalf("feed after event = %q, want the bare line", got)
		}
		if tail := deframer.flush(); len(tail) != 0 {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("unterminated bare chunk passes through immediately", func(t *testing.T) {
		deframer := &executorSSEDeframer{}
		got := deframer.feed([]byte("plugin-example"))
		if len(got) != 1 || string(got[0]) != "plugin-example" {
			t.Fatalf("feed(%q) = %q, want the chunk itself", "plugin-example", got)
		}
		if tail := deframer.flush(); len(tail) != 0 {
			t.Errorf("flush = %q, want empty", tail)
		}
	})

	t.Run("flush strips trailing line framing", func(t *testing.T) {
		deframer := &executorSSEDeframer{}
		_ = deframer.feed([]byte("data: [DONE]\r"))
		if tail := string(deframer.flush()); tail != "data: [DONE]" {
			t.Errorf("flush = %q, want %q", tail, "data: [DONE]")
		}
		blank := &executorSSEDeframer{}
		_ = blank.feed([]byte("\r\n"))
		if tail := blank.flush(); len(tail) != 0 {
			t.Errorf("blank flush = %q, want empty", tail)
		}
	})
}

func collectStreamChunkPayloads(t *testing.T, chunks <-chan coreexecutor.StreamChunk) ([]string, error) {
	t.Helper()
	var got []string
	var streamErr error
	for chunk := range chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
		if len(chunk.Payload) > 0 {
			got = append(got, string(chunk.Payload))
		}
	}
	return got, streamErr
}

func streamAdapterForTest(t *testing.T, format sdktranslator.Format, payloads ...[]byte) *executorAdapter {
	t.Helper()
	record := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-stream-test"})
	host := newHostWithRecords(record)
	streamChunks := make(chan pluginapi.ExecutorStreamChunk, len(payloads))
	for _, payload := range payloads {
		streamChunks <- pluginapi.ExecutorStreamChunk{Payload: payload}
	}
	close(streamChunks)
	exec := &fakeExecutor{
		identifier: "plugin-provider-stream-test",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: streamChunks}, nil
		},
	}
	adapter := newExecutorAdapterForRecordForTest(host, record, exec,
		[]sdktranslator.Format{format},
		[]sdktranslator.Format{format},
	)
	adapter.provider = "plugin-provider-stream-test"
	return adapter
}

func executeSameFormatStream(t *testing.T, adapter *executorAdapter, format sdktranslator.Format) ([]string, error) {
	t.Helper()
	auth := &coreauth.Auth{ID: "auth-stream-test-1", Provider: "plugin-provider-stream-test"}
	req := coreexecutor.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","stream":true}`)}
	opts := coreexecutor.Options{SourceFormat: format, ResponseFormat: format, Stream: true}
	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}
	return collectStreamChunkPayloads(t, streamRes.Chunks)
}

// TestExecutorAdapterExecuteStreamSameFormatArbitrarySplits covers the raw
// HTTP relay case: one SSE event is split mid-JSON across two chunks and the
// [DONE] marker shares a chunk with the final data frame. The adapter output
// must equal exactly the bare JSON payloads with a single terminal marker
// dropped, and re-framing them the way the OpenAI HTTP handler does must
// produce exactly one "data: " prefix per frame and one [DONE].
func TestExecutorAdapterExecuteStreamSameFormatArbitrarySplits(t *testing.T) {
	adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI,
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hel"),
		[]byte("lo\"}}]}\n\ndata: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
	)

	got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
	if err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	want := []string{
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	}
	assertStringsEqual(t, "split", got, want)

	var wire bytes.Buffer
	for _, payload := range got {
		fmt.Fprintf(&wire, "data: %s\n\n", payload)
	}
	wire.WriteString("data: [DONE]\n\n")
	wantWire := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"
	if wire.String() != wantWire {
		t.Errorf("handler-framed wire =\n%q\nwant:\n%q", wire.String(), wantWire)
	}
	if strings.Contains(wire.String(), "data: data:") {
		t.Errorf("wire contains doubled framing: %q", wire.String())
	}
	if strings.Count(wire.String(), "[DONE]") != 1 {
		t.Errorf("wire = %q, want exactly one [DONE]", wire.String())
	}
}

// TestExecutorAdapterExecuteStreamSameFormatMissingDoneIsSeededAndDropped
// covers a stream that ends without [DONE]: the translation tail feeds one
// terminal marker through the translator so it can finalize, and the marker
// itself never reaches adapter callers (the HTTP handler writes the only
// [DONE]).
func TestExecutorAdapterExecuteStreamSameFormatBlankLineBetweenEvents(t *testing.T) {
	// A blank line landing between two SSE events in a later chunk must
	// not push the stream back into bare mode: the following event (split
	// mid-JSON) must be reassembled and stripped like any other.
	adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI,
		[]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"),
		[]byte("\ndata:{\"choices\":"),
		[]byte("[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
	)
	got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
	if err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	assertStringsEqual(t, "blank between events", got, []string{
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	})
}

func TestExecutorAdapterExecuteStreamSameFormatNoSpaceDataPrefix(t *testing.T) {
	// The space after "data:" is optional in SSE. A same-format event using
	// the no-space form and split mid-payload across chunks must still be
	// reassembled into one event and stripped to bare JSON.
	adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI,
		[]byte("data:{\"choices\":"),
		[]byte("[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata:[DONE]\n\n"),
	)
	got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
	if err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	assertStringsEqual(t, "no space", got, []string{
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	})
}

func TestExecutorAdapterExecuteStreamSameFormatMissingDoneIsSeededAndDropped(t *testing.T) {
	adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI,
		[]byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"),
	)
	got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
	if err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	assertStringsEqual(t, "seeded", got, []string{
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	})
}

// TestExecutorAdapterExecuteStreamGeminiSameFormatStripsSSEFraming covers the
// second registered same-format normalizer (Gemini to Gemini) under split
// and coalesced chunk delivery. Gemini streams have no [DONE] marker, so
// nothing extra is seeded when the channel closes.
func TestExecutorAdapterExecuteStreamGeminiSameFormatStripsSSEFraming(t *testing.T) {
	adapter := streamAdapterForTest(t, sdktranslator.FormatGemini,
		[]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Hi\"}]}}"),
		[]byte("]}\n\ndata: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n"),
	)
	got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatGemini)
	if err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	want := []string{
		`{"candidates":[{"content":{"parts":[{"text":"Hi"}]}}]}`,
		`{"candidates":[{"finishReason":"STOP"}]}`,
	}
	assertStringsEqual(t, "gemini", got, want)
}

// TestExecutorAdapterExecuteStreamSameFormatBareJSONUnchanged covers plugins
// that already emit bare payloads (as the documented examples do): they must
// pass through both the deframer and the same-format translator untouched.
func TestExecutorAdapterExecuteStreamSameFormatBareJSONUnchanged(t *testing.T) {
	t.Run("no trailing newline", func(t *testing.T) {
		adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI, []byte("plugin-example"))
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "bare", got, []string{"plugin-example"})
	})

	t.Run("bare objects keep chunk boundaries without newlines", func(t *testing.T) {
		// Plugins that emit bare JSON objects without line breaks rely on
		// one chunk per object: the objects must not be concatenated into
		// one invalid payload across chunk boundaries.
		adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI,
			[]byte(`{"choices":[{"delta":{"content":"Hi"}}]}`),
			[]byte(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`),
		)
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "bare objects", got, []string{
			`{"choices":[{"delta":{"content":"Hi"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		})
	})

	t.Run("leading newline bare payload stays whole", func(t *testing.T) {
		adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI, []byte("\n{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n"))
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "bare leading newline", got, []string{
			"\n{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n",
		})
	})

	t.Run("partial sse prefix stays buffered", func(t *testing.T) {
		// A first chunk holding only "da" must not be released as a bare
		// payload: later bytes complete the SSE data line and the deframer
		// must rebuild the whole event before translation.
		adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI,
			[]byte("da"),
			[]byte("ta: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
		)
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "partial prefix", got, []string{
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		})
	})

	t.Run("bare json line", func(t *testing.T) {
		adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI, []byte("{\"choices\":[]}\n"))
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "bare line", got, []string{"{\"choices\":[]}\n"})
	})

	t.Run("leading newline bare payloads across chunks stay whole", func(t *testing.T) {
		// In bare mode (no SSE event ever seen), consecutive newline-led
		// chunks must keep their boundaries: they are never dropped as
		// blank lines and never merged.
		adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI,
			[]byte("\n{\"a\":1}"),
			[]byte("\n{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}"),
		)
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "bare newline chunks", got, []string{
			"\n{\"a\":1}",
			"\n{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}",
		})
	})

	t.Run("bare lines with finish reason feed the handler", func(t *testing.T) {
		// A plugin that emits bare JSON lines (no SSE framing anywhere) must
		// still deliver the finish_reason frame to callers: the OpenAI HTTP
		// handler writes its only [DONE] marker only after some chunk
		// carried a finish_reason, and otherwise fails the stream as
		// truncated. The [DONE] fed through the tail path is dropped by the
		// same-format translator and never appears as an adapter payload.
		adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAI,
			[]byte("{\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n"),
			[]byte("{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n"),
		)
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAI)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "bare finish", got, []string{
			"{\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n",
			"{\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n",
		})

		var wire bytes.Buffer
		for _, payload := range got {
			fmt.Fprintf(&wire, "data: %s\n\n", payload)
		}
		wire.WriteString("data: [DONE]\n\n")
		if strings.Count(wire.String(), "[DONE]") != 1 {
			t.Errorf("wire = %q, want exactly one [DONE]", wire.String())
		}
		if strings.Contains(wire.String(), "data: data:") {
			t.Errorf("wire contains doubled framing: %q", wire.String())
		}
	})
}

// TestExecutorAdapterExecuteStreamSameFormatFramedPassthrough covers formats
// without a registered same-format stream translator and with no response
// hooks installed: SSE framing (including event lines, partial frames, and
// the [DONE] marker for responses) must reach callers byte for byte because
// those handlers consume verbatim frames.
func TestExecutorAdapterExecuteStreamSameFormatFramedPassthrough(t *testing.T) {
	t.Run("claude event frames unchanged", func(t *testing.T) {
		payload := []byte("event: message\ndata: {\"type\":\"message\"}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		adapter := streamAdapterForTest(t, sdktranslator.FormatClaude, payload)
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatClaude)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "claude", got, []string{string(payload)})
	})

	t.Run("responses frames unchanged apart from usage details", func(t *testing.T) {
		frame := []byte("data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":3,\"output_tokens\":5}}}\n\n")
		adapter := streamAdapterForTest(t, sdktranslator.FormatOpenAIResponse, frame)
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatOpenAIResponse)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("got %d chunks %q, want 1", len(got), got)
		}
		if !strings.HasPrefix(got[0], "data: ") {
			t.Errorf("responses frame lost its SSE prefix: %q", got[0])
		}
		if !strings.Contains(got[0], "\"input_tokens_details\"") || !strings.Contains(got[0], "\"output_tokens_details\"") {
			t.Errorf("responses usage details not normalized: %q", got[0])
		}
		if strings.Contains(got[0], "[DONE]") {
			t.Errorf("responses stream gained a chat [DONE] marker: %q", got[0])
		}
	})

	t.Run("partial frames unchanged", func(t *testing.T) {
		adapter := streamAdapterForTest(t, sdktranslator.FormatClaude,
			[]byte("event: message\ndata: {\"ty"),
			[]byte("pe\":\"message\"}\n\n"),
		)
		got, err := executeSameFormatStream(t, adapter, sdktranslator.FormatClaude)
		if err != nil {
			t.Fatalf("stream returned error: %v", err)
		}
		assertStringsEqual(t, "partial", got, []string{
			"event: message\ndata: {\"ty",
			"pe\":\"message\"}\n\n",
		})
	})
}

// TestExecutorAdapterExecuteStreamCrossFormatSplitKeepsUsageAndData covers
// the safety net around the changes above: cross-format plugin streams never
// enter the same-format deframer or passthrough gate, so a cross-format
// split stream must still be translated frame by frame and its usage must
// still be observed from the raw plugin chunks.
func TestExecutorAdapterExecuteStreamCrossFormatKeepsUsageAndData(t *testing.T) {
	plugin := newTestUsageCapturePlugin("plugin-provider-cross-split")
	registerTestUsagePlugin(t, "test-executor-adapter-cross-split", plugin)

	record := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-cross-split"})
	host := newHostWithRecords(record)

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 3)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":8,\"total_tokens\":15}}\n\ndata: [DONE]\n\n")}
	close(streamChunks)

	exec := &fakeExecutor{
		identifier: "plugin-provider-cross-split",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: streamChunks}, nil
		},
	}
	adapter := newExecutorAdapterForRecordForTest(host, record, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-cross-split"

	auth := &coreauth.Auth{ID: "auth-cross-split-1", Provider: "plugin-provider-cross-split"}
	req := coreexecutor.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","stream":true}`)}
	opts := coreexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
	}
	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}
	got, streamErr := collectStreamChunkPayloads(t, streamRes.Chunks)
	if streamErr != nil {
		t.Fatalf("stream returned error: %v", streamErr)
	}
	if len(got) == 0 {
		t.Fatal("expected translated claude frames, got none")
	}
	joined := strings.Join(got, "")
	if !strings.Contains(joined, "Hello") {
		t.Errorf("translated claude frames lost the content delta: %q", joined)
	}
	if !strings.Contains(joined, "message_start") || !strings.Contains(joined, "message_stop") {
		t.Errorf("translated claude stream is missing start/stop events: %q", joined)
	}
	if strings.Contains(joined, "data: data:") {
		t.Errorf("cross-format output contains doubled framing: %q", joined)
	}
	rec := plugin.waitRecord(t, 500*time.Millisecond)
	if rec.Detail.InputTokens != 7 || rec.Detail.OutputTokens != 8 || rec.Detail.TotalTokens != 15 {
		t.Errorf("got usage %+v, want prompt=7 completion=8 total=15", rec.Detail)
	}
}

// TestExecutorAdapterExecuteStreamSameFormatHooksStillApplied covers the hook
// path of the same-format gate: when an active plugin provides response
// translation hooks, a same-format stream without a native translator still
// goes through TranslateStream, so hook rewrites apply. Without the hooks it
// remains verbatim (covered by TestExecutorAdapterExecuteStreamSameFormatFramedPassthrough).
func TestExecutorAdapterExecuteStreamSameFormatHooksStillApplied(t *testing.T) {
	hookRecord := normalizeTestCapabilityRecord(capabilityRecord{
		id: "response-hook-plugin",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ResponseBeforeTranslator: &testResponseNormalizerFunc{
				normalize: func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: bytes.ReplaceAll(req.Body, []byte("want"), []byte("hook"))}, nil
				},
			},
			ResponseAfterTranslator: &testResponseNormalizerFunc{
				normalize: func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					return pluginapi.PayloadResponse{Body: append(bytes.Clone(req.Body), 'X')}, nil
				},
			},
		}},
	})
	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-hook-stream"})
	host := newHostWithRecords(executorRecord, hookRecord)

	sdktranslator.SetPluginHooks(host)
	t.Cleanup(func() {
		sdktranslator.SetPluginHooks(nil)
	})

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 1)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("event: message\ndata: {\"type\":\"message\",\"content\":\"want\"}\n\n")}
	close(streamChunks)
	exec := &fakeExecutor{
		identifier: "plugin-provider-hook-stream",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: streamChunks}, nil
		},
	}
	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatClaude},
		[]sdktranslator.Format{sdktranslator.FormatClaude},
	)
	adapter.provider = "plugin-provider-hook-stream"

	auth := &coreauth.Auth{ID: "auth-hook-stream-1", Provider: "plugin-provider-hook-stream"}
	req := coreexecutor.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","stream":true}`)}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, Stream: true}
	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}
	got, err := collectStreamChunkPayloads(t, streamRes.Chunks)
	if err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	assertStringsEqual(t, "hooks", got, []string{
		"event: message\ndata: {\"type\":\"message\",\"content\":\"hook\"}\n\nX",
	})
}

// testResponseNormalizerFunc adapts a function to pluginapi.ResponseNormalizer.
type testResponseNormalizerFunc struct {
	normalize func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error)
}

func (f *testResponseNormalizerFunc) NormalizeResponse(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
	return f.normalize(ctx, req)
}

// TestDeframeExecutorSSEChunksErrorFlushesPartial covers the channel-level
// contract directly: a partial event is emitted before an error chunk, the
// error is forwarded unchanged, and no terminal marker is synthesized at
// this layer (the translation tail owns that).
func TestDeframeExecutorSSEChunksErrorFlushesPartial(t *testing.T) {
	wantErr := errors.New("stream failed")
	in := make(chan pluginapi.ExecutorStreamChunk, 2)
	in <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"a\"")}
	in <- pluginapi.ExecutorStreamChunk{Err: wantErr}
	close(in)

	out := deframeExecutorSSEChunks(context.Background(), in)
	var got []pluginapi.ExecutorStreamChunk
	for chunk := range out {
		got = append(got, chunk)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks %+v, want partial then error", len(got), got)
	}
	if string(got[0].Payload) != "data: {\"a\"" {
		t.Errorf("first chunk = %q, want partial payload", got[0].Payload)
	}
	if !errors.Is(got[1].Err, wantErr) {
		t.Errorf("second chunk error = %v, want %v", got[1].Err, wantErr)
	}
}

// TestExecutorAdapterExecuteStreamSameFormatHooksSeeTerminalOnce covers the
// terminal-marker ownership contract: a same-format chat stream missing its
// [DONE] marker feeds exactly one synthetic marker through translation
// (from emitTranslatedExecutorStreamTail), so plugin response hooks observe
// exactly one terminal payload and cannot duplicate terminal frames. The
// deframer must not add a second one.
func TestExecutorAdapterExecuteStreamSameFormatHooksSeeTerminalOnce(t *testing.T) {
	var bodies [][]byte
	hookRecord := normalizeTestCapabilityRecord(capabilityRecord{
		id: "response-hook-plugin-terminal",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			ResponseBeforeTranslator: &testResponseNormalizerFunc{
				normalize: func(ctx context.Context, req pluginapi.ResponseTransformRequest) (pluginapi.PayloadResponse, error) {
					bodies = append(bodies, bytes.Clone(req.Body))
					return pluginapi.PayloadResponse{Body: req.Body}, nil
				},
			},
		}},
	})
	executorRecord := normalizeTestCapabilityRecord(capabilityRecord{id: "executor-plugin-terminal-stream"})
	host := newHostWithRecords(executorRecord, hookRecord)

	sdktranslator.SetPluginHooks(host)
	t.Cleanup(func() {
		sdktranslator.SetPluginHooks(nil)
	})

	streamChunks := make(chan pluginapi.ExecutorStreamChunk, 2)
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")}
	streamChunks <- pluginapi.ExecutorStreamChunk{Payload: []byte("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")}
	close(streamChunks)
	exec := &fakeExecutor{
		identifier: "plugin-provider-terminal-stream",
		executeStream: func(ctx context.Context, req pluginapi.ExecutorRequest) (pluginapi.ExecutorStreamResponse, error) {
			return pluginapi.ExecutorStreamResponse{Chunks: streamChunks}, nil
		},
	}
	adapter := newExecutorAdapterForRecordForTest(host, executorRecord, exec,
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
		[]sdktranslator.Format{sdktranslator.FormatOpenAI},
	)
	adapter.provider = "plugin-provider-terminal-stream"

	auth := &coreauth.Auth{ID: "auth-terminal-stream-1", Provider: "plugin-provider-terminal-stream"}
	req := coreexecutor.Request{Model: "test-model", Payload: []byte(`{"model":"test-model","stream":true}`)}
	opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI, Stream: true}
	streamRes, err := adapter.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream returned unexpected error: %v", err)
	}
	got, err := collectStreamChunkPayloads(t, streamRes.Chunks)
	if err != nil {
		t.Fatalf("stream returned error: %v", err)
	}
	assertStringsEqual(t, "terminal", got, []string{
		`{"choices":[{"delta":{"content":"Hello"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	})

	doneCount := 0
	for _, body := range bodies {
		if bytes.Contains(body, []byte("[DONE]")) {
			doneCount++
		}
	}
	if doneCount != 1 {
		t.Errorf("response hook saw [DONE] %d times, want exactly once; bodies=%q", doneCount, bodies)
	}
	var wire bytes.Buffer
	for _, payload := range got {
		fmt.Fprintf(&wire, "data: %s\n\n", payload)
	}
	wire.WriteString("data: [DONE]\n\n")
	if strings.Count(wire.String(), "[DONE]") != 1 {
		t.Errorf("wire = %q, want exactly one [DONE]", wire.String())
	}
}

// TestDeframeExecutorSSEChunksNeverSynthesizesDone asserts the channel-level
// contract that the deframer forwards terminal markers verbatim but never
// invents one: emitTranslatedExecutorStreamTail is the single place that
// feeds a synthetic [DONE] through translation, exactly once.
func TestDeframeExecutorSSEChunksNeverSynthesizesDone(t *testing.T) {
	run := func(payload string) []string {
		t.Helper()
		in := make(chan pluginapi.ExecutorStreamChunk, 1)
		in <- pluginapi.ExecutorStreamChunk{Payload: []byte(payload)}
		close(in)
		out := deframeExecutorSSEChunks(context.Background(), in)
		var got []string
		for chunk := range out {
			got = append(got, string(chunk.Payload))
		}
		return got
	}

	t.Run("missing done is not invented", func(t *testing.T) {
		got := run("data: {\"choices\":[]}\n\n")
		assertStringsEqual(t, "no seed", got, []string{
			"data: {\"choices\":[]}\n\n",
		})
	})

	t.Run("explicit done forwarded once", func(t *testing.T) {
		got := run("data: {\"choices\":[]}\n\ndata: [DONE]\n\n")
		if n := strings.Count(strings.Join(got, "|"), "[DONE]"); n != 1 {
			t.Errorf("got %q, want exactly one [DONE]", got)
		}
	})

	t.Run("bare payloads pass through untouched", func(t *testing.T) {
		got := run("plugin-example")
		assertStringsEqual(t, "bare", got, []string{"plugin-example"})
	})
}
