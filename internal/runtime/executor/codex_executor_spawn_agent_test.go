package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorOptimizeMultiAgentV2(t *testing.T) {
	modelID := "codex-executor-spawn-agent-test-model"
	clientID := "codex-executor-spawn-agent-test-client"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:          modelID,
		Description: "Executor test model.",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}})
	defer modelRegistry.UnregisterClient(clientID)

	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		upstreamBody, _ = io.ReadAll(request.Body)
		if request.URL.Path == "/responses/compact" {
			w.Header().Set("Content-Type", "application/json")
			namespace := gjson.GetBytes(upstreamBody, "input.0.tools.0.name").String()
			compact := fmt.Sprintf(`{"id":"resp_1","object":"response.compaction","output":[{"type":"function_call","name":"spawn_agent","namespace":%q,"arguments":"{}","call_id":"call_1"}]}`, namespace)
			_, _ = w.Write([]byte(compact))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		namespace := gjson.GetBytes(upstreamBody, "input.0.tools.0.name").String()
		completed := fmt.Sprintf(`data: {"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[{"type":"function_call","name":"spawn_agent","namespace":%q,"arguments":"{}","call_id":"call_1"}]}}`+"\n\n", namespace)
		_, _ = w.Write([]byte(completed))
	}))
	defer server.Close()

	payload := codexSpawnAgentTestPayload()
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	tests := []struct {
		name    string
		enabled bool
		mode    string
	}{
		{name: "execute enabled", enabled: true, mode: "execute"},
		{name: "execute disabled", enabled: false, mode: "execute"},
		{name: "stream enabled", enabled: true, mode: "stream"},
		{name: "stream disabled", enabled: false, mode: "stream"},
		{name: "compact enabled", enabled: true, mode: "compact"},
		{name: "compact disabled", enabled: false, mode: "compact"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstreamBody = nil
			executor := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: tt.enabled}})
			ctx := codexSpawnAgentTestContext()
			headers := http.Header{"User-Agent": []string{"overridden-client/1.0"}}
			req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai-response"), Headers: headers}

			var clientPayload []byte
			switch tt.mode {
			case "stream":
				result, errExecute := executor.ExecuteStream(ctx, auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				for chunk := range result.Chunks {
					clientPayload = append(clientPayload, chunk.Payload...)
				}
			case "compact":
				opts.Alt = "responses/compact"
				response, errExecute := executor.Execute(ctx, auth, req, opts)
				if errExecute != nil {
					t.Fatalf("compact Execute() error = %v", errExecute)
				}
				clientPayload = response.Payload
			default:
				response, errExecute := executor.Execute(ctx, auth, req, opts)
				if errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				clientPayload = response.Payload
			}

			assertCodexSpawnAgentOptimization(t, upstreamBody, modelID, tt.enabled)
			assertCodexSpawnAgentRequestMessage(t, upstreamBody, tt.enabled)
			assertCodexSpawnAgentClientNamespace(t, clientPayload)
		})
	}
}

func codexSpawnAgentTestPayload() []byte {
	return []byte(`{
		"model":"gpt-5.4",
		"input":[{
			"type":"additional_tools",
			"role":"developer",
			"tools":[{
				"type":"namespace",
				"name":"collaboration",
				"tools":[{
					"type":"function",
					"name":"spawn_agent",
					"description":"Available model overrides (optional; inherited parent model is preferred):\n- old-model\nSpawns an agent.",
					"parameters":{"type":"object","properties":{"message":{"type":"string","encrypted":true}}}
				}]
			}]
		},{
			"type":"agent_message",
			"id":"amsg_1",
			"author":"/root",
			"recipient":"/root/worker",
			"content":[
				{"type":"input_text","text":"Payload:\n"},
				{"type":"encrypted_content","encrypted_content":"delegated task"}
			],
			"internal_chat_message_metadata_passthrough":{"turn_id":"turn_1"}
		}]
	}`)
}

func codexSpawnAgentTestContext() context.Context {
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "codex-tui/0.145.0")
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = request
	return context.WithValue(context.Background(), "gin", ginCtx)
}

func assertCodexSpawnAgentClientNamespace(t *testing.T, payload []byte) {
	t.Helper()
	if strings.Contains(string(payload), "collaboration-optimize") {
		t.Fatalf("optimized namespace leaked to client: %s", payload)
	}
	if !strings.Contains(string(payload), `"namespace":"collaboration"`) {
		t.Fatalf("restored collaboration namespace missing from client payload: %s", payload)
	}
}

func assertCodexSpawnAgentRequestMessage(t *testing.T, payload []byte, enabled bool) {
	t.Helper()
	message := gjson.GetBytes(payload, "input.1")
	if message.Get("type").String() != "agent_message" || message.Get("role").Exists() {
		t.Fatalf("Codex executor changed outer agent message: %s", payload)
	}
	if message.Get("author").String() != "/root" || message.Get("recipient").String() != "/root/worker" || message.Get("internal_chat_message_metadata_passthrough.turn_id").String() != "turn_1" {
		t.Fatalf("Codex executor changed agent message metadata: %s", payload)
	}
	if enabled {
		if message.Get("content.1.type").String() != "input_text" || message.Get("content.1.text").String() != "delegated task" {
			t.Fatalf("Codex executor did not normalize agent message content: %s", payload)
		}
		if message.Get("content.1.encrypted_content").Exists() {
			t.Fatalf("Codex executor preserved encrypted_content: %s", payload)
		}
		return
	}
	if message.Get("content.1.type").String() != "encrypted_content" || message.Get("content.1.encrypted_content").String() != "delegated task" {
		t.Fatalf("disabled optimization changed agent message content: %s", payload)
	}
}

func assertCodexSpawnAgentOptimization(t *testing.T, payload []byte, modelID string, enabled bool) {
	t.Helper()
	namespace := gjson.GetBytes(payload, "input.0.tools.0.name").String()
	description := gjson.GetBytes(payload, "input.0.tools.0.tools.0.description").String()
	encrypted := gjson.GetBytes(payload, "input.0.tools.0.tools.0.parameters.properties.message.encrypted")
	if enabled {
		if namespace != "collaboration-optimize" {
			t.Fatalf("optimized namespace = %q, want collaboration-optimize", namespace)
		}
		wantModel := "- `" + modelID + "`: Executor test model. Reasoning efforts: low, medium (default), high."
		if !strings.Contains(description, wantModel) {
			t.Fatalf("description does not contain model metadata: %q", description)
		}
		if encrypted.Exists() {
			t.Fatalf("message encrypted was not removed: %s", encrypted.Raw)
		}
		return
	}
	if namespace != "collaboration" {
		t.Fatalf("disabled namespace = %q, want collaboration", namespace)
	}
	if !strings.Contains(description, "- old-model") {
		t.Fatalf("disabled optimization changed description: %q", description)
	}
	if !encrypted.Bool() {
		t.Fatalf("disabled optimization removed message encrypted: %s", encrypted.Raw)
	}
}
