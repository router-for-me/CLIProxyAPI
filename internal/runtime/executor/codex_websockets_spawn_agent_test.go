package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexWebsocketsExecutorRestoresMultiAgentV2NamespaceAcrossIncrementalTurns(t *testing.T) {
	for _, tt := range []struct {
		name   string
		stream bool
	}{
		{name: "execute"},
		{name: "stream", stream: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			capturedPayload := make(chan []byte, 6)
			var connectionCount atomic.Int32
			var requestCount atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				connectionCount.Add(1)
				conn, errUpgrade := upgrader.Upgrade(w, request, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()

				for {
					_, payload, errRead := conn.ReadMessage()
					if errRead != nil {
						return
					}
					capturedPayload <- append([]byte(nil), payload...)
					turn := requestCount.Add(1)
					completed := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_%d","object":"response","status":"completed","output":[{"type":"function_call","name":"spawn_agent","namespace":"collaboration-optimize","arguments":"{}","call_id":"call_%d"}]}}`, turn, turn))
					if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
						t.Errorf("write websocket response: %v", errWrite)
						return
					}
					if turn == 6 {
						return
					}
				}
			}))
			t.Cleanup(server.Close)

			executor := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: true}})
			const executionSessionID = "multi-agent-v2-incremental"
			t.Cleanup(func() { executor.CloseExecutionSession(executionSessionID) })
			auth := &cliproxyauth.Auth{
				ID: "codex-test",
				Attributes: map[string]string{
					"base_url": server.URL,
					"api_key":  "test",
				},
			}
			opts := cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FromString("openai-response"),
				ResponseFormat: sdktranslator.FromString("openai-response"),
				Headers:        http.Header{"User-Agent": []string{"overridden-client/1.0"}},
				Metadata: map[string]any{
					cliproxyexecutor.ExecutionSessionMetadataKey: executionSessionID,
				},
			}
			execute := func(payload []byte) []byte {
				t.Helper()
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: payload}
				if !tt.stream {
					response, errExecute := executor.Execute(codexSpawnAgentTestContext(), auth, req, opts)
					if errExecute != nil {
						t.Fatalf("Execute() error = %v", errExecute)
					}
					return response.Payload
				}

				result, errExecute := executor.ExecuteStream(codexSpawnAgentTestContext(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				var responsePayload []byte
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatalf("stream chunk error = %v", chunk.Err)
					}
					responsePayload = append(responsePayload, chunk.Payload...)
				}
				return responsePayload
			}

			firstClientPayload := execute(codexSpawnAgentTestPayload())
			firstUpstreamPayload := <-capturedPayload
			if namespace := gjson.GetBytes(firstUpstreamPayload, "input.0.tools.0.name").String(); namespace != "collaboration-optimize" {
				t.Fatalf("first upstream namespace = %q, want collaboration-optimize", namespace)
			}
			assertCodexSpawnAgentClientNamespace(t, firstClientPayload)

			secondRequest := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call_1","output":"done"}]}`)
			secondClientPayload := execute(secondRequest)
			secondUpstreamPayload := <-capturedPayload
			if strings.Contains(string(secondUpstreamPayload), "collaboration") || strings.Contains(string(secondUpstreamPayload), "spawn_agent") {
				t.Fatalf("incremental upstream request unexpectedly contains collaboration tools: %s", secondUpstreamPayload)
			}
			assertCodexSpawnAgentClientNamespace(t, secondClientPayload)

			conflictingRequest := []byte(`{"model":"gpt-5.4","tools":[{"type":"namespace","name":"collaboration-optimize","tools":[{"type":"function","name":"spawn_agent","description":"User-defined tool."}]}],"input":[{"type":"message","role":"user","content":"use the user-defined namespace"}]}`)
			conflictingClientPayload := execute(conflictingRequest)
			conflictingUpstreamPayload := <-capturedPayload
			if namespace := gjson.GetBytes(conflictingUpstreamPayload, "tools.0.name").String(); namespace != "collaboration-optimize" {
				t.Fatalf("conflicting upstream namespace = %q, want collaboration-optimize", namespace)
			}
			if !strings.Contains(string(conflictingClientPayload), `"namespace":"collaboration-optimize"`) {
				t.Fatalf("user-defined collaboration-optimize namespace was rewritten: %s", conflictingClientPayload)
			}

			fourthRequest := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_3","input":[{"type":"function_call_output","call_id":"call_3","output":"done"}]}`)
			fourthClientPayload := execute(fourthRequest)
			fourthUpstreamPayload := <-capturedPayload
			if strings.Contains(string(fourthUpstreamPayload), "collaboration") || strings.Contains(string(fourthUpstreamPayload), "spawn_agent") {
				t.Fatalf("post-conflict incremental upstream request unexpectedly contains collaboration tools: %s", fourthUpstreamPayload)
			}
			if !strings.Contains(string(fourthClientPayload), `"namespace":"collaboration-optimize"`) {
				t.Fatalf("user-defined namespace was rewritten on the post-conflict incremental turn: %s", fourthClientPayload)
			}

			fifthClientPayload := execute(codexSpawnAgentTestPayload())
			fifthUpstreamPayload := <-capturedPayload
			if namespace := gjson.GetBytes(fifthUpstreamPayload, "input.0.tools.0.name").String(); namespace != "collaboration-optimize" {
				t.Fatalf("re-enabled upstream namespace = %q, want collaboration-optimize", namespace)
			}
			assertCodexSpawnAgentClientNamespace(t, fifthClientPayload)

			sixthRequest := []byte(`{"model":"gpt-5.4","previous_response_id":"resp_5","input":[{"type":"function_call_output","call_id":"call_5","output":"done"}]}`)
			sixthClientPayload := execute(sixthRequest)
			sixthUpstreamPayload := <-capturedPayload
			if strings.Contains(string(sixthUpstreamPayload), "collaboration") || strings.Contains(string(sixthUpstreamPayload), "spawn_agent") {
				t.Fatalf("re-enabled incremental upstream request unexpectedly contains collaboration tools: %s", sixthUpstreamPayload)
			}
			assertCodexSpawnAgentClientNamespace(t, sixthClientPayload)

			if got := connectionCount.Load(); got != 1 {
				t.Fatalf("upstream websocket connections = %d, want 1", got)
			}
		})
	}
}

func TestCodexWebsocketsExecutorOptimizeMultiAgentV2(t *testing.T) {
	modelID := "codex-websocket-spawn-agent-test-model"
	clientID := "codex-websocket-spawn-agent-test-client"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:          modelID,
		Description: "Executor test model.",
		Thinking: &registry.ThinkingSupport{
			Levels: []string{"low", "medium", "high"},
		},
	}})
	defer modelRegistry.UnregisterClient(clientID)

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	capturedPayload := make(chan []byte, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, request, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read websocket request: %v", errRead)
			return
		}
		capturedPayload <- payload
		namespace := gjson.GetBytes(payload, "input.0.tools.0.name").String()
		completed := []byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_1","object":"response","status":"completed","output":[{"type":"function_call","name":"spawn_agent","namespace":%q,"arguments":"{}","call_id":"call_1"}]}}`, namespace))
		if errWrite := conn.WriteMessage(websocket.TextMessage, completed); errWrite != nil {
			t.Errorf("write websocket response: %v", errWrite)
		}
	}))
	defer server.Close()

	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: codexSpawnAgentTestPayload()}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Headers:      http.Header{"User-Agent": []string{"overridden-client/1.0"}},
	}

	for _, tt := range []struct {
		name    string
		enabled bool
		stream  bool
	}{
		{name: "execute enabled", enabled: true},
		{name: "execute disabled", enabled: false},
		{name: "stream enabled", enabled: true, stream: true},
		{name: "stream disabled", enabled: false, stream: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			executor := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{OptimizeMultiAgentV2: tt.enabled}})
			var clientPayload []byte
			if tt.stream {
				result, errExecute := executor.ExecuteStream(codexSpawnAgentTestContext(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("ExecuteStream() error = %v", errExecute)
				}
				for chunk := range result.Chunks {
					clientPayload = append(clientPayload, chunk.Payload...)
				}
			} else {
				response, errExecute := executor.Execute(codexSpawnAgentTestContext(), auth, req, opts)
				if errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
				clientPayload = response.Payload
			}
			upstreamPayload := <-capturedPayload
			assertCodexSpawnAgentOptimization(t, upstreamPayload, modelID, tt.enabled)
			assertCodexSpawnAgentRequestMessage(t, upstreamPayload, tt.enabled)
			assertCodexSpawnAgentClientNamespace(t, clientPayload)
		})
	}
}
