package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestCodexExecutorExecuteStream_GrokBuildConvertsKeepaliveToSSEComment(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: keepalive\n"))
		_, _ = w.Write([]byte(`data: {"type":"keepalive","sequence_number":3}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	tests := []struct {
		name      string
		userAgent string
	}{
		{
			name:      "Grok Build with grok-pager and grok-shell",
			userAgent: "grok-pager/1.0.5 grok-shell/1.0.5 (linux; x86_64)",
		},
		{
			name:      "Grok Shell only",
			userAgent: "grok-shell/0.2.119 (macos; aarch64)",
		},
		{
			name:      "Grok Pager only",
			userAgent: "grok-pager/1.0.5 (linux; x86_64)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gpt-5.6-luna",
				Payload: []byte(`{"model":"gpt-5.6-luna","input":"test"}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai-response"),
				Stream:       true,
				Headers:      http.Header{"User-Agent": []string{tc.userAgent}},
			})
			if err != nil {
				t.Fatalf("ExecuteStream error: %v", err)
			}

			var fullOutput bytes.Buffer
			timeout := time.After(3 * time.Second)
			done := false
			for !done {
				select {
				case chunk, ok := <-res.Chunks:
					if !ok {
						done = true
						break
					}
					if chunk.Err != nil {
						t.Fatalf("unexpected chunk error: %v", chunk.Err)
					}
					fullOutput.Write(chunk.Payload)
				case <-timeout:
					t.Fatal("timed out reading stream chunks")
				}
			}

			outputStr := fullOutput.String()
			if strings.Contains(outputStr, `{"type":"keepalive"`) || strings.Contains(outputStr, "event: keepalive") {
				t.Fatalf("output must not contain keepalive event/data frame, got:\n%s", outputStr)
			}
			if !strings.Contains(outputStr, ": keepalive") {
				t.Fatalf("output must contain ': keepalive' SSE comment, got:\n%s", outputStr)
			}
			if !strings.Contains(outputStr, "response.created") || !strings.Contains(outputStr, "response.completed") {
				t.Fatalf("output missing normal lifecycle events, got:\n%s", outputStr)
			}
		})
	}
}

func TestCodexExecutorExecuteStream_GrokBuildWithBuffering(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: keepalive\n"))
		_, _ = w.Write([]byte(`data: {"type":"keepalive","sequence_number":3}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.Codex.StreamBootstrapBuffering = true
	executor := NewCodexExecutor(cfg)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":"test"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
		Headers:      http.Header{"User-Agent": []string{"grok-shell/1.0.5"}},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var fullOutput bytes.Buffer
	timeout := time.After(3 * time.Second)
	done := false
	for !done {
		select {
		case chunk, ok := <-res.Chunks:
			if !ok {
				done = true
				break
			}
			if chunk.Err != nil {
				t.Fatalf("unexpected chunk error: %v", chunk.Err)
			}
			fullOutput.Write(chunk.Payload)
		case <-timeout:
			t.Fatal("timed out reading stream chunks")
		}
	}

	outputStr := fullOutput.String()
	if strings.Contains(outputStr, `{"type":"keepalive"`) || strings.Contains(outputStr, "event: keepalive") {
		t.Fatalf("output must not contain keepalive event/data frame, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, ": keepalive") {
		t.Fatalf("output must contain ': keepalive' SSE comment, got:\n%s", outputStr)
	}
}

func TestCodexExecutorExecuteStream_GrokBuildDetectedFromGinContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: keepalive\n"))
		_, _ = w.Write([]byte(`data: {"type":"keepalive","sequence_number":3}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "grok-pager/1.0.5")
	ctx := context.WithValue(context.Background(), "gin", c)

	res, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":"test"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var fullOutput bytes.Buffer
	timeout := time.After(3 * time.Second)
	done := false
	for !done {
		select {
		case chunk, ok := <-res.Chunks:
			if !ok {
				done = true
				break
			}
			if chunk.Err != nil {
				t.Fatalf("unexpected chunk error: %v", chunk.Err)
			}
			fullOutput.Write(chunk.Payload)
		case <-timeout:
			t.Fatal("timed out reading stream chunks")
		}
	}

	outputStr := fullOutput.String()
	if strings.Contains(outputStr, `{"type":"keepalive"`) || strings.Contains(outputStr, "event: keepalive") {
		t.Fatalf("output must not contain keepalive event/data frame, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, ": keepalive") {
		t.Fatalf("output must contain ': keepalive' SSE comment, got:\n%s", outputStr)
	}
}

func TestCodexExecutorExecuteStream_NonGrokClientKeepsVerbatim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.created\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.6-luna"}}` + "\n\n"))
		_, _ = w.Write([]byte("event: keepalive\n"))
		_, _ = w.Write([]byte(`data: {"type":"keepalive","sequence_number":3}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}` + "\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	res, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":"test"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
		Headers:      http.Header{"User-Agent": []string{"curl/8.7.1"}},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var fullOutput bytes.Buffer
	timeout := time.After(3 * time.Second)
	done := false
	for !done {
		select {
		case chunk, ok := <-res.Chunks:
			if !ok {
				done = true
				break
			}
			if chunk.Err != nil {
				t.Fatalf("unexpected chunk error: %v", chunk.Err)
			}
			fullOutput.Write(chunk.Payload)
		case <-timeout:
			t.Fatal("timed out reading stream chunks")
		}
	}

	outputStr := fullOutput.String()
	if !strings.Contains(outputStr, `{"type":"keepalive"`) && !strings.Contains(outputStr, "event: keepalive") {
		t.Fatalf("expected verbatim keepalive for non-Grok client, got:\n%s", outputStr)
	}
}
