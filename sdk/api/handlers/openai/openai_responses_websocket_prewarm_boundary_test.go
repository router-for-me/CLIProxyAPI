package openai

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type websocketPrewarmPrefixRetryExecutor struct {
	websocketProviderCaptureExecutor
	failFirst bool
}

func (e *websocketPrewarmPrefixRetryExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if e.failFirst && e.streamCalls == 0 {
		e.streamCalls++
		e.payloads = append(e.payloads, bytes.Clone(req.Payload))
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Err: websocketPinnedFailoverStatusError{status: http.StatusBadRequest, msg: "retry diagnostic"}}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	return e.websocketCaptureExecutor.ExecuteStream(ctx, auth, req, opts)
}

func TestResponsesWebsocketPrewarmPrefixRepairCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	previousOutput := log.StandardLogger().Out
	log.SetOutput(io.Discard)
	t.Cleanup(func() { log.SetOutput(previousOutput) })
	for _, tc := range []struct {
		name, input                                                          string
		parent, failFirst, wrongParent, invalidFirst, invalidType, omitModel bool
		wantPrefix                                                           bool
	}{
		{name: "compacted_delta", input: `[{"type":"compaction","encrypted_content":"opaque-checkpoint"},{"type":"message","role":"user","content":"current input"}]`, parent: true, wantPrefix: true},
		{name: "ordinary_delta", input: `[{"type":"message","role":"user","content":"current input"}]`, parent: true, wantPrefix: true},
		{name: "append_delta", input: `[{"type":"compaction","encrypted_content":"opaque-checkpoint"},{"type":"message","role":"user","content":"current input"}]`, parent: true, wantPrefix: true},
		{name: "compaction_summary_delta", input: `[{"type":"compaction_summary","content":"synthetic summary"},{"type":"message","role":"user","content":"current input"}]`, parent: true, wantPrefix: true},
		{name: "failed_attempt_reconnect", input: `[{"type":"compaction","encrypted_content":"opaque-checkpoint"},{"type":"message","role":"user","content":"current input"}]`, parent: true, failFirst: true, wantPrefix: true},
		{name: "invalid_delta_retry", input: `[{"type":"compaction","encrypted_content":"opaque-checkpoint"},{"type":"message","role":"user","content":"current input"}]`, parent: true, invalidFirst: true, wantPrefix: true},
		{name: "invalid_type_retry", input: `[{"type":"message","role":"user","content":"current input"}]`, parent: true, invalidType: true, wantPrefix: true},
		{name: "replacement_empty_input", input: `[]`},
		{name: "replacement_inherits_defaults", input: `[{"type":"additional_tools","role":"developer","tools":[]}]`, omitModel: true},
		{name: "invalid_replacement_retry", input: `[{"type":"additional_tools","role":"developer","tools":[]}]`, invalidFirst: true},
		{name: "replacement_empty_tools", input: `[{"type":"additional_tools","role":"developer","tools":[]},{"type":"message","role":"user","content":"replacement"}]`},
		{name: "replacement_new_tools", input: `[{"type":"additional_tools","role":"developer","tools":[{"type":"function","name":"replacement_tool"}]},{"type":"message","role":"user","content":"replacement"}]`},
		{name: "unrelated_parent", input: `[{"type":"message","role":"user","content":"not this warmup"}]`, parent: true, wrongParent: true, wantPrefix: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			executor := &websocketPrewarmPrefixRetryExecutor{websocketProviderCaptureExecutor: websocketProviderCaptureExecutor{provider: "codex"}, failFirst: tc.failFirst}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			auth := &coreauth.Auth{ID: "prewarm-prefix-" + tc.name, Provider: "codex", Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "false"}}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "prewarm-prefix-model"}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			h := NewOpenAIResponsesAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			router := gin.New()
			router.GET("/v1/responses/ws", h.ResponsesWebsocket)
			server := httptest.NewServer(router)
			defer server.Close()
			conn, _, errDial := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", http.Header{"Session_id": []string{auth.ID}})
			if errDial != nil {
				t.Fatal(errDial)
			}
			defer func() {
				_ = conn.Close()
			}()
			send := func(raw string) {
				t.Helper()
				if errSend := conn.WriteMessage(websocket.TextMessage, []byte(raw)); errSend != nil {
					t.Fatal(errSend)
				}
			}
			read := func() []byte {
				t.Helper()
				_, b, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatal(errRead)
				}
				return b
			}
			warmup := `{"type":"response.create","model":"prewarm-prefix-model","instructions":"legacy base","generate":false,"input":[{"type":"additional_tools","id":"warm-tools","role":"developer","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec"}]}]},{"type":"message","id":"warm-base","role":"developer","content":"base instructions"}]}`
			send(warmup)
			created := read()
			parent := gjson.GetBytes(created, "response.id").String()
			if !strings.HasPrefix(parent, "resp_prewarm_") {
				t.Fatalf("expected synthetic warmup: %s", created)
			}
			if got := gjson.GetBytes(read(), "type").String(); got != wsEventTypeCompleted {
				t.Fatalf("warmup event=%s", got)
			}
			if executor.streamCalls != 0 {
				t.Fatal("warmup reached upstream")
			}
			parentField := ""
			if tc.parent {
				if tc.wrongParent {
					parent = "resp_prewarm_unrelated"
				}
				parentField = fmt.Sprintf(`,"previous_response_id":%q`, parent)
			}
			followup := fmt.Sprintf(`{"type":"response.create","model":"prewarm-prefix-model"%s,"input":%s,"client_metadata":{"source":"automation_heartbeat","keep":"unchanged"}}`, parentField, tc.input)
			if tc.name == "append_delta" {
				followup = strings.Replace(followup, "response.create", "response.append", 1)
			}
			if tc.omitModel {
				followup = strings.Replace(followup, `,"model":"prewarm-prefix-model"`, "", 1)
			}
			if tc.invalidType {
				send(fmt.Sprintf(`{"type":"unsupported","previous_response_id":%q,"input":[]}`, parent))
				if gjson.GetBytes(read(), "type").String() != "error" || executor.streamCalls != 0 {
					t.Fatal("invalid request type reached upstream")
				}
			}
			if tc.invalidFirst {
				if tc.parent {
					send(fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":{}}`, parent))
				} else {
					send(`{"type":"response.create","input":{}}`)
				}
				if gjson.GetBytes(read(), "type").String() != "error" || executor.streamCalls != 0 {
					t.Fatal("invalid delta did not fail before upstream")
				}
			}
			send(followup)
			result := read()
			if tc.wrongParent {
				if gjson.GetBytes(result, "type").String() != "error" || executor.streamCalls != 0 {
					t.Fatal("unrelated parent inherited warmup")
				}
				send(strings.ReplaceAll(followup, parent, gjson.GetBytes(created, "response.id").String()))
				result = read()
			}
			if tc.failFirst {
				if gjson.GetBytes(result, "type").String() != "error" {
					t.Fatalf("expected first failure: %s", result)
				}
				// Terminal upstream errors close the connection. Codex reconnects
				// and establishes a new warm-up before retrying the full request.
				_ = conn.Close()
				var errReconnect error
				conn, _, errReconnect = websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses/ws", http.Header{"Session_id": []string{auth.ID}})
				if errReconnect != nil {
					t.Fatal(errReconnect)
				}
				send(warmup)
				newParent := gjson.GetBytes(read(), "response.id").String()
				if gjson.GetBytes(read(), "type").String() != wsEventTypeCompleted {
					t.Fatal("retry warmup failed")
				}
				send(strings.ReplaceAll(followup, parent, newParent))
				result = read()
			}
			if gjson.GetBytes(result, "type").String() != wsEventTypeCompleted {
				t.Fatalf("followup failed: %s", result)
			}
			wantCalls := 1
			if tc.failFirst {
				wantCalls = 2
			}
			if executor.streamCalls != wantCalls || len(executor.payloads) != wantCalls {
				t.Fatalf("unexpected executor counts: calls=%d payloads=%d want=%d", executor.streamCalls, len(executor.payloads), wantCalls)
			}
			for _, forwarded := range executor.payloads {
				if gjson.GetBytes(forwarded, "model").String() != "prewarm-prefix-model" || gjson.GetBytes(forwarded, "instructions").String() != "legacy base" {
					t.Fatalf("request defaults lost: %s", forwarded)
				}
				input := gjson.GetBytes(forwarded, "input").Array()
				want := gjson.Parse(tc.input).Array()
				if tc.wantPrefix {
					prefix := gjson.Get(warmup, "input").Array()
					if len(input) != len(want)+2 || input[0].Raw != prefix[0].Raw || input[1].Raw != prefix[1].Raw {
						t.Fatal("acknowledged tools/base prefix lost, changed or duplicated")
					}
					input = input[2:]
				} else if len(input) != len(want) {
					t.Fatalf("stale prefix inherited by replacement: %s", forwarded)
				}
				for i := range want {
					if input[i].Raw != want[i].Raw {
						t.Fatalf("delta changed: got %s want %s", input[i].Raw, want[i].Raw)
					}
				}
				if gjson.GetBytes(forwarded, "previous_response_id").Exists() || gjson.GetBytes(forwarded, "generate").Exists() {
					t.Fatalf("synthetic state leaked upstream: %s", forwarded)
				}
				if gjson.GetBytes(forwarded, "client_metadata.keep").String() != "unchanged" {
					t.Fatal("metadata changed")
				}
			}
			if tc.name == "compacted_delta" {
				send(`{"type":"response.create","model":"prewarm-prefix-model","previous_response_id":"resp-upstream","input":[{"type":"message","role":"user","content":"next input"}]}`)
				if gjson.GetBytes(read(), "type").String() != wsEventTypeCompleted {
					t.Fatal("real-response continuation failed")
				}
				last := executor.payloads[len(executor.payloads)-1]
				if len(gjson.GetBytes(last, `input.#(type=="additional_tools")#`).Array()) != 1 || gjson.GetBytes(last, "input").Array()[len(gjson.GetBytes(last, "input").Array())-1].Get("content").String() != "next input" {
					t.Fatal("continuation lost or duplicated state")
				}
			}
		})
	}
}

func TestResponsesWebsocketPrewarmPrefixRejectsInvalidInitialInput(t *testing.T) {
	for _, input := range []string{`null`, `{}`, `"not an array"`, `1`} {
		t.Run(input, func(t *testing.T) {
			request := []byte(fmt.Sprintf(`{"type":"response.create","model":"fixture-model","generate":false,"input":%s}`, input))
			normalized, state, errMsg := normalizeResponseCreateRequest(request)
			if errMsg == nil || errMsg.StatusCode != http.StatusBadRequest || normalized != nil || state != nil {
				t.Fatal("invalid prewarm input was accepted or changed connection state")
			}
		})
	}
}
