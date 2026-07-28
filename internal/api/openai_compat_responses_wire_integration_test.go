package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	"github.com/tidwall/gjson"
)

type openAICompatWireCapturedRequest struct {
	path          string
	authorization string
	body          []byte
}

func TestOpenAICompatResponsesWireHandlerEndToEnd(t *testing.T) {
	t.Run("responses JSON preserves Hermes and repository fields", func(t *testing.T) {
		server, model, requests := newOpenAICompatWireTestServer(t, "responses", false)
		payload := fmt.Sprintf(`{
			"model":%q,
			"instructions":"Keep the response structured.",
			"input":[
				{"role":"user","content":[{"type":"input_text","text":"Use the add tool."}]},
				{"type":"function_call","call_id":"call_1","name":"add","arguments":"{\"a\":1,\"b\":1}"},
				{"type":"function_call_output","call_id":"call_1","output":"2"},
				{"role":"user","content":[{"type":"input_text","text":"Return JSON."}]}
			],
			"tools":[
				{"type":"function","name":"add","parameters":{"type":"object","properties":{}}},
				{"type":"web_search"}
			],
			"text":{"format":{"type":"json_schema","name":"answer","strict":true,"schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}}},
			"store":false
		}`, model)

		recorder := performOpenAICompatWireRequest(t, server, "/v1/responses", payload)
		captured := <-requests
		if captured.path != "/v1/responses" {
			t.Fatalf("upstream path = %q, want /v1/responses", captured.path)
		}
		if captured.authorization != "Bearer test-upstream-key" {
			t.Fatalf("upstream Authorization = %q", captured.authorization)
		}
		if gjson.GetBytes(captured.body, "messages").Exists() {
			t.Fatalf("native Responses request was converted to messages: %s", captured.body)
		}
		if got := gjson.GetBytes(captured.body, "input.2.output").String(); got != "2" {
			t.Fatalf("function_call_output = %q, want 2; body=%s", got, captured.body)
		}
		if got := gjson.GetBytes(captured.body, `tools.#(type=="web_search").type`).String(); got != "web_search" {
			t.Fatalf("web_search tool type = %q, want web_search; body=%s", got, captured.body)
		}
		if !gjson.GetBytes(captured.body, "text.format.strict").Bool() {
			t.Fatalf("strict JSON schema was not preserved: %s", captured.body)
		}
		if got := gjson.GetBytes(recorder.Body.Bytes(), "object").String(); got != "response" {
			t.Fatalf("downstream object = %q, want response; body=%s", got, recorder.Body.String())
		}
		if got := gjson.GetBytes(recorder.Body.Bytes(), "output.0.content.0.text").String(); got != `{"answer":"ok"}` {
			t.Fatalf("downstream output text = %q; body=%s", got, recorder.Body.String())
		}
	})

	t.Run("responses SSE preserves Codex Responses Lite input", func(t *testing.T) {
		server, model, requests := newOpenAICompatWireTestServer(t, "responses", true)
		payload := fmt.Sprintf(`{
			"model":%q,
			"input":[
				{"type":"additional_tools","role":"developer","tools":[{"type":"custom","name":"exec","description":"Run a command","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}}]},
				{"role":"user","content":[{"type":"input_text","text":"Run pwd."}]}
			],
			"client_metadata":{"ws_request_header_x_openai_internal_codex_responses_lite":"true"},
			"stream":true
		}`, model)

		recorder := performOpenAICompatWireRequest(t, server, "/v1/responses", payload)
		captured := <-requests
		if captured.path != "/v1/responses" {
			t.Fatalf("upstream path = %q, want /v1/responses", captured.path)
		}
		if got := gjson.GetBytes(captured.body, "input.0.type").String(); got != "additional_tools" {
			t.Fatalf("input.0.type = %q, want additional_tools; body=%s", got, captured.body)
		}
		if got := gjson.GetBytes(captured.body, "input.0.tools.0.type").String(); got != "custom" {
			t.Fatalf("additional custom tool type = %q, want custom; body=%s", got, captured.body)
		}
		if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
			t.Fatalf("downstream Content-Type = %q, want text/event-stream", got)
		}
		streamBody := recorder.Body.String()
		createdIndex := strings.Index(streamBody, "response.created")
		completedIndex := strings.Index(streamBody, "response.completed")
		if createdIndex < 0 || completedIndex <= createdIndex {
			t.Fatalf("downstream SSE event order invalid: %s", streamBody)
		}
		if strings.Contains(streamBody, "[DONE]") {
			t.Fatalf("native Responses stream contains synthetic [DONE]: %s", streamBody)
		}
	})

	t.Run("responses without configured wire keeps Chat compatibility", func(t *testing.T) {
		server, model, requests := newOpenAICompatWireTestServer(t, "", false)
		payload := fmt.Sprintf(`{"model":%q,"input":"hello"}`, model)

		recorder := performOpenAICompatWireRequest(t, server, "/v1/responses", payload)
		captured := <-requests
		if captured.path != "/v1/chat/completions" {
			t.Fatalf("upstream path = %q, want /v1/chat/completions", captured.path)
		}
		if !gjson.GetBytes(captured.body, "messages").Exists() {
			t.Fatalf("default Responses request was not converted to messages: %s", captured.body)
		}
		if got := gjson.GetBytes(recorder.Body.Bytes(), "object").String(); got != "response" {
			t.Fatalf("downstream object = %q, want response; body=%s", got, recorder.Body.String())
		}
	})

	t.Run("configured Responses wire leaves Chat requests unchanged", func(t *testing.T) {
		server, model, requests := newOpenAICompatWireTestServer(t, "responses", false)
		payload := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model)

		recorder := performOpenAICompatWireRequest(t, server, "/v1/chat/completions", payload)
		captured := <-requests
		if captured.path != "/v1/chat/completions" {
			t.Fatalf("upstream path = %q, want /v1/chat/completions", captured.path)
		}
		if !gjson.GetBytes(captured.body, "messages").Exists() {
			t.Fatalf("Chat request lost messages: %s", captured.body)
		}
		if got := gjson.GetBytes(recorder.Body.Bytes(), "object").String(); got != "chat.completion" {
			t.Fatalf("downstream object = %q, want chat.completion; body=%s", got, recorder.Body.String())
		}
	})
}

func newOpenAICompatWireTestServer(t *testing.T, wireAPI string, streamResponse bool) (*Server, string, <-chan openAICompatWireCapturedRequest) {
	t.Helper()

	requests := make(chan openAICompatWireCapturedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream request body: %v", errRead)
		}
		requests <- openAICompatWireCapturedRequest{
			path:          r.URL.Path,
			authorization: r.Header.Get("Authorization"),
			body:          body,
		}
		if streamResponse {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.created\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_wire\",\"status\":\"in_progress\"}}\n\n")
			_, _ = io.WriteString(w, "event: response.completed\n")
			_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_wire\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1,\"total_tokens\":4}}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			_, _ = io.WriteString(w, `{"id":"resp_wire","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{\"answer\":\"ok\"}"}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"chatcmpl_wire","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`)
	}))
	t.Cleanup(upstream.Close)

	model := fmt.Sprintf("wire-e2e-%d", time.Now().UnixNano())
	providerName := "jiurelay-wire-test"
	cfg := &proxyconfig.Config{
		OpenAICompatibility: []proxyconfig.OpenAICompatibility{{
			Name:    providerName,
			BaseURL: upstream.URL + "/v1",
			WireAPI: wireAPI,
			APIKeyEntries: []proxyconfig.OpenAICompatibilityAPIKey{{
				APIKey: "test-upstream-key",
			}},
			Models: []proxyconfig.OpenAICompatibilityModel{{Name: model, Alias: model}},
		}},
	}
	auths, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Unix(0, 0),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("synthesize OpenAI-compatible auth: %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("synthesized auth count = %d, want 1", len(auths))
	}
	credential := auths[0]

	server := newTestServer(t)
	server.handlers.AuthManager.SetConfig(cfg)
	server.handlers.AuthManager.RegisterExecutor(runtimeexecutor.NewOpenAICompatExecutor(credential.Provider, cfg))
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register OpenAI-compatible auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(credential.ID)
	})

	return server, model, requests
}

func performOpenAICompatWireRequest(t *testing.T, server *Server, path, payload string) *httptest.ResponseRecorder {
	t.Helper()

	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	server.engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("downstream status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	return recorder
}
