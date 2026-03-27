package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	runtimeusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator/builtin"
	"github.com/tidwall/gjson"
)

type codexCapturedRequest struct {
	Path          string
	Accept        string
	Authorization string
	Version       string
	SessionID     string
	Body          []byte
}

type codexStatusError interface {
	error
	StatusCode() int
	RetryAfter() *time.Duration
}

func TestCodexExecutorExecute_LocalServer_SuccessAndHeaders(t *testing.T) {
	t.Parallel()

	var (
		mu       sync.Mutex
		captured codexCapturedRequest
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		captured = codexCapturedRequest{
			Path:          r.URL.Path,
			Accept:        r.Header.Get("Accept"),
			Authorization: r.Header.Get("Authorization"),
			Version:       r.Header.Get("Version"),
			SessionID:     r.Header.Get("Session_id"),
			Body:          body,
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n")
		_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_success", "gpt-5.4", "ok-success")+"\n")
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, err := executor.Execute(
		context.Background(),
		newCodexTestAuth(server.URL, "success-key"),
		newCodexResponsesRequest("verify header and body rewrites"),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if captured.Path != "/responses" {
		t.Fatalf("path = %q, want %q", captured.Path, "/responses")
	}
	if captured.Accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want %q", captured.Accept, "text/event-stream")
	}
	if captured.Authorization != "Bearer success-key" {
		t.Fatalf("Authorization = %q, want %q", captured.Authorization, "Bearer success-key")
	}
	if captured.Version != "" {
		t.Fatalf("Version = %q, want empty by default", captured.Version)
	}
	if captured.SessionID == "" {
		t.Fatal("expected Session_id header to be populated")
	}
	if gotModel := gjson.GetBytes(captured.Body, "model").String(); gotModel != "gpt-5.4" {
		t.Fatalf("request model = %q, want %q", gotModel, "gpt-5.4")
	}
	if gotRole := gjson.GetBytes(captured.Body, "input.0.role").String(); gotRole != "developer" {
		t.Fatalf("request input[0].role = %q, want %q", gotRole, "developer")
	}
	if gotStream := gjson.GetBytes(captured.Body, "stream"); !gotStream.Exists() || !gotStream.Bool() {
		t.Fatalf("request stream = %s, want true", gotStream.Raw)
	}
	if gotInstructions := gjson.GetBytes(captured.Body, "instructions"); !gotInstructions.Exists() || gotInstructions.String() != "" {
		t.Fatalf("request instructions = %s, want empty string", gotInstructions.Raw)
	}
	if gotID := gjson.GetBytes(resp.Payload, "id").String(); gotID != "resp_success" {
		t.Fatalf("response id = %q, want %q", gotID, "resp_success")
	}
	if gotText := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); gotText != "ok-success" {
		t.Fatalf("response text = %q, want %q", gotText, "ok-success")
	}
}

func TestCodexExecutorExecute_LocalServer_SucceedsWithoutTerminalNewline(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_no_newline", "gpt-5.4", "ok-no-newline"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, err := executor.Execute(
		context.Background(),
		newCodexTestAuth(server.URL, "success-key"),
		newCodexResponsesRequest("verify no terminal newline"),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotID := gjson.GetBytes(resp.Payload, "id").String(); gotID != "resp_no_newline" {
		t.Fatalf("response id = %q, want %q", gotID, "resp_no_newline")
	}
	if gotText := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); gotText != "ok-no-newline" {
		t.Fatalf("response text = %q, want %q", gotText, "ok-no-newline")
	}
}

func TestCodexExecutorExecute_LocalServer_ReturnsAfterCompletedBeforeUpstreamCloses(t *testing.T) {
	t.Parallel()

	clientClosed := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n")
		_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_early_return", "gpt-5.4", "ok-early-return")+"\n")
		if flusher != nil {
			flusher.Flush()
		}

		select {
		case <-r.Context().Done():
			clientClosed <- struct{}{}
		case <-time.After(1500 * time.Millisecond):
		}
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	started := time.Now()
	resp, err := executor.Execute(
		context.Background(),
		newCodexTestAuth(server.URL, "early-return-key"),
		newCodexResponsesRequest("return immediately after completed event"),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
	)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotText := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); gotText != "ok-early-return" {
		t.Fatalf("response text = %q, want %q", gotText, "ok-early-return")
	}
	if elapsed >= 1200*time.Millisecond {
		t.Fatalf("Execute() elapsed = %s, want < 1.2s", elapsed)
	}

	select {
	case <-clientClosed:
	case <-time.After(800 * time.Millisecond):
		t.Fatal("expected Execute() to close upstream body after response.completed")
	}
}

func TestCodexExecutorExecute_LocalServer_ReusesConnectionWhenCompletedEOFIsBrieflyDelayed(t *testing.T) {
	var newConnections atomic.Int64

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"type\":\"response.created\"}\n")
		_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_keepalive", "gpt-5.4", "ok-keepalive")+"\n")
		if flusher != nil {
			flusher.Flush()
		}
		time.Sleep(30 * time.Millisecond)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	for i := 0; i < 2; i++ {
		resp, err := executor.Execute(
			context.Background(),
			newCodexTestAuth(server.URL, "keepalive-key"),
			newCodexResponsesRequest(fmt.Sprintf("keepalive-%d", i)),
			cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
		)
		if err != nil {
			t.Fatalf("Execute() #%d error = %v", i+1, err)
		}
		if got := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); got != "ok-keepalive" {
			t.Fatalf("response text #%d = %q, want %q", i+1, got, "ok-keepalive")
		}
	}

	if got := newConnections.Load(); got != 1 {
		t.Fatalf("new TCP connections = %d, want 1", got)
	}
}

func TestCodexExecutorExecute_LocalServer_PublishesRequestStatsWithoutUsageFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+codexCompletedEventWithoutUsageJSON("resp_no_usage", "gpt-5.4", "ok-no-usage")+"\n")
	}))
	defer server.Close()

	apiKey := fmt.Sprintf("stats-no-usage-%d", time.Now().UnixNano())
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Set("apiKey", apiKey)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	executor := NewCodexExecutor(&config.Config{})
	resp, err := executor.Execute(
		ctx,
		newCodexTestAuth(server.URL, "success-key"),
		newCodexResponsesRequest("verify zero-token success stats"),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotID := gjson.GetBytes(resp.Payload, "id").String(); gotID != "resp_no_usage" {
		t.Fatalf("response id = %q, want %q", gotID, "resp_no_usage")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snapshot := runtimeusage.GetRequestStatistics().Snapshot()
		apiStats, ok := snapshot.APIs[apiKey]
		if !ok {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		modelStats, ok := apiStats.Models["gpt-5.4"]
		if !ok || len(modelStats.Details) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		detail := modelStats.Details[len(modelStats.Details)-1]
		if detail.Failed {
			t.Fatal("expected successful request statistics record")
		}
		if detail.Tokens.TotalTokens != 0 {
			t.Fatalf("total tokens = %d, want 0 for usage-free completed event", detail.Tokens.TotalTokens)
		}
		return
	}

	t.Fatalf("timed out waiting for usage statistics record for API key %q", apiKey)
}

func TestCodexExecutorExecuteCompact_LocalServer_RequestShapeAndPayload(t *testing.T) {
	t.Parallel()

	var captured codexCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = codexCapturedRequest{
			Path:          r.URL.Path,
			Accept:        r.Header.Get("Accept"),
			Authorization: r.Header.Get("Authorization"),
			Body:          body,
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_compact","object":"response","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`)
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	resp, err := executor.Execute(
		context.Background(),
		newCodexTestAuth(server.URL, "compact-key"),
		newCodexResponsesRequest("compact request"),
		cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai-response"),
			Alt:          "responses/compact",
		},
	)
	if err != nil {
		t.Fatalf("Execute() compact error = %v", err)
	}

	if captured.Path != "/responses/compact" {
		t.Fatalf("path = %q, want %q", captured.Path, "/responses/compact")
	}
	if captured.Accept != "application/json" {
		t.Fatalf("Accept = %q, want %q", captured.Accept, "application/json")
	}
	if captured.Authorization != "Bearer compact-key" {
		t.Fatalf("Authorization = %q, want %q", captured.Authorization, "Bearer compact-key")
	}
	if gotStream := gjson.GetBytes(captured.Body, "stream"); gotStream.Exists() {
		t.Fatalf("request stream should be removed for compact path, got %s", gotStream.Raw)
	}
	if gotInstructions := gjson.GetBytes(captured.Body, "instructions"); !gotInstructions.Exists() || gotInstructions.String() != "" {
		t.Fatalf("request instructions = %s, want empty string", gotInstructions.Raw)
	}
	if string(resp.Payload) != `{"id":"resp_compact","object":"response","status":"completed","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestCodexExecutorExecute_LocalServer_Parses429RetryAfter(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":17}}`)
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(
		context.Background(),
		newCodexTestAuth(server.URL, "quota-key"),
		newCodexResponsesRequest("quota request"),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	statusErr, ok := err.(codexStatusError)
	if !ok {
		t.Fatalf("error type = %T, want status error with retryAfter", err)
	}
	if statusErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status code = %d, want %d", statusErr.StatusCode(), http.StatusTooManyRequests)
	}
	retryAfter := statusErr.RetryAfter()
	if retryAfter == nil || *retryAfter != 17*time.Second {
		if retryAfter == nil {
			t.Fatal("RetryAfter = nil, want 17s")
		}
		t.Fatalf("RetryAfter = %v, want %v", *retryAfter, 17*time.Second)
	}
}

func TestCodexExecutorExecuteStream_LocalServer_EmitsChunksAndCompletion(t *testing.T) {
	t.Parallel()

	var captured codexCapturedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured = codexCapturedRequest{
			Path:          r.URL.Path,
			Accept:        r.Header.Get("Accept"),
			Authorization: r.Header.Get("Authorization"),
			Body:          body,
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"type":"response.created","response":{"id":"resp_stream"}}`,
			`data: {"type":"response.output_text.delta","delta":"hel"}`,
			"data: " + codexCompletedEventJSON("resp_stream", "gpt-5.4", "hello stream"),
		} {
			_, _ = io.WriteString(w, line+"\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	result, err := executor.ExecuteStream(
		context.Background(),
		newCodexTestAuth(server.URL, "stream-key"),
		newCodexResponsesRequest("stream request"),
		cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true},
	)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	var chunks []string
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		payload := strings.TrimSpace(string(chunk.Payload))
		if payload != "" {
			chunks = append(chunks, payload)
		}
	}

	if captured.Path != "/responses" {
		t.Fatalf("path = %q, want %q", captured.Path, "/responses")
	}
	if captured.Accept != "text/event-stream" {
		t.Fatalf("Accept = %q, want %q", captured.Accept, "text/event-stream")
	}
	if gotModel := gjson.GetBytes(captured.Body, "model").String(); gotModel != "gpt-5.4" {
		t.Fatalf("request model = %q, want %q", gotModel, "gpt-5.4")
	}
	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d, want 3; chunks=%q", len(chunks), chunks)
	}
	if !strings.Contains(chunks[0], `"type":"response.created"`) {
		t.Fatalf("first chunk = %q, want response.created event", chunks[0])
	}
	if !strings.Contains(chunks[1], `"type":"response.output_text.delta"`) {
		t.Fatalf("second chunk = %q, want response.output_text.delta event", chunks[1])
	}
	if !strings.Contains(chunks[2], `"type":"response.completed"`) {
		t.Fatalf("third chunk = %q, want response.completed event", chunks[2])
	}
}

func TestCodexExecutorExecute_LocalServer_ConcurrentMixedAccountsRemainIsolated(t *testing.T) {
	scenarios := []struct {
		key            string
		wantStatusCode int
		wantText       string
		wantRetryAfter time.Duration
	}{
		{key: "success-a", wantStatusCode: http.StatusOK, wantText: "ok-success-a"},
		{key: "success-b", wantStatusCode: http.StatusOK, wantText: "ok-success-b"},
		{key: "invalid-401", wantStatusCode: http.StatusUnauthorized},
		{key: "quota-429", wantStatusCode: http.StatusTooManyRequests, wantRetryAfter: 9 * time.Second},
		{key: "upstream-502", wantStatusCode: http.StatusBadGateway},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch token {
		case "success-a":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_success_a", "gpt-5.4", "ok-success-a")+"\n")
		case "success-b":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_success_b", "gpt-5.4", "ok-success-b")+"\n")
		case "invalid-401":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_api_key","message":"unauthorized"}}`)
		case "quota-429":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":9}}`)
		case "upstream-502":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"type":"upstream_error","message":"bad gateway"}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"type":"unexpected_token"}}`)
		}
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	const iterations = 200
	errCh := make(chan error, iterations)
	var wg sync.WaitGroup

	for i := 0; i < iterations; i++ {
		scenario := scenarios[i%len(scenarios)]
		wg.Add(1)
		go func(index int, scenario struct {
			key            string
			wantStatusCode int
			wantText       string
			wantRetryAfter time.Duration
		}) {
			defer wg.Done()

			resp, err := executor.Execute(
				context.Background(),
				newCodexTestAuth(server.URL, scenario.key),
				newCodexResponsesRequest(fmt.Sprintf("request-%d", index)),
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
			)

			if scenario.wantStatusCode == http.StatusOK {
				if err != nil {
					errCh <- fmt.Errorf("%s: Execute() unexpected error = %v", scenario.key, err)
					return
				}
				if gotText := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); gotText != scenario.wantText {
					errCh <- fmt.Errorf("%s: response text = %q, want %q", scenario.key, gotText, scenario.wantText)
				}
				return
			}

			if err == nil {
				errCh <- fmt.Errorf("%s: expected error with status %d, got nil", scenario.key, scenario.wantStatusCode)
				return
			}
			statusErr, ok := err.(codexStatusError)
			if !ok {
				errCh <- fmt.Errorf("%s: error type = %T, want status error", scenario.key, err)
				return
			}
			if statusErr.StatusCode() != scenario.wantStatusCode {
				errCh <- fmt.Errorf("%s: status code = %d, want %d", scenario.key, statusErr.StatusCode(), scenario.wantStatusCode)
				return
			}
			if scenario.wantRetryAfter > 0 {
				retryAfter := statusErr.RetryAfter()
				if retryAfter == nil || *retryAfter != scenario.wantRetryAfter {
					if retryAfter == nil {
						errCh <- fmt.Errorf("%s: RetryAfter = nil, want %v", scenario.key, scenario.wantRetryAfter)
						return
					}
					errCh <- fmt.Errorf("%s: RetryAfter = %v, want %v", scenario.key, *retryAfter, scenario.wantRetryAfter)
				}
			}
		}(i, scenario)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

func TestCodexExecutorExecute_LocalServer_ConcurrentMixedAccountsRemainIsolated_LongRequest(t *testing.T) {
	scenarios := []struct {
		key            string
		wantStatusCode int
		wantRetryAfter time.Duration
	}{
		{key: "success-a", wantStatusCode: http.StatusOK},
		{key: "success-b", wantStatusCode: http.StatusOK},
		{key: "invalid-401", wantStatusCode: http.StatusUnauthorized},
		{key: "quota-429", wantStatusCode: http.StatusTooManyRequests, wantRetryAfter: 9 * time.Second},
		{key: "upstream-502", wantStatusCode: http.StatusBadGateway},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gotRole := gjson.GetBytes(body, "input.0.role").String(); gotRole != "developer" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"system role was not rewritten"}}`)
			return
		}
		prompt := gjson.GetBytes(body, "input.1.content.0.text").String()
		if len(prompt) < 60_000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"request was not long enough"}}`)
			return
		}
		marker := extractLongPromptMarker(prompt)
		if marker == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"marker missing from long request"}}`)
			return
		}

		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch token {
		case "success-a":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_long_success_a", "gpt-5.4", "ack:"+marker)+"\n")
		case "success-b":
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_long_success_b", "gpt-5.4", "ack:"+marker)+"\n")
		case "invalid-401":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_api_key","message":"unauthorized"}}`)
		case "quota-429":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":9}}`)
		case "upstream-502":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"type":"upstream_error","message":"bad gateway"}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"type":"unexpected_token"}}`)
		}
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	const iterations = 80
	errCh := make(chan error, iterations)
	var wg sync.WaitGroup

	for i := 0; i < iterations; i++ {
		scenario := scenarios[i%len(scenarios)]
		wg.Add(1)
		go func(index int, scenario struct {
			key            string
			wantStatusCode int
			wantRetryAfter time.Duration
		}) {
			defer wg.Done()

			marker := fmt.Sprintf("long-request-%d", index)
			resp, err := executor.Execute(
				context.Background(),
				newCodexTestAuth(server.URL, scenario.key),
				newLongCodexResponsesRequest(marker),
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
			)

			if scenario.wantStatusCode == http.StatusOK {
				if err != nil {
					errCh <- fmt.Errorf("%s: Execute() unexpected error = %v", scenario.key, err)
					return
				}
				if gotText := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); gotText != "ack:"+marker {
					errCh <- fmt.Errorf("%s: response text = %q, want %q", scenario.key, gotText, "ack:"+marker)
				}
				return
			}

			if err == nil {
				errCh <- fmt.Errorf("%s: expected error with status %d, got nil", scenario.key, scenario.wantStatusCode)
				return
			}
			statusErr, ok := err.(codexStatusError)
			if !ok {
				errCh <- fmt.Errorf("%s: error type = %T, want status error", scenario.key, err)
				return
			}
			if statusErr.StatusCode() != scenario.wantStatusCode {
				errCh <- fmt.Errorf("%s: status code = %d, want %d", scenario.key, statusErr.StatusCode(), scenario.wantStatusCode)
				return
			}
			if scenario.wantRetryAfter > 0 {
				retryAfter := statusErr.RetryAfter()
				if retryAfter == nil || *retryAfter != scenario.wantRetryAfter {
					if retryAfter == nil {
						errCh <- fmt.Errorf("%s: RetryAfter = nil, want %v", scenario.key, scenario.wantRetryAfter)
						return
					}
					errCh <- fmt.Errorf("%s: RetryAfter = %v, want %v", scenario.key, *retryAfter, scenario.wantRetryAfter)
				}
			}
		}(i, scenario)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

func TestCodexExecutorExecute_LocalServer_SustainsOver6KRPM_MixedLongRequests(t *testing.T) {
	if os.Getenv("CLI_PROXY_INTEGRATION_STRESS") != "1" {
		t.Skip("set CLI_PROXY_INTEGRATION_STRESS=1 to run the 6k RPM mixed long-request stress test")
	}

	type stressScenario struct {
		key            string
		long           bool
		wantStatusCode int
		wantRetryAfter time.Duration
	}

	const (
		totalRequests = 768
		workerCount   = 160
		successDelay  = 1800 * time.Millisecond
		errorDelay    = 300 * time.Millisecond
		targetRPM     = 6000.0
	)

	scenarios := []stressScenario{
		{key: "success-a", long: true, wantStatusCode: http.StatusOK},
		{key: "success-b", long: false, wantStatusCode: http.StatusOK},
		{key: "success-a", long: true, wantStatusCode: http.StatusOK},
		{key: "success-b", long: true, wantStatusCode: http.StatusOK},
		{key: "success-a", long: false, wantStatusCode: http.StatusOK},
		{key: "invalid-401", long: false, wantStatusCode: http.StatusUnauthorized},
		{key: "quota-429", long: true, wantStatusCode: http.StatusTooManyRequests, wantRetryAfter: 9 * time.Second},
		{key: "upstream-502", long: false, wantStatusCode: http.StatusBadGateway},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gotRole := gjson.GetBytes(body, "input.0.role").String(); gotRole != "developer" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"system role was not rewritten"}}`)
			return
		}

		prompt := gjson.GetBytes(body, "input.1.content.0.text").String()
		marker := prompt
		if strings.HasPrefix(prompt, "marker=") {
			if len(prompt) < 60_000 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"request was not long enough"}}`)
				return
			}
			marker = extractLongPromptMarker(prompt)
			if marker == "" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"marker missing from long request"}}`)
				return
			}
		}

		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		switch token {
		case "success-a", "success-b":
			time.Sleep(successDelay)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_"+token, "gpt-5.4", "ack:"+marker)+"\n")
		case "invalid-401":
			time.Sleep(errorDelay)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"type":"invalid_api_key","message":"unauthorized"}}`)
		case "quota-429":
			time.Sleep(errorDelay)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"usage_limit_reached","resets_in_seconds":9}}`)
		case "upstream-502":
			time.Sleep(errorDelay)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, `{"error":{"type":"upstream_error","message":"bad gateway"}}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"type":"unexpected_token"}}`)
		}
	}))
	defer server.Close()

	runtime.GC()
	debug.FreeOSMemory()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	executor := NewCodexExecutor(&config.Config{})
	jobs := make(chan int)
	start := make(chan struct{})
	errCh := make(chan error, totalRequests)

	var (
		wg           sync.WaitGroup
		inflight     atomic.Int64
		peakInflight atomic.Int64
		successes    atomic.Int64
		unauthorized atomic.Int64
		quota        atomic.Int64
		badGateway   atomic.Int64
		latMu        sync.Mutex
		latencies    = make([]time.Duration, 0, totalRequests)
	)

	samplerDone := make(chan struct{})
	peakSampleCh := startCodexStressSampler(samplerDone, &inflight)

	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for index := range jobs {
				scenario := scenarios[index%len(scenarios)]
				marker := fmt.Sprintf("stress-%d", index)

				var req cliproxyexecutor.Request
				if scenario.long {
					req = newLongCodexResponsesRequest(marker)
				} else {
					req = newCodexResponsesRequest(marker)
				}

				currentInflight := inflight.Add(1)
				updateAtomicMaxInt64(&peakInflight, currentInflight)
				started := time.Now()
				resp, err := executor.Execute(
					context.Background(),
					newCodexTestAuth(server.URL, scenario.key),
					req,
					cliproxyexecutor.Options{
						SourceFormat: sdktranslator.FromString("openai-response"),
						Metadata: map[string]any{
							cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.4",
						},
					},
				)
				duration := time.Since(started)
				inflight.Add(-1)

				latMu.Lock()
				latencies = append(latencies, duration)
				latMu.Unlock()

				if scenario.wantStatusCode == http.StatusOK {
					if err != nil {
						errCh <- fmt.Errorf("%s: Execute() unexpected error = %v", scenario.key, err)
						continue
					}
					if gotText := gjson.GetBytes(resp.Payload, "output.0.content.0.text").String(); gotText != "ack:"+marker {
						errCh <- fmt.Errorf("%s: response text = %q, want %q", scenario.key, gotText, "ack:"+marker)
						continue
					}
					successes.Add(1)
					continue
				}

				if err == nil {
					errCh <- fmt.Errorf("%s: expected error with status %d, got nil", scenario.key, scenario.wantStatusCode)
					continue
				}
				statusErr, ok := err.(codexStatusError)
				if !ok {
					errCh <- fmt.Errorf("%s: error type = %T, want status error", scenario.key, err)
					continue
				}
				if statusErr.StatusCode() != scenario.wantStatusCode {
					errCh <- fmt.Errorf("%s: status code = %d, want %d", scenario.key, statusErr.StatusCode(), scenario.wantStatusCode)
					continue
				}
				if scenario.wantRetryAfter > 0 {
					retryAfter := statusErr.RetryAfter()
					if retryAfter == nil || *retryAfter != scenario.wantRetryAfter {
						if retryAfter == nil {
							errCh <- fmt.Errorf("%s: RetryAfter = nil, want %v", scenario.key, scenario.wantRetryAfter)
							continue
						}
						errCh <- fmt.Errorf("%s: RetryAfter = %v, want %v", scenario.key, *retryAfter, scenario.wantRetryAfter)
						continue
					}
				}

				switch scenario.wantStatusCode {
				case http.StatusUnauthorized:
					unauthorized.Add(1)
				case http.StatusTooManyRequests:
					quota.Add(1)
				case http.StatusBadGateway:
					badGateway.Add(1)
				}
			}
		}()
	}

	begin := time.Now()
	close(start)
	for i := 0; i < totalRequests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(begin)
	close(samplerDone)
	peakSample := <-peakSampleCh
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	runtime.GC()
	debug.FreeOSMemory()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	rpm := float64(totalRequests) / elapsed.Seconds() * 60
	if rpm < targetRPM {
		t.Fatalf("throughput = %.0f rpm, want >= %.0f rpm", rpm, targetRPM)
	}

	var expectedSuccesses, expected401, expected429, expected502 int
	for i := 0; i < totalRequests; i++ {
		switch scenarios[i%len(scenarios)].wantStatusCode {
		case http.StatusOK:
			expectedSuccesses++
		case http.StatusUnauthorized:
			expected401++
		case http.StatusTooManyRequests:
			expected429++
		case http.StatusBadGateway:
			expected502++
		}
	}
	if got := successes.Load(); got != int64(expectedSuccesses) {
		t.Fatalf("successes = %d, want %d", got, expectedSuccesses)
	}
	if got := unauthorized.Load(); got != int64(expected401) {
		t.Fatalf("401 count = %d, want %d", got, expected401)
	}
	if got := quota.Load(); got != int64(expected429) {
		t.Fatalf("429 count = %d, want %d", got, expected429)
	}
	if got := badGateway.Load(); got != int64(expected502) {
		t.Fatalf("502 count = %d, want %d", got, expected502)
	}

	p50 := percentileDuration(latencies, 0.50)
	p95 := percentileDuration(latencies, 0.95)
	p99 := percentileDuration(latencies, 0.99)

	t.Logf(
		"6k rpm codex stress: requests=%d workers=%d elapsed=%s rpm=%.0f peak_inflight=%d sampler_peak_inflight=%d p50=%s p95=%s p99=%s heap_alloc_before=%d heap_alloc_after=%d peak_heap_alloc=%d peak_heap_inuse=%d peak_goroutines=%d",
		totalRequests,
		workerCount,
		elapsed,
		rpm,
		peakInflight.Load(),
		peakSample.Inflight,
		p50,
		p95,
		p99,
		before.HeapAlloc,
		after.HeapAlloc,
		peakSample.HeapAlloc,
		peakSample.HeapInuse,
		peakSample.Goroutines,
	)
}

func TestCodexExecutorExecuteStreamTTFTHighConcurrency_LongConversationPreviousResponseID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping high-concurrency stream ttft coverage in short mode")
	}
	const (
		totalRequests = 1024
		concurrency   = 256
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if gjson.GetBytes(body, "previous_response_id").Exists() {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"previous_response_id should be stripped for HTTP streaming"}}`)
			return
		}
		prompt := gjson.GetBytes(body, "input.1.content.0.text").String()
		if len(prompt) < 60_000 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"request was not long enough"}}`)
			return
		}
		if !strings.Contains(prompt, "marker=") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"type":"bad_request","message":"marker missing from long request"}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"type":"response.created","response":{"id":"resp_stream_ttft"}}`,
			`data: {"type":"response.output_text.delta","delta":"bench"}`,
			"data: " + codexCompletedEventJSON("resp_stream_ttft", "gpt-5.4", "ok-stream-ttft"),
		} {
			_, _ = io.WriteString(w, line+"\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := newCodexTestAuth(server.URL, "bench-stream-ttft-key")

	jobs := make(chan int, totalRequests)
	start := make(chan struct{})
	errCh := make(chan error, totalRequests)
	latencies := make([]time.Duration, totalRequests)

	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for index := range jobs {
				req := newLongCodexResponsesRequestWithPreviousResponseID(fmt.Sprintf("ttft-stream-%d", index), "resp-prev-http-stream")
				started := time.Now()
				result, err := executor.ExecuteStream(
					context.Background(),
					auth,
					req,
					cliproxyexecutor.Options{
						SourceFormat: sdktranslator.FromString("openai-response"),
						Stream:       true,
						Metadata: map[string]any{
							cliproxyexecutor.RequestedModelMetadataKey: "gpt-5.4",
						},
					},
				)
				if err != nil {
					errCh <- fmt.Errorf("ExecuteStream() error = %w", err)
					continue
				}

				seenDelta := false
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						errCh <- fmt.Errorf("stream chunk error = %w", chunk.Err)
						break
					}
					if !seenDelta && bytes.Contains(chunk.Payload, []byte(`"type":"response.output_text.delta"`)) {
						elapsed := time.Since(started)
						if elapsed <= 0 {
							elapsed = time.Nanosecond
						}
						latencies[index] = elapsed
						seenDelta = true
					}
				}
				if !seenDelta {
					errCh <- fmt.Errorf("no delta chunk observed")
				}
			}
		}()
	}

	for i := 0; i < totalRequests; i++ {
		jobs <- i
	}
	close(jobs)

	begin := time.Now()
	close(start)
	wg.Wait()
	close(errCh)
	elapsed := time.Since(begin)

	for err := range errCh {
		t.Fatal(err)
	}

	p50 := percentileDuration(latencies, 0.50)
	p95 := percentileDuration(latencies, 0.95)
	p99 := percentileDuration(latencies, 0.99)
	var total time.Duration
	for _, latency := range latencies {
		total += latency
	}
	avg := total / time.Duration(len(latencies))

	t.Logf(
		"codex http stream ttft long+previous_response_id: requests=%d concurrency=%d elapsed=%s avg=%s p50=%s p95=%s p99=%s",
		totalRequests,
		concurrency,
		elapsed,
		avg,
		p50,
		p95,
		p99,
	)
}

func BenchmarkCodexExecutorExecute_LocalServerParallel(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: "+codexCompletedEventJSON("resp_bench", "gpt-5.4", "ok-bench")+"\n")
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := newCodexTestAuth(server.URL, "bench-key")

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			resp, err := executor.Execute(
				context.Background(),
				auth,
				newCodexResponsesRequest("benchmark non-stream"),
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response")},
			)
			if err != nil {
				b.Fatalf("Execute() error = %v", err)
			}
			if len(resp.Payload) == 0 {
				b.Fatal("empty payload")
			}
		}
	})
}

func BenchmarkCodexExecutorExecuteStream_LocalServerParallel(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		for _, line := range []string{
			`data: {"type":"response.created","response":{"id":"resp_stream_bench"}}`,
			`data: {"type":"response.output_text.delta","delta":"bench"}`,
			"data: " + codexCompletedEventJSON("resp_stream_bench", "gpt-5.4", "ok-stream-bench"),
		} {
			_, _ = io.WriteString(w, line+"\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := newCodexTestAuth(server.URL, "bench-stream-key")

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			result, err := executor.ExecuteStream(
				context.Background(),
				auth,
				newCodexResponsesRequest("benchmark stream"),
				cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Stream: true},
			)
			if err != nil {
				b.Fatalf("ExecuteStream() error = %v", err)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					b.Fatalf("stream chunk error = %v", chunk.Err)
				}
			}
		}
	})
}

func newCodexTestAuth(baseURL, apiKey string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": baseURL,
			"api_key":  apiKey,
		},
	}
}

func newCodexResponsesRequest(prompt string) cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(fmt.Sprintf(`{
			"model":"gpt-5.4",
			"input":[
				{"type":"message","role":"system","content":[{"type":"input_text","text":"be precise"}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}
			],
			"stream":false,
			"user":"integration-test"
		}`, prompt)),
	}
}

func newLongCodexResponsesRequest(marker string) cliproxyexecutor.Request {
	longPrompt := buildExecutorLongPrompt(marker)
	return cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(fmt.Sprintf(`{
			"model":"gpt-5.4",
			"input":[
				{"type":"message","role":"system","content":[{"type":"input_text","text":%q}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}
			],
			"stream":false,
			"user":"integration-test"
		}`, strings.Repeat("be precise and preserve latency. ", 2048), longPrompt)),
	}
}

func newLongCodexResponsesRequestWithPreviousResponseID(marker string, previousResponseID string) cliproxyexecutor.Request {
	longPrompt := buildExecutorLongPrompt(marker)
	return cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(fmt.Sprintf(`{
			"model":"gpt-5.4",
			"previous_response_id":%q,
			"input":[
				{"type":"message","role":"system","content":[{"type":"input_text","text":%q}]},
				{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}
			],
			"stream":false,
			"user":"integration-test"
		}`, previousResponseID, strings.Repeat("be precise and preserve latency. ", 2048), longPrompt)),
	}
}

func buildExecutorLongPrompt(marker string) string {
	return "marker=" + marker + "\n" + strings.Repeat("executor-long-request-segment-"+marker+";", 4096)
}

func extractLongPromptMarker(prompt string) string {
	line, _, _ := strings.Cut(prompt, "\n")
	return strings.TrimPrefix(line, "marker=")
}

func codexCompletedEventJSON(id, model, text string) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"object":"response","model":%q,"status":"completed","output":[{"type":"message","id":"msg_%s","role":"assistant","content":[{"type":"output_text","text":%q}]}],"usage":{"input_tokens":3,"output_tokens":5,"total_tokens":8}}}`, id, model, id, text)
}

func codexCompletedEventWithoutUsageJSON(id, model, text string) string {
	return fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"object":"response","model":%q,"status":"completed","output":[{"type":"message","id":"msg_%s","role":"assistant","content":[{"type":"output_text","text":%q}]}]}}`, id, model, id, text)
}

type codexStressHighWater struct {
	HeapAlloc  uint64
	HeapInuse  uint64
	Goroutines int
	Inflight   int64
}

func startCodexStressSampler(done <-chan struct{}, inflight *atomic.Int64) <-chan codexStressHighWater {
	result := make(chan codexStressHighWater, 1)
	go func() {
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()

		var peak codexStressHighWater
		sample := func() {
			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > peak.HeapAlloc {
				peak.HeapAlloc = ms.HeapAlloc
			}
			if ms.HeapInuse > peak.HeapInuse {
				peak.HeapInuse = ms.HeapInuse
			}
			if goroutines := runtime.NumGoroutine(); goroutines > peak.Goroutines {
				peak.Goroutines = goroutines
			}
			if currentInflight := inflight.Load(); currentInflight > peak.Inflight {
				peak.Inflight = currentInflight
			}
		}

		sample()
		for {
			select {
			case <-ticker.C:
				sample()
			case <-done:
				sample()
				result <- peak
				return
			}
		}
	}()
	return result
}

func updateAtomicMaxInt64(dst *atomic.Int64, candidate int64) {
	for {
		current := dst.Load()
		if candidate <= current {
			return
		}
		if dst.CompareAndSwap(current, candidate) {
			return
		}
	}
}

func percentileDuration(values []time.Duration, fraction float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(float64(len(sorted)-1) * fraction)
	return sorted[index]
}
