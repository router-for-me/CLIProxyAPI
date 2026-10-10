package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/http2"
)

// codexLogCapture is a logrus hook that records entries emitted through the standard logger so
// tests can assert on operator-visible diagnostics without parsing stdout.
type codexLogCapture struct {
	mu      sync.Mutex
	entries []*log.Entry
}

func (c *codexLogCapture) Levels() []log.Level { return log.AllLevels }

func (c *codexLogCapture) Fire(entry *log.Entry) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, entry)
	return nil
}

func (c *codexLogCapture) messages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	messages := make([]string, 0, len(c.entries))
	for _, entry := range c.entries {
		messages = append(messages, entry.Message)
	}
	return messages
}

func attachCodexLogCapture(t *testing.T) *codexLogCapture {
	t.Helper()
	capture := &codexLogCapture{}
	logger := log.StandardLogger()
	logger.AddHook(capture)
	t.Cleanup(func() {
		for _, level := range log.AllLevels {
			hooks := logger.Hooks[level]
			filtered := hooks[:0]
			for _, hook := range hooks {
				if hook != capture {
					filtered = append(filtered, hook)
				}
			}
			logger.Hooks[level] = filtered
		}
	})
	return capture
}

// An SSE stream that ends without a recognized terminal event must leave one metadata-only
// termination diagnostic on the operator log. The normalized 408 the client sees cannot say
// whether the cause was a clean EOF, a transport read error, or something else; without this
// line an operator who cannot enable request-log (private prompts and tool output) has no
// evidence at all. The diagnostic carries event counts and classes only, never bodies.
func TestCodexExecutor_IncompleteStreamLogsTerminationDiagnostics(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexOutputDeltaEvent)
	defer server.Close()

	capture := attachCodexLogCapture(t)

	req, opts := codexTestRequest()
	result, err := NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if err != nil {
		t.Fatalf("stream start must not fail synchronously: %v", err)
	}
	_, streamErr := drainChunks(result)
	if streamErr == nil {
		t.Fatal("expected the incomplete-stream error to arrive as an in-stream chunk")
	}
	if got := statusCodeFromTestError(t, streamErr); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d (normalized client behaviour must not change)", got, http.StatusRequestTimeout)
	}

	var diagnostic string
	for _, message := range capture.messages() {
		if strings.Contains(message, "upstream SSE stream terminated") {
			diagnostic = message
			break
		}
	}
	if diagnostic == "" {
		t.Fatalf("expected a metadata-only termination diagnostic on the operator log, got: %v", capture.messages())
	}
	for _, want := range []string{
		"read_error=clean-eof",
		"events=2",
		"last_event_type=response.output_text.delta",
	} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("termination diagnostic %q missing %q", diagnostic, want)
		}
	}
}

// fakeNetTimeoutErr satisfies net.Error with the timeout flag set, without dragging in a real
// socket.
type fakeNetTimeoutErr struct{}

func (fakeNetTimeoutErr) Error() string   { return "some io hustle timed out" }
func (fakeNetTimeoutErr) Timeout() bool   { return true }
func (fakeNetTimeoutErr) Temporary() bool { return true }

// classifyCasesWithoutHttp2 is the closed vocabulary the diagnostic line may spell. The http2
// entries below use the concrete x/net/http2.StreamError type, because that is what the uTLS h2
// path surfaces on RST_STREAM and its Error() text ("stream error: stream ID N; CODE") contains no
// "http2" substring for the message matcher to find.
func classifyCasesWithoutHttp2() []struct {
	name string
	err  error
	want string
} {
	return []struct {
		name string
		err  error
		want string
	}{
		{"nil read error is a clean eof", nil, "clean-eof"},
		{"unexpected eof sentinel", io.ErrUnexpectedEOF, "unexpected-eof"},
		{"context cancelled", context.Canceled, "client-cancelled"},
		{"net timeout flag", fakeNetTimeoutErr{}, "timeout"},
		{
			"reset never leaks the peer address",
			errors.New("read tcp 10.1.2.3:52344->93.184.216.34:443: connection reset by peer"),
			"connection-reset",
		},
		{"unexpected eof in text", errors.New("http: unexpected EOF reading frame"), "unexpected-eof"},
		{"deadline text", errors.New("context deadline exceeded while reading"), "timeout"},
		{"tls failure class", errors.New("remote error: tls: bad certificate"), "tls-error"},
		{"http2 class in text", errors.New("http2: stream closed with error code CANCEL"), "http2-stream-error"},
		{"http/2 class in text", errors.New("http/2: stream closed with error code CANCEL"), "http2-stream-error"},
		{"http2 stream error type", http2.StreamError{StreamID: 3, Code: http2.ErrCodeInternal}, "http2-stream-error"},
		// The standard-library transport (net/http's bundled http2 stack, reachable through
		// http.DefaultTransport on the fallback round-tripper path) returns its own unexported
		// stream error, so errors.As cannot match it; its Error() text has a stable prefix instead.
		{
			"stdlib bundled h2 stream error text",
			errors.New("stream error: stream ID 7; INTERNAL_ERROR; received from peer"),
			"http2-stream-error",
		},
		{"opaque error stays sanitized", errors.New("mystery transport gremlin from 10.0.0.9"), "read-error"},
	}
}

func TestClassifyCodexSSEReadError(t *testing.T) {
	for _, tc := range classifyCasesWithoutHttp2() {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyCodexSSEReadError(tc.err); got != tc.want {
				t.Fatalf("classifyCodexSSEReadError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

// An upstream-supplied event type is untrusted gjson text: the diagnostic line is metadata-only,
// so it may never echo upstream-controlled content. A character allow-list is not enough - prompt
// text is usually lowercase letters and digits and would sail through - so only the closed
// vocabulary of real Codex event names may appear; anything else collapses to "unknown".
func TestCodexDiagnosticEventTypeSanitizesUpstreamSuppliedTypes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"real event types pass through", "response.output_text.delta", "response.output_text.delta"},
		{"empty becomes a dash", "", "-"},
		{"whitespace only becomes a dash", "  \n\t", "-"},
		{"control characters around a real type are tolerated", "res\x00po\x1bnse.created", "response.created"},
		{"lowercase prompt-like text never passes", "someleakedpromptcontent", "unknown"},
		{"unknown vocabulary types map to unknown", "response.brand_new_event.done", "unknown"},
		{"newline cannot forge a log record", "response.created\nfake line", "unknown"},
		{"oversized unknown text maps to unknown", "response." + strings.Repeat("x", 200), "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := codexDiagnosticEventType(tc.in); got != tc.want {
				t.Fatalf("codexDiagnosticEventType(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The bootstrap-buffered scan consumes the handshake frames before the unbuffered goroutine's
// counters exist, so the diagnostic must be seeded from the held frames: without that, a clean EOF
// right after the handshake reports events=0 / last_event_type=- and total time near zero, and an
// operator reads "the upstream sent nothing" when it sent - and the client received - the whole
// opening sequence.
func TestCodexExecutor_BootstrapBuffering_TerminationDiagnosticSeesHeldEvents(t *testing.T) {
	server := codexSSEServer(codexCreatedEvent, codexInProgressEvent)
	defer server.Close()

	capture := attachCodexLogCapture(t)

	req, opts := codexTestRequest()
	_, err := NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)
	if got := statusCodeFromTestError(t, err); got != http.StatusRequestTimeout {
		t.Fatalf("status code = %d, want %d (normalized client behaviour must not change)", got, http.StatusRequestTimeout)
	}

	diagnostic := ""
	for _, message := range capture.messages() {
		if strings.Contains(message, "upstream SSE stream terminated") {
			diagnostic = message
			break
		}
	}
	if diagnostic == "" {
		t.Fatalf("expected a metadata-only termination diagnostic on the operator log, got: %v", capture.messages())
	}
	for _, want := range []string{
		"read_error=clean-eof",
		"events=2",
		"last_event_type=response.in_progress",
	} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("termination diagnostic %q missing %q", diagnostic, want)
		}
	}
}

// A stream that dies before any frame at all still owes the operator the one diagnostic line: a
// clean EOF on the very first read is exactly the silent-hang class the feature exists to explain,
// and the buffered path ends this way without ever reaching the unbuffered goroutine.
func TestCodexExecutor_BootstrapBuffering_TerminationDiagnosticOnEmptyStream(t *testing.T) {
	server := codexSSEServer()
	defer server.Close()

	capture := attachCodexLogCapture(t)

	req, opts := codexTestRequest()
	// The empty-stream failure keeps its existing client-visible shape (this PR only adds the
	// diagnostic, it does not change what the client sees), so only the log line is asserted.
	NewCodexExecutor(codexBufferingConfig(true)).ExecuteStream(context.Background(), codexTestAuth(server.URL), req, opts)

	diagnostic := ""
	for _, message := range capture.messages() {
		if strings.Contains(message, "upstream SSE stream terminated") {
			diagnostic = message
			break
		}
	}
	if diagnostic == "" {
		t.Fatalf("expected a metadata-only termination diagnostic on the operator log, got: %v", capture.messages())
	}
	for _, want := range []string{"read_error=clean-eof", "events=0", "last_event_type=-"} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("termination diagnostic %q missing %q", diagnostic, want)
		}
	}
}
