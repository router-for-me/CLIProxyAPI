package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorClaudeResponsesBridgeUsesOAuthToken(t *testing.T) {
	var gotPath string
	var gotAuthorization string
	var gotAccountID string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		gotAccountID = r.Header.Get("Chatgpt-Account-Id")
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": upstream.URL},
		Metadata:   map[string]any{"access_token": "oauth-token", "account_id": "oauth-account"},
	}
	requestBody := []byte(`{"model":"gpt-5.6-sol","max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	response, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, cliproxyexecutor.Options{
		Alt:             constant.ClaudeResponsesBridgeAlt,
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: requestBody,
		Headers:         http.Header{"Authorization": []string{"Bearer local-proxy-token"}, "X-Api-Key": []string{"local-proxy-key"}},
	})
	if errExecute != nil {
		t.Fatalf("Execute error: %v", errExecute)
	}

	if gotPath != "/responses" {
		t.Fatalf("path = %q, want /responses", gotPath)
	}
	if gotAuthorization != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth token", gotAuthorization)
	}
	if gotAccountID != "oauth-account" {
		t.Fatalf("Chatgpt-Account-Id = %q, want OAuth account", gotAccountID)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("upstream model = %q, want gpt-5.6-sol; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(gotBody, "input.0.content.0.text").String(); got != "hello" {
		t.Fatalf("upstream input text = %q, want hello; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(response.Payload, "content.0.text").String(); got != "hello" {
		t.Fatalf("translated response text = %q, want hello; response=%s", got, response.Payload)
	}
}

func TestCodexExecutorClaudeResponsesBridgeStreamUsesOAuthToken(t *testing.T) {
	var gotAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"model\":\"gpt-5.6-sol\"}}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"status\":\"completed\",\"model\":\"gpt-5.6-sol\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": upstream.URL},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}
	requestBody := []byte(`{"model":"gpt-5.6-sol","stream":true,"max_tokens":128,"messages":[{"role":"user","content":"hello"}]}`)
	stream, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, cliproxyexecutor.Options{
		Alt:             constant.ClaudeResponsesBridgeAlt,
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: requestBody,
		Headers:         http.Header{"X-Api-Key": []string{"local-proxy-key"}},
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
	if gotAuthorization != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth token", gotAuthorization)
	}
	if !strings.Contains(output.String(), "event: message_start") || !strings.Contains(output.String(), "hello") {
		t.Fatalf("unexpected Claude stream: %s", output.String())
	}
}

func TestCodexExecutorClaudeResponsesCompactBridgeUsesOAuthToken(t *testing.T) {
	var gotPath string
	var gotAuthorization string
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuthorization = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_compact","object":"response.compaction","output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]},{"type":"compaction","encrypted_content":"encrypted"}],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`))
	}))
	defer upstream.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": upstream.URL},
		Metadata:   map[string]any{"access_token": "oauth-token"},
	}
	requestBody := []byte(`{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"Your task is to create a detailed summary of the conversation so far."}]}`)
	response, errExecute := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.6-sol",
		Payload: requestBody,
	}, cliproxyexecutor.Options{
		Alt:             constant.ClaudeResponsesCompactBridgeAlt,
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatOpenAIResponse,
		OriginalRequest: requestBody,
		Headers:         http.Header{"Authorization": []string{"Bearer local-proxy-token"}},
	})
	if errExecute != nil {
		t.Fatalf("Execute compact error: %v", errExecute)
	}
	if gotPath != "/responses/compact" {
		t.Fatalf("path = %q, want /responses/compact", gotPath)
	}
	if gotAuthorization != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q, want OAuth token", gotAuthorization)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "gpt-5.6-sol" {
		t.Fatalf("compact upstream model = %q; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(gotBody, "input.0.content.0.text").String(); !strings.Contains(got, "detailed summary") {
		t.Fatalf("compact upstream input = %q; body=%s", got, gotBody)
	}
	if got := gjson.GetBytes(response.Payload, "object").String(); got != "response.compaction" {
		t.Fatalf("compact response object = %q; response=%s", got, response.Payload)
	}
}

func TestApplyClaudeResponsesCompactionReplayPrependsOpaqueItems(t *testing.T) {
	source := []byte(`{"cpa_responses_compaction":{"output":[{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]},{"type":"compaction","encrypted_content":"encrypted"}]}}`)
	translated := []byte(`{"model":"gpt-5.6-sol","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"new"}]}]}`)
	got := applyClaudeResponsesCompactionReplay(translated, source, cliproxyexecutor.Options{Alt: constant.ClaudeResponsesBridgeAlt})
	if itemType := gjson.GetBytes(got, "input.1.type").String(); itemType != "compaction" {
		t.Fatalf("input.1.type = %q, want compaction; body=%s", itemType, got)
	}
	if text := gjson.GetBytes(got, "input.2.content.0.text").String(); text != "new" {
		t.Fatalf("new input text = %q, want new; body=%s", text, got)
	}
	if gjson.GetBytes(got, constant.ClaudeResponsesCompactionField).Exists() {
		t.Fatalf("internal compaction field leaked upstream: %s", got)
	}
}
