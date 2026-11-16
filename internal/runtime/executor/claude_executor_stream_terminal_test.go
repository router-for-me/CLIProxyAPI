package executor

import (
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

type captureClaudeUsagePlugin struct {
	records chan usage.Record
}

func (p *captureClaudeUsagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	if p == nil || record.Provider != "claude" {
		return
	}
	select {
	case p.records <- record:
	default:
	}
}

type noopClaudeUsagePlugin struct{}

func (noopClaudeUsagePlugin) HandleUsage(context.Context, usage.Record) {}

func TestClaudeExecutor_ExecuteStream_Translated_ClientDisconnectAfterTerminalEventIsNotFailed(t *testing.T) {
	const streamData = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	upstreamClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte(streamData))
			flusher.Flush()
		}
		// Hold the upstream body open until client disconnects or test ends,
		// reproducing upstream lag where body close happens after terminal event.
		select {
		case <-r.Context().Done():
		case <-upstreamClosed:
		}
	}))
	defer func() {
		close(upstreamClosed)
		server.Close()
	}()

	pluginName := "test-claude-translated-disconnect"
	plugin := &captureClaudeUsagePlugin{
		records: make(chan usage.Record, 4),
	}
	usage.RegisterNamedPlugin(pluginName, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
	})

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "key-123",
			"base_url": server.URL,
		},
	}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	sawTerminalEvent := false
	// Read chunks until terminal event is seen, then immediately cancel downstream context
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error during stream read: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), "response.completed") {
			// Client disconnects immediately upon receiving terminal event (e.g. Codex CLI >=0.153.4)
			sawTerminalEvent = true
			cancel()
			break
		}
	}
	if !sawTerminalEvent {
		t.Fatal("expected to observe terminal response.completed event before stream finished")
	}

	select {
	case record := <-plugin.records:
		if record.Failed {
			t.Fatalf("expected usage record to not be marked failed, but got failed=true, fail status: %d body: %s", record.Fail.StatusCode, record.Fail.Body)
		}
		if record.Detail.InputTokens != 100 {
			t.Errorf("InputTokens = %d, want 100", record.Detail.InputTokens)
		}
		if record.Detail.OutputTokens != 15 {
			t.Errorf("OutputTokens = %d, want 15", record.Detail.OutputTokens)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

func TestClaudeExecutor_ExecuteStream_Passthrough_ClientDisconnectAfterTerminalEventIsNotFailed(t *testing.T) {
	const streamData = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":{"input_tokens":100,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1}}}` + "\n\n" +
		"event: content_block_start\n" +
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n" +
		"event: content_block_stop\n" +
		`data: {"type":"content_block_stop","index":0}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	upstreamClosed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte(streamData))
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-upstreamClosed:
		}
	}))
	defer func() {
		close(upstreamClosed)
		server.Close()
	}()

	pluginName := "test-claude-passthrough-disconnect"
	plugin := &captureClaudeUsagePlugin{
		records: make(chan usage.Record, 4),
	}
	usage.RegisterNamedPlugin(pluginName, plugin)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(pluginName, noopClaudeUsagePlugin{})
	})

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "key-123",
			"base_url": server.URL,
		},
	}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "claude-opus-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	sawTerminalEvent := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), "message_stop") {
			sawTerminalEvent = true
			cancel()
			break
		}
	}
	if !sawTerminalEvent {
		t.Fatal("expected to observe terminal message_stop event before stream finished")
	}

	select {
	case record := <-plugin.records:
		if record.Failed {
			t.Fatalf("expected usage record to not be marked failed in passthrough, but got failed=true, fail status: %d body: %s", record.Fail.StatusCode, record.Fail.Body)
		}
		if record.Detail.InputTokens != 100 {
			t.Errorf("InputTokens = %d, want 100", record.Detail.InputTokens)
		}
		if record.Detail.OutputTokens != 15 {
			t.Errorf("OutputTokens = %d, want 15", record.Detail.OutputTokens)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

func TestClaudeExecutor_ExecuteStream_DoesNotDrainOrReuseUpstreamConnectionAfterMessageStop(t *testing.T) {
	const streamData = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_123","type":"message","role":"assistant","content":[],"model":"claude-opus-5","stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n" +
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

	var (
		newConns atomic.Int32
		release  = make(chan struct{}, 4)
		closed   = make(chan struct{}, 4)
		done     = make(chan struct{})
		mu       sync.Mutex
		reused   []bool
	)
	defer close(done)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(streamData))
		w.(http.Flusher).Flush()
		// Hold the chunked terminator back until the client either attempts a
		// post-message_stop drain read or closes the connection.
		select {
		case <-release:
		case <-r.Context().Done():
		case <-done:
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		switch state {
		case http.StateNew:
			newConns.Add(1)
		case http.StateClosed, http.StateHijacked:
			select {
			case closed <- struct{}{}:
			default:
			}
		}
	}
	server.Start()
	defer server.Close()

	baseTransport := &http.Transport{}
	t.Cleanup(baseTransport.CloseIdleConnections)
	transport := postMessageStopReadReleaseRoundTripper{
		base:    baseTransport,
		total:   len(streamData),
		release: release,
	}
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			mu.Lock()
			reused = append(reused, info.Reused)
			mu.Unlock()
		},
		PutIdleConn: func(err error) {
			if err == nil {
				select {
				case closed <- struct{}{}:
				default:
				}
			}
		},
	}

	exec := NewClaudeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "key-no-drain",
			"base_url": server.URL,
		},
	}
	payload := []byte(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	for i := 0; i < 2; i++ {
		ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
		ctx = httptrace.WithClientTrace(ctx, trace)
		result, err := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
			Model:   "claude-opus-5",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat:   sdktranslator.FormatClaude,
			ResponseFormat: sdktranslator.FormatClaude,
		})
		if err != nil {
			t.Fatalf("round %d ExecuteStream() error = %v", i+1, err)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("round %d chunk error = %v", i+1, chunk.Err)
			}
		}
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatalf("round %d connection did not settle", i+1)
		}
	}

	mu.Lock()
	gotReused := append([]bool(nil), reused...)
	mu.Unlock()
	if got := newConns.Load(); got != 2 {
		t.Fatalf("stream upstream connections = %d, want 2 (reused = %v)", got, gotReused)
	}
	if len(gotReused) != 2 || gotReused[0] || gotReused[1] {
		t.Fatalf("stream GotConn reused = %v, want [false false]", gotReused)
	}
}

type postMessageStopReadReleaseRoundTripper struct {
	base    http.RoundTripper
	total   int
	release chan<- struct{}
}

func (rt postMessageStopReadReleaseRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &postMessageStopReadReleaseBody{
		ReadCloser: resp.Body,
		total:      rt.total,
		release:    rt.release,
	}
	return resp, nil
}

type postMessageStopReadReleaseBody struct {
	io.ReadCloser
	total   int
	read    int
	release chan<- struct{}
	once    sync.Once
}

func (b *postMessageStopReadReleaseBody) Read(p []byte) (int, error) {
	if b.read >= b.total {
		b.once.Do(func() {
			select {
			case b.release <- struct{}{}:
			default:
			}
		})
	}
	n, err := b.ReadCloser.Read(p)
	b.read += n
	return n, err
}
