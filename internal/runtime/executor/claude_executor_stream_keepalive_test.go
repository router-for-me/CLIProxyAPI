package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const keepaliveStreamStart = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_keepalive","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"

const keepaliveStreamData = keepaliveStreamStart +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
	"event: content_block_delta\n" +
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
	"event: content_block_stop\n" +
	`data: {"type":"content_block_stop","index":0}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

// keepaliveFailSafe only bounds how long a broken implementation may hang; it
// never orders events in a passing run.
const keepaliveFailSafe = 10 * time.Second

// keepaliveHarness runs streamed requests through the Claude executor against
// a local chunked SSE server and counts upstream connections.
type keepaliveHarness struct {
	t         *testing.T
	server    *httptest.Server
	transport http.RoundTripper
	exec      *ClaudeExecutor
	auth      *cliproxyauth.Auth
	format    sdktranslator.Format
	// gzipBody makes the server gzip the stream, holding the gzip trailer
	// back together with the terminating chunk (see keepaliveGzipParts).
	gzipBody bool
	newConns atomic.Int32
	settled  chan struct{}
	trace    *httptrace.ClientTrace
	mu       sync.Mutex
	reused   []bool
}

// newKeepaliveHarness starts a server that writes keepaliveStreamData with
// chunked encoding and then holds the terminating chunk back until it
// receives from release (or the client drops the connection). This mirrors
// the real API, where the terminator arrives in a later segment than
// message_stop, and keeps net/http from seeing EOF together with the last
// event. With holdSeparator, the blank line that ends the message_stop event
// is held back as well and sent only after release.
func newKeepaliveHarness(t *testing.T, release <-chan struct{}, holdSeparator bool) *keepaliveHarness {
	t.Helper()
	h := &keepaliveHarness{t: t, settled: make(chan struct{}, 4), format: sdktranslator.FormatClaude}
	notify := func() {
		select {
		case h.settled <- struct{}{}:
		default:
		}
	}
	h.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		data := keepaliveStreamData
		if holdSeparator {
			data = data[:len(data)-1]
		}
		if h.gzipBody {
			prefix, suffix := keepaliveGzipParts(data)
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(prefix)
			w.(http.Flusher).Flush()
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write(suffix)
			return
		}
		_, _ = w.Write([]byte(data))
		// Flushing forces chunked transfer encoding.
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if holdSeparator {
			_, _ = w.Write([]byte("\n"))
		}
	}))
	h.server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			h.newConns.Add(1)
		case http.StateClosed, http.StateHijacked:
			notify()
		}
	}
	h.server.Start()
	t.Cleanup(h.server.Close)
	// Registered after server.Close, so it runs first: the requests are done
	// by then, and their usage records must not leak into later tests.
	t.Cleanup(func() { flushKeepaliveUsage(t) })

	base := &http.Transport{}
	t.Cleanup(base.CloseIdleConnections)
	h.transport = base
	h.trace = &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			h.mu.Lock()
			h.reused = append(h.reused, info.Reused)
			h.mu.Unlock()
		},
		PutIdleConn: func(err error) {
			if err == nil {
				notify()
			}
		},
	}
	h.exec = NewClaudeExecutor(&config.Config{})
	h.auth = &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-keepalive",
		"base_url": h.server.URL,
	}}
	return h
}

func (h *keepaliveHarness) start(ctx context.Context) <-chan cliproxyexecutor.StreamChunk {
	h.t.Helper()
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", h.transport)
	ctx = httptrace.WithClientTrace(ctx, h.trace)
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	if h.format == sdktranslator.FormatOpenAIResponse {
		payload = []byte(`{"model":"claude-opus-5","input":"hi","stream":true}`)
	}
	result, err := h.exec.ExecuteStream(ctx, h.auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   h.format,
		ResponseFormat: h.format,
	})
	if err != nil {
		h.t.Fatalf("ExecuteStream() error = %v", err)
	}
	return result.Chunks
}

// drainChunks consumes the stream like a real client and fails if it does not
// close within the fail-safe.
func (h *keepaliveHarness) drainChunks(chunks <-chan cliproxyexecutor.StreamChunk) {
	h.t.Helper()
	failSafe := time.NewTimer(keepaliveFailSafe)
	defer failSafe.Stop()
	for {
		select {
		case chunk, ok := <-chunks:
			if !ok {
				return
			}
			if chunk.Err != nil {
				h.t.Fatalf("chunk error = %v", chunk.Err)
			}
		case <-failSafe.C:
			h.t.Fatal("Chunks did not close after message_stop")
		}
	}
}

// waitSettled waits until the previous connection was pooled or closed, so a
// following request deterministically observes the outcome.
func (h *keepaliveHarness) waitSettled() {
	h.t.Helper()
	select {
	case <-h.settled:
	case <-time.After(keepaliveFailSafe):
		h.t.Fatal("upstream connection was neither pooled nor closed")
	}
}

func (h *keepaliveHarness) assertSingleConnection() {
	h.t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if got := h.newConns.Load(); got != 1 {
		h.t.Fatalf("upstream connections = %d, want 1 (GotConn reused = %v)", got, h.reused)
	}
	if len(h.reused) != 2 || !h.reused[1] {
		h.t.Fatalf("second request did not reuse the connection (GotConn reused = %v)", h.reused)
	}
}

// TestClaudeExecutor_ExecuteStream_ReusesUpstreamConnection guards against the
// stream loop closing the upstream body right after message_stop without
// consuming the chunked terminator, which makes net/http discard the
// connection instead of returning it to the idle pool.
func TestClaudeExecutor_ExecuteStream_ReusesUpstreamConnection(t *testing.T) {
	release := make(chan struct{}, 4)
	h := newKeepaliveHarness(t, release, false)
	// A bare Close never releases the terminator, so the bounded post-close
	// drain newer Go versions do inside net/http cannot mask a missing drain.
	h.transport = keepaliveReleaseRoundTripper{base: h.transport, total: len(keepaliveStreamData), release: release}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.drainChunks(h.start(ctx))
	h.waitSettled()
	h.drainChunks(h.start(ctx))
	h.assertSingleConnection()
}

// TestClaudeExecutor_ExecuteStream_ReusesUpstreamConnectionWithGzip covers
// compressed upstream responses, where the end of the body also carries the
// gzip trailer: the drain must read the raw body to EOF, not just the decoded
// stream.
func TestClaudeExecutor_ExecuteStream_ReusesUpstreamConnectionWithGzip(t *testing.T) {
	release := make(chan struct{}, 4)
	h := newKeepaliveHarness(t, release, false)
	h.gzipBody = true
	prefix, _ := keepaliveGzipParts(keepaliveStreamData)
	h.transport = keepaliveReleaseRoundTripper{base: h.transport, total: len(prefix), release: release}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.drainChunks(h.start(ctx))
	h.waitSettled()
	h.drainChunks(h.start(ctx))
	h.assertSingleConnection()
}

// TestClaudeExecutor_ExecuteStream_ReusesUpstreamConnectionAfterDownstreamCancel
// mirrors the real handler, which cancels the request context as soon as
// Chunks closes. The drain after message_stop must not be aborted by that.
func TestClaudeExecutor_ExecuteStream_ReusesUpstreamConnectionAfterDownstreamCancel(t *testing.T) {
	release := make(chan struct{}, 4)
	h := newKeepaliveHarness(t, release, false)
	// The drain's first read past the event data is held until the test has
	// cancelled the downstream context, so the drain deterministically runs
	// after the cancellation.
	cancelled := make(chan struct{}, 4)
	failSafe := make(chan struct{})
	t.Cleanup(func() { close(failSafe) })
	h.transport = keepaliveReleaseRoundTripper{base: h.transport, total: len(keepaliveStreamData), release: release, gate: cancelled, failSafe: failSafe}

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		h.drainChunks(h.start(ctx))
		cancel()
		cancelled <- struct{}{}
		if i == 0 {
			h.waitSettled()
		}
	}
	h.assertSingleConnection()
}

// TestClaudeExecutor_ExecuteStream_ClosesChunksWhenUpstreamStallsAfterMessageStop
// ensures stream completion never waits on the best-effort drain: an upstream
// that sends message_stop but never finishes the response must not keep
// Chunks open.
func TestClaudeExecutor_ExecuteStream_ClosesChunksWhenUpstreamStallsAfterMessageStop(t *testing.T) {
	stall := make(chan struct{})
	h := newKeepaliveHarness(t, stall, false)
	// Registered after the harness, so it runs before server.Close and lets a
	// still-running drain finish.
	t.Cleanup(func() { close(stall) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.drainChunks(h.start(ctx))
}

// TestClaudeExecutor_ExecuteStream_DownstreamCancelBeforeMessageStopAbortsUpstream
// ensures that, until message_stop, downstream cancellation still aborts the
// upstream request so no tokens are generated for a gone client.
func TestClaudeExecutor_ExecuteStream_DownstreamCancelBeforeMessageStopAbortsUpstream(t *testing.T) {
	t.Cleanup(func() { flushKeepaliveUsage(t) })
	upstreamDone := make(chan struct{})
	testDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(keepaliveStreamStart))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(upstreamDone)
		case <-testDone:
			// Lets server.Close return when the upstream was never aborted.
		}
	}))
	defer server.Close()
	defer close(testDone)

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-keepalive",
		"base_url": server.URL,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	failSafe := time.NewTimer(keepaliveFailSafe)
	defer failSafe.Stop()
	select {
	case chunk := <-result.Chunks:
		if chunk.Err != nil {
			t.Fatalf("chunk error = %v", chunk.Err)
		}
	case <-failSafe.C:
		t.Fatal("did not receive message_start")
	}
	cancel()

	select {
	case <-upstreamDone:
	case <-failSafe.C:
		t.Fatal("downstream cancel before message_stop did not abort the upstream request")
	}
	for {
		select {
		case _, ok := <-result.Chunks:
			if !ok {
				return
			}
		case <-failSafe.C:
			t.Fatal("Chunks did not close after downstream cancel")
		}
	}
}

// TestClaudeExecutor_ExecuteStream_Translated_ReusesUpstreamConnectionAfterCancelOnTerminalEvent
// covers the translated path: translation runs per line, so the message_stop
// data line already yields response.completed, and the Responses handler
// cancels on its delivery before the executor reads the event's blank
// separator. The upstream request must be detached before that output is
// delivered, so the connection is still pooled.
func TestClaudeExecutor_ExecuteStream_Translated_ReusesUpstreamConnectionAfterCancelOnTerminalEvent(t *testing.T) {
	release := make(chan struct{}, 4)
	h := newKeepaliveHarness(t, release, true)
	h.format = sdktranslator.FormatOpenAIResponse
	// The executor's read of the held-back separator waits until the client
	// has cancelled on response.completed.
	cancelled := make(chan struct{}, 4)
	failSafe := make(chan struct{})
	t.Cleanup(func() { close(failSafe) })
	h.transport = keepaliveReleaseRoundTripper{base: h.transport, total: len(keepaliveStreamData) - 1, release: release, gate: cancelled, failSafe: failSafe}

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		chunks := h.start(ctx)
		timer := time.NewTimer(keepaliveFailSafe)
		sawTerminal := false
	read:
		for {
			select {
			case chunk, ok := <-chunks:
				if !ok {
					break read
				}
				if chunk.Err != nil {
					t.Fatalf("request %d: chunk error = %v", i+1, chunk.Err)
				}
				if !sawTerminal && strings.Contains(string(chunk.Payload), "response.completed") {
					// Like the Responses handler: cancel as soon as the
					// terminal event was delivered.
					sawTerminal = true
					cancel()
					cancelled <- struct{}{}
				}
			case <-timer.C:
				t.Fatalf("request %d: Chunks did not close", i+1)
			}
		}
		timer.Stop()
		cancel()
		if !sawTerminal {
			t.Fatalf("request %d: no response.completed event", i+1)
		}
		if i == 0 {
			h.waitSettled()
		}
	}
	h.assertSingleConnection()
}

// TestClaudeExecutor_ExecuteStream_Translated_ClosesChunksWhenSeparatorStalls
// ensures the translated path treats the message_stop data line as completion:
// an upstream that never sends the event's blank separator (until after Chunks
// closes) must not keep the stream open.
func TestClaudeExecutor_ExecuteStream_Translated_ClosesChunksWhenSeparatorStalls(t *testing.T) {
	stall := make(chan struct{})
	h := newKeepaliveHarness(t, stall, true)
	h.format = sdktranslator.FormatOpenAIResponse
	// Registered after the harness, so it runs before server.Close and lets the
	// background drain finish.
	t.Cleanup(func() { close(stall) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.drainChunks(h.start(ctx))
}

// keepaliveGzipParts gzips data and splits the result after a sync flush: the
// prefix decodes to all of data, the suffix holds the final block and the
// gzip trailer.
func keepaliveGzipParts(data string) (prefix, suffix []byte) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(data))
	_ = zw.Flush()
	prefix = bytes.Clone(buf.Bytes())
	_ = zw.Close()
	return prefix, bytes.Clone(buf.Bytes()[len(prefix):])
}

// TestClaudeExecutor_ExecuteStream_Translated_MalformedMessageStopIsNotCompletion
// ensures the early completion on the message_stop data line only accepts
// valid JSON, like the event observer: a truncated line must still end in the
// incomplete-stream error.
func TestClaudeExecutor_ExecuteStream_Translated_MalformedMessageStopIsNotCompletion(t *testing.T) {
	t.Cleanup(func() { flushKeepaliveUsage(t) })
	valid := `data: {"type":"message_stop"}`
	cut := strings.Index(keepaliveStreamData, valid)
	if cut < 0 {
		t.Fatal("stream data has no message_stop line")
	}
	data := keepaliveStreamData[:cut] + `data: {"type":"message_stop"` + "\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(data))
	}))
	defer server.Close()

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"api_key":  "key-keepalive",
		"base_url": server.URL,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: []byte(`{"model":"claude-opus-5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	var streamErr error
	failSafe := time.NewTimer(keepaliveFailSafe)
	defer failSafe.Stop()
	for done := false; !done; {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				done = true
				break
			}
			if chunk.Err != nil {
				streamErr = chunk.Err
			}
		case <-failSafe.C:
			t.Fatal("Chunks did not close")
		}
	}
	if streamErr == nil || !strings.Contains(streamErr.Error(), "before message_stop") {
		t.Fatalf("stream error = %v, want the incomplete-stream error", streamErr)
	}
}

// flushKeepaliveUsage waits until every usage record published so far has been
// dispatched, so records from these tests cannot reach a later test's usage
// plugin. The default usage queue is FIFO with a single worker, so once a
// sentinel record arrives, all earlier records were delivered.
func flushKeepaliveUsage(t *testing.T) {
	t.Helper()
	const name = "keepalive-usage-flush"
	id := "keepalive-sentinel-" + t.Name()
	seen := make(chan struct{}, 1)
	usage.RegisterNamedPlugin(name, keepaliveSentinelPlugin{id: id, seen: seen})
	defer usage.RegisterNamedPlugin(name, noopClaudeUsagePlugin{})
	usage.PublishRecord(context.Background(), usage.Record{Provider: "keepalive-sentinel", RequestID: id})
	select {
	case <-seen:
	case <-time.After(keepaliveFailSafe):
		t.Fatal("usage queue did not flush")
	}
}

type keepaliveSentinelPlugin struct {
	id   string
	seen chan<- struct{}
}

func (p keepaliveSentinelPlugin) HandleUsage(_ context.Context, record usage.Record) {
	if record.RequestID != p.id {
		return
	}
	select {
	case p.seen <- struct{}{}:
	default:
	}
}

// keepaliveReleaseRoundTripper wraps response bodies so the test server is
// released once the client asks for more than the event data. With a gate, that
// first read past the data waits for one gate signal (or the fail-safe) before
// going on.
type keepaliveReleaseRoundTripper struct {
	base     http.RoundTripper
	total    int
	release  chan<- struct{}
	gate     <-chan struct{}
	failSafe <-chan struct{}
}

func (rt keepaliveReleaseRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &keepaliveReleaseBody{ReadCloser: resp.Body, rt: rt}
	return resp, nil
}

type keepaliveReleaseBody struct {
	io.ReadCloser
	rt   keepaliveReleaseRoundTripper
	read int
	once sync.Once
}

func (b *keepaliveReleaseBody) Read(p []byte) (int, error) {
	if b.read >= b.rt.total {
		b.once.Do(func() {
			if b.rt.gate != nil {
				select {
				case <-b.rt.gate:
				case <-b.rt.failSafe:
				}
			}
			b.rt.release <- struct{}{}
		})
	}
	n, err := b.ReadCloser.Read(p)
	b.read += n
	return n, err
}
