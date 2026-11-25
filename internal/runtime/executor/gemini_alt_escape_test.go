package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiExecutorAltEscape(t *testing.T) {
	const injectedAlt = "json&injected=1"
	const wantRawQuery = "$alt=json%26injected%3D1"
	const okResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`

	var mu sync.Mutex
	var lastRawQuery string
	var hasInjected bool
	var gotAltParam string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastRawQuery = r.URL.RawQuery
		q := r.URL.Query()
		hasInjected = q.Has("injected")
		gotAltParam = q.Get("$alt")
		mu.Unlock()

		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+okResponse+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okResponse)
	}))
	defer server.Close()

	exec := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "gemini-alt-escape",
		Provider: "gemini",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
		Alt:          injectedAlt,
	}

	t.Run("Execute", func(t *testing.T) {
		if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if lastRawQuery != wantRawQuery {
			t.Fatalf("RawQuery = %q, want %q", lastRawQuery, wantRawQuery)
		}
		if hasInjected {
			t.Fatalf("unexpected injected query parameter in %q", lastRawQuery)
		}
		if gotAltParam != injectedAlt {
			t.Fatalf("$alt = %q, want %q", gotAltParam, injectedAlt)
		}
	})

	t.Run("ExecuteStream", func(t *testing.T) {
		stream, err := exec.ExecuteStream(context.Background(), auth, req, opts)
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if lastRawQuery != wantRawQuery {
			t.Fatalf("RawQuery = %q, want %q", lastRawQuery, wantRawQuery)
		}
		if hasInjected {
			t.Fatalf("unexpected injected query parameter in %q", lastRawQuery)
		}
		if gotAltParam != injectedAlt {
			t.Fatalf("$alt = %q, want %q", gotAltParam, injectedAlt)
		}
	})
}

func TestGeminiVertexExecutorAltEscape(t *testing.T) {
	const injectedAlt = "json&injected=1"
	const wantRawQuery = "$alt=json%26injected%3D1"
	const okResponse = `{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`

	var mu sync.Mutex
	var lastRawQuery string
	var hasInjected bool
	var gotAltParam string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lastRawQuery = r.URL.RawQuery
		q := r.URL.Query()
		hasInjected = q.Has("injected")
		gotAltParam = q.Get("$alt")
		mu.Unlock()

		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: "+okResponse+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, okResponse)
	}))
	defer server.Close()

	exec := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "vertex-alt-escape",
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-key",
			"base_url": server.URL,
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
		Alt:          injectedAlt,
	}

	t.Run("Execute", func(t *testing.T) {
		if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if lastRawQuery != wantRawQuery {
			t.Fatalf("RawQuery = %q, want %q", lastRawQuery, wantRawQuery)
		}
		if hasInjected {
			t.Fatalf("unexpected injected query parameter in %q", lastRawQuery)
		}
		if gotAltParam != injectedAlt {
			t.Fatalf("$alt = %q, want %q", gotAltParam, injectedAlt)
		}
	})

	t.Run("ExecuteStream", func(t *testing.T) {
		stream, err := exec.ExecuteStream(context.Background(), auth, req, opts)
		if err != nil {
			t.Fatalf("ExecuteStream() error = %v", err)
		}
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error = %v", chunk.Err)
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if lastRawQuery != wantRawQuery {
			t.Fatalf("RawQuery = %q, want %q", lastRawQuery, wantRawQuery)
		}
		if hasInjected {
			t.Fatalf("unexpected injected query parameter in %q", lastRawQuery)
		}
		if gotAltParam != injectedAlt {
			t.Fatalf("$alt = %q, want %q", gotAltParam, injectedAlt)
		}
	})
}
