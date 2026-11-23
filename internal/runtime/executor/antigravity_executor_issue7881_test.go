package executor

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	internalcache "github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/kvstore"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func enableIssue7881LocalKV(t *testing.T) {
	t.Helper()
	homekv.ClearCurrent()
	internalcache.ClearAntigravityReasoningReplayCache()
	if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
		t.Fatalf("kvstore.ShutdownLocal() setup error = %v", errShutdown)
	}
	dbPath := filepath.Join(t.TempDir(), "issue7881-kv.db")
	if errCfg := kvstore.ConfigureLocal(true, dbPath); errCfg != nil {
		t.Fatalf("kvstore.ConfigureLocal(true, %q) error = %v", dbPath, errCfg)
	}
	t.Cleanup(func() {
		internalcache.ClearAntigravityReasoningReplayCache()
		homekv.ClearCurrent()
		if errShutdown := kvstore.ShutdownLocal(); errShutdown != nil {
			t.Errorf("kvstore.ShutdownLocal() cleanup error = %v", errShutdown)
		}
	})
}

func newIssue7881TestAuth(id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       id,
		Provider: "antigravity",
		Metadata: map[string]any{
			"access_token":  "token-" + id,
			"refresh_token": "refresh-" + id,
			"project_id":    "project-1",
			"expired":       time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
}

type issue7881StepStreamBody struct {
	chunk      []byte
	sent       bool
	onSecondRd func() error
	closeOnce  sync.Once
}

func (b *issue7881StepStreamBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		n := copy(p, b.chunk)
		return n, nil
	}
	if b.onSecondRd != nil {
		return 0, b.onSecondRd()
	}
	return 0, io.EOF
}

func (b *issue7881StepStreamBody) Close() error {
	b.closeOnce.Do(func() {})
	return nil
}

func TestAntigravityIssue7881_ExecuteStreamCommitsReplayOnDisconnectAfterTerminalChunk(t *testing.T) {
	const (
		model = "gemini-3.8-flash-high"
	)
	signature := strings.Repeat("s", 128)

	tests := []struct {
		name      string
		secondErr func(ctx context.Context) error
	}{
		{
			name: "eof_after_terminal_chunk_with_canceled_ctx",
			secondErr: func(_ context.Context) error {
				return io.EOF
			},
		},
		{
			name: "scanner_canceled_after_terminal_chunk_before_eof",
			secondErr: func(ctx context.Context) error {
				return ctx.Err()
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enableIssue7881LocalKV(t)

			sessionID := "issue7881-" + tc.name
			exec := NewAntigravityExecutor(&config.Config{})
			auth := newIssue7881TestAuth("auth-" + tc.name)

			turn1Body := `{"model":"` + model + `","max_tokens":1024,"stream":true,"tools":[{"name":"lookup","description":"lookup key","input_schema":{"type":"object","properties":{"key":{"type":"string"}}}}],"messages":[{"role":"user","content":"hello"}]}`
			terminalSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true,"thoughtSignature":"` + signature + `"},{"functionCall":{"id":"call_issue7881","name":"lookup","args":{"key":"value"}},"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33},"modelVersion":"` + model + `","responseId":"resp-issue7881"}}` + "\n\n"

			ctx1, cancel1 := context.WithCancel(context.Background())
			defer cancel1()

			turn1Ctx := context.WithValue(ctx1, "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
				body := &issue7881StepStreamBody{
					chunk: []byte(terminalSSE),
					onSecondRd: func() error {
						// Second Read runs only after the terminal chunk has been emitted
						// to out and terminalDelivered is set to true on the same goroutine.
						cancel1()
						return tc.secondErr(ctx1)
					},
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       body,
				}, nil
			}))

			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatClaude,
				ResponseFormat: sdktranslator.FormatClaude,
				Stream:         true,
				Headers:        http.Header{"Session-Id": []string{sessionID}},
			}
			stream1, errStream1 := exec.ExecuteStream(turn1Ctx, auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(turn1Body),
			}, opts)
			if errStream1 != nil {
				t.Fatalf("Turn 1 ExecuteStream() error = %v", errStream1)
			}

			var sawMessageDelta bool
			for chunk := range stream1.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected stream error: %v", chunk.Err)
				}
				if strings.Contains(string(chunk.Payload), `"type":"message_delta"`) {
					sawMessageDelta = true
				}
			}
			if !sawMessageDelta {
				t.Fatal("expected Turn 1 stream to deliver message_delta terminal chunk before disconnect")
			}

			// Turn 2 omits client-side thinking/signature blocks and must recover
			// the thoughtSignature from KV replay cache committed on Turn 1.
			var capturedTurn2Request []byte
			turn2Ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				raw, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					return nil, errRead
				}
				capturedTurn2Request = append([]byte(nil), raw...)
				respSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":12,"candidatesTokenCount":5,"totalTokenCount":17},"modelVersion":"` + model + `","responseId":"resp-issue7881-turn2"}}` + "\n\n"
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(respSSE)),
				}, nil
			}))

			turn2Body := `{"model":"` + model + `","max_tokens":1024,"stream":true,"tools":[{"name":"lookup","description":"lookup key","input_schema":{"type":"object","properties":{"key":{"type":"string"}}}}],"messages":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"tool_use","id":"call_issue7881","name":"lookup","input":{"key":"value"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_issue7881","content":"found"}]}]}`
			stream2, errStream2 := exec.ExecuteStream(turn2Ctx, auth, cliproxyexecutor.Request{
				Model:   model,
				Payload: []byte(turn2Body),
			}, opts)
			if errStream2 != nil {
				t.Fatalf("Turn 2 ExecuteStream() error = %v", errStream2)
			}
			for chunk := range stream2.Chunks {
				if chunk.Err != nil {
					t.Fatalf("Turn 2 stream error: %v", chunk.Err)
				}
			}

			var restored bool
			for _, content := range ojson.GetBytesNoCopy(capturedTurn2Request, "request.contents").Array() {
				for _, part := range content.Get("parts").Array() {
					if part.Get("functionCall.name").String() == "lookup" && part.Get("thoughtSignature").String() == signature {
						restored = true
					}
				}
			}
			if !restored {
				t.Fatalf("Turn 2 missed thoughtSignature replay after Turn 1 terminal disconnect; upstream request = %s", string(capturedTurn2Request))
			}
		})
	}
}

func TestAntigravityIssue7881_CommitPersistsWithCanceledContextInKVMode(t *testing.T) {
	enableIssue7881LocalKV(t)

	const (
		model      = "gemini-3.8-flash-high"
		sessionKey = "responses:issue7881-direct-commit"
	)
	signature := strings.Repeat("a", 128)

	_, snapshot, found, errGet := internalcache.GetAntigravityReasoningReplayItemsWithSnapshotRequired(context.Background(), model, sessionKey)
	if errGet != nil || found {
		t.Fatalf("initial KV read = found %v err %v, want miss without error", found, errGet)
	}

	scope := antigravityReasoningReplayScope{
		modelName:     model,
		sessionKey:    sessionKey,
		cacheSnapshot: snapshot,
	}
	reqPayload := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
	acc := newAntigravityReasoningReplayAccumulator(scope, reqPayload)
	acc.ObserveSSELine([]byte(`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_direct","name":"lookup","args":{"k":"v"}},"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}]}}`))

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	acc.Commit(canceledCtx)

	items, persisted, errAfter := internalcache.GetAntigravityReasoningReplayItemsRequired(context.Background(), model, sessionKey)
	if errAfter != nil {
		t.Fatalf("post-commit KV read error = %v", errAfter)
	}
	if !persisted || len(items) == 0 {
		t.Fatalf("Commit(canceledCtx) dropped terminal replay state in KV mode: persisted=%v items=%d", persisted, len(items))
	}
	var hasSig bool
	for _, item := range items {
		if strings.Contains(string(item), signature) {
			hasSig = true
			break
		}
	}
	if !hasSig {
		t.Fatalf("persisted items did not contain expected thoughtSignature: %q", items)
	}
}

func TestAntigravityIssue7881_ExecuteStreamMidStreamCancelDoesNotCommitReplay(t *testing.T) {
	enableIssue7881LocalKV(t)

	const (
		model     = "gemini-3.8-flash-high"
		sessionID = "issue7881-midstream-cancel"
	)
	signature := strings.Repeat("m", 128)

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7881TestAuth("auth-midstream")

	reqBody := `{"model":"` + model + `","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hello"}]}`
	// Non-terminal chunk: no finishReason.
	partialSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"partial thought","thought":true,"thoughtSignature":"` + signature + `"}]}}],"modelVersion":"` + model + `","responseId":"resp-partial"}}` + "\n\n"

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamCtx := context.WithValue(ctx, "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		body := &issue7881StepStreamBody{
			chunk: []byte(partialSSE),
			onSecondRd: func() error {
				cancel()
				return ctx.Err()
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}, nil
	}))

	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
		Headers:        http.Header{"Session-Id": []string{sessionID}},
	}
	stream, errStream := exec.ExecuteStream(streamCtx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(reqBody),
	}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range stream.Chunks {
	}

	items, found, errGet := internalcache.GetAntigravityReasoningReplayItemsRequired(context.Background(), model, "responses:"+sessionID)
	if errGet != nil {
		t.Fatalf("KV read error = %v", errGet)
	}
	if found || len(items) != 0 {
		t.Fatalf("incomplete canceled stream must not commit replay items, got found=%v items=%d", found, len(items))
	}
}

func TestAntigravityIssue7881_ExecuteStreamUpstreamCanceledWithLiveCallerCtxDoesNotCommitReplay(t *testing.T) {
	enableIssue7881LocalKV(t)

	const (
		model     = "gemini-3.8-flash-high"
		sessionID = "issue7881-upstream-canceled-live-ctx"
	)
	signature := strings.Repeat("u", 128)

	exec := NewAntigravityExecutor(&config.Config{})
	auth := newIssue7881TestAuth("auth-upstream-cancel")

	reqBody := `{"model":"` + model + `","max_tokens":1024,"stream":true,"tools":[{"name":"lookup","description":"lookup key","input_schema":{"type":"object","properties":{"key":{"type":"string"}}}}],"messages":[{"role":"user","content":"hello"}]}`
	terminalSSE := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_upstream_cancel","name":"lookup","args":{"key":"value"}},"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33},"modelVersion":"` + model + `","responseId":"resp-upstream-cancel"}}` + "\n\n"

	// Caller context stays live while the upstream body returns context.Canceled
	// after the terminal chunk; the stream surfaces a read error to downstream
	// and must not commit replay state.
	streamCtx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		body := &issue7881StepStreamBody{
			chunk: []byte(terminalSSE),
			onSecondRd: func() error {
				return context.Canceled
			},
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       body,
		}, nil
	}))

	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
		Stream:         true,
		Headers:        http.Header{"Session-Id": []string{sessionID}},
	}
	stream, errStream := exec.ExecuteStream(streamCtx, auth, cliproxyexecutor.Request{
		Model:   model,
		Payload: []byte(reqBody),
	}, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}

	var sawErr bool
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected stream to surface read error when upstream returns context.Canceled with live caller ctx")
	}

	items, found, errGet := internalcache.GetAntigravityReasoningReplayItemsRequired(context.Background(), model, "responses:"+sessionID)
	if errGet != nil {
		t.Fatalf("KV read error = %v", errGet)
	}
	if found || len(items) != 0 {
		t.Fatalf("upstream context.Canceled with live caller ctx must not commit replay items, got found=%v items=%d", found, len(items))
	}
}
