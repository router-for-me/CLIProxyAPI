package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// codexDiagnosticsFrameGap is the scripted pause before each upstream frame and
	// codexDiagnosticsFinalGap the pause before the response ends. They differ so an end-to-end
	// case distinguishes the silence before the stream ended from the whole attempt: an upstream
	// that sends n frames runs for n*codexDiagnosticsFrameGap + codexDiagnosticsFinalGap, and only
	// the latter may appear as "silent for".
	codexDiagnosticsFrameGap = time.Second
	codexDiagnosticsFinalGap = 5 * time.Second
	// codexDiagnosticsClockWait bounds how long the scripted upstream waits for the executor to
	// read the clock before it gives up, so a broken expectation fails instead of hanging. A
	// healthy run never waits: the reads it waits for have usually already happened.
	codexDiagnosticsClockWait = 5 * time.Second
	// codexDiagnosticsReadsPerEvent is how many clock reads one upstream event costs the executor:
	// event:, data: and the blank separator are three lines and every line read is timestamped.
	// It is a deliberate tripwire - an executor that stops timestamping every line starves the
	// barrier below instead of quietly making the assertions meaningless.
	codexDiagnosticsReadsPerEvent = 3
)

// codexScriptedStreamClock is a manual clock for the stream idle hook. It never moves on its own:
// the scripted upstream advances it between frames, so every interval an end-to-end case asserts
// is one the test wrote rather than one the clock's shape guarantees.
type codexScriptedStreamClock struct {
	mu  sync.Mutex
	cur time.Time
	// reads receives one value per clock read, letting the upstream wait for the executor to
	// timestamp the lines it has written before it advances again.
	reads chan struct{}
	// seen counts the reads already consumed from reads. Only the scripted upstream touches it.
	seen int
}

func newCodexScriptedStreamClock(t *testing.T) *codexScriptedStreamClock {
	t.Helper()
	clock := &codexScriptedStreamClock{
		cur:   time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC),
		reads: make(chan struct{}, 1024),
	}
	t.Cleanup(setCodexStreamNowForTest(clock.now))
	return clock
}

func (c *codexScriptedStreamClock) now() time.Time {
	c.mu.Lock()
	cur := c.cur
	c.mu.Unlock()
	select {
	case c.reads <- struct{}{}:
	default:
	}
	return cur
}

func (c *codexScriptedStreamClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur = c.cur.Add(d)
}

// waitForReads blocks until the clock has been read at least total times, so the caller knows
// every line it has written is already timestamped and the next advance lands after those reads
// instead of racing them.
func (c *codexScriptedStreamClock) waitForReads(t *testing.T, total int) bool {
	t.Helper()
	for c.seen < total {
		select {
		case <-c.reads:
			c.seen++
		case <-time.After(codexDiagnosticsClockWait):
			t.Errorf("timed out after %d of %d clock reads: the executor must timestamp every line it reads", c.seen, total)
			return false
		}
	}
	return true
}

// codexScriptedSSEEventName mirrors the event name codexSSEServer derives from a payload, so the
// scripted upstream writes the same three-line shape: event:, data: and the blank separator.
func codexScriptedSSEEventName(event string) string {
	if parsed := strings.SplitN(event, `"type":"`, 2); len(parsed) == 2 {
		return strings.SplitN(parsed[1], `"`, 2)[0]
	}
	return "message"
}

// codexScriptedSSEServer streams the supplied events with the clock advanced explicitly around
// them: codexDiagnosticsFrameGap before each event, codexDiagnosticsFinalGap before the response
// ends. Each step waits for the executor to read the clock for the lines already written, so the
// executor measures the scripted gaps and not whatever the goroutines happened to interleave.
func codexScriptedSSEServer(t *testing.T, clock *codexScriptedStreamClock, events ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("scripted upstream needs a flushing ResponseWriter")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
		// The executor reads the clock once when the response headers arrive, to seed the interval
		// for an upstream that then sends nothing at all.
		reads := 1
		if !clock.waitForReads(t, reads) {
			return
		}
		for _, event := range events {
			clock.advance(codexDiagnosticsFrameGap)
			_, _ = w.Write([]byte("event: " + codexScriptedSSEEventName(event) + "\n"))
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
			flusher.Flush()
			reads += codexDiagnosticsReadsPerEvent
			if !clock.waitForReads(t, reads) {
				return
			}
		}
		clock.advance(codexDiagnosticsFinalGap)
	}))
}

// codexIncompleteStreamWant renders the message an attempt must produce: whatever it read, the
// silence it reports is the final gap and never the whole attempt.
func codexIncompleteStreamWant(lastEvent string, frames int) string {
	return fmt.Sprintf("%s (last event: %s, data frames: %d, silent for %s)",
		codexIncompleteStreamMessage, lastEvent, frames, codexDiagnosticsFinalGap)
}

// codexIncompleteStreamAttempt drives one ExecuteStream against a scripted upstream and returns
// the incomplete-stream error, whether it surfaced from the call or from the stream, together with
// whether the stream was released downstream.
func codexIncompleteStreamAttempt(t *testing.T, buffering bool, headers http.Header, events ...string) (bool, error) {
	t.Helper()
	clock := newCodexScriptedStreamClock(t)
	server := codexScriptedSSEServer(t, clock, events...)
	defer server.Close()

	req, opts := codexTestRequest()
	opts.Headers = headers
	result, err := NewCodexExecutor(codexBufferingConfig(buffering)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		if result != nil {
			t.Fatal("expected no stream result alongside an ExecuteStream error")
		}
		return false, err
	}
	if result == nil {
		t.Fatal("expected either an error or a stream result")
	}
	_, streamErr := drainChunks(result)
	return true, streamErr
}

func TestCodexIncompleteStreamDiagnosticsMessage(t *testing.T) {
	cases := []struct {
		name string
		diag codexIncompleteStreamDiagnostics
		want string
	}{
		{
			name: "no event or frame",
			diag: codexIncompleteStreamDiagnostics{}.withIdle(31 * time.Second),
			want: codexIncompleteStreamMessage + " (last event: none, data frames: 0, silent for 31s)",
		},
		{
			name: "untyped frames",
			diag: codexIncompleteStreamDiagnostics{dataFrames: 3}.withIdle(30 * time.Second),
			want: codexIncompleteStreamMessage + " (last event: none, data frames: 3, silent for 30s)",
		},
		{
			name: "blank event type",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "   ", dataFrames: 1}.withIdle(time.Second),
			want: codexIncompleteStreamMessage + " (last event: none, data frames: 1, silent for 1s)",
		},
		{
			name: "stopped after a delta",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "response.output_text.delta", dataFrames: 12}.withIdle(30040 * time.Millisecond),
			want: codexIncompleteStreamMessage + " (last event: response.output_text.delta, data frames: 12, silent for 30s)",
		},
		{
			name: "rounded up to a second",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "response.in_progress", dataFrames: 2}.withIdle(30600 * time.Millisecond),
			want: codexIncompleteStreamMessage + " (last event: response.in_progress, data frames: 2, silent for 31s)",
		},
		{
			name: "sub-second interval",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "response.output_text.delta", dataFrames: 12}.withIdle(400 * time.Millisecond),
			want: codexIncompleteStreamMessage + " (last event: response.output_text.delta, data frames: 12, silent for <1s)",
		},
		{
			name: "second boundary",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "response.output_text.delta", dataFrames: 12}.withIdle(1000 * time.Millisecond),
			want: codexIncompleteStreamMessage + " (last event: response.output_text.delta, data frames: 12, silent for 1s)",
		},
		{
			name: "negative interval",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "keepalive", dataFrames: 5}.withIdle(-time.Second),
			want: codexIncompleteStreamMessage + " (last event: keepalive, data frames: 5, silent for <1s)",
		},
		{
			// A JSON string escape ("a\nb") or a bare \r mid-line puts control characters in the type.
			name: "control characters dropped",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "{\n  \"status\": \"failed\"\r}", dataFrames: 4}.withIdle(2 * time.Second),
			want: codexIncompleteStreamMessage + " (last event: { \"status\": \"failed\"}, data frames: 4, silent for 2s)",
		},
		{
			// Bidi overrides and Unicode line separators are not control characters but must not reach a log line.
			name: "unprintable characters dropped",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "response.\u202ecreated\u2028", dataFrames: 2}.withIdle(2 * time.Second),
			want: codexIncompleteStreamMessage + " (last event: response.created, data frames: 2, silent for 2s)",
		},
		{
			name: "whitespace runs collapsed",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "keepalive" + strings.Repeat(" ", 200) + "x", dataFrames: 1}.withIdle(time.Second),
			want: codexIncompleteStreamMessage + " (last event: keepalive x, data frames: 1, silent for 1s)",
		},
		{
			name: "over-long event type cut with a marker",
			diag: codexIncompleteStreamDiagnostics{lastEventType: strings.Repeat("a", codexIncompleteStreamEventTypeRuneLimit+50), dataFrames: 7}.withIdle(3 * time.Second),
			want: codexIncompleteStreamMessage + " (last event: " + strings.Repeat("a", codexIncompleteStreamEventTypeRuneLimit) + "..., data frames: 7, silent for 3s)",
		},
		{
			// The limit is in runes, so a multi-byte type is cut between characters, never inside one.
			name: "over-long multi-byte event type cut between runes",
			diag: codexIncompleteStreamDiagnostics{lastEventType: strings.Repeat("日", codexIncompleteStreamEventTypeRuneLimit+2), dataFrames: 2}.withIdle(time.Second),
			want: codexIncompleteStreamMessage + " (last event: " + strings.Repeat("日", codexIncompleteStreamEventTypeRuneLimit) + "..., data frames: 2, silent for 1s)",
		},
		{
			name: "non-stream omits interval",
			diag: codexIncompleteStreamDiagnostics{lastEventType: "response.created", dataFrames: 1},
			want: codexIncompleteStreamMessage + " (last event: response.created, data frames: 1)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.diag.message()
			if got != tc.want {
				t.Fatalf("message() = %q, want %q", got, tc.want)
			}
			// Guards the table itself: no future row may expect a message that spans log records.
			if strings.ContainsAny(got, "\r\n") {
				t.Fatalf("message() must stay on one line, got %q", got)
			}
			// The constant is a prefix downstream matches on, so it must survive verbatim.
			if !strings.HasPrefix(got, codexIncompleteStreamMessage) {
				t.Fatalf("message() = %q, want prefix %q", got, codexIncompleteStreamMessage)
			}
			if !strings.Contains(got, "stream closed before response.completed") {
				t.Fatalf("message() = %q, want it to keep the downstream match substring", got)
			}
		})
	}
}

// Every data: frame counts, but only one that carries a type may replace the reported event, so a
// run of untyped frames after a delta cannot erase what the upstream last announced.
func TestCodexIncompleteStreamDiagnosticsObserveDataFrame(t *testing.T) {
	var diag codexIncompleteStreamDiagnostics
	diag.observeDataFrame("response.created")
	diag.observeDataFrame("")
	diag.observeDataFrame("response.output_text.delta")
	diag.observeDataFrame("")
	if diag.dataFrames != 4 {
		t.Fatalf("dataFrames = %d, want 4: a frame with no type still counts", diag.dataFrames)
	}
	if diag.lastEventType != "response.output_text.delta" {
		t.Fatalf("lastEventType = %q, want %q: a frame with no type must not overwrite it", diag.lastEventType, "response.output_text.delta")
	}

	// A whitespace-only type is not blank here, so it is remembered and does replace the previous
	// value; message() is what turns it back into "none", by trimming before it renders.
	diag.observeDataFrame("   ")
	if diag.dataFrames != 5 {
		t.Fatalf("dataFrames = %d, want 5", diag.dataFrames)
	}
	if diag.lastEventType != "   " {
		t.Fatalf("lastEventType = %q, want the whitespace-only type", diag.lastEventType)
	}
	want := codexIncompleteStreamMessage + " (last event: none, data frames: 5, silent for 5s)"
	if got := diag.withIdle(5 * time.Second).message(); got != want {
		t.Fatalf("message() = %q, want %q", got, want)
	}
}

// The interval must be the silence before the stream ended, not how long the attempt ran. The
// upstream here sends three frames a second apart and then goes quiet for five, so an executor
// that failed to refresh the interval as it read would report the eight-second total instead.
func TestCodexExecutorExecuteStreamIncompleteStreamIdleIsTheFinalSilence(t *testing.T) {
	frames := []string{codexInProgressEvent, codexInProgressEvent, codexInProgressEvent}
	released, streamErr := codexIncompleteStreamAttempt(t, false, nil, frames...)
	if !released {
		t.Fatal("the unbuffered path must release the stream")
	}
	if streamErr == nil {
		t.Fatal("expected an incomplete-stream error, got nil")
	}

	want := codexIncompleteStreamWant("response.in_progress", len(frames))
	whole := time.Duration(len(frames))*codexDiagnosticsFrameGap + codexDiagnosticsFinalGap
	if got := streamErr.Error(); got != want {
		t.Fatalf("silent for must be the final gap (%s), not the whole run (%s): got %q, want %q", codexDiagnosticsFinalGap, whole, got, want)
	}
}

// An upstream that stops after a content delta must report the event it stopped after, its frame
// count and its silence, so it reads differently from one that never produced anything.
func TestCodexExecutorExecuteStreamIncompleteStreamReportsDiagnostics(t *testing.T) {
	released, streamErr := codexIncompleteStreamAttempt(t, false, nil, codexCreatedEvent, codexOutputDeltaEvent)
	if !released {
		t.Fatal("the unbuffered path must release the stream")
	}
	if streamErr == nil {
		t.Fatal("expected an incomplete-stream error, got nil")
	}

	want := codexIncompleteStreamWant("response.output_text.delta", 2)
	if got := streamErr.Error(); got != want {
		t.Fatalf("stream error = %q, want %q", got, want)
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, streamErr)
	}
	assertRequestScopedTestError(t, streamErr)
}

// The same diagnostics must be produced when the stream never leaves the bootstrap buffer, because
// every frame it saw was one the buffer may hold.
func TestCodexExecutorExecuteStreamIncompleteStreamReportsDiagnosticsWhileBuffering(t *testing.T) {
	released, err := codexIncompleteStreamAttempt(t, true, nil, codexCreatedEvent, codexInProgressEvent)
	if released {
		t.Fatal("expected the bootstrap buffer to hold every frame, so no stream result")
	}
	if err == nil {
		t.Fatal("expected an incomplete-stream error, got nil")
	}

	want := codexIncompleteStreamWant("response.in_progress", 2)
	if got := err.Error(); got != want {
		t.Fatalf("stream error = %q, want %q", got, want)
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, err)
	}
	assertRequestScopedTestError(t, err)
}

// The counters must survive the handover to the streaming goroutine: the delta frame releases the
// stream, the count must still cover the response.created frame the bootstrap loop consumed, and
// the goroutine must keep counting the frames that arrive after the release.
func TestCodexExecutorExecuteStreamIncompleteStreamCountsAcrossBootstrapHandover(t *testing.T) {
	// created and delta are both consumed by the buffering loop, the delta releasing the stream, so
	// only a third frame can show that the goroutine keeps counting and keeps updating the type.
	released, streamErr := codexIncompleteStreamAttempt(t, true, nil, codexCreatedEvent, codexOutputDeltaEvent, codexOutputAddedEvent)
	if !released {
		t.Fatal("expected the delta frame to release the stream")
	}
	if streamErr == nil {
		t.Fatal("expected an incomplete-stream error, got nil")
	}

	want := codexIncompleteStreamWant("response.output_item.added", 3)
	if got := streamErr.Error(); got != want {
		t.Fatalf("stream error = %q, want %q", got, want)
	}
}

// A frame whose JSON carries no type counts but leaves the reported event alone, so the message
// still names the last thing the upstream announced rather than going blank.
func TestCodexExecutorExecuteStreamIncompleteStreamUntypedFrameKeepsLastEvent(t *testing.T) {
	released, streamErr := codexIncompleteStreamAttempt(t, false, nil, codexOutputDeltaEvent, `{}`)
	if !released {
		t.Fatal("the unbuffered path must release the stream")
	}
	if streamErr == nil {
		t.Fatal("expected an incomplete-stream error, got nil")
	}

	want := codexIncompleteStreamWant("response.output_text.delta", 2)
	if got := streamErr.Error(); got != want {
		t.Fatalf("stream error = %q, want %q", got, want)
	}
}

// The diagnostics describe the upstream, so a downstream user agent that makes the executor rewrite
// keepalive frames into SSE comments must not change what the failure reports.
func TestCodexExecutorExecuteStreamIncompleteStreamDiagnosticsIgnoreDownstreamUserAgent(t *testing.T) {
	grokHeaders := http.Header{"User-Agent": []string{"grok-shell/1.0"}}
	events := []string{codexCreatedEvent, codexKeepaliveEvent, codexKeepaliveEvent}
	want := codexIncompleteStreamWant("keepalive", len(events))

	for _, buffering := range []bool{false, true} {
		t.Run(fmt.Sprintf("buffering=%v", buffering), func(t *testing.T) {
			clients := []struct {
				name    string
				headers http.Header
			}{
				{name: "default client", headers: nil},
				{name: "grok client", headers: grokHeaders},
			}
			for _, client := range clients {
				t.Run(client.name, func(t *testing.T) {
					_, err := codexIncompleteStreamAttempt(t, buffering, client.headers, events...)
					if err == nil {
						t.Fatal("expected an incomplete-stream error, got nil")
					}
					if got := err.Error(); got != want {
						t.Fatalf("stream error = %q, want %q", got, want)
					}
				})
			}
		})
	}
}

// An upstream that commits headers and then sends nothing at all reports no event and no frame,
// while still reporting how long it stayed silent.
func TestCodexExecutorExecuteStreamIncompleteStreamWithoutAnyFrame(t *testing.T) {
	released, streamErr := codexIncompleteStreamAttempt(t, false, nil)
	if !released {
		t.Fatal("the unbuffered path must release the stream")
	}
	if streamErr == nil {
		t.Fatal("expected an incomplete-stream error, got nil")
	}

	want := codexIncompleteStreamWant("none", 0)
	if got := streamErr.Error(); got != want {
		t.Fatalf("stream error = %q, want %q", got, want)
	}
}

// The non-stream path reads the upstream body to completion before parsing it, so it reports the
// last event and the frame count and omits the idle interval it cannot measure.
func TestCodexExecutorExecuteIncompleteStreamReportsDiagnostics(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexOutputDeltaEvent)
	defer server.Close()

	req, opts := codexTestRequest()
	opts.Stream = false
	_, err := NewCodexExecutor(codexBufferingConfig(false)).Execute(context.Background(), codexTestAuth(server.URL), req, opts)
	if err == nil {
		t.Fatal("expected an incomplete-stream error, got nil")
	}

	want := codexIncompleteStreamMessage + " (last event: response.output_text.delta, data frames: 2)"
	if got := err.Error(); got != want {
		t.Fatalf("execute error = %q, want %q", got, want)
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d; err=%v", got, http.StatusRequestTimeout, err)
	}
	assertRequestScopedTestError(t, err)
}
