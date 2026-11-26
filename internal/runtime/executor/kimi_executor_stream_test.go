package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type kimiStreamUsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *kimiStreamUsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if record.Provider == "kimi" && record.AuthID == c.authID {
		c.records <- record
	}
}

type kimiStreamUsageNoop struct{}

func (kimiStreamUsageNoop) HandleUsage(context.Context, usage.Record) {}

func registerKimiUsageCapture(t *testing.T) *kimiStreamUsageCapture {
	t.Helper()
	capture := &kimiStreamUsageCapture{
		authID:  t.Name(),
		records: make(chan usage.Record, 4),
	}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), kimiStreamUsageNoop{})
	})
	return capture
}

func waitKimiUsageRecord(t *testing.T, capture *kimiStreamUsageCapture) usage.Record {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case record := <-capture.records:
		return record
	case <-timer.C:
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

func waitKimiChunksClosed(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case _, ok := <-chunks:
			if !ok {
				return
			}
		case <-timer.C:
			t.Fatal("timed out waiting for stream channel to close")
		}
	}
}

func TestKimiExecutorExecuteStream_CanceledMidStreamPublishesFailure(t *testing.T) {
	capture := registerKimiUsageCapture(t)
	releaseUpstream := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// Include usage on the first chunk; translating to Claude produces multiple
		// SSE frames (message_start, content_block_start, content_block_delta),
		// allowing deterministic cancellation mid-delivery after streamUsage is populated.
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":3,\"total_tokens\":15}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-releaseUpstream:
		}
	}))
	defer func() {
		close(releaseUpstream)
		server.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exec := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       capture.authID,
		Provider: "kimi",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5",
		Payload: []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case _, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before first chunk")
		}
		// Pause receiving after cancel so the unbuffered send cannot win the select.
		cancel()
	case <-timer.C:
		t.Fatal("timed out waiting for first chunk")
	}

	record := waitKimiUsageRecord(t, capture)
	waitKimiChunksClosed(t, result.Chunks)
	if !record.Failed {
		t.Fatalf("expected canceled stream to publish failure, got Failed=false (detail=%+v)", record.Detail)
	}
	if record.Detail.InputTokens != 12 || record.Detail.OutputTokens != 3 || record.Detail.TotalTokens != 15 {
		t.Fatalf("unexpected usage detail on canceled stream: %+v", record.Detail)
	}
}

func TestKimiExecutorExecuteResponsesStream_CanceledMidStreamPublishesFailure(t *testing.T) {
	capture := registerKimiUsageCapture(t)
	releaseUpstream := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// With apply_patch enabled, the responses bridge buffers the "event:" header
		// and flushes both the event header and data line when processing the
		// "data: {\"type\":\"response.completed\",...}" line. Canceling right after
		// reading the "event: response.completed" chunk and pausing receives
		// deterministically forces <-ctx.Done() AFTER streamUsage.Observe has run
		// and BEFORE the terminal data line is delivered.
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\",\"model\":\"kimi-k2.5\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":21,\"output_tokens\":7,\"total_tokens\":28}}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-releaseUpstream:
		}
	}))
	defer func() {
		close(releaseUpstream)
		server.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exec := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       capture.authID,
		Provider: "kimi",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5",
		Payload: []byte(`{"model":"kimi-k2.5","input":"hi","stream":true,"tools":[{"type":"custom","name":"apply_patch"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	canceled := false
	for !canceled {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("stream closed before event: response.completed header")
			}
			if bytes.Contains(chunk.Payload, []byte("event: response.completed")) {
				// Pause receiving after cancel so the terminal data send cannot race with ctx.Done().
				cancel()
				canceled = true
			}
		case <-timer.C:
			t.Fatal("timed out waiting for event: response.completed header")
		}
	}

	record := waitKimiUsageRecord(t, capture)
	waitKimiChunksClosed(t, result.Chunks)
	if !record.Failed {
		t.Fatalf("expected canceled responses stream to publish failure, got Failed=false (detail=%+v)", record.Detail)
	}
	if record.Detail.InputTokens != 21 || record.Detail.OutputTokens != 7 || record.Detail.TotalTokens != 28 {
		t.Fatalf("unexpected usage detail on canceled responses stream: %+v", record.Detail)
	}
}

func TestKimiExecutorExecuteResponsesStream_CancelBeforeTerminalBlankLinePublishesFailure(t *testing.T) {
	tests := []struct {
		name           string
		sse            string
		holdConnection bool
	}{
		{
			name:           "with_blank_line_pending",
			sse:            "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-incomplete-frame\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":14,\"output_tokens\":6,\"total_tokens\":20}}}\n\n",
			holdConnection: true,
		},
		{
			name:           "clean_eof_without_blank_line",
			sse:            "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-no-blank\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":14,\"output_tokens\":6,\"total_tokens\":20}}}\n",
			holdConnection: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := registerKimiUsageCapture(t)
			releaseUpstream := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				if !tc.holdConnection {
					w.Header().Set("Content-Length", fmt.Sprint(len(tc.sse)))
				}
				_, _ = io.WriteString(w, tc.sse)
				w.(http.Flusher).Flush()
				if tc.holdConnection {
					select {
					case <-r.Context().Done():
					case <-releaseUpstream:
					}
				}
			}))
			defer func() {
				close(releaseUpstream)
				server.Close()
			}()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			exec := NewKimiExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				ID:       capture.authID,
				Provider: "kimi",
				Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": server.URL,
				},
			}
			result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
				Model:   "kimi-k2.5",
				Payload: []byte(`{"model":"kimi-k2.5","input":"hi","stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAIResponse,
				ResponseFormat: sdktranslator.FormatOpenAIResponse,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}

			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			canceled := false
			for !canceled {
				select {
				case chunk, ok := <-result.Chunks:
					if !ok {
						t.Fatal("stream closed before response.completed data line")
					}
					// Cancel right after receiving the data line, before any terminating empty SSE line ("\n").
					if bytes.Contains(chunk.Payload, []byte(`"type":"response.completed"`)) {
						cancel()
						canceled = true
					}
				case <-timer.C:
					t.Fatal("timed out waiting for response.completed data line")
				}
			}

			record := waitKimiUsageRecord(t, capture)
			waitKimiChunksClosed(t, result.Chunks)
			if !record.Failed {
				t.Fatalf("expected cancel before terminating blank SSE line to publish failure, got Failed=false (detail=%+v)", record.Detail)
			}
			if record.Detail.InputTokens != 14 || record.Detail.OutputTokens != 6 || record.Detail.TotalTokens != 20 {
				t.Fatalf("unexpected usage detail: %+v", record.Detail)
			}
		})
	}
}

func TestKimiExecutorExecuteStream_AllowsFrameLargerThan1MB(t *testing.T) {
	largeContent := strings.Repeat("a", 1_500_000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-big\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":\"stop\"}]}\n\n", largeContent)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	exec := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "kimi",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	result, errExecute := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5",
		Payload: []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"big"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatOpenAI,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var combined bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error on >1MB frame: %v", chunk.Err)
		}
		combined.Write(chunk.Payload)
	}
	if !strings.Contains(combined.String(), largeContent) {
		t.Fatalf("expected >1MB content to be streamed intact, got len=%d", combined.Len())
	}
}

func TestKimiExecutorExecuteStream_TruncatedEOFDoesNotSynthesizeDone(t *testing.T) {
	tests := []struct {
		name string
		sse  string
	}{
		{
			name: "null_finish_reason_without_done",
			sse:  "data: {\"id\":\"chatcmpl-trunc\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n",
		},
		{
			name: "null_primary_choice_finish_reason_with_secondary_stop",
			sse:  "data: {\"id\":\"chatcmpl-trunc2\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null},{\"index\":1,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := registerKimiUsageCapture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.sse)
			}))
			defer server.Close()

			exec := NewKimiExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				ID:       capture.authID,
				Provider: "kimi",
				Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": server.URL,
				},
			}
			result, errExecute := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "kimi-k2.5",
				Payload: []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAI,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}

			var output bytes.Buffer
			var streamErr error
			for chunk := range result.Chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if streamErr == nil {
				t.Fatalf("expected truncated EOF error, got nil; output=%s", output.String())
			}
			if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
				t.Fatalf("expected error wrapping io.ErrUnexpectedEOF, got %v", streamErr)
			}
			var truncErr cliproxyexecutor.StreamTruncatedError
			if !errors.As(streamErr, &truncErr) || !truncErr.IsStreamTruncated() {
				t.Fatalf("expected StreamTruncatedError, got %T (%v)", streamErr, streamErr)
			}
			if bytes.Contains(output.Bytes(), []byte("event: message_stop")) || bytes.Contains(output.Bytes(), []byte("[DONE]")) {
				t.Fatalf("truncated stream must not synthesize terminal events, got: %s", output.String())
			}

			record := waitKimiUsageRecord(t, capture)
			if !record.Failed {
				t.Fatalf("expected truncated EOF to publish failure, got Failed=false")
			}
		})
	}
}

func TestKimiExecutorExecuteStream_InBandSSEErrorFailsWithoutSynthesizingDone(t *testing.T) {
	tests := []struct {
		name  string
		frame string
	}{
		{
			name:  "data_error_object",
			frame: "data: {\"error\":{\"message\":\"upstream overloaded\",\"type\":\"server_error\"}}\n\n",
		},
		{
			name:  "event_error_frame",
			frame: "event: error\ndata: {\"error\":{\"message\":\"upstream overloaded\"}}\n\n",
		},
		{
			name:  "event_error_plain_text_data",
			frame: "event: error\ndata: upstream overloaded\n\n",
		},
		{
			name:  "multiline_error_frame_followed_by_done",
			frame: "data: {\"error\":\ndata: {\"message\":\"upstream overloaded\",\"type\":\"server_error\"}}\n\ndata: [DONE]\n\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := registerKimiUsageCapture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-err\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"partial\"},\"finish_reason\":null}],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":1,\"total_tokens\":9}}\n\n")
				_, _ = io.WriteString(w, tc.frame)
			}))
			defer server.Close()

			exec := NewKimiExecutor(&config.Config{SDKConfig: config.SDKConfig{RequestLog: true}})
			auth := &cliproxyauth.Auth{
				ID:       capture.authID,
				Provider: "kimi",
				Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": server.URL,
				},
			}
			ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
			apiCapture := logging.NewDeferredAPICapture()
			reqCtx := logging.WithDeferredAPICapture(context.WithValue(context.Background(), "gin", ginCtx), apiCapture)
			result, errExecute := exec.ExecuteStream(reqCtx, auth, cliproxyexecutor.Request{
				Model:   "kimi-k2.5",
				Payload: []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAI,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}

			var output bytes.Buffer
			var streamErr error
			for chunk := range result.Chunks {
				output.Write(chunk.Payload)
				if chunk.Err != nil {
					streamErr = chunk.Err
				}
			}
			if streamErr == nil {
				t.Fatalf("expected in-band SSE error, got nil; output=%s", output.String())
			}
			if !strings.Contains(streamErr.Error(), "upstream overloaded") {
				t.Fatalf("expected error message to mention upstream error, got %q", streamErr.Error())
			}
			if bytes.Contains(output.Bytes(), []byte("event: message_stop")) || bytes.Contains(output.Bytes(), []byte("[DONE]")) {
				t.Fatalf("in-band SSE error must not synthesize terminal events, got: %s", output.String())
			}

			record := waitKimiUsageRecord(t, capture)
			if !record.Failed {
				t.Fatalf("expected in-band SSE error to publish failure, got Failed=false")
			}
			if record.Fail.Body != "upstream stream returned an error payload" {
				t.Fatalf("expected sanitized usage failure body, got %q", record.Fail.Body)
			}
			diags := apiCapture.ResponseDiagnostics()
			if len(diags) != 1 || diags[0] == nil || diags[0].Error == nil {
				t.Fatalf("expected 1 recorded API response error diagnostic, got %+v", diags)
			}
			if gotDiag := diags[0].Error.Error(); gotDiag != "upstream stream returned an error payload" || strings.Contains(gotDiag, "upstream overloaded") {
				t.Fatalf("expected sanitized API response error log without upstream payload, got %q", gotDiag)
			}
		})
	}
}

func TestKimiExecutorExecuteStream_NormalCompletionPublishesSuccess(t *testing.T) {
	tests := []struct {
		name      string
		sendDone  bool
		wantEvent string
	}{
		{name: "with_finish_reason_and_done", sendDone: true, wantEvent: "event: message_stop"},
		{name: "with_finish_reason_without_done", sendDone: false, wantEvent: "event: message_stop"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := registerKimiUsageCapture(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-ok\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":4,\"total_tokens\":13}}\n\n")
				if tc.sendDone {
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				}
			}))
			defer server.Close()

			exec := NewKimiExecutor(&config.Config{})
			auth := &cliproxyauth.Auth{
				ID:       capture.authID,
				Provider: "kimi",
				Attributes: map[string]string{
					"api_key":  "test-key",
					"base_url": server.URL,
				},
			}
			result, errExecute := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "kimi-k2.5",
				Payload: []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatOpenAI,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}

			var output bytes.Buffer
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected stream error: %v", chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if !bytes.Contains(output.Bytes(), []byte(tc.wantEvent)) {
				t.Fatalf("expected %q in output, got: %s", tc.wantEvent, output.String())
			}

			record := waitKimiUsageRecord(t, capture)
			if record.Failed {
				t.Fatalf("expected completed stream to publish success, got Failed=true (%+v)", record.Fail)
			}
			if record.Detail.InputTokens != 9 || record.Detail.OutputTokens != 4 || record.Detail.TotalTokens != 13 {
				t.Fatalf("unexpected usage detail: %+v", record.Detail)
			}
		})
	}
}

func TestKimiExecutorExecuteStream_CancelAfterTranslatedTerminalPublishesSuccess(t *testing.T) {
	capture := registerKimiUsageCapture(t)
	releaseUpstream := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// OpenAI->Claude translator emits message_stop once the trailing usage chunk
		// arrives after finish_reason, even before upstream sends [DONE].
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-term\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2.5\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-term\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2.5\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":5,\"total_tokens\":16}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-releaseUpstream:
		}
	}))
	defer func() {
		close(releaseUpstream)
		server.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exec := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       capture.authID,
		Provider: "kimi",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5",
		Payload: []byte(`{"model":"kimi-k2.5","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAI,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	canceled := false
	for !canceled {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("stream closed before event: message_stop")
			}
			if bytes.Contains(chunk.Payload, []byte("event: message_stop")) {
				cancel()
				canceled = true
			}
		case <-timer.C:
			t.Fatal("timed out waiting for event: message_stop")
		}
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error after message_stop delivery: %v", chunk.Err)
		}
	}

	record := waitKimiUsageRecord(t, capture)
	if record.Failed {
		t.Fatalf("expected cancel after translated message_stop to publish success, got Failed=true (%+v)", record.Fail)
	}
	if record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 5 || record.Detail.TotalTokens != 16 {
		t.Fatalf("unexpected usage detail: %+v", record.Detail)
	}
}

func TestKimiExecutorExecuteResponsesStream_CancelAfterTerminalBlankLinePublishesSuccess(t *testing.T) {
	capture := registerKimiUsageCapture(t)
	releaseUpstream := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-ok\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":14,\"output_tokens\":6,\"total_tokens\":20}}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-releaseUpstream:
		}
	}))
	defer func() {
		close(releaseUpstream)
		server.Close()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	exec := NewKimiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       capture.authID,
		Provider: "kimi",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	result, errExecute := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "kimi-k2.5",
		Payload: []byte(`{"model":"kimi-k2.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	sawTerminalData := false
	canceled := false
	for !canceled {
		select {
		case chunk, ok := <-result.Chunks:
			if !ok {
				t.Fatal("stream closed before terminating blank SSE line")
			}
			if bytes.Contains(chunk.Payload, []byte(`"type":"response.completed"`)) {
				sawTerminalData = true
				continue
			}
			if sawTerminalData && string(chunk.Payload) == "\n" {
				cancel()
				canceled = true
			}
		case <-timer.C:
			t.Fatal("timed out waiting for terminating blank SSE line")
		}
	}

	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error after terminal frame delivery: %v", chunk.Err)
		}
	}

	record := waitKimiUsageRecord(t, capture)
	if record.Failed {
		t.Fatalf("expected cancel after terminal frame delivery to publish success, got Failed=true (%+v)", record.Fail)
	}
	if record.Detail.InputTokens != 14 || record.Detail.OutputTokens != 6 || record.Detail.TotalTokens != 20 {
		t.Fatalf("unexpected usage detail: %+v", record.Detail)
	}
}
