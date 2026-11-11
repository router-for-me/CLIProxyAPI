package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func devinContentFrame(text string) []byte {
	return appendDevinFieldBytes(nil, 3, []byte(text))
}

func devinUsageFrame(prompt, completion uint64) []byte {
	var usage []byte
	usage = appendVarintField(usage, 2, prompt)
	usage = appendVarintField(usage, 3, completion)
	return appendDevinFieldBytes(nil, 7, usage)
}

func devinStopFrame(reason uint64) []byte {
	return appendVarintField(nil, 5, reason)
}

func devinConnectStream(frames ...[]byte) []byte {
	var buf bytes.Buffer
	for _, frame := range frames {
		buf.Write(helps.WrapConnectEnvelope(frame))
	}
	buf.Write(helps.WrapConnectEnvelopeWithFlag(helps.ConnectFlagEndStream, []byte(`{}`)))
	return buf.Bytes()
}

func devinInteractionsRequest() (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	payload := []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`)
	req := cliproxyexecutor.Request{Model: "devin/swe-2", Payload: payload}
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatInteractions,
		ResponseFormat:  sdktranslator.FormatInteractions,
		OriginalRequest: payload,
		Stream:          true,
	}
	return req, opts
}

func assertDevinRetryableFailure(t *testing.T, err error, wantMessage string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want retryable upstream failure")
	}
	var statusCoder cliproxyexecutor.StatusError
	if !errors.As(err, &statusCoder) || statusCoder.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %v, want 502; err=%v", statusCoder, err)
	}
	if !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("error = %q, want substring %q", err.Error(), wantMessage)
	}
	var scoped cliproxyexecutor.RequestScopedError
	if errors.As(err, &scoped) && scoped.IsRequestScoped() {
		t.Fatalf("error %T is request-scoped and will not fail over", err)
	}
	var truncated cliproxyexecutor.StreamTruncatedError
	if errors.As(err, &truncated) && truncated.IsStreamTruncated() {
		t.Fatalf("error %T is stream-truncated and will not fail over", err)
	}
}

func TestConsumeDevinFramesEmptyCompletedTurnIsRetryable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		body []byte
		want string
	}{
		{name: "eos only", body: devinConnectStream(), want: devinEmptyCompletionMessage},
		{name: "explicit zero usage", body: devinConnectStream(devinUsageFrame(0, 0)), want: devinEmptyCompletionMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, respLog, err := consumeDevinFramesToInteractions(bytes.NewReader(tc.body), "devin/swe-2", "swe-2-high")
			assertDevinRetryableFailure(t, err, tc.want)
			if respLog == nil || respLog.Status != "empty_completion" {
				t.Fatalf("respLog = %+v, want empty_completion", respLog)
			}
		})
	}
}

func TestConsumeDevinFramesZeroOutputWithContentSucceeds(t *testing.T) {
	t.Parallel()

	body := devinConnectStream(devinContentFrame("ready"), devinUsageFrame(4, 0))
	out, respLog, err := consumeDevinFramesToInteractions(bytes.NewReader(body), "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consume error = %v, want success", err)
	}
	if respLog == nil || respLog.Status != "completed" {
		t.Fatalf("respLog status = %+v, want completed", respLog)
	}
	if !bytes.Contains(out, []byte("ready")) {
		t.Fatalf("payload = %s, want generated text", out)
	}
}

func TestConsumeDevinFramesIncompleteWithoutOutputStaysIncomplete(t *testing.T) {
	t.Parallel()

	// MAX_TOKENS is an incomplete terminal state, not the phantom completed turn.
	body := devinConnectStream(devinStopFrame(3))
	out, _, err := consumeDevinFramesToInteractions(bytes.NewReader(body), "devin/swe-2", "swe-2-high")
	if err != nil {
		t.Fatalf("consume error = %v, want incomplete success", err)
	}
	if !bytes.Contains(out, []byte(`"status":"incomplete"`)) {
		t.Fatalf("payload = %s, want incomplete status", out)
	}
}

func TestStreamDevinFramesEmptyCompletedTurnIsRetryable(t *testing.T) {
	t.Parallel()

	exec := &DevinExecutor{}
	out := make(chan cliproxyexecutor.StreamChunk, 8)
	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			bytes.NewReader(devinConnectStream(devinUsageFrame(0, 0))),
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions},
			"swe-2-high",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	var payloads [][]byte
	var streamErr error
	for chunk := range out {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		if len(chunk.Payload) > 0 {
			payloads = append(payloads, chunk.Payload)
		}
	}
	assertDevinRetryableFailure(t, streamErr, devinEmptyCompletionMessage)
	for _, payload := range payloads {
		if bytes.Contains(payload, []byte("interaction.completed")) || bytes.Contains(payload, []byte("interaction.created")) {
			t.Fatalf("empty turn emitted client payload before the error: %s", payload)
		}
	}
}

func TestStreamDevinFramesIdleWatchdogAbortsDanglingStream(t *testing.T) {
	t.Parallel()

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })

	exec := &DevinExecutor{streamIdleTimeout: 200 * time.Millisecond}
	out := make(chan cliproxyexecutor.StreamChunk, 4)
	go func() {
		defer close(out)
		exec.streamDevinFrames(
			context.Background(),
			reader,
			cliproxyexecutor.Request{Model: "devin/swe-2"},
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatInteractions},
			"swe-2-high",
			sdktranslator.FormatInteractions,
			nil,
			out,
		)
	}()

	select {
	case chunk, ok := <-out:
		if !ok {
			t.Fatal("stream closed without an idle error")
		}
		assertDevinRetryableFailure(t, chunk.Err, devinStreamIdleMessage)
		if len(chunk.Payload) > 0 {
			t.Fatalf("idle abort emitted payload %s", chunk.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dangling connect stream was not aborted")
	}
}

func TestDevinExecutorExecuteEmptyTurnFailsOver(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		w.Header().Set("Content-Type", "application/connect+proto")
		if strings.Contains(r.Header.Get("Authorization"), "stall-key") {
			_, _ = w.Write(devinConnectStream(devinUsageFrame(0, 0)))
			return
		}
		_, _ = w.Write(devinConnectStream(devinContentFrame("ready"), devinUsageFrame(3, 1)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(NewDevinExecutor(nil))

	const model = "devin/swe-2"
	register := func(id, key string) {
		t.Helper()
		auth := &cliproxyauth.Auth{
			ID:       id,
			Provider: "devin",
			Status:   cliproxyauth.StatusActive,
			Attributes: map[string]string{
				"api_key":  key,
				"base_url": server.URL,
			},
		}
		if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register(%s) error = %v", id, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "devin", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	register("a-devin-empty", "stall-key")
	register("b-devin-ok", "ok-key")

	req, opts := devinInteractionsRequest()
	opts.Stream = false
	resp, err := manager.Execute(context.Background(), []string{"devin"}, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v, want failover success", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
	if !bytes.Contains(resp.Payload, []byte("ready")) {
		t.Fatalf("payload = %s, want recovered text", resp.Payload)
	}
}

func TestDevinExecutorExecuteStreamEmptyTurnFailsOver(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		calls.Add(1)
		w.Header().Set("Content-Type", "application/connect+proto")
		if strings.Contains(r.Header.Get("Authorization"), "stall-key") {
			_, _ = w.Write(devinConnectStream(devinUsageFrame(0, 0)))
			return
		}
		_, _ = w.Write(devinConnectStream(devinContentFrame("ready"), devinUsageFrame(3, 1)))
	}))
	defer server.Close()

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(NewDevinExecutor(nil))

	const model = "devin/swe-2"
	register := func(id, key string) {
		t.Helper()
		auth := &cliproxyauth.Auth{
			ID:       id,
			Provider: "devin",
			Status:   cliproxyauth.StatusActive,
			Attributes: map[string]string{
				"api_key":  key,
				"base_url": server.URL,
			},
		}
		if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register(%s) error = %v", id, errReg)
		}
		registry.GetGlobalRegistry().RegisterClient(id, "devin", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	register("a-devin-stream-empty", "stall-key")
	register("b-devin-stream-ok", "ok-key")

	req, opts := devinInteractionsRequest()
	result, err := manager.ExecuteStream(context.Background(), []string{"devin"}, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v, want failover stream", err)
	}
	var buf bytes.Buffer
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error after failover = %v", chunk.Err)
		}
		buf.Write(chunk.Payload)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
	if !bytes.Contains(buf.Bytes(), []byte("ready")) {
		t.Fatalf("stream = %s, want recovered text", buf.Bytes())
	}
}

func TestDevinExecutorExecuteStreamIdleAborts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/connect+proto")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	exec := NewDevinExecutor(nil)
	exec.streamIdleTimeout = 200 * time.Millisecond
	auth := &cliproxyauth.Auth{
		ID:       "devin-idle",
		Provider: "devin",
		Attributes: map[string]string{
			"api_key":  "idle-key",
			"base_url": server.URL,
		},
	}
	req, opts := devinInteractionsRequest()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	started := time.Now()
	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed without an idle error")
		}
		assertDevinRetryableFailure(t, chunk.Err, devinStreamIdleMessage)
		if time.Since(started) > 2*time.Second {
			t.Fatalf("idle abort took %s, want the frame watchdog rather than the request deadline", time.Since(started))
		}
	case <-ctx.Done():
		t.Fatal("dangling connect stream waited until the request context ended")
	}
}
