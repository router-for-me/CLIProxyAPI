package openai

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	multiagentv2 "github.com/router-for-me/CLIProxyAPI/v7/internal/client/codex/optimize-multi-agent-v2"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestPrepareCodexMultiAgentV2ToolsAtResponsesBoundary(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{CodexOptimizeMultiAgentV2: true}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = request

	payload := []byte(`{
        "tools":[{"type":"namespace","name":"collaboration","tools":[
            {"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"properties":{"message":{"encrypted":true}}}},
            {"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}}
        ]}]
    }`)
	got := handler.prepareCodexMultiAgentV2Tools(ginContext, payload)

	if namespace := gjson.GetBytes(got, "tools.0.name").String(); namespace != "collaboration" {
		t.Fatalf("namespace = %q, want collaboration", namespace)
	}
	for _, path := range []string{"tools.0.tools.0", "tools.0.tools.1"} {
		if encrypted := gjson.GetBytes(got, path+".parameters.properties.message.encrypted"); encrypted.Exists() {
			t.Fatalf("%s message.encrypted was not removed: %s", path, encrypted.Raw)
		}
	}
	prepared, exists := ginContext.Get(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey)
	if !exists || prepared != true {
		t.Fatalf("prepared marker = %#v, want true", prepared)
	}
}

func TestResponsesPreparesCodexMultiAgentV2ToolsForHTTPAndSSE(t *testing.T) {
	t.Parallel()

	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			executor := &responsesMultiAgentCaptureExecutor{}
			handler, modelID := newResponsesMultiAgentTestHandler(t, executor)
			router := gin.New()
			router.POST("/v1/responses", handler.Responses)

			payload := fmt.Sprintf(`{"model":%q,"stream":%t,"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"properties":{"message":{"encrypted":true}}}}]}]}`, modelID, stream)
			request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(payload))
			request.Header.Set("User-Agent", "codex_cli_rs/0.144.1")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
			}

			payloads := executor.Payloads()
			if len(payloads) != 1 {
				t.Fatalf("captured payload count = %d, want 1", len(payloads))
			}
			captured := payloads[0]
			if encrypted := gjson.GetBytes(captured, "tools.0.tools.0.parameters.properties.message.encrypted"); encrypted.Exists() {
				t.Fatalf("message.encrypted was not removed: %s", captured)
			}
			if namespace := gjson.GetBytes(captured, "tools.0.name").String(); namespace != "collaboration" {
				t.Fatalf("namespace = %q, want collaboration", namespace)
			}
		})
	}
}

type responsesMultiAgentCaptureExecutor struct {
	websocketDirectCaptureExecutor
}

func (e *responsesMultiAgentCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	e.mu.Unlock()
	return coreexecutor.Response{Payload: []byte(`{"id":"resp-1","output":[]}`)}, nil
}

func (e *responsesMultiAgentCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	e.mu.Unlock()
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[]}}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func newResponsesMultiAgentTestHandler(t *testing.T, executor *responsesMultiAgentCaptureExecutor) (*OpenAIResponsesAPIHandler, string) {
	t.Helper()

	modelID := "responses-multi-agent-test-model"
	authID := "responses-multi-agent-test-auth"
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct"}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{CodexOptimizeMultiAgentV2: true}, manager)
	return NewOpenAIResponsesAPIHandler(base), modelID
}

func TestResponsesWebsocketPreparesCodexMultiAgentV2Tools(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &websocketDirectCaptureExecutor{provider: "codex"}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "responses-multi-agent-ws-auth", Provider: "codex", Status: coreauth.StatusActive, ProxyURL: "direct"}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register auth: %v", errRegister)
	}
	modelID := "responses-multi-agent-ws-model"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{CodexOptimizeMultiAgentV2: true}, manager)
	handler := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, http.Header{"User-Agent": []string{"codex_cli_rs/0.144.1"}})
	if errDial != nil {
		t.Fatalf("dial websocket: %v", errDial)
	}
	defer func() { _ = conn.Close() }()

	request := fmt.Sprintf(`{"type":"response.create","model":%q,"input":[],"tools":[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"Spawns an agent.","parameters":{"properties":{"message":{"encrypted":true}}}}]}]}`, modelID)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(request)); errWrite != nil {
		t.Fatalf("write websocket request: %v", errWrite)
	}
	if _, _, errRead := conn.ReadMessage(); errRead != nil {
		t.Fatalf("read websocket response: %v", errRead)
	}

	payloads := executor.Payloads()
	if len(payloads) != 1 {
		t.Fatalf("captured payload count = %d, want 1", len(payloads))
	}
	captured := payloads[0]
	if encrypted := gjson.GetBytes(captured, "tools.0.tools.0.parameters.properties.message.encrypted"); encrypted.Exists() {
		t.Fatalf("message.encrypted was not removed: %s", captured)
	}
	if namespace := gjson.GetBytes(captured, "tools.0.name").String(); namespace != "collaboration" {
		t.Fatalf("namespace = %q, want collaboration", namespace)
	}
}

func TestPrepareCodexMultiAgentV2ToolsAtResponsesBoundarySkipsOtherClients(t *testing.T) {
	t.Parallel()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{CodexOptimizeMultiAgentV2: true}, nil)
	handler := NewOpenAIResponsesAPIHandler(base)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	request.Header.Set("User-Agent", "curl/8.7.1")
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginContext.Request = request

	payload := []byte(`{"tools":[{"type":"function","name":"send_message","parameters":{"properties":{"message":{"encrypted":true}}}}]}`)
	got := handler.prepareCodexMultiAgentV2Tools(ginContext, payload)

	if string(got) != string(payload) {
		t.Fatalf("other client payload changed: %s", got)
	}
	if _, exists := ginContext.Get(multiagentv2.CodexMultiAgentV2ToolsPreparedContextKey); exists {
		t.Fatal("other client unexpectedly received prepared marker")
	}
}
