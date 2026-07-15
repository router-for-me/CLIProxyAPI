package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexAutoExecutorClaudeResponsesBridgeStreamsOverWebsocket(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedAuthorization := make(chan string, 1)
	capturedPayload := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("request path = %q, want /responses", r.URL.Path)
		}
		capturedAuthorization <- r.Header.Get("Authorization")
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()

		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read websocket request: %v", errRead)
			return
		}
		capturedPayload <- bytes.Clone(payload)
		for _, event := range [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_ws","model":"gpt-5.6-sol"}}`),
			[]byte(`{"type":"response.output_text.delta","delta":"websocket ok"}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ws","status":"completed","model":"gpt-5.6-sol","output":[],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`),
		} {
			if errWrite := conn.WriteMessage(websocket.TextMessage, event); errWrite != nil {
				t.Errorf("write websocket event: %v", errWrite)
				return
			}
		}
	}))
	defer server.Close()

	exec := NewCodexAutoExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{
		ID:         "bridge-ws-auth",
		Provider:   constant.Codex,
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}
	requestBody := []byte(`{"model":"gpt-5.6-sol","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`)
	stream, errExecute := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, cliproxyexecutor.Options{
		Alt:             constant.ClaudeResponsesBridgeAlt,
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: requestBody,
		Headers:         http.Header{"Authorization": []string{"Bearer local-proxy-token"}},
		Stream:          true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream error: %v", errExecute)
	}
	var output strings.Builder
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error: %v", chunk.Err)
		}
		output.Write(chunk.Payload)
	}

	select {
	case authorization := <-capturedAuthorization:
		if authorization != "Bearer oauth-token" {
			t.Fatalf("Authorization = %q, want OAuth token", authorization)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket authorization")
	}
	select {
	case payload := <-capturedPayload:
		if got := gjson.GetBytes(payload, "input.0.content.0.text").String(); got != "hello" {
			t.Fatalf("translated websocket input = %q, want hello; payload=%s", got, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket payload")
	}
	if !strings.Contains(output.String(), "event: message_start") || !strings.Contains(output.String(), "websocket ok") {
		t.Fatalf("unexpected Claude websocket stream: %s", output.String())
	}
}
