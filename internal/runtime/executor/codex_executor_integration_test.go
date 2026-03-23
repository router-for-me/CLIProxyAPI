package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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
	if captured.Version == "" {
		t.Fatal("expected Version header to be populated")
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
