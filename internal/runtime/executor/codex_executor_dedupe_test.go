package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

func TestCodexExecutorCompactDedupesConcurrentInFlightRequests(t *testing.T) {
	var upstreamCalls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamCalls.Add(1) == 1 {
			close(started)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "auth-1",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	request := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_key":"cache-key","input":"hello"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	}

	results := make([]cliproxyexecutor.Response, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], errs[0] = executor.Execute(context.Background(), auth, request, opts)
	}()

	<-started

	wg.Add(1)
	go func() {
		defer wg.Done()
		results[1], errs[1] = executor.Execute(context.Background(), auth, request, opts)
	}()

	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Execute(%d) error: %v", i, err)
		}
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", upstreamCalls.Load())
	}
	if string(results[0].Payload) != string(results[1].Payload) {
		t.Fatalf("payload mismatch:\nfirst=%s\nsecond=%s", string(results[0].Payload), string(results[1].Payload))
	}
}

func TestCodexExecutorCompactDedupeSeparatesPromptCacheKeys(t *testing.T) {
	var upstreamCalls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upstreamCalls.Add(1) == 1 {
			close(started)
			<-release
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID: "auth-1",
		Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	}

	first := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_key":"cache-key-1","input":"hello"}`),
	}
	second := cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","prompt_cache_key":"cache-key-2","input":"hello"}`),
	}

	errs := make([]error, 2)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errs[0] = executor.Execute(context.Background(), auth, first, opts)
	}()

	<-started

	wg.Add(1)
	go func() {
		defer wg.Done()
		_, errs[1] = executor.Execute(context.Background(), auth, second, opts)
	}()

	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Execute(%d) error: %v", i, err)
		}
	}
	if upstreamCalls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", upstreamCalls.Load())
	}
}

func TestCollectCodexResponseAggregateDoesNotCaptureIncompleteAsCompleted(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}",
		"",
		"data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_1\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}",
		"",
	}, "\n") + "\n"

	got, err := collectCodexResponseAggregate(bytes.NewBufferString(sse), false)
	if err != nil {
		t.Fatalf("collectCodexResponseAggregate error: %v", err)
	}
	if len(got.completedData) != 0 {
		t.Fatalf("completedData must be empty on response.incomplete; got %q", string(got.completedData))
	}
}

func TestCollectCodexResponseAggregateDoesNotCaptureFailedAsCompleted(t *testing.T) {
	sse := strings.Join([]string{
		"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\"}}",
		"",
		"data: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp_1\",\"error\":{\"message\":\"server overloaded\"}}}",
		"",
	}, "\n") + "\n"

	got, err := collectCodexResponseAggregate(bytes.NewBufferString(sse), false)
	if err != nil {
		t.Fatalf("collectCodexResponseAggregate error: %v", err)
	}
	if len(got.completedData) != 0 {
		t.Fatalf("completedData must be empty on response.failed; got %q", string(got.completedData))
	}
}

func TestCollectCodexResponseAggregateIdleTimeoutClosesReader(t *testing.T) {
	reader := newBlockingReadCloser()
	start := time.Now()

	_, err := collectCodexResponseAggregateWithIdleTimeout(reader, false, 20*time.Millisecond)
	if err == nil {
		t.Fatal("collectCodexResponseAggregateWithIdleTimeout error = nil, want timeout close error")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("idle timeout took %s, want under 1s", elapsed)
	}
	if !reader.closed() {
		t.Fatal("expected idle timeout to close reader")
	}
}

func TestCollectCodexResponseAggregateIdleTimerStopsAfterSuccess(t *testing.T) {
	reader := &trackingReadCloser{Reader: strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\"}}\n\n")}

	got, err := collectCodexResponseAggregateWithIdleTimeout(reader, false, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("collectCodexResponseAggregateWithIdleTimeout error: %v", err)
	}
	if len(got.completedData) == 0 {
		t.Fatal("expected completedData")
	}
	time.Sleep(50 * time.Millisecond)
	if reader.closed() {
		t.Fatal("reader was closed after successful aggregate read")
	}
}

type trackingReadCloser struct {
	*strings.Reader
	closedFlag atomic.Bool
}

func (r *trackingReadCloser) Close() error {
	r.closedFlag.Store(true)
	return nil
}

func (r *trackingReadCloser) closed() bool {
	return r.closedFlag.Load()
}

type blockingReadCloser struct {
	closeOnce sync.Once
	closeCh   chan struct{}
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closeCh: make(chan struct{})}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	<-r.closeCh
	return 0, io.ErrClosedPipe
}

func (r *blockingReadCloser) Close() error {
	r.closeOnce.Do(func() { close(r.closeCh) })
	return nil
}

func (r *blockingReadCloser) closed() bool {
	select {
	case <-r.closeCh:
		return true
	default:
		return false
	}
}
