package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
)

// TestCodexExecutorExecuteStream_ChunkBoundary_MidLineSplitFlushes pins a
// streaming-pipeline correctness invariant: the executor's bufio.Scanner
// must reassemble SSE event lines correctly even when the upstream flushes
// in the middle of a line. After Phase C streaming/logging refactors, this
// test gates the change.
//
// Scenario: a deterministic upstream sends a single SSE event split across
// FOUR writes with explicit flushes between them. The executor must emit
// the same set of downstream chunks as the same event sent in one write.
func TestCodexExecutorExecuteStream_ChunkBoundary_MidLineSplitFlushes(t *testing.T) {
	wholeEvent := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4-mini-2026-03-17\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":8,\"output_tokens\":1,\"total_tokens\":9}}}\n\n"

	splitWrite := func(w http.ResponseWriter, r *http.Request, parts []string) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("test server: ResponseWriter is not a Flusher")
			return
		}
		flusher.Flush()
		for _, part := range parts {
			if _, err := w.Write([]byte(part)); err != nil {
				t.Errorf("test server: write error: %v", err)
				return
			}
			flusher.Flush()
			// brief pause so the client genuinely sees split TCP chunks
			time.Sleep(2 * time.Millisecond)
		}
		_ = r.Context().Err()
	}

	collectChunks := func(t *testing.T, parts []string) [][]byte {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			splitWrite(w, r, parts)
		}))
		t.Cleanup(server.Close)

		executor := NewCodexExecutor(&config.Config{})
		auth := &cliproxyauth.Auth{Attributes: map[string]string{
			"base_url": server.URL,
			"api_key":  "test",
		}}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		t.Cleanup(cancel)

		stream, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
			Model:   "gpt-5.4-mini",
			Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`),
		}, cliproxyexecutor.Options{
			SourceFormat: sdktranslator.FromString("openai"),
			Stream:       true,
		})
		if err != nil {
			t.Fatalf("ExecuteStream error: %v", err)
		}

		var got [][]byte
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				t.Fatalf("stream chunk error: %v", chunk.Err)
			}
			got = append(got, chunk.Payload)
		}
		return got
	}

	// Reference run: server sends the whole event in one write+flush.
	reference := collectChunks(t, []string{wholeEvent})
	if len(reference) == 0 {
		t.Fatal("reference run produced zero chunks; sanity check failed")
	}

	// Split-flush run: send the same event in four mid-line splits.
	mid := len(wholeEvent) / 2
	quarter := len(wholeEvent) / 4
	threeQuarter := len(wholeEvent) - quarter
	parts := []string{
		wholeEvent[:quarter],
		wholeEvent[quarter:mid],
		wholeEvent[mid:threeQuarter],
		wholeEvent[threeQuarter:],
	}
	split := collectChunks(t, parts)

	if len(split) != len(reference) {
		t.Fatalf("split run produced %d chunks; reference produced %d", len(split), len(reference))
	}
	for i := range reference {
		if string(split[i]) != string(reference[i]) {
			t.Fatalf("chunk %d mismatch:\n  reference: %q\n  split:     %q", i, reference[i], split[i])
		}
	}
}

// TestCodexExecutorExecuteStream_ChunkBoundary_MultipleEventsSingleWrite
// pins the inverse: when the upstream writes multiple complete SSE events
// in a single write, the executor must emit one logical chunk batch per
// event (not lose events, not concatenate them into one).
func TestCodexExecutorExecuteStream_ChunkBoundary_MultipleEventsSingleWrite(t *testing.T) {
	events := []string{
		`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"}]},"output_index":0}`,
		`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"b"}]},"output_index":1}`,
		`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":1775555723,"status":"completed","model":"gpt-5.4-mini-2026-03-17","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":8,"output_tokens":2,"total_tokens":10}}}`,
	}
	body := strings.Join(events, "\n") + "\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)

	stream, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4-mini",
		Payload: []byte(`{"model":"gpt-5.4-mini","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var chunks [][]byte
	for c := range stream.Chunks {
		if c.Err != nil {
			t.Fatalf("chunk error: %v", c.Err)
		}
		chunks = append(chunks, c.Payload)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected at least one downstream chunk for %d upstream events, got 0", len(events))
	}
	// Sanity: at least the completion event surfaces a non-empty payload.
	hasCompletion := false
	for _, c := range chunks {
		if strings.Contains(string(c), "response.completed") || strings.Contains(string(c), `"finish_reason"`) || strings.Contains(string(c), `"object":"chat.completion"`) {
			hasCompletion = true
			break
		}
	}
	if !hasCompletion {
		t.Fatalf("none of %d chunks contained completion marker; chunks: %v", len(chunks), chunks)
	}
}
