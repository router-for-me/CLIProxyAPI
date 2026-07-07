package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityExecutorExecuteStreamTranslatesInteractionsRequest(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1internal:streamGenerateContent" {
			t.Fatalf("path = %q, want /v1internal:streamGenerateContent", r.URL.Path)
		}
		if gotAlt := r.URL.Query().Get("alt"); gotAlt != "sse" {
			t.Fatalf("alt = %q, want sse", gotAlt)
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read upstream body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}}\n\n"))
	}))
	defer server.Close()

	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := &cliproxyauth.Auth{
		ID:       "interactions-antigravity-stream-auth",
		Provider: "antigravity",
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
			"project_id":   "project-1",
			"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	payload := []byte(`{"model":"gemini-3.5-flash-low","input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]}],"tools":[{"name":"get_weather","description":"weather","type":"function","parameters":{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}}],"generation_config":{"tool_choice":"auto","thinking_level":"high","thinking_summaries":"auto"},"stream":true,"store":false}`)
	result, errExecute := exec.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.5-flash-low",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatInteractions,
		ResponseFormat:  sdktranslator.FormatInteractions,
		Stream:          true,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	if len(upstreamBody) == 0 {
		t.Fatal("upstream body was not captured")
	}

	for _, path := range []string{
		"request.stream",
		"request.generationConfig.toolChoice",
		"request.generationConfig.thinkingLevel",
		"request.generationConfig.thinkingSummaries",
	} {
		if gjson.GetBytes(upstreamBody, path).Exists() {
			t.Fatalf("%s should not be sent upstream: %s", path, string(upstreamBody))
		}
	}
	if gjson.GetBytes(upstreamBody, "input").Exists() {
		t.Fatalf("raw interactions input should not be sent upstream: %s", string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.0.parts.0.text").String(); got != "hi" {
		t.Fatalf("request.contents.0.parts.0.text = %q, want hi. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
		t.Fatalf("request.toolConfig.functionCallingConfig.mode = %q, want AUTO. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.generationConfig.thinkingConfig.thinkingLevel").String(); got != "high" {
		t.Fatalf("request.generationConfig.thinkingConfig.thinkingLevel = %q, want high. Body: %s", got, string(upstreamBody))
	}
	if got := gjson.GetBytes(upstreamBody, "request.generationConfig.thinkingConfig.includeThoughts").Bool(); !got {
		t.Fatalf("request.generationConfig.thinkingConfig.includeThoughts = false, want true. Body: %s", string(upstreamBody))
	}
}
