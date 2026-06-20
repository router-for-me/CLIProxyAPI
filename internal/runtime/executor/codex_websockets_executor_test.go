package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestBuildCodexWebsocketRequestBodyPreservesPreviousResponseID(t *testing.T) {
	body := []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1"}]}`)

	wsReqBody := buildCodexWebsocketRequestBody(body)

	if got := gjson.GetBytes(wsReqBody, "type").String(); got != "response.create" {
		t.Fatalf("type = %s, want response.create", got)
	}
	if got := gjson.GetBytes(wsReqBody, "previous_response_id").String(); got != "resp-1" {
		t.Fatalf("previous_response_id = %s, want resp-1", got)
	}
	if gjson.GetBytes(wsReqBody, "input.0.id").String() != "msg-1" {
		t.Fatalf("input item id mismatch")
	}
	if got := gjson.GetBytes(wsReqBody, "type").String(); got == "response.append" {
		t.Fatalf("unexpected websocket request type: %s", got)
	}
}

func TestSanitizeCodexHTTPFallbackPayloadDropsWebSearchAction(t *testing.T) {
	payload := []byte(`{"type":"response.create","model":"gpt-5-codex","generate":false,"input":[{"type":"message","id":"msg-1"},{"type":"web_search_call","id":"ws-1","status":"completed","action":{"type":"search","query":"weather"}}]}`)

	sanitized := sanitizeCodexHTTPFallbackPayload(payload)

	if gjson.GetBytes(sanitized, "type").Exists() {
		t.Fatalf("websocket request type leaked into HTTP fallback: %s", sanitized)
	}
	if gjson.GetBytes(sanitized, "generate").Exists() {
		t.Fatalf("generate leaked into HTTP fallback: %s", sanitized)
	}
	if gjson.GetBytes(sanitized, "input.1.action").Exists() {
		t.Fatalf("web search action leaked into HTTP fallback input: %s", sanitized)
	}
	if got := gjson.GetBytes(sanitized, "input.1.type").String(); got != "web_search_call" {
		t.Fatalf("input.1.type = %s, want web_search_call", got)
	}
	if got := gjson.GetBytes(sanitized, "input.1.id").String(); got != "ws-1" {
		t.Fatalf("input.1.id = %s, want ws-1", got)
	}
}

func TestCodexWebsocketsExecutePreservesPreviousResponseIDUpstream(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("request path = %s, want /responses", r.URL.Path)
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Fatalf("upgrade websocket: %v", err)
		}
		defer func() { _ = conn.Close() }()

		msgType, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read upstream websocket message: %v", err)
		}
		if msgType != websocket.TextMessage {
			t.Fatalf("message type = %d, want text", msgType)
		}
		capturedPayload <- bytes.Clone(payload)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Fatalf("write completed websocket message: %v", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","id":"msg-1"}]}`),
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("codex")}

	if _, err := exec.Execute(context.Background(), auth, req, opts); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("upstream type = %s, want response.create; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "resp-1" {
			t.Fatalf("upstream previous_response_id = %s, want resp-1; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
}

func TestCodexWebsocketsExecuteStreamPassesThroughUpstreamWebsocketPayloadForDownstreamWebsocket(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	delta := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, delta); errWrite != nil {
			t.Errorf("write delta websocket message: %v", errWrite)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write completed websocket message: %v", errWrite)
			return
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before first chunk")
		}
		if chunk.Err != nil {
			t.Fatalf("first chunk error = %v", chunk.Err)
		}
		if !bytes.Equal(bytes.TrimSpace(chunk.Payload), delta) {
			t.Fatalf("first chunk = %q, want raw upstream websocket payload %q", chunk.Payload, delta)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first stream chunk")
	}
}

func TestCodexWebsocketsExecuteStreamPropagatesUpstreamErrorForDownstreamWebsocket(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errorPayload := []byte(`{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached","message":"too many websockets"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, errorPayload); errWrite != nil {
			t.Errorf("write error websocket message: %v", errWrite)
			return
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before error chunk")
		}
		if len(bytes.TrimSpace(chunk.Payload)) != 0 {
			t.Fatalf("error chunk payload = %q, want empty", chunk.Payload)
		}
		if chunk.Err == nil {
			t.Fatal("error chunk Err = nil, want upstream error")
		}
		statusErr, ok := chunk.Err.(interface{ StatusCode() int })
		if !ok {
			t.Fatalf("error type %T does not expose StatusCode", chunk.Err)
		}
		if got := statusErr.StatusCode(); got != http.StatusTooManyRequests {
			t.Fatalf("status = %d, want %d", got, http.StatusTooManyRequests)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for error stream chunk")
	}
}

func TestCodexWebsocketsExecuteStreamReturnsStatuslessUpstreamError(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errorPayload := []byte(`{"type":"error","error":{"message":"statusless upstream failed"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, errorPayload); errWrite != nil {
			t.Errorf("write error websocket message: %v", errWrite)
			return
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-statusless-error-stream"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}

	result, err := exec.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before statusless error chunk")
		}
		if len(bytes.TrimSpace(chunk.Payload)) != 0 {
			t.Fatalf("error chunk payload = %q, want empty", chunk.Payload)
		}
		statusErr, ok := chunk.Err.(interface{ StatusCode() int })
		if !ok {
			t.Fatalf("error type %T does not expose StatusCode", chunk.Err)
		}
		if got := statusErr.StatusCode(); got != http.StatusInternalServerError {
			t.Fatalf("status = %d, want %d", got, http.StatusInternalServerError)
		}
		if !strings.Contains(chunk.Err.Error(), "statusless upstream failed") {
			t.Fatalf("error = %v, want upstream message", chunk.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for statusless error stream chunk")
	}

	select {
	case errDisconnect, ok := <-disconnectCh:
		if !ok {
			t.Fatal("disconnect channel closed before delivering error")
		}
		statusErr, ok := errDisconnect.(interface{ StatusCode() int })
		if !ok || statusErr.StatusCode() != http.StatusInternalServerError {
			t.Fatalf("disconnect error = %v, want status 500", errDisconnect)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream disconnect signal")
	}
}

func TestCodexWebsocketsExecuteReturnsStatuslessUpstreamError(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errorPayload := []byte(`{"type":"error","error":{"message":"statusless upstream failed"}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()

		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			t.Errorf("read upstream websocket message: %v", errRead)
			return
		}
		if errWrite := conn.WriteMessage(websocket.TextMessage, errorPayload); errWrite != nil {
			t.Errorf("write error websocket message: %v", errWrite)
			return
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-statusless-error"
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := exec.Execute(ctx, auth, req, opts)
	if err == nil {
		t.Fatal("Execute() error = nil, want statusless upstream error")
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type %T does not expose StatusCode", err)
	}
	if got := statusErr.StatusCode(); got != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", got, http.StatusInternalServerError)
	}
	if !strings.Contains(err.Error(), "statusless upstream failed") {
		t.Fatalf("error = %v, want upstream message", err)
	}
}

func TestCodexWebsocketsExecuteDoesNotReplayAfterPartialPayload(t *testing.T) {
	tests := []struct {
		name              string
		closeCode         int
		wantMessageTooBig bool
	}{
		{
			name:      "normal_close",
			closeCode: websocket.CloseNormalClosure,
		},
		{
			name:              "message_too_big",
			closeCode:         websocket.CloseMessageTooBig,
			wantMessageTooBig: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			errCh := make(chan error, 4)
			var websocketConnections atomic.Int32
			var httpFallbackRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
					http.Error(w, "not found", http.StatusNotFound)
					return
				}

				if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					body, _ := io.ReadAll(r.Body)
					httpFallbackRequests.Add(1)
					errCh <- fmt.Errorf("unexpected HTTP fallback request: %s", body)
					http.Error(w, "unexpected fallback", http.StatusInternalServerError)
					return
				}

				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					errCh <- fmt.Errorf("upgrade websocket: %w", err)
					return
				}
				defer func() { _ = conn.Close() }()

				connectionIndex := websocketConnections.Add(1)
				if connectionIndex != 1 {
					errCh <- fmt.Errorf("unexpected websocket retry connection: %d", connectionIndex)
					return
				}
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					errCh <- fmt.Errorf("read upstream websocket message: %w", errRead)
					return
				}
				created := []byte(`{"type":"response.created","response":{"id":"resp-created","output":[]}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, created); errWrite != nil {
					errCh <- fmt.Errorf("write created websocket message: %w", errWrite)
					return
				}
				closePayload := websocket.FormatCloseMessage(tc.closeCode, "after created")
				if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
					errCh <- fmt.Errorf("write close after partial payload: %w", errWrite)
				}
			}))
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			sessionID := "sess-partial-payload-" + tc.name
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
			req := cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: []byte(`{"type":"response.create","model":"gpt-5-codex","generate":false,"input":[{"type":"message","role":"user","content":"hello"}]}`),
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}
			ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

			_, err := exec.Execute(ctx, auth, req, opts)
			if err == nil {
				t.Fatal("Execute() error = nil, want websocket close error")
			}
			if tc.wantMessageTooBig && !isCodexWebsocketMessageTooBigError(err) {
				t.Fatalf("Execute() error = %v, want websocket 1009 error", err)
			}
			if got := websocketConnections.Load(); got != 1 {
				t.Fatalf("websocket connection count = %d, want 1", got)
			}
			if got := httpFallbackRequests.Load(); got != 0 {
				t.Fatalf("HTTP fallback request count = %d, want 0", got)
			}
			select {
			case errServer := <-errCh:
				t.Fatal(errServer)
			default:
			}
		})
	}
}

func TestCodexWebsocketsExecuteStreamFallsBackToHTTPOnMessageTooBig(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	wsPayloadCh := make(chan []byte, 1)
	httpPayloadCh := make(chan []byte, 1)
	errCh := make(chan error, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				errCh <- fmt.Errorf("upgrade websocket: %w", err)
				return
			}
			defer func() { _ = conn.Close() }()

			msgType, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				errCh <- fmt.Errorf("read upstream websocket message: %w", errRead)
				return
			}
			if msgType != websocket.TextMessage {
				errCh <- fmt.Errorf("message type = %d, want text", msgType)
				return
			}
			wsPayloadCh <- bytes.Clone(payload)

			closePayload := websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message too big")
			if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
				errCh <- fmt.Errorf("write close 1009: %w", errWrite)
			}
			return
		}

		if r.Method != http.MethodPost {
			errCh <- fmt.Errorf("HTTP fallback method = %s, want POST", r.Method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			errCh <- fmt.Errorf("read HTTP fallback body: %w", errRead)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		httpPayloadCh <- bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-http\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-fallback-1009"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"type":"response.create","model":"gpt-5-codex","generate":false,"input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}

	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before HTTP fallback chunk")
		}
		if chunk.Err != nil {
			t.Fatalf("fallback chunk error = %v", chunk.Err)
		}
		if !bytes.Contains(chunk.Payload, []byte(`"type":"response.completed"`)) {
			t.Fatalf("fallback chunk = %q, want response.completed", chunk.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP fallback chunk")
	}
	select {
	case chunk, ok := <-result.Chunks:
		if ok {
			t.Fatalf("unexpected extra fallback chunk: payload=%s err=%v", chunk.Payload, chunk.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP fallback stream to close")
	}
	select {
	case payload := <-wsPayloadCh:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("websocket payload type = %s, want response.create; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream websocket payload")
	}
	select {
	case payload := <-httpPayloadCh:
		if got := gjson.GetBytes(payload, "model").String(); got != "gpt-5-codex" {
			t.Fatalf("HTTP fallback model = %s, want gpt-5-codex; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != "" {
			t.Fatalf("HTTP fallback type = %s, want empty; payload=%s", got, payload)
		}
		if gjson.GetBytes(payload, "generate").Exists() {
			t.Fatalf("generate leaked into HTTP fallback payload: %s", payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP fallback payload")
	}
	select {
	case errServer := <-errCh:
		t.Fatal(errServer)
	default:
	}
	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("upstream disconnect signaled during HTTP fallback: err=%v ok=%v", errDisconnect, ok)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestCodexWebsocketsExecuteStreamRetriesWebsocketAfterMessageTooBigFallback(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	wsPayloadCh := make(chan []byte, 2)
	httpPayloadCh := make(chan []byte, 1)
	errCh := make(chan error, 4)
	var websocketConnections atomic.Int32
	var httpRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				errCh <- fmt.Errorf("upgrade websocket: %w", err)
				return
			}
			defer func() { _ = conn.Close() }()

			connectionIndex := websocketConnections.Add(1)
			msgType, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				errCh <- fmt.Errorf("read upstream websocket message %d: %w", connectionIndex, errRead)
				return
			}
			if msgType != websocket.TextMessage {
				errCh <- fmt.Errorf("message type = %d, want text", msgType)
				return
			}
			wsPayloadCh <- bytes.Clone(payload)

			switch connectionIndex {
			case 1:
				closePayload := websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message too big")
				if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
					errCh <- fmt.Errorf("write close 1009: %w", errWrite)
				}
			case 2:
				completed := []byte(`{"type":"response.completed","response":{"id":"resp-ws","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
					errCh <- fmt.Errorf("write second websocket completed message: %w", errWrite)
				}
			default:
				errCh <- fmt.Errorf("unexpected websocket connection index: %d", connectionIndex)
			}
			return
		}

		httpRequests.Add(1)
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			errCh <- fmt.Errorf("read HTTP fallback body: %w", errRead)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		httpPayloadCh <- bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-http\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-retry-ws-after-1009-fallback"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	firstReq := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"type":"response.create","model":"gpt-5-codex","generate":false,"input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	result, err := exec.ExecuteStream(ctx, auth, firstReq, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() first request error = %v", err)
	}
	assertCodexWebsocketCompletedChunk(t, result, "resp-http")

	secondReq := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"type":"response.create","model":"gpt-5-codex","previous_response_id":"resp-http","input":[{"type":"message","role":"user","content":"next"}]}`),
	}
	result, err = exec.ExecuteStream(ctx, auth, secondReq, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() second request error = %v", err)
	}
	assertCodexWebsocketCompletedChunk(t, result, "resp-ws")

	select {
	case payload := <-wsPayloadCh:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("first websocket payload type = %s, want response.create; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "" {
			t.Fatalf("first websocket previous_response_id = %s, want empty; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first upstream websocket payload")
	}
	select {
	case payload := <-wsPayloadCh:
		if got := gjson.GetBytes(payload, "type").String(); got != "response.create" {
			t.Fatalf("second websocket payload type = %s, want response.create; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "previous_response_id").String(); got != "resp-http" {
			t.Fatalf("second websocket previous_response_id = %s, want resp-http; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for second upstream websocket payload")
	}
	select {
	case payload := <-httpPayloadCh:
		if got := gjson.GetBytes(payload, "type").String(); got != "" {
			t.Fatalf("HTTP fallback type = %s, want empty; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP fallback payload")
	}
	if got := websocketConnections.Load(); got != 2 {
		t.Fatalf("websocket connection count = %d, want 2", got)
	}
	if got := httpRequests.Load(); got != 1 {
		t.Fatalf("HTTP fallback request count = %d, want 1", got)
	}
	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("upstream disconnect signaled during retry after HTTP fallback: err=%v ok=%v", errDisconnect, ok)
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case errServer := <-errCh:
		t.Fatal(errServer)
	default:
	}
}

func TestCodexWebsocketsInactiveMessageTooBigDoesNotCloseDownstreamBeforeFallback(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	httpPayloadCh := make(chan []byte, 1)
	errCh := make(chan error, 4)
	var websocketConnections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				errCh <- fmt.Errorf("upgrade websocket: %w", err)
				return
			}
			defer func() { _ = conn.Close() }()

			connectionIndex := websocketConnections.Add(1)
			switch connectionIndex {
			case 1:
				closePayload := websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message too big")
				if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
					errCh <- fmt.Errorf("write inactive close 1009: %w", errWrite)
				}
			case 2:
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					errCh <- fmt.Errorf("read active upstream websocket message: %w", errRead)
					return
				}
				closePayload := websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message too big")
				if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
					errCh <- fmt.Errorf("write active close 1009: %w", errWrite)
				}
			default:
				errCh <- fmt.Errorf("unexpected websocket connection index: %d", connectionIndex)
			}
			return
		}

		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			errCh <- fmt.Errorf("read HTTP fallback body: %w", errRead)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		httpPayloadCh <- bytes.Clone(body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-http\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-inactive-1009"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	wsURL, err := buildCodexResponsesWebsocketURL(server.URL + "/responses")
	if err != nil {
		t.Fatalf("build websocket URL: %v", err)
	}
	sess := exec.getOrCreateSession(sessionID)
	if sess == nil {
		t.Fatal("expected session")
	}
	if _, _, errDial := exec.ensureUpstreamConn(context.Background(), auth, sess, "auth-test", wsURL, http.Header{}); errDial != nil {
		t.Fatalf("ensureUpstreamConn() error = %v", errDial)
	}
	waitForCodexWebsocketSessionConnCleared(t, sess)

	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("upstream disconnect signaled for inactive 1009: err=%v ok=%v", errDisconnect, ok)
	default:
	}

	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"type":"response.create","model":"gpt-5-codex","generate":false,"input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	assertCodexWebsocketCompletedChunk(t, result, "resp-http")

	select {
	case payload := <-httpPayloadCh:
		if got := gjson.GetBytes(payload, "type").String(); got != "" {
			t.Fatalf("HTTP fallback type = %s, want empty; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for HTTP fallback payload")
	}
	if got := websocketConnections.Load(); got != 2 {
		t.Fatalf("websocket connection count = %d, want 2", got)
	}
	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("upstream disconnect signaled during HTTP fallback: err=%v ok=%v", errDisconnect, ok)
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case errServer := <-errCh:
		t.Fatal(errServer)
	default:
	}
}

func TestCodexWebsocketsExecuteStreamRetriesFastFollowAfterTerminalClose(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errCh := make(chan error, 8)
	oldConnSecondRequestCh := make(chan struct{}, 1)
	var websocketConnections atomic.Int32
	var websocketRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			errCh <- fmt.Errorf("unexpected non-websocket request")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- fmt.Errorf("upgrade websocket: %w", err)
			return
		}
		defer func() { _ = conn.Close() }()

		connectionIndex := websocketConnections.Add(1)
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			errCh <- fmt.Errorf("read upstream websocket message: %w", errRead)
			return
		}
		websocketRequests.Add(1)

		switch connectionIndex {
		case 1:
			completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				errCh <- fmt.Errorf("write first completed websocket message: %w", errWrite)
				return
			}
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				errCh <- fmt.Errorf("read fast-follow websocket message: %w", errRead)
				return
			}
			websocketRequests.Add(1)
			oldConnSecondRequestCh <- struct{}{}
			closePayload := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done")
			if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
				errCh <- fmt.Errorf("write close after terminal: %w", errWrite)
			}
		case 2:
			completed := []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
			if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
				errCh <- fmt.Errorf("write retry completed websocket message: %w", errWrite)
				return
			}
		default:
			errCh <- fmt.Errorf("unexpected websocket connection index: %d", connectionIndex)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-terminal-close"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() first request error = %v", err)
	}
	assertCodexWebsocketCompletedChunk(t, result, "resp-1")

	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("upstream disconnect signaled after terminal close: err=%v ok=%v", errDisconnect, ok)
	default:
	}

	result, err = exec.ExecuteStream(ctx, auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() second request error = %v", err)
	}
	assertCodexWebsocketCompletedChunk(t, result, "resp-2")

	select {
	case <-oldConnSecondRequestCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for fast-follow request on old websocket")
	}
	if got := websocketConnections.Load(); got != 2 {
		t.Fatalf("websocket connection count = %d, want 2", got)
	}
	if got := websocketRequests.Load(); got != 3 {
		t.Fatalf("websocket request count = %d, want 3", got)
	}
	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("upstream disconnect signaled after reconnect: err=%v ok=%v", errDisconnect, ok)
	default:
	}
	select {
	case errServer := <-errCh:
		t.Fatal(errServer)
	default:
	}
}

func TestCodexWebsocketsExecuteStreamDoesNotRetryIncrementalRequestAfterStaleTerminalClose(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "response_append",
			payload: []byte(`{"type":"response.append","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"next"}]}`),
		},
		{
			name:    "incremental_response_create",
			payload: []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"next"}]}`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			errCh := make(chan error, 8)
			oldConnFollowUpRequestCh := make(chan struct{}, 1)
			var websocketConnections atomic.Int32
			var websocketRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					errCh <- fmt.Errorf("unexpected non-websocket request")
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}

				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					errCh <- fmt.Errorf("upgrade websocket: %w", err)
					return
				}
				defer func() { _ = conn.Close() }()

				connectionIndex := websocketConnections.Add(1)
				if connectionIndex != 1 {
					errCh <- fmt.Errorf("unexpected websocket connection index: %d", connectionIndex)
					return
				}
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					errCh <- fmt.Errorf("read first websocket message: %w", errRead)
					return
				}
				websocketRequests.Add(1)

				completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
					errCh <- fmt.Errorf("write first completed websocket message: %w", errWrite)
					return
				}
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					errCh <- fmt.Errorf("read follow-up websocket message: %w", errRead)
					return
				}
				websocketRequests.Add(1)
				oldConnFollowUpRequestCh <- struct{}{}
				closePayload := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done")
				if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
					errCh <- fmt.Errorf("write close after terminal: %w", errWrite)
				}
			}))
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			sessionID := "sess-terminal-close-incremental-" + tc.name
			disconnectCh := exec.UpstreamDisconnectChan(sessionID)
			if disconnectCh == nil {
				t.Fatal("expected disconnect channel")
			}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
			createReq := cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
			}
			followUpReq := cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: tc.payload,
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}
			ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

			result, err := exec.ExecuteStream(ctx, auth, createReq, opts)
			if err != nil {
				t.Fatalf("ExecuteStream() first request error = %v", err)
			}
			assertCodexWebsocketCompletedChunk(t, result, "resp-1")

			select {
			case errDisconnect, ok := <-disconnectCh:
				t.Fatalf("upstream disconnect signaled after first terminal event: err=%v ok=%v", errDisconnect, ok)
			default:
			}

			result, err = exec.ExecuteStream(ctx, auth, followUpReq, opts)
			if err != nil {
				t.Fatalf("ExecuteStream() follow-up request error = %v", err)
			}

			select {
			case <-oldConnFollowUpRequestCh:
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for follow-up request on old websocket")
			}
			select {
			case chunk, ok := <-result.Chunks:
				if !ok {
					t.Fatal("follow-up stream closed before stale terminal close error")
				}
				if !isCodexWebsocketStaleTerminalCloseError(chunk.Err) {
					t.Fatalf("follow-up chunk Err = %v, want stale terminal close error", chunk.Err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for follow-up stale terminal close error")
			}
			select {
			case errDisconnect, ok := <-disconnectCh:
				if !ok {
					t.Fatal("disconnect channel closed before delivering error")
				}
				if !isCodexWebsocketStaleTerminalCloseError(errDisconnect) {
					t.Fatalf("disconnect error = %v, want stale terminal close error", errDisconnect)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for upstream disconnect signal")
			}
			if got := websocketConnections.Load(); got != 1 {
				t.Fatalf("websocket connection count = %d, want 1", got)
			}
			if got := websocketRequests.Load(); got != 2 {
				t.Fatalf("websocket request count = %d, want 2", got)
			}
			select {
			case errServer := <-errCh:
				t.Fatal(errServer)
			default:
			}
		})
	}
}

func TestCodexWebsocketsExecuteStreamRejectsAppendAfterUpstreamStateCleared(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errCh := make(chan error, 8)
	var websocketConnections atomic.Int32
	var websocketRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			errCh <- fmt.Errorf("unexpected non-websocket request")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- fmt.Errorf("upgrade websocket: %w", err)
			return
		}
		defer func() { _ = conn.Close() }()

		connectionIndex := websocketConnections.Add(1)
		if connectionIndex != 1 {
			errCh <- fmt.Errorf("unexpected websocket connection index: %d", connectionIndex)
			return
		}
		if _, _, errRead := conn.ReadMessage(); errRead != nil {
			errCh <- fmt.Errorf("read first websocket message: %w", errRead)
			return
		}
		websocketRequests.Add(1)

		completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			errCh <- fmt.Errorf("write completed websocket message: %w", errWrite)
			return
		}
		closePayload := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done")
		if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
			errCh <- fmt.Errorf("write close after terminal: %w", errWrite)
		}
	}))
	defer server.Close()

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-terminal-close-append-after-clear"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	createReq := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
	}
	appendReq := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"type":"response.append","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"next"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, createReq, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() first request error = %v", err)
	}
	assertCodexWebsocketCompletedChunk(t, result, "resp-1")
	waitForCodexWebsocketSessionConnCleared(t, exec.getOrCreateSession(sessionID))

	select {
	case errDisconnect, ok := <-disconnectCh:
		t.Fatalf("upstream disconnect signaled after terminal close: err=%v ok=%v", errDisconnect, ok)
	default:
	}

	result, err = exec.ExecuteStream(ctx, auth, appendReq, opts)
	if err == nil {
		t.Fatalf("ExecuteStream() append request error = nil, result=%v", result)
	}
	if !isCodexWebsocketRequestWithoutUpstreamContextError(err) {
		t.Fatalf("ExecuteStream() append request error = %v, want append without upstream context", err)
	}

	select {
	case errDisconnect, ok := <-disconnectCh:
		if !ok {
			t.Fatal("disconnect channel closed before delivering error")
		}
		if !isCodexWebsocketRequestWithoutUpstreamContextError(errDisconnect) {
			t.Fatalf("disconnect error = %v, want append without upstream context", errDisconnect)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream disconnect signal")
	}
	if got := websocketConnections.Load(); got != 1 {
		t.Fatalf("websocket connection count = %d, want 1", got)
	}
	if got := websocketRequests.Load(); got != 1 {
		t.Fatalf("websocket request count = %d, want 1", got)
	}
	select {
	case errServer := <-errCh:
		t.Fatal(errServer)
	default:
	}
}

func TestCodexWebsocketsExecuteStreamRejectsPreviousResponseIDAfterUpstreamStateCleared(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{
			name:    "response_create",
			payload: []byte(`{"type":"response.create","model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":"next"}]}`),
		},
		{
			name:    "response_append",
			payload: []byte(`{"type":"response.append","model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":"next"}]}`),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			errCh := make(chan error, 8)
			var websocketConnections atomic.Int32
			var websocketRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					errCh <- fmt.Errorf("unexpected non-websocket request")
					http.Error(w, "bad request", http.StatusBadRequest)
					return
				}

				conn, err := upgrader.Upgrade(w, r, nil)
				if err != nil {
					errCh <- fmt.Errorf("upgrade websocket: %w", err)
					return
				}
				defer func() { _ = conn.Close() }()

				connectionIndex := websocketConnections.Add(1)
				if connectionIndex != 1 {
					errCh <- fmt.Errorf("unexpected websocket connection index: %d", connectionIndex)
					return
				}
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					errCh <- fmt.Errorf("read first websocket message: %w", errRead)
					return
				}
				websocketRequests.Add(1)

				completed := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
				if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
					errCh <- fmt.Errorf("write completed websocket message: %w", errWrite)
					return
				}
				closePayload := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done")
				if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
					errCh <- fmt.Errorf("write close after terminal: %w", errWrite)
				}
			}))
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			sessionID := "sess-terminal-close-previous-response-id-" + tc.name
			disconnectCh := exec.UpstreamDisconnectChan(sessionID)
			if disconnectCh == nil {
				t.Fatal("expected disconnect channel")
			}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
			createReq := cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: []byte(`{"model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"hello"}]}`),
			}
			req := cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: tc.payload,
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}
			ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

			result, err := exec.ExecuteStream(ctx, auth, createReq, opts)
			if err != nil {
				t.Fatalf("ExecuteStream() first request error = %v", err)
			}
			assertCodexWebsocketCompletedChunk(t, result, "resp-1")
			waitForCodexWebsocketSessionConnCleared(t, exec.getOrCreateSession(sessionID))

			result, err = exec.ExecuteStream(ctx, auth, req, opts)
			if err == nil {
				t.Fatalf("ExecuteStream() previous_response_id request error = nil, result=%v", result)
			}
			if !isCodexWebsocketRequestWithoutUpstreamContextError(err) {
				t.Fatalf("ExecuteStream() previous_response_id request error = %v, want request without upstream context", err)
			}

			select {
			case errDisconnect, ok := <-disconnectCh:
				if !ok {
					t.Fatal("disconnect channel closed before delivering error")
				}
				if !isCodexWebsocketRequestWithoutUpstreamContextError(errDisconnect) {
					t.Fatalf("disconnect error = %v, want request without upstream context", errDisconnect)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for upstream disconnect signal")
			}
			if got := websocketConnections.Load(); got != 1 {
				t.Fatalf("websocket connection count = %d, want 1", got)
			}
			if got := websocketRequests.Load(); got != 1 {
				t.Fatalf("websocket request count = %d, want 1", got)
			}
			select {
			case errServer := <-errCh:
				t.Fatal(errServer)
			default:
			}
		})
	}
}

func TestCodexWebsocketsExecuteStreamDoesNotRetryAppendAfterSendError(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	errCh := make(chan error, 4)
	var websocketConnections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			errCh <- fmt.Errorf("unexpected non-websocket request")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			errCh <- fmt.Errorf("upgrade websocket: %w", err)
			return
		}
		websocketConnections.Add(1)
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()

	wsURL, err := buildCodexResponsesWebsocketURL(strings.TrimSuffix(server.URL, "/") + "/responses")
	if err != nil {
		t.Fatalf("build websocket URL: %v", err)
	}
	staleConn, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
	closeHTTPResponseBody(resp, "test close handshake response body")
	if err != nil {
		t.Fatalf("dial stale websocket: %v", err)
	}
	if errClose := staleConn.Close(); errClose != nil {
		t.Fatalf("close stale websocket: %v", errClose)
	}

	exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-append-send-error"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}
	sess := exec.getOrCreateSession(sessionID)
	sess.connMu.Lock()
	sess.conn = staleConn
	sess.readerConn = staleConn
	sess.wsURL = wsURL
	sess.authID = "test-auth"
	sess.connMu.Unlock()

	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
	appendReq := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"type":"response.append","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"next"}]}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FromString("openai-response"),
		ResponseFormat: sdktranslator.FromString("openai-response"),
		Metadata: map[string]any{
			cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
		},
	}
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

	result, err := exec.ExecuteStream(ctx, auth, appendReq, opts)
	if err == nil {
		t.Fatalf("ExecuteStream() append request error = nil, result=%v", result)
	}
	if !isCodexWebsocketRequestWithoutUpstreamContextError(err) {
		t.Fatalf("ExecuteStream() append request error = %v, want append without upstream context", err)
	}

	select {
	case errDisconnect, ok := <-disconnectCh:
		if !ok {
			t.Fatal("disconnect channel closed before delivering error")
		}
		if !isCodexWebsocketRequestWithoutUpstreamContextError(errDisconnect) {
			t.Fatalf("disconnect error = %v, want append without upstream context", errDisconnect)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for upstream disconnect signal")
	}
	if got := websocketConnections.Load(); got != 1 {
		t.Fatalf("websocket connection count = %d, want 1", got)
	}
	select {
	case errServer := <-errCh:
		t.Fatal(errServer)
	default:
	}
}

func assertCodexWebsocketCompletedChunk(t *testing.T, result *cliproxyexecutor.StreamResult, responseID string) {
	t.Helper()
	if result == nil {
		t.Fatal("stream result is nil")
	}
	select {
	case chunk, ok := <-result.Chunks:
		if !ok {
			t.Fatal("stream closed before completed chunk")
		}
		if chunk.Err != nil {
			t.Fatalf("completed chunk error = %v", chunk.Err)
		}
		payload := bytes.TrimSpace(chunk.Payload)
		if got := gjson.GetBytes(payload, "type").String(); got != "response.completed" {
			t.Fatalf("completed chunk type = %s, want response.completed; payload=%s", got, payload)
		}
		if got := gjson.GetBytes(payload, "response.id").String(); got != responseID {
			t.Fatalf("completed response id = %s, want %s; payload=%s", got, responseID, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for completed chunk")
	}

	select {
	case chunk, ok := <-result.Chunks:
		if ok {
			t.Fatalf("unexpected extra chunk after terminal event: payload=%s err=%v", chunk.Payload, chunk.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for stream to close after terminal event")
	}
}

func waitForCodexWebsocketSessionConnCleared(t *testing.T, sess *codexWebsocketSession) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sess.connMu.Lock()
		conn := sess.conn
		sess.connMu.Unlock()
		if conn == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for websocket session conn to clear")
}

func TestCodexWebsocketsExecuteStreamDoesNotFallbackToHTTPForIncrementalRequest(t *testing.T) {
	tests := []struct {
		name                      string
		payload                   []byte
		wantAppendWithoutUpstream bool
	}{
		{
			name:    "previous_response_id",
			payload: []byte(`{"model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":"next"}]}`),
		},
		{
			name:                      "response_append",
			payload:                   []byte(`{"type":"response.append","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"next"}]}`),
			wantAppendWithoutUpstream: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			httpPayloadCh := make(chan []byte, 1)
			errCh := make(chan error, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/responses" {
					errCh <- fmt.Errorf("request path = %s, want /responses", r.URL.Path)
					http.Error(w, "not found", http.StatusNotFound)
					return
				}

				if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
					conn, err := upgrader.Upgrade(w, r, nil)
					if err != nil {
						errCh <- fmt.Errorf("upgrade websocket: %w", err)
						return
					}
					defer func() { _ = conn.Close() }()

					if _, _, errRead := conn.ReadMessage(); errRead != nil {
						errCh <- fmt.Errorf("read upstream websocket message: %w", errRead)
						return
					}
					closePayload := websocket.FormatCloseMessage(websocket.CloseMessageTooBig, "message too big")
					if errWrite := conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); errWrite != nil {
						errCh <- fmt.Errorf("write close 1009: %w", errWrite)
					}
					return
				}

				body, _ := io.ReadAll(r.Body)
				httpPayloadCh <- bytes.Clone(body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-http\",\"output\":[]}}\n\n"))
			}))
			defer server.Close()

			exec := NewCodexWebsocketsExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
			sessionID := "sess-incremental-1009-" + tc.name
			disconnectCh := exec.UpstreamDisconnectChan(sessionID)
			if disconnectCh == nil {
				t.Fatal("expected disconnect channel")
			}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-test", "base_url": server.URL}}
			req := cliproxyexecutor.Request{
				Model:   "gpt-5-codex",
				Payload: tc.payload,
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: sessionID,
				},
			}
			ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())

			result, err := exec.ExecuteStream(ctx, auth, req, opts)
			if tc.wantAppendWithoutUpstream {
				if err == nil {
					t.Fatalf("ExecuteStream() error = nil, result=%v", result)
				}
				if !isCodexWebsocketRequestWithoutUpstreamContextError(err) {
					t.Fatalf("ExecuteStream() error = %v, want append without upstream context", err)
				}
				select {
				case errDisconnect, ok := <-disconnectCh:
					if !ok {
						t.Fatal("disconnect channel closed before delivering error")
					}
					if !isCodexWebsocketRequestWithoutUpstreamContextError(errDisconnect) {
						t.Fatalf("disconnect error = %v, want append without upstream context", errDisconnect)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("timed out waiting for upstream disconnect signal")
				}
				select {
				case payload := <-httpPayloadCh:
					t.Fatalf("unexpected HTTP fallback payload for incremental request: %s", payload)
				case <-time.After(200 * time.Millisecond):
				}
				select {
				case errServer := <-errCh:
					t.Fatal(errServer)
				default:
				}
				return
			}
			if err != nil {
				t.Fatalf("ExecuteStream() error = %v", err)
			}

			select {
			case chunk, ok := <-result.Chunks:
				if !ok {
					t.Fatal("stream closed before websocket close error")
				}
				if chunk.Err == nil {
					t.Fatalf("chunk Err = nil, want websocket 1009 error; payload=%q", chunk.Payload)
				}
				if !isCodexWebsocketMessageTooBigError(chunk.Err) {
					t.Fatalf("chunk Err = %v, want websocket 1009 error", chunk.Err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for websocket close error")
			}

			select {
			case errDisconnect, ok := <-disconnectCh:
				if !ok {
					t.Fatal("disconnect channel closed before delivering error")
				}
				if !isCodexWebsocketMessageTooBigError(errDisconnect) {
					t.Fatalf("disconnect error = %v, want websocket 1009 error", errDisconnect)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for upstream disconnect signal")
			}

			select {
			case payload := <-httpPayloadCh:
				t.Fatalf("unexpected HTTP fallback payload for incremental request: %s", payload)
			case <-time.After(200 * time.Millisecond):
			}
			select {
			case errServer := <-errCh:
				t.Fatal(errServer)
			default:
			}
		})
	}
}

func TestCanFallbackCodexWebsocketRequestToHTTPBlocksLiveSessionCreate(t *testing.T) {
	liveConn := &websocket.Conn{}
	sess := &codexWebsocketSession{}
	sess.connMu.Lock()
	sess.conn = liveConn
	sess.terminalStateConn = liveConn
	sess.connMu.Unlock()

	incrementalCreate := []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"message","role":"user","content":"next"}]}`)
	if canFallbackCodexWebsocketRequestToHTTP(sess, liveConn, incrementalCreate) {
		t.Fatal("incremental response.create on a live terminal-state websocket should not fallback to HTTP")
	}

	if !canFallbackCodexWebsocketRequestToHTTP(nil, nil, incrementalCreate) {
		t.Fatal("initial response.create without live websocket state should fallback to HTTP")
	}

	replayCreate := []byte(`{"type":"response.create","model":"gpt-5-codex","input":[{"type":"compaction_summary","summary":"history"},{"type":"message","role":"user","content":"next"}]}`)
	if !canFallbackCodexWebsocketRequestToHTTP(sess, liveConn, replayCreate) {
		t.Fatal("transcript replay response.create should fallback to HTTP")
	}

	withPreviousResponseID := []byte(`{"type":"response.create","model":"gpt-5-codex","previous_response_id":"resp-1","input":[{"type":"message","role":"user","content":"next"}]}`)
	if canFallbackCodexWebsocketRequestToHTTP(sess, liveConn, withPreviousResponseID) {
		t.Fatal("previous_response_id request should not fallback to HTTP")
	}
}

func TestCodexWebsocketsUpstreamDisconnectChanSignalsOnInvalidate(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade websocket: %v", err)
			return
		}
		defer func() { _ = conn.Close() }()
		for {
			if _, _, errRead := conn.ReadMessage(); errRead != nil {
				return
			}
		}
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() { _ = conn.Close() }()

	exec := NewCodexWebsocketsExecutor(&config.Config{})
	sessionID := "sess-1"
	disconnectCh := exec.UpstreamDisconnectChan(sessionID)
	if disconnectCh == nil {
		t.Fatal("expected disconnect channel")
	}

	sess := exec.getOrCreateSession(sessionID)
	if sess == nil {
		t.Fatal("expected session")
	}
	sess.connMu.Lock()
	sess.conn = conn
	sess.authID = "auth-1"
	sess.wsURL = "ws://example.test/responses"
	sess.readerConn = conn
	sess.connMu.Unlock()

	upstreamErr := errors.New("upstream gone")
	exec.invalidateUpstreamConn(sess, conn, "test_invalidate", upstreamErr)

	select {
	case errRead, ok := <-disconnectCh:
		if !ok {
			t.Fatal("expected disconnect channel to deliver error before closing")
		}
		if errRead == nil || errRead.Error() != upstreamErr.Error() {
			t.Fatalf("disconnect error = %v, want %v", errRead, upstreamErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for disconnect signal")
	}
}

func TestCodexAutoExecutorDelegatesUpstreamSessionActive(t *testing.T) {
	exec := NewCodexAutoExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	sessionID := "sess-codex-auto-active"
	if exec.UpstreamSessionActive(sessionID) {
		t.Fatal("new auto executor session should not be active")
	}

	sess := exec.wsExec.getOrCreateSession(sessionID)
	conn := &websocket.Conn{}
	sess.connMu.Lock()
	sess.conn = conn
	sess.connMu.Unlock()

	if !exec.UpstreamSessionActive(sessionID) {
		t.Fatal("auto executor did not delegate active session lookup")
	}
}

func TestApplyCodexWebsocketHeadersDefaultsToCurrentResponsesBeta(t *testing.T) {
	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, nil, "", nil)

	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
	if got := headers.Get("User-Agent"); got != codexUserAgent {
		t.Fatalf("User-Agent = %s, want %s", got, codexUserAgent)
	}
	if !strings.HasPrefix(codexUserAgent, codexOriginator+"/") {
		t.Fatalf("default Codex User-Agent = %s, want prefix %s/", codexUserAgent, codexOriginator)
	}
	if !strings.HasPrefix(codexUserAgent, "codex-tui/") {
		t.Fatalf("default Codex User-Agent = %s, want codex-tui prefix", codexUserAgent)
	}
	if !strings.Contains(codexUserAgent, "(codex-tui;") {
		t.Fatalf("default Codex User-Agent = %s, want codex-tui suffix", codexUserAgent)
	}
	if got := headers.Get("Originator"); got != codexOriginator {
		t.Fatalf("Originator = %s, want %s", got, codexOriginator)
	}
	if got := headers.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want empty", got)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"User-Agent":            "codex_cli_rs/0.1.0",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
		"session-id":            "legacy-session",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", nil)

	if got := headers.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := headers.Get("User-Agent"); got != "codex_cli_rs/0.1.0" {
		t.Fatalf("User-Agent = %s, want %s", got, "codex_cli_rs/0.1.0")
	}
	if got := headers.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := headers.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := headers.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
	if got := headers["session_id"]; len(got) != 1 || got[0] != "legacy-session" {
		t.Fatalf("session_id = %#v, want [legacy-session]", got)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("Session-Id = %s, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersCanonicalizesLegacyUnderscoreSessionHeader(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"Originator": "Codex Desktop",
		"User-Agent": "codex_cli_rs/0.1.0",
		"Session_id": "legacy-underscore-session",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", nil)

	if got := headers["session_id"]; len(got) != 1 || got[0] != "legacy-underscore-session" {
		t.Fatalf("session_id = %#v, want [legacy-underscore-session]", got)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("Session-Id = %s, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersUsesConfigDefaultsForOAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "my-codex-client/1.0",
			BetaFeatures: "feature-a,feature-b",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "my-codex-client/1.0" {
		t.Fatalf("User-Agent = %s, want %s", got, "my-codex-client/1.0")
	}
	if got := headers.Get("x-codex-beta-features"); got != "feature-a,feature-b" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "feature-a,feature-b")
	}
	if got := headers.Get("OpenAI-Beta"); got != codexResponsesWebsocketBetaHeaderValue {
		t.Fatalf("OpenAI-Beta = %s, want %s", got, codexResponsesWebsocketBetaHeaderValue)
	}
}

func TestApplyCodexWebsocketHeadersPrefersExistingHeadersOverClientAndConfig(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})
	headers := http.Header{}
	headers.Set("User-Agent", "existing-ua")
	headers.Set("X-Codex-Beta-Features", "existing-beta")

	got := applyCodexWebsocketHeaders(ctx, headers, auth, "", cfg)

	if gotVal := got.Get("User-Agent"); gotVal != "existing-ua" {
		t.Fatalf("User-Agent = %s, want %s", gotVal, "existing-ua")
	}
	if gotVal := got.Get("x-codex-beta-features"); gotVal != "existing-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", gotVal, "existing-beta")
	}
}

func TestApplyCodexWebsocketHeadersConfigUserAgentOverridesClientHeader(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	ctx := contextWithGinHeaders(map[string]string{
		"User-Agent":            "client-ua",
		"X-Codex-Beta-Features": "client-beta",
	})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "", cfg)

	if got := headers.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := headers.Get("x-codex-beta-features"); got != "client-beta" {
		t.Fatalf("x-codex-beta-features = %s, want %s", got, "client-beta")
	}
}

func TestApplyCodexWebsocketHeadersIgnoresConfigForAPIKeyAuth(t *testing.T) {
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"api_key": "sk-test"},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "sk-test", cfg)

	if got := headers.Get("User-Agent"); got != "" {
		t.Fatalf("User-Agent = %s, want empty", got)
	}
	if got := headers.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
	if got := headers.Get("Originator"); got != "" {
		t.Fatalf("Originator = %s, want empty", got)
	}
}

func TestApplyCodexWebsocketHeadersPreservesExplicitAPIKeyUserAgent(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "codex", Attributes: map[string]string{"api_key": "sk-test"}}
	ctx := contextWithGinHeaders(map[string]string{"User-Agent": "api-key-client/1.0", "Originator": "explicit-origin"})

	headers := applyCodexWebsocketHeaders(ctx, http.Header{}, auth, "sk-test", nil)

	if got := headers.Get("User-Agent"); got != "api-key-client/1.0" {
		t.Fatalf("User-Agent = %s, want api-key-client/1.0", got)
	}
	if got := headers.Get("Originator"); got != "explicit-origin" {
		t.Fatalf("Originator = %s, want explicit-origin", got)
	}
}

func TestApplyCodexWebsocketHeadersUsesCanonicalAccountHeader(t *testing.T) {
	auth := &cliproxyauth.Auth{Provider: "codex", Metadata: map[string]any{"account_id": "acct-1"}}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "", nil)

	if got := headerValueCaseInsensitive(headers, "ChatGPT-Account-ID"); got != "acct-1" {
		t.Fatalf("ChatGPT-Account-ID = %s, want acct-1", got)
	}
	values, ok := headers["ChatGPT-Account-ID"]
	if !ok {
		t.Fatalf("expected exact ChatGPT-Account-ID key, got %#v", headers)
	}
	if len(values) != 1 || values[0] != "acct-1" {
		t.Fatalf("ChatGPT-Account-ID values = %#v, want [acct-1]", values)
	}
}

func TestApplyCodexPromptCacheHeadersSetsSessionIDAndLegacyConversation(t *testing.T) {
	req := cliproxyexecutor.Request{Model: "gpt-5-codex", Payload: []byte(`{"prompt_cache_key":"cache-1"}`)}

	_, headers := applyCodexPromptCacheHeaders("openai-response", req, []byte(`{"model":"gpt-5-codex"}`))

	if got := headers["session_id"]; len(got) != 1 || got[0] != "cache-1" {
		t.Fatalf("session_id = %#v, want [cache-1]", got)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("Session-Id = %s, want empty", got)
	}
	if got := headers.Get("Conversation_id"); got != "cache-1" {
		t.Fatalf("Conversation_id = %s, want cache-1", got)
	}
}

func TestApplyCodexPromptCacheHeadersClaudeUsesClaudeCodeSessionID(t *testing.T) {
	firstReq := cliproxyexecutor.Request{
		Model: "gpt-5-codex-claude-ws-cache-session",
		Payload: []byte(`{
			"metadata":{"user_id":"{\"device_id\":\"device-a\",\"account_uuid\":\"\",\"session_id\":\"ws-cache-session-1\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"first"}]}]
		}`),
	}
	secondReq := cliproxyexecutor.Request{
		Model: "gpt-5-codex-claude-ws-cache-session",
		Payload: []byte(`{
			"metadata":{"user_id":"{\"device_id\":\"device-b\",\"account_uuid\":\"\",\"session_id\":\"ws-cache-session-1\"}"},
			"messages":[{"role":"user","content":[{"type":"text","text":"next"}]}]
		}`),
	}

	firstBody, firstHeaders := applyCodexPromptCacheHeaders("claude", firstReq, []byte(`{"model":"gpt-5-codex"}`))
	secondBody, secondHeaders := applyCodexPromptCacheHeaders("claude", secondReq, []byte(`{"model":"gpt-5-codex"}`))

	firstKey := gjson.GetBytes(firstBody, "prompt_cache_key").String()
	secondKey := gjson.GetBytes(secondBody, "prompt_cache_key").String()
	if firstKey == "" {
		t.Fatalf("first prompt_cache_key is empty; body=%s", string(firstBody))
	}
	if secondKey != firstKey {
		t.Fatalf("same Claude Code session_id produced different websocket prompt_cache_key: first=%q second=%q", firstKey, secondKey)
	}
	if got := firstHeaders["session_id"]; len(got) != 1 || got[0] != firstKey {
		t.Fatalf("first session_id = %#v, want [%q]", got, firstKey)
	}
	if got := secondHeaders["session_id"]; len(got) != 1 || got[0] != firstKey {
		t.Fatalf("second session_id = %#v, want [%q]", got, firstKey)
	}
}

func TestApplyCodexPromptCacheHeadersClaudeRejectsBareUserID(t *testing.T) {
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex-claude-ws-cache-bare-user",
		Payload: []byte(`{"metadata":{"user_id":"same-user-across-chats"},"messages":[{"role":"user","content":[{"type":"text","text":"first"}]}]}`),
	}

	body, headers := applyCodexPromptCacheHeaders("claude", req, []byte(`{"model":"gpt-5-codex"}`))

	if got := gjson.GetBytes(body, "prompt_cache_key").String(); got != "" {
		t.Fatalf("bare metadata.user_id must not create websocket prompt_cache_key, got %q; body=%s", got, string(body))
	}
	if got := headers["session_id"]; len(got) != 0 {
		t.Fatalf("bare metadata.user_id must not create websocket session_id, got %#v", got)
	}
	if got := headers.Get("Session-Id"); got != "" {
		t.Fatalf("bare metadata.user_id must not create websocket Session-Id, got %q", got)
	}
	if got := headers.Get("Conversation_id"); got != "" {
		t.Fatalf("bare metadata.user_id must not create websocket Conversation_id, got %q", got)
	}
}

func TestApplyCodexWebsocketHeadersIdentityConfuseRemapsPromptCacheKey(t *testing.T) {
	cfg := &config.Config{
		Routing: config.RoutingConfig{SessionAffinity: true},
		Codex:   config.CodexConfig{IdentityConfuse: true},
	}
	auth := &cliproxyauth.Auth{ID: "auth-ws-1", Provider: "codex"}
	req := cliproxyexecutor.Request{
		Model:   "gpt-5-codex",
		Payload: []byte(`{"prompt_cache_key":"cache-ws-1","client_metadata":{"x-codex-installation-id":"install-ws-1"}}`),
	}

	body, headers := applyCodexPromptCacheHeaders("openai-response", req, []byte(`{"model":"gpt-5-codex"}`))
	body, identityState := applyCodexIdentityConfuseBody(cfg, auth, req.Payload, body)
	ctx := contextWithGinHeaders(map[string]string{
		"X-Codex-Turn-Metadata": `{"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1","window_id":"cache-ws-1:0"}`,
		"X-Client-Request-Id":   "client-request-1",
	})
	headers = applyCodexWebsocketHeaders(ctx, headers, auth, "oauth-token", cfg)
	applyCodexIdentityConfuseHeaders(headers, &identityState)

	expectedPromptCacheKey := codexIdentityConfuseUUID("auth-ws-1", "prompt-cache", "cache-ws-1")
	expectedTurnID := codexIdentityConfuseUUID("auth-ws-1", "turn", "turn-ws-1")
	if gotKey := gjson.GetBytes(body, "prompt_cache_key").String(); gotKey != expectedPromptCacheKey {
		t.Fatalf("prompt_cache_key = %q, want %q", gotKey, expectedPromptCacheKey)
	}
	if gotSession := headers["session_id"]; len(gotSession) != 1 || gotSession[0] != expectedPromptCacheKey {
		t.Fatalf("session_id = %#v, want [%q]", gotSession, expectedPromptCacheKey)
	}
	if gotCanonicalSession := headers.Get("Session-Id"); gotCanonicalSession != "" {
		t.Fatalf("Session-Id = %q, want empty", gotCanonicalSession)
	}
	if gotRequestID := headers.Get("X-Client-Request-Id"); gotRequestID != expectedPromptCacheKey {
		t.Fatalf("X-Client-Request-Id = %q, want %q", gotRequestID, expectedPromptCacheKey)
	}
	if gotThreadID := headers.Get("Thread-Id"); gotThreadID != expectedPromptCacheKey {
		t.Fatalf("Thread-Id = %q, want %q", gotThreadID, expectedPromptCacheKey)
	}
	if gotConversation := headers.Get("Conversation_id"); gotConversation != expectedPromptCacheKey {
		t.Fatalf("Conversation_id = %q, want %q", gotConversation, expectedPromptCacheKey)
	}
	if gotWindowID := headers.Get("X-Codex-Window-Id"); gotWindowID != expectedPromptCacheKey+":0" {
		t.Fatalf("X-Codex-Window-Id = %q, want %q", gotWindowID, expectedPromptCacheKey+":0")
	}
	gotMetadata := headers.Get("X-Codex-Turn-Metadata")
	if gotMetadataPromptCacheKey := gjson.Get(gotMetadata, "prompt_cache_key").String(); gotMetadataPromptCacheKey != expectedPromptCacheKey {
		t.Fatalf("X-Codex-Turn-Metadata.prompt_cache_key = %q, want %q", gotMetadataPromptCacheKey, expectedPromptCacheKey)
	}
	if gotMetadataTurnID := gjson.Get(gotMetadata, "turn_id").String(); gotMetadataTurnID != expectedTurnID {
		t.Fatalf("X-Codex-Turn-Metadata.turn_id = %q, want %q", gotMetadataTurnID, expectedTurnID)
	}
	if gotMetadataWindowID := gjson.Get(gotMetadata, "window_id").String(); gotMetadataWindowID != expectedPromptCacheKey+":0" {
		t.Fatalf("X-Codex-Turn-Metadata.window_id = %q, want %q", gotMetadataWindowID, expectedPromptCacheKey+":0")
	}
	expectedInstallationID := codexIdentityConfuseUUID("auth-ws-1", "installation", "install-ws-1")
	if gotInstallationID := gjson.GetBytes(body, "client_metadata.x-codex-installation-id").String(); gotInstallationID != expectedInstallationID {
		t.Fatalf("installation id = %q, want %q", gotInstallationID, expectedInstallationID)
	}
}

func TestCodexIdentityConfuseResponsePayloadHidesUpstreamAndRestoresClient(t *testing.T) {
	state := codexIdentityConfuseState{
		enabled:                true,
		authID:                 "auth-ws-1",
		originalPromptCacheKey: "cache-ws-1",
		promptCacheKey:         codexIdentityConfuseUUID("auth-ws-1", "prompt-cache", "cache-ws-1"),
	}
	expectedTurnID := state.confuseTurnID("turn-ws-1")
	rawPayload := []byte(`{"type":"response.completed","response":{"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1"},"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1"}`)

	upstreamPayload := applyCodexIdentityConfuseResponsePayload(rawPayload, state)
	if bytes.Contains(upstreamPayload, []byte(`cache-ws-1`)) {
		t.Fatalf("upstream payload still contains original prompt_cache_key: %s", string(upstreamPayload))
	}
	if bytes.Contains(upstreamPayload, []byte(`turn-ws-1`)) {
		t.Fatalf("upstream payload still contains original turn_id: %s", string(upstreamPayload))
	}
	if !bytes.Contains(upstreamPayload, []byte(state.promptCacheKey)) {
		t.Fatalf("upstream payload missing confused prompt_cache_key: %s", string(upstreamPayload))
	}
	if !bytes.Contains(upstreamPayload, []byte(expectedTurnID)) {
		t.Fatalf("upstream payload missing confused turn_id: %s", string(upstreamPayload))
	}

	clientPayload := applyCodexIdentityExposeResponsePayload(upstreamPayload, state)
	if bytes.Contains(clientPayload, []byte(state.promptCacheKey)) {
		t.Fatalf("client payload still contains confused prompt_cache_key: %s", string(clientPayload))
	}
	if bytes.Contains(clientPayload, []byte(expectedTurnID)) {
		t.Fatalf("client payload still contains confused turn_id: %s", string(clientPayload))
	}
	if !bytes.Contains(clientPayload, []byte(`cache-ws-1`)) {
		t.Fatalf("client payload missing original prompt_cache_key: %s", string(clientPayload))
	}
	if !bytes.Contains(clientPayload, []byte(`turn-ws-1`)) {
		t.Fatalf("client payload missing original turn_id: %s", string(clientPayload))
	}

	rawSSE := []byte(`data: {"type":"response.completed","response":{"prompt_cache_key":"cache-ws-1","turn_id":"turn-ws-1"}}`)
	upstreamSSE := applyCodexIdentityConfuseResponsePayload(rawSSE, state)
	if bytes.Contains(upstreamSSE, []byte(`cache-ws-1`)) {
		t.Fatalf("upstream SSE still contains original prompt_cache_key: %s", string(upstreamSSE))
	}
	if bytes.Contains(upstreamSSE, []byte(`turn-ws-1`)) {
		t.Fatalf("upstream SSE still contains original turn_id: %s", string(upstreamSSE))
	}
	clientSSE := applyCodexIdentityExposeResponsePayload(upstreamSSE, state)
	if !bytes.Contains(clientSSE, []byte(`cache-ws-1`)) || bytes.Contains(clientSSE, []byte(state.promptCacheKey)) {
		t.Fatalf("client SSE prompt_cache_key was not restored: %s", string(clientSSE))
	}
	if !bytes.Contains(clientSSE, []byte(`turn-ws-1`)) || bytes.Contains(clientSSE, []byte(expectedTurnID)) {
		t.Fatalf("client SSE turn_id was not restored: %s", string(clientSSE))
	}
}

func TestBuildCodexResponsesWebsocketURLRequiresHTTPURL(t *testing.T) {
	if got, err := buildCodexResponsesWebsocketURL("https://example.com/backend/responses"); err != nil || got != "wss://example.com/backend/responses" {
		t.Fatalf("https URL = %q, %v; want wss URL", got, err)
	}
	if _, err := buildCodexResponsesWebsocketURL("ftp://example.com/responses"); err == nil {
		t.Fatalf("expected unsupported scheme error")
	}
	if _, err := buildCodexResponsesWebsocketURL("https:///responses"); err == nil {
		t.Fatalf("expected empty host error")
	}
}

func TestParseCodexWebsocketErrorMarksConnectionLimitRetryable(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"error":{"code":"websocket_connection_limit_reached","message":"too many websockets"},"headers":{"retry-after":"1"}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok || status.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("status = %#v, want 429", err)
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected retryable websocket connection limit error")
	}
	if got := *retryable.RetryAfter(); got != 0 {
		t.Fatalf("retryAfter = %v, want connection-limit fallback 0", got)
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("retry-after") != "1" {
		t.Fatalf("headers = %#v, want retry-after", err)
	}
}

func TestParseCodexWebsocketErrorUsesUsageLimitRetryMetadata(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"body":{"error":{"type":"usage_limit_reached","message":"usage limit reached","resets_in_seconds":7}}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}

	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected retryable usage limit websocket error")
	}
	if got := *retryable.RetryAfter(); got != 7*time.Second {
		t.Fatalf("retryAfter = %v, want 7s", got)
	}
}

func TestParseCodexWebsocketErrorPreservesWrappedBodyAndHeaders(t *testing.T) {
	err, ok := parseCodexWebsocketError([]byte(`{"type":"error","status":429,"body":{"error":{"code":"websocket_connection_limit_reached","type":"server_error","message":"too many websocket connections"}},"headers":{"x-request-id":"req-1"}}`))
	if !ok {
		t.Fatalf("expected websocket error")
	}

	parsed := gjson.Parse(err.Error())
	if got := parsed.Get("status").Int(); got != http.StatusTooManyRequests {
		t.Fatalf("wrapped status = %d, want 429; payload=%s", got, err.Error())
	}
	if got := parsed.Get("body.error.code").String(); got != "websocket_connection_limit_reached" {
		t.Fatalf("wrapped body error code = %s, want websocket_connection_limit_reached; payload=%s", got, err.Error())
	}
	if got := parsed.Get("error.code").String(); got != "websocket_connection_limit_reached" {
		t.Fatalf("surface error code = %s, want websocket_connection_limit_reached; payload=%s", got, err.Error())
	}
	retryable, ok := err.(interface{ RetryAfter() *time.Duration })
	if !ok || retryable.RetryAfter() == nil {
		t.Fatalf("expected body.error.code websocket connection limit to be retryable")
	}
	withHeaders, ok := err.(interface{ Headers() http.Header })
	if !ok || withHeaders.Headers().Get("x-request-id") != "req-1" {
		t.Fatalf("headers = %#v, want x-request-id", err)
	}
}

func TestApplyCodexHeadersUsesConfigUserAgentForOAuth(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	cfg := &config.Config{
		CodexHeaderDefaults: config.CodexHeaderDefaults{
			UserAgent:    "config-ua",
			BetaFeatures: "config-beta",
		},
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"User-Agent": "client-ua",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, cfg)

	if got := req.Header.Get("User-Agent"); got != "config-ua" {
		t.Fatalf("User-Agent = %s, want %s", got, "config-ua")
	}
	if got := req.Header.Get("x-codex-beta-features"); got != "" {
		t.Fatalf("x-codex-beta-features = %q, want empty", got)
	}
}

func TestApplyCodexHeadersPassesThroughClientIdentityHeaders(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	auth := &cliproxyauth.Auth{
		Provider: "codex",
		Metadata: map[string]any{"email": "user@example.com"},
	}
	req = req.WithContext(contextWithGinHeaders(map[string]string{
		"Originator":            "Codex Desktop",
		"Version":               "0.115.0-alpha.27",
		"X-Codex-Turn-Metadata": `{"turn_id":"turn-1"}`,
		"X-Client-Request-Id":   "019d2233-e240-7162-992d-38df0a2a0e0d",
	}))

	applyCodexHeaders(req, auth, "oauth-token", true, nil)

	if got := req.Header.Get("Originator"); got != "Codex Desktop" {
		t.Fatalf("Originator = %s, want %s", got, "Codex Desktop")
	}
	if got := req.Header.Get("Version"); got != "0.115.0-alpha.27" {
		t.Fatalf("Version = %s, want %s", got, "0.115.0-alpha.27")
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != `{"turn_id":"turn-1"}` {
		t.Fatalf("X-Codex-Turn-Metadata = %s, want %s", got, `{"turn_id":"turn-1"}`)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "019d2233-e240-7162-992d-38df0a2a0e0d" {
		t.Fatalf("X-Client-Request-Id = %s, want %s", got, "019d2233-e240-7162-992d-38df0a2a0e0d")
	}
}

func TestApplyCodexHeadersDoesNotInjectClientOnlyHeadersByDefault(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.com/responses", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	applyCodexHeaders(req, nil, "oauth-token", true, nil)

	if got := req.Header.Get("Version"); got != "" {
		t.Fatalf("Version = %q, want empty", got)
	}
	if got := req.Header.Get("X-Codex-Turn-Metadata"); got != "" {
		t.Fatalf("X-Codex-Turn-Metadata = %q, want empty", got)
	}
	if got := req.Header.Get("X-Client-Request-Id"); got != "" {
		t.Fatalf("X-Client-Request-Id = %q, want empty", got)
	}
}

func contextWithGinHeaders(headers map[string]string) context.Context {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	ginCtx.Request.Header = make(http.Header, len(headers))
	for key, value := range headers {
		ginCtx.Request.Header.Set(key, value)
	}
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func TestNewProxyAwareWebsocketDialerDirectDisablesProxy(t *testing.T) {
	t.Parallel()

	dialer := newProxyAwareWebsocketDialer(
		&config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
		&cliproxyauth.Auth{ProxyURL: "direct"},
	)

	if dialer.Proxy != nil {
		t.Fatal("expected websocket proxy function to be nil for direct mode")
	}
}
