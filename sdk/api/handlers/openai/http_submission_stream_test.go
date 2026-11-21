package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestHTTPSubmissionEmptyAcceptedStreamDoesNotReplayPooledModel(t *testing.T) {
	var accepted atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		accepted.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	const provider = "openai-compatible-submission-empty-images"
	const compatName = "submission-empty-images"
	const model = "submission-pooled-image"
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name: compatName, BaseURL: upstream.URL,
		Models: []config.OpenAICompatibilityModel{
			{Name: "image-a", Alias: model, Image: true},
			{Name: "image-b", Alias: model, Image: true},
		},
	}}}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(provider, cfg))
	auth := &coreauth.Auth{ID: provider, Provider: provider, Status: coreauth.StatusActive, ProxyURL: "direct",
		Attributes: map[string]string{"base_url": upstream.URL, "api_key": "synthetic", "compat_name": compatName, "provider_key": provider}}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, provider, []*registry.ModelInfo{{ID: model, Type: registry.OpenAIImageModelType}})
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIAPIHandler(base)
	response := performImagesEndpointRequest(t, imagesGenerationsPath, "application/json",
		strings.NewReader(`{"model":"`+model+`","prompt":"synthetic","stream":true}`), handler.ImagesGenerations)
	t.Logf("accepted requests=%d, public status=%d", accepted.Load(), response.Code)
	if accepted.Load() != 1 || response.Code != http.StatusBadGateway {
		t.Fatalf("accepted=%d status=%d body=%s", accepted.Load(), response.Code, response.Body.String())
	}
	for _, value := range []string{`"code":"upstream_outcome_unknown"`, `"retryable":false`} {
		if !strings.Contains(response.Body.String(), value) {
			t.Fatalf("body=%s, missing %s", response.Body.String(), value)
		}
	}
	if response.Header().Get("x-should-retry") != "false" {
		t.Fatalf("x-should-retry=%q", response.Header().Get("x-should-retry"))
	}
}

func TestHTTPSubmissionMessagesLateTruncationHasUnknownOutcome(t *testing.T) {
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprint(complete), func(t *testing.T) {
			var accepted atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				accepted.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n")
				_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
				w.(http.Flusher).Flush()
				if complete {
					_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
				}
			}))
			defer upstream.Close()
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
			auth := &coreauth.Auth{ID: "submission-late-" + t.Name(), Provider: "claude", Status: coreauth.StatusActive, ProxyURL: "direct",
				Attributes: map[string]string{"base_url": upstream.URL, "api_key": "synthetic"}}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			const model = "claude-opus-5"
			registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			router := gin.New()
			router.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"`+model+`","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":true}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			body := response.Body.String()
			if accepted.Load() != 1 || response.Code != http.StatusOK || !strings.Contains(body, "hello") {
				t.Fatalf("accepted=%d status=%d body=%s", accepted.Load(), response.Code, body)
			}
			if complete {
				if !strings.Contains(body, "event: message_stop") || strings.Contains(body, "upstream_outcome_unknown") || strings.Contains(body, "event: error") {
					t.Fatalf("complete stream=%s", body)
				}
				return
			}
			for _, value := range []string{"event: error", `"code":"upstream_outcome_unknown"`, `"retryable":false`} {
				if !strings.Contains(body, value) {
					t.Fatalf("truncated stream=%s, missing %s", body, value)
				}
			}
			if strings.Contains(body, "event: message_stop") {
				t.Fatalf("truncated stream invented completion: %s", body)
			}
		})
	}
}

func TestHTTPSubmissionExplicitStreamErrorCanFailOver(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		if attempts.Add(1) == 1 {
			_, _ = io.WriteString(w, "event: error\ndata: {\"error\":{\"message\":\"not accepted\",\"code\":503}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "data: {\"id\":\"synthetic\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	response := submissionRequest(t, upstream.URL, true, nil)
	if attempts.Load() != 2 || response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "recovered") {
		t.Fatalf("attempts=%d status=%d body=%s", attempts.Load(), response.Code, response.Body.String())
	}
}

func TestHTTPSubmissionIncompleteFrameDoesNotReplay(t *testing.T) {
	for _, late := range []bool{false, true} {
		for _, ending := range []string{"eof", "done"} {
			t.Run(fmt.Sprintf("late=%t/%s", late, ending), func(t *testing.T) {
				var accepted atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					accepted.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					if late {
						_, _ = io.WriteString(w, `data: {"id":"cut","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"partial"},"finish_reason":null}]}`+"\n\n")
						w.(http.Flusher).Flush()
					}
					_, _ = io.WriteString(w, `data: {"id":"cut"`)
					if ending == "done" {
						_, _ = io.WriteString(w, "\ndata: [DONE]\n\n")
					}
				}))
				defer upstream.Close()
				response := submissionRequest(t, upstream.URL, true, nil)
				body := response.Body.String()
				t.Logf("accepted=%d status=%d body=%s", accepted.Load(), response.Code, body)
				if accepted.Load() != 1 {
					t.Fatalf("incomplete accepted frame replayed: accepted=%d", accepted.Load())
				}
				for _, value := range []string{`"code":"upstream_outcome_unknown"`, `"retryable":false`} {
					if !strings.Contains(body, value) {
						t.Fatalf("body=%s, missing %s", body, value)
					}
				}
				if late {
					if response.Code != http.StatusOK || !strings.Contains(body, "partial") || strings.Contains(body, "[DONE]") || strings.Contains(body, `"finish_reason":"stop"`) {
						t.Fatalf("late truncation status=%d body=%s", response.Code, body)
					}
				} else if response.Code != http.StatusBadGateway || response.Header().Get("x-should-retry") != "false" {
					t.Fatalf("preheader status=%d retry=%q body=%s", response.Code, response.Header().Get("x-should-retry"), body)
				}
			})
		}
	}
}
