package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"google.golang.org/protobuf/encoding/protowire"
)

const applyPatchTestInput = "*** Begin Patch\n*** Add File: a.txt\n+hello\n*** End Patch\n"
const applyPatchTestPartial = `{"input":"*** Begin Patch\n*** Add File: a.txt\n+hello\n`
const applyPatchTestRemainder = `*** End Patch\n"}`

func applyPatchTestRequest() []byte {
	return []byte(`{"model":"test","input":[{"role":"user","content":"edit a.txt"}],"tools":[{"type":"custom","name":"apply_patch","format":{"type":"grammar","syntax":"lark","definition":"start: patch"}}]}`)
}

func applyPatchTestPayloads(chunk []byte) [][]byte {
	var payloads [][]byte
	for _, line := range bytes.Split(chunk, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[len("data:"):])
			if gjson.ValidBytes(payload) {
				payloads = append(payloads, bytes.Clone(payload))
			}
		}
	}
	if len(payloads) == 0 && gjson.ValidBytes(chunk) {
		payloads = append(payloads, bytes.Clone(chunk))
	}
	return payloads
}

// applyPatchTestFrames separates real parameter fragments from terminal snapshots.
func applyPatchTestFrames(protocol, name string) (string, string) {
	args := applyPatchTestPartial + applyPatchTestRemainder
	switch protocol {
	case "claude":
		first := fmt.Sprintf("data: {\"type\":\"message_start\",\"message\":{\"id\":\"r1\",\"role\":\"assistant\",\"model\":\"claude-sonnet-4-6\"}}\n\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"c1\",\"name\":%q,\"input\":{}}}\n\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\n", name, applyPatchTestPartial)
		last := fmt.Sprintf("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":%q}}\n\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"}}\n\ndata: {\"type\":\"message_stop\"}\n\n", applyPatchTestRemainder)
		return first, last
	case "interactions":
		first := fmt.Sprintf("data: {\"event_type\":\"interaction.created\",\"interaction\":{\"id\":\"r1\"}}\n\ndata: {\"event_type\":\"step.start\",\"index\":0,\"step\":{\"type\":\"function_call\",\"id\":\"c1\",\"call_id\":\"c1\",\"name\":%q}}\n\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":%q}}\n\n", name, applyPatchTestPartial)
		last := fmt.Sprintf("data: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"arguments_delta\",\"arguments\":%q}}\n\ndata: {\"event_type\":\"step.stop\",\"index\":0}\n\ndata: {\"event_type\":\"interaction.completed\",\"interaction\":{\"id\":\"r1\"}}\n\ndata: [DONE]\n\n", applyPatchTestRemainder)
		return first, last
	case "responses":
		item := fmt.Sprintf(`{"type":"function_call","id":"a1","call_id":"c1","name":%q,"arguments":%q}`, name, args)
		first := fmt.Sprintf("data: {\"type\":\"response.created\",\"response\":{\"id\":\"r1\"}}\n\ndata: {\"type\":\"response.output_item.added\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"a1\",\"call_id\":\"c1\",\"name\":%q,\"arguments\":\"\"}}\n\ndata: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"a1\",\"delta\":%q}\n\n", name, applyPatchTestPartial)
		last := fmt.Sprintf("data: {\"type\":\"response.function_call_arguments.delta\",\"output_index\":0,\"item_id\":\"a1\",\"delta\":%q}\n\ndata: {\"type\":\"response.function_call_arguments.done\",\"output_index\":0,\"item_id\":\"a1\",\"arguments\":%q}\n\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":%s}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[%s]}}\n\n", applyPatchTestRemainder, args, item, item)
		return first, last
	case "devin":
		return string(task6DevinFrames(applyPatchTestPartial, false, "", name)), string(task6DevinFrames(applyPatchTestRemainder, false, `{}`, name))
	case "gemini", "antigravity":
		first := `{"responseId":"r1","candidates":[{"content":{"parts":[{"text":"preparing"}]}}]}`
		last := fmt.Sprintf(`{"responseId":"r1","candidates":[{"content":{"parts":[{"functionCall":{"name":%q,"args":%s}}]},"finishReason":"STOP"}]}`, name, args)
		if protocol == "antigravity" {
			first, last = `{"response":`+first+`}`, `{"response":`+last+`}`
		}
		return "data: " + first + "\n\n", "data: " + last + "\n\n"
	default:
		first := fmt.Sprintf("data: {\"id\":\"r1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":%q,\"arguments\":%q}}]}}]}\n\n", name, applyPatchTestPartial)
		last := fmt.Sprintf("data: {\"id\":\"r1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":%q}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n", applyPatchTestRemainder)
		return first, last
	}
}

func applyPatchTestDeclaration(t *testing.T, protocol string, body []byte) {
	t.Helper()
	var tool gjson.Result
	parameters := "parameters"
	switch protocol {
	case "chat":
		if gjson.GetBytes(body, "tools.0.type").String() != "function" {
			t.Fatalf("not a standard function: %s", body)
		}
		tool = gjson.GetBytes(body, "tools.0.function")
	case "gemini":
		tool = gjson.GetBytes(body, "tools.0.functionDeclarations.0")
		parameters = "parametersJsonSchema"
	case "antigravity":
		tool = gjson.GetBytes(body, "request.tools.0.functionDeclarations.0")
		parameters = "parameters"
	case "devin":
		if len(body) < 5 {
			t.Fatal("missing Connect envelope")
		}
		wire := applyPatchTestWireField(body[5:], 10)
		tool = gjson.Parse(fmt.Sprintf(`{"name":%q,"description":%q,"parameters":%s}`, applyPatchTestWireField(wire, 1), applyPatchTestWireField(wire, 2), applyPatchTestWireField(wire, 3)))
	default:
		tool = gjson.GetBytes(body, "tools.0")
		if protocol == "claude" {
			parameters = "input_schema"
		}
	}
	description := tool.Get("description").String()
	for _, marker := range []string{"*** Begin Patch", "*** End Patch", "*** Add File:", "*** Update File:", "*** Delete File:", "@@", "start: patch"} {
		if !strings.Contains(description, marker) {
			t.Fatalf("missing patch format marker %q: %s", marker, tool.Raw)
		}
	}
	if tool.Get(parameters+".properties.input.type").String() != "string" || tool.Get(parameters+".required.0").String() != "input" {
		t.Fatalf("wrong standard input schema: %s", tool.Raw)
	}
	// Antigravity's existing schema sanitizer replaces additionalProperties with a description.
	if protocol == "antigravity" {
		if !strings.Contains(tool.Get(parameters+".description").String(), "No extra properties allowed") {
			t.Fatalf("sanitized schema lost the strict contract: %s", tool.Raw)
		}
	} else if !tool.Get(parameters+".additionalProperties").Exists() || tool.Get(parameters+".additionalProperties").Bool() {
		t.Fatalf("schema permits extra properties: %s", tool.Raw)
	}
}

func applyPatchTestWireField(data []byte, target protowire.Number) []byte {
	for len(data) > 0 {
		number, kind, n := protowire.ConsumeTag(data)
		if n < 0 {
			return nil
		}
		data = data[n:]
		if kind == protowire.BytesType {
			value, m := protowire.ConsumeBytes(data)
			if m < 0 {
				return nil
			}
			if number == target {
				return value
			}
			data = data[m:]
		} else {
			m := protowire.ConsumeFieldValue(number, kind, data)
			if m < 0 {
				return nil
			}
			data = data[m:]
		}
	}
	return nil
}

// Use a real downstream socket, not ResponseRecorder, to prove flush reaches a client.
func newApplyPatchTestGateway(t *testing.T, exec cliproxyauth.ProviderExecutor, auth *cliproxyauth.Auth) *httptest.Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(exec)
	auth.Status = cliproxyauth.StatusActive
	if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
		t.Fatal(errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, exec.Identifier(), []*registry.ModelInfo{{ID: "test"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Client: sdkconfig.ClientConfig{
			Codex: sdkconfig.CodexClientConfig{EnableApplyPatch: true},
		},
	}, manager)
	router := gin.New()
	router.POST("/v1/responses", openai.NewOpenAIResponsesAPIHandler(base).Responses)
	router.GET("/v1/models", openai.NewOpenAIAPIHandler(base).OpenAIModels)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}

// The server cannot send the suffix or terminal frame until the client acknowledges
// a complete decoded line. Snapshot paths instead acknowledge unrelated text.
func TestApplyPatchBridgeLiveHTTPPreviewMatrix(t *testing.T) {
	for _, tc := range []struct {
		provider, protocol string
		snapshot           bool
	}{
		{"custom-compat", "chat", false}, {"claude", "claude", false}, {"claude-oauth", "claude", false},
		{"gemini-interactions", "interactions", false}, {"devin", "devin", false},
		{"xai", "responses", false}, {"meta", "responses", false}, {"kimi", "responses", false},
		{"gemini", "gemini", true}, {"vertex", "gemini", true}, {"antigravity", "antigravity", true},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			release := make(chan struct{})
			requestBody := make(chan []byte, 1)
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errReadAll := io.ReadAll(r.Body)
				if errReadAll != nil {
					t.Error(errReadAll)
					return
				}
				requestBody <- body
				name := "apply_patch"
				if tc.protocol == "claude" {
					name = gjson.GetBytes(body, "tools.0.name").String()
				}
				first, last := applyPatchTestFrames(tc.protocol, name)
				w.Header().Set("Content-Type", "text/event-stream")
				if _, errWriteString := io.WriteString(w, first); errWriteString != nil {
					return
				}
				w.(http.Flusher).Flush()
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				_, _ = io.WriteString(w, last)
			}))
			defer upstream.Close()
			defer unblock()
			exec := task6Executor(tc.provider)
			if tc.protocol == "responses" {
				exec = task6RepairExecutor(tc.provider)
			} else if tc.provider == "devin" {
				exec = NewDevinExecutor(&config.Config{})
			}
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: exec.Identifier(), Attributes: map[string]string{"api_key": "test", "base_url": upstream.URL}}
			if tc.provider == "claude-oauth" {
				auth.Attributes["api_key"] = "sk-ant-oat-test"
				auth.Metadata = map[string]any{"account_uuid": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"}
			}
			if tc.provider == "antigravity" {
				auth.Metadata = map[string]any{"access_token": "test", "expires_in": 3600, "timestamp": "2099-01-01T00:00:00Z", "project_id": "test"}
			}
			gateway := newApplyPatchTestGateway(t, exec, auth)
			// Catalog claims must be backed by the executor used by the POST below.
			for _, version := range []string{"0.137.0", "0.153.4", "cpa"} {
				applyPatchTestCatalog(t, gateway, version)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			payload, errSetBytes := sjson.SetBytes(applyPatchTestRequest(), "stream", true)
			if errSetBytes != nil {
				t.Fatal(errSetBytes)
			}
			request, errNewRequestWithContext := http.NewRequestWithContext(ctx, http.MethodPost, gateway.URL+"/v1/responses", bytes.NewReader(payload))
			if errNewRequestWithContext != nil {
				t.Fatal(errNewRequestWithContext)
			}
			request.Header.Set("Content-Type", "application/json")
			response, errDo := gateway.Client().Do(request)
			if errDo != nil {
				t.Fatal(errDo)
			}
			defer func() {
				if errClose := response.Body.Close(); errClose != nil {
					t.Error(errClose)
				}
			}()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", response.StatusCode)
			}
			scanner := bufio.NewScanner(response.Body)
			var lifecycle applyPatchTestLifecycle
			acknowledged := false
			for scanner.Scan() {
				for _, raw := range applyPatchTestPayloads(scanner.Bytes()) {
					event := gjson.ParseBytes(raw)
					lifecycle.consume(t, event)
					if !acknowledged {
						if tc.snapshot {
							if lifecycle.deltas.Len() != 0 {
								t.Fatal("fabricated patch progress before upstream snapshot")
							}
							acknowledged = event.Get("type").String() == "response.output_text.delta"
						} else {
							acknowledged = strings.Contains(lifecycle.deltas.String(), "+hello\n")
						}
						if acknowledged {
							if lifecycle.completed != 0 || lifecycle.inputDone != "" || lifecycle.itemDone != "" {
								t.Fatal("preview arrived only after completion")
							}
							unblock()
						}
					}
				}
			}
			if errScan := scanner.Err(); errScan != nil {
				t.Fatal(errScan)
			}
			if !acknowledged {
				t.Fatal("client never acknowledged the flushed frame")
			}
			lifecycle.assert(t)
			applyPatchTestDeclaration(t, tc.protocol, <-requestBody)
		})
	}
}

func applyPatchTestCatalog(t *testing.T, server *httptest.Server, version string) {
	t.Helper()
	request, errNewRequestWithContext := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/v1/models?client_version="+version, nil)
	if errNewRequestWithContext != nil {
		t.Fatal(errNewRequestWithContext)
	}
	response, errDo := server.Client().Do(request)
	if errDo != nil {
		t.Fatal(errDo)
	}
	defer func() {
		if errClose := response.Body.Close(); errClose != nil {
			t.Error(errClose)
		}
	}()
	body, errReadAll := io.ReadAll(response.Body)
	if errReadAll != nil {
		t.Fatal(errReadAll)
	}
	entry := gjson.GetBytes(body, `models.#(slug=="test")`)
	if response.StatusCode != http.StatusOK || entry.Get("apply_patch_tool_type").String() != "freeform" {
		t.Fatalf("catalog not backed by the registered route: %d %s", response.StatusCode, body)
	}
	if entry.Get("prefer_websockets").Bool() || len(entry.Get("service_tiers").Array()) != 0 {
		t.Fatalf("patch support enabled unrelated capabilities: %s", entry.Raw)
	}
}

type applyPatchTestLifecycle struct {
	deltas                            strings.Builder
	inputDone, itemDone, responseDone string
	completed, inputs, items          int
	itemID, callID                    string
}

func (s *applyPatchTestLifecycle) consume(t *testing.T, event gjson.Result) {
	t.Helper()
	switch event.Get("type").String() {
	case "response.failed", "error", "response.function_call_arguments.delta":
		t.Fatalf("unexpected bridge event: %s", event.Raw)
	case "response.custom_tool_call_input.delta", "response.custom_tool_call_input.done":
		id, call := event.Get("item_id").String(), event.Get("call_id").String()
		if id == "" || call == "" || (s.itemID != "" && (s.itemID != id || s.callID != call)) {
			t.Fatalf("unstable call identity: %s", event.Raw)
		}
		s.itemID, s.callID = id, call
		if event.Get("type").String() == "response.custom_tool_call_input.delta" {
			s.deltas.WriteString(event.Get("delta").String())
		} else {
			s.inputs++
			s.inputDone = event.Get("input").String()
		}
	case "response.output_item.done":
		item := event.Get("item")
		if item.Get("type").String() == "custom_tool_call" {
			if item.Get("id").String() != s.itemID || item.Get("call_id").String() != s.callID || item.Get("name").String() != "apply_patch" {
				t.Fatalf("item lost identity: %s", item.Raw)
			}
			s.items++
			s.itemDone = item.Get("input").String()
		}
	case "response.completed":
		s.completed++
		item := event.Get(`response.output.#(type=="custom_tool_call")`)
		if item.Get("id").String() != s.itemID || item.Get("call_id").String() != s.callID {
			t.Fatalf("response lost identity: %s", item.Raw)
		}
		s.responseDone = item.Get("input").String()
	}
}

func (s *applyPatchTestLifecycle) assert(t *testing.T) {
	t.Helper()
	if s.deltas.String() != applyPatchTestInput || s.inputDone != applyPatchTestInput || s.itemDone != applyPatchTestInput || s.responseDone != applyPatchTestInput || s.completed != 1 || s.inputs != 1 || s.items != 1 {
		t.Fatalf("inconsistent lifecycle: delta=%q input=%q item=%q response=%q counts=%d/%d/%d", s.deltas.String(), s.inputDone, s.itemDone, s.responseDone, s.inputs, s.items, s.completed)
	}
}

func newApplyPatchCompatTestExecutor(t *testing.T, reply string) (*OpenAICompatExecutor, *cliproxyauth.Auth, <-chan []byte) {
	t.Helper()
	bodies := make(chan []byte, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errReadAll := io.ReadAll(r.Body)
		if errReadAll != nil {
			http.Error(w, "request read failed", http.StatusBadRequest)
			return
		}
		bodies <- body
		contentType := "application/json"
		if strings.HasPrefix(reply, "data:") {
			contentType = "text/event-stream"
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(server.Close)
	return NewOpenAICompatExecutor("custom-compat", &config.Config{}), &cliproxyauth.Auth{Provider: "custom-compat", Attributes: map[string]string{"base_url": server.URL + "/v1", "api_key": "test"}}, bodies
}

func applyPatchTestChatReply(arguments string) string {
	return fmt.Sprintf(`{"id":"r1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"apply_patch","arguments":%q}}]},"finish_reason":"tool_calls"}]}`, arguments)
}

func TestApplyPatchBridgeInvalidArgumentsReturnsBadGateway(t *testing.T) {
	for _, args := range []string{`{"input":7}`, `{}`, `{"input":null}`, `{"input":"x","input":"y"}`, `{"input":"x","extra":"RAW_SECRET"}`, `{"input":"RAW_SECRET"`, `{"input":"x"} trailing`} {
		t.Run(args, func(t *testing.T) {
			exec, auth, _ := newApplyPatchCompatTestExecutor(t, applyPatchTestChatReply(args))
			payload := applyPatchTestRequest()
			response, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload})
			var status interface{ StatusCode() int }
			if errExecute == nil || !errors.As(errExecute, &status) || status.StatusCode() != http.StatusBadGateway || len(response.Payload) != 0 {
				t.Fatalf("expected empty gateway error, got %s %v", response.Payload, errExecute)
			}
			assertTask6PatchError(t, errExecute)
		})
	}
}

func TestApplyPatchBridgeHistoryRoundTrip(t *testing.T) {
	exec, auth, bodies := newApplyPatchCompatTestExecutor(t, applyPatchTestChatReply(applyPatchTestPartial+applyPatchTestRemainder))
	payload := applyPatchTestRequest()
	response, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	applyPatchTestDeclaration(t, "chat", <-bodies)
	call := gjson.GetBytes(response.Payload, `output.#(type=="custom_tool_call")`)
	if call.Get("call_id").String() != "c1" || call.Get("input").String() != applyPatchTestInput {
		t.Fatalf("wrong client call: %s", response.Payload)
	}
	next, errSetRawBytes := sjson.SetRawBytes(payload, "input", []byte("["+call.Raw+`,{"type":"custom_tool_call_output","call_id":"c1","output":"Success"}]`))
	if errSetRawBytes != nil {
		t.Fatal(errSetRawBytes)
	}
	_, errExecute = exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: next}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: next})
	if errExecute != nil {
		t.Fatal(errExecute)
	}
	body := <-bodies
	arguments := gjson.GetBytes(body, `messages.#(role=="assistant").tool_calls.0.function.arguments`).String()
	if !gjson.Valid(arguments) || gjson.Get(arguments, "input").String() != call.Get("input").String() || gjson.GetBytes(body, `messages.#(role=="assistant").tool_calls.0.id`).String() != "c1" {
		t.Fatalf("wrong replay arguments/identity: %s", body)
	}
	if gjson.GetBytes(body, `messages.#(role=="tool").tool_call_id`).String() != "c1" || gjson.GetBytes(body, `messages.#(role=="tool").content`).String() != "Success" {
		t.Fatalf("wrong replay result identity: %s", body)
	}
}

func TestApplyPatchBridgeTruncatedStreamDoesNotComplete(t *testing.T) {
	first, _ := applyPatchTestFrames("chat", "apply_patch")
	exec, auth, _ := newApplyPatchCompatTestExecutor(t, first)
	payload := applyPatchTestRequest()
	result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload, Stream: true})
	if errExecuteStream != nil {
		t.Fatal(errExecuteStream)
	}
	assertTask6FailedStream(t, result.Chunks)
}

func TestApplyPatchBridgeOrdinaryFunctionControl(t *testing.T) {
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		t.Run(source.String(), func(t *testing.T) {
			args := `{"input":"RAW_SECRET","extra":7}`
			exec, auth, bodies := newApplyPatchCompatTestExecutor(t, applyPatchTestChatReply(args))
			payload := []byte(`{"model":"test","messages":[{"role":"user","content":"edit"}],"tools":[{"type":"function","function":{"name":"apply_patch","parameters":{"type":"object","properties":{"input":{"type":"string"},"extra":{"type":"integer"}}}}}]}`)
			if source == sdktranslator.FormatOpenAIResponse {
				payload = []byte(`{"model":"test","input":"edit","tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object","properties":{"input":{"type":"string"},"extra":{"type":"integer"}}}}]}`)
			}
			response, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: payload}, cliproxyexecutor.Options{SourceFormat: source, OriginalRequest: payload})
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			body := <-bodies
			if !gjson.GetBytes(body, "tools.0.function.parameters.properties.extra").Exists() || strings.Contains(gjson.GetBytes(body, "tools.0.function.description").String(), "*** Begin Patch") {
				t.Fatalf("ordinary function schema changed: %s", body)
			}
			path := "choices.0.message.tool_calls.0.function.arguments"
			if source == sdktranslator.FormatOpenAIResponse {
				path = `output.#(type=="function_call").arguments`
			}
			arguments := gjson.GetBytes(response.Payload, path).String()
			if arguments != args || !gjson.Valid(arguments) || bytes.Contains(response.Payload, []byte(`"custom_tool_call"`)) {
				t.Fatalf("ordinary JSON function promoted to freeform: %s", response.Payload)
			}
		})
	}
}

// Kimi selects its reused Chat and Claude executors for these ordinary clients.
// Custom Responses clients select Kimi's Responses branch, covered by the HTTP matrix.
func TestApplyPatchBridgeKimiReusedClientControls(t *testing.T) {
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatClaude} {
		t.Run(source.String(), func(t *testing.T) {
			args := `{"input":"RAW_SECRET","extra":7}`
			reply := applyPatchTestChatReply(args)
			payload := []byte(`{"messages":[{"role":"user","content":"edit"}],"tools":[{"type":"function","function":{"name":"apply_patch","parameters":{"type":"object","properties":{"input":{"type":"string"},"extra":{"type":"integer"}}}}}]}`)
			if source == sdktranslator.FormatClaude {
				payload = []byte(`{"messages":[{"role":"user","content":"edit"}],"tools":[{"name":"apply_patch","input_schema":{"type":"object","properties":{"input":{"type":"string"},"extra":{"type":"integer"}}}}]}`)
				reply = fmt.Sprintf(`{"id":"r1","type":"message","role":"assistant","content":[{"type":"tool_use","id":"c1","name":"apply_patch","input":%s}],"stop_reason":"tool_use"}`, args)
			}
			_, auth, bodies := newApplyPatchCompatTestExecutor(t, reply)
			auth.Provider = "kimi"
			exec := NewKimiExecutor(&config.Config{}).ForAPIKey()
			response, errExecute := exec.Execute(t.Context(), auth, cliproxyexecutor.Request{Model: "kimi-k2.5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: source, OriginalRequest: payload})
			if errExecute != nil {
				t.Fatal(errExecute)
			}
			body := <-bodies
			tool := gjson.GetBytes(body, "tools.0.function")
			parameters := "parameters"
			if source == sdktranslator.FormatClaude {
				tool, parameters = gjson.GetBytes(body, "tools.0"), "input_schema"
			}
			if !tool.Get(parameters+".properties.extra").Exists() || strings.Contains(tool.Get("description").String(), "*** Begin Patch") || bytes.Contains(response.Payload, []byte("custom_tool_call")) || !bytes.Contains(response.Payload, []byte("RAW_SECRET")) {
				t.Fatalf("ordinary reused client was promoted or restricted: request=%s response=%s", body, response.Payload)
			}
			arguments := gjson.GetBytes(response.Payload, "choices.0.message.tool_calls.0.function.arguments").String()
			if source == sdktranslator.FormatClaude {
				arguments = gjson.GetBytes(response.Payload, "content.0.input").Raw
			}
			if !gjson.Valid(arguments) || gjson.Get(arguments, "extra").Int() != 7 {
				t.Fatalf("ordinary function lost valid JSON: %s", response.Payload)
			}
		})
	}
}

func applyPatchTestPreviewChunks(t *testing.T, chunks <-chan cliproxyexecutor.StreamChunk, unblock func()) []byte {
	t.Helper()
	var lifecycle applyPatchTestLifecycle
	var output []byte
	preview := false
	for chunk := range chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		output = append(output, chunk.Payload...)
		for _, raw := range applyPatchTestPayloads(chunk.Payload) {
			lifecycle.consume(t, gjson.ParseBytes(raw))
			if !preview && strings.Contains(lifecycle.deltas.String(), "+hello\n") {
				if lifecycle.completed != 0 || lifecycle.inputDone != "" || lifecycle.itemDone != "" {
					t.Fatal("preview arrived only at completion")
				}
				preview = true
				unblock()
			}
		}
	}
	if !preview {
		t.Fatal("no live line preview")
	}
	lifecycle.assert(t)
	return output
}

func TestApplyPatchBridgeLiveWebsocketPreview(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native-codex=%v", native), func(t *testing.T) {
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			bodies := make(chan []byte, 1)
			first, last := applyPatchTestFrames("responses", "apply_patch")
			if native {
				first = "data: { \"type\":\"response.output_item.added\", \"output_index\":0,\"item\":{\"type\":\"custom_tool_call\",\"id\":\"a1\",\"call_id\":\"c1\",\"name\":\"apply_patch\",\"input\":\"\"}}\n\ndata: { \"type\":\"response.custom_tool_call_input.delta\", \"output_index\":0,\"item_id\":\"a1\",\"call_id\":\"c1\",\"delta\":\"*** Begin Patch\\n*** Add File: a.txt\\n+hello\\n\"}\n\n"
				item := fmt.Sprintf(`{ "type":"custom_tool_call", "id":"a1","call_id":"c1","name":"apply_patch","input":%q}`, applyPatchTestInput)
				last = fmt.Sprintf("data: { \"type\":\"response.custom_tool_call_input.delta\", \"output_index\":0,\"item_id\":\"a1\",\"call_id\":\"c1\",\"delta\":\"*** End Patch\\n\"}\n\ndata: { \"type\":\"response.custom_tool_call_input.done\", \"item_id\":\"a1\",\"call_id\":\"c1\",\"input\":%q}\n\ndata: { \"type\":\"response.output_item.done\", \"output_index\":0,\"item\":%s}\n\ndata: { \"type\":\"response.completed\", \"response\":{\"id\":\"r1\",\"status\":\"completed\",\"output\":[%s]}}\n\n", applyPatchTestInput, item, item)
			}
			upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, errUpgrade := upgrader.Upgrade(w, r, nil)
				if errUpgrade != nil {
					t.Error(errUpgrade)
					return
				}
				defer func() {
					if errClose := conn.Close(); errClose != nil {
						t.Error(errClose)
					}
				}()
				_, body, errReadMessage := conn.ReadMessage()
				if errReadMessage != nil {
					t.Error(errReadMessage)
					return
				}
				bodies <- body
				for _, raw := range applyPatchTestPayloads([]byte(first)) {
					if errWriteMessage := conn.WriteMessage(websocket.TextMessage, raw); errWriteMessage != nil {
						return
					}
				}
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
				for _, raw := range applyPatchTestPayloads([]byte(last)) {
					if errWriteMessage := conn.WriteMessage(websocket.TextMessage, raw); errWriteMessage != nil {
						return
					}
				}
			}))
			defer server.Close()
			defer unblock()
			var exec cliproxyauth.ProviderExecutor
			if native {
				codex := NewCodexWebsocketsExecutor(&config.Config{Codex: config.CodexConfig{DisableCodexCloaking: true}})
				codex.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				exec = codex
			} else {
				xai := NewXAIWebsocketsExecutor(&config.Config{})
				xai.store = &codexWebsocketSessionStore{sessions: make(map[string]*codexWebsocketSession)}
				exec = xai
			}
			ctx, cancel := context.WithCancel(cliproxyexecutor.WithDownstreamWebsocket(t.Context()))
			defer cancel()
			payload := applyPatchTestRequest()
			auth := &cliproxyauth.Auth{ID: t.Name(), Provider: exec.Identifier(), Attributes: map[string]string{"api_key": "test", "base_url": server.URL, "websockets": "true"}}
			model := "grok-4"
			if native {
				model = "gpt-5.6-sol"
			}
			result, errExecuteStream := exec.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: model, Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload, Stream: true, Headers: http.Header{codexResponsesLiteHeader: {"true"}}})
			if errExecuteStream != nil {
				t.Fatal(errExecuteStream)
			}
			output := applyPatchTestPreviewChunks(t, result.Chunks, unblock)
			body := <-bodies
			if native {
				if gjson.GetBytes(body, "tools.0").Raw != gjson.GetBytes(payload, "tools.0").Raw {
					t.Fatalf("native grammar declaration altered: %s", body)
				}
				for _, raw := range applyPatchTestPayloads([]byte(first + last)) {
					if !bytes.Contains(output, raw) {
						t.Fatalf("native event bytes altered: %s", raw)
					}
				}
			} else {
				applyPatchTestDeclaration(t, "responses", body)
			}
		})
	}
}

func TestApplyPatchBridgeOrdinaryFunctionStreamControl(t *testing.T) {
	for _, source := range []sdktranslator.Format{sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse} {
		t.Run(source.String(), func(t *testing.T) {
			args := `{"input":"RAW_SECRET","extra":7}`
			reply := strings.ReplaceAll(task6ProviderFixture("custom-compat", "stream", "apply_patch"), fmt.Sprintf("%q", `{"input":7,"secret":"RAW_SECRET"}`), fmt.Sprintf("%q", args))
			exec, auth, bodies := newApplyPatchCompatTestExecutor(t, reply)
			payload := []byte(`{"messages":[{"role":"user","content":"edit"}],"tools":[{"type":"function","function":{"name":"apply_patch","parameters":{"type":"object","properties":{"extra":{"type":"integer"}}}}}]}`)
			if source == sdktranslator.FormatOpenAIResponse {
				payload = []byte(`{"input":"edit","tools":[{"type":"function","name":"apply_patch","parameters":{"type":"object","properties":{"extra":{"type":"integer"}}}}]}`)
			}
			result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: payload}, cliproxyexecutor.Options{SourceFormat: source, OriginalRequest: payload, Stream: true})
			if errExecuteStream != nil {
				t.Fatal(errExecuteStream)
			}
			var arguments strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				if bytes.Contains(chunk.Payload, []byte("custom_tool_call")) || bytes.Contains(chunk.Payload, []byte("response.failed")) {
					t.Fatalf("ordinary stream was promoted or restricted: %s", chunk.Payload)
				}
				for _, raw := range applyPatchTestPayloads(chunk.Payload) {
					event := gjson.ParseBytes(raw)
					if source == sdktranslator.FormatOpenAI {
						arguments.WriteString(event.Get("choices.0.delta.tool_calls.0.function.arguments").String())
					} else if event.Get("type").String() == "response.function_call_arguments.delta" {
						arguments.WriteString(event.Get("delta").String())
					}
				}
			}
			if !gjson.Valid(arguments.String()) || arguments.String() != args || !gjson.GetBytes(<-bodies, "tools.0.function.parameters.properties.extra").Exists() {
				t.Fatalf("ordinary stream did not preserve JSON arguments/schema: %q", arguments.String())
			}
		})
	}
}

func TestApplyPatchBridgeInvalidStreamArguments(t *testing.T) {
	// Valid decoded prefixes can precede EOF failure; raw envelope fields must not leak.
	for _, args := range []string{`{"input":7}`, `{}`, `{"input":null}`, `{"input":"x","input":"y"}`, `{"input":"x","extra":"RAW_SECRET"}`, `{"input":"unfinished"`, `{"input":"x"} trailing`} {
		t.Run(args, func(t *testing.T) {
			reply := strings.ReplaceAll(task6ProviderFixture("custom-compat", "stream", "apply_patch"), fmt.Sprintf("%q", `{"input":7,"secret":"RAW_SECRET"}`), fmt.Sprintf("%q", args))
			exec, auth, _ := newApplyPatchCompatTestExecutor(t, reply)
			payload := applyPatchTestRequest()
			result, errExecuteStream := exec.ExecuteStream(t.Context(), auth, cliproxyexecutor.Request{Model: "test", Payload: payload}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: payload, Stream: true})
			if errExecuteStream != nil {
				t.Fatal(errExecuteStream)
			}
			assertTask6FailedStream(t, result.Chunks)
		})
	}
}
