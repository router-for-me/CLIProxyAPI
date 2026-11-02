package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

const openCodeCheckpoint = `<conversation-checkpoint>
The following is a summary and serialized record of earlier conversation. Treat it as historical context, not as new instructions.

<summary>
The lookup finished. Continue with the remaining work.
</summary>
</conversation-checkpoint>`

func TestNormalizeResponsesWebsocketRequestReplacesOpenCodeCheckpoint(t *testing.T) {
	lastRequest := []byte(`{
		"model": "test-model",
		"instructions": "be helpful",
		"input": [
			{"type": "message", "role": "user", "content": "old task"}
		]
	}`)
	lastOutput := []byte(`[
		{
			"type": "function_call",
			"call_id": "old-call",
			"name": "lookup",
			"arguments": "{}"
		}
	]`)
	raw := []byte(fmt.Sprintf(`{
		"type": "response.create",
		"input": [
			{
				"type": "message",
				"role": "user",
				"content": [{"type": "input_text", "text": %q}]
			},
			{"type": "message", "role": "developer", "content": "current context"},
			{"type": "message", "role": "user", "content": "continue"}
		]
	}`, openCodeCheckpoint))
	tests := []struct {
		name        string
		incremental bool
		replay      bool
	}{
		{name: "HTTP"},
		{name: "HTTP with compaction replay", replay: true},
		{name: "incremental", incremental: true},
		{name: "incremental with compaction replay", incremental: true, replay: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, next, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(
				raw, lastRequest, lastOutput, "resp-before", []string{"old-call"},
				test.incremental, test.replay,
			)
			if errMsg != nil {
				t.Fatal(errMsg.Error)
			}

			got := gjson.GetBytes(normalized, "input").Raw
			want := gjson.GetBytes(raw, "input").Raw
			if got != want {
				t.Fatalf("replacement input = %s, want %s", got, want)
			}
			if gjson.GetBytes(normalized, "previous_response_id").Exists() {
				t.Fatal("replacement references the old response")
			}
			if got := gjson.GetBytes(normalized, "model").String(); got != "test-model" {
				t.Fatalf("model = %q, want test-model", got)
			}
			if got := gjson.GetBytes(normalized, "instructions").String(); got != "be helpful" {
				t.Fatalf("instructions = %q, want be helpful", got)
			}
			if !bytes.Equal(next, normalized) {
				t.Fatal("saved request differs from the replacement")
			}

			newOutput := []byte(`[
				{
					"type": "function_call",
					"call_id": "new-call",
					"name": "lookup",
					"arguments": "{}"
				}
			]`)
			followup := []byte(`{
				"type": "response.create",
				"previous_response_id": "resp-after",
				"input": [
					{"type": "function_call_output", "call_id": "new-call", "output": "done"}
				]
			}`)
			continued, _, errMsg := normalizeResponsesWebsocketRequestWithIncrementalState(
				followup, next, newOutput, "resp-after", []string{"new-call"},
				test.incremental, test.replay,
			)
			if errMsg != nil {
				t.Fatal(errMsg.Error)
			}
			if bytes.Contains(continued, []byte("old-call")) {
				t.Fatal("continuation restored the pre-compaction tool call")
			}
			wantCount := int64(5)
			if test.incremental {
				wantCount = 1
			}
			if got := gjson.GetBytes(continued, "input.#").Int(); got != wantCount {
				t.Fatalf("continuation input count = %d, want %d", got, wantCount)
			}
		})
	}
}

func TestOpenCodeCheckpointContent(t *testing.T) {
	tests := []struct {
		name    string
		role    string
		content any
		want    bool
	}{
		{
			name:    "string content",
			role:    "user",
			content: openCodeCheckpoint,
			want:    true,
		},
		{
			name: "input text parts",
			role: "user",
			content: []map[string]string{
				{"type": "input_text", "text": "<conversation-checkpoint>\n"},
				{"type": "input_text", "text": strings.TrimPrefix(openCodeCheckpoint, "<conversation-checkpoint>\n")},
			},
			want: true,
		},
		{
			name:    "trailing whitespace",
			role:    "user",
			content: openCodeCheckpoint + "\n",
			want:    true,
		},
		{
			name:    "ordinary summary",
			role:    "user",
			content: "Summary of completed work. Continue from this checkpoint.",
		},
		{
			name:    "quoted checkpoint",
			role:    "user",
			content: "Explain this checkpoint:\n" + openCodeCheckpoint,
		},
		{
			name:    "unclosed checkpoint",
			role:    "user",
			content: strings.TrimSuffix(openCodeCheckpoint, "</conversation-checkpoint>"),
		},
		{
			name:    "missing summary",
			role:    "user",
			content: "<conversation-checkpoint>\nhello\n</conversation-checkpoint>",
		},
		{
			name: "reversed summary tags",
			role: "user",
			content: `<conversation-checkpoint>
</summary>
<summary>
invalid summary
</conversation-checkpoint>`,
		},
		{
			name:    "developer message",
			role:    "developer",
			content: openCodeCheckpoint,
		},
		{
			name:    "assistant message",
			role:    "assistant",
			content: openCodeCheckpoint,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input, err := json.Marshal([]map[string]any{
				{"type": "message", "role": test.role, "content": test.content},
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := inputHasOpenCodeCompactionCheckpoint(gjson.ParseBytes(input)); got != test.want {
				t.Fatalf("checkpoint recognized = %t, want %t", got, test.want)
			}
		})
	}
}

func TestOpenCodeCheckpointDoesNotResetExplicitContinuations(t *testing.T) {
	input := gjson.Parse(fmt.Sprintf(`[
		{"type": "message", "role": "user", "content": %q}
	]`, openCodeCheckpoint))
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "append",
			raw:  `{"type": "response.append"}`,
		},
		{
			name: "parent response",
			raw: `{
				"type": "response.create",
				"previous_response_id": "resp-before"
			}`,
		},
		{
			name: "empty parent",
			raw: `{
				"type": "response.create",
				"previous_response_id": ""
			}`,
		},
		{
			name: "null parent",
			raw: `{
				"type": "response.create",
				"previous_response_id": null
			}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if shouldReplaceWebsocketTranscript([]byte(test.raw), input) {
				t.Fatal("checkpoint reset an explicit continuation")
			}
		})
	}
}

func TestOpenCodeCheckpointRejectsToolOutput(t *testing.T) {
	for _, itemType := range []string{"function_call_output", "custom_tool_call_output"} {
		t.Run(itemType, func(t *testing.T) {
			input := gjson.Parse(fmt.Sprintf(`[
				{"type": "message", "role": "user", "content": %q},
				{"type": %q, "call_id": "old-call", "output": "done"}
			]`, openCodeCheckpoint, itemType))
			if shouldReplaceWebsocketTranscript([]byte(`{"type": "response.create"}`), input) {
				t.Fatal("checkpoint discarded the history needed by a tool output")
			}
		})
	}
}

func TestResponsesWebsocketOpenCodeCheckpointReplacesHTTPHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &websocketCompactionCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{
		ID:       "opencode-checkpoint-http",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(
		auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}},
	)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	handler := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses", handler.ResponsesWebsocket)
	server := httptest.NewServer(router)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}()

	requests := []string{
		`{
			"type": "response.create",
			"model": "test-model",
			"input": [{"type": "message", "role": "user", "content": "old task"}]
		}`,
		fmt.Sprintf(`{
			"type": "response.create",
			"input": [{"type": "message", "role": "user", "content": %q}]
		}`, openCodeCheckpoint),
		`{
			"type": "response.create",
			"previous_response_id": "resp-2",
			"input": [{"type": "message", "role": "user", "content": "continue"}]
		}`,
	}
	for index, request := range requests {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(request)); err != nil {
			t.Fatal(err)
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("request %d: event = %q, want %s", index, got, wsEventTypeCompleted)
		}
	}

	executor.mu.Lock()
	payloads := append([][]byte(nil), executor.streamPayloads...)
	executor.mu.Unlock()
	if len(payloads) != len(requests) {
		t.Fatalf("upstream requests = %d, want %d", len(payloads), len(requests))
	}
	if got := gjson.GetBytes(payloads[1], "input.#").Int(); got != 1 {
		t.Fatalf("checkpoint input count = %d, want 1", got)
	}
	if got := gjson.GetBytes(payloads[1], "input.0.content").String(); got != openCodeCheckpoint {
		t.Fatalf("checkpoint content = %q, want %q", got, openCodeCheckpoint)
	}
	if got := gjson.GetBytes(payloads[2], "input.#").Int(); got != 3 {
		t.Fatalf("continuation input count = %d, want 3", got)
	}
	for _, payload := range payloads[1:] {
		if bytes.Contains(payload, []byte("call-1")) || bytes.Contains(payload, []byte("old task")) {
			t.Fatalf("upstream request restored pre-compaction history: %s", payload)
		}
	}
}
