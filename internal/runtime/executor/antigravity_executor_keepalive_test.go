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

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

// TestAntigravityBuildRequestKeepsConnectionAlive guards the regression where the
// upstream request forced "Connection: close", which discarded every established
// TCP + TLS session and made connection pooling impossible. The native Antigravity
// client omits the Connection header entirely.
func TestAntigravityBuildRequestKeepsConnectionAlive(t *testing.T) {
	e := &AntigravityExecutor{}
	auth := &cliproxyauth.Auth{Metadata: map[string]any{"project_id": "project-1"}}
	payload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}}`)

	for _, stream := range []bool{false, true} {
		name := "unary"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			req, err := e.buildRequest(context.Background(), auth, "token", "gemini-3.6-flash-high", payload, stream, "", antigravityBaseURLDaily)
			if err != nil {
				t.Fatalf("buildRequest error: %v", err)
			}
			if req.Close {
				t.Fatal("Antigravity upstream request must not force Connection: close")
			}
			if v := req.Header.Get("Connection"); v != "" {
				t.Fatalf("Antigravity upstream request must not send a Connection header, got %q", v)
			}
		})
	}
}

// TestAntigravityExecuteStreamReusesUpstreamConnection drives the real executor
// against a local upstream and proves that repeated streaming requests share a
// single pooled TCP connection and never advertise Connection: close.
func TestAntigravityExecuteStreamReusesUpstreamConnection(t *testing.T) {
	var mu sync.Mutex
	remotes := map[string]int{}
	var connectionHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr]++
		connectionHeaders = append(connectionHeaders, r.Header.Get("Connection"))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := &cliproxyauth.Auth{
		ID:         "antigravity-keepalive-auth",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	const requests = 6
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	for i := 0; i < requests; i++ {
		result, errExecute := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gemini-3.6-flash-high",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatGemini,
			ResponseFormat:  sdktranslator.FormatGemini,
			Stream:          true,
			OriginalRequest: payload,
		})
		if errExecute != nil {
			t.Fatalf("request %d: ExecuteStream() error = %v", i, errExecute)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("request %d: stream chunk error: %v", i, chunk.Err)
			}
		}
	}

	mu.Lock()
	distinct := len(remotes)
	total := 0
	for _, c := range remotes {
		total += c
	}
	headers := append([]string(nil), connectionHeaders...)
	mu.Unlock()

	if total != requests {
		t.Fatalf("expected %d upstream requests, got %d", requests, total)
	}
	for i, h := range headers {
		if h != "" {
			t.Fatalf("upstream request %d advertised Connection: %q", i, h)
		}
	}
	if distinct != 1 {
		t.Fatalf("expected %d streaming requests to reuse one upstream connection, got %d connections", requests, distinct)
	}
}

// TestAntigravityCountTokensReusesUpstreamConnection covers the second upstream
// request builder, which shares the same connection pool.
func TestAntigravityCountTokensReusesUpstreamConnection(t *testing.T) {
	var mu sync.Mutex
	remotes := map[string]int{}
	var connectionHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr]++
		connectionHeaders = append(connectionHeaders, r.Header.Get("Connection"))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":7}`))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := &cliproxyauth.Auth{
		ID:         "antigravity-counttokens-auth",
		Provider:   "antigravity",
		Attributes: map[string]string{"base_url": server.URL},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	const requests = 4
	payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	for i := 0; i < requests; i++ {
		ctx := cliproxyexecutor.WithUpstreamAttemptTracker(context.Background())
		if _, errCount := exec.CountTokens(ctx, auth, cliproxyexecutor.Request{
			Model:   "gemini-3.6-flash-high",
			Payload: payload,
		}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatGemini,
			ResponseFormat:  sdktranslator.FormatGemini,
			OriginalRequest: payload,
		}); errCount != nil {
			t.Fatalf("request %d: CountTokens() error = %v", i, errCount)
		}
		if !cliproxyexecutor.UpstreamAttempted(ctx) {
			t.Fatalf("request %d: CountTokens() did not mark the upstream attempt", i)
		}
	}

	mu.Lock()
	distinct := len(remotes)
	headers := append([]string(nil), connectionHeaders...)
	mu.Unlock()
	for i, h := range headers {
		if h != "" {
			t.Fatalf("countTokens request %d advertised Connection: %q", i, h)
		}
	}
	if distinct != 1 {
		t.Fatalf("expected %d countTokens requests to reuse one upstream connection, got %d connections", requests, distinct)
	}
}

// TestAntigravityHTTPRequestReusesUpstreamConnection covers the raw passthrough
// path and verifies its whitelist does not reintroduce Connection: close.
// It exercises both ways a downstream caller can request a close: the header,
// which the whitelist strips, and Request.Close, which is a struct field that
// req.WithContext copies verbatim and the header whitelist cannot reach.
func TestAntigravityHTTPRequestReusesUpstreamConnection(t *testing.T) {
	var mu sync.Mutex
	remotes := map[string]int{}
	var connectionHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		remotes[r.RemoteAddr]++
		connectionHeaders = append(connectionHeaders, r.Header.Get("Connection"))
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-http-request-auth",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	const requests = 4
	for i := 0; i < requests; i++ {
		req, errRequest := http.NewRequest(http.MethodPost, server.URL, nil)
		if errRequest != nil {
			t.Fatalf("request %d: NewRequest() error = %v", i, errRequest)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Connection", "close")
		// Go's server sets this field for an inbound "Connection: close"; it must not
		// reach the Antigravity upstream.
		req.Close = true
		resp, errDo := exec.HttpRequest(context.Background(), auth, req)
		if errDo != nil {
			t.Fatalf("request %d: HttpRequest() error = %v", i, errDo)
		}
		if _, errDrain := io.Copy(io.Discard, resp.Body); errDrain != nil {
			t.Fatalf("request %d: drain response body: %v", i, errDrain)
		}
		if errClose := resp.Body.Close(); errClose != nil {
			t.Fatalf("request %d: close response body: %v", i, errClose)
		}
	}

	mu.Lock()
	distinct := len(remotes)
	headers := append([]string(nil), connectionHeaders...)
	mu.Unlock()
	for i, h := range headers {
		if h != "" {
			t.Fatalf("raw request %d advertised Connection: %q", i, h)
		}
	}
	if distinct != 1 {
		t.Fatalf("expected %d raw requests to reuse one upstream connection, got %d connections", requests, distinct)
	}
}

// TestAntigravityHTTPRequestConcurrentSessionsStayIsolated forces concurrent
// requests from one auth to complete in reverse order and verifies each caller
// receives only its own response body.
func TestAntigravityHTTPRequestConcurrentSessionsStayIsolated(t *testing.T) {
	const sessions = 12
	gates := make(map[string]chan struct{}, sessions)
	markers := make([]string, sessions)
	for i := range sessions {
		markers[i] = fmt.Sprintf("session-%02d", i)
		gates[markers[i]] = make(chan struct{})
	}
	arrived := make(chan string, sessions)
	completed := make(chan string, sessions)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		marker, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			http.Error(w, errRead.Error(), http.StatusBadRequest)
			return
		}
		gate, ok := gates[string(marker)]
		if !ok {
			http.Error(w, "unknown session marker", http.StatusBadRequest)
			return
		}
		arrived <- string(marker)
		<-gate
		_, _ = w.Write(marker)
		completed <- string(marker)
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "antigravity-concurrent-sessions-auth",
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}

	errs := make(chan error, sessions)
	var wg sync.WaitGroup
	wg.Add(sessions)
	for _, marker := range markers {
		go func(marker string) {
			defer wg.Done()
			req, errRequest := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(marker))
			if errRequest != nil {
				errs <- fmt.Errorf("%s: NewRequest: %w", marker, errRequest)
				return
			}
			resp, errDo := exec.HttpRequest(context.Background(), auth, req)
			if errDo != nil {
				errs <- fmt.Errorf("%s: HttpRequest: %w", marker, errDo)
				return
			}
			body, errRead := io.ReadAll(resp.Body)
			errClose := resp.Body.Close()
			if errRead != nil {
				errs <- fmt.Errorf("%s: read response: %w", marker, errRead)
				return
			}
			if errClose != nil {
				errs <- fmt.Errorf("%s: close response: %w", marker, errClose)
				return
			}
			if string(body) != marker {
				errs <- fmt.Errorf("%s received response for %q", marker, body)
			}
		}(marker)
	}

	seen := make(map[string]struct{}, sessions)
	for range sessions {
		marker := <-arrived
		seen[marker] = struct{}{}
	}
	if len(seen) != sessions {
		t.Fatalf("only %d/%d session markers reached upstream", len(seen), sessions)
	}
	for i := sessions - 1; i >= 0; i-- {
		close(gates[markers[i]])
		if marker := <-completed; marker != markers[i] {
			t.Fatalf("completion order = %q, want %q", marker, markers[i])
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
}
