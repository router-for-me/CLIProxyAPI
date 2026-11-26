package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/clienterror"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

type issue8223UsageCapture struct {
	authID  string
	records chan usage.Record
}

func (c *issue8223UsageCapture) HandleUsage(_ context.Context, record usage.Record) {
	if c == nil || record.AuthID != c.authID {
		return
	}
	select {
	case c.records <- record:
	default:
	}
}

type issue8223NoopUsagePlugin struct{}

func (issue8223NoopUsagePlugin) HandleUsage(context.Context, usage.Record) {}

func registerIssue8223UsageCapture(t *testing.T, authID string) *issue8223UsageCapture {
	t.Helper()
	capture := &issue8223UsageCapture{
		authID:  authID,
		records: make(chan usage.Record, 2),
	}
	usage.RegisterNamedPlugin(t.Name(), capture)
	t.Cleanup(func() {
		usage.RegisterNamedPlugin(t.Name(), issue8223NoopUsagePlugin{})
	})
	return capture
}

func waitIssue8223UsageRecord(t *testing.T, capture *issue8223UsageCapture) usage.Record {
	t.Helper()
	select {
	case record := <-capture.records:
		return record
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for usage record")
		return usage.Record{}
	}
}

type issue8223CallbackReadCloser struct {
	mu       sync.Mutex
	reads    []func([]byte) (int, error)
	index    int
	closed   bool
	onClosed chan struct{}
}

func (r *issue8223CallbackReadCloser) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.index >= len(r.reads) {
		r.mu.Unlock()
		return 0, io.EOF
	}
	fn := r.reads[r.index]
	r.index++
	r.mu.Unlock()
	return fn(p)
}

func (r *issue8223CallbackReadCloser) Close() error {
	r.mu.Lock()
	if !r.closed {
		r.closed = true
		if r.onClosed != nil {
			close(r.onClosed)
		}
	}
	r.mu.Unlock()
	return nil
}

func TestXAIStream_CancelAfterUsageBeforeTerminalDeliveredPublishesFailure(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         t.Name(),
		Provider:   "xai",
		Attributes: map[string]string{"base_url": "https://xai.example/v1"},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":4,\"total_tokens\":16}}}\n\n"

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, sse), nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, r.Context().Err()
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	result, errExec := NewXAIExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	firstChunk, ok := <-result.Chunks
	if !ok || firstChunk.Err != nil {
		t.Fatalf("expected first event line chunk, got ok=%v chunk=%+v", ok, firstChunk)
	}
	if !strings.Contains(string(firstChunk.Payload), "event: response.completed") {
		t.Fatalf("unexpected first chunk payload = %q", string(firstChunk.Payload))
	}
	cancel()

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected canceled stream to be marked failed, got record=%+v", record)
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
	if record.Detail.InputTokens != 12 || record.Detail.OutputTokens != 4 {
		t.Fatalf("reported usage = %+v, want input 12 output 4", record.Detail)
	}
}

func TestXAIStream_CancelAfterTerminalDeliveredIsNotFailed(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         t.Name(),
		Provider:   "xai",
		Attributes: map[string]string{"base_url": "https://xai.example/v1"},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "event: response.completed\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hi\"}]}],\"usage\":{\"input_tokens\":12,\"output_tokens\":4,\"total_tokens\":16}}}\n\n"

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, sse), nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, r.Context().Err()
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	result, errExec := NewXAIExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	canceled := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error before cancellation: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), `"type":"response.completed"`) {
			canceled = true
			cancel()
			break
		}
	}
	if !canceled {
		t.Fatal("stream ended before response.completed payload was delivered")
	}

	record := waitIssue8223UsageRecord(t, capture)
	if record.Failed {
		t.Fatalf("expected post-terminal cancel to succeed, got failed record=%+v", record)
	}
	if record.Detail.InputTokens != 12 || record.Detail.OutputTokens != 4 {
		t.Fatalf("reported usage = %+v, want input 12 output 4", record.Detail)
	}
}

func TestXAIStream_CleanEOFBeforeTerminalEventIsFailed(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         t.Name(),
		Provider:   "xai",
		Attributes: map[string]string{"base_url": "https://xai.example/v1"},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"partial\"}\n\n"
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}))

	result, errExec := NewXAIExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if streamErr == nil {
		t.Fatal("clean EOF without terminal event did not emit a stream error")
	}
	statusCodeErr, okStatus := streamErr.(interface{ StatusCode() int })
	if !okStatus || statusCodeErr.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error = %v, want HTTP 502 statusErr", streamErr)
	}
	truncatedErr, okTruncated := streamErr.(cliproxyexecutor.StreamTruncatedError)
	if !okTruncated || !truncatedErr.IsStreamTruncated() {
		t.Fatalf("stream error = %T (%v), want IsStreamTruncated() = true", streamErr, streamErr)
	}

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected truncated stream to be marked failed, got record=%+v", record)
	}
	if record.Fail.StatusCode != http.StatusBadGateway {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, http.StatusBadGateway)
	}
	if record.Fail.Code != usage.FailureStreamTruncatedCode {
		t.Fatalf("Fail.Code = %q, want %q", record.Fail.Code, usage.FailureStreamTruncatedCode)
	}
}

func TestMetaStream_CancelAfterUsageBeforeTerminalDeliveredPublishesFailure(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "meta",
		Attributes: map[string]string{
			"api_key":  "meta-token",
			"base_url": "https://meta.example/v1",
		},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "event: response.completed\n" + metaTerminalDisconnectCompleted

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, sse), nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, r.Context().Err()
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	result, errExec := NewMetaExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "muse-wonder-1.0",
		Payload: []byte(`{"model":"muse-wonder-1.0","input":"hello","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	firstChunk, ok := <-result.Chunks
	if !ok || firstChunk.Err != nil {
		t.Fatalf("expected first event line chunk, got ok=%v chunk=%+v", ok, firstChunk)
	}
	if len(firstChunk.Payload) == 0 {
		t.Fatal("expected non-empty first chunk payload before cancellation")
	}
	cancel()

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected canceled Meta stream to be marked failed, got record=%+v", record)
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
	if record.Detail.InputTokens != 10 || record.Detail.OutputTokens != 5 {
		t.Fatalf("reported usage = %+v, want input 10 output 5", record.Detail)
	}
}

func TestOpenAICompatStream_CancelAfterUsagePublishesFailure(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     "https://compat.example/v1",
			"api_key":      "compat-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":7,\"completion_tokens\":3,\"total_tokens\":10}}\n\n" +
		"data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" more\"}}]}\n\n"

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, sse), nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, r.Context().Err()
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	result, errExec := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	firstChunk, ok := <-result.Chunks
	if !ok || firstChunk.Err != nil {
		t.Fatalf("expected first chat chunk, got ok=%v chunk=%+v", ok, firstChunk)
	}
	cancel()

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected canceled openai-compat stream to be marked failed, got record=%+v", record)
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
	if record.Detail.InputTokens != 7 || record.Detail.OutputTokens != 3 {
		t.Fatalf("reported usage = %+v, want input 7 output 3", record.Detail)
	}
}

func TestOpenAICompatImageStream_CancelMidStreamPublishesFailure(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     "https://compat.example/v1",
			"api_key":      "compat-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	firstRead := make(chan struct{})
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		chunk := "data: {\"type\":\"image_generation.partial_image\",\"model\":\"gpt-image-1\",\"b64_json\":\"AA==\"}\n\n"
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					n := copy(p, chunk)
					close(firstRead)
					return n, nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, r.Context().Err()
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	_, errExec := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-image-1",
		Payload: []byte(`{"model":"gpt-image-1","prompt":"sunset"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	<-firstRead
	cancel()

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected canceled openai-compat image stream to be marked failed, got record=%+v", record)
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
}

func TestCopilotStream_CancelBeforeTerminalOnCleanEOFPublishesFailure(t *testing.T) {
	const model = "claude-opus-5"
	auth := copilotTestAuth(t, t.Name(), model, copilot.Chat)
	capture := registerIssue8223UsageCapture(t, auth.ID)

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		delta := "data: {\"id\":\"chat-1\",\"model\":\"claude-opus-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2,\"total_tokens\":11}}\n\n"
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, delta), nil
				},
				func(_ []byte) (int, error) {
					cancel()
					return 0, io.EOF
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	result, errExec := NewCopilotExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   copilot.ModelPrefix + model,
		Payload: []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	for range result.Chunks {
	}

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected canceled copilot stream to be marked failed, got record=%+v", record)
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
	if record.Detail.InputTokens != 9 || record.Detail.OutputTokens != 2 {
		t.Fatalf("reported usage = %+v, want input 9 output 2", record.Detail)
	}
}

func TestCopilotStream_CancelDuringEmitBeforeTerminalPublishesFailure(t *testing.T) {
	const model = "claude-opus-5"
	auth := copilotTestAuth(t, t.Name(), model, copilot.Chat)
	capture := registerIssue8223UsageCapture(t, auth.ID)

	firstRead := make(chan struct{})
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		delta := "data: {\"id\":\"chat-1\",\"model\":\"claude-opus-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":1,\"total_tokens\":7}}\n\n"
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					n := copy(p, delta)
					close(firstRead)
					return n, nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, r.Context().Err()
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	_, errExec := NewCopilotExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   copilot.ModelPrefix + model,
		Payload: []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	<-firstRead
	cancel()

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected canceled copilot stream during emit to be marked failed, got record=%+v", record)
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
	if record.Detail.InputTokens != 6 || record.Detail.OutputTokens != 1 {
		t.Fatalf("reported usage = %+v, want input 6 output 1", record.Detail)
	}
}

func TestXAIStream_UpstreamErrorEventDoesNotEmitTruncatedError(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         t.Name(),
		Provider:   "xai",
		Attributes: map[string]string{"base_url": "https://xai.example/v1"},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "event: response.failed\n" +
		"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_err\",\"status\":\"failed\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"rate_limit_exceeded\",\"message\":\"Too many requests\"}}}\n\n"
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}))

	result, errExec := NewXAIExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","input":"hi","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAIResponse,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	var errs []error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			errs = append(errs, chunk.Err)
		}
	}
	if len(errs) != 1 {
		t.Fatalf("expected exactly 1 upstream error chunk, got %d: %v", len(errs), errs)
	}
	if truncatedErr, okTrunc := errs[0].(cliproxyexecutor.StreamTruncatedError); okTrunc && truncatedErr.IsStreamTruncated() {
		t.Fatalf("upstream failure was misclassified as stream truncated: %v", errs[0])
	}
	statusCodeErr, okStatus := errs[0].(interface{ StatusCode() int })
	if !okStatus || statusCodeErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want HTTP 429", errs[0])
	}

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected failed usage record, got %+v", record)
	}
	if record.Fail.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, http.StatusTooManyRequests)
	}
	if record.Fail.Code == usage.FailureStreamTruncatedCode {
		t.Fatalf("Fail.Code = %q, must not be stream_truncated", record.Fail.Code)
	}
}

func TestXAIStream_ResponseDoneTranslatesToOpenAIChat(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:         t.Name(),
		Provider:   "xai",
		Attributes: map[string]string{"base_url": "https://xai.example/v1"},
		Metadata:   map[string]any{"access_token": "xai-token"},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "event: response.created\n" +
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_done\",\"model\":\"grok-4.5\"}}\n\n" +
		"event: response.output_text.delta\n" +
		"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n" +
		"event: response.done\n" +
		"data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_done\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"grok-4.5\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":8,\"output_tokens\":3,\"total_tokens\":11}}}\n\n"

	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       io.NopCloser(strings.NewReader(sse)),
		}, nil
	}))

	result, errExec := NewXAIExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "grok-4.5",
		Payload: []byte(`{"model":"grok-4.5","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	var output strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}
	if !strings.Contains(output.String(), `"finish_reason":"stop"`) {
		t.Fatalf("translated OpenAI Chat stream missing finish_reason: %s", output.String())
	}

	record := waitIssue8223UsageRecord(t, capture)
	if record.Failed {
		t.Fatalf("expected response.done translated stream to succeed, got failed record=%+v", record)
	}
	if record.Detail.InputTokens != 8 || record.Detail.OutputTokens != 3 {
		t.Fatalf("reported usage = %+v, want input 8 output 3", record.Detail)
	}
}

func TestOpenAICompatStream_ClaudeCancelWithCauseAfterMessageStopBeforeDoneIsNotFailed(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     "https://compat.example/v1",
			"api_key":      "compat-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4,\"total_tokens\":15}}\n\n"

	customCancelCause := errors.New("client closed connection after message_stop")
	baseCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, sse), nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, context.Cause(r.Context())
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	result, errExec := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"model":"gpt-4o","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	canceled := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error before cancellation: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), "message_stop") {
			canceled = true
			cancel(customCancelCause)
			break
		}
	}
	if !canceled {
		t.Fatal("stream ended before message_stop was delivered")
	}

	record := waitIssue8223UsageRecord(t, capture)
	if record.Failed {
		t.Fatalf("expected custom-cause cancel after message_stop to succeed, got failed record=%+v", record)
	}
	if record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 4 {
		t.Fatalf("reported usage = %+v, want input 11 output 4", record.Detail)
	}
}

func TestOpenAICompatImageStream_CancelWithCauseAfterCompletedEventBeforeEOFIsNotFailed(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     "https://compat.example/v1",
			"api_key":      "compat-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	customCancelCause := errors.New("client closed connection after image_generation.completed")
	baseCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		completed := "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"model\":\"gpt-image-1\",\"b64_json\":\"AA==\"}\n\n"
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, completed), nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, context.Cause(r.Context())
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	result, errExec := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-image-1",
		Payload: []byte(`{"model":"gpt-image-1","prompt":"sunset"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	chunk, ok := <-result.Chunks
	if !ok || chunk.Err != nil {
		t.Fatalf("expected completed image chunk, got ok=%v chunk=%+v", ok, chunk)
	}
	if !strings.Contains(string(chunk.Payload), "image_generation.completed") {
		t.Fatalf("unexpected chunk payload = %q", string(chunk.Payload))
	}
	cancel(customCancelCause)

	record := waitIssue8223UsageRecord(t, capture)
	if record.Failed {
		t.Fatalf("expected custom-cause cancel after image_generation.completed to succeed, got failed record=%+v", record)
	}
}

func TestOpenAICompatStream_ClaudeCancelAfterMessageStopBeforeDoneIsNotFailed(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     "https://compat.example/v1",
			"api_key":      "compat-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	sse := "data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n" +
		"data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chat-1\",\"model\":\"gpt-4o\",\"choices\":[],\"usage\":{\"prompt_tokens\":11,\"completion_tokens\":4,\"total_tokens\":15}}\n\n"

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, sse), nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, r.Context().Err()
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	result, errExec := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-4o",
		Payload: []byte(`{"model":"gpt-4o","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	canceled := false
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error before cancellation: %v", chunk.Err)
		}
		if strings.Contains(string(chunk.Payload), "message_stop") {
			canceled = true
			cancel()
			break
		}
	}
	if !canceled {
		t.Fatal("stream ended before message_stop was delivered")
	}

	record := waitIssue8223UsageRecord(t, capture)
	if record.Failed {
		t.Fatalf("expected cancel after message_stop to succeed, got failed record=%+v", record)
	}
	if record.Detail.InputTokens != 11 || record.Detail.OutputTokens != 4 {
		t.Fatalf("reported usage = %+v, want input 11 output 4", record.Detail)
	}
}

func TestOpenAICompatImageStream_CancelAfterCompletedEventBeforeEOFIsNotFailed(t *testing.T) {
	auth := &cliproxyauth.Auth{
		ID:       t.Name(),
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"base_url":     "https://compat.example/v1",
			"api_key":      "compat-key",
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}
	capture := registerIssue8223UsageCapture(t, auth.ID)

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		completed := "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"model\":\"gpt-image-1\",\"b64_json\":\"AA==\"}\n\n"
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, completed), nil
				},
				func(_ []byte) (int, error) {
					<-r.Context().Done()
					return 0, io.EOF
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
	})
	result, errExec := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-image-1",
		Payload: []byte(`{"model":"gpt-image-1","prompt":"sunset"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	chunk, ok := <-result.Chunks
	if !ok || chunk.Err != nil {
		t.Fatalf("expected completed image chunk, got ok=%v chunk=%+v", ok, chunk)
	}
	if !strings.Contains(string(chunk.Payload), "image_generation.completed") {
		t.Fatalf("unexpected chunk payload = %q", string(chunk.Payload))
	}
	cancel()

	record := waitIssue8223UsageRecord(t, capture)
	if record.Failed {
		t.Fatalf("expected cancel after image_generation.completed to succeed, got failed record=%+v", record)
	}
}

func TestCopilotStream_CancelAfterFinishReasonOnEOFPublishesFailure(t *testing.T) {
	const model = "claude-opus-5"
	auth := copilotTestAuth(t, t.Name(), model, copilot.Chat)
	capture := registerIssue8223UsageCapture(t, auth.ID)

	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := context.WithValue(baseCtx, "cliproxy.roundtripper", roundTripFunc(func(r *http.Request) (*http.Response, error) {
		finishChunk := "data: {\"id\":\"chat-1\",\"model\":\"claude-opus-5\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n"
		body := &issue8223CallbackReadCloser{
			reads: []func([]byte) (int, error){
				func(p []byte) (int, error) {
					return copy(p, finishChunk), nil
				},
				func(_ []byte) (int, error) {
					cancel()
					return 0, io.EOF
				},
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"text/event-stream"}},
			Request:    r,
			Body:       body,
		}, nil
	}))

	result, errExec := NewCopilotExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   copilot.ModelPrefix + model,
		Payload: []byte(`{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream() error = %v", errExec)
	}

	for range result.Chunks {
	}

	record := waitIssue8223UsageRecord(t, capture)
	if !record.Failed {
		t.Fatalf("expected canceled EOF after finish_reason to be marked failed, got record=%+v", record)
	}
	if record.Fail.StatusCode != clienterror.StatusClientClosedRequest {
		t.Fatalf("Fail.StatusCode = %d, want %d", record.Fail.StatusCode, clienterror.StatusClientClosedRequest)
	}
}
