package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type zaiRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f zaiRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

const zaiChatFixture = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"glm-5.3-flash","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`

func zaiTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "zai",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"access_token": "ak-123.sk-456"},
	}
}

func TestZaiPrepareRequestSendsRawKey(t *testing.T) {
	executor := NewZaiExecutor(&config.Config{})
	req, _ := http.NewRequest(http.MethodPost, "https://api.z.ai/api/coding/paas/v4/chat/completions", nil)
	if err := executor.PrepareRequest(req, zaiTestAuth()); err != nil {
		t.Fatalf("PrepareRequest err = %v", err)
	}
	// Z.AI rejects the Bearer prefix: the key goes out verbatim.
	if got := req.Header.Get("Authorization"); got != "ak-123.sk-456" {
		t.Fatalf("Authorization = %q, want raw key", got)
	}
}

func TestZaiRequestToFormatByLane(t *testing.T) {
	executor := NewZaiExecutor(&config.Config{})
	openAIReq := cliproxyexecutor.Request{Model: "glm-5.3-flash"}
	if got := executor.RequestToFormat(openAIReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); got != sdktranslator.FormatOpenAI {
		t.Fatalf("coding lane = %v, want OpenAI", got)
	}
	claudeReq := cliproxyexecutor.Request{Model: "glm-5.3"}
	if got := executor.RequestToFormat(claudeReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); got != sdktranslator.FormatClaude {
		t.Fatalf("glm lane = %v, want Claude", got)
	}
	// No Responses endpoint exists: even Responses input reports the lane format.
	respReq := cliproxyexecutor.Request{Model: "glm-5.3-flash"}
	if got := executor.RequestToFormat(respReq, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}); got != sdktranslator.FormatOpenAI {
		t.Fatalf("responses input on coding lane = %v, want OpenAI", got)
	}
}

func TestZaiOpenAIRoundTrip(t *testing.T) {
	var upstreamURL, authHeader, upstreamModel string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", zaiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		authHeader = req.Header.Get("Authorization")
		body, _ := io.ReadAll(req.Body)
		upstreamModel = gjson.GetBytes(body, "model").String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(zaiChatFixture)),
		}, nil
	}))

	executor := NewZaiExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, zaiTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://api.z.ai/api/coding/paas/v4/chat/completions" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if authHeader != "ak-123.sk-456" {
		t.Fatalf("Authorization = %q, want raw key without Bearer", authHeader)
	}
	if upstreamModel != "glm-5.3-flash" {
		t.Fatalf("upstream model = %q", upstreamModel)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("response text = %q", got)
	}
}

func TestZaiResponsesInputTranslatesToChat(t *testing.T) {
	var upstreamURL string
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", zaiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(zaiChatFixture)),
		}, nil
	}))

	executor := NewZaiExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, zaiTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://api.z.ai/api/coding/paas/v4/chat/completions" {
		t.Fatalf("responses input must ride chat completions (no zai responses endpoint), got %q", upstreamURL)
	}
	if got := gjson.GetBytes(upstreamBody, "messages.0.role").String(); got != "user" {
		t.Fatalf("translated role = %q (body %s)", got, upstreamBody)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty translated response")
	}
}

const zaiClaudeFixture = `{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.3","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":3}}`

func zaiStampedAuth() *cliproxyauth.Auth {
	// Mirrors a persisted login record: base_url stamped with the Anthropic base.
	return &cliproxyauth.Auth{
		Provider:   "zai",
		Attributes: map[string]string{"base_url": "https://api.z.ai/api/anthropic", "auth_kind": "oauth"},
		Metadata:   map[string]any{"access_token": "ak-123.sk-456"},
	}
}

func TestZaiOpenAILaneIgnoresAnthropicStamp(t *testing.T) {
	// Regression: the coding lane must not POST to .../api/anthropic/chat/completions.
	var upstreamURL string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", zaiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(zaiChatFixture)),
		}, nil
	}))

	executor := NewZaiExecutor(&config.Config{})
	_, err := executor.Execute(ctx, zaiStampedAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.3-flash",
		Payload: []byte(`{"model":"glm-5.3-flash","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://api.z.ai/api/coding/paas/v4/chat/completions" {
		t.Fatalf("upstreamURL = %q, want the coding lane", upstreamURL)
	}
}

func TestZaiClaudeLaneSendsVerbatimKey(t *testing.T) {
	// Regression: delegation stamped Bearer on non-Anthropic hosts, which Z.AI
	// rejects. The native lane must send the key verbatim to /v1/messages —
	// the bare /messages path answers HTTP 200 with a JSON error envelope
	// ({"code":500,"msg":"404 NOT_FOUND",...}), which clients read as an
	// empty/malformed response.
	var upstreamURL, authHeader string
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", zaiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		authHeader = req.Header.Get("Authorization")
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(zaiClaudeFixture)),
		}, nil
	}))

	executor := NewZaiExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, zaiStampedAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.3",
		Payload: []byte(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://api.z.ai/api/anthropic/v1/messages" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if authHeader != "ak-123.sk-456" {
		t.Fatalf("Authorization = %q, want raw key without Bearer", authHeader)
	}
	if got := gjson.GetBytes(upstreamBody, "model").String(); got != "glm-5.3" {
		t.Fatalf("upstream model = %q", got)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty translated response")
	}
}

func TestZaiClaudeSourceStaysNative(t *testing.T) {
	var upstreamURL, authHeader string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", zaiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		authHeader = req.Header.Get("Authorization")
		_, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(zaiClaudeFixture)),
		}, nil
	}))

	executor := NewZaiExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, zaiStampedAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.3",
		Payload: []byte(`{"model":"glm-5.3","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://api.z.ai/api/anthropic/v1/messages" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if authHeader != "ak-123.sk-456" {
		t.Fatalf("Authorization = %q, want raw key", authHeader)
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.text").String(); got != "hello" {
		t.Fatalf("claude response text = %q (payload %s)", got, resp.Payload)
	}
}

const zaiClaudeStreamFixture = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"glm-5.3\",\"content\":[],\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n" +
	"\n" +
	"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n" +
	"\n" +
	"event: message_stop\ndata: {\"type\":\"message_stop\"}\n" +
	"\n"

func TestZaiClaudeStreamPreservesSSEFraming(t *testing.T) {
	// Regression: the Claude lane streamed bare scanner lines — the SSE
	// newline separators were stripped by the reader and never rebuilt, so
	// clients concatenated "event: ...data: {...}" into one line. Native
	// passthrough must reassemble complete events like ClaudeExecutor does.
	var upstreamURL string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", zaiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(zaiClaudeStreamFixture)),
		}, nil
	}))

	executor := NewZaiExecutor(&config.Config{})
	result, err := executor.ExecuteStream(ctx, zaiStampedAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.3",
		Payload: []byte(`{"model":"glm-5.3","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	if upstreamURL != "https://api.z.ai/api/anthropic/v1/messages" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	var joined []byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error = %v", chunk.Err)
		}
		joined = append(joined, chunk.Payload...)
	}
	joinedStr := string(joined)
	if strings.Contains(joinedStr, "data: {") && !strings.Contains(joinedStr, "\ndata: {") {
		t.Fatalf("SSE data lines are not newline-separated (framing lost): %q", joinedStr)
	}
	for _, want := range []string{"event: message_start\n", "event: message_stop\n"} {
		if !strings.Contains(joinedStr, want) {
			t.Fatalf("missing framed event %q in stream: %q", want, joinedStr)
		}
	}
	if strings.Contains(joinedStr, "[DONE]") {
		t.Fatalf("claude lane must not emit OpenAI-style [DONE] sentinel: %q", joinedStr)
	}
}
