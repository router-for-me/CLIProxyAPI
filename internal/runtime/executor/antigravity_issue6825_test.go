package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const issue6825FirstRequest = `{"model":"gemini-3-flash","tool_choice":"auto","messages":[{"role":"user","content":"What time is it?"}],"tools":[{"type":"function","function":{"name":"get_time","parameters":{"type":"object","properties":{"tz":{"type":"string"}}}}}]}`

func TestIssue6825StreamToolIndicesAcrossChunks(t *testing.T) {
	original := []byte(issue6825FirstRequest)
	translated := sdktranslator.TranslateRequest(sdktranslator.FormatOpenAI, sdktranslator.FormatAntigravity, "gemini-3-flash", original, true)
	chunks := []string{
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_time","args":{"tz":"UTC"}},"thoughtSignature":"` + issue4959GeminiThoughtSignature() + `"}]}}]}}`,
		`{"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"get_time","args":{"tz":"America/Santiago"}}},{"functionCall":{"name":"get_time","args":{"tz":"Asia/Tokyo"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`,
	}
	var param any
	var indices []int64
	var arguments []string
	for _, chunk := range chunks {
		for _, out := range sdktranslator.TranslateStream(context.Background(), sdktranslator.FormatAntigravity, sdktranslator.FormatOpenAI, "gemini-3-flash", original, translated, []byte(chunk), &param) {
			for _, call := range gjson.GetBytes(out, "choices.0.delta.tool_calls").Array() {
				indices = append(indices, call.Get("index").Int())
				arguments = append(arguments, call.Get("function.arguments").String())
			}
		}
	}
	if want := []int64{0, 1, 2}; !reflect.DeepEqual(indices, want) {
		t.Errorf("stream tool indices = %v, want %v; clients must not merge distinct calls", indices, want)
	}
	if want := []string{`{"tz":"UTC"}`, `{"tz":"America/Santiago"}`, `{"tz":"Asia/Tokyo"}`}; !reflect.DeepEqual(arguments, want) {
		t.Errorf("tool arguments = %v, want %v", arguments, want)
	}
}

// issue6825Conversation keeps translation, request preparation and replay real;
// only the remote Antigravity service is replaced with a local HTTP endpoint.
type issue6825Conversation struct {
	t        *testing.T
	executor *AntigravityExecutor
	auth     *cliproxyauth.Auth
	requests chan issue6825UpstreamRequest
}

type issue6825UpstreamRequest struct {
	body []byte
	path string
}

func newIssue6825Conversation(t *testing.T, responses ...string) *issue6825Conversation {
	t.Helper()
	cache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(cache.ClearAntigravityReasoningReplayCache)
	requests := make(chan issue6825UpstreamRequest, 8)
	responseIndex := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read upstream request: %v", errRead)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		requests <- issue6825UpstreamRequest{body: body, path: r.URL.Path}
		response := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Done."}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1,"totalTokenCount":11}}}`
		if responseIndex < len(responses) {
			response = responses[responseIndex]
		}
		responseIndex++
		if strings.Contains(r.URL.Path, "streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprintf(w, "data: %s\n\n", response)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, response)
		}
	}))
	t.Cleanup(server.Close)
	auth := testAntigravityAuth(server.URL)
	auth.ID = "issue-6825-" + t.Name()
	auth.Metadata["project_id"] = "test-project"
	return &issue6825Conversation{t: t, executor: NewAntigravityExecutor(&config.Config{}), auth: auth, requests: requests}
}

func issue6825ToolResponse(signature, prefix string) string {
	return `{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[` + prefix + `{"functionCall":{"name":"get_time","args":{"tz":"UTC"}},"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}}}`
}

func (c *issue6825Conversation) callWithContext(ctx context.Context, payload []byte, opts cliproxyexecutor.Options, enrich bool, reqMetadata map[string]any) ([]byte, issue6825UpstreamRequest) {
	c.t.Helper()
	if ctx == nil {
		ctx = context.Background()
	}
	opts.SourceFormat = sdktranslator.FormatOpenAI
	req := cliproxyexecutor.Request{Model: "gemini-3-flash", Payload: payload, Metadata: reqMetadata}
	if enrich {
		req, opts = session.Enrich(req, opts)
	}
	if !opts.Stream {
		response, errExecute := c.executor.Execute(ctx, c.auth, req, opts)
		if errExecute != nil {
			c.t.Fatalf("Execute: %v", errExecute)
		}
		message := gjson.GetBytes(response.Payload, "choices.0.message")
		if !message.Exists() {
			c.t.Fatalf("missing OpenAI assistant message: %s", response.Payload)
		}
		return []byte(message.Raw), <-c.requests
	}
	response, errExecute := c.executor.ExecuteStream(ctx, c.auth, req, opts)
	if errExecute != nil {
		c.t.Fatalf("ExecuteStream: %v", errExecute)
	}
	message := []byte(`{"role":"assistant"}`)
	var calls []map[string]any
	var content, reasoning string
	for chunk := range response.Chunks {
		if chunk.Err != nil {
			c.t.Fatalf("stream: %v", chunk.Err)
		}
		for _, line := range bytes.Split(chunk.Payload, []byte("\n")) {
			line = bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			delta := gjson.GetBytes(line, "choices.0.delta")
			content += delta.Get("content").String()
			reasoning += delta.Get("reasoning_content").String()
			for _, call := range delta.Get("tool_calls").Array() {
				index := int(call.Get("index").Int())
				if index != len(calls) {
					c.t.Fatalf("unexpected new tool index %d, want %d", index, len(calls))
				}
				calls = append(calls, map[string]any{"id": call.Get("id").String(), "type": "function", "function": map[string]string{"name": call.Get("function.name").String(), "arguments": call.Get("function.arguments").String()}})
			}
		}
	}
	if len(calls) > 0 {
		message, _ = sjson.SetBytes(message, "tool_calls", calls)
	}
	if content != "" {
		message, _ = sjson.SetBytes(message, "content", content)
	}
	if reasoning != "" {
		message, _ = sjson.SetBytes(message, "reasoning_content", reasoning)
	}
	return message, <-c.requests
}

func (c *issue6825Conversation) call(payload []byte, opts cliproxyexecutor.Options, enrich bool, reqMetadata map[string]any) ([]byte, issue6825UpstreamRequest) {
	return c.callWithContext(context.Background(), payload, opts, enrich, reqMetadata)
}

func issue6825Followup(t *testing.T, assistant []byte) []byte {
	t.Helper()
	calls := gjson.GetBytes(assistant, "tool_calls").Array()
	if len(calls) == 0 {
		t.Fatalf("first round returned no tool calls: %s", assistant)
	}
	out, errSet := sjson.SetRawBytes([]byte(issue6825FirstRequest), "messages.-1", assistant)
	if errSet != nil {
		t.Fatal(errSet)
	}
	for _, call := range calls {
		if call.Get("id").String() == "" || call.Get("function.name").String() != "get_time" || call.Get("function.arguments").String() != `{"tz":"UTC"}` {
			t.Fatalf("invalid client-visible tool call: %s", call.Raw)
		}
		out, errSet = sjson.SetBytes(out, "messages.-1", map[string]string{"role": "tool", "tool_call_id": call.Get("id").String(), "content": "12:00 UTC"})
		if errSet != nil {
			t.Fatal(errSet)
		}
	}
	return out
}

func issue6825FirstToolSignature(t *testing.T, request []byte) string {
	t.Helper()
	for _, content := range gjson.GetBytes(request, "request.contents").Array() {
		for _, part := range content.Get("parts").Array() {
			if part.Get("functionCall.name").String() == "get_time" {
				return part.Get("thoughtSignature").String()
			}
		}
	}
	t.Fatalf("missing historical functionCall: %s", request)
	return ""
}

func TestIssue6825ToolPolicyKeepsHistoricalSignature(t *testing.T) {
	for _, policy := range []string{"auto", "none", "omitted_tools"} {
		t.Run(policy, func(t *testing.T) {
			signature := issue4959GeminiThoughtSignature()
			conversation := newIssue6825Conversation(t, issue6825ToolResponse(signature, ""))
			opts := cliproxyexecutor.Options{Stream: true, Headers: http.Header{"Session-Id": {t.Name()}}}
			assistant, _ := conversation.call([]byte(issue6825FirstRequest), opts, false, nil)
			followup := issue6825Followup(t, assistant)
			if policy == "omitted_tools" {
				followup, _ = sjson.DeleteBytes(followup, "tools")
			} else {
				followup, _ = sjson.SetBytes(followup, "tool_choice", policy)
			}
			_, upstream := conversation.call(followup, opts, false, nil)
			if policy == "none" {
				if got := gjson.GetBytes(upstream.body, "request.toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
					t.Errorf("function calling mode = %q, want NONE", got)
				}
				if got := gjson.GetBytes(upstream.body, "request.tools.0.functionDeclarations.0.name").String(); got != "get_time" {
					t.Errorf("historical tool declaration = %q, want get_time", got)
				}
			}
			if got := issue6825FirstToolSignature(t, upstream.body); got != signature {
				t.Errorf("historical signature = %q, want %q after AUTO -> %s", got, signature, policy)
			}
		})
	}
}

func TestIssue6825NonStreamTwoTurnSignatureReplay(t *testing.T) {
	signature := issue4959GeminiThoughtSignature()
	for _, fixture := range []struct {
		name           string
		response       string
		stripReasoning bool
		wantCallID     string
	}{
		{name: "tool_only", response: issue6825ToolResponse(signature, "")},
		{name: "thought_before_tool", response: issue6825ToolResponse(signature, `{"thought":true,"text":"Check the current time."},`)},
		{name: "visible_text_before_tool", response: issue6825ToolResponse(signature, `{"text":"I will check."},`)},
		{
			name:     "thought_carries_signature_before_unsigned_tool",
			response: `{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"text":"Check the current time.","thoughtSignature":"` + signature + `"},{"functionCall":{"name":"get_time","args":{"tz":"UTC"}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}}}`,
		},
		{
			name:           "thought_carries_signature_client_omits_reasoning_content",
			response:       `{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[{"thought":true,"text":"Check the current time.","thoughtSignature":"` + signature + `"},{"functionCall":{"name":"get_time","args":{"tz":"UTC"}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}}}`,
			stripReasoning: true,
		},
		{
			name:     "tool_response_without_finish_reason",
			response: `{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"get_time","args":{"tz":"UTC"}},"thoughtSignature":"` + signature + `"}]}}],"modelVersion":"gemini-3-flash","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}}}`,
		},
		{
			name:       "upstream_tool_call_id_preserved",
			response:   `{"response":{"candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"id":"call_upstream_6825","name":"get_time","args":{"tz":"UTC"}},"thoughtSignature":"` + signature + `"}]},"finishReason":"STOP"}],"modelVersion":"gemini-3-flash","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}}}`,
			wantCallID: "call_upstream_6825",
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			conversation := newIssue6825Conversation(t, fixture.response)
			opts := cliproxyexecutor.Options{Headers: http.Header{"Session-Id": {t.Name()}}}
			assistant, first := conversation.call([]byte(issue6825FirstRequest), opts, false, nil)
			if !strings.HasSuffix(first.path, ":generateContent") {
				t.Fatalf("first upstream path = %q, want generateContent", first.path)
			}
			if fixture.wantCallID != "" {
				if gotID := gjson.GetBytes(assistant, "tool_calls.0.id").String(); gotID != fixture.wantCallID {
					t.Errorf("round 1 tool_calls[0].id = %q, want %q", gotID, fixture.wantCallID)
				}
			}
			if fixture.stripReasoning {
				assistant, _ = sjson.DeleteBytes(assistant, "reasoning_content")
			}
			_, second := conversation.call(issue6825Followup(t, assistant), opts, false, nil)
			if !strings.HasSuffix(second.path, ":generateContent") {
				t.Fatalf("second upstream path = %q, want generateContent", second.path)
			}
			if got := issue6825FirstToolSignature(t, second.body); got != signature {
				t.Errorf("non-stream historical signature = %q, want %q", got, signature)
			}
		})
	}
}

func TestIssue6825PolicyReplayRejectsChangedContext(t *testing.T) {
	for _, change := range []string{"unchanged", "system", "history", "arguments", "caller"} {
		t.Run(change, func(t *testing.T) {
			signature := issue4959GeminiThoughtSignature()
			conversation := newIssue6825Conversation(t, issue6825ToolResponse(signature, ""))
			opts := cliproxyexecutor.Options{
				Headers:  http.Header{"Session-Id": {t.Name()}},
				Metadata: map[string]any{cliproxyexecutor.CallerScopeMetadataKey: session.CallerScope("trusted-caller-A")},
			}
			assistant, _ := conversation.call([]byte(issue6825FirstRequest), opts, false, nil)
			followup := issue6825Followup(t, assistant)
			followup, _ = sjson.SetBytes(followup, "tool_choice", "none")
			switch change {
			case "system":
				messages := gjson.GetBytes(followup, "messages").Raw
				followup, _ = sjson.SetRawBytes(followup, "messages", []byte(`[{"role":"system","content":"Different system instructions"},`+messages[1:]))
			case "history":
				followup, _ = sjson.SetBytes(followup, "messages.0.content", "Different user history")
			case "arguments":
				followup, _ = sjson.SetBytes(followup, "messages.1.tool_calls.0.function.arguments", `{"tz":"Asia/Tokyo"}`)
			case "caller":
				// Untrusted body metadata must not override the trusted execution scope.
				followup, _ = sjson.SetBytes(followup, "tool_choice", "auto")
				followup, _ = sjson.SetBytes(followup, "metadata.caller_scope", session.CallerScope("trusted-caller-A"))
				opts.Metadata = map[string]any{cliproxyexecutor.CallerScopeMetadataKey: session.CallerScope("trusted-caller-B")}
			}
			_, second := conversation.call(followup, opts, false, nil)
			want := "skip_thought_signature_validator"
			if change == "unchanged" {
				want = signature
			}
			if got := issue6825FirstToolSignature(t, second.body); got != want {
				t.Errorf("historical signature after %s change = %q, want %q", change, got, want)
			}
		})
	}
}

func TestIssue6825PayloadRulesRemainFinal(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			conversation := newIssue6825Conversation(t, issue6825ToolResponse(issue4959GeminiThoughtSignature(), ""))
			opts := cliproxyexecutor.Options{Stream: streaming, Headers: http.Header{"Session-Id": {t.Name()}}}
			assistant, _ := conversation.call([]byte(issue6825FirstRequest), opts, false, nil)
			conversation.executor.cfg.Payload = config.PayloadConfig{
				Override: []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: map[string]any{"toolConfig.functionCallingConfig.mode": "NONE"}}},
				Filter:   []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "gemini-*"}}, Params: []string{"tools", "contents.1.parts.0.thoughtSignature"}}},
			}
			_, second := conversation.call(issue6825Followup(t, assistant), opts, false, nil)
			if gjson.GetBytes(second.body, "request.tools").Exists() || gjson.GetBytes(second.body, "request.contents.1.parts.0.thoughtSignature").Exists() {
				t.Errorf("payload filters were undone after replay: %s", second.body)
			}
			if got := gjson.GetBytes(second.body, "request.toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
				t.Errorf("configured final mode = %q, want NONE", got)
			}
		})
	}
}

func TestIssue6825SDKCallerReplayIsolation(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		enrich      bool
		reqMetadata bool
		header      string
		shared      bool
		derived     bool
		ginContext  bool
	}{
		{name: "direct_options_caller"},
		{name: "direct_request_caller", reqMetadata: true},
		{name: "enriched_inferred_session", enrich: true},
		{name: "enriched_session_id", enrich: true, header: "Session-Id"},
		{name: "enriched_x_session_id", enrich: true, header: "X-Session-Id"},
		{name: "shared_derived_id_different_callers", enrich: true, derived: true},
		{name: "shared_session_id_different_callers", enrich: true, header: "Session-Id", shared: true},
		{name: "shared_x_session_id_different_callers", enrich: true, header: "X-Session-Id", shared: true},
		{name: "gin_context_user_api_key_without_metadata", ginContext: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			sigA, sigB := issue4959GeminiThoughtSignature(), testGeminiSignaturePayload()
			conversation := newIssue6825Conversation(t, issue6825ToolResponse(sigA, ""), issue6825ToolResponse(sigB, ""))
			options := func(caller string) (cliproxyexecutor.Options, map[string]any) {
				metadata := map[string]any{cliproxyexecutor.CallerScopeMetadataKey: session.CallerScope("downstream-api-key-" + caller)}
				if scenario.derived {
					metadata[cliproxyexecutor.DerivedSessionIDMetadataKey] = "same-derived-id"
				}
				opts := cliproxyexecutor.Options{}
				if scenario.header != "" {
					sessionID := "conversation-" + caller
					if scenario.shared {
						sessionID = "shared-conversation"
					}
					opts.Headers = http.Header{scenario.header: {sessionID}}
				}
				if scenario.reqMetadata {
					return opts, metadata
				}
				opts.Metadata = metadata
				return opts, nil
			}
			optsA, metadataA := options("A")
			optsB, metadataB := options("B")
			ctxA := context.Background()
			ctxB := context.Background()
			if scenario.ginContext {
				optsA.Metadata = nil
				optsB.Metadata = nil
				gin.SetMode(gin.TestMode)
				ginCtxA, _ := gin.CreateTestContext(httptest.NewRecorder())
				ginCtxA.Set("userApiKey", "downstream-api-key-A")
				ctxA = context.WithValue(context.Background(), "gin", ginCtxA) //nolint:staticcheck -- "gin" is the shared Gin context key contract.
				ginCtxB, _ := gin.CreateTestContext(httptest.NewRecorder())
				ginCtxB.Set("userApiKey", "downstream-api-key-B")
				ctxB = context.WithValue(context.Background(), "gin", ginCtxB) //nolint:staticcheck -- "gin" is the shared Gin context key contract.
			}
			assistantA, _ := conversation.callWithContext(ctxA, []byte(issue6825FirstRequest), optsA, scenario.enrich, metadataA)
			assistantB, _ := conversation.callWithContext(ctxB, []byte(issue6825FirstRequest), optsB, scenario.enrich, metadataB)
			_, secondA := conversation.callWithContext(ctxA, issue6825Followup(t, assistantA), optsA, scenario.enrich, metadataA)
			if got := issue6825FirstToolSignature(t, secondA.body); got != sigA {
				t.Errorf("caller A historical signature = %q, want its own %q (caller B = %q)", got, sigA, sigB)
			}
			_, secondB := conversation.callWithContext(ctxB, issue6825Followup(t, assistantB), optsB, scenario.enrich, metadataB)
			if got := issue6825FirstToolSignature(t, secondB.body); got != sigB {
				t.Errorf("caller B historical signature = %q, want its own %q", got, sigB)
			}
		})
	}
}
