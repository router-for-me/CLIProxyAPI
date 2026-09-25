package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestResponsesWebsocketReasoningReplayPreservesCompleteOutputItems(t *testing.T) {
	const encryptedContent = "gAAAAA-test-encrypted-reasoning-content"

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	for _, payload := range [][]byte{
		[]byte(`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc-1","call_id":"call-1","name":"lookup","arguments":"{\"query\":\"status\"}","caller":"program-1","future_call":{"version":2}}}`),
		[]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs-1","encrypted_content":"` + encryptedContent + `","summary":[{"type":"summary_text","text":"checked the repository"}],"future_reasoning":{"version":2,"flags":["keep","opaque"]}}}`),
		[]byte(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg-assistant","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"ready","annotations":[]}],"future_message":{"preserve":true}}}`),
	} {
		collectResponsesWebsocketOutputItem(payload, outputItemsByIndex, &outputItemsFallback)
	}

	completedOutput := responseCompletedOutputFromPayload(
		[]byte(`{"type":"response.completed","response":{"id":"resp-a","output":[]}}`),
		outputItemsByIndex,
		outputItemsFallback,
	)
	collected := gjson.ParseBytes(completedOutput).Array()
	if len(collected) != 3 {
		t.Fatalf("collected output len = %d, want 3: %s", len(collected), completedOutput)
	}
	if got := collected[0].Get("encrypted_content").String(); got != encryptedContent {
		t.Fatalf("collected encrypted_content = %q, want exact ciphertext", got)
	}
	if got := collected[0].Get("future_reasoning").Raw; got != `{"version":2,"flags":["keep","opaque"]}` {
		t.Fatalf("collected future reasoning fields changed: %s", got)
	}
	if got := collected[1].Get("phase").String(); got != "final_answer" {
		t.Fatalf("collected assistant phase = %q, want final_answer", got)
	}
	if !collected[1].Get("future_message.preserve").Bool() {
		t.Fatalf("collected future assistant fields changed: %s", collected[1].Raw)
	}

	lastRequest := []byte(`{
		"model":"gpt-5.6-terra",
		"store":false,
		"stream":true,
		"instructions":"Preserve the complete response history.",
		"input":[{"type":"message","id":"msg-user-1","role":"user","content":[{"type":"input_text","text":"inspect"}]}]
	}`)
	raw := []byte(`{
		"type":"response.create",
		"previous_response_id":"resp-a",
		"reasoning":{"effort":"medium","context":"all_turns"},
		"include":["reasoning.encrypted_content"],
		"client_metadata":{"contract_test":"reasoning-replay"},
		"input":[
			{"type":"function_call_output","id":"fco-1","call_id":"call-1","output":"done","future_output":{"preserve":true}},
			{"type":"message","id":"msg-user-2","role":"user","content":[{"type":"input_text","text":"continue"}]}
		]
	}`)

	normalized, _, errMsg := normalizeResponseSubsequentRequest(
		raw,
		lastRequest,
		completedOutput,
		"resp-a",
		nil,
		false,
		false,
	)
	if errMsg != nil {
		t.Fatalf("normalizeResponseSubsequentRequest() error = %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("full replay retained previous_response_id: %s", normalized)
	}
	if got := gjson.GetBytes(normalized, "reasoning.context").String(); got != "all_turns" {
		t.Fatalf("reasoning.context = %q, want all_turns: %s", got, normalized)
	}
	if got := gjson.GetBytes(normalized, "client_metadata.contract_test").String(); got != "reasoning-replay" {
		t.Fatalf("client_metadata was not preserved: %s", normalized)
	}

	input := gjson.GetBytes(normalized, "input").Array()
	wantIDs := []string{"msg-user-1", "rs-1", "msg-assistant", "fc-1", "fco-1", "msg-user-2"}
	if len(input) != len(wantIDs) {
		t.Fatalf("replay input len = %d, want %d: %s", len(input), len(wantIDs), normalized)
	}
	for index, wantID := range wantIDs {
		if got := input[index].Get("id").String(); got != wantID {
			t.Fatalf("replay input[%d].id = %q, want %q: %s", index, got, wantID, normalized)
		}
	}
	if got := input[1].Get("encrypted_content").String(); got != encryptedContent {
		t.Fatalf("replayed encrypted_content = %q, want exact ciphertext", got)
	}
	if got := input[1].Get("future_reasoning").Raw; got != `{"version":2,"flags":["keep","opaque"]}` {
		t.Fatalf("replayed future reasoning fields changed: %s", got)
	}
	if got := input[2].Get("phase").String(); got != "final_answer" {
		t.Fatalf("replayed assistant phase = %q, want final_answer", got)
	}
	if got := input[3].Get("caller").String(); got != "program-1" {
		t.Fatalf("replayed function caller = %q, want program-1", got)
	}
	if !input[4].Get("future_output.preserve").Bool() {
		t.Fatalf("replayed future tool-output fields changed: %s", input[4].Raw)
	}

	var decoded map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(normalized, &decoded); errUnmarshal != nil {
		t.Fatalf("normalized replay is invalid JSON: %v", errUnmarshal)
	}
}

func TestResponsesWebsocketAuthFailoverPreservesCompleteReasoningReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		modelName        = "reasoning-auth-failover-model"
		encryptedContent = "gAAAAA-cross-account-encrypted-reasoning"
	)

	executor := &websocketPinnedFailoverExecutor{
		failStatus: http.StatusTooManyRequests,
		initialResponseChunks: [][]byte{
			[]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs-a","encrypted_content":"` + encryptedContent + `","summary":[{"type":"summary_text","text":"kept"}],"future_reasoning":{"preserve":true}}}`),
			[]byte(`{"type":"response.output_item.done","output_index":1,"item":{"type":"message","id":"msg-a","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"ready"}],"future_message":{"preserve":true}}}`),
			[]byte(`{"type":"response.output_item.done","output_index":2,"item":{"type":"function_call","id":"fc-a","call_id":"call-a","name":"lookup","arguments":"{}","caller":"program-a"}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp-auth-a-1","status":"completed","output":[]}}`),
		},
	}
	selector := &orderedWebsocketSelector{order: []string{"auth-a", "auth-b"}}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)
	for _, authID := range []string{"auth-a", "auth-b"} {
		auth := &coreauth.Auth{
			ID:         authID,
			Provider:   executor.Identifier(),
			Status:     coreauth.StatusActive,
			Attributes: map[string]string{"websockets": "true"},
		}
		if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", authID, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: modelName}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	}

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", handler.ResponsesWebsocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"

	dial := func() *websocket.Conn {
		t.Helper()
		conn, _, errDial := websocket.DefaultDialer.Dial(wsURL, nil)
		if errDial != nil {
			t.Fatalf("dial responses websocket: %v", errDial)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	readCompleted := func(conn *websocket.Conn) []byte {
		t.Helper()
		for {
			_, payload, errRead := conn.ReadMessage()
			if errRead != nil {
				t.Fatalf("read websocket response: %v", errRead)
			}
			if gjson.GetBytes(payload, "type").String() == wsEventTypeCompleted {
				return payload
			}
		}
	}

	conn := dial()
	firstRequest := fmt.Sprintf(`{"type":"response.create","model":%q,"store":false,"reasoning":{"context":"all_turns"},"include":["reasoning.encrypted_content"],"input":[{"type":"message","id":"msg-user-a","role":"user","content":"inspect"}]}`, modelName)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(firstRequest)); errWrite != nil {
		t.Fatalf("write first request: %v", errWrite)
	}
	firstCompleted := readCompleted(conn)
	firstOutput := gjson.GetBytes(firstCompleted, "response.output")
	if !firstOutput.IsArray() || len(firstOutput.Array()) != 3 {
		t.Fatalf("reconstructed first output = %s, want three collected items: %s", firstOutput.Raw, firstCompleted)
	}
	if got := firstOutput.Get("0.encrypted_content").String(); got != encryptedContent {
		t.Fatalf("reconstructed encrypted_content = %q, want exact ciphertext", got)
	}
	if got := firstOutput.Get("1.phase").String(); got != "final_answer" {
		t.Fatalf("reconstructed assistant phase = %q, want final_answer", got)
	}

	incremental := []byte(`{"type":"response.create","previous_response_id":"resp-auth-a-1","input":[{"type":"function_call_output","id":"fco-a","call_id":"call-a","output":"done","future_output":{"preserve":true}},{"type":"message","id":"msg-user-b","role":"user","content":"continue"}]}`)
	if errWrite := conn.WriteMessage(websocket.TextMessage, incremental); errWrite != nil {
		t.Fatalf("write failing incremental request: %v", errWrite)
	}
	_, _, errReadClose := conn.ReadMessage()
	var replayClose *websocket.CloseError
	if !errors.As(errReadClose, &replayClose) || replayClose.Code != websocket.CloseServiceRestart || replayClose.Text != wsHTTPReplayRequiredCloseReason {
		t.Fatalf("credential failure response = %v, want replay close %d %q", errReadClose, websocket.CloseServiceRestart, wsHTTPReplayRequiredCloseReason)
	}

	replayConn := dial()
	fullReplay := fmt.Sprintf(`{
		"type":"response.create",
		"model":%q,
		"store":false,
		"reasoning":{"effort":"medium","context":"all_turns"},
		"include":["reasoning.encrypted_content"],
		"client_metadata":{"contract_test":"auth-failover"},
		"input":[
			{"type":"message","id":"msg-user-a","role":"user","content":"inspect"},
			%s,
			{"type":"function_call_output","id":"fco-a","call_id":"call-a","output":"done","future_output":{"preserve":true}},
			{"type":"message","id":"msg-user-b","role":"user","content":"continue"}
		]
	}`, modelName, strings.TrimSuffix(strings.TrimPrefix(firstOutput.Raw, "["), "]"))
	if errWrite := replayConn.WriteMessage(websocket.TextMessage, []byte(fullReplay)); errWrite != nil {
		t.Fatalf("write full replay: %v", errWrite)
	}
	_ = readCompleted(replayConn)

	authBPayloads := executor.Payloads("auth-b")
	if len(authBPayloads) != 1 {
		t.Fatalf("auth-b payloads = %d, want 1", len(authBPayloads))
	}
	authBPayload := authBPayloads[0]
	if gjson.GetBytes(authBPayload, "previous_response_id").Exists() {
		t.Fatalf("auth-b full replay retained previous_response_id: %s", authBPayload)
	}
	if got := gjson.GetBytes(authBPayload, "reasoning.context").String(); got != "all_turns" {
		t.Fatalf("auth-b reasoning.context = %q, want all_turns: %s", got, authBPayload)
	}
	if got := gjson.GetBytes(authBPayload, "client_metadata.contract_test").String(); got != "auth-failover" {
		t.Fatalf("auth-b client_metadata was not preserved: %s", authBPayload)
	}
	input := gjson.GetBytes(authBPayload, "input").Array()
	wantIDs := []string{"msg-user-a", "rs-a", "msg-a", "fc-a", "fco-a", "msg-user-b"}
	if len(input) != len(wantIDs) {
		t.Fatalf("auth-b replay input len = %d, want %d: %s", len(input), len(wantIDs), authBPayload)
	}
	for index, wantID := range wantIDs {
		if got := input[index].Get("id").String(); got != wantID {
			t.Fatalf("auth-b input[%d].id = %q, want %q: %s", index, got, wantID, authBPayload)
		}
	}
	if got := input[1].Get("encrypted_content").String(); got != encryptedContent {
		t.Fatalf("auth-b encrypted_content = %q, want exact ciphertext", got)
	}
	if !input[1].Get("future_reasoning.preserve").Bool() || !input[2].Get("future_message.preserve").Bool() {
		t.Fatalf("auth-b replay lost future output fields: %s", authBPayload)
	}
	if got := input[2].Get("phase").String(); got != "final_answer" {
		t.Fatalf("auth-b assistant phase = %q, want final_answer", got)
	}
	if got := input[3].Get("caller").String(); got != "program-a" {
		t.Fatalf("auth-b function caller = %q, want program-a", got)
	}
	if !input[4].Get("future_output.preserve").Bool() {
		t.Fatalf("auth-b tool output lost future fields: %s", input[4].Raw)
	}
}
