package helps

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps/cursorproto"
	claudetranslator "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/openai/claude"
	"google.golang.org/protobuf/proto"
)

func TestBuildCursorRunPayloadWithImageAndTools(t *testing.T) {
	image := base64.StdEncoding.EncodeToString([]byte("image"))
	request := map[string]any{
		"model": "gpt-test",
		"messages": []any{
			map[string]any{"role": "system", "content": "Be concise."},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "text", "text": "Describe this"},
				map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + image}},
			}},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{
			"name": "lookup", "description": "Lookup a value", "parameters": map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}},
		}}},
	}
	raw, _ := json.Marshal(request)
	run, err := BuildCursorRunPayload(raw, "gpt-test")
	if err != nil {
		t.Fatalf("BuildCursorRunPayload() error = %v", err)
	}
	if len(run.Blobs) < 2 || len(run.Tools) != 1 {
		t.Fatalf("unexpected run: blobs=%d tools=%d", len(run.Blobs), len(run.Tools))
	}
	var message cursorproto.AgentClientMessage
	if errDecode := proto.Unmarshal(run.Message, &message); errDecode != nil {
		t.Fatalf("decode run message: %v", errDecode)
	}
	runRequest := message.GetRunRequest()
	if run.SystemPrompt != "Be concise." {
		t.Fatalf("system prompt = %q, want %q", run.SystemPrompt, "Be concise.")
	}
	userAction := runRequest.GetAction().GetUserMessageAction()
	if userAction == nil || len(userAction.GetUserMessage().GetSelectedContext().GetSelectedImages()) != 1 {
		t.Fatal("current image was not encoded")
	}
}

func TestRespondCursorExecIncludesSystemPromptInRequestContext(t *testing.T) {
	pipeReader, pipeWriter := io.Pipe()
	writer := &cursorRequestWriter{pipe: pipeWriter}
	tool := &cursorproto.McpToolDefinition{Name: "inspect_project"}
	done := make(chan error, 1)
	go func() {
		_, err := respondCursorExec(writer, []*cursorproto.McpToolDefinition{tool}, "Follow the coding agent instructions.", &cursorproto.ExecServerMessage{
			Id:      7,
			Message: &cursorproto.ExecServerMessage_RequestContextArgs{RequestContextArgs: &cursorproto.RequestContextArgs{}},
		})
		done <- err
	}()

	_, frame, errFrame := readCursorConnectFrame(pipeReader)
	if errFrame != nil {
		t.Fatalf("read response frame: %v", errFrame)
	}
	var message cursorproto.AgentClientMessage
	if errDecode := proto.Unmarshal(frame, &message); errDecode != nil {
		t.Fatalf("decode response frame: %v", errDecode)
	}
	context := message.GetExecClientMessage().GetRequestContextResult().GetSuccess().GetRequestContext()
	if got := context.GetCloudRule(); got != "Follow the coding agent instructions." {
		t.Fatalf("cloud rule = %q", got)
	}
	if len(context.GetTools()) != 1 || context.GetTools()[0].GetName() != "inspect_project" {
		t.Fatalf("tools = %#v", context.GetTools())
	}
	if errExec := <-done; errExec != nil {
		t.Fatalf("respondCursorExec() error = %v", errExec)
	}
	writer.close()
	_ = pipeReader.Close()
}

func TestBuildCursorRunPayloadRejectsRemoteImage(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com/image.png"}}]}]}`)
	_, err := BuildCursorRunPayload(raw, "gpt-test")
	if err == nil || !strings.Contains(err.Error(), "remote image URLs") {
		t.Fatalf("error = %v", err)
	}
}

func TestBuildCursorRunPayloadContinuesAfterToolResult(t *testing.T) {
	raw := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"Find it"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"found"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`)
	run, err := BuildCursorRunPayload(raw, "gpt-test")
	if err != nil {
		t.Fatalf("BuildCursorRunPayload() error = %v", err)
	}
	var message cursorproto.AgentClientMessage
	if errDecode := proto.Unmarshal(run.Message, &message); errDecode != nil {
		t.Fatalf("decode run message: %v", errDecode)
	}
	runRequest := message.GetRunRequest()
	continuation := runRequest.GetAction().GetUserMessageAction()
	if continuation == nil {
		t.Fatal("tool continuation did not use UserMessageAction")
	}
	continuationText := continuation.GetUserMessage().GetText()
	for _, want := range []string{"Find it", "found", cursorToolContinuationText} {
		if !strings.Contains(continuationText, want) {
			t.Fatalf("continuation text does not contain %q: %q", want, continuationText)
		}
	}
	if len(runRequest.GetConversationState().GetTurns()) != 1 {
		t.Fatalf("turns = %d, want 1", len(runRequest.GetConversationState().GetTurns()))
	}
}

func TestBuildCursorRunPayloadToolContinuationPreservesSystemPromptAndUserText(t *testing.T) {
	anthropic := []byte(`{"model":"gpt-test","system":"Follow the user request.","messages":[{"role":"user","content":[{"type":"text","text":"写一首诗"}]},{"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"done"}]}],"tools":[{"name":"lookup","input_schema":{"type":"object"}}]}`)
	raw := claudetranslator.ConvertClaudeRequestToOpenAI("gpt-test", anthropic, false)
	run, err := BuildCursorRunPayload(raw, "gpt-test")
	if err != nil {
		t.Fatalf("BuildCursorRunPayload() error = %v", err)
	}

	var message cursorproto.AgentClientMessage
	if errDecode := proto.Unmarshal(run.Message, &message); errDecode != nil {
		t.Fatalf("decode run message: %v", errDecode)
	}
	runRequest := message.GetRunRequest()
	continuation := runRequest.GetAction().GetUserMessageAction()
	if continuation == nil {
		t.Fatal("tool continuation did not use UserMessageAction")
	}
	continuationText := continuation.GetUserMessage().GetText()
	for _, want := range []string{"写一首诗", "done", cursorToolContinuationText} {
		if !strings.Contains(continuationText, want) {
			t.Fatalf("continuation text does not contain %q: %q", want, continuationText)
		}
	}
	if got := run.SystemPrompt; got != "Follow the user request." {
		t.Fatalf("system prompt = %q, want %q", got, "Follow the user request.")
	}

	turnIDs := runRequest.GetConversationState().GetTurns()
	if len(turnIDs) != 1 {
		t.Fatalf("turns = %d, want 1", len(turnIDs))
	}
	turnBytes, ok := run.Blobs[hex.EncodeToString(turnIDs[0])]
	if !ok {
		t.Fatal("historical turn blob is missing")
	}
	var turn cursorproto.ConversationTurnStructure
	if errDecode := proto.Unmarshal(turnBytes, &turn); errDecode != nil {
		t.Fatalf("decode historical turn: %v", errDecode)
	}
	userBlobID := turn.GetAgentConversationTurn().GetUserMessage()
	userBytes, ok := run.Blobs[hex.EncodeToString(userBlobID)]
	if !ok {
		t.Fatal("historical user message blob is missing")
	}
	var userMessage cursorproto.UserMessage
	if errDecode := proto.Unmarshal(userBytes, &userMessage); errDecode != nil {
		t.Fatalf("decode historical user message: %v", errDecode)
	}
	if got := userMessage.GetText(); got != "写一首诗" {
		t.Fatalf("historical user text = %q, want %q", got, "写一首诗")
	}
}

func TestParseCursorConnectEndStatus(t *testing.T) {
	err := parseCursorConnectEnd([]byte(`{"error":{"code":"resource_exhausted","message":"context length exceeded"}}`))
	statusErr, ok := err.(*CursorStatusError)
	if !ok || statusErr.StatusCode() != 400 {
		t.Fatalf("error = %#v", err)
	}
	err = parseCursorConnectEnd([]byte(`{"error":{"code":"resource_exhausted","message":"quota exhausted"}}`))
	statusErr, ok = err.(*CursorStatusError)
	if !ok || statusErr.StatusCode() != 429 {
		t.Fatalf("error = %#v", err)
	}
}
