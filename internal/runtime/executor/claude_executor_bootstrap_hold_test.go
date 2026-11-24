package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func setClaudeBootstrapHoldTimeBoundForTest(bound time.Duration) func() {
	previous := claudeBootstrapHoldTimeBound.Swap(int64(bound))
	return func() { claudeBootstrapHoldTimeBound.Store(previous) }
}

func claudeHoldTestEvent(name, data string) string {
	return "event: " + name + "\ndata: " + data + "\n\n"
}

var (
	claudeHoldTestMessageStart = claudeHoldTestEvent("message_start", `{"type":"message_start","message":{"id":"msg_hold_1","type":"message","role":"assistant","model":"claude-opus-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}}`)
	claudeHoldTestPing         = claudeHoldTestEvent("ping", `{"type": "ping"}`)
	claudeHoldTestRateLimit    = claudeHoldTestEvent("error", `{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`)
	claudeHoldTestOverloaded   = claudeHoldTestEvent("error", `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	claudeHoldTestTextStart    = claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
	claudeHoldTestTextDelta    = claudeHoldTestEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
	claudeHoldTestTextDelta2   = claudeHoldTestEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`)
	claudeHoldTestBlockStop    = claudeHoldTestEvent("content_block_stop", `{"type":"content_block_stop","index":0}`)
	claudeHoldTestMessageEnd   = claudeHoldTestEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`) +
		claudeHoldTestEvent("message_stop", `{"type":"message_stop"}`)
)

func claudeHoldTestTextEvents() []string {
	return []string{claudeHoldTestMessageStart, claudeHoldTestPing, claudeHoldTestTextStart, claudeHoldTestTextDelta, claudeHoldTestTextDelta2, claudeHoldTestBlockStop, claudeHoldTestMessageEnd}
}

func claudeHoldTestToolUseEvents() []string {
	return []string{
		claudeHoldTestMessageStart,
		claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_hold_1","name":"get_weather","input":{}}}`),
		claudeHoldTestEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`),
		claudeHoldTestEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Paris\"}"}}`),
		claudeHoldTestBlockStop,
		claudeHoldTestEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":9}}`),
		claudeHoldTestEvent("message_stop", `{"type":"message_stop"}`),
	}
}

func claudeHoldTestThinkingEvents() []string {
	return []string{
		claudeHoldTestMessageStart,
		claudeHoldTestPing,
		claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`),
		claudeHoldTestEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think."}}`),
		claudeHoldTestEvent("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"c2lnbmF0dXJl"}}`),
		claudeHoldTestBlockStop,
		claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`),
		claudeHoldTestEvent("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}`),
		claudeHoldTestEvent("content_block_stop", `{"type":"content_block_stop","index":1}`),
		claudeHoldTestMessageEnd,
	}
}

// claudeHoldTestServer writes each event in its own flushed write, the way the
// upstream delivers them.
func claudeHoldTestServer(t *testing.T, header http.Header, events []string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for name, values := range header {
			w.Header()[name] = values
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, event := range events {
			_, _ = io.WriteString(w, event)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func claudeHoldTestExecutor(hold bool) *ClaudeExecutor {
	return NewClaudeExecutor(&config.Config{Claude: config.ClaudeConfig{StreamBootstrapHold: &hold}})
}

func claudeHoldTestAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-bootstrap-hold", "base_url": serverURL}}
}

func claudeHoldTestStream(t *testing.T, exec *ClaudeExecutor, serverURL string, format sdktranslator.Format) *cliproxyexecutor.StreamResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	result, err := exec.ExecuteStream(ctx, claudeHoldTestAuth(serverURL), cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: format, Stream: true})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	return result
}

type claudeHoldTestOutput struct {
	payloads []string
	err      error
}

func claudeHoldTestDrain(result *cliproxyexecutor.StreamResult) claudeHoldTestOutput {
	var output claudeHoldTestOutput
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			output.err = chunk.Err
			continue
		}
		output.payloads = append(output.payloads, string(chunk.Payload))
	}
	return output
}

var claudeHoldTestCreated = regexp.MustCompile(`"created":\d+`)

// claudeHoldTestComparable removes the translator's wall-clock created stamp, the
// only field that differs between two otherwise identical runs.
func claudeHoldTestComparable(payloads []string) []string {
	normalized := make([]string, len(payloads))
	for i, payload := range payloads {
		normalized[i] = claudeHoldTestCreated.ReplaceAllString(payload, `"created":0`)
	}
	return normalized
}

func claudeHoldTestErrorSummary(err error) string {
	if err == nil {
		return "<nil>"
	}
	status := 0
	var coder interface{ StatusCode() int }
	if errors.As(err, &coder) {
		status = coder.StatusCode()
	}
	return fmt.Sprintf("%d %s", status, err.Error())
}

func claudeHoldTestFormats() []sdktranslator.Format {
	return []sdktranslator.Format{sdktranslator.FormatClaude, sdktranslator.FormatOpenAI}
}

// A 200 stream that is the account's rate limit becomes one 429 error chunk
// carrying the retry time of the 200's headers, and nothing else.
func TestClaudeBootstrapHoldRateLimitFailsWithoutPayload(t *testing.T) {
	header := http.Header{
		"Retry-After":                           {"120"},
		"Anthropic-Ratelimit-Unified-Status":    {"rejected"},
		"Anthropic-Ratelimit-Unified-5h-Status": {"rejected"},
	}
	for _, format := range claudeHoldTestFormats() {
		t.Run(format.String(), func(t *testing.T) {
			pluginName := "test-claude-bootstrap-hold-" + format.String()
			plugin := &captureClaudeUsagePlugin{records: make(chan usage.Record, 4)}
			usage.RegisterNamedPlugin(pluginName, plugin)
			t.Cleanup(func() { usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{}) })

			server := claudeHoldTestServer(t, header, []string{claudeHoldTestMessageStart, claudeHoldTestPing, claudeHoldTestRateLimit})
			var chunks []cliproxyexecutor.StreamChunk
			for chunk := range claudeHoldTestStream(t, claudeHoldTestExecutor(true), server.URL, format).Chunks {
				chunks = append(chunks, chunk)
			}
			if len(chunks) != 1 || len(chunks[0].Payload) != 0 || chunks[0].Err == nil {
				t.Fatalf("chunks = %#v, want exactly one error chunk and no payload", chunks)
			}
			errLimit := chunks[0].Err
			var coder interface{ StatusCode() int }
			if !errors.As(errLimit, &coder) || coder.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("error = %T %v, want 429", errLimit, errLimit)
			}
			var retry interface{ RetryAfter() *time.Duration }
			if !errors.As(errLimit, &retry) || retry.RetryAfter() == nil {
				t.Fatalf("error = %T %v, want a retry time from the response headers", errLimit, errLimit)
			}
			// Retry-After 120s plus the parser's 1-30s fuzz.
			if got := *retry.RetryAfter(); got < 119*time.Second || got > 151*time.Second {
				t.Fatalf("RetryAfter = %v, want 120s plus fuzz", got)
			}
			if !strings.Contains(errLimit.Error(), "rate_limit_error") {
				t.Fatalf("error = %v, want the upstream error payload", errLimit)
			}
			select {
			case record := <-plugin.records:
				if !record.Failed {
					t.Fatal("usage record not marked failed")
				}
				if record.Fail.StatusCode != http.StatusTooManyRequests {
					t.Fatalf("usage failure status = %d, want 429", record.Fail.StatusCode)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for failed usage record")
			}
		})
	}

	t.Run("hold off forwards in-stream as before", func(t *testing.T) {
		server := claudeHoldTestServer(t, header, []string{claudeHoldTestMessageStart, claudeHoldTestPing, claudeHoldTestRateLimit})
		output := claudeHoldTestDrain(claudeHoldTestStream(t, claudeHoldTestExecutor(false), server.URL, sdktranslator.FormatClaude))
		if len(output.payloads) == 0 || output.payloads[0] != claudeHoldTestMessageStart || output.err == nil {
			t.Fatalf("hold off output = %#v, want message_start forwarded then an in-stream error", output)
		}
	})
}

// Nothing generates within the time bound, so every held frame goes out at
// the bound while the upstream is silent. The rest of the stream, a
// rate_limit_error after release included, is what it is without the hold.
func TestClaudeBootstrapHoldReleasesAtTimeBound(t *testing.T) {
	const bound = 150 * time.Millisecond
	t.Cleanup(setClaudeBootstrapHoldTimeBoundForTest(bound))
	tail := []string{claudeHoldTestTextStart, claudeHoldTestTextDelta, claudeHoldTestRateLimit}

	run := func(t *testing.T, hold bool, format sdktranslator.Format) (time.Duration, claudeHoldTestOutput) {
		proceed := make(chan struct{})
		var once sync.Once
		release := func() { once.Do(func() { close(proceed) }) }
		t.Cleanup(release)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, claudeHoldTestMessageStart)
			flusher.Flush()
			select {
			case <-proceed:
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Second):
				return
			}
			for _, event := range tail {
				_, _ = io.WriteString(w, event)
				flusher.Flush()
			}
		}))
		t.Cleanup(server.Close)

		start := time.Now()
		result := claudeHoldTestStream(t, claudeHoldTestExecutor(hold), server.URL, format)
		var first cliproxyexecutor.StreamChunk
		select {
		case first = <-result.Chunks:
		case <-time.After(5 * time.Second):
			t.Fatal("no chunk while the upstream is silent: the time bound did not fire")
		}
		elapsed := time.Since(start)
		// The upstream sends nothing more until the first chunk is seen.
		release()
		output := claudeHoldTestDrain(result)
		if first.Err != nil {
			t.Fatalf("first chunk error = %v", first.Err)
		}
		output.payloads = append([]string{string(first.Payload)}, output.payloads...)
		return elapsed, output
	}

	for _, format := range claudeHoldTestFormats() {
		t.Run(format.String(), func(t *testing.T) {
			elapsedOn, on := run(t, true, format)
			if elapsedOn < bound {
				t.Fatalf("first chunk after %v, want the hold to keep it for the %v bound", elapsedOn, bound)
			}
			_, off := run(t, false, format)
			if got, want := claudeHoldTestComparable(on.payloads), claudeHoldTestComparable(off.payloads); strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("payloads with hold differ from without:\n on=%q\noff=%q", got, want)
			}
			if format == sdktranslator.FormatClaude {
				want := append([]string{claudeHoldTestMessageStart}, tail...)
				if strings.Join(on.payloads, "") != strings.Join(want, "") {
					t.Fatalf("payloads = %q, want every upstream event once, in order", on.payloads)
				}
			}
			if claudeHoldTestErrorSummary(on.err) != claudeHoldTestErrorSummary(off.err) {
				t.Fatalf("stream error with hold = %s, without = %s", claudeHoldTestErrorSummary(on.err), claudeHoldTestErrorSummary(off.err))
			}
			var coder interface{ StatusCode() int }
			if on.err == nil || (errors.As(on.err, &coder) && coder.StatusCode() == http.StatusTooManyRequests) {
				t.Fatalf("stream error = %v, want the in-stream error after release, not a failover 429", on.err)
			}
		})
	}
}

// What the hold costs the caller's first byte when generation starts 800ms
// after message_start.
func TestClaudeBootstrapHoldFirstByteCost(t *testing.T) {
	const gap = 800 * time.Millisecond
	measure := func(hold bool) time.Duration {
		sent := make(chan time.Time, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			sent <- time.Now()
			_, _ = io.WriteString(w, claudeHoldTestMessageStart)
			flusher.Flush()
			time.Sleep(gap)
			for _, event := range []string{claudeHoldTestTextStart, claudeHoldTestTextDelta, claudeHoldTestBlockStop, claudeHoldTestMessageEnd} {
				_, _ = io.WriteString(w, event)
				flusher.Flush()
			}
		}))
		defer server.Close()
		result := claudeHoldTestStream(t, claudeHoldTestExecutor(hold), server.URL, sdktranslator.FormatClaude)
		first := <-result.Chunks
		firstAt := time.Now()
		if first.Err != nil || len(first.Payload) == 0 {
			t.Fatalf("first chunk = %#v", first)
		}
		claudeHoldTestDrain(result)
		return firstAt.Sub(<-sent)
	}
	off := measure(false)
	on := measure(true)
	t.Logf("first chunk after message_start, first content_block_delta %v later: hold off %v, hold on %v", gap, off, on)
	if on < gap-50*time.Millisecond || on <= off {
		t.Fatalf("hold on %v, hold off %v: want the hold to wait for the first generating event", on, off)
	}
}

// With no limit, the hold changes nothing the caller receives.
func TestClaudeBootstrapHoldByteIdentity(t *testing.T) {
	cases := []struct {
		name   string
		events []string
	}{
		{name: "text", events: claudeHoldTestTextEvents()},
		{name: "tool_use first", events: claudeHoldTestToolUseEvents()},
		{name: "extended thinking", events: claudeHoldTestThinkingEvents()},
		// Not a limit: released, then forwarded and failed in-stream, as without the hold.
		{name: "overloaded while holding", events: []string{claudeHoldTestMessageStart, claudeHoldTestPing, claudeHoldTestOverloaded}},
		{name: "eof while holding", events: []string{claudeHoldTestMessageStart, claudeHoldTestPing}},
	}
	for _, tc := range cases {
		for _, format := range claudeHoldTestFormats() {
			t.Run(tc.name+"/"+format.String(), func(t *testing.T) {
				server := claudeHoldTestServer(t, nil, tc.events)
				on := claudeHoldTestDrain(claudeHoldTestStream(t, claudeHoldTestExecutor(true), server.URL, format))
				off := claudeHoldTestDrain(claudeHoldTestStream(t, claudeHoldTestExecutor(false), server.URL, format))
				if len(on.payloads) == 0 {
					t.Fatal("no payload")
				}
				got, want := claudeHoldTestComparable(on.payloads), claudeHoldTestComparable(off.payloads)
				if len(got) != len(want) {
					t.Fatalf("chunk count with hold %d, without %d:\n on=%q\noff=%q", len(got), len(want), got, want)
				}
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("chunk %d differs:\n on=%q\noff=%q", i, got[i], want[i])
					}
				}
				if format == sdktranslator.FormatClaude && strings.Join(on.payloads, "") != strings.Join(tc.events, "") {
					t.Fatalf("passthrough bytes = %q, want the upstream bytes", strings.Join(on.payloads, ""))
				}
				if claudeHoldTestErrorSummary(on.err) != claudeHoldTestErrorSummary(off.err) {
					t.Fatalf("stream error with hold = %s, without = %s", claudeHoldTestErrorSummary(on.err), claudeHoldTestErrorSummary(off.err))
				}
			})
		}
	}

	// The hold is stream-only: a non-streamed call reads the same upstream the
	// same way with it on or off.
	t.Run("non-stream", func(t *testing.T) {
		message := `{"id":"msg_hold_json","type":"message","role":"assistant","model":"claude-opus-5","content":[{"type":"text","text":"Hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":3,"output_tokens":1}}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if bytes.Contains(body, []byte(`"stream":true`)) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, strings.Join(claudeHoldTestTextEvents(), ""))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, message)
		}))
		defer server.Close()
		for _, format := range claudeHoldTestFormats() {
			execute := func(hold bool) string {
				resp, err := claudeHoldTestExecutor(hold).Execute(context.Background(), claudeHoldTestAuth(server.URL), cliproxyexecutor.Request{
					Model:   "claude-opus-5",
					Payload: []byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
				}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: format})
				if err != nil {
					t.Fatalf("%s Execute(hold=%v) error = %v", format, hold, err)
				}
				return claudeHoldTestComparable([]string{string(resp.Payload)})[0]
			}
			if on, off := execute(true), execute(false); on != off || on == "" {
				t.Fatalf("%s non-stream response with hold = %q, without = %q", format, on, off)
			}
		}
	})
}

func TestClaudeBootstrapHoldClassification(t *testing.T) {
	cases := []struct {
		name  string
		event string
		want  claudeBootstrapEventKind
	}{
		{name: "message_start", event: claudeHoldTestMessageStart, want: claudeBootstrapEventHoldable},
		{name: "ping", event: claudeHoldTestPing, want: claudeBootstrapEventHoldable},
		{name: "comment", event: ": keepalive\n\n", want: claudeBootstrapEventHoldable},
		{name: "blank", event: "\n", want: claudeBootstrapEventHoldable},
		{name: "empty text start", event: claudeHoldTestTextStart, want: claudeBootstrapEventHoldable},
		{name: "empty thinking start", event: claudeHoldTestThinkingEvents()[2], want: claudeBootstrapEventHoldable},
		{name: "signed thinking start", event: claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":"c2ln"}}`), want: claudeBootstrapEventGeneration},
		{name: "text with content", event: claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"x"}}`), want: claudeBootstrapEventGeneration},
		{name: "tool_use start", event: claudeHoldTestToolUseEvents()[1], want: claudeBootstrapEventGeneration},
		{name: "server_tool_use start", event: claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`), want: claudeBootstrapEventGeneration},
		{name: "web_search_tool_result start", event: claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[]}}`), want: claudeBootstrapEventGeneration},
		{name: "redacted_thinking start", event: claudeHoldTestEvent("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"abc"}}`), want: claudeBootstrapEventGeneration},
		{name: "text delta", event: claudeHoldTestTextDelta, want: claudeBootstrapEventGeneration},
		{name: "message_delta", event: claudeHoldTestEvent("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"}}`), want: claudeBootstrapEventGeneration},
		{name: "message_stop", event: claudeHoldTestEvent("message_stop", `{"type":"message_stop"}`), want: claudeBootstrapEventGeneration},
		{name: "unknown event", event: claudeHoldTestEvent("future_event", `{"type":"future_event"}`), want: claudeBootstrapEventGeneration},
		{name: "rate_limit_error", event: claudeHoldTestRateLimit, want: claudeBootstrapEventRateLimit},
		{name: "overloaded_error", event: claudeHoldTestOverloaded, want: claudeBootstrapEventGeneration},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, payload := classifyClaudeBootstrapEvent([]byte(tc.event))
			if got != tc.want {
				t.Fatalf("classifyClaudeBootstrapEvent(%q) = %d, want %d", tc.event, got, tc.want)
			}
			if got == claudeBootstrapEventRateLimit && !bytes.Contains(payload, []byte(`"rate_limit_error"`)) {
				t.Fatalf("rate limit payload = %q", payload)
			}
		})
	}
}

// A real 200 carries no Retry-After and an allowed unified status, so the limit
// inside it has no retry time; it still fails over as the account's 429. A
// limit that is the last event, without its blank line, is caught the same way
// on both paths, and a non-streamed translated call that buffers the stream
// answers the account's 429 instead of a 502.
func TestClaudeBootstrapHoldRealisticLimits(t *testing.T) {
	realistic := http.Header{"Anthropic-Ratelimit-Unified-Status": {"allowed"}}
	lastWithoutBlank := strings.TrimSuffix(claudeHoldTestRateLimit, "\n")
	cases := []struct {
		name   string
		events []string
	}{
		{name: "realistic headers", events: []string{claudeHoldTestMessageStart, claudeHoldTestPing, claudeHoldTestRateLimit}},
		{name: "last event without blank line", events: []string{claudeHoldTestMessageStart, claudeHoldTestPing, lastWithoutBlank}},
	}
	for _, tc := range cases {
		for _, format := range claudeHoldTestFormats() {
			t.Run(tc.name+"/"+format.String(), func(t *testing.T) {
				server := claudeHoldTestServer(t, realistic, tc.events)
				var chunks []cliproxyexecutor.StreamChunk
				for chunk := range claudeHoldTestStream(t, claudeHoldTestExecutor(true), server.URL, format).Chunks {
					chunks = append(chunks, chunk)
				}
				if len(chunks) != 1 || len(chunks[0].Payload) != 0 || chunks[0].Err == nil {
					t.Fatalf("chunks = %#v, want exactly one error chunk and no payload", chunks)
				}
				var coder interface{ StatusCode() int }
				if !errors.As(chunks[0].Err, &coder) || coder.StatusCode() != http.StatusTooManyRequests {
					t.Fatalf("error = %T %v, want 429", chunks[0].Err, chunks[0].Err)
				}
			})
		}
	}

	t.Run("non-stream translated", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, claudeHoldTestMessageStart+claudeHoldTestPing+claudeHoldTestRateLimit)
		}))
		defer server.Close()
		_, err := claudeHoldTestExecutor(true).Execute(context.Background(), claudeHoldTestAuth(server.URL), cliproxyexecutor.Request{
			Model:   "claude-opus-5",
			Payload: []byte(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
		}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatOpenAI})
		var coder interface{ StatusCode() int }
		if err == nil || !errors.As(err, &coder) || coder.StatusCode() != http.StatusTooManyRequests {
			t.Fatalf("Execute() error = %T %v, want the account's 429", err, err)
		}
	})
}
