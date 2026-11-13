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

func TestHTTPSubmissionRedirectedResultDoesNotReplayPOST(t *testing.T) {
	for _, mode := range []string{"disconnect", "truncated", "refusal", "success"} {
		t.Run(mode, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Method == http.MethodPost {
					posts.Add(1)
					w.Header().Set("Location", "/result")
					w.WriteHeader(http.StatusSeeOther)
					return
				}
				if mode == "success" {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"ok","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
					return
				}
				if mode == "refusal" {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = io.WriteString(w, `{"error":{"message":"result unavailable"}}`)
					return
				}
				if mode == "truncated" {
					w.Header().Set("Content-Length", "1000")
					_, _ = io.WriteString(w, `{"id":"partial"`)
					return
				}
				conn, _, errHijack := w.(http.Hijacker).Hijack()
				if errHijack != nil {
					t.Error(errHijack)
					return
				}
				_ = conn.Close()
			}))
			defer server.Close()
			response := submissionRequest(t, server.URL, false, nil)
			t.Logf("posts=%d status=%d body=%s", posts.Load(), response.Code, response.Body.String())
			if mode == "success" {
				if posts.Load() != 1 || response.Code != http.StatusOK {
					t.Fatal("successful redirect changed")
				}
				return
			}
			if posts.Load() != 1 || response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"code":"upstream_outcome_unknown"`) {
				t.Fatal("redirected result caused replay or lost unknown outcome")
			}
		})
	}
}

func TestHTTPSubmissionCodexMalformedAcceptedResponseDoesNotReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				posts.Add(1)
				w.Header().Set("Content-Encoding", "synthetic-unsupported")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(&config.Config{}))
			const model = "gpt-5.2-codex"
			for _, suffix := range []string{"a", "b"} {
				auth := &coreauth.Auth{ID: "malformed-codex-" + suffix, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic"}}
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			router := gin.New()
			router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(`{"model":"gpt-5.2-codex","input":"hello","stream":%t}`, stream)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			t.Logf("posts=%d status=%d body=%s", posts.Load(), response.Code, response.Body.String())
			if posts.Load() != 1 || response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"code":"upstream_outcome_unknown"`) || response.Header().Get("x-should-retry") != "false" {
				t.Fatal("accepted malformed response replayed or missing unknown contract")
			}
		})
	}
}

func TestHTTPSubmissionClaudeMalformedAcceptedResponseDoesNotReplay(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				posts.Add(1)
				w.Header().Set("Content-Encoding", "synthetic-unsupported")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
			manager.RegisterExecutor(runtimeexecutor.NewClaudeExecutor(&config.Config{}))
			const model = "claude-opus-5"
			for _, suffix := range []string{"a", "b"} {
				auth := &coreauth.Auth{ID: "malformed-claude-" + suffix, Provider: "claude", Status: coreauth.StatusActive, ProxyURL: "direct", Attributes: map[string]string{"base_url": server.URL, "api_key": "synthetic"}}
				if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
					t.Fatal(errRegister)
				}
				registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			}
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			router := gin.New()
			router.POST("/v1/messages", claude.NewClaudeCodeAPIHandler(base).ClaudeMessages)
			request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":"claude-opus-5","max_tokens":64,"messages":[{"role":"user","content":"hello"}],"stream":%t}`, stream)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			t.Logf("posts=%d status=%d body=%s", posts.Load(), response.Code, response.Body.String())
			if posts.Load() != 1 || response.Code != http.StatusBadGateway || !strings.Contains(response.Body.String(), `"code":"upstream_outcome_unknown"`) || response.Header().Get("x-should-retry") != "false" {
				t.Fatal("accepted malformed response replayed or missing unknown contract")
			}
		})
	}
}
