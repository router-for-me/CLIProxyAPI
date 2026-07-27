package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

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
