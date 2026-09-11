package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

const (
	codexOverloadEvent      = `{"type":"error","error":{"type":"service_unavailable_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later.","param":null},"sequence_number":2}`
	codexCapacityEvent      = `{"type":"error","error":{"message":"Selected model is at capacity. Please try a different model."},"sequence_number":2}`
	codexInvalidEvent       = `{"type":"error","error":{"type":"invalid_request_error","code":"invalid_value","message":"Invalid input."},"sequence_number":2}`
	codexCreatedEvent       = `{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-terra"}}`
	codexInProgressEvent    = `{"type":"response.in_progress","response":{"id":"resp_1"}}`
	codexOutputAddedEvent   = `{"type":"response.output_item.added","item":{"id":"msg_1","type":"message","role":"assistant","content":[]},"output_index":0}`
	codexKeepaliveEvent     = `{"type":"keepalive","sequence_number":1}`
	codexOutputDeltaEvent   = `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`
	codexCompletedEventBody = `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`
)

func codexBufferingConfig(enabled bool) *config.Config {
	return &config.Config{Codex: config.CodexConfig{StreamBootstrapBuffering: enabled}}
}

func codexTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{Attributes: map[string]string{"base_url": baseURL, "api_key": "test"}}
}

func codexTestRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{
			Model:   "gpt-5.6-terra",
			Payload: []byte(`{"model":"gpt-5.6-terra","input":"hello"}`),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Stream:       true,
		}
}

// codexSSEServer streams the supplied event payloads as an HTTP 200 SSE response.
func codexSSEServer(events ...string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range events {
			eventType := "message"
			if parsed := strings.SplitN(event, `"type":"`, 2); len(parsed) == 2 {
				eventType = strings.SplitN(parsed[1], `"`, 2)[0]
			}
			_, _ = w.Write([]byte("event: " + eventType + "\n"))
			_, _ = w.Write([]byte("data: " + event + "\n\n"))
		}
	}))
}

// codexWebsocketServer echoes the supplied frames after receiving the client request frame.
func codexWebsocketServer(t *testing.T, frames ...string) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read websocket message: %v", errRead)
			return
		}
		for _, frame := range frames {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(frame))
		}
	}))
}

func codexWebsocketRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	return cliproxyexecutor.Request{
			Model:   "gpt-5.6-terra",
			Payload: []byte(`{"model":"gpt-5.6-terra","input":[{"type":"message","role":"user","content":"hello"}]}`),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
		}
}

// drainChunks collects every payload and the first error from a stream result.
func drainChunks(result *cliproxyexecutor.StreamResult) (string, error) {
	var payloads [][]byte
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			if streamErr == nil {
				streamErr = chunk.Err
			}
			continue
		}
		payloads = append(payloads, chunk.Payload)
	}
	return string(bytes.Join(payloads, []byte("\n"))), streamErr
}

// An overload rejection smuggled into an HTTP 200 stream must fail the whole attempt before any
// downstream chunk escapes, so the conductor can retry on another credential. A nil StreamResult
// is the invariant: with no channel there is no way for the buffered handshake to reach the client.
func TestCodexExecutor_BootstrapBuffering_OverloadFailsAttemptWithoutLeakingHandshake(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexOverloadEvent)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err == nil {
		t.Fatal("expected ExecuteStream to fail the attempt on an overload rejection")
	}
	if result != nil {
		t.Fatal("expected nil result so no buffered handshake chunk can reach the client")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d (upstream hides 503 behind HTTP 200)", got, http.StatusServiceUnavailable)
	}
}

func TestCodexExecutor_BootstrapBuffering_CapacityFailsAttemptWithoutLeakingHandshake(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexCapacityEvent)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err == nil {
		t.Fatal("expected ExecuteStream to fail the attempt on a capacity rejection")
	}
	if result != nil {
		t.Fatal("expected nil result so no buffered handshake chunk can reach the client")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
}

// A non-overload terminal failure must keep the original in-stream delivery semantics: the
// buffered handshake is flushed first and the error arrives as a stream chunk, so the conductor
// sees a committed stream and does not burn another credential on a request-level fault.
func TestCodexExecutor_BootstrapBuffering_NonOverloadStaysInStream(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexInvalidEvent)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err != nil {
		t.Fatalf("non-overload failure must not fail the attempt synchronously: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result for in-stream error delivery")
	}
	combined, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected the invalid-request failure to arrive as an in-stream chunk error")
	}
	if !strings.Contains(combined, `"type":"response.created"`) {
		t.Fatalf("buffered handshake must be flushed before the in-stream error: %s", combined)
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusBadRequest {
		t.Fatalf("status code = %d, want %d", got, http.StatusBadRequest)
	}
}

// Once the buffer limit is exceeded the stream is released and overload probing stops, which
// bounds how long the downstream response headers can stay uncommitted.
func TestCodexExecutor_BootstrapBuffering_BufferLimitReleasesStream(t *testing.T) {
	events := make([]string, 0, codexBootstrapMaxBufferedFrames+2)
	for i := 0; i < codexBootstrapMaxBufferedFrames+1; i++ {
		events = append(events, fmt.Sprintf(`{"type":"response.in_progress","response":{"id":"resp_%d"}}`, i))
	}
	events = append(events, codexOverloadEvent)
	server := codexSSEServer(events...)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err != nil {
		t.Fatalf("expected the stream to be released once the buffer limit is hit: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result after the buffer limit released the stream")
	}
	_, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected the overload error to be delivered in-stream after the limit was hit")
	}
}

// codexWebsocketRawServer writes the supplied frames and then closes the connection, so a test can
// drive an exact frame sequence rather than the canonical handshake.
func codexWebsocketRawServer(t *testing.T, frames []string, tail ...string) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			return
		}
		for _, frame := range frames {
			if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(frame)); errWrite != nil {
				return
			}
		}
		for _, frame := range tail {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(frame))
		}
	}))
}

// The websocket frame budget must bound allow-listed frames too, not only the skippable ones.
func TestCodexWebsocketsExecutor_BootstrapBuffering_FrameBudgetReleasesStream(t *testing.T) {
	frames := make([]string, 0, codexBootstrapMaxBufferedFrames+1)
	for i := 0; i < codexBootstrapMaxBufferedFrames+1; i++ {
		frames = append(frames, codexInProgressEvent)
	}
	server := codexWebsocketRawServer(t, frames, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("the frame budget must release the stream before the overload arrives: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the frame budget released the stream")
	}
	drainChunks(result)
}

// The websocket budget is pinned on both sides, the same way the SSE one is.
func TestCodexWebsocketsExecutor_BootstrapBuffering_BudgetBoundaryIsExact(t *testing.T) {
	send := func(n int) error {
		frames := make([]string, n)
		for i := range frames {
			frames[i] = codexInProgressEvent
		}
		server := codexWebsocketRawServer(t, frames, codexOverloadEvent)
		defer server.Close()
		req, opts := codexWebsocketRequest()
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if result != nil {
			drainChunks(result)
		}
		return err
	}
	if err := send(codexBootstrapMaxBufferedFrames - 1); err == nil {
		t.Fatalf("%d messages are inside the budget and must still fail over", codexBootstrapMaxBufferedFrames-1)
	}
	// The last message the window still admits. Without this case a budget one frame too small
	// looks identical, since the overload is recognised before the window is consulted and so the
	// max-1 case fails over either way.
	if err := send(codexBootstrapMaxBufferedFrames); err == nil {
		t.Fatalf("%d messages exactly fill the budget and must still fail over", codexBootstrapMaxBufferedFrames)
	}
	if err := send(codexBootstrapMaxBufferedFrames + 1); err != nil {
		t.Fatalf("%d messages exceed the budget and must release: %v", codexBootstrapMaxBufferedFrames+1, err)
	}
}

// The websocket byte budget has the same two properties as the SSE one: a message larger than the
// cap is refused admission rather than taken on the strength of an empty buffer, and the budget is
// seeded from the upstream message so it still advances for a downstream that renders nothing.
func TestCodexWebsocketsExecutor_BootstrapBuffering_ByteCapOrderingAndSeed(t *testing.T) {
	t.Run("oversized message is not admitted", func(t *testing.T) {
		oversized := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("q", codexBootstrapMaxBufferedBytes*2) + `"}}`
		server := codexWebsocketRawServer(t, []string{oversized}, codexOverloadEvent)
		defer server.Close()

		req, opts := codexWebsocketRequest()
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err != nil {
			t.Fatalf("a message larger than the cap must be released, not admitted: %v", err)
		}
		if result == nil {
			t.Fatal("expected a stream result")
		}
		drainChunks(result)
	})

	t.Run("budget counts the upstream message", func(t *testing.T) {
		frame := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("w", codexBootstrapMaxBufferedBytes/8) + `"}}`
		frames := make([]string, 10)
		for i := range frames {
			frames[i] = frame
		}
		server := codexWebsocketRawServer(t, frames, codexOverloadEvent)
		defer server.Close()

		req, _ := codexWebsocketRequest()
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai")}
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err != nil {
			t.Fatalf("ten messages of cap/8 must exhaust the byte budget: %v", err)
		}
		if result == nil {
			t.Fatal("expected a stream result")
		}
		drainChunks(result)
	})
}

// The frame that trips the budget must still be delivered. Releasing the window is a decision about
// what to do with later frames, not a licence to drop the one in hand: on a slow turn that frame is
// the first token of the answer.
func TestCodexWebsocketsExecutor_BootstrapBuffering_BudgetFrameIsNotDropped(t *testing.T) {
	frames := make([]string, 0, codexBootstrapMaxBufferedFrames+2)
	for i := 0; i < codexBootstrapMaxBufferedFrames; i++ {
		frames = append(frames, codexInProgressEvent)
	}
	frames = append(frames, `{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"FIRSTTOKEN"}`)
	server := codexWebsocketRawServer(t, frames, codexCompletedEventBody)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("unexpected ExecuteStream error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result")
	}
	combined, _ := drainChunks(result)
	if !strings.Contains(combined, "FIRSTTOKEN") {
		t.Fatalf("the frame that tripped the budget was dropped: %s", combined)
	}
}

// The websocket bound must be enforced where the frame is counted. A peer that sends only frames the
// bootstrap loop skips never reaches the buffering branch, so a release decided there would never
// run and the downstream headers would stay uncommitted for as long as the peer keeps sending.
func TestCodexWebsocketsExecutor_BootstrapBuffering_SkippedFramesExhaustTheWindow(t *testing.T) {
	cases := []struct {
		name  string
		frame string
	}{
		{"whitespace only frames", "   \n\t "},
		{"empty frames", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frames := make([]string, codexBootstrapMaxBufferedFrames+2)
			for i := range frames {
				frames[i] = tc.frame
			}
			server := codexWebsocketRawServer(t, frames, codexOverloadEvent)
			defer server.Close()

			req, opts := codexWebsocketRequest()
			result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

			if err != nil {
				t.Fatalf("the window must release before the overload arrives, got failover: %v", err)
			}
			if result == nil {
				t.Fatal("expected a stream result once the window was exhausted")
			}
			drainChunks(result)
		})
	}
}

// The window only protects anything if it is small. Asserting it relative to itself, as the framing
// tests below must, cannot catch a budget that has grown to a useless size.
func TestCodexBootstrapBudgetsStaySmall(t *testing.T) {
	// Pinned exactly, because the framing tests below express their expectations as multiples of
	// these constants and so cannot notice the budgets growing.
	// TestCodexExecutor_BootstrapBuffering_HeartbeatArithmeticPerFraming states the resulting
	// heartbeat counts in absolute numbers, which is what config.example.yaml quotes to operators.
	if codexBootstrapMaxBufferedFrames != 48 {
		t.Fatalf("frame budget = %d, want 48; changing it changes how long the headers are held, so update config.example.yaml too", codexBootstrapMaxBufferedFrames)
	}
	if codexBootstrapMaxBufferedBytes != 1<<20 {
		t.Fatalf("byte budget = %d, want 1MiB", codexBootstrapMaxBufferedBytes)
	}
}

// In-stream delivery depends on the held frames rendering to at least one downstream chunk: the
// conductor commits a stream only once it has seen a non-empty payload, so against a format that
// renders the handshake as nothing there is nothing to commit and the attempt still fails over.
func TestCodexExecutor_BootstrapBuffering_InStreamDeliveryNeedsRenderedFrames(t *testing.T) {
	incomplete := `{"type":"response.incomplete","response":{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexOutputAddedEvent, incomplete)
	defer server.Close()

	req, _ := codexTestRequest()
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("the executor still returns a stream; it is the conductor that cannot commit it: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result")
	}
	combined, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected the failure to arrive as an in-stream chunk error")
	}
	if combined != "" {
		t.Fatalf("Chat Completions renders these frames as nothing, so no payload precedes the error: %q", combined)
	}
}

// An empty response.incomplete is not an overload rejection, so it keeps its in-stream delivery:
// the buffered frames are flushed and the error arrives as a chunk rather than burning another
// credential. The websocket executor already routes this condition that way; both transports agree
// on it. A truncated stream is deliberately left alone - both transports fail that one over.
func TestCodexExecutor_BootstrapBuffering_NonOverloadTerminalStaysInStream(t *testing.T) {
	cases := []struct{ name, tail string }{
		{"empty incomplete", `{"type":"response.incomplete","response":{"id":"resp_1","output":[],"usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1}}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexOutputAddedEvent, tc.tail)
			defer server.Close()

			req, opts := codexTestRequest()
			result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

			if err != nil {
				t.Fatalf("a non-overload terminal failure must not fail the attempt over: %v", err)
			}
			if result == nil {
				t.Fatal("expected a stream result carrying the buffered frames and the error")
			}
			combined, streamErr := drainChunks(result)
			if streamErr == nil {
				t.Fatal("expected the failure to arrive as an in-stream chunk error")
			}
			if !strings.Contains(combined, `"type":"response.created"`) {
				t.Fatalf("buffered frames must be flushed before the in-stream error: %s", combined)
			}
		})
	}
}

// The cap is consulted before a frame is taken, not after. A frame that alone exceeds it is refused
// admission and released, so the rejection that follows it immediately - with no line in between to
// trigger a late release - is delivered in-stream rather than failing the attempt over.
func TestCodexExecutor_BootstrapBuffering_OversizedFrameIsNotAdmitted(t *testing.T) {
	oversized := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("q", codexBootstrapMaxBufferedBytes*2) + `"}}`
	body := "data: " + oversized + "\n" + "data: " + codexOverloadEvent + "\n\n"
	server := codexSSERawServer(body)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("a frame larger than the cap must be released, not admitted: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the oversized frame released the stream")
	}
	drainChunks(result)
}

// The byte budget is seeded from the upstream frame, not only from the chunks it translates into.
// Against a Chat Completions downstream these frames render as zero chunks, so an accounting that
// measured chunks alone would never advance and only the frame budget would remain - which these ten
// frames stay well inside.
func TestCodexExecutor_BootstrapBuffering_ByteCapCountsUpstreamFrames(t *testing.T) {
	frame := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("w", codexBootstrapMaxBufferedBytes/8) + `"}}`
	body := strings.Repeat("data: "+frame+"\n\n", 10) + "data: " + codexOverloadEvent + "\n\n"
	server := codexSSERawServer(body)
	defer server.Close()

	req, _ := codexTestRequest()
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("ten frames of cap/8 must exhaust the byte budget: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte budget released the stream")
	}
	drainChunks(result)
}

// The websocket byte cap needs its own coverage: without it a handful of large frames would sit
// inside the frame budget while retaining far more than the cap.
func TestCodexWebsocketsExecutor_BootstrapBuffering_ByteCapReleasesStream(t *testing.T) {
	third := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("z", codexBootstrapMaxBufferedBytes/3) + `"}}`
	server := codexWebsocketRawServer(t, []string{third, third, third, third}, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err != nil {
		t.Fatalf("the websocket byte cap must release the stream before the overload arrives: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte cap released the stream")
	}
	drainChunks(result)
}

// codexSSERawServer writes raw bytes, so a test can choose its own SSE framing.
func codexSSERawServer(body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(body))
	}))
}

// Pins the budget at its exact boundary. Comment heartbeats cost one scanned line each, so the
// budget holds exactly codexBootstrapMaxBufferedFrames of them and the next one releases. Written
// with absolute arithmetic on purpose: the framing cases below scale with the constant and so cannot
// notice an off-by-one in the comparison.
func TestCodexExecutor_BootstrapBuffering_BudgetBoundaryIsExact(t *testing.T) {
	overload := "data: " + codexOverloadEvent + "\n\n"

	t.Run("one line under the budget still fails over", func(t *testing.T) {
		body := strings.Repeat(": keepalive\n", codexBootstrapMaxBufferedFrames-1) + overload
		server := codexSSERawServer(body)
		defer server.Close()

		req, opts := codexTestRequest()
		_, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err == nil {
			t.Fatalf("%d held lines are inside the %d-line budget and must still fail over", codexBootstrapMaxBufferedFrames-1, codexBootstrapMaxBufferedFrames)
		}
	})

	t.Run("exactly the budget still fails over", func(t *testing.T) {
		body := strings.Repeat(": keepalive\n", codexBootstrapMaxBufferedFrames) + overload
		server := codexSSERawServer(body)
		defer server.Close()

		req, opts := codexTestRequest()
		_, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err == nil {
			t.Fatalf("%d held lines fill the budget exactly and must still fail over", codexBootstrapMaxBufferedFrames)
		}
	})

	t.Run("one line over the budget releases", func(t *testing.T) {
		body := strings.Repeat(": keepalive\n", codexBootstrapMaxBufferedFrames+1) + overload
		server := codexSSERawServer(body)
		defer server.Close()

		req, opts := codexTestRequest()
		result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err != nil {
			t.Fatalf("%d held lines exceed the %d-line budget and must release: %v", codexBootstrapMaxBufferedFrames+1, codexBootstrapMaxBufferedFrames, err)
		}
		if result == nil {
			t.Fatal("expected a stream result once the budget released the stream")
		}
		drainChunks(result)
	})
}

// "data:\n\n" is the standard SSE heartbeat idiom and carries nothing, so it must not commit the
// headers. Before this was handled it produced an empty event type, fell through the allow-list and
// silently turned the feature off for the whole stream - while the websocket transport, which skips
// an empty message, kept working.
func TestCodexExecutor_BootstrapBuffering_EmptyDataFrameDoesNotReleaseStream(t *testing.T) {
	for _, frame := range []string{"data:\n\n", "data: \n\n", "data:   \t\n\n"} {
		body := frame + "data: " + codexOverloadEvent + "\n\n"
		server := codexSSERawServer(body)

		req, opts := codexTestRequest()
		result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err == nil {
			t.Errorf("an empty data frame %q must keep the bootstrap window open", frame)
		}
		if result != nil {
			t.Errorf("frame %q: expected nil result so no buffered chunk can reach the client", frame)
		}
		server.Close()
	}
}

// The bound must fire whatever framing the upstream picks. Each case uses a framing that a bound
// derived from blank separators, from data: lines, or from translated chunks would fail to advance
// on, and a Chat Completions downstream, which renders these frames as zero chunks. That last part
// is why these cases cannot also assert what was flushed - there is nothing downstream to look at.
// That the frames are held at all is covered by BufferLimitReleasesStream and the
// NonContentFramesDoNotReleaseStream cases, which run against a passthrough downstream.
func TestCodexExecutor_BootstrapBuffering_BoundHoldsForEverySSEFraming(t *testing.T) {
	overload := "data: " + codexOverloadEvent + "\n\n"
	handshake := `{"type":"response.in_progress","response":{"id":"resp_1"}}`

	cases := []struct {
		name string
		body string
	}{
		{
			// Comment heartbeats need no blank separator at all.
			name: "comment heartbeats without blank separators",
			body: strings.Repeat(": keepalive\n", codexBootstrapMaxBufferedFrames*4) + overload,
		},
		{
			// A data: line is a complete frame; a blank line is optional between them.
			name: "data frames without blank separators",
			body: strings.Repeat("data: "+handshake+"\n", codexBootstrapMaxBufferedFrames*4) + overload,
		},
		{
			// event:/data:/blank, the framing the canonical test server emits.
			name: "three line framing",
			body: strings.Repeat("event: response.in_progress\ndata: "+handshake+"\n\n", codexBootstrapMaxBufferedFrames*4) + overload,
		},
		{
			// Extra blank separators are legal and must not be mistaken for extra frames.
			name: "double blank separators",
			body: strings.Repeat("data: "+handshake+"\n\n\n", codexBootstrapMaxBufferedFrames*4) + overload,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := codexSSERawServer(tc.body)
			defer server.Close()

			req, _ := codexTestRequest()
			// Chat Completions renders an unrecognised frame as zero chunks, so a bound derived from
			// the downstream chunk count would never advance here.
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: true}
			result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

			if err != nil {
				t.Fatalf("the bound must release the stream before the overload arrives, got failover: %v", err)
			}
			if result == nil {
				t.Fatal("expected a stream result once the bound released the stream")
			}
			if _, streamErr := drainChunks(result); streamErr == nil {
				t.Fatal("expected the overload to arrive in-stream after the bound released the stream")
			}
		})
	}
}

// A single frame may be large: scanner.Buffer allows 50MB per line, so a frame budget alone is not a
// memory bound. Measured against a passthrough downstream, where a held frame really does occupy
// memory.
func TestCodexExecutor_BootstrapBuffering_ByteCapReleasesStream(t *testing.T) {
	// Frames that are individually well inside the cap but add up past it must still release, which
	// is what makes this a cap on the buffer rather than a per-frame size limit.
	third := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("y", codexBootstrapMaxBufferedBytes/3) + `"}}`
	body := "data: " + third + "\n\n" + "data: " + third + "\n\n" + "data: " + third + "\n\n" +
		"data: " + third + "\n\n" + "data: " + codexOverloadEvent + "\n\n"
	server := codexSSERawServer(body)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err != nil {
		t.Fatalf("the byte cap must release the stream before the overload arrives, got failover: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte cap released the stream")
	}
}

// Upstream interleaves keepalive heartbeats and item announcements while the model is still
// thinking. Neither has reached the client, so a rejection arriving after one must still fail the
// attempt over rather than surface in-stream.
func TestCodexExecutor_BootstrapBuffering_NonContentFramesDoNotReleaseStream(t *testing.T) {
	cases := []struct{ name, frame string }{
		{"keepalive", codexKeepaliveEvent},
		{"output item added", codexOutputAddedEvent},
		{"content part added", `{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`},
		{"reasoning summary part added", `{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, tc.frame, codexOverloadEvent)
			defer server.Close()

			req, opts := codexTestRequest()
			result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

			if err == nil {
				t.Fatalf("a %s frame must keep the bootstrap window open", tc.name)
			}
			if result != nil {
				t.Fatal("expected nil result so no buffered chunk can reach the client")
			}
			if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
				t.Fatalf("status code = %d, want %d", got, http.StatusServiceUnavailable)
			}
		})
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_NonContentFramesDoNotReleaseStream(t *testing.T) {
	for _, frame := range []string{codexKeepaliveEvent, codexOutputAddedEvent} {
		server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, frame, codexOverloadEvent)
		req, opts := codexWebsocketRequest()
		result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if err == nil || result != nil {
			t.Fatalf("frame %s must keep the websocket bootstrap window open", frame)
		}
		server.Close()
	}
}

// The mirror image of the two tests above, and the reason the list is closed: a frame carrying
// generated output, or announcing a server-side tool that may already be running, must commit the
// downstream headers so a later rejection can no longer replay it on another credential. Without
// this pair both call sites can stop consulting the allow-list altogether and the suite stays green.
func TestCodexExecutor_BootstrapBuffering_ContentFrameReleasesBeforeOverload(t *testing.T) {
	for _, tc := range codexReleasingFrameCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, tc.frame, codexOverloadEvent)
			defer server.Close()

			req, opts := codexTestRequest()
			result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
			if err != nil {
				t.Fatalf("a %s frame must release the stream, not fail the attempt over: %v", tc.name, err)
			}
			if result == nil {
				t.Fatalf("a %s frame must release the stream", tc.name)
			}
			if _, streamErr := drainChunks(result); streamErr == nil {
				t.Fatalf("the overload after a %s frame must be delivered in-stream", tc.name)
			}
		})
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_ContentFrameReleasesBeforeOverload(t *testing.T) {
	for _, tc := range codexReleasingFrameCases() {
		t.Run(tc.name, func(t *testing.T) {
			server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, tc.frame, codexOverloadEvent)
			defer server.Close()

			req, opts := codexWebsocketRequest()
			result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
			if err != nil {
				t.Fatalf("a %s frame must release the stream, not fail the attempt over: %v", tc.name, err)
			}
			if result == nil {
				t.Fatalf("a %s frame must release the stream", tc.name)
			}
			if _, streamErr := drainChunks(result); streamErr == nil {
				t.Fatalf("the overload after a %s frame must be delivered in-stream", tc.name)
			}
		})
	}
}

// One frame the client has already seen, and one that means the upstream has dispatched work of its
// own. Both must reach the client rather than be held.
func codexReleasingFrameCases() []struct{ name, frame string } {
	return []struct{ name, frame string }{
		{"output text delta", codexOutputDeltaEvent},
		{"web search call announced", `{"type":"response.output_item.added","item":{"id":"ws_1","type":"web_search_call","status":"in_progress"},"output_index":0}`},
	}
}

// The existing cancellation test cancels before the request is sent; this one cancels mid-bootstrap,
// with frames already held, which is the case the feature makes common. It pins the returned error
// only. The guard it exercises also suppresses recording the cancellation as an upstream failure
// against the credential, and that half is not observable from here: the scan error raised by a
// cancelled read is the context.Canceled sentinel itself, so removing the guard leaves the returned
// value unchanged and this test green.
func TestCodexExecutor_BootstrapBuffering_CancelDuringBootstrapIsNotAnUpstreamFailure(t *testing.T) {
	released := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: " + codexCreatedEvent + "\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-released
	}))
	defer server.Close()
	defer close(released)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(ctx, codexTestAuth(server.URL), req, opts)
	if result != nil {
		drainChunks(result)
	}
	// Identity rather than errors.Is, so a transport error that merely wraps the cancellation cannot
	// satisfy it.
	if err != context.Canceled {
		t.Fatalf("a cancelled caller must surface as context.Canceled itself, got %T: %v", err, err)
	}
}

// The frame budget is a line count, so what it is worth to an operator depends on how the upstream
// frames a heartbeat. These are the numbers config.example.yaml quotes, in absolute form: a test
// that says "codexBootstrapMaxBufferedFrames heartbeats" would move with the constant and could
// never catch the documented figure drifting. Each count is one short of the obvious division
// because the rejection frame's own event: line is charged to the same budget before its data: line
// is parsed.
func TestCodexExecutor_BootstrapBuffering_HeartbeatArithmeticPerFraming(t *testing.T) {
	overload := "event: error\ndata: " + codexOverloadEvent + "\n\n"
	cases := []struct {
		name      string
		heartbeat string
		protected int
	}{
		{"three-line event:/data:/blank", "event: keepalive\ndata: " + codexKeepaliveEvent + "\n\n", 15},
		{"two-line : keepalive comment", ": keepalive\n\n", 23},
		{"one-line : keepalive comment", ": keepalive\n", 47},
	}
	failsOver := func(t *testing.T, heartbeats int, heartbeat, overload string) bool {
		t.Helper()
		server := codexSSERawServer(strings.Repeat(heartbeat, heartbeats) + overload)
		defer server.Close()
		req, opts := codexTestRequest()
		result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
		if result != nil {
			drainChunks(result)
		}
		return err != nil
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !failsOver(t, tc.protected, tc.heartbeat, overload) {
				t.Fatalf("%d heartbeats of the %s framing must still fail over; config.example.yaml documents %d", tc.protected, tc.name, tc.protected)
			}
			if failsOver(t, tc.protected+1, tc.heartbeat, overload) {
				t.Fatalf("%d heartbeats of the %s framing exceed the budget and must release the stream", tc.protected+1, tc.name)
			}
		})
	}
}

// The byte budget is charged for the upstream frame *and* for the chunks it translates into, because
// what is retained is the chunks. Both padded frames fit the cap on their upstream bytes alone, so
// only a budget that also counts the translated chunks releases the stream here.
func TestCodexExecutor_BootstrapBuffering_ByteCapCountsTranslatedChunks(t *testing.T) {
	padded := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("p", 300<<10) + `"}}`
	server := codexSSEServer(padded, padded, codexOverloadEvent)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("two frames whose upstream bytes fit the cap but whose chunks do not must release the stream: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte budget released the stream")
	}
	if _, streamErr := drainChunks(result); streamErr == nil {
		t.Fatal("expected the overload to arrive in-stream after the byte budget released")
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_ByteCapCountsTranslatedChunks(t *testing.T) {
	padded := `{"type":"response.in_progress","response":{"id":"` + strings.Repeat("p", 300<<10) + `"}}`
	server := codexWebsocketRawServer(t, []string{padded, padded}, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("two frames whose upstream bytes fit the cap but whose chunks do not must release the stream: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result once the byte budget released the stream")
	}
	if _, streamErr := drainChunks(result); streamErr == nil {
		t.Fatal("expected the overload to arrive in-stream after the byte budget released")
	}
}

// Anything the client has seen, anything a server-side tool has already started, and anything
// unrecognised must release the stream. This is what keeps the list closed.
func TestIsCodexBootstrapBufferableEvent(t *testing.T) {
	hold := []string{
		`{"type":"response.created"}`,
		`{"type":"response.in_progress"}`,
		`{"type":"codex.rate_limits"}`,
		`{"type":"codex.response.metadata"}`,
		codexKeepaliveEvent,
		codexOutputAddedEvent,
		`{"type":"response.output_item.added","item":{"id":"rs_1","type":"reasoning"}}`,
		`{"type":"response.output_item.added","item":{"id":"fc_1","type":"function_call","arguments":""}}`,
		`{"type":"response.output_item.added","item":{"id":"ct_1","type":"custom_tool_call","input":""}}`,
		`{"type":"response.output_item.added","item":{"id":"msg_2","type":"message","content":[{"type":"output_text","text":""}]}}`,
		`{"type":"response.output_item.added","item":{"id":"msg_3","type":"message","content":[{"type":"refusal","refusal":""}]}}`,
		`{"type":"response.output_item.added","item":{"id":"rs_2","type":"reasoning","summary":[{"type":"summary_text","text":""}]}}`,
		`{"type":"response.output_item.added","item":{"id":"rs_3","type":"reasoning","content":[{"type":"reasoning_text","text":""}]}}`,
		`{"type":"response.content_part.added","part":{"type":"text","text":""}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"reasoning_text","text":""}}`,
		`{"type":"response.content_part.added","part":{"type":"output_text","text":""}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":""}}`,
		``,
		`   `,
	}
	for _, payload := range hold {
		eventType := gjson.GetBytes([]byte(payload), "type").String()
		if !isCodexBootstrapBufferableEvent(eventType, []byte(payload)) {
			t.Errorf("must stay bufferable: %s", payload)
		}
	}

	release := []string{
		`{"type":"response.output_text.delta","delta":"hi"}`,
		`{"type":"response.reasoning_summary_text.delta","delta":"x"}`,
		`{"type":"response.shell_call_command.delta","delta":"ls"}`,
		`{"type":"response.shell_call_output_content.delta","delta":{"stdout":"x"}}`,
		`{"type":"response.shell_call_output_content.done","output":[]}`,
		`{"type":"response.output_item.done","item":{"id":"m1","type":"message"}}`,
		`{"type":"response.output_item.added","item":{"id":"ws_1","type":"web_search_call","status":"in_progress"}}`,
		`{"type":"response.output_item.added","item":{"id":"fs_1","type":"file_search_call"}}`,
		`{"type":"response.output_item.added","item":{"id":"ig_1","type":"image_generation_call"}}`,
		`{"type":"response.output_item.added","item":{"id":"x_1","type":"some_future_server_tool"}}`,
		`{"type":"response.content_part.added","part":{"type":"output_text","text":"already here"}}`,
		`{"type":"response.reasoning_summary_part.added","part":{"type":"summary_text","text":"already here"}}`,
		`{"type":"response.content_part.added","part":{"type":"output_audio","audio":"AAAA"}}`,
		`{"type":"response.content_part.added","part":{"type":"refusal","refusal":"I cannot help"}}`,
		`{"type":"response.output_item.added","item":{"id":"rf1","type":"message","content":[{"type":"refusal","refusal":"I cannot help"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"m1","type":"message","content":[{"type":"output_text","text":"already generated"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"r1","type":"reasoning","summary":[{"type":"summary_text","text":"already reasoned"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"f1","type":"function_call","arguments":"{\"path\":\"/\"}"}}`,
		`{"type":"response.output_item.added","item":{"id":"c1","type":"custom_tool_call","input":"already here"}}`,
		`{"type":"response.output_item.added","item":{"id":"a1","type":"message","content":[{"type":"output_audio","audio":"AAAA"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"i1","type":"message","content":[{"type":"output_image","image_url":"data:x"}]}}`,
		`{"type":"response.output_item.added","item":{"id":"r2","type":"reasoning","encrypted_content":"BLOB"}}`,
		`{"type":"response.output_item.added","item":{"id":"r3","type":"reasoning","content":[{"type":"reasoning_text","text":"already reasoned"}]}}`,
		`{"type":"response.web_search_call.searching","item_id":"ws_1"}`,
		codexCompletedEventBody,
		codexOverloadEvent,
		`{"type":"response.some_future_event_we_have_never_seen"}`,
	}
	for _, payload := range release {
		eventType := gjson.GetBytes([]byte(payload), "type").String()
		if isCodexBootstrapBufferableEvent(eventType, []byte(payload)) {
			t.Errorf("must release the stream: %s", payload)
		}
	}
}

// Buffered handshake events must be replayed in upstream order ahead of the first generated event.
func TestCodexExecutor_BootstrapBuffering_FlushesInOrderOnFirstOutput(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent, codexOutputAddedEvent, codexOutputDeltaEvent, codexCompletedEventBody)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("unexpected ExecuteStream error: %v", err)
	}

	combined, streamErr := drainChunks(result)
	if streamErr != nil {
		t.Fatalf("unexpected chunk error: %v", streamErr)
	}
	// Matched on the payload form throughout. The bare type name also appears in the `event:` line,
	// and that line is held whatever the frame turns out to be, so an index on it would find a
	// buffered chunk even if the released `data:` frame were dropped entirely.
	// Pinned as a chain rather than a single held-before-released comparison: the released frame is
	// always flushed after the whole held batch, so comparing against it alone cannot see the held
	// frames permuted among themselves.
	order := []string{`"type":"response.created"`, `"type":"response.in_progress"`, `"type":"response.output_item.added"`, `"type":"response.output_text.delta"`}
	prev := -1
	for _, marker := range order {
		at := strings.Index(combined, marker)
		if at < 0 {
			t.Fatalf("missing %s: %s", marker, combined)
		}
		if at <= prev {
			t.Fatalf("frames must be replayed in upstream order, %s came too early: %s", marker, combined)
		}
		prev = at
	}
	for _, marker := range order {
		if n := strings.Count(combined, marker); n != 1 {
			t.Fatalf("held frame %s was emitted %d times, want 1: %s", marker, n, combined)
		}
	}
}

// With the feature disabled the overload rejection keeps its legacy in-stream delivery.
func TestCodexExecutor_BootstrapBuffering_DefaultDisabledPassthrough(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexOverloadEvent)
	defer server.Close()

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("default unbuffered ExecuteStream returned error at call time: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result in default unbuffered mode")
	}
	_, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected stream error in chunks for default unbuffered mode")
	}
	// Disabling the feature must restore the previous behaviour exactly, status classification
	// included: the 503 restoration is scoped to the buffered failover path, so an unbuffered
	// overload still classifies as a bad gateway and keeps its old cooldown treatment.
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d while buffering is disabled", got, http.StatusBadGateway)
	}
}

// A cancelled downstream request must surface the context error rather than being recorded as an
// upstream failure that penalises the credential.
func TestCodexExecutor_BootstrapBuffering_ContextCancelDuringBootstrap(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req, opts := codexTestRequest()
	_, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(ctx, codexTestAuth(server.URL), req, opts)
	if err == nil {
		t.Fatal("expected an error for a cancelled bootstrap")
	}
	if !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("expected the context cancellation to surface, got: %v", err)
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_OverloadFailsAttempt(t *testing.T) {
	server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err == nil {
		t.Fatal("expected ExecuteStream to fail the attempt on a websocket overload rejection")
	}
	if result != nil {
		t.Fatal("expected nil result so no buffered handshake frame can reach the client")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_CapacityFailsAttempt(t *testing.T) {
	server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, codexCapacityEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err == nil {
		t.Fatal("expected ExecuteStream to fail the attempt on a websocket capacity rejection")
	}
	if result != nil {
		t.Fatal("expected nil result so no buffered frame can reach the client")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", got, http.StatusTooManyRequests)
	}
}

// The websocket transport prefixes response events with private metadata frames. Frame order
// below matches live wire capture: codex.rate_limits and codex.response.metadata both arrive
// *before* response.created, making the first generated event the fifth frame. They must be
// treated as handshake events, otherwise a fixed 3-event window would release the stream at
// response.created and never observe the rejection.
func TestCodexWebsocketsExecutor_BootstrapBuffering_PrivateHandshakeFramesDoNotExhaustWindow(t *testing.T) {
	server := codexWebsocketServer(t,
		`{"type":"codex.rate_limits","rate_limits":{"primary":{"used_percent":1}}}`,
		`{"type":"codex.response.metadata","metadata":{"conversation_id":"conv_1"}}`,
		codexCreatedEvent,
		codexInProgressEvent,
		codexOverloadEvent,
	)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err == nil {
		t.Fatal("expected the overload rejection to be caught past the private handshake frames")
	}
	if result != nil {
		t.Fatal("expected nil result so no buffered frame can reach the client")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", got, http.StatusServiceUnavailable)
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_NonOverloadStaysInStream(t *testing.T) {
	server := codexWebsocketServer(t, codexCreatedEvent, codexInProgressEvent, codexInvalidEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	if err != nil {
		t.Fatalf("non-overload failure must not fail the attempt synchronously: %v", err)
	}
	if result == nil {
		t.Fatal("expected a stream result for in-stream error delivery")
	}
	combined, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected the invalid-request failure to arrive as an in-stream chunk error")
	}
	if !strings.Contains(combined, `"type":"response.created"`) {
		t.Fatalf("buffered handshake must be flushed before the in-stream error: %s", combined)
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_FlushesInOrderOnFirstOutput(t *testing.T) {
	server := codexWebsocketServer(t,
		codexCreatedEvent,
		codexInProgressEvent,
		codexOutputAddedEvent,
		codexOutputDeltaEvent,
		`{"type":"response.completed","response":{"id":"resp_1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`,
	)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("unexpected ExecuteStream error: %v", err)
	}

	combined, streamErr := drainChunks(result)
	if streamErr != nil {
		t.Fatalf("unexpected chunk error: %v", streamErr)
	}
	// Pinned as a chain rather than a single held-before-released comparison: the released frame is
	// always flushed after the whole held batch, so comparing against it alone cannot see the held
	// frames permuted among themselves. Mirrors the SSE assertions exactly.
	order := []string{`"type":"response.created"`, `"type":"response.in_progress"`, `"type":"response.output_item.added"`, `"type":"response.output_text.delta"`}
	prev := -1
	for _, marker := range order {
		at := strings.Index(combined, marker)
		if at < 0 {
			t.Fatalf("missing %s: %s", marker, combined)
		}
		if at <= prev {
			t.Fatalf("frames must be replayed in upstream order, %s came too early: %s", marker, combined)
		}
		prev = at
	}
	for _, marker := range order {
		if got := strings.Count(combined, marker); got != 1 {
			t.Fatalf("held frame %s was emitted %d times, want 1: %s", marker, got, combined)
		}
	}
}

func TestCodexWebsocketsExecutor_BootstrapBuffering_DefaultDisabledPassthrough(t *testing.T) {
	server := codexWebsocketServer(t, codexCreatedEvent, codexOverloadEvent)
	defer server.Close()

	req, opts := codexWebsocketRequest()
	result, err := NewCodexWebsocketsExecutor(&config.Config{}).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("default unbuffered ExecuteStream returned error at call time: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result in default unbuffered mode")
	}
	_, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected stream error in chunks for default unbuffered mode")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d while buffering is disabled", got, http.StatusBadGateway)
	}
}

// The 503 restoration is scoped to the buffered failover path, so this only covers which
// rejections are eligible to replace the whole attempt.
func TestIsCodexOverloadBootstrapFailureRejectsRequestFaults(t *testing.T) {
	notOverload := []string{
		`{"error":{"type":"invalid_request_error","code":"invalid_value"}}`,
		`{"error":{"type":"authentication_error","code":"invalid_api_key"}}`,
		`{"error":{"type":"upstream_error","code":"unknown"}}`,
		`{"error":{"type":"server_error","code":"server_error","message":"An internal error occurred without retry advice"}}`,
	}
	for _, body := range notOverload {
		if isCodexOverloadBootstrapFailure([]byte(body)) {
			t.Fatalf("request-level fault must not trigger bootstrap failover: %s", body)
		}
	}
	if !isCodexOverloadBootstrapFailure([]byte(`{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded"}}`)) {
		t.Fatal("rate limit rejections should be eligible for bootstrap failover")
	}
	serverErrorFull := `{"error":{"type":"server_error","code":"server_error","message":"An error occurred while processing your request. You can retry your request, or contact us through our help center at help.openai.com if the error persists. Please include the request ID 2f5014c9-7cfe-4fb7-813e-ecb447da3edd in your message."}}`
	if !isCodexOverloadBootstrapFailure([]byte(serverErrorFull)) {
		t.Fatal("server_error with 'You can retry your request' should be eligible for bootstrap failover")
	}
	serverErrorShort := `{"error":{"type":"server_error","code":"server_error","message":"You can retry your request"}}`
	if !isCodexOverloadBootstrapFailure([]byte(serverErrorShort)) {
		t.Fatal("short server_error with 'You can retry your request' should be eligible for bootstrap failover")
	}
	capacityError := `{"error":{"message":"Selected model is at capacity. Please try a different model."}}`
	if !isCodexOverloadBootstrapFailure([]byte(capacityError)) {
		t.Fatal("model capacity error should be eligible for bootstrap failover")
	}
	capacityErrorShort := `{"error":{"message":"Selected Model is at capacity"}}`
	if !isCodexOverloadBootstrapFailure([]byte(capacityErrorShort)) {
		t.Fatal("short model capacity error should be eligible for bootstrap failover")
	}
}

// codexWebsocketServerHoldingConnection behaves like codexWebsocketServer but keeps the upstream
// connection open after writing the frames, so the executor's own teardown path is the only
// source of session invalidation. With the plain helper the connection closes immediately, the
// reader goroutine observes EOF first and reports upstream_disconnected, which both masks the
// path under test and can make a disconnect assertion pass for the wrong reason.
func codexWebsocketServerHoldingConnection(t *testing.T, frames ...string) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read websocket message: %v", errRead)
			return
		}
		for _, frame := range frames {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(frame))
		}
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
}

// executeWebsocketStreamInSession runs ExecuteStream bound to a named execution session and
// reports whether the upstream teardown was signalled to the downstream handler.
//
// The downstream Responses WebSocket handler subscribes to UpstreamDisconnectChan and closes
// the client connection as soon as a disconnect is published. A bootstrap overload is retried
// on another credential, so publishing there would tear down the client connection before the
// retry can deliver anything, and the client would observe an abnormal close with zero frames.
func executeWebsocketStreamInSession(t *testing.T, frames ...string) (notified bool, err error) {
	t.Helper()

	server := codexWebsocketServerHoldingConnection(t, frames...)
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(codexBufferingConfig(true))
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}

	const sessionID = "bootstrap-session"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected a disconnect channel")
	}

	req, opts := codexWebsocketRequest()
	opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID}
	_, err = exec.ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	select {
	case <-disconnectCh:
		notified = true
	default:
	}
	return notified, err
}

func TestCodexWebsocketsExecutor_BootstrapOverload_DoesNotNotifyDownstreamDisconnect(t *testing.T) {
	notified, err := executeWebsocketStreamInSession(t, codexCreatedEvent, codexInProgressEvent, codexOverloadEvent)

	if err == nil {
		t.Fatal("expected the overload rejection to fail the attempt")
	}
	if got := statusCodeFromTestError(t, err); got != http.StatusServiceUnavailable {
		t.Fatalf("status code = %d, want %d", got, http.StatusServiceUnavailable)
	}
	if notified {
		t.Fatal("bootstrap overload must not signal a downstream disconnect: the conductor still has to retry on another credential, and signalling closes the client connection with zero frames delivered")
	}
}

// A non-overload terminal failure is delivered in-stream and genuinely ends the session, so it
// must keep signalling the disconnect exactly as it did before buffering existed.
func TestCodexWebsocketsExecutor_BootstrapNonOverload_StillNotifiesDownstreamDisconnect(t *testing.T) {
	notified, err := executeWebsocketStreamInSession(t, codexCreatedEvent, codexInProgressEvent, codexInvalidEvent)

	if err != nil {
		t.Fatalf("non-overload failures stay in-stream, got err = %v", err)
	}
	if !notified {
		t.Fatal("a terminal failure that is delivered in-stream must still signal the downstream disconnect")
	}
}
