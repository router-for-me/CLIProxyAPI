package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func codexV1WebsocketJSON(t *testing.T, value any) []byte {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal websocket payload: %v", err)
	}
	return payload
}

func codexV1WebsocketAuth(baseURL, id string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{ID: id, Provider: "codex", Attributes: map[string]string{"api_key": "sk-test", "base_url": baseURL, "websockets": "true"}}
}

func codexV1WebsocketExecutor(baseURL string, optedIn, steering bool) *CodexWebsocketsExecutor {
	model := config.CodexModel{Name: "summary-model", Alias: "summary-model", UseV1Compaction: optedIn}
	cfg := codexV1CompactionConfig(baseURL, model)
	cfg.Codex.ResponseSteering = steering
	exec := NewCodexWebsocketsExecutor(cfg)
	exec.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
	return exec
}

func codexV1WebsocketRequest() cliproxyexecutor.Request {
	payload, _ := json.Marshal(map[string]any{
		"input": []any{
			map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "keep-original"}}},
			map[string]any{"type": "compaction_trigger", "id": "trigger-1", "opaque": map[string]any{"keep": true}},
		},
		"tools":       []any{map[string]any{"type": "function", "name": "keep-tool"}},
		"tool_choice": "auto",
	})
	return cliproxyexecutor.Request{Model: "summary-model", Payload: payload}
}

func codexV1WebsocketOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
}

func codexV1WebsocketWrite(t *testing.T, conn *websocket.Conn, value any) bool {
	t.Helper()
	if errWrite := conn.WriteMessage(websocket.TextMessage, codexV1WebsocketJSON(t, value)); errWrite != nil {
		t.Errorf("write websocket event: %v", errWrite)
		return false
	}
	return true
}

func TestCodexWebsocketsV1CompactionReplaysCapsuleOnSameConnection(t *testing.T) {
	var connections atomic.Int32
	firstRequest := make(chan []byte, 1)
	secondRequest := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read summary request: %v", errRead)
			return
		}
		firstRequest <- bytes.Clone(body)
		if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.output_item.done", "sequence_number": 3, "output_index": 0, "response_id": "resp-summary", "item": map[string]any{"type": "message", "id": "msg-summary", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Carry the task context forward."}}}}) {
			return
		}
		if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.completed", "sequence_number": 4, "response": map[string]any{"id": "resp-summary", "object": "response", "status": "completed", "model": "summary-model", "output": []any{}, "usage": map[string]any{"input_tokens": 17, "output_tokens": 9, "total_tokens": 26}}}) {
			return
		}
		_, body, errRead = conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read capsule replay: %v", errRead)
			return
		}
		secondRequest <- bytes.Clone(body)
		if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-replay", "status": "completed", "model": "summary-model", "output": []any{}, "usage": map[string]any{"input_tokens": 4, "output_tokens": 2, "total_tokens": 6}}}) {
			return
		}
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	exec := codexV1WebsocketExecutor(server.URL, true, false)
	auth := codexV1WebsocketAuth(server.URL, "ws-compaction")
	sessionID := "ws-v1-compaction-" + t.Name()
	opts := codexV1WebsocketOptions()
	opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: sessionID}
	t.Cleanup(func() { closeCodexWebsocketSession(exec.getOrCreateSession(sessionID), "test_complete") })
	request := codexV1WebsocketRequest()
	request.Payload = codexV1WebsocketJSON(t, map[string]any{"previous_response_id": "resp-prior", "input": []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "keep-original"}}},
		map[string]any{"type": "compaction_trigger", "id": "trigger-1", "opaque": map[string]any{"keep": true}},
	}})
	result, errExecute := exec.Execute(context.Background(), auth, request, opts)
	if errExecute != nil {
		t.Fatalf("summary Execute() error: %v", errExecute)
	}
	firstPayload := <-firstRequest
	if gjson.GetBytes(firstPayload, "type").String() != "response.create" || gjson.GetBytes(firstPayload, "previous_response_id").String() != "resp-prior" {
		t.Fatalf("summary create lost its transport state: %s", firstPayload)
	}
	if gjson.GetBytes(firstPayload, "input.2.type").String() != "compaction_trigger" || !gjson.GetBytes(firstPayload, "input.2.opaque.keep").Bool() {
		t.Fatalf("summary create did not retain its opaque trigger: %s", firstPayload)
	}
	if strings.Count(gjson.GetBytes(firstPayload, "input").Raw, "Summarize the conversation so far") != 1 {
		t.Fatalf("summary instruction count is not one: %s", firstPayload)
	}
	if got := gjson.GetBytes(result.Payload, "output.0.content.0.text").String(); got != "Carry the task context forward." {
		t.Fatalf("original assistant output changed: %s", result.Payload)
	}
	if got := gjson.GetBytes(result.Payload, "output.1.type").String(); got != "compaction" {
		t.Fatalf("compaction item missing from response: %s", result.Payload)
	}
	if got := gjson.GetBytes(result.Payload, "usage.total_tokens").Int(); got != 26 {
		t.Fatalf("terminal usage changed: %d", got)
	}
	capsule := json.RawMessage(gjson.GetBytes(result.Payload, "output.1").Raw)
	replay := cliproxyexecutor.Request{Model: "summary-model", Payload: codexV1WebsocketJSON(t, map[string]any{"input": []any{
		map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "continue"}}},
		capsule,
	}})}
	if _, errReplay := exec.Execute(context.Background(), auth, replay, opts); errReplay != nil {
		t.Fatalf("capsule replay Execute() error: %v", errReplay)
	}
	secondPayload := <-secondRequest
	if gjson.GetBytes(secondPayload, "input.1.role").String() != "developer" || !strings.Contains(gjson.GetBytes(secondPayload, "input.1.content.0.text").String(), "Carry the task context forward.") {
		t.Fatalf("capsule was not expanded to a developer summary: %s", secondPayload)
	}
	if strings.Contains(gjson.GetBytes(secondPayload, "input").Raw, "\"type\":\"compaction\"") || gjson.GetBytes(secondPayload, "previous_response_id").Exists() {
		t.Fatalf("expanded replay retained opaque capsule state: %s", secondPayload)
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("upstream websocket connections = %d, want one reused connection", got)
	}
}

func TestCodexWebsocketsV1CompactionStreamAppendsTerminalItemEvents(t *testing.T) {
	capturedRequest := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read summary request: %v", errRead)
			return
		}
		capturedRequest <- bytes.Clone(body)
		if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.output_item.done", "sequence_number": 3, "output_index": 0, "response_id": "resp-stream", "item": map[string]any{"type": "message", "id": "msg-stream", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "stream summary"}}}}) {
			return
		}
		codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.completed", "sequence_number": 4, "response": map[string]any{"id": "resp-stream", "status": "completed", "model": "summary-model", "output": []any{}, "usage": map[string]any{"input_tokens": 17, "output_tokens": 9, "total_tokens": 26}}})
	}))
	defer server.Close()
	exec := codexV1WebsocketExecutor(server.URL, true, false)
	auth := codexV1WebsocketAuth(server.URL, "ws-compaction-stream")
	request := codexV1WebsocketRequest()
	request.Payload = codexV1WebsocketJSON(t, map[string]any{
		"client_metadata": map[string]any{"ws_request_header_x_openai_internal_codex_responses_lite": true},
		"input":           []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "keep-original"}}}, map[string]any{"type": "compaction_trigger", "id": "trigger-1"}},
	})
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	stream, errStream := exec.ExecuteStream(ctx, auth, request, codexV1WebsocketOptions())
	if errStream != nil {
		t.Fatalf("ExecuteStream() error: %v", errStream)
	}
	var events [][]byte
	for chunk := range stream.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		events = append(events, bytes.Clone(chunk.Payload))
	}
	if len(events) != 4 {
		t.Fatalf("stream event count = %d, want original item, capsule added/done, and terminal: %s", len(events), events)
	}
	if gjson.GetBytes(events[0], "type").String() != "response.output_item.done" || gjson.GetBytes(events[1], "type").String() != "response.output_item.added" || gjson.GetBytes(events[2], "type").String() != "response.output_item.done" {
		t.Fatalf("stream item event order is wrong: %s", events)
	}
	if gjson.GetBytes(events[1], "output_index").Int() != 1 || gjson.GetBytes(events[2], "sequence_number").Int() != 5 {
		t.Fatalf("synthetic capsule event index or sequence is wrong: %s %s", events[1], events[2])
	}
	terminal := events[3]
	if gjson.GetBytes(terminal, "type").String() != "response.completed" || gjson.GetBytes(terminal, "sequence_number").Int() != 6 {
		t.Fatalf("terminal event was not shifted after appended item events: %s", terminal)
	}
	if gjson.GetBytes(terminal, "response.id").String() != "resp-stream" || gjson.GetBytes(terminal, "response.output.1.type").String() != "compaction" || gjson.GetBytes(terminal, "response.usage.total_tokens").Int() != 26 {
		t.Fatalf("converted terminal lost response data: %s", terminal)
	}
	firstPayload := <-capturedRequest
	if gjson.GetBytes(firstPayload, "input.2.type").String() != "compaction_trigger" {
		t.Fatalf("native request did not retain the trigger: %s", firstPayload)
	}
}

func TestCodexWebsocketsDuplexV1CompactionAppendsBeforeTerminal(t *testing.T) {
	capturedRequest := make(chan []byte, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, errUpgrade := upgrader.Upgrade(w, r, nil)
		if errUpgrade != nil {
			t.Errorf("upgrade websocket: %v", errUpgrade)
			return
		}
		defer func() { _ = conn.Close() }()
		_, body, errRead := conn.ReadMessage()
		if errRead != nil {
			t.Errorf("read initial summary request: %v", errRead)
			return
		}
		capturedRequest <- bytes.Clone(body)
		if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-duplex", "output": []any{}}}) {
			return
		}
		if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.output_item.done", "sequence_number": 3, "output_index": 0, "response_id": "resp-duplex", "item": map[string]any{"type": "message", "id": "msg-duplex", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "duplex summary"}}}}) {
			return
		}
		codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.completed", "sequence_number": 4, "response": map[string]any{"id": "resp-duplex", "status": "completed", "model": "summary-model", "output": []any{}, "usage": map[string]any{"input_tokens": 11, "output_tokens": 5, "total_tokens": 16}}})
		_, _, _ = conn.ReadMessage()
	}))
	defer server.Close()
	exec := codexV1WebsocketExecutor(server.URL, true, true)
	auth := codexV1WebsocketAuth(server.URL, "ws-compaction-duplex")
	input := make(chan cliproxyexecutor.WebsocketInput)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), input)
	opts := codexV1WebsocketOptions()
	opts.Metadata = map[string]any{cliproxyexecutor.ExecutionSessionMetadataKey: "ws-v1-compaction-duplex-" + t.Name()}
	stream, errStream := exec.ExecuteStream(ctx, auth, codexV1WebsocketRequest(), opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error: %v", errStream)
	}
	var events [][]byte
	for {
		select {
		case chunk, ok := <-stream.Chunks:
			if !ok {
				t.Fatal("duplex stream closed before converted terminal")
			}
			if chunk.Err != nil {
				t.Fatalf("duplex stream error: %v", chunk.Err)
			}
			events = append(events, bytes.Clone(chunk.Payload))
			if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
				cancel()
				goto collected
			}
		case <-ctx.Done():
			t.Fatalf("timed out collecting duplex events: %v", ctx.Err())
		}
	}
collected:
	if len(events) != 5 {
		t.Fatalf("duplex event count = %d, want created, original item, capsule added/done, and terminal: %s", len(events), events)
	}
	if gjson.GetBytes(events[2], "type").String() != "response.output_item.added" || gjson.GetBytes(events[3], "type").String() != "response.output_item.done" {
		t.Fatalf("duplex did not send synthetic capsule events before terminal: %s", events)
	}
	if gjson.GetBytes(events[4], "response.output.1.type").String() != "compaction" || gjson.GetBytes(events[4], "response.id").String() != "resp-duplex" {
		t.Fatalf("duplex terminal did not preserve and append response output: %s", events[4])
	}
	if gjson.GetBytes(<-capturedRequest, "input.2.type").String() != "compaction_trigger" {
		t.Fatal("duplex create did not retain the trigger")
	}
}

func TestCodexWebsocketsV1CompactionDisabledAndNativeOutput(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		optedIn      bool
		nativeOutput bool
	}{
		{name: "disabled", optedIn: false},
		{name: "native capsule", optedIn: true, nativeOutput: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			capturedRequest := make(chan []byte, 1)
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Errorf("upgrade websocket: %v", errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				_, body, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Errorf("read request: %v", errRead)
					return
				}
				capturedRequest <- bytes.Clone(body)
				output := []any{}
				if testCase.nativeOutput {
					output = []any{map[string]any{"type": "compaction", "encrypted_content": "native-opaque"}}
				}
				codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp-native", "status": "completed", "model": "summary-model", "output": output}})
			}))
			defer server.Close()
			exec := codexV1WebsocketExecutor(server.URL, testCase.optedIn, false)
			auth := codexV1WebsocketAuth(server.URL, "ws-compaction-"+testCase.name)
			result, errExecute := exec.Execute(context.Background(), auth, codexV1WebsocketRequest(), codexV1WebsocketOptions())
			if errExecute != nil {
				t.Fatalf("Execute() error: %v", errExecute)
			}
			body := <-capturedRequest
			if strings.Contains(gjson.GetBytes(body, "input").Raw, "Summarize the conversation so far") != testCase.optedIn {
				t.Fatalf("summary instruction opt-in mismatch: %s", body)
			}
			if got := gjson.GetBytes(result.Payload, "output.0.type").String(); got != map[bool]string{true: "compaction", false: ""}[testCase.nativeOutput] {
				t.Fatalf("response output type = %q: %s", got, result.Payload)
			}
		})
	}
}

func TestCodexWebsocketsV1CompactionRejectsCorruptCapsuleBeforeDial(t *testing.T) {
	var connections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		connections.Add(1)
		http.Error(w, "unexpected request", http.StatusBadRequest)
	}))
	defer server.Close()
	exec := codexV1WebsocketExecutor(server.URL, true, false)
	auth := codexV1WebsocketAuth(server.URL, "ws-compaction-invalid-capsule")
	request := cliproxyexecutor.Request{Model: "summary-model", Payload: codexV1WebsocketJSON(t, map[string]any{"input": []any{
		map[string]any{"type": "compaction", "encrypted_content": "cpa-responses-v1-compaction-v1.not-valid"},
	}})}
	if _, errExecute := exec.Execute(context.Background(), auth, request, codexV1WebsocketOptions()); errExecute == nil {
		t.Fatal("Execute() accepted a corrupt capsule")
	}
	if got := connections.Load(); got != 0 {
		t.Fatalf("upstream requests = %d, want zero before capsule validation", got)
	}
}

func TestCodexWebsocketsV1CompactionRejectsMalformedTerminalBeforeReconstruction(t *testing.T) {
	for _, tc := range []struct{ mode, output string }{
		{"execute", `[,]`},
		{"buffered", `[,]`},
		{"live", `[,]`},
		{"duplex", `[,]`},
		{"execute", `{"bad":"field"}`},
		{"buffered", `{"bad":"field"}`},
		{"live", `{"bad":"field"}`},
		{"duplex", `{"bad":"field"}`},
	} {
		mode := tc.mode
		t.Run(mode+"/"+tc.output, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Error(errRead)
					return
				}
				if mode == "duplex" {
					codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-invalid", "output": []any{}}})
				}
				if mode == "live" {
					codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.output_text.delta", "delta": "summary"})
				}
				codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.output_item.done", "output_index": 0, "item": json.RawMessage(gjson.GetBytes(codexV1CompactionSummaryResponse(), "output.0").Raw)})
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.completed","response":{"id":"resp-invalid","status":"completed","output":`+tc.output+`}}`)); errWrite != nil {
					t.Error(errWrite)
				}
			}))
			defer server.Close()
			exec := codexV1WebsocketExecutor(server.URL, true, mode == "duplex")
			auth := codexV1WebsocketAuth(server.URL, "ws-invalid-"+mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
			if mode == "duplex" {
				ctx = cliproxyexecutor.WithWebsocketInput(ctx, make(chan cliproxyexecutor.WebsocketInput))
			}
			var observedError error
			if mode == "execute" {
				_, observedError = exec.Execute(ctx, auth, codexV1WebsocketRequest(), codexV1WebsocketOptions())
			} else {
				stream, errStream := exec.ExecuteStream(ctx, auth, codexV1WebsocketRequest(), codexV1WebsocketOptions())
				observedError = errStream
				if stream != nil {
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							observedError = chunk.Err
						}
						if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" || gjson.GetBytes(chunk.Payload, "item.type").String() == "compaction" {
							t.Fatalf("malformed terminal produced compaction output: %s", chunk.Payload)
						}
					}
				}
			}
			if scoped, ok := observedError.(interface{ IsRequestScoped() bool }); !ok || !scoped.IsRequestScoped() {
				t.Fatalf("malformed terminal did not return a request-scoped error: %v", observedError)
			}
		})
	}
}

func TestCodexWebsocketsDuplexV1CompactionRejectsDuplicateAndAllowsNextResponse(t *testing.T) {
	for _, duplicate := range []bool{true, false} {
		t.Run(fmt.Sprintf("duplicate=%t", duplicate), func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Error(errRead)
					return
				}
				for index := 0; index < 2; index++ {
					responseID := "resp-summary-1"
					if index == 1 && !duplicate {
						if _, _, errRead := conn.ReadMessage(); errRead != nil {
							t.Error(errRead)
							return
						}
						responseID = "resp-summary-2"
					}
					if index == 0 || !duplicate {
						if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": responseID}}) {
							return
						}
					}
					if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.completed", "sequence_number": 4, "response": map[string]any{"id": responseID, "status": "completed", "output": json.RawMessage(gjson.GetBytes(codexV1CompactionSummaryResponse(), "output").Raw)}}) {
						return
					}
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			exec := codexV1WebsocketExecutor(server.URL, true, true)
			auth := codexV1WebsocketAuth(server.URL, "ws-duplicate-"+t.Name())
			input := make(chan cliproxyexecutor.WebsocketInput, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx = cliproxyexecutor.WithWebsocketInput(cliproxyexecutor.WithDownstreamWebsocket(ctx), input)
			stream, err := exec.ExecuteStream(ctx, auth, codexV1WebsocketRequest(), codexV1WebsocketOptions())
			if err != nil {
				t.Fatal(err)
			}
			var observedError error
			completions, capsules := 0, 0
			for chunk := range stream.Chunks {
				if chunk.Err != nil {
					observedError = chunk.Err
				}
				if gjson.GetBytes(chunk.Payload, "type").String() == "response.output_item.done" && gjson.GetBytes(chunk.Payload, "item.type").String() == "compaction" {
					capsules++
				}
				if gjson.GetBytes(chunk.Payload, "type").String() == "response.completed" {
					completions++
					if !duplicate && completions == 1 {
						input <- cliproxyexecutor.WebsocketInput{Payload: []byte(`{"type":"response.create","model":"summary-model","input":[{"type":"compaction_trigger"}]}`)}
					}
					if completions == 2 {
						cancel()
					}
				}
			}
			if duplicate {
				if scoped, ok := observedError.(interface{ IsRequestScoped() bool }); !ok || !scoped.IsRequestScoped() || completions != 1 || capsules != 1 {
					t.Fatalf("duplicate produced %d completions and %d capsules, error %v", completions, capsules, observedError)
				}
			} else if observedError != nil || completions != 2 || capsules != 2 {
				t.Fatalf("next response produced %d completions and %d capsules, error %v", completions, capsules, observedError)
			}
		})
	}
}

func TestCodexWebsocketsV1CompactionConversionFailurePreservesUsage(t *testing.T) {
	for _, mode := range []string{"execute", "buffered", "live", "duplex"} {
		t.Run(mode, func(t *testing.T) {
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() { _ = conn.Close() }()
				if _, _, errRead := conn.ReadMessage(); errRead != nil {
					t.Error(errRead)
					return
				}
				if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.created", "response": map[string]any{"id": "resp-refusal", "model": "upstream-model"}}) {
					return
				}
				if mode == "live" && !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.output_text.delta", "delta": "Cannot summarize."}) {
					return
				}
				if !codexV1WebsocketWrite(t, conn, map[string]any{"type": "response.completed", "response": json.RawMessage(`{"id":"resp-refusal","model":"upstream-model","status":"completed","output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"refusal","refusal":"Cannot summarize."}]}],"usage":{"input_tokens":17,"output_tokens":9,"total_tokens":26}}`)}) {
					return
				}
				_, _, _ = conn.ReadMessage()
			}))
			defer server.Close()
			capture := &multiProviderUsageCapture{alias: t.Name(), records: make(chan coreusage.Record, 4)}
			coreusage.RegisterNamedPlugin(t.Name(), capture)
			t.Cleanup(func() { coreusage.RegisterNamedPlugin(t.Name(), multiProviderNoopUsagePlugin{}) })
			ctx, cancel := context.WithTimeout(coreusage.WithRequestedModelAlias(context.Background(), t.Name()), 5*time.Second)
			defer cancel()
			ctx = cliproxyexecutor.WithDownstreamWebsocket(ctx)
			if mode == "duplex" {
				ctx = cliproxyexecutor.WithWebsocketInput(ctx, make(chan cliproxyexecutor.WebsocketInput))
			}
			exec := codexV1WebsocketExecutor(server.URL, true, mode == "duplex")
			auth := codexV1WebsocketAuth(server.URL, "ws-refusal-"+mode)
			var observedError error
			if mode == "execute" {
				_, observedError = exec.Execute(ctx, auth, codexV1WebsocketRequest(), codexV1WebsocketOptions())
			} else {
				stream, errStream := exec.ExecuteStream(ctx, auth, codexV1WebsocketRequest(), codexV1WebsocketOptions())
				observedError = errStream
				if stream != nil {
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							observedError = chunk.Err
						}
					}
				}
			}
			if scoped, ok := observedError.(interface{ IsRequestScoped() bool }); !ok || !scoped.IsRequestScoped() {
				t.Fatalf("conversion failure = %v, want request-scoped error", observedError)
			}
			record := capture.await(t)
			if !record.Failed || record.Detail.InputTokens != 17 || record.Detail.OutputTokens != 9 || record.Detail.TotalTokens != 26 || record.ResponseModel != "upstream-model" {
				t.Fatalf("failed conversion usage = %+v, want failed upstream-model with 17/9/26 tokens", record)
			}
		})
	}
}
