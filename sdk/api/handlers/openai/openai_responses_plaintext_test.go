package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Reproduce the declaration boundary without an upstream account or a live child.
// The OAuth-only optimizer must remain deferred, but the independent opt-in must
// remove the encryption declaration before the parent's response is generated.
func TestResponsesCollaborationPlaintextWithOAuthScope(t *testing.T) {
	for _, layout := range []string{"flat", "namespace", "additional_tools"} {
		for _, enabled := range []bool{false, true} {
			for _, userAgent := range []string{"Codex Desktop/0.159.0", "curl/8.7.1"} {
				t.Run(fmt.Sprintf("%s/enabled=%t/client=%s", layout, enabled, userAgent), func(t *testing.T) {
					cfg, err := sdkconfig.ParseConfigBytes([]byte(fmt.Sprintf(`requests:
  codex-collaboration-plaintext: %t
oauth:
  providers:
    codex:
      optimize-multi-agent-v2: true
`, enabled)))
					if err != nil {
						t.Fatal(err)
					}
					handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, nil))
					request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
					request.Header.Set("User-Agent", userAgent)
					c, _ := gin.CreateTestContext(httptest.NewRecorder())
					c.Request = request
					tools := `[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"properties":{"message":{"type":"string","encrypted":true},"fork_turns":{"type":"string"}}}},
{"type":"function","name":"send_message","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}},
{"type":"function","name":"followup_task","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}},
{"type":"function","name":"unrelated","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}]`
					prefix := "tools"
					payload := []byte(`{"model":"parent-via-api-key","tools":` + tools + `,"input":[{"type":"compaction","encrypted_content":"opaque-checkpoint"}]}`)
					if layout == "namespace" {
						prefix = "tools.0.tools"
						payload = []byte(`{"tools":[{"type":"namespace","name":"agents","tools":` + tools + `}],"input":[{"type":"compaction","encrypted_content":"opaque-checkpoint"}]}`)
					} else if layout == "additional_tools" {
						prefix = "input.1.tools.0.tools"
						payload = []byte(`{"input":[{"type":"compaction","encrypted_content":"opaque-checkpoint"},{"type":"additional_tools","tools":[{"type":"namespace","name":"collaboration","tools":` + tools + `}]}]}`)
					}
					want := bytes.Clone(payload)
					if enabled && userAgent != "curl/8.7.1" {
						for i := 0; i < 3; i++ {
							want, err = sjson.DeleteBytes(want, fmt.Sprintf("%s.%d.parameters.properties.message.encrypted", prefix, i))
							if err != nil {
								t.Fatal(err)
							}
						}
					}
					got := handler.prepareCodexMultiAgentV2Tools(c, payload)
					if !bytes.Equal(got, want) {
						t.Fatalf("collaboration plaintext mismatch:\n got: %s\nwant: %s", got, want)
					}
					if _, exists := c.Get(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey); exists {
						t.Fatal("plaintext-only preparation must not mark the OAuth optimizer prepared")
					}
					if gotAgain := handler.prepareCodexMultiAgentV2Tools(c, got); !bytes.Equal(gotAgain, got) {
						t.Fatal("plaintext preparation is not idempotent")
					}
					if !gjson.GetBytes(got, prefix+".3.parameters.properties.message.encrypted").Bool() {
						t.Fatal("unrelated tool encryption changed")
					}
				})
			}
		}
	}
}

func TestResponsesCollaborationPlaintextTransports(t *testing.T) {
	for _, transport := range []string{"http", "sse", "websocket"} {
		t.Run(transport, func(t *testing.T) {
			cfg, err := sdkconfig.ParseConfigBytes([]byte(`requests: {codex-collaboration-plaintext: true}
oauth: {providers: {codex: {optimize-multi-agent-v2: true}}}
`))
			if err != nil {
				t.Fatal(err)
			}
			executor := &responsesMultiAgentCaptureExecutor{}
			executor.provider = "plaintext-test-provider"
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			auth := &coreauth.Auth{ID: "plaintext-" + transport, Provider: executor.provider, Status: coreauth.StatusActive,
				ProxyURL: "direct", Attributes: map[string]string{"api_key": "synthetic-test-key"}}
			if _, err := manager.Register(context.Background(), auth); err != nil {
				t.Fatal(err)
			}
			model := "plaintext-model-" + transport
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
			router := gin.New()
			router.POST("/v1/responses", handler.Responses)
			router.GET("/v1/responses", handler.ResponsesWebsocket)
			payload := fmt.Sprintf(`{"model":%q,"stream":%t,"input":[],"tools":[{"type":"namespace","name":"agents","tools":[{"type":"function","name":"spawn_agent","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}]}]}`, model, transport != "http")
			if transport == "websocket" {
				server := httptest.NewServer(router)
				defer server.Close()
				conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses",
					http.Header{"User-Agent": {"Codex Desktop/0.159.0"}})
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
					t.Fatal(err)
				}
				payload, err = sjson.Set(payload, "type", "response.create")
				if err != nil {
					t.Fatal(err)
				}
				if err := conn.WriteMessage(websocket.TextMessage, []byte(payload)); err != nil {
					t.Fatal(err)
				}
				if _, _, err := conn.ReadMessage(); err != nil {
					t.Fatal(err)
				}
			} else {
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
				request.Header.Set("User-Agent", "Codex Desktop/0.159.0")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
			}
			payloads := executor.Payloads()
			if len(payloads) != 1 {
				t.Fatalf("captured %d requests", len(payloads))
			}
			if gjson.GetBytes(payloads[0], "tools.0.tools.0.parameters.properties.message.encrypted").Exists() {
				t.Fatalf("API-key request still contains encrypted declaration: %s", payloads[0])
			}
			if gjson.GetBytes(payloads[0], "tools.0.name").String() != "agents" {
				t.Fatal("OAuth-only namespace rewrite leaked into API-key request")
			}
		})
	}
}

func TestResponsesCollaborationPlaintextWithoutOptimizer(t *testing.T) {
	handler := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(
		&sdkconfig.SDKConfig{CodexCollaborationPlaintext: true}, nil))
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = request
	payload := []byte(`{"tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"type":"string","encrypted":true}}}}]}`)
	got := handler.prepareCodexMultiAgentV2Tools(c, payload)
	if gjson.GetBytes(got, "tools.0.parameters.properties.message.encrypted").Exists() {
		t.Fatal("independent setting requires optimizer")
	}
	if _, exists := c.Get(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey); exists {
		t.Fatal("plaintext-only preparation marked optimizer prepared")
	}
}
