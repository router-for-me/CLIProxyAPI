package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

func TestCodexPayloadRulesFollowCredentialFallback(t *testing.T) {
	for _, selector := range []string{"auth-index", "prefix"} {
		for _, useWebsocket := range []bool{false, true} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/websocket=%t/stream=%t", selector, useWebsocket, stream), func(t *testing.T) {
					type attempt struct {
						key  string
						body []byte
					}
					attempts := make(chan attempt, 16)
					const completed = `{"type":"response.completed","response":{"id":"resp_payload","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
						if useWebsocket {
							upgrader := websocket.Upgrader{}
							conn, err := upgrader.Upgrade(w, r, nil)
							if err != nil {
								t.Errorf("upgrade: %v", err)
								return
							}
							defer func() { _ = conn.Close() }()
							_, body, err := conn.ReadMessage()
							if err != nil {
								t.Errorf("read request: %v", err)
								return
							}
							attempts <- attempt{key: key, body: body}
							response := completed
							if key == "key-a" {
								response = `{"type":"error","status":503,"error":{"message":"temporary upstream failure"}}`
							}
							if err := conn.WriteMessage(websocket.TextMessage, []byte(response)); err != nil {
								t.Errorf("write response: %v", err)
							}
							return
						}
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Errorf("read request: %v", err)
							return
						}
						attempts <- attempt{key: key, body: body}
						if key == "key-a" {
							w.WriteHeader(http.StatusServiceUnavailable)
							_, _ = io.WriteString(w, `{"error":{"message":"temporary upstream failure"}}`)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+completed+"\n\n")
					}))
					defer server.Close()

					model := fmt.Sprintf("payload-fallback-%s-%t-%t", selector, useWebsocket, stream)
					authA := &cliproxyauth.Auth{ID: model + "-a", Provider: "codex", Prefix: "gateway-a", Status: cliproxyauth.StatusActive, Attributes: map[string]string{"api_key": "key-a", "base_url": server.URL, "priority": "10"}}
					authB := &cliproxyauth.Auth{ID: model + "-b", Provider: "codex", Prefix: "gateway-b", Status: cliproxyauth.StatusActive, Attributes: map[string]string{"api_key": "key-b", "base_url": server.URL, "priority": "0"}}
					selected := authA.EnsureIndex()
					if selector == "prefix" {
						selected = authA.Prefix
					} else {
						authA.Prefix = ""
						authB.Prefix = ""
					}
					var cfg config.Config
					if err := yaml.Unmarshal([]byte(fmt.Sprintf(`
payload:
  override:
    - models: &selected_credential
        - name: %q
          protocol: codex
          %s: %q
      params:
        service_tier: priority
  filter:
    - models: *selected_credential
      params:
        - 'tools.#(type=="tool_search")'
`, model, selector, selected)), &cfg); err != nil {
						t.Fatal(err)
					}
					manager := cliproxyauth.NewManager(nil, nil, nil)
					manager.SetConfig(&cfg)
					if useWebsocket {
						manager.RegisterExecutor(NewCodexWebsocketsExecutor(&cfg))
					} else {
						manager.RegisterExecutor(NewCodexExecutor(&cfg))
					}
					defer manager.StopAutoRefresh()
					for _, auth := range []*cliproxyauth.Auth{authA, authB} {
						if _, err := manager.Register(context.Background(), auth); err != nil {
							t.Fatal(err)
						}
						registry.GetGlobalRegistry().RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}})
						defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)
					}
					payload := []byte(fmt.Sprintf(`{"model":%q,"input":[],"instructions":"fixture","tools":[{"type":"tool_search"},{"type":"function","name":"lookup","parameters":{"type":"object","properties":{}}}]}`, model))
					original := string(payload)
					req := cliproxyexecutor.Request{Model: model, Payload: payload}
					opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, ResponseFormat: sdktranslator.FormatOpenAIResponse, Stream: stream, OriginalRequest: payload}
					if stream {
						result, err := manager.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
						if err != nil {
							t.Fatal(err)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
						}
					} else if _, err := manager.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
						t.Fatal(err)
					}
					if string(payload) != original {
						t.Errorf("original request mutated: %s", payload)
					}
					if len(attempts) < 2 {
						t.Fatalf("got %d upstream attempts; expected credential fallback", len(attempts))
					}
					seenA, seenB := false, false
					for len(attempts) > 0 {
						got := <-attempts
						want := ""
						switch got.key {
						case "key-a":
							seenA = true
							want = "priority"
							if seenB {
								t.Error("returned to failed credential after successful fallback")
							}
						case "key-b":
							seenB = true
						default:
							t.Errorf("unexpected credential: %q", got.key)
						}
						if value := gjson.GetBytes(got.body, "service_tier").String(); value != want {
							t.Errorf("%s service_tier = %q, want %q; body=%s", got.key, value, want, got.body)
						}
						if search := gjson.GetBytes(got.body, `tools.#(type=="tool_search")`).Exists(); search != (got.key == "key-b") {
							t.Errorf("%s tool_search present = %t; body=%s", got.key, search, got.body)
						}
					}
					if !seenA || !seenB {
						t.Errorf("credential fallback missing: a=%t b=%t", seenA, seenB)
					}
				})
			}
		}
	}
}
