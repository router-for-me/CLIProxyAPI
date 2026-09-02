package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/sirupsen/logrus/hooks/test"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"google.golang.org/protobuf/encoding/protowire"
)

func testGeminiSignaturePayload() string {
	payload := append([]byte{0x0A}, bytes.Repeat([]byte{0x56}, 48)...)
	return base64.StdEncoding.EncodeToString(payload)
}

// testFakeClaudeSignature returns a base64 string starting with 'E' that passes
// the lightweight hasValidClaudeSignature check but has invalid protobuf content
// (first decoded byte 0x12 is correct, but no valid protobuf field 2 follows),
// so it fails deep validation in strict mode.
func testFakeClaudeSignature() string {
	return base64.StdEncoding.EncodeToString([]byte{0x12, 0xFF, 0xFE, 0xFD})
}

func issue4959GeminiThoughtSignature() string {
	return "EjQKMgEMOdbHO0Gd+c9Mxk4ELwPGbpCEcp2mFfYYLix2UVtBH3fL8GECc4+JITVnHF4qZDsA"
}

// issue4959ResponsesModelFirstPayload is the #4959 Responses history: a
// next:function reasoning carrier, then function_call / function_call_output,
// then trailing assistant turns. Trailing model turns are left for a follow-up.
func issue4959ResponsesModelFirstPayload() []byte {
	carrier := "cpa-gemini-responses-carrier-v1:next:function:" + base64.RawStdEncoding.EncodeToString([]byte(issue4959GeminiThoughtSignature()))
	return []byte(`{"model":"gemini-3.7-flash-high","input":[` +
		`{"type":"reasoning","id":"rs_resp_test_detached_before_0","summary":[],"encrypted_content":"` + carrier + `"},` +
		`{"type":"function_call","call_id":"call_bash_1","name":"Bash","arguments":"{\"command\":\"true\"}"},` +
		`{"type":"function_call_output","call_id":"call_bash_1","output":"ok"},` +
		`{"role":"assistant","content":[{"type":"output_text","text":"first"}]},` +
		`{"role":"assistant","content":[{"type":"output_text","text":"second"}]}` +
		`]}`)
}

func contentHasNamedPart(content gjson.Result, partKind, name string) bool {
	for _, part := range content.Get("parts").Array() {
		if part.Get(partKind+".name").String() == name {
			return true
		}
	}
	return false
}

func assertIssue4959LeadingUserContents(t *testing.T, contents []gjson.Result) {
	t.Helper()
	if len(contents) < 3 {
		t.Fatalf("contents too short: %d", len(contents))
	}
	leadingText := contents[0].Get("parts.0.text")
	if contents[0].Get("role").String() != "user" || !leadingText.Exists() || leadingText.String() != "" {
		t.Fatalf("synthetic leading user missing: %s", contents[0].Raw)
	}
	if contents[1].Get("role").String() != "model" || !contentHasNamedPart(contents[1], "functionCall", "Bash") {
		t.Fatalf("function call is not immediately after the synthetic user: %s", contents[1].Raw)
	}
	if !contentHasNamedPart(contents[2], "functionResponse", "Bash") {
		t.Fatalf("function response missing or moved: %s", contents[2].Raw)
	}
}

func testAntigravityAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": baseURL,
		},
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
}

func invalidClaudeThinkingPayload() []byte {
	return []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "bad", "signature": "` + testFakeClaudeSignature() + `"},
					{"type": "text", "text": "hello"}
				]
			}
		]
	}`)
}

func newSignatureDebugHook(t *testing.T) *test.Hook {
	t.Helper()

	previousLevel := log.GetLevel()
	log.SetLevel(log.DebugLevel)
	hook := test.NewLocal(log.StandardLogger())
	t.Cleanup(func() {
		hook.Reset()
		log.SetLevel(previousLevel)
	})
	return hook
}

func assertSignatureDebugDoesNotLeak(t *testing.T, hook *test.Hook, forbidden string) {
	t.Helper()

	if forbidden == "" {
		return
	}
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, forbidden) {
			t.Fatalf("debug log leaked signature in message: %q", entry.Message)
		}
		for key, value := range entry.Data {
			if strings.Contains(fmt.Sprint(value), forbidden) {
				t.Fatalf("debug log leaked signature in field %q: %v", key, value)
			}
		}
	}
}

func TestSanitizeAntigravityGeminiRequestSignaturesFinalizesParallelCalls(t *testing.T) {
	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	encoded := protowire.AppendTag(nil, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, inner)
	nativeSignature := base64.StdEncoding.EncodeToString(encoded)

	tests := []struct {
		name               string
		firstSignature     string
		secondSignature    string
		wantFirstSignature string
	}{
		{
			name:               "synthetic",
			wantFirstSignature: "skip_thought_signature_validator",
		},
		{
			name:               "native",
			firstSignature:     nativeSignature,
			secondSignature:    "skip_thought_signature_validator",
			wantFirstSignature: nativeSignature,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"name":"first","args":{}}},{"functionCall":{"name":"second","args":{}}}]},{"role":"user","parts":[{"functionResponse":{"name":"first","response":{"result":"ok"}}},{"functionResponse":{"name":"second","response":{"result":"ok"}}}]}]}}`)
			if tt.firstSignature != "" {
				payload, _ = sjson.SetBytes(payload, "request.contents.0.parts.0.thoughtSignature", tt.firstSignature)
			}
			if tt.secondSignature != "" {
				payload, _ = sjson.SetBytes(payload, "request.contents.0.parts.1.thoughtSignature", tt.secondSignature)
			}

			output := sanitizeAntigravityGeminiRequestSignatures("gemini-3.5-flash", payload)
			if got := gjson.GetBytes(output, "request.contents.0.parts.0.thoughtSignature").String(); got != tt.wantFirstSignature {
				t.Fatalf("first signature = %q, want %q; output=%s", got, tt.wantFirstSignature, output)
			}
			if signature := gjson.GetBytes(output, "request.contents.0.parts.1.thoughtSignature"); signature.Exists() {
				t.Fatalf("second parallel call should remain unsigned; output=%s", output)
			}
			if got := gjson.GetBytes(output, "request.contents.1.role").String(); got != "model" {
				t.Fatalf("functionResponse role = %q, want native Antigravity model role; output=%s", got, output)
			}
		})
	}
}

func TestAntigravitySensitiveWordsObfuscatesSystemInstructionOnly(t *testing.T) {
	executor := NewAntigravityExecutor(&config.Config{
		Antigravity: config.AntigravityConfig{SensitiveWords: []string{"proxy"}},
	})
	payload := []byte(`{"request":{"systemInstruction":{"parts":[{"text":"Use proxy safely"}]},"contents":[{"role":"user","parts":[{"text":"proxy remains unchanged"}]}]}}`)

	got := executor.obfuscateSensitiveWords(payload)
	if systemText := gjson.GetBytes(got, "request.systemInstruction.parts.0.text").String(); systemText != "Use p\u200Broxy safely" {
		t.Fatalf("system instruction = %q, want zero-width obfuscation", systemText)
	}
	if contentText := gjson.GetBytes(got, "request.contents.0.parts.0.text").String(); contentText != "proxy remains unchanged" {
		t.Fatalf("content text = %q, want unchanged", contentText)
	}
}

func TestAntigravityStreamObfuscatesSensitiveSystemInstruction(t *testing.T) {
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		captured <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {}\n\n"))
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{SensitiveWords: []string{"Hermes", "Nous Research"}},
		RequestRetry: 1,
	})
	result, errExecute := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.6-flash-high",
		Payload: []byte(`{"model":"gemini-3.6-flash-high","instructions":"You are Hermes Agent, an intelligent AI assistant created by Nous Research.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	body := <-captured
	got := gjson.GetBytes(body, "request.systemInstruction.parts.0.text").String()
	want := "You are H\u200Bermes Agent, an intelligent AI assistant created by N\u200Bous Research."
	if got != want {
		t.Fatalf("system instruction = %q, want %q; body=%s", got, want, body)
	}
}

func TestAntigravityStreamDoesNotEmitSyntheticTerminalOnReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}
		_, _ = io.WriteString(w, `data: {"response":{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}}`+"\n")
		flusher.Flush()
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			return
		}
		conn, _, errHijack := hijacker.Hijack()
		if errHijack == nil {
			_ = conn.Close()
		}
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{
		Antigravity:  config.AntigravityConfig{},
		RequestRetry: 1,
	})
	result, errExecute := executor.ExecuteStream(context.Background(), &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "token-123",
			"expired":      time.Now().Add(24 * time.Hour).Format(time.RFC3339),
			"project_id":   "project-1",
		},
		Attributes: map[string]string{"base_url": server.URL},
	}, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash",
		Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatGemini,
		ResponseFormat: sdktranslator.FormatGemini,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}

	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
			continue
		}
		if finishReason := gjson.GetBytes(chunk.Payload, "candidates.0.finishReason").String(); finishReason == "STOP" {
			t.Fatalf("read error must not emit synthetic STOP: %s", chunk.Payload)
		}
	}
	if streamErr == nil {
		t.Fatal("expected stream read error")
	}
}

func TestAntigravityStreamPrependsLeadingUserForGemini(t *testing.T) {
	assertLeadingFunctionHistory := func(t *testing.T, body []byte) {
		t.Helper()
		contents := gjson.GetBytes(body, "request.contents").Array()
		if len(contents) != 3 || contents[0].Get("role").String() != "user" {
			t.Fatalf("upstream roles malformed: %s", body)
		}
		leadingText := contents[0].Get("parts.0.text")
		if !leadingText.Exists() || leadingText.String() != "" {
			t.Fatalf("synthetic leading user missing: %s", body)
		}
		if !contents[1].Get("parts.0.functionCall").Exists() || !contents[2].Get("parts.0.functionResponse").Exists() {
			t.Fatalf("function history changed: %s", body)
		}
	}

	tests := []struct {
		name    string
		format  sdktranslator.Format
		payload string
		assert  func(*testing.T, []byte)
	}{
		{
			name:   "Gemini prepends user before leading function call",
			format: sdktranslator.FormatGemini,
			payload: `{"contents":[` +
				`{"role":"model","parts":[{"functionCall":{"name":"run","args":{}}}]},` +
				`{"role":"user","parts":[{"functionResponse":{"name":"run","response":{"result":"ok"}}}]}` +
				`]}`,
			assert: assertLeadingFunctionHistory,
		},
		{
			name:   "OpenAI Chat prepends user before leading tool call",
			format: sdktranslator.FormatOpenAI,
			payload: `{"messages":[` +
				`{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"run","arguments":"{}"}}]},` +
				`{"role":"tool","tool_call_id":"call-1","content":"ok"}` +
				`]}`,
			assert: assertLeadingFunctionHistory,
		},
		{
			name:   "OpenAI Responses prepends user before leading function call",
			format: sdktranslator.FormatOpenAIResponse,
			payload: `{"input":[` +
				`{"type":"function_call","call_id":"call-1","name":"run","arguments":"{}"},` +
				`{"type":"function_call_output","call_id":"call-1","output":"ok"}` +
				`]}`,
			assert: assertLeadingFunctionHistory,
		},
		{
			name:   "Claude prepends user before leading tool use",
			format: sdktranslator.FormatClaude,
			payload: `{"messages":[` +
				`{"role":"assistant","content":[{"type":"tool_use","id":"run-call-1","name":"run","input":{}}]},` +
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"run-call-1","content":"ok"}]}` +
				`]}`,
			assert: assertLeadingFunctionHistory,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read request body: %v", errRead)
					return
				}
				captured <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
			}))
			defer server.Close()

			executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
			auth := testAntigravityAuth(server.URL)
			auth.Metadata["project_id"] = "project-1"
			result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "gemini-3.6-flash-high",
				Payload: []byte(tt.payload),
			}, cliproxyexecutor.Options{
				SourceFormat:   tt.format,
				ResponseFormat: tt.format,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
			}
			tt.assert(t, <-captured)
		})
	}
}

func TestAntigravityStreamPrependsLeadingUserForIssue4959ResponsesHistory(t *testing.T) {
	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		captured <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
	}))
	defer server.Close()

	executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := testAntigravityAuth(server.URL)
	auth.Metadata["project_id"] = "project-1"
	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.7-flash-high",
		Payload: issue4959ResponsesModelFirstPayload(),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatOpenAIResponse,
		ResponseFormat: sdktranslator.FormatOpenAIResponse,
		Stream:         true,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}
	assertIssue4959LeadingUserContents(t, gjson.GetBytes(<-captured, "request.contents").Array())
}

func TestAntigravityStreamPrependsLeadingUserAfterReplayInsertsFunctionCall(t *testing.T) {
	cache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(cache.ClearAntigravityReasoningReplayCache)

	const sessionID = "replay-insert-at-zero"
	const nativeID = "call-1"
	const nativeArgs = `{}`
	clientID := util.GeminiClaudeToolUseID(nativeID, "run", nativeArgs)
	item := []byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"call_id":"` + nativeID + `","name":"run","args":` + nativeArgs + `,"thoughtSignature":"replay-inserted-call-signature-123456"}`)
	if !cache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "responses:"+sessionID, [][]byte{item}) {
		t.Fatal("failed to cache omitted function call")
	}

	captured := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Errorf("read request body: %v", errRead)
			return
		}
		captured <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
	}))
	defer server.Close()

	payload := []byte(`{"model":"gemini-3.6-flash-high","session_id":"` + sessionID + `","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + clientID + `","content":"ok"}]}],"tools":[{"name":"run","input_schema":{"type":"object"}}]}`)
	executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	auth := testAntigravityAuth(server.URL)
	auth.Metadata["project_id"] = "project-1"
	result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gemini-3.6-flash-high",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		Stream:          true,
		OriginalRequest: payload,
	})
	if errExecute != nil {
		t.Fatalf("ExecuteStream() error = %v", errExecute)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
	}

	body := <-captured
	contents := gjson.GetBytes(body, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("contents len = %d, want 3; body=%s", len(contents), body)
	}
	leadingText := contents[0].Get("parts.0.text")
	if contents[0].Get("role").String() != "user" || !leadingText.Exists() || leadingText.String() != "" {
		t.Fatalf("synthetic leading user missing after replay insert: %s", contents[0].Raw)
	}
	if contents[1].Get("role").String() != "model" || contents[1].Get("parts.0.functionCall.id").String() != "call-1" {
		t.Fatalf("replayed functionCall is not immediately after the synthetic user: %s", contents[1].Raw)
	}
	if !contentHasNamedPart(contents[2], "functionResponse", "run") {
		t.Fatalf("functionResponse missing or moved: %s", contents[2].Raw)
	}
}

func TestAntigravityStreamDoesNotPrependLeadingUserForClaudeTarget(t *testing.T) {
	tests := []struct {
		name    string
		format  sdktranslator.Format
		payload string
	}{
		{
			name:   "Gemini model-first history",
			format: sdktranslator.FormatGemini,
			payload: `{"contents":[` +
				`{"role":"model","parts":[{"text":"prior answer"}]},` +
				`{"role":"user","parts":[{"text":"continue"}]}` +
				`]}`,
		},
		{
			name:   "OpenAI Responses assistant-first history",
			format: sdktranslator.FormatOpenAIResponse,
			payload: `{"input":[` +
				`{"type":"message","role":"assistant","content":[{"type":"output_text","text":"prior answer"}]},` +
				`{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}` +
				`]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			captured := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Errorf("read request body: %v", errRead)
					return
				}
				captured <- body
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}}\n\n"))
			}))
			defer server.Close()

			executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
			auth := testAntigravityAuth(server.URL)
			auth.Metadata["project_id"] = "project-1"
			result, errExecute := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-sonnet-4-6",
				Payload: []byte(tt.payload),
			}, cliproxyexecutor.Options{
				SourceFormat:   tt.format,
				ResponseFormat: tt.format,
				Stream:         true,
			})
			if errExecute != nil {
				t.Fatalf("ExecuteStream() error = %v", errExecute)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream chunk error = %v", chunk.Err)
				}
			}

			body := <-captured
			contents := gjson.GetBytes(body, "request.contents").Array()
			if len(contents) != 2 || contents[0].Get("role").String() != "model" || contents[1].Get("role").String() != "user" {
				t.Fatalf("Claude target history changed: %s", body)
			}
			if got := contents[0].Get("parts.0.text").String(); got != "prior answer" {
				t.Fatalf("first Claude model turn = %q, want prior answer; body=%s", got, body)
			}
		})
	}
}

func TestAntigravityRequestPathsDoNotFallbackEndpoints(t *testing.T) {
	type upstreamCall struct {
		host string
		path string
	}
	routes := []struct {
		name     string
		kind     string
		model    string
		wantPath string
	}{
		{name: "generate non-stream", kind: "execute", model: "gemini-3.6-flash-high", wantPath: antigravityGeneratePath},
		{name: "Claude non-stream", kind: "execute", model: "claude-sonnet-4-6", wantPath: antigravityStreamPath},
		{name: "generate stream", kind: "stream", model: "gemini-3.6-flash-high", wantPath: antigravityStreamPath},
		{name: "count tokens", kind: "count", model: "gemini-3.6-flash-high", wantPath: antigravityCountTokensPath},
	}
	failures := []struct {
		name      string
		transport bool
	}{
		{name: "HTTP 429"},
		{name: "transport error", transport: true},
	}

	for _, route := range routes {
		for _, failure := range failures {
			t.Run(route.name+"/"+failure.name, func(t *testing.T) {
				calls := make([]upstreamCall, 0, 1)
				transportErr := errors.New("daily endpoint unavailable")
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					calls = append(calls, upstreamCall{host: req.URL.Host, path: req.URL.Path})
					if failure.transport {
						return nil, transportErr
					}
					return &http.Response{
						StatusCode: http.StatusTooManyRequests,
						Header:     http.Header{"Content-Type": []string{"application/json"}},
						Body:       io.NopCloser(strings.NewReader(`{"error":{"code":429,"status":"RESOURCE_EXHAUSTED"}}`)),
					}, nil
				}))
				auth := &cliproxyauth.Auth{Metadata: map[string]any{
					"access_token": "token",
					"expired":      time.Now().Add(2 * time.Hour).Format(time.RFC3339),
					"project_id":   "project-1",
				}}
				req := cliproxyexecutor.Request{
					Model:   route.model,
					Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat:   sdktranslator.FormatGemini,
					ResponseFormat: sdktranslator.FormatGemini,
				}
				executor := NewAntigravityExecutor(&config.Config{})

				var errRequest error
				switch route.kind {
				case "execute":
					_, errRequest = executor.Execute(ctx, auth, req, opts)
				case "stream":
					_, errRequest = executor.ExecuteStream(ctx, auth, req, opts)
				case "count":
					_, errRequest = executor.CountTokens(ctx, auth, req, opts)
				default:
					t.Fatalf("unknown route kind %q", route.kind)
				}
				if errRequest == nil {
					t.Fatal("request error = nil, want original upstream error")
				}
				if failure.transport {
					if !errors.Is(errRequest, transportErr) {
						t.Fatalf("request error = %v, want transport error", errRequest)
					}
				} else {
					status, ok := errRequest.(interface{ StatusCode() int })
					if !ok || status.StatusCode() != http.StatusTooManyRequests {
						t.Fatalf("request error = %v, want status 429", errRequest)
					}
				}
				if len(calls) != 1 {
					t.Fatalf("upstream calls = %v, want exactly one daily endpoint request", calls)
				}
				if got, want := calls[0].host, resolveHost(antigravityBaseURLDaily); got != want {
					t.Fatalf("upstream host = %q, want %q", got, want)
				}
				if got := calls[0].path; got != route.wantPath {
					t.Fatalf("upstream path = %q, want %q", got, route.wantPath)
				}
			})
		}
	}
}

func TestAntigravityCountTokensMatchesTargetLeadingUserPolicy(t *testing.T) {
	tests := []struct {
		name      string
		model     string
		wantRoles string
	}{
		{name: "Gemini target prepends user", model: "gemini-3.6-flash-high", wantRoles: "user,model,user"},
		{name: "Claude target preserves history", model: "claude-sonnet-4-6", wantRoles: "model,user"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var upstreamBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != antigravityCountTokensPath {
					t.Fatalf("path = %q, want %q", r.URL.Path, antigravityCountTokensPath)
				}
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					t.Fatalf("read countTokens body: %v", errRead)
				}
				upstreamBody = append([]byte(nil), body...)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"totalTokens":42}`))
			}))
			defer server.Close()

			executor := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
			payload := []byte(`{"contents":[` +
				`{"role":"model","parts":[{"text":"prior output"}]},` +
				`{"role":"user","parts":[{"text":"continue"}]}` +
				`]}`)
			_, errCount := executor.CountTokens(context.Background(), testAntigravityAuth(server.URL), cliproxyexecutor.Request{
				Model:   tt.model,
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat:   sdktranslator.FormatGemini,
				ResponseFormat: sdktranslator.FormatGemini,
			})
			if errCount != nil {
				t.Fatalf("CountTokens() error = %v", errCount)
			}

			contents := gjson.GetBytes(upstreamBody, "request.contents").Array()
			roles := make([]string, 0, len(contents))
			for _, content := range contents {
				roles = append(roles, content.Get("role").String())
			}
			if got := strings.Join(roles, ","); got != tt.wantRoles {
				t.Fatalf("countTokens roles = %q, want %q; body=%s", got, tt.wantRoles, upstreamBody)
			}
			if strings.HasPrefix(tt.wantRoles, "user,") {
				text := contents[0].Get("parts.0.text")
				if !text.Exists() || text.String() != "" {
					t.Fatalf("synthetic countTokens user missing: %s", upstreamBody)
				}
			}
		})
	}
}

func TestAntigravityExecutorCountTokensSanitizesGeminiToolHistory(t *testing.T) {
	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	encoded := protowire.AppendTag(nil, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, inner)
	nativeSignature := base64.StdEncoding.EncodeToString(encoded)

	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != antigravityCountTokensPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, antigravityCountTokensPath)
		}
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read countTokens body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":42}`))
	}))
	defer server.Close()

	payload := []byte(`{"model":"gemini-3.6-flash-high","messages":[{"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"read","input":{"file":"one"},"signature":"` + nativeSignature + `"},{"type":"tool_use","id":"call-2","name":"read","input":{"file":"two"},"signature":"skip_thought_signature_validator"}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-2","content":"two"},{"type":"tool_result","tool_use_id":"call-1","content":"one"}]}]}`)
	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	_, errCount := exec.CountTokens(context.Background(), testAntigravityAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gemini-3.6-flash-high",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: payload,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if len(upstreamBody) == 0 {
		t.Fatal("countTokens upstream body was not captured")
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.1.parts.0.thoughtSignature").String(); got != nativeSignature {
		t.Fatalf("first call signature = %q, want native signature; body=%s", got, upstreamBody)
	}
	if signature := gjson.GetBytes(upstreamBody, "request.contents.1.parts.1.thoughtSignature"); signature.Exists() {
		t.Fatalf("second sibling bypass was not removed: %s", upstreamBody)
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.2.role").String(); got != "model" {
		t.Fatalf("functionResponse role = %q, want model; body=%s", got, upstreamBody)
	}
	if got := gjson.GetBytes(upstreamBody, "request.contents.2.parts.0.functionResponse.id").String(); got != "call-1" {
		t.Fatalf("first functionResponse.id = %q, want call-1; body=%s", got, upstreamBody)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(upstreamBody); errPairing != nil {
		t.Fatalf("countTokens tool history is invalid: %v; body=%s", errPairing, upstreamBody)
	}
}

func TestAntigravityExecutorCountTokensReconstructsCompactedClaudeToolCall(t *testing.T) {
	cache.ClearAntigravityReasoningReplayCache()
	t.Cleanup(cache.ClearAntigravityReasoningReplayCache)

	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	encoded := protowire.AppendTag(nil, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, inner)
	nativeSignature := base64.StdEncoding.EncodeToString(encoded)
	const nativeID = "native-count-token-call"
	const nativeArgs = `{"command":"true"}`
	clientID := util.GeminiClaudeToolUseID(nativeID, "Bash", nativeArgs)
	item := []byte(`{"type":"function_call_part","contentIndex":0,"partIndex":0,"targetOccurrence":0,"call_id":"` + nativeID + `","name":"Bash","args":` + nativeArgs + `,"thoughtSignature":"` + nativeSignature + `"}`)
	if !cache.CacheAntigravityReasoningReplayItems("gemini-3.6-flash-high", "responses:count-token-replay", [][]byte{item}) {
		t.Fatal("failed to cache native tool provenance")
	}

	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read countTokens body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens":42}`))
	}))
	defer server.Close()

	payload := []byte(`{"model":"gemini-3.6-flash-high","session_id":"count-token-replay","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + clientID + `","content":"ok"}]}],"tools":[{"name":"Bash","input_schema":{"type":"object","properties":{"command":{"type":"string"}}}}]}`)
	exec := NewAntigravityExecutor(&config.Config{RequestRetry: 1})
	_, errCount := exec.CountTokens(context.Background(), testAntigravityAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gemini-3.6-flash-high",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		ResponseFormat:  sdktranslator.FormatClaude,
		OriginalRequest: payload,
	})
	if errCount != nil {
		t.Fatalf("CountTokens() error = %v", errCount)
	}
	if len(upstreamBody) == 0 {
		t.Fatal("countTokens upstream body was not captured")
	}
	leadingText := gjson.GetBytes(upstreamBody, "request.contents.0.parts.0.text")
	if gjson.GetBytes(upstreamBody, "request.contents.0.role").String() != "user" || !leadingText.Exists() || leadingText.String() != "" {
		t.Fatalf("synthetic leading user missing after replay insert: %s", upstreamBody)
	}
	call := gjson.GetBytes(upstreamBody, "request.contents.1.parts.0")
	if call.Get("functionCall.id").String() != nativeID || call.Get("functionCall.name").String() != "Bash" || call.Get("thoughtSignature").String() != nativeSignature {
		t.Fatalf("native function call provenance was not reconstructed: %s", upstreamBody)
	}
	response := gjson.GetBytes(upstreamBody, "request.contents.2.parts.0.functionResponse")
	if response.Get("id").String() != nativeID || response.Get("name").String() != "Bash" {
		t.Fatalf("native function response provenance was not reconstructed: %s", upstreamBody)
	}
	if strings.Contains(string(upstreamBody), clientID) {
		t.Fatalf("Claude opaque provenance ID leaked upstream: %s", upstreamBody)
	}
	if errPairing := internalsignature.ValidateGeminiFunctionCallPairing(upstreamBody); errPairing != nil {
		t.Fatalf("countTokens compacted tool history is invalid: %v; body=%s", errPairing, upstreamBody)
	}
}

func TestNormalizeAntigravityGeminiFunctionResponseRolesOrdersMixedParallelResponses(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read","args":{"file":"one"}}},{"functionCall":{"id":"call-2","name":"read","args":{"file":"two"}}}]},{"role":"user","parts":[{"text":"results follow"},{"functionResponse":{"id":"call-2","name":"read","response":{"result":"two"}}},{"text":"continue"},{"functionResponse":{"id":"call-1","name":"read","response":{"result":"one"}}}]}]}}`)
	if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(payload); errValidate == nil {
		t.Fatal("permuted input unexpectedly passed function call pairing validation")
	}
	output := normalizeAntigravityGeminiFunctionResponseRoles(payload)
	if got := gjson.GetBytes(output, "request.contents.1.role").String(); got != "user" {
		t.Fatalf("mixed functionResponse/user content role = %q, want user; output=%s", got, output)
	}
	parts := gjson.GetBytes(output, "request.contents.1.parts").Array()
	if len(parts) != 4 {
		t.Fatalf("mixed response parts = %d, want 4; output=%s", len(parts), output)
	}
	if got := parts[0].Get("functionResponse.id").String(); got != "call-1" {
		t.Fatalf("first functionResponse.id = %q, want call-1; output=%s", got, output)
	}
	if got := parts[1].Get("functionResponse.id").String(); got != "call-2" {
		t.Fatalf("second functionResponse.id = %q, want call-2; output=%s", got, output)
	}
	if got := parts[2].Get("text").String(); got != "results follow" {
		t.Fatalf("first trailing text = %q; output=%s", got, output)
	}
	if got := parts[3].Get("text").String(); got != "continue" {
		t.Fatalf("second trailing text = %q; output=%s", got, output)
	}
	if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(output); errValidate != nil {
		t.Fatalf("normalized mixed parallel responses are invalid: %v; output=%s", errValidate, output)
	}
}

func TestNormalizeAntigravityGeminiFunctionResponseRolesOrdersParallelResponses(t *testing.T) {
	payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read","args":{"file":"one"}}},{"functionCall":{"id":"call-2","name":"read","args":{"file":"two"}}}]},{"role":" Model ","parts":[{"functionResponse":{"id":"call-2","name":"read","response":{"result":"two"}}},{"functionResponse":{"id":"call-1","name":"read","response":{"result":"one"}}}]}]}}`)
	output := normalizeAntigravityGeminiFunctionResponseRoles(payload)
	if got := gjson.GetBytes(output, "request.contents.1.role").String(); got != "model" {
		t.Fatalf("functionResponse role = %q, want model; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "request.contents.1.parts.0.functionResponse.id").String(); got != "call-1" {
		t.Fatalf("first functionResponse.id = %q, want call-1; output=%s", got, output)
	}
	if got := gjson.GetBytes(output, "request.contents.1.parts.1.functionResponse.id").String(); got != "call-2" {
		t.Fatalf("second functionResponse.id = %q, want call-2; output=%s", got, output)
	}
	if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(output); errValidate != nil {
		t.Fatalf("normalized parallel responses are invalid: %v; output=%s", errValidate, output)
	}
}

func TestNormalizeAntigravityGeminiFunctionResponseRolesDoesNotCrossEmptyContentBoundary(t *testing.T) {
	for _, boundary := range []string{
		`{"role":"user","parts":[]}`,
		`{"role":"user"}`,
		`{"role":"user","parts":null}`,
	} {
		payload := []byte(`{"request":{"contents":[{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"read","args":{}}},{"functionCall":{"id":"call-2","name":"read","args":{}}}]},` + boundary + `,{"role":"user","parts":[{"functionResponse":{"id":"call-2","name":"read","response":{"result":"two"}}},{"functionResponse":{"id":"call-1","name":"read","response":{"result":"one"}}}]}]}}`)
		output := normalizeAntigravityGeminiFunctionResponseRoles(payload)
		if got := gjson.GetBytes(output, "request.contents.2.role").String(); got != "model" {
			t.Fatalf("pure functionResponse role = %q, want model; output=%s", got, output)
		}
		if got := gjson.GetBytes(output, "request.contents.2.parts.0.functionResponse.id").String(); got != "call-2" {
			t.Fatalf("response crossed content boundary %s and was reordered: first id=%q; output=%s", boundary, got, output)
		}
		if errValidate := internalsignature.ValidateGeminiFunctionCallPairing(output); errValidate == nil {
			t.Fatalf("responses crossing content boundary %s were accepted: %s", boundary, output)
		}
	}
}

func TestAntigravityExecutor_GeminiTargetPreservesGeminiThinkingCarrier(t *testing.T) {
	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	encoded := protowire.AppendTag(nil, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, inner)
	validSignature := base64.StdEncoding.EncodeToString(encoded)
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"answer"},{"type":"thinking","thinking":"","signature":"` + validSignature + `"},{"type":"thinking","thinking":"","signature":"invalid"}]}]}`)

	output, err := validateAntigravityRequestSignatures(context.Background(), "gemini-3.6-flash-high", sdktranslator.FormatClaude, payload)
	if err != nil {
		t.Fatalf("validateAntigravityRequestSignatures() error = %v", err)
	}
	content := gjson.GetBytes(output, "messages.0.content").Array()
	if len(content) != 2 {
		t.Fatalf("content length = %d, want text plus valid Gemini carrier: %s", len(content), output)
	}
	if got := content[1].Get("signature").String(); got != validSignature {
		t.Fatalf("preserved signature = %q, want Gemini carrier", got)
	}
}

func TestAntigravityExecutor_StrictBypassStripsInvalidSignature(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	previousStrict := cache.SignatureBypassStrictMode()
	cache.SetSignatureCacheEnabled(false)
	cache.SetSignatureBypassStrictMode(true)
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previousCache)
		cache.SetSignatureBypassStrictMode(previousStrict)
	})

	payload := invalidClaudeThinkingPayload()
	from := sdktranslator.FromString("claude")

	output, err := validateAntigravityRequestSignatures(context.Background(), "claude-sonnet-4-5-thinking", from, payload)
	if err != nil {
		t.Fatalf("strict bypass should strip invalid signatures instead of rejecting request: %v", err)
	}
	parts := gjson.GetBytes(output, "messages.0.content").Array()
	if len(parts) != 1 {
		t.Fatalf("content length = %d, want 1 after invalid thinking strip: %s", len(parts), output)
	}
	if got := parts[0].Get("type").String(); got != "text" {
		t.Fatalf("remaining part type = %q, want text: %s", got, output)
	}
}

func TestAntigravityExecutor_StrictBypassLogsStrippedInvalidSignature(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	previousStrict := cache.SignatureBypassStrictMode()
	cache.SetSignatureCacheEnabled(false)
	cache.SetSignatureBypassStrictMode(true)
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previousCache)
		cache.SetSignatureBypassStrictMode(previousStrict)
	})

	hook := newSignatureDebugHook(t)
	rawSignature := testFakeClaudeSignature()
	payload := []byte(`{
		"model": "claude-sonnet-4-5-thinking",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "bad", "signature": "` + rawSignature + `"},
					{"type": "text", "text": "hello"}
				]
			}
		]
	}`)
	from := sdktranslator.FromString("claude")

	if _, err := validateAntigravityRequestSignatures(context.Background(), "claude-sonnet-4-5-thinking", from, payload); err != nil {
		t.Fatalf("strict bypass should strip invalid signatures instead of rejecting request: %v", err)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.DebugLevel {
			continue
		}
		if entry.Data["component"] != "signature_sanitizer" ||
			entry.Data["executor"] != "antigravity" ||
			entry.Data["action"] != "drop_thinking_blocks" ||
			entry.Data["stage"] != "strict_bypass" {
			continue
		}
		if entry.Data["count"] != 1 {
			t.Fatalf("debug drop count = %v, want 1", entry.Data["count"])
		}
		found = true
	}
	if !found {
		t.Fatal("expected debug log for stripped Antigravity Claude thinking signature")
	}
	assertSignatureDebugDoesNotLeak(t, hook, rawSignature)
}

func TestClaudeExecutor_LogsSanitizedClaudeUpstreamSignatures(t *testing.T) {
	hook := newSignatureDebugHook(t)
	rawSignature := "skip_thought_signature_validator"
	body := []byte(`{
		"model": "claude-sonnet-4-5",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "bad", "signature": "` + rawSignature + `"},
					{"type": "text", "text": "hello"},
					{"type": "tool_use", "id": "call_123", "name": "get_weather", "input": {}, "signature": "` + rawSignature + `"}
				]
			}
		]
	}`)

	output := sanitizeClaudeMessagesForClaudeUpstreamWithDebug(context.Background(), body, "claude-sonnet-4-5")
	parts := gjson.GetBytes(output, "messages.0.content").Array()
	if len(parts) != 2 {
		t.Fatalf("content length = %d, want 2 after invalid thinking strip: %s", len(parts), output)
	}
	if parts[1].Get("signature").Exists() {
		t.Fatalf("tool_use signature should be removed before Claude upstream: %s", output)
	}

	found := false
	for _, entry := range hook.AllEntries() {
		if entry.Level != log.DebugLevel {
			continue
		}
		if entry.Data["component"] != "signature_sanitizer" ||
			entry.Data["executor"] != "claude" ||
			entry.Data["action"] != "sanitize_claude_messages" {
			continue
		}
		if entry.Data["dropped_blocks"] != 1 {
			t.Fatalf("dropped_blocks = %v, want 1", entry.Data["dropped_blocks"])
		}
		if entry.Data["dropped_signatures"] != 1 {
			t.Fatalf("dropped_signatures = %v, want 1", entry.Data["dropped_signatures"])
		}
		found = true
	}
	if !found {
		t.Fatal("expected debug log for Claude upstream signature sanitization")
	}
	assertSignatureDebugDoesNotLeak(t, hook, rawSignature)
}

func TestAntigravityExecutor_NonStrictBypassSkipsPrecheck(t *testing.T) {
	previousCache := cache.SignatureCacheEnabled()
	previousStrict := cache.SignatureBypassStrictMode()
	cache.SetSignatureCacheEnabled(false)
	cache.SetSignatureBypassStrictMode(false)
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previousCache)
		cache.SetSignatureBypassStrictMode(previousStrict)
	})

	payload := invalidClaudeThinkingPayload()
	from := sdktranslator.FromString("claude")

	_, err := validateAntigravityRequestSignatures(context.Background(), "claude-sonnet-4-5-thinking", from, payload)
	if err != nil {
		t.Fatalf("non-strict bypass should skip precheck, got: %v", err)
	}
}

func TestAntigravityExecutor_CacheModeSkipsPrecheck(t *testing.T) {
	previous := cache.SignatureCacheEnabled()
	cache.SetSignatureCacheEnabled(true)
	t.Cleanup(func() {
		cache.SetSignatureCacheEnabled(previous)
	})

	payload := invalidClaudeThinkingPayload()
	from := sdktranslator.FromString("claude")

	_, err := validateAntigravityRequestSignatures(context.Background(), "claude-sonnet-4-5-thinking", from, payload)
	if err != nil {
		t.Fatalf("cache mode should skip precheck, got: %v", err)
	}
}
