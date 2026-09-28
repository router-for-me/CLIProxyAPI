package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/responsestools"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	_ "github.com/router-for-me/CLIProxyAPI/v8/internal/translator"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	openaihandlers "github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// responsesToolsIntegrationHarness wires the real HTTP handler, the real auth
// Manager, the real Codex executor, and an httptest mock upstream. The mock
// upstream speaks Codex SSE, asserts it never receives search built-ins or
// custom declarations, and returns scripted turns.
type responsesToolsIntegrationHarness struct {
	t          *testing.T
	manager    *cliproxyauth.Manager
	handler    *openaihandlers.OpenAIResponsesAPIHandler
	model      string
	mu         sync.Mutex
	nextTurn   atomic.Int32
	wireBodies []string
	turns      []string
	expectCore []bool
}

func newResponsesToolsIntegrationHarness(t *testing.T, model string, turns []string) (*responsesToolsIntegrationHarness, *httptest.Server) {
	return newResponsesToolsIntegrationHarnessWithCustomMode(t, model, turns, "inherit")
}

func newResponsesToolsIntegrationHarnessWithCustomMode(t *testing.T, model string, turns []string, customTools string) (*responsesToolsIntegrationHarness, *httptest.Server) {
	return newResponsesToolsIntegrationHarnessWithExpectations(t, model, turns, customTools, nil)
}

func newResponsesToolsIntegrationHarnessWithExpectations(t *testing.T, model string, turns []string, customTools string, expectCore []bool) (*responsesToolsIntegrationHarness, *httptest.Server) {
	t.Helper()
	h := &responsesToolsIntegrationHarness{t: t, model: model, turns: turns, expectCore: expectCore}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("mock upstream decode: %v", err)
		}
		raw, _ := json.Marshal(body)
		text := string(raw)
		h.mu.Lock()
		h.wireBodies = append(h.wireBodies, text)
		h.mu.Unlock()
		turnIndex := int(h.nextTurn.Add(1) - 1)
		if turnIndex >= len(h.turns) {
			t.Errorf("unexpected upstream call %d; scripted turns=%d", turnIndex+1, len(h.turns))
			http.Error(w, "unexpected upstream call", http.StatusInternalServerError)
			return
		}
		coreExpected := true
		if turnIndex < len(h.expectCore) {
			coreExpected = h.expectCore[turnIndex]
		}
		if coreExpected {
			if strings.Contains(text, `"type":"tool_search"`) || strings.Contains(text, `"type": "tool_search"`) {
				t.Errorf("search built-in leaked upstream: %.500s", text)
			}
			if strings.Contains(text, "tool_search_call") || strings.Contains(text, "tool_search_output") {
				t.Errorf("search protocol item leaked upstream: %.500s", text)
			}
			if strings.Contains(text, `"type":"custom"`) || strings.Contains(text, `"type": "custom"`) {
				t.Errorf("custom declaration leaked upstream: %.500s", text)
			}
		} else if !strings.Contains(text, `"type":"tool_search"`) && !strings.Contains(text, `"type": "tool_search"`) {
			t.Errorf("core-off migration turn did not pass through tool_search: %.500s", text)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if _, err := fmt.Fprint(w, h.turns[turnIndex]); err != nil {
			t.Errorf("mock upstream write: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	cfg := &config.Config{}
	enabled := true
	cfg.ResponsesTools.Enabled = &enabled
	cfg.ResponsesTools.Routes = []config.ResponsesToolsRoute{{
		Match:        config.ResponsesToolsMatch{Provider: "codex", AuthKind: "oauth", UpstreamModel: model, UpstreamFormat: "codex"},
		ClientSearch: "bridge", CustomTools: customTools,
	}}
	cfg.NormalizeResponsesToolsConfig()
	if err := cfg.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("config: %v", err)
	}
	manager.SetConfig(cfg)
	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))

	const authID = "rt-integration-auth"
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: authID, Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL, "api_key": "integration", "auth_kind": "oauth"},
		Metadata:   map[string]any{"disable_cooling": true},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	h.manager = manager
	gin.SetMode(gin.TestMode)
	h.handler = openaihandlers.NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	return h, server
}

// responsesToolsSSECompleted builds one Codex SSE body whose completed output
// carries a single function_call item.
func responsesToolsSSECompleted(callID, name, args string) string {
	itemID := "item_" + callID
	responseID := "resp_" + callID
	item := fmt.Sprintf(`{"id":%q,"type":"function_call","name":%q,"call_id":%q,"arguments":%q}`, itemID, name, callID, args)
	frames := []string{
		fmt.Sprintf(`{"type":"response.created","response":{"id":%q}}`, responseID),
		fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"id":%q,"type":"function_call","name":%q,"call_id":%q,"arguments":""}}`, itemID, name, callID),
		fmt.Sprintf(`{"type":"response.function_call_arguments.delta","item_id":%q,"output_index":0,"delta":%q}`, itemID, args),
		fmt.Sprintf(`{"type":"response.function_call_arguments.done","item_id":%q,"output_index":0,"arguments":%q}`, itemID, args),
		fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":%s}`, item),
		fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"status":"completed","output":[%s]}}`, responseID, item),
	}
	var sb strings.Builder
	for _, frame := range frames {
		sb.WriteString("data: " + frame + "\n\n")
	}
	return sb.String()
}

func responsesToolsSSECompletedMessage(text string) string {
	textJSON, _ := json.Marshal(text)
	item := fmt.Sprintf(`{"type":"message","id":"msg_final","status":"completed","role":"assistant","content":[{"type":"output_text","text":%s}]}`, textJSON)
	return fmt.Sprintf("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_final\",\"status\":\"completed\",\"output\":[%s]}}\n\n", item)
}

func (h *responsesToolsIntegrationHarness) postResponses(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.handler.Responses(c)
	return recorder
}

func openResponsesIntegrationWebsocket(t *testing.T, handler *openaihandlers.OpenAIResponsesAPIHandler) *websocket.Conn {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial responses websocket: %v", err)
	}
	t.Cleanup(func() {
		if errClose := conn.Close(); errClose != nil {
			t.Errorf("close responses websocket: %v", errClose)
		}
	})
	return conn
}

func postResponsesIntegrationWebsocketTurn(t *testing.T, conn *websocket.Conn, body string) []map[string]any {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set websocket read deadline: %v", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(body)); err != nil {
		t.Fatalf("write websocket request: %v", err)
	}
	var events []map[string]any
	for {
		_, payload, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Fatalf("read websocket event: %v", errRead)
		}
		var event map[string]any
		if errUnmarshal := json.Unmarshal(payload, &event); errUnmarshal != nil {
			t.Fatalf("decode websocket event %q: %v", payload, errUnmarshal)
		}
		if event["type"] == "error" || event["type"] == "response.failed" {
			t.Fatalf("websocket turn failed: %s", payload)
		}
		events = append(events, event)
		if event["type"] == "response.completed" {
			return events
		}
	}
}

func completedWebsocketEvent(t *testing.T, events []map[string]any) map[string]any {
	t.Helper()
	for _, event := range events {
		if event["type"] == "response.completed" {
			return event
		}
	}
	t.Fatalf("websocket turn has no response.completed event: %+v", events)
	return nil
}

func websocketOutputItemDone(t *testing.T, events []map[string]any) map[string]any {
	t.Helper()
	for _, event := range events {
		if event["type"] != "response.output_item.done" {
			continue
		}
		if item, ok := event["item"].(map[string]any); ok {
			return item
		}
	}
	t.Fatalf("websocket turn has no response.output_item.done event: %+v", events)
	return nil
}

func integrationResponseID(t *testing.T, event map[string]any) string {
	t.Helper()
	if response, ok := event["response"].(map[string]any); ok {
		if id, ok := response["id"].(string); ok {
			return id
		}
	}
	if id, ok := event["id"].(string); ok {
		return id
	}
	t.Fatalf("completed event has no response id: %#v", event)
	return ""
}

// completedOutputItems extracts output items from either the flat completed
// envelope or the nested response.completed shape the Codex executor emits.
func completedOutputItems(t *testing.T, decoded map[string]any) []any {
	t.Helper()
	if output, ok := decoded["output"].([]any); ok && len(output) > 0 {
		return output
	}
	if response, ok := decoded["response"].(map[string]any); ok {
		if output, ok := response["output"].([]any); ok && len(output) > 0 {
			return output
		}
	}
	t.Fatalf("empty completed output: %v", decoded)
	return nil
}

func integrationToolDeclarationNames(t *testing.T, body string) map[string]struct{} {
	t.Helper()
	request := decodeIntegrationRequest(t, body)
	names := make(map[string]struct{})
	var visit func(any)
	visit = func(value any) {
		switch current := value.(type) {
		case []any:
			for _, child := range current {
				visit(child)
			}
		case map[string]any:
			if kind, _ := current["type"].(string); kind == "namespace" {
				visit(current["tools"])
				return
			}
			if kind, _ := current["type"].(string); kind == "function" {
				if name, _ := current["name"].(string); name != "" {
					names[name] = struct{}{}
				}
			}
		}
	}
	visit(request["tools"])
	return names
}

func decodeIntegrationRequest(t *testing.T, body string) map[string]any {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal([]byte(body), &request); err != nil {
		t.Fatalf("decode outbound request: %v", err)
	}
	return request
}

func integrationInputItems(t *testing.T, body string) []map[string]any {
	t.Helper()
	request := decodeIntegrationRequest(t, body)
	input, _ := request["input"].([]any)
	items := make([]map[string]any, 0, len(input))
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item != nil {
			items = append(items, item)
		}
	}
	return items
}

func integrationHistoryHasItem(items []map[string]any, itemType, callID, name string) bool {
	for _, item := range items {
		if item["type"] != itemType || item["call_id"] != callID {
			continue
		}
		if name != "" && item["name"] != name {
			continue
		}
		return true
	}
	return false
}

func integrationHistoryOutputDeclaresAlias(t *testing.T, items []map[string]any, callID, alias string) bool {
	t.Helper()
	for _, item := range items {
		if item["type"] != "function_call_output" || item["call_id"] != callID {
			continue
		}
		output, _ := item["output"].(string)
		var manifest map[string]any
		if err := json.Unmarshal([]byte(output), &manifest); err != nil {
			t.Fatalf("decode discovery manifest %q: %v", output, err)
		}
		tools, _ := manifest["tools"].([]any)
		for _, rawTool := range tools {
			tool, _ := rawTool.(map[string]any)
			if tool["name"] == alias {
				return true
			}
		}
	}
	return false
}

func decodeResponsesSSEFrames(t *testing.T, body string) []map[string]any {
	t.Helper()
	var decoded []map[string]any
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		if strings.TrimSpace(frame) == "" {
			continue
		}
		var dataLines []string
		for _, line := range strings.Split(frame, "\n") {
			line = strings.TrimRight(line, "\r")
			if strings.HasPrefix(line, "data:") {
				dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(dataLines) == 0 {
			t.Fatalf("SSE frame has no data field: %q", frame)
		}
		payload := strings.Join(dataLines, "\n")
		if payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			t.Fatalf("invalid JSON in SSE data frame %q: %v", frame, err)
		}
		decoded = append(decoded, event)
	}
	return decoded
}

// TestResponsesToolsIntegrationSearchLoop runs the complete client search
// loop through the real HTTP handler, Manager, Codex executor, and mock
// upstream: search -> discovery -> call -> result -> final answer.
func TestResponsesToolsIntegrationSearchLoop(t *testing.T) {
	const model = "gpt-5.6-sol"
	h, _ := newResponsesToolsIntegrationHarness(t, model, []string{
		responsesToolsSSECompleted("cs_1", "tool_search", `{"query":"read file"}`),
		responsesToolsSSECompleted("call_2", "fs__read", ` {}`),
		responsesToolsSSECompletedMessage("done"),
	})

	turn1 := fmt.Sprintf(`{"model":%q,"tools":[{"type":"tool_search"},{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","defer_loading":true,"parameters":{"type":"object"}}]}],"input":[{"type":"message","role":"user","content":"read the file"}]}`, model)
	recorder := h.postResponses(t, turn1)
	if recorder.Code != http.StatusOK {
		t.Fatalf("turn1 status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var decoded1 map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded1); err != nil {
		t.Fatalf("turn1 decode: %v", err)
	}
	output1 := completedOutputItems(t, decoded1)
	item1, _ := output1[0].(map[string]any)
	if item1["type"] != "tool_search_call" || item1["call_id"] != "cs_1" {
		t.Fatalf("turn1 search identity not restored: %s", recorder.Body.String())
	}

	turn2 := fmt.Sprintf(`{"model":%q,"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"cs_1","arguments":{"query":"read file"}},{"type":"tool_search_output","call_id":"cs_1","tools":[{"type":"function","name":"read","namespace":"fs","parameters":{"type":"object"}}]}]}`, model)
	recorder2 := h.postResponses(t, turn2)
	if recorder2.Code != http.StatusOK {
		t.Fatalf("turn2 status=%d body=%s", recorder2.Code, recorder2.Body.String())
	}
	var decoded2 map[string]any
	if err := json.Unmarshal(recorder2.Body.Bytes(), &decoded2); err != nil {
		t.Fatalf("turn2 decode: %v", err)
	}
	output := completedOutputItems(t, decoded2)
	item2, _ := output[0].(map[string]any)
	if item2["type"] != "function_call" || item2["name"] != "read" || item2["namespace"] != "fs" || item2["call_id"] != "call_2" {
		t.Fatalf("turn2 call identity not restored: %s", recorder2.Body.String())
	}

	turn3 := fmt.Sprintf(`{"model":%q,"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_call","call_id":"cs_1","arguments":{"query":"read file"}},{"type":"tool_search_output","call_id":"cs_1","tools":[{"type":"function","name":"read","namespace":"fs","parameters":{"type":"object"}}]},{"type":"function_call","name":"read","namespace":"fs","call_id":"call_2","arguments":" {}"},{"type":"function_call_output","call_id":"call_2","output":"contents"}]}`, model)
	recorder3 := h.postResponses(t, turn3)
	if recorder3.Code != http.StatusOK {
		t.Fatalf("turn3 status=%d body=%s", recorder3.Code, recorder3.Body.String())
	}
	if !strings.Contains(recorder3.Body.String(), "done") {
		t.Fatalf("turn3 final answer missing: %s", recorder3.Body.String())
	}

	h.mu.Lock()
	wireBodies := append([]string(nil), h.wireBodies...)
	h.mu.Unlock()
	if len(wireBodies) != 3 {
		t.Fatalf("expected 3 upstream calls, got %d", len(wireBodies))
	}
	activatedAlias := ""
	for i, wireBody := range wireBodies {
		names := integrationToolDeclarationNames(t, wireBody)
		if _, ok := names["tool_search"]; !ok {
			t.Errorf("turn%d did not declare the translated search function upstream: %s", i+1, wireBody)
		}
		turnAlias := ""
		for name := range names {
			if strings.HasPrefix(name, "cts_") {
				turnAlias = name
				break
			}
		}
		if i == 0 && turnAlias != "" {
			t.Errorf("turn1 eagerly declared deferred tool alias %q: %s", turnAlias, wireBody)
		}
		if i > 0 && turnAlias == "" {
			t.Errorf("turn%d did not activate discovered tool upstream: %s", i+1, wireBody)
		}
		if i == 1 {
			activatedAlias = turnAlias
			items := integrationInputItems(t, wireBody)
			if !integrationHistoryHasItem(items, "function_call", "cs_1", "tool_search") ||
				!integrationHistoryHasItem(items, "function_call_output", "cs_1", "") ||
				!integrationHistoryOutputDeclaresAlias(t, items, "cs_1", turnAlias) {
				t.Errorf("turn2 did not preserve discovery history and active alias: %s", wireBody)
			}
			if integrationHistoryHasItem(items, "function_call", "call_2", "") {
				t.Errorf("turn2 unexpectedly contains the call returned by that same turn: %s", wireBody)
			}
		}
		if i == 2 {
			items := integrationInputItems(t, wireBody)
			if turnAlias != activatedAlias ||
				!integrationHistoryHasItem(items, "function_call", "cs_1", "tool_search") ||
				!integrationHistoryOutputDeclaresAlias(t, items, "cs_1", turnAlias) ||
				!integrationHistoryHasItem(items, "function_call", "call_2", turnAlias) ||
				!integrationHistoryHasItem(items, "function_call_output", "call_2", "") {
				t.Errorf("turn3 did not preserve discovery and tool-call history: %s", wireBody)
			}
		}
	}
}

// TestResponsesToolsIntegrationSSEStream runs the search bridge over the real
// HTTP SSE path and asserts the downstream stream restores tool_search_call.
func TestResponsesToolsIntegrationSSEStream(t *testing.T) {
	const model = "gpt-5.6-sol"
	h, _ := newResponsesToolsIntegrationHarness(t, model, []string{
		responsesToolsSSECompleted("cs_stream", "tool_search", `{"query":"x"}`),
	})
	turn := fmt.Sprintf(`{"model":%q,"stream":true,"tools":[{"type":"tool_search"}],"input":[{"type":"message","role":"user","content":"hi"}],"stream_options":{"include_usage":true}}`, model)
	recorder := h.postResponses(t, turn)
	if recorder.Code != http.StatusOK {
		t.Fatalf("stream status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if recorder.Header().Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream content type=%q, want text/event-stream", recorder.Header().Get("Content-Type"))
	}
	frames := decodeResponsesSSEFrames(t, body)
	sawSearchCall, sawCompleted := false, false
	for _, frame := range frames {
		switch frame["type"] {
		case "response.output_item.done":
			item, _ := frame["item"].(map[string]any)
			if item["type"] == "tool_search_call" && item["call_id"] == "cs_stream" {
				sawSearchCall = true
			}
		case "response.completed":
			sawCompleted = true
			output := completedOutputItems(t, frame)
			item, _ := output[0].(map[string]any)
			if item["type"] != "tool_search_call" || item["call_id"] != "cs_stream" {
				t.Fatalf("SSE response.completed did not restore search output: %#v", item)
			}
		}
	}
	if !sawSearchCall || !sawCompleted {
		t.Fatalf("SSE stream missing restored tool_search_call or terminal event: %+v", frames)
	}
}

// TestResponsesToolsIntegrationCustomStream verifies custom input is restored
// through the real HTTP handler, Manager, Codex executor, and SSE framer.
func TestResponsesToolsIntegrationCustomStream(t *testing.T) {
	const model = "gpt-5.6-sol"
	const customInput = "line1\nline2\r\n  unicode: 中文 \\ {\"json\": true}"
	args, err := json.Marshal(map[string]string{"input": customInput})
	if err != nil {
		t.Fatalf("marshal custom function arguments: %v", err)
	}
	alias := responsestools.CustomAliasFor("", "apply_patch")
	h, _ := newResponsesToolsIntegrationHarnessWithCustomMode(t, model, []string{
		responsesToolsSSECompleted("custom_1", alias, string(args)),
	}, "function")
	turn := fmt.Sprintf(`{"model":%q,"stream":true,"tools":[{"type":"custom","name":"apply_patch","description":"apply a patch"}],"input":[{"type":"message","role":"user","content":"apply this patch"}]}`, model)
	recorder := h.postResponses(t, turn)
	if recorder.Code != http.StatusOK {
		t.Fatalf("custom stream status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	frames := decodeResponsesSSEFrames(t, recorder.Body.String())
	sawCustomCall, sawCustomCompletion := false, false
	for _, frame := range frames {
		switch frame["type"] {
		case "response.output_item.done":
			item, _ := frame["item"].(map[string]any)
			if item["type"] == "custom_tool_call" {
				if item["name"] != "apply_patch" || item["call_id"] != "custom_1" || item["input"] != customInput {
					t.Fatalf("custom call was not restored exactly: %#v", item)
				}
				sawCustomCall = true
			}
		case "response.completed":
			for _, rawItem := range completedOutputItems(t, frame) {
				item, _ := rawItem.(map[string]any)
				if item["type"] == "custom_tool_call" && item["name"] == "apply_patch" &&
					item["call_id"] == "custom_1" && item["input"] == customInput {
					sawCustomCompletion = true
				}
			}
		}
	}
	if !sawCustomCall || !sawCustomCompletion {
		t.Fatalf("custom stream did not restore both output_item.done and response.completed: %+v", frames)
	}
}

func TestResponsesToolsIntegrationNamespacedCustomCall(t *testing.T) {
	const model = "gpt-5.6-sol"
	const customInput = "apply patch in namespace"
	args, err := json.Marshal(map[string]string{"input": customInput})
	if err != nil {
		t.Fatalf("marshal custom function arguments: %v", err)
	}
	alias := responsestools.CustomAliasFor("fs", "apply_patch")
	h, _ := newResponsesToolsIntegrationHarnessWithCustomMode(t, model, []string{
		responsesToolsSSECompleted("custom_ns_1", alias, string(args)),
	}, "function")
	turn := fmt.Sprintf(`{"model":%q,"tools":[{"type":"namespace","name":"fs","tools":[{"type":"custom","name":"apply_patch","description":"apply a patch"}]}],"input":[{"type":"message","role":"user","content":"apply this patch"}]}`, model)
	recorder := h.postResponses(t, turn)
	if recorder.Code != http.StatusOK {
		t.Fatalf("namespaced custom status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode namespaced custom response: %v", err)
	}
	output := completedOutputItems(t, decoded)
	item, _ := output[0].(map[string]any)
	if item["type"] != "custom_tool_call" || item["name"] != "apply_patch" ||
		item["namespace"] != "fs" || item["call_id"] != "custom_ns_1" || item["input"] != customInput {
		t.Fatalf("namespaced custom identity not restored: %#v", item)
	}

	h.mu.Lock()
	wireBodies := append([]string(nil), h.wireBodies...)
	h.mu.Unlock()
	if len(wireBodies) != 1 {
		t.Fatalf("expected one namespaced custom upstream request, got %d", len(wireBodies))
	}
	names := integrationToolDeclarationNames(t, wireBodies[0])
	if _, exists := names[alias]; !exists {
		t.Fatalf("namespaced custom alias %q not found upstream: %s", alias, wireBodies[0])
	}
}

// TestResponsesToolsIntegrationWebsocketReplay runs search discovery, an
// activated namespaced function call, and its result through the real
// Responses websocket handler while replaying previous_response_id locally.
func TestResponsesToolsIntegrationWebsocketReplay(t *testing.T) {
	const model = "gpt-5.6-sol"
	searchAlias := responsestools.HashedToolAlias(responsestools.ToolIdentity{
		Namespace: "fs", Name: "read", Kind: responsestools.ToolKindFunction,
	})
	h, _ := newResponsesToolsIntegrationHarness(t, model, []string{
		responsesToolsSSECompleted("cs_ws", "tool_search", `{"query":"read file"}`),
		responsesToolsSSECompleted("call_ws", searchAlias, ` {}`),
		responsesToolsSSECompletedMessage("done"),
	})
	conn := openResponsesIntegrationWebsocket(t, h.handler)

	turn1 := fmt.Sprintf(`{"type":"response.create","model":%q,"tools":[{"type":"tool_search"},{"type":"namespace","name":"fs","tools":[{"type":"function","name":"read","defer_loading":true,"parameters":{"type":"object"}}]}],"input":[{"type":"message","role":"user","content":"read the file"}]}`, model)
	events1 := postResponsesIntegrationWebsocketTurn(t, conn, turn1)
	response1 := completedWebsocketEvent(t, events1)
	searchCall := websocketOutputItemDone(t, events1)
	if searchCall["type"] != "tool_search_call" || searchCall["call_id"] != "cs_ws" {
		h.mu.Lock()
		wireBodies := append([]string(nil), h.wireBodies...)
		h.mu.Unlock()
		t.Fatalf("websocket turn1 did not restore tool_search_call: events=%+v upstream=%v", events1, wireBodies)
	}
	completedOutput1 := completedOutputItems(t, response1)
	completedSearchCall, _ := completedOutput1[0].(map[string]any)
	if completedSearchCall["type"] != "tool_search_call" || completedSearchCall["call_id"] != "cs_ws" {
		t.Fatalf("websocket response.completed did not preserve the restored search call: %#v; events=%+v", completedSearchCall, events1)
	}
	responseID1 := integrationResponseID(t, response1)

	turn2 := fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"model":%q,"tools":[{"type":"tool_search"}],"input":[{"type":"tool_search_output","call_id":"cs_ws","tools":[{"type":"function","name":"read","namespace":"fs","parameters":{"type":"object"}}]}]}`, responseID1, model)
	events2 := postResponsesIntegrationWebsocketTurn(t, conn, turn2)
	response2 := completedWebsocketEvent(t, events2)
	functionCall := websocketOutputItemDone(t, events2)
	if functionCall["type"] != "function_call" || functionCall["name"] != "read" ||
		functionCall["namespace"] != "fs" || functionCall["call_id"] != "call_ws" {
		h.mu.Lock()
		wireBodies := append([]string(nil), h.wireBodies...)
		h.mu.Unlock()
		t.Fatalf("websocket turn2 did not restore discovered tool identity: output=%#v events=%+v upstream=%v", functionCall, events2, wireBodies)
	}
	responseID2 := integrationResponseID(t, response2)

	turn3 := fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"model":%q,"tools":[{"type":"tool_search"}],"input":[{"type":"function_call_output","call_id":"call_ws","output":"contents"}]}`, responseID2, model)
	events3 := postResponsesIntegrationWebsocketTurn(t, conn, turn3)
	response3 := completedWebsocketEvent(t, events3)
	output3 := completedOutputItems(t, response3)
	if !strings.Contains(fmt.Sprint(output3), "done") {
		t.Fatalf("websocket final answer missing: %#v", output3)
	}

	h.mu.Lock()
	wireBodies := append([]string(nil), h.wireBodies...)
	h.mu.Unlock()
	if len(wireBodies) != 3 {
		t.Fatalf("expected three websocket replay upstream calls, got %d", len(wireBodies))
	}
	for i, wireBody := range wireBodies {
		names := integrationToolDeclarationNames(t, wireBody)
		if _, ok := names["tool_search"]; !ok {
			t.Errorf("websocket turn%d did not declare translated search function: %s", i+1, wireBody)
		}
		if i > 0 {
			if _, ok := names[searchAlias]; !ok {
				t.Errorf("websocket turn%d did not declare activated alias %q: %s", i+1, searchAlias, wireBody)
			}
		}
		if strings.Contains(wireBody, "tool_search_call") || strings.Contains(wireBody, "tool_search_output") {
			t.Errorf("websocket turn%d leaked client search history upstream: %s", i+1, wireBody)
		}
	}
}

func responsesToolsToggleConfig(model string, coreEnabled bool) *config.Config {
	cfg := &config.Config{}
	if coreEnabled {
		enabled := true
		cfg.ResponsesTools.Enabled = &enabled
		cfg.ResponsesTools.Routes = []config.ResponsesToolsRoute{{
			Match: config.ResponsesToolsMatch{
				Provider: "codex", AuthKind: "oauth", UpstreamModel: model, UpstreamFormat: "codex",
			},
			ClientSearch: "bridge", CustomTools: "inherit",
		}}
	}
	cfg.NormalizeResponsesToolsConfig()
	return cfg
}

// TestResponsesToolsIntegrationToggleAndRollback drives the emergency gate
// end to end: an explicit bridge route restores the client search call, and
// dropping back to the convention policy stops rewriting the declaration.
func TestResponsesToolsIntegrationToggleAndRollback(t *testing.T) {
	const model = "gpt-5.6-sol"
	h, _ := newResponsesToolsIntegrationHarnessWithExpectations(t, model, []string{
		responsesToolsSSECompleted("passthrough_1", "tool_search", `{"query":"x"}`),
		responsesToolsSSECompleted("core_1", "tool_search", `{"query":"x"}`),
		responsesToolsSSECompleted("passthrough_2", "tool_search", `{"query":"x"}`),
	}, "inherit", []bool{false, true, false})

	convention := responsesToolsToggleConfig(model, false)
	if err := convention.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("validate convention baseline: %v", err)
	}
	h.manager.SetConfig(convention)

	postSearch := func(t *testing.T, callID string) map[string]any {
		t.Helper()
		body := fmt.Sprintf(`{"model":%q,"tools":[{"type":"tool_search"}],"input":[{"type":"message","role":"user","content":"find a tool"}]}`, model)
		recorder := h.postResponses(t, body)
		if recorder.Code != http.StatusOK {
			t.Fatalf("toggle turn %s status=%d body=%s", callID, recorder.Code, recorder.Body.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("toggle turn %s decode: %v", callID, err)
		}
		output := completedOutputItems(t, decoded)
		item, _ := output[0].(map[string]any)
		if item["call_id"] != callID {
			t.Fatalf("toggle turn call_id=%v, want %s: %#v", item["call_id"], callID, item)
		}
		return item
	}

	before := postSearch(t, "passthrough_1")
	if before["type"] != "function_call" {
		t.Fatalf("a native route must keep the upstream function call, got %#v", before)
	}

	bridged := responsesToolsToggleConfig(model, true)
	if err := bridged.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("validate bridge cutover: %v", err)
	}
	h.manager.SetConfig(bridged)
	coreCall := postSearch(t, "core_1")
	if coreCall["type"] != "tool_search_call" {
		t.Fatalf("bridged route did not restore client search: %#v", coreCall)
	}

	rollback := responsesToolsToggleConfig(model, false)
	if err := rollback.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("validate rollback config: %v", err)
	}
	h.manager.SetConfig(rollback)
	after := postSearch(t, "passthrough_2")
	if after["type"] != "function_call" {
		t.Fatalf("rollback must restore the convention behavior, got %#v", after)
	}

	h.mu.Lock()
	wireBodies := append([]string(nil), h.wireBodies...)
	h.mu.Unlock()
	if len(wireBodies) != 3 {
		t.Fatalf("toggle drill upstream calls=%d, want 3", len(wireBodies))
	}
}

// TestResponsesToolsIntegrationNativePassthroughByDefault proves the
// convention-on posture through the full chain: an OpenAI-native route keeps
// the native protocol, so the client search declaration is forwarded
// byte-for-byte without any configuration.
func TestResponsesToolsIntegrationNativePassthroughByDefault(t *testing.T) {
	const model = "gpt-5.6-sol"
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	cfg := &config.Config{}
	manager.SetConfig(cfg)

	var wire atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		wire.Store(string(raw))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, responsesToolsSSECompleted("c1", "tool_search", `{"query":"x"}`))
	}))
	defer server.Close()

	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
	const authID = "rt-integration-off"
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: authID, Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL, "api_key": "off", "auth_kind": "oauth"},
		Metadata:   map[string]any{"disable_cooling": true},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	body := fmt.Sprintf(`{"model":%q,"tools":[{"type":"tool_search"}],"input":[]}`, model)
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)}
	if _, err := manager.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got, _ := wire.Load().(string)
	if !strings.Contains(got, `"type":"tool_search"`) {
		t.Fatalf("native route must pass the declaration through untouched: %.500s", got)
	}
}

// TestResponsesToolsIntegrationEmergencyGate verifies that an explicit
// enabled:false turns the whole feature off, including routes whose convention
// policy would otherwise bridge.
func TestResponsesToolsIntegrationEmergencyGate(t *testing.T) {
	const model = "gpt-5.6-sol"
	manager := cliproxyauth.NewManager(nil, &cliproxyauth.RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	disabled := false
	cfg := &config.Config{}
	cfg.ResponsesTools.Enabled = &disabled
	cfg.ResponsesTools.Routes = []config.ResponsesToolsRoute{{
		Match: config.ResponsesToolsMatch{
			Provider: "codex", AuthKind: "oauth", UpstreamModel: model, UpstreamFormat: "codex",
		},
		ClientSearch: "bridge", CustomTools: "inherit",
	}}
	cfg.NormalizeResponsesToolsConfig()
	if err := cfg.ValidateResponsesToolsConfig(); err != nil {
		t.Fatalf("config: %v", err)
	}
	manager.SetConfig(cfg)

	var wire atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := json.Marshal(body)
		wire.Store(string(raw))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, responsesToolsSSECompleted("c1", "tool_search", `{"query":"x"}`))
	}))
	defer server.Close()

	manager.RegisterExecutor(runtimeexecutor.NewCodexExecutor(cfg))
	const authID = "rt-integration-gate"
	registry.GetGlobalRegistry().RegisterClient(authID, "codex", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	if _, err := manager.Register(context.Background(), &cliproxyauth.Auth{
		ID: authID, Provider: "codex", Status: cliproxyauth.StatusActive,
		Attributes: map[string]string{"base_url": server.URL, "api_key": "off", "auth_kind": "oauth"},
		Metadata:   map[string]any{"disable_cooling": true},
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	body := fmt.Sprintf(`{"model":%q,"tools":[{"type":"tool_search"}],"input":[]}`, model)
	req := cliproxyexecutor.Request{Model: model, Payload: []byte(body)}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatCodex, OriginalRequest: []byte(body)}
	if _, err := manager.Execute(context.Background(), []string{"codex"}, req, opts); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got, _ := wire.Load().(string)
	if !strings.Contains(got, `"type":"tool_search"`) {
		t.Fatalf("emergency gate must pass the declaration through untouched: %.500s", got)
	}
}
