package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	traeauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/trae"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestTraeExecutorExecute(t *testing.T) {
	var receivedBody []byte
	var receivedTraceparent string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != traeauth.RawChatPath {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Cloud-CLI-JWT live-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		receivedTraceparent = request.Header.Get("X-Flow-Traceparent")
		receivedBody = readTestBody(t, request)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "event: metadata\ndata: {\"model\":\"gpt-5.6-sol\",\"session_id\":\"session-1\"}\n\nevent: output\ndata: {\"response\":\"Hello\",\"reasoning_content\":\"brief thought\",\"tool_calls\":null}\n\nevent: token_usage\ndata: {\"prompt_tokens\":10,\"completion_tokens\":3,\"total_tokens\":13,\"cache_creation_input_tokens_total\":2,\"cache_read_input_tokens_total\":4,\"reasoning_tokens_total\":1}\n\nevent: done\ndata: {\"finish_reason\":\"stop\"}\n\n")
	}))
	defer server.Close()

	auth := testTraeAuth(t, server.URL)
	executor := NewTraeExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "GPT-5.6-Sol",
		Payload: []byte(`{"model":"GPT-5.6-Sol","messages":[{"role":"user","content":"hello"}]}`),
		Format:  sdktranslator.FormatOpenAI,
	}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI}
	response, errExecute := executor.Execute(context.Background(), auth, req, opts)
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "Hello" {
		t.Fatalf("content = %q, payload = %s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.reasoning_content").String(); got != "brief thought" {
		t.Fatalf("reasoning = %q, payload = %s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "usage.prompt_tokens").Int(); got != 10 {
		t.Fatalf("prompt_tokens = %d, payload = %s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "usage.prompt_tokens_details.cached_tokens").Int(); got != 4 {
		t.Fatalf("cached_tokens = %d, payload = %s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "usage.completion_tokens_details.reasoning_tokens").Int(); got != 1 {
		t.Fatalf("reasoning_tokens = %d, payload = %s", got, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "usage.cache_creation_input_tokens").Int(); got != 2 {
		t.Fatalf("cache_creation_input_tokens = %d, payload = %s", got, response.Payload)
	}
	if got := gjson.GetBytes(receivedBody, "config_name").String(); got != "gpt-5.6-sol" {
		t.Fatalf("config_name = %q, body = %s", got, receivedBody)
	}
	if got := gjson.GetBytes(receivedBody, "model_name").String(); got != "gpt-5.6-sol__dev" {
		t.Fatalf("model_name = %q, body = %s", got, receivedBody)
	}
	if got := gjson.GetBytes(receivedBody, "messages.0.content.0.text").String(); got != "hello" {
		t.Fatalf("normalized message = %q, body = %s", got, receivedBody)
	}
	if !gjson.GetBytes(receivedBody, "is_preset").Bool() || gjson.GetBytes(receivedBody, "reasoning_effort").String() != "xhigh" {
		t.Fatalf("Trae request defaults missing, body = %s", receivedBody)
	}
	sessionID := strings.ReplaceAll(gjson.GetBytes(receivedBody, "session_id").String(), "-", "")
	if len(sessionID) != 32 {
		t.Fatalf("session_id = %q, body = %s", sessionID, receivedBody)
	}
	if receivedTraceparent != "00-"+sessionID+"-"+sessionID[:16]+"-01" {
		t.Fatalf("traceparent %q does not match session_id %q", receivedTraceparent, sessionID)
	}
}

func TestTraeExecutorExecuteStreamConvertsToolCalls(t *testing.T) {
	var receivedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		receivedBody = readTestBody(t, request)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, `event: metadata
data: {"model":"gpt-5.6-sol","session_id":"session-2"}

event: output
data: {"response":"","reasoning_content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function_call":{"name":"lookup","arguments":"","partial_arguments":"{"}}]}

event: output
data: {"response":"","reasoning_content":null,"tool_calls":[{"index":0,"id":"","type":"","function_call":{"name":"","arguments":"","partial_arguments":"\"q\":\"x\"}"}}]}

event: output
data: {"response":"","reasoning_content":null,"tool_calls":[{"index":0,"id":"call_1","type":"function","function_call":{"name":"lookup","arguments":""}}]}

event: token_usage
data: {"prompt_tokens":8,"completion_tokens":4,"total_tokens":12}

event: done
data: {"finish_reason":"tool_calls"}

`)
	}))
	defer server.Close()

	auth := testTraeAuth(t, server.URL)
	executor := NewTraeExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "GPT-5.6-Sol",
		Payload: []byte(`{"model":"GPT-5.6-Sol","messages":[{"role":"user","content":"look up x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}],"tool_choice":"required"}`),
		Format:  sdktranslator.FormatOpenAI,
	}
	result, errExecute := executor.ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{Stream: true, SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	var output strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream error = %v", chunk.Err)
		}
		output.Write(chunk.Payload)
		output.WriteByte('\n')
	}
	stream := output.String()
	if strings.Count(stream, `"name":"lookup"`) != 1 {
		t.Fatalf("tool name was not emitted exactly once: %s", stream)
	}
	var arguments strings.Builder
	for _, line := range strings.Split(stream, "\n") {
		data := strings.TrimPrefix(line, "data: ")
		if data == line {
			continue
		}
		if fragment := gjson.Get(data, "choices.0.delta.tool_calls.0.function.arguments"); fragment.Exists() {
			arguments.WriteString(fragment.String())
		}
	}
	if got := arguments.String(); got != `{"q":"x"}` {
		t.Fatalf("tool arguments = %q, stream = %s", got, stream)
	}
	if got := gjson.GetBytes(receivedBody, "tool_choice").String(); got != "required" {
		t.Fatalf("tool_choice = %q, body = %s", got, receivedBody)
	}
	if !strings.Contains(stream, `"finish_reason":"tool_calls"`) || !strings.Contains(stream, "[DONE]") {
		t.Fatalf("stream termination missing: %s", stream)
	}
}

func TestTraeExecutorExecuteAccumulatesPartialToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, `event: metadata
data: {"model":"gpt-5.6-sol","session_id":"session-3"}

event: output
data: {"tool_calls":[{"index":0,"id":"call_2","type":"function","function_call":{"name":"lookup","partial_arguments":"{"}}]}

event: output
data: {"tool_calls":[{"index":0,"function_call":{"partial_arguments":"\"q\":\"x\"}"}}]}

event: done
data: {"finish_reason":"tool_calls"}

`)
	}))
	defer server.Close()

	auth := testTraeAuth(t, server.URL)
	executor := NewTraeExecutor(&config.Config{})
	req := cliproxyexecutor.Request{
		Model:   "GPT-5.6-Sol",
		Payload: []byte(`{"model":"GPT-5.6-Sol","messages":[{"role":"user","content":"look up x"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`),
		Format:  sdktranslator.FormatOpenAI,
	}
	response, errExecute := executor.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v", errExecute)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.tool_calls.0.function.arguments").String(); got != `{"q":"x"}` {
		t.Fatalf("tool arguments = %q, payload = %s", got, response.Payload)
	}
}

func TestTraeExecutorRefreshCatalogFailureUsesUnexpiredReferencedCredential(t *testing.T) {
	tests := []struct {
		name      string
		expiresAt time.Time
		wantErr   bool
	}{
		{name: "unexpired", expiresAt: time.Now().Add(time.Hour)},
		{name: "expired", expiresAt: time.Now().Add(-time.Hour), wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authPath := filepath.Join(t.TempDir(), "auth.json")
			expiresAt := test.expiresAt.UTC().Format(time.RFC3339)
			authJSON := fmt.Sprintf(`{"auth_mode":"trae","last_refresh":"2026-09-12T08:00:00Z","trae":{"access_token":"live-token","user_id":"test-user","expires_at":%q,"region":"CN"}}`, expiresAt)
			if errWrite := os.WriteFile(authPath, []byte(authJSON), 0o600); errWrite != nil {
				t.Fatal(errWrite)
			}
			models := []traeauth.Model{{
				Name:          "GPT-5.6-Sol",
				ConfigName:    "gpt-5.6-sol",
				BackendModel:  "gpt-5.6-sol__dev",
				Provider:      traeauth.Provider,
				ContextWindow: 272000,
			}}
			auth := &cliproxyauth.Auth{
				ID:       "trae-refresh-test.json",
				Provider: traeauth.Provider,
				Metadata: map[string]any{
					"trae_cli_path":  filepath.Join(t.TempDir(), "missing-traecli"),
					"trae_auth_path": authPath,
					"models":         models,
				},
			}
			refreshed, errRefresh := NewTraeExecutor(&config.Config{}).Refresh(context.Background(), auth)
			if (errRefresh != nil) != test.wantErr {
				t.Fatalf("Refresh() error = %v, wantErr %t", errRefresh, test.wantErr)
			}
			if test.wantErr {
				return
			}
			if _, ok := traeauth.ModelFor(refreshed.Metadata, "GPT-5.6-Sol"); !ok {
				t.Fatalf("Refresh() discarded cached model metadata: %+v", refreshed.Metadata)
			}
			if got := refreshed.Metadata["expires_at"]; got != expiresAt {
				t.Fatalf("expires_at = %v, want %q", got, expiresAt)
			}
		})
	}
}

func testTraeAuth(t *testing.T, baseURL string) *cliproxyauth.Auth {
	t.Helper()
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if errWrite := os.WriteFile(authPath, []byte(`{"auth_mode":"trae","trae":{"access_token":"live-token","user_id":"test-user","region":"CN"}}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	models := []traeauth.Model{{
		Name:          "GPT-5.6-Sol",
		ConfigName:    "gpt-5.6-sol",
		BackendModel:  "gpt-5.6-sol__dev",
		Provider:      traeauth.Provider,
		ContextWindow: 272000,
	}}
	return &cliproxyauth.Auth{
		ID:       "trae-test.json",
		Provider: traeauth.Provider,
		Metadata: map[string]any{
			"trae_auth_path": authPath,
			"base_url":       baseURL,
			"client_version": "0.204.1",
			"models":         models,
		},
	}
}

func readTestBody(t *testing.T, request *http.Request) []byte {
	t.Helper()
	raw, errRead := io.ReadAll(request.Body)
	if errRead != nil {
		t.Fatal(errRead)
	}
	return raw
}
