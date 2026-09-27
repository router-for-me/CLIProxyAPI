package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

type opencodeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f opencodeRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

const opencodeChatFixture = `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`

func opencodeTestAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		Provider:   "opencode",
		Attributes: map[string]string{},
		Metadata:   map[string]any{"api_key": "sk-test", "access_token": "sk-test"},
	}
}

func TestOpencodeUpstreamRoute(t *testing.T) {
	if got := opencodeUpstreamRoute("minimax-m2.5"); got != "anthropic" {
		t.Fatalf("minimax route = %q", got)
	}
	if got := opencodeUpstreamRoute("qwen3.8-flash"); got != "anthropic" {
		t.Fatalf("qwen flash route = %q", got)
	}
	if got := opencodeUpstreamRoute("gpt-5.6-luna"); got != "responses" {
		t.Fatalf("luna route = %q", got)
	}
	if got := opencodeUpstreamRoute("glm-5.2"); got != "chat" {
		t.Fatalf("glm route = %q", got)
	}
	if got := registry.OpencodeUpstreamRoute("glm-5.2(high)"); got != "chat" {
		t.Fatalf("suffixed route = %q", got)
	}
}

func TestOpencodeChatRoundTrip(t *testing.T) {
	var upstreamURL, authHeader, uaHeader, upstreamModel string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		authHeader = req.Header.Get("Authorization")
		uaHeader = req.Header.Get("User-Agent")
		body, _ := io.ReadAll(req.Body)
		upstreamModel = gjson.GetBytes(body, "model").String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Fatalf("upstreamURL = %q", upstreamURL)
	}
	if authHeader != "Bearer sk-test" {
		t.Fatalf("Authorization = %q", authHeader)
	}
	if !strings.HasPrefix(uaHeader, "CLIProxyAPI/") {
		t.Fatalf("User-Agent = %q, want CLIProxyAPI/", uaHeader)
	}
	if upstreamModel != "glm-5.2" {
		t.Fatalf("upstream model = %q", upstreamModel)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "hello" {
		t.Fatalf("response text = %q", got)
	}
}

func TestOpencodeClaudeSourceTranslates(t *testing.T) {
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	// Claude source on a chat-route model goes out as OpenAI upstream.
	if got := gjson.GetBytes(upstreamBody, "messages.0.role").String(); got != "user" {
		t.Fatalf("upstream role = %q", got)
	}
	if got := gjson.GetBytes(resp.Payload, "content.0.text").String(); got != "hello" {
		t.Fatalf("claude response text = %q (payload %s)", got, resp.Payload)
	}
}

func TestOpencodeNoToolChoiceOnDeepseek(t *testing.T) {
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	_, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash",
		Payload: []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gjson.GetBytes(upstreamBody, "tool_choice").Exists() {
		t.Fatalf("tool_choice should be stripped for deepseek lanes")
	}
	if got := gjson.GetBytes(upstreamBody, "tools.0.type").String(); got != "function" {
		t.Fatalf("tools type = %q", got)
	}
}

func TestOpencodeToolChoiceKeptOnVisionExp(t *testing.T) {
	// The vision-exp lane explicitly keeps tool_choice: the explicit lane set
	// must not over-strip the way substring matching would.
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	_, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4-flash-vision-exp",
		Payload: []byte(`{"model":"deepseek-v4-flash-vision-exp","messages":[{"role":"user","content":"hi"}],"tool_choice":"auto","tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(upstreamBody, "tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want preserved auto on vision-exp", got)
	}
}

func TestOpencodeMissingKeyUnauthorized(t *testing.T) {
	executor := NewOpenCodeExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Provider: "opencode"}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err == nil {
		t.Fatalf("expected unauthorized without key")
	}
}

func TestOpencodeForwardsIncomingSessionHeader(t *testing.T) {
	var upstreamSession string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamSession = req.Header.Get("x-opencode-session")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	headers := http.Header{}
	headers.Set("x-opencode-session", "sess-downstream-123")
	_, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Headers: headers})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamSession != "sess-downstream-123" {
		t.Fatalf("x-opencode-session = %q, want passthrough sess-downstream-123", upstreamSession)
	}
}

func TestOpencodeSynthesizesSessionHeader(t *testing.T) {
	var upstreamSession string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamSession = req.Header.Get("x-opencode-session")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	headers := http.Header{}
	headers.Set("X-Claude-Code-Session-Id", "claude-sess-456")
	_, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "glm-5.2",
		Payload: []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Headers: headers})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamSession != "claude:claude-sess-456" {
		t.Fatalf("x-opencode-session = %q, want canonical claude:claude-sess-456 fallback", upstreamSession)
	}
}

const opencodeResponsesFixture = `{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-5.6-luna","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8}}`

func TestOpencodeResponsesSourceOnChatRouteTranslates(t *testing.T) {
	// Regression: Responses input on chat-route lanes went to /responses
	// natively, which the Zen gateway rejects with ModelProtocolUnsupported
	// (Codex clients could not use any chat-route model, while Claude Code
	// worked via the translated chat path). Chat-route models must ride the
	// translated chat path for every source format.
	var upstreamURL string
	var upstreamBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		var err error
		upstreamBody, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeChatFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "mimo-v2.6-flash",
		Payload: []byte(`{"model":"mimo-v2.6-flash","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://opencode.ai/zen/go/v1/chat/completions" {
		t.Fatalf("chat-route responses input must ride /chat/completions, got %q", upstreamURL)
	}
	if got := gjson.GetBytes(upstreamBody, "messages.0.role").String(); got != "user" {
		t.Fatalf("translated role = %q (body %s)", got, upstreamBody)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty translated response")
	}
}

func TestOpencodeResponsesRouteStaysNative(t *testing.T) {
	// Responses-native lanes keep the native /responses passthrough.
	var upstreamURL string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", opencodeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		upstreamURL = req.URL.String()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(opencodeResponsesFixture)),
		}, nil
	}))

	executor := NewOpenCodeExecutor(&config.Config{})
	resp, err := executor.Execute(ctx, opencodeTestAuth(), cliproxyexecutor.Request{
		Model:   "gpt-5.6-luna",
		Payload: []byte(`{"model":"gpt-5.6-luna","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if upstreamURL != "https://opencode.ai/zen/go/v1/responses" {
		t.Fatalf("responses-native lane must stay on /responses, got %q", upstreamURL)
	}
	if len(resp.Payload) == 0 {
		t.Fatalf("empty passthrough response")
	}
}
