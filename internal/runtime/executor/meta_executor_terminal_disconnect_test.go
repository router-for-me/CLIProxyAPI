package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type metaTerminalDisconnectUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *metaTerminalDisconnectUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c == nil || record.Provider != "meta" || record.AuthID != c.authID {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type metaTerminalDisconnectUsageNoop struct{}

func (metaTerminalDisconnectUsageNoop) HandleUsage(context.Context, usage.Record) {}

const (
	metaTerminalDisconnectDelta     = "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n"
	metaTerminalDisconnectCompleted = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"muse-wonder-1.0\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":10,\"output_tokens\":5,\"total_tokens\":15}}}\n\n"
)

// The upstream keeps the body open after the written events, as Meta does
// while it still has to send response.subscription_usage. A Claude client
// cancels once it sees cancelAfter.
func runMetaClaudeDisconnect(t *testing.T, upstreamSSE, cancelAfter string) usage.Record {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, upstreamSSE)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}
	capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, errExecute := NewMetaExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: []byte(`{"model":"muse-wonder-1.0","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	canceled := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error before cancellation: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), cancelAfter) {
			canceled = true
			cancel()
			break
		}
	}
	if !canceled {
		t.Fatalf("stream ended before %q", cancelAfter)
	}

	select {
	case record := <-capture.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

func TestMetaStream_ClientDisconnectAfterTerminalEventIsNotFailed(t *testing.T) {
	record := runMetaClaudeDisconnect(t, metaTerminalDisconnectDelta+metaTerminalDisconnectCompleted, "message_stop")
	if record.Failed {
		t.Fatalf("usage record marked failed: status=%d", record.Fail.StatusCode)
	}
	if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
		t.Fatalf("reported usage = %+v, want input 10 output 5", record.Detail)
	}
}

func TestMetaStream_ClientDisconnectBeforeTerminalEventIsFailed(t *testing.T) {
	record := runMetaClaudeDisconnect(t, metaTerminalDisconnectDelta, "content_block_delta")
	if !record.Failed {
		t.Fatal("usage record not marked failed")
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
}

func TestMetaStream_CleanEOFBeforeTerminalEventIsFailed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, metaTerminalDisconnectDelta)
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}
	capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{})
	})

	result, errExecute := NewMetaExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: []byte(`{"model":"muse-wonder-1.0","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var streamed strings.Builder
	var streamErr error
	for chunk := range result.Chunks {
		streamed.Write(chunk.Payload)
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if !strings.Contains(streamed.String(), "hello") {
		t.Fatalf("partial output missing from stream: %q", streamed.String())
	}
	if streamErr == nil {
		t.Fatal("clean EOF without a terminal event did not produce a stream error")
	}
	statusErr, okStatus := streamErr.(interface{ StatusCode() int })
	if !okStatus || statusErr.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error status = %v, want %d", streamErr, http.StatusBadGateway)
	}
	truncatedErr, okTruncated := streamErr.(cliproxyexecutor.StreamTruncatedError)
	if !okTruncated || !truncatedErr.IsStreamTruncated() {
		t.Fatalf("stream error = %T (%v), want IsStreamTruncated() = true", streamErr, streamErr)
	}

	select {
	case record := <-capture.records:
		if !record.Failed {
			t.Fatal("usage record not marked failed")
		}
		if record.Fail.StatusCode != http.StatusBadGateway {
			t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, http.StatusBadGateway)
		}
		if record.Fail.Code != usage.FailureStreamTruncatedCode {
			t.Fatalf("Fail.Code = %q, want %q", record.Fail.Code, usage.FailureStreamTruncatedCode)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

func TestMetaStream_ResponseDoneTerminalEventRecordsSuccessUsage(t *testing.T) {
	metaTerminalDone := strings.Replace(metaTerminalDisconnectCompleted, "response.completed", "response.done", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, metaTerminalDisconnectDelta+metaTerminalDone)
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}
	capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{})
	})

	result, errExecute := NewMetaExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: []byte(`{"model":"muse-wonder-1.0","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error after response.done: %v", chunk.Err)
		}
	}

	select {
	case record := <-capture.records:
		if record.Failed {
			t.Fatalf("usage record marked failed: status=%d code=%q", record.Fail.StatusCode, record.Fail.Code)
		}
		if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
			t.Fatalf("reported usage = %+v, want input 10 output 5", record.Detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

func TestMetaStream_ResponseDoneTerminalEventTranslatesToClaude(t *testing.T) {
	metaTerminalDone := "event: response.done\n" + strings.Replace(metaTerminalDisconnectCompleted, "response.completed", "response.done", 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, metaTerminalDisconnectDelta+metaTerminalDone)
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": server.URL,
		},
	}
	capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{})
	})

	result, errExecute := NewMetaExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: []byte(`{"model":"muse-wonder-1.0","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var streamed strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error after response.done: %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	if !strings.Contains(streamed.String(), "hello") {
		t.Fatalf("translated Claude stream missing text delta: %q", streamed.String())
	}
	if got := strings.Count(streamed.String(), "event: message_stop"); got != 1 {
		t.Fatalf("translated Claude stream event: message_stop count = %d, want 1; output=%q", got, streamed.String())
	}

	select {
	case record := <-capture.records:
		if record.Failed {
			t.Fatalf("usage record marked failed: status=%d code=%q", record.Fail.StatusCode, record.Fail.Code)
		}
		if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
			t.Fatalf("reported usage = %+v, want input 10 output 5", record.Detail)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for usage record")
	}
}

func TestMetaExecute_ResponseDoneTerminalEventReturnsBodyAndUsage(t *testing.T) {
	metaTerminalDone := "event: response.done\n" + strings.Replace(metaTerminalDisconnectCompleted, "response.completed", "response.done", 1)
	for _, tc := range []struct {
		name    string
		format  sdktranslator.Format
		payload string
	}{
		{
			name:    "responses",
			format:  sdktranslator.FormatOpenAIResponse,
			payload: `{"model":"muse-wonder-1.0","input":"hi","stream":false}`,
		},
		{
			name:    "claude",
			format:  sdktranslator.FormatClaude,
			payload: `{"model":"muse-wonder-1.0","max_tokens":64,"stream":false,"messages":[{"role":"user","content":"hello"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, metaTerminalDisconnectDelta+metaTerminalDone)
			}))
			defer server.Close()

			auth := &cliproxyauth.Auth{
				ID:       t.Name(),
				Provider: "meta",
				Attributes: map[string]string{
					"api_key":  "meta-token",
					"base_url": server.URL,
				},
			}
			capture := &metaTerminalDisconnectUsageCapture{authID: auth.ID, records: make(chan usage.Record, 1)}
			usage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() {
				usage.RegisterNamedPlugin(t.Name(), metaTerminalDisconnectUsageNoop{})
			})

			resp, errExecute := NewMetaExecutor(&config.Config{}).Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "muse-wonder-1.0",
				Payload: []byte(tc.payload),
			}, cliproxyexecutor.Options{
				SourceFormat: tc.format,
				Stream:       false,
			})
			if errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			if !strings.Contains(string(resp.Payload), "hello") {
				t.Fatalf("Execute() payload = %q, want body containing hello", resp.Payload)
			}

			select {
			case record := <-capture.records:
				if record.Failed {
					t.Fatalf("usage record marked failed: status=%d code=%q", record.Fail.StatusCode, record.Fail.Code)
				}
				if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
					t.Fatalf("reported usage = %+v, want input 10 output 5", record.Detail)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for usage record")
			}
		})
	}
}
