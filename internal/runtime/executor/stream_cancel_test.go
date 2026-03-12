package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

type streamCancelableExecutor interface {
	ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
}

type streamExecutorCase struct {
	name  string
	exec  func(baseURL string) streamCancelableExecutor
	auth  func(baseURL string) *cliproxyauth.Auth
	model string
}

func TestStreamExecutorsCancelUpstreamBeforeFirstChunk(t *testing.T) {
	t.Parallel()
	for _, execCase := range streamExecutorCases() {
		execCase := execCase
		t.Run(execCase.name, func(t *testing.T) {
			rt := newStreamingRoundTripper(streamScenarioBeforeFirst)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			result := mustExecuteStream(t, execCase, "https://example.test", ctx)

			select {
			case <-rt.started:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for upstream stream start")
			}

			cancel()
			waitForUpstreamCanceled(t, rt.upstreamCanceled)
			waitForStreamClosed(t, result.Chunks)
		})
	}
}

func TestStreamExecutorsCancelUpstreamAfterFirstChunk(t *testing.T) {
	t.Parallel()
	for _, execCase := range streamExecutorCases() {
		execCase := execCase
		t.Run(execCase.name, func(t *testing.T) {
			rt := newStreamingRoundTripper(streamScenarioAfterFirst)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			result := mustExecuteStream(t, execCase, "https://example.test", ctx)

			select {
			case <-rt.firstChunkWritten:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out waiting for first chunk to be written")
			}

			cancel()
			waitForUpstreamCanceled(t, rt.upstreamCanceled)
			waitForStreamClosed(t, result.Chunks)
		})
	}
}

func TestStreamExecutorsCancelUpstreamMidStream(t *testing.T) {
	t.Parallel()
	for _, execCase := range streamExecutorCases() {
		execCase := execCase
		t.Run(execCase.name, func(t *testing.T) {
			rt := newStreamingRoundTripper(streamScenarioMidStream)
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", rt)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			result := mustExecuteStream(t, execCase, "https://example.test", ctx)

			for index := 0; index < 2; index++ {
				_ = readNonEmptyChunkWithTimeout(t, result.Chunks)
			}

			deadline := time.Now().Add(2 * time.Second)
			for rt.writes.Load() < 3 {
				if time.Now().After(deadline) {
					t.Fatalf("expected at least 3 upstream writes before cancel, got %d", rt.writes.Load())
				}
				time.Sleep(10 * time.Millisecond)
			}

			cancel()
			waitForUpstreamCanceled(t, rt.upstreamCanceled)
			waitForStreamClosed(t, result.Chunks)
		})
	}
}

func streamExecutorCases() []streamExecutorCase {
	return []streamExecutorCase{
		{
			name: "openai_compat",
			exec: func(baseURL string) streamCancelableExecutor {
				return NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			},
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{Attributes: map[string]string{
					"base_url": baseURL + "/v1",
					"api_key":  "test",
				}}
			},
			model: "gpt-4o-mini",
		},
		{
			name: "kimi",
			exec: func(baseURL string) streamCancelableExecutor {
				return NewKimiExecutor(&config.Config{})
			},
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{Attributes: map[string]string{
					"access_token": "test",
				}}
			},
			model: "kimi-k2",
		},
		{
			name: "qwen",
			exec: func(baseURL string) streamCancelableExecutor {
				return NewQwenExecutor(&config.Config{})
			},
			auth: func(baseURL string) *cliproxyauth.Auth {
				return &cliproxyauth.Auth{Attributes: map[string]string{
					"base_url": baseURL + "/v1",
					"api_key":  "test",
				}}
			},
			model: "qwen-max",
		},
	}
}

func mustExecuteStream(t *testing.T, execCase streamExecutorCase, baseURL string, ctx context.Context) *cliproxyexecutor.StreamResult {
	t.Helper()
	result, err := execCase.exec(baseURL).ExecuteStream(ctx, execCase.auth(baseURL), cliproxyexecutor.Request{
		Model:   execCase.model,
		Payload: []byte(`{"model":"` + execCase.model + `","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	return result
}

func writeSSEChunk(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	if _, err := w.Write([]byte(`data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"` + content + `"}}]}` + "\n\n")); err != nil {
		t.Fatalf("write SSE chunk: %v", err)
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

type streamScenario string

const (
	streamScenarioBeforeFirst streamScenario = "before_first"
	streamScenarioAfterFirst  streamScenario = "after_first"
	streamScenarioMidStream   streamScenario = "mid_stream"
)

type streamingRoundTripper struct {
	scenario          streamScenario
	started           chan struct{}
	firstChunkWritten chan struct{}
	upstreamCanceled  chan struct{}
	writes            atomic.Int32
	cancelOnce        sync.Once
	startOnce         sync.Once
	firstOnce         sync.Once
}

func newStreamingRoundTripper(scenario streamScenario) *streamingRoundTripper {
	return &streamingRoundTripper{
		scenario:          scenario,
		started:           make(chan struct{}),
		firstChunkWritten: make(chan struct{}),
		upstreamCanceled:  make(chan struct{}),
	}
}

func (rt *streamingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	pr, pw := io.Pipe()
	rt.startOnce.Do(func() { close(rt.started) })
	go func() {
		defer func() {
			rt.cancelOnce.Do(func() { close(rt.upstreamCanceled) })
			_ = pw.Close()
		}()
		switch rt.scenario {
		case streamScenarioBeforeFirst:
			<-req.Context().Done()
			return
		case streamScenarioAfterFirst:
			rt.writeChunk(req.Context(), pw, "first")
			<-req.Context().Done()
			return
		case streamScenarioMidStream:
			ticker := time.NewTicker(15 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-req.Context().Done():
					return
				case <-ticker.C:
					count := rt.writes.Add(1)
					if !rt.writeChunk(req.Context(), pw, fmt.Sprintf("chunk-%d", count)) {
						return
					}
				}
			}
		default:
			<-req.Context().Done()
		}
	}()
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       pr,
		Request:    req,
	}
	resp.Header.Set("Content-Type", "text/event-stream")
	return resp, nil
}

func (rt *streamingRoundTripper) writeChunk(ctx context.Context, w *io.PipeWriter, content string) bool {
	rt.firstOnce.Do(func() { close(rt.firstChunkWritten) })
	if _, err := io.WriteString(w, `data: {"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":"`+content+`"}}]}`+"\n\n"); err != nil {
		select {
		case <-ctx.Done():
			return false
		default:
			return false
		}
	}
	return true
}

func readNonEmptyChunkWithTimeout(t *testing.T, ch <-chan cliproxyexecutor.StreamChunk) cliproxyexecutor.StreamChunk {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				t.Fatal("expected stream chunk, got closed channel")
			}
			if chunk.Err != nil {
				t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
			}
			if len(chunk.Payload) == 0 {
				if time.Now().After(deadline) {
					t.Fatal("timed out waiting for non-empty stream chunk")
				}
				continue
			}
			return chunk
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for stream chunk")
			return cliproxyexecutor.StreamChunk{}
		}
	}
}

func waitForUpstreamCanceled(t *testing.T, canceled <-chan struct{}) {
	t.Helper()
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for upstream cancellation")
	}
}

func waitForStreamClosed(t *testing.T, ch <-chan cliproxyexecutor.StreamChunk) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			t.Fatal("timed out waiting for stream close")
		}
		select {
		case _, ok := <-ch:
			if !ok {
				return
			}
		case <-time.After(remaining):
			t.Fatal("timed out waiting for stream close")
		}
	}
}
