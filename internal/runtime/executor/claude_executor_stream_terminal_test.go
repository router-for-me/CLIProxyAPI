package executor

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// These regression tests distinguish completed Claude responses from
// responses that end before their protocol-level terminal condition.

const claudeStreamPrematureEOFBody = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_premature","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n" +
	"\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n" +
	"\n"

const claudeStreamCompleteBody = claudeStreamPrematureEOFBody +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n" +
	"\n"

func newClaudeStreamTerminalExecutor(server *httptest.Server) (*ClaudeExecutor, *cliproxyauth.Auth) {
	executor := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-stream-terminal",
		"base_url": server.URL,
	}}
	return executor, auth
}

type claudeStreamRecorder struct {
	payloads []string
	errs     []error
}

func (r *claudeStreamRecorder) record(result *cliproxyexecutor.StreamResult) {
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			r.errs = append(r.errs, chunk.Err)
			continue
		}
		r.payloads = append(r.payloads, string(chunk.Payload))
	}
}

func TestClaudeExecutor_ExecuteStreamDirectPassthroughPrematureEOFYieldsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeStreamPrematureEOFBody))
	}))
	defer server.Close()

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var rec claudeStreamRecorder
	rec.record(result)

	if len(rec.payloads) == 0 {
		t.Fatal("no payloads emitted before premature EOF")
	}
	for _, p := range rec.payloads {
		if strings.Contains(p, `"message_stop"`) {
			t.Fatalf("synthetic message_stop emitted after premature EOF: %q", p)
		}
	}
	if len(rec.errs) != 1 {
		t.Fatalf("error chunk count = %d, want exactly 1 after payloads (premature EOF must not close the channel silently); payloads=%d", len(rec.errs), len(rec.payloads))
	}
}

func TestClaudeExecutor_ExecuteStreamTranslatedPrematureEOFYieldsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeStreamPrematureEOFBody))
	}))
	defer server.Close()

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var rec claudeStreamRecorder
	rec.record(result)

	if len(rec.payloads) == 0 {
		t.Fatal("no translated payloads emitted before premature EOF")
	}
	for _, p := range rec.payloads {
		if strings.Contains(p, "[DONE]") {
			t.Fatalf("translated success terminal emitted after premature EOF: %q", p)
		}
	}
	if len(rec.errs) != 1 {
		t.Fatalf("error chunk count = %d, want exactly 1 after translated payloads (premature EOF must not close the channel silently); payloads=%d", len(rec.errs), len(rec.payloads))
	}
}

func runClaudeStreamBody(
	t *testing.T,
	body string,
	contentType string,
	sourceFormat sdktranslator.Format,
) claudeStreamRecorder {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if sourceFormat != sdktranslator.FromString("claude") {
		payload = []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sourceFormat})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var rec claudeStreamRecorder
	rec.record(result)
	return rec
}

func TestScanClaudeResponseLinesAcceptsSSELineEndings(t *testing.T) {
	for _, separator := range []string{"\n", "\r\n", "\r"} {
		t.Run(fmt.Sprintf("%q", separator), func(t *testing.T) {
			scanner := bufio.NewScanner(strings.NewReader(
				"event: test" + separator + "data: {}" + separator + separator,
			))
			scanner.Split(scanClaudeResponseLines)
			var lines []string
			for scanner.Scan() {
				lines = append(lines, scanner.Text())
			}
			if err := scanner.Err(); err != nil {
				t.Fatalf("scan lines: %v", err)
			}
			if got := strings.Join(lines, "|"); got != "event: test|data: {}|" {
				t.Fatalf("lines = %q", got)
			}
		})
	}
}

func TestClaudeExecutor_ExecuteStreamCROnlyMessageStopSucceeds(t *testing.T) {
	body := strings.ReplaceAll(claudeStreamCompleteBody, "\n", "\r")
	for _, test := range []struct {
		name         string
		sourceFormat sdktranslator.Format
	}{
		{name: "direct", sourceFormat: sdktranslator.FromString("claude")},
		{name: "translated", sourceFormat: sdktranslator.FormatOpenAI},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := runClaudeStreamBody(t, body, "text/event-stream", test.sourceFormat)
			if len(rec.errs) != 0 {
				t.Fatalf("complete CR-only stream produced %d error chunk(s): %v", len(rec.errs), rec.errs)
			}
			if len(rec.payloads) == 0 {
				t.Fatal("complete CR-only stream emitted no payload")
			}
		})
	}
}

func TestClaudeExecutor_ExecuteStreamMessageStopThenPlainEOFSucceeds(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(claudeStreamCompleteBody))
	}))
	defer server.Close()

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var rec claudeStreamRecorder
	rec.record(result)

	if len(rec.payloads) == 0 {
		t.Fatal("no payloads emitted for completed stream")
	}
	if len(rec.errs) != 0 {
		t.Fatalf("completed stream produced %d error chunk(s): %v", len(rec.errs), rec.errs)
	}
	joined := strings.Join(rec.payloads, "")
	if !strings.Contains(joined, `"message_stop"`) {
		t.Fatalf("completed stream is missing real message_stop: %q", joined)
	}
}

// runClaudeStreamTerminalSSE varies Content-Type without changing the SSE body
// so classification is tested independently from the header.
func runClaudeStreamTerminalSSE(t *testing.T, contentType string) claudeStreamRecorder {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		_, _ = w.Write([]byte(claudeStreamPrematureEOFBody))
	}))
	defer server.Close()

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var rec claudeStreamRecorder
	rec.record(result)
	return rec
}

func assertClaudeStreamPrematureEOFFails(t *testing.T, rec claudeStreamRecorder) {
	t.Helper()
	if len(rec.payloads) == 0 {
		t.Fatal("no payloads emitted before premature EOF")
	}
	for _, p := range rec.payloads {
		if strings.Contains(p, `"message_stop"`) {
			t.Fatalf("synthetic message_stop emitted after premature EOF: %q", p)
		}
	}
	if len(rec.errs) != 1 {
		t.Fatalf("error chunk count = %d, want exactly 1 after payloads; payloads=%d", len(rec.errs), len(rec.payloads))
	}
}

// Observable SSE framing must keep the truncation guard active even when the
// upstream omits or mislabels Content-Type.
func TestClaudeExecutor_ExecuteStreamSSEWithoutContentTypeHeaderStillFailsOnPrematureEOF(t *testing.T) {
	assertClaudeStreamPrematureEOFFails(t, runClaudeStreamTerminalSSE(t, ""))
}

func TestClaudeExecutor_ExecuteStreamSSEWithMislabelledContentTypeStillFailsOnPrematureEOF(t *testing.T) {
	assertClaudeStreamPrematureEOFFails(t, runClaudeStreamTerminalSSE(t, "application/json"))
}

func TestClaudeResponseTracker_MislabelledSSEStopsJSONCapture(t *testing.T) {
	tracker := newClaudeResponseTracker(http.Header{"Content-Type": {"application/json"}})
	body := strings.Repeat("event: content_block_delta\ndata: {}\n\n", 4096)
	scanner := bufio.NewScanner(tracker.reader(strings.NewReader(body)))
	for scanner.Scan() {
		tracker.observe(scanner.Bytes())
		if retained := tracker.jsonBody.Len(); retained != 0 {
			t.Fatalf("mislabelled SSE retained %d JSON byte(s)", retained)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan mislabelled SSE: %v", err)
	}
	if !tracker.isSSE() {
		t.Fatal("observable SSE framing was not recognized")
	}
}

// A declared length larger than the body turns connection close into a read
// error, which is distinct from a clean protocol-level EOF.
func writeClaudeBodyWithTruncatedContentLength(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Content-Length", "1048576")
	_, _ = w.Write([]byte(body))
}

func TestClaudeExecutor_ExecuteStreamMessageStopThenReadErrorStaysSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeClaudeBodyWithTruncatedContentLength(w, claudeStreamCompleteBody)
	}))
	defer server.Close()

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var rec claudeStreamRecorder
	rec.record(result)

	if len(rec.payloads) == 0 {
		t.Fatal("no payloads emitted for completed stream")
	}
	joined := strings.Join(rec.payloads, "")
	if !strings.Contains(joined, `"message_stop"`) {
		t.Fatalf("stream is missing real message_stop: %q", joined)
	}
	if len(rec.errs) != 0 {
		t.Fatalf("transport error after real message_stop turned completed stream into failure: %v", rec.errs)
	}
}

func TestClaudeExecutor_ExecuteStreamReadErrorBeforeMessageStopYieldsSingleError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeClaudeBodyWithTruncatedContentLength(w, claudeStreamPrematureEOFBody)
	}))
	defer server.Close()

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var rec claudeStreamRecorder
	rec.record(result)

	if len(rec.payloads) == 0 {
		t.Fatal("no payloads emitted before read error")
	}
	for _, p := range rec.payloads {
		if strings.Contains(p, `"message_stop"`) {
			t.Fatalf("synthetic message_stop emitted after read error: %q", p)
		}
	}
	if len(rec.errs) != 1 {
		t.Fatalf("error chunk count = %d, want exactly 1 after read error before message_stop", len(rec.errs))
	}
}

// Keep the JSON document valid so this helper isolates transport truncation
// from application-level JSON completeness.
func writeClaudeNonSSEBodyWithTruncatedContentLength(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", "1048576")
	_, _ = w.Write([]byte(body))
}

const claudeNonSSEMessageBody = `{"id":"msg_nonsse","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"text","text":"partial"}],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}`

func runClaudeStreamNonSSEResponse(
	t *testing.T,
	sourceFormat sdktranslator.Format,
	writeResponse func(http.ResponseWriter),
) claudeStreamRecorder {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponse(w)
	}))
	defer server.Close()

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	if sourceFormat != sdktranslator.FromString("claude") {
		payload = []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sourceFormat})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var rec claudeStreamRecorder
	rec.record(result)
	return rec
}

func runClaudeStreamNonSSEReadError(t *testing.T, sourceFormat sdktranslator.Format) claudeStreamRecorder {
	t.Helper()
	return runClaudeStreamNonSSEResponse(t, sourceFormat, func(w http.ResponseWriter) {
		writeClaudeNonSSEBodyWithTruncatedContentLength(w, claudeNonSSEMessageBody)
	})
}

const claudeNonSSEIncompleteMessageBody = `{"id":"msg_nonsse","type":"message","role":"assistant","content":[{"type":"text","text":"partial-json-canary"}]`

func runClaudeStreamNonSSECleanEOF(t *testing.T, sourceFormat sdktranslator.Format, body string) claudeStreamRecorder {
	t.Helper()
	return runClaudeStreamNonSSEResponse(t, sourceFormat, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	})
}

func assertClaudeIncompleteJSONFails(t *testing.T, rec claudeStreamRecorder, terminal string) {
	t.Helper()
	for _, payload := range rec.payloads {
		if terminal != "" && strings.Contains(payload, terminal) {
			t.Fatalf("successful terminal emitted after incomplete JSON: %q", payload)
		}
	}
	if len(rec.errs) != 1 {
		t.Fatalf("error chunk count = %d, want exactly 1 after incomplete JSON", len(rec.errs))
	}
	status, ok := rec.errs[0].(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("incomplete JSON error = %v, want HTTP 502", rec.errs[0])
	}
	if strings.Contains(rec.errs[0].Error(), "partial-json-canary") {
		t.Fatalf("incomplete JSON error leaked response body: %v", rec.errs[0])
	}
}

func TestClaudeExecutor_ExecuteStreamIncompleteJSONCleanEOFYieldsSingleError(t *testing.T) {
	rec := runClaudeStreamNonSSECleanEOF(
		t,
		sdktranslator.FromString("claude"),
		claudeNonSSEIncompleteMessageBody,
	)
	if len(rec.payloads) == 0 {
		t.Fatal("no payload emitted before incomplete JSON EOF")
	}
	assertClaudeIncompleteJSONFails(t, rec, `"message_stop"`)
}

func TestClaudeExecutor_ExecuteStreamTranslatedIncompleteJSONCleanEOFYieldsSingleError(t *testing.T) {
	rec := runClaudeStreamNonSSECleanEOF(
		t,
		sdktranslator.FormatOpenAI,
		claudeNonSSEIncompleteMessageBody,
	)
	assertClaudeIncompleteJSONFails(t, rec, "[DONE]")
}

func writeClaudeOversizedMultilineJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, "{\"content\":[\n")
	line := `"` + strings.Repeat("x", 1024) + `",` + "\n"
	written := len("{\"content\":[\n")
	for written <= claudeResponseScanLimit+len(line) {
		_, _ = io.WriteString(w, line)
		written += len(line)
	}
	_, _ = io.WriteString(w, `"end"]}`)
}

func runClaudeOversizedJSON(t *testing.T, sourceFormat sdktranslator.Format) claudeStreamRecorder {
	t.Helper()
	return runClaudeStreamNonSSEResponse(t, sourceFormat, writeClaudeOversizedMultilineJSON)
}

func assertClaudeOversizedJSONFails(t *testing.T, rec claudeStreamRecorder, terminal string) {
	t.Helper()
	if len(rec.errs) != 1 {
		t.Fatalf("error chunk count = %d, want exactly 1 for oversized JSON", len(rec.errs))
	}
	status, ok := rec.errs[0].(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("oversized JSON error = %v, want HTTP 502", rec.errs[0])
	}
	for _, payload := range rec.payloads {
		if terminal != "" && strings.Contains(payload, terminal) {
			t.Fatalf("successful terminal emitted after oversized JSON: %q", payload)
		}
	}
}

func TestClaudeExecutor_ExecuteStreamOversizedMultilineJSONYieldsSingleError(t *testing.T) {
	assertClaudeOversizedJSONFails(
		t,
		runClaudeOversizedJSON(t, sdktranslator.FromString("claude")),
		`"message_stop"`,
	)
}

func TestClaudeExecutor_ExecuteStreamTranslatedOversizedMultilineJSONYieldsSingleError(t *testing.T) {
	assertClaudeOversizedJSONFails(
		t,
		runClaudeOversizedJSON(t, sdktranslator.FormatOpenAI),
		"[DONE]",
	)
}

func TestClaudeExecutor_ExecuteStreamCompleteJSONCleanEOFSucceeds(t *testing.T) {
	for _, test := range []struct {
		name         string
		sourceFormat sdktranslator.Format
	}{
		{name: "direct", sourceFormat: sdktranslator.FromString("claude")},
		{name: "translated", sourceFormat: sdktranslator.FormatOpenAI},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := runClaudeStreamNonSSECleanEOF(t, test.sourceFormat, claudeNonSSEMessageBody)
			if len(rec.errs) != 0 {
				t.Fatalf("complete JSON produced %d error chunk(s): %v", len(rec.errs), rec.errs)
			}
		})
	}
}

func TestClaudeExecutor_ExecuteStreamNonSSEReadErrorYieldsSingleError(t *testing.T) {
	rec := runClaudeStreamNonSSEReadError(t, sdktranslator.FromString("claude"))
	if len(rec.errs) != 1 {
		t.Fatalf("error chunk count = %d, want exactly 1 (non-SSE read error must not close the channel silently)", len(rec.errs))
	}
	for _, p := range rec.payloads {
		if strings.Contains(p, `"message_stop"`) {
			t.Fatalf("synthetic message_stop emitted after non-SSE read error: %q", p)
		}
	}
}

func TestClaudeExecutor_ExecuteStreamTranslatedNonSSEReadErrorYieldsSingleError(t *testing.T) {
	rec := runClaudeStreamNonSSEReadError(t, sdktranslator.FormatOpenAI)
	for _, p := range rec.payloads {
		if strings.Contains(p, "[DONE]") {
			t.Fatalf("translated success terminal emitted after non-SSE read error: %q", p)
		}
	}
	if len(rec.errs) != 1 {
		t.Fatalf("error chunk count = %d, want exactly 1 (non-SSE read error must not close the channel silently)", len(rec.errs))
	}
}

func TestClaudeExecutor_ExecuteStreamClientCancellationIsNotProviderFailure(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n" +
			`data: {"type":"message_start","message":{"id":"msg_cancel","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}` + "\n\n"))
		w.(http.Flusher).Flush()
		<-release
	}))
	defer server.Close()
	defer close(release)

	executor, auth := newClaudeStreamTerminalExecutor(server)
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-3-5-sonnet-20241022",
		Payload: payload,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("claude")})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	// Cancel only after the first payload so this exercises mid-stream cancellation.
	first, ok := <-result.Chunks
	if !ok || first.Err != nil {
		t.Fatalf("first chunk = (%q, %v), want message_start payload", first.Payload, first.Err)
	}

	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for chunk := range result.Chunks {
			if chunk.Err == nil {
				continue
			}
			var statusErr interface{ StatusCode() int }
			if as, ok := chunk.Err.(interface{ StatusCode() int }); ok {
				statusErr = as
			}
			if statusErr != nil {
				t.Errorf("client cancellation surfaced as provider failure with HTTP status: %v", chunk.Err)
			}
			if !strings.Contains(chunk.Err.Error(), "context canceled") {
				t.Errorf("client cancellation surfaced as non-cancellation error: %v", chunk.Err)
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out draining stream after client cancellation")
	}
}
