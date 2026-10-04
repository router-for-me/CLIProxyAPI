package cache_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestIssue6332ReplayCommitPrecedesCompletion(t *testing.T) {
	for _, mode := range []string{"held_open", "clean_eof", "internal_eof"} {
		t.Run(mode, func(t *testing.T) {
			const model = "gemini-3.7-flash"
			signature := strings.Repeat("s", 128)
			session := t.Name()
			cache.ClearAntigravityReasoningReplayCache()
			t.Cleanup(cache.ClearAntigravityReasoningReplayCache)
			entered, release, calls := cache.Issue6332BlockCommit(t)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			requests := make(chan []byte, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Error(errRead)
					return
				}
				requests <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"native-call","name":"lookup","args":{"key":"value"}},"thoughtSignature":"`+signature+`"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":22,"totalTokenCount":33},"responseId":"resp-replay"}}`+"\n\n")
				w.(http.Flusher).Flush()
				if mode == "held_open" {
					<-r.Context().Done()
				}
			}))
			defer server.Close()
			manager := cliproxyauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(runtimeexecutor.NewAntigravityExecutor(&config.Config{}))
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "antigravity", Status: cliproxyauth.StatusActive, ProxyURL: "direct", Metadata: map[string]any{"access_token": "token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339), "project_id": "project"}, Attributes: map[string]string{"base_url": server.URL}}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := openai.NewOpenAIResponsesAPIHandler(base)
			router := gin.New()
			router.Use(logging.CPATraceIDMiddleware(), middleware.RequestLoggingMiddleware(logging.NewFileRequestLogger(true, t.TempDir(), "", 0)))
			router.POST("/v1/responses", h.Responses)
			call := func(payload string) string {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "internal_eof" {
					stream, errExecute := base.ExecuteModelStream(ctx, handlers.ModelExecutionRequest{EntryProtocol: "openai-response", ExitProtocol: "openai-response", Model: model, Stream: true, Body: []byte(payload)})
					if errExecute != nil {
						t.Error(errExecute)
						return ""
					}
					var output strings.Builder
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							t.Error(chunk.Err)
						}
						output.Write(chunk.Payload)
					}
					return output.String()
				}
				writer := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload)).WithContext(ctx)
				request.Header.Set("Session-Id", session)
				router.ServeHTTP(writer, request)
				return writer.Body.String()
			}
			first := `{"model":"gemini-3.7-flash","input":[{"role":"user","content":"hello"}],"stream":true,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"key":{"type":"string"}}}}]}`
			first, _ = sjson.Set(first, "metadata.session_id", session)
			firstDone := make(chan string, 1)
			go func() { firstDone <- call(first) }()
			<-requests
			<-entered
			select {
			case <-firstDone:
				t.Fatal("completion escaped before replay commit")
			default:
			}
			unblock()
			downstream := <-firstDone
			var functionCall gjson.Result
			for _, line := range strings.Split(downstream, "\n") {
				if !strings.HasPrefix(line, "data:") {
					continue
				}
				event := gjson.Parse(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				if event.Get("type").String() == "response.completed" {
					for _, item := range event.Get("response.output").Array() {
						if item.Get("type").String() == "function_call" {
							functionCall = item
						}
					}
				}
			}
			if !functionCall.Exists() {
				t.Fatalf("no completed tool call: %s", downstream)
			}
			// Deliberately omit encrypted reasoning: restoration must come from the session ledger.
			second, _ := sjson.Set(first, "input.-1", map[string]any{"type": "function_call", "call_id": functionCall.Get("call_id").String(), "name": "lookup", "arguments": `{"key":"value"}`})
			second, _ = sjson.Set(second, "input.-1", map[string]any{"type": "function_call_output", "call_id": functionCall.Get("call_id").String(), "output": "found"})
			call(second)
			upstream := <-requests
			restored := false
			for _, content := range gjson.GetBytes(upstream, "request.contents").Array() {
				for _, part := range content.Get("parts").Array() {
					if part.Get("functionCall.name").String() == "lookup" && part.Get("thoughtSignature").String() == signature {
						restored = true
					}
				}
			}

			if got := calls(); got != 4 {
				t.Errorf("replay storage calls = %d, want one read and one commit per turn", got)
			}
			if !restored {
				t.Errorf("second turn lost tool-call signature: %s", upstream)
			}
		})
	}
}
