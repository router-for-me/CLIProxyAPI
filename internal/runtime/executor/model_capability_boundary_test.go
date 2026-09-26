package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

// Exercise actual executor translation with synthetic OAuth and loopback upstreams.
// These tests do not establish live pool routing, host-specific headers, or entitlement.
func TestModelCapabilityBoundary(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		models := registry.GetCodexProModels()
		if provider == "claude" {
			models = registry.GetClaudeModels()
		}
		clientID := "capability-boundary-" + provider
		registry.GetGlobalRegistry().RegisterClient(clientID, provider, models)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(clientID) })
	}
	cases := []struct {
		provider, model string
		efforts         []string
		blockedLevel    string // Native discovery advertises this, but the release registry rejects it.
	}{
		{"codex", "gpt-5.6-luna", []string{"low", "medium", "high", "xhigh", "max"}, ""},
		{"codex", "gpt-5.6-sol", []string{"low", "medium", "high", "xhigh", "max", "ultra"}, "ultra"},
		{"codex", "gpt-5.6-terra", []string{"ultra"}, "ultra"},
		{"codex", "gpt-6-astra", []string{"ultra"}, "ultra"},
		{"codex", "codex-auto-review", []string{"max"}, "max"},
		{"claude", "claude-fable-5-1", []string{"low", "medium", "high", "xhigh", "max"}, ""},
	}
	for _, test := range cases {
		for _, effort := range test.efforts {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/%s/stream=%t", test.provider, test.model, effort, stream), func(t *testing.T) {
					bodies := make(chan []byte, 1)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, errRead := io.ReadAll(r.Body)
						if errRead != nil {
							t.Error(errRead)
							return
						}
						bodies <- body
						if test.provider == "codex" {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"synthetic\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
						} else {
							w.Header().Set("Content-Type", "text/event-stream")
							_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"synthetic\",\"model\":\"claude-fable-5-1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
						}
					}))
					defer server.Close()
					auth := &cliproxyauth.Auth{ID: "synthetic-" + test.provider, Provider: test.provider,
						Attributes: map[string]string{"base_url": server.URL},
						Metadata:   map[string]any{"access_token": "synthetic-access-token"}}
					if test.provider == "claude" {
						auth.Metadata = claudeOAuthTestMetadata()
						auth.Metadata["access_token"] = "sk-ant-oat-synthetic-access-token"
					}
					payload := []byte(fmt.Sprintf(`{"model":%q,"input":[{"role":"user","content":"synthetic probe"}],"reasoning":{"effort":%q},"tools":[{"type":"function","name":"synthetic_tool","parameters":{"type":"object","properties":{}}}]}`, test.model, effort))
					request := cliproxyexecutor.Request{Model: test.model, Payload: payload}
					options := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload, Stream: stream}
					var errExecute error
					if stream {
						var result *cliproxyexecutor.StreamResult
						if test.provider == "codex" {
							result, errExecute = NewCodexExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, request, options)
						} else {
							result, errExecute = NewClaudeExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, request, options)
						}
						if errExecute == nil {
							for chunk := range result.Chunks {
								if chunk.Err != nil {
									errExecute = chunk.Err
								}
							}
						}
					} else if test.provider == "codex" {
						_, errExecute = NewCodexExecutor(&config.Config{}).Execute(context.Background(), auth, request, options)
					} else {
						_, errExecute = NewClaudeExecutor(&config.Config{}).Execute(context.Background(), auth, request, options)
					}
					if effort == test.blockedLevel {
						if errExecute == nil || !strings.Contains(errExecute.Error(), "not supported") {
							t.Fatalf("expected release registry rejection, got %v", errExecute)
						}
						select {
						case <-bodies:
							t.Fatal("rejected effort reached upstream")
						default:
						}
						observation, _ := json.Marshal(map[string]string{"provider": test.provider, "model": test.model, "input": effort, "outcome": "rejected-before-upstream"})
						t.Logf("CAPABILITY_OBSERVATION %s", observation)
						return
					}
					if errExecute != nil {
						t.Fatalf("executor rejected effort %s: %v", effort, errExecute)
					}
					var body []byte
					select {
					case body = <-bodies:
					default:
						t.Fatal("upstream received no request")
					}
					path := "reasoning.effort"
					if test.provider == "claude" {
						path = "output_config.effort"
					}
					observation, _ := json.Marshal(map[string]string{"provider": test.provider, "model": test.model, "input": effort, "output": gjson.GetBytes(body, path).String(), "outputPath": path, "thinkingMode": gjson.GetBytes(body, "thinking.type").String(), "outcome": "forwarded"})
					t.Logf("CAPABILITY_OBSERVATION %s", observation)
					if got := gjson.GetBytes(body, path).String(); got != effort {
						t.Fatalf("%s = %q, want %q; thinking=%s", path, got, effort, gjson.GetBytes(body, "thinking").Raw)
					}
					if test.provider == "claude" && gjson.GetBytes(body, "thinking.type").String() != "adaptive" {
						t.Fatalf("thinking=%s, want adaptive", gjson.GetBytes(body, "thinking").Raw)
					}
					if len(gjson.GetBytes(body, "tools").Array()) == 0 {
						t.Fatal("tool declaration lost")
					}
				})
			}
		}
	}
}
