package executor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	qoderauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

type qoderTestRoundTripFunc func(*http.Request) (*http.Response, error)

func (f qoderTestRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func qoderTestAuth(id, provider string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID: id, Provider: provider, Label: "user@example.com",
		Metadata: map[string]any{
			"type": provider, "region": qoderauth.RegionGlobal, "access_token": "dt-test", "refresh_token": "drt-test",
			"user_id": "user-test", "name": "Test User", "email": "user@example.com", "machine_id": "machine-test",
			"expire_time": time.Now().Add(time.Hour).Unix(),
		},
		Attributes: map[string]string{"auth_kind": "oauth"},
	}
}

func cacheQoderTestModel(t *testing.T, authID string) {
	t.Helper()
	qoderauth.CacheCatalog(authID, &qoderauth.Catalog{
		Models: nil,
		Configs: map[string]json.RawMessage{
			"auto":             json.RawMessage(`{"key":"auto","display_name":"Auto","source":"system","format":"openai","max_input_tokens":180000,"max_output_tokens":32000,"is_vl":true,"is_reasoning":true,"thinking_config":{"disabled":{},"enabled":{"efforts":{"low":{},"high":{},"max":{}}}}}`),
			"configured-model": json.RawMessage(`{"key":"configured-model","source":"system","format":"openai"}`),
		},
	})
	t.Cleanup(func() { qoderauth.DropCatalog(authID) })
}

func qoderTestResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func qoderEnvelope(inner string) string {
	encoded, _ := json.Marshal(inner)
	return `{"statusCodeValue":200,"body":` + string(encoded) + `}`
}

func qoderTestStream() string {
	return "data: " + qoderEnvelope(`{"id":"chatcmpl-qoder","created":123,"choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`) + "\n\n" +
		"data: " + qoderEnvelope(`{"id":"chatcmpl-qoder","created":123,"choices":[{"index":0,"delta":{"content":"lo"}}]}`) + "\n\n" +
		"data: " + qoderEnvelope(`{"id":"chatcmpl-qoder","created":123,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`) + "\n\n" +
		"data: [DONE]\n\n"
}

func TestQoderExecuteUsesMetadataCredentialAndAggregatesUsage(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderGlobal)
	cacheQoderTestModel(t, auth.ID)
	var captured []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var err error
		captured, err = io.ReadAll(req.Body)
		if err != nil {
			t.Error(err)
		}
		if req.URL.Host != "api2.qoder.sh" || req.URL.Path != "/algo/api/v2/service/pro/sse/agent_chat_generation" {
			t.Errorf("upstream URL = %s", req.URL)
		}
		if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer COSY.") || req.Header.Get("X-Model-Key") != "auto" || req.Header.Get("Accept-Encoding") != "identity" {
			t.Errorf("signed headers = %v", req.Header)
		}
		return qoderTestResponse(req, http.StatusOK, qoderTestStream()), nil
	}))
	executor := NewQoderExecutor(&config.Config{})
	response, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model: "auto", Payload: []byte(`{"model":"auto","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"}],"max_tokens":100}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.content").String(); got != "Hello" {
		t.Fatalf("content = %q, response = %s", got, response.Payload)
	}
	if gjson.GetBytes(response.Payload, "usage.total_tokens").Int() != 7 {
		t.Fatalf("usage missing: %s", response.Payload)
	}
	decoded, err := decodeQoderExecutorBody(string(captured))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(decoded, "model_config.key").String() != "auto" || gjson.GetBytes(decoded, "parameters.max_tokens").Int() != 100 {
		t.Fatalf("business payload = %s", decoded)
	}
	if gjson.GetBytes(decoded, "system").String() != "be concise" || gjson.GetBytes(decoded, "messages.0.role").String() != "system" {
		t.Fatalf("system normalization = %s", decoded)
	}
}

func TestQoderPayloadRulesAreFinalBeforeEncodingAndSigning(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderGlobal)
	cacheQoderTestModel(t, auth.ID)
	cfg := &config.Config{Payload: config.PayloadConfig{
		Override: []config.PayloadRule{{
			Models: []config.PayloadModelRule{{Name: "*", Protocol: "qoder"}},
			Params: map[string]any{"parameters.max_tokens": 17, "parameters.reasoning_effort": "max", "model_config.key": "configured-model"},
		}},
		Filter: []config.PayloadFilterRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: []string{"business", "chat_context"}}},
	}}
	var captured []byte
	var signature string
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured, _ = io.ReadAll(req.Body)
		signature = req.Header.Get("Authorization")
		if req.Header.Get("X-Model-Key") != "configured-model" {
			t.Errorf("transport model was restored after payload rules: %q", req.Header.Get("X-Model-Key"))
		}
		return qoderTestResponse(req, http.StatusOK, qoderTestStream()), nil
	}))
	executor := NewQoderExecutor(cfg)
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model: "auto", Payload: []byte(`{"model":"auto","messages":[{"role":"user","content":"hello"}],"max_tokens":100}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeQoderExecutorBody(string(captured))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(decoded, "parameters.max_tokens").Int() != 17 || gjson.GetBytes(decoded, "parameters.reasoning_effort").String() != "max" || gjson.GetBytes(decoded, "model_config.key").String() != "configured-model" {
		t.Fatalf("override was overwritten: %s", decoded)
	}
	if gjson.GetBytes(decoded, "business").Exists() || gjson.GetBytes(decoded, "chat_context").Exists() {
		t.Fatalf("filtered fields were restored: %s", decoded)
	}
	if !strings.HasPrefix(signature, "Bearer COSY.") {
		t.Fatal("final encoded payload was not signed")
	}
}

func TestQoderRefreshWorksAfterFileReload(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderCN)
	auth.Metadata["region"] = qoderauth.RegionCN
	auth.Metadata["machine_type"] = "5"
	auth.Metadata["machine_code"] = "code-test"
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "openapi.qoder.com.cn" || req.URL.Path != "/api/v1/deviceToken/refresh" {
			t.Errorf("refresh URL = %s", req.URL)
		}
		body, _ := io.ReadAll(req.Body)
		if gjson.GetBytes(body, "refresh_token").String() != "drt-test" || gjson.GetBytes(body, "machine_id").String() != "machine-test" {
			t.Errorf("refresh body = %s", body)
		}
		return qoderTestResponse(req, http.StatusOK, `{"device_token":"dt-new","refresh_token":"drt-new","expires_at":"2030-01-01T00:00:00Z"}`), nil
	}))
	executor := NewQoderExecutor(&config.Config{})
	updated, err := executor.Refresh(ctx, auth)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Metadata["access_token"] != "dt-new" || updated.Metadata["refresh_token"] != "drt-new" || updated.Attributes["region"] != qoderauth.RegionCN {
		t.Fatalf("updated = %+v", updated)
	}
	if auth.Metadata["access_token"] != "dt-test" {
		t.Fatal("refresh mutated the shared auth snapshot")
	}
}

func TestQoderExecuteTranslatesClaudeResponse(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderGlobal)
	cacheQoderTestModel(t, auth.ID)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return qoderTestResponse(req, http.StatusOK, qoderTestStream()), nil
	}))
	executor := NewQoderExecutor(&config.Config{})
	response, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model: "auto", Payload: []byte(`{"model":"auto","max_tokens":100,"messages":[{"role":"user","content":"hello"}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(response.Payload, "content.0.text").String() != "Hello" {
		t.Fatalf("Claude response = %s", response.Payload)
	}
}

func TestQoderExecuteAggregatesToolCalls(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderGlobal)
	cacheQoderTestModel(t, auth.ID)
	stream := "data: " + qoderEnvelope(`{"id":"chatcmpl-tools","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]}}]}`) + "\n\n" +
		"data: " + qoderEnvelope(`{"id":"chatcmpl-tools","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"qoder\"}"}}]}}]}`) + "\n\n" +
		"data: " + qoderEnvelope(`{"id":"chatcmpl-tools","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`) + "\n\n" +
		"data: [DONE]\n\n"
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		decoded, err := decodeQoderExecutorBody(string(body))
		if err != nil {
			t.Error(err)
		} else if gjson.GetBytes(decoded, "tools.0.function.name").String() != "lookup" {
			t.Errorf("upstream tools = %s", decoded)
		}
		return qoderTestResponse(req, http.StatusOK, stream), nil
	}))
	executor := NewQoderExecutor(&config.Config{})
	response, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model: "auto", Payload: []byte(`{"model":"auto","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}}]}`),
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(response.Payload, "choices.0.message.tool_calls.0.function.arguments").String(); got != `{"q":"qoder"}` {
		t.Fatalf("tool arguments = %q, response = %s", got, response.Payload)
	}
	if gjson.GetBytes(response.Payload, "choices.0.finish_reason").String() != "tool_calls" {
		t.Fatalf("finish reason = %s", response.Payload)
	}
}

func TestQoderStreamRejectsIncompleteUpstream(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderGlobal)
	cacheQoderTestModel(t, auth.ID)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return qoderTestResponse(req, http.StatusOK, "data: "+qoderEnvelope(`{"choices":[{"delta":{"content":"partial"}}]}`)+"\n\n"), nil
	}))
	executor := NewQoderExecutor(&config.Config{})
	result, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{Model: "auto", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	var terminal error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			terminal = chunk.Err
		}
	}
	if terminal == nil || !strings.Contains(terminal.Error(), "before completion") {
		t.Fatalf("terminal error = %v", terminal)
	}
}

func TestQoderCNPayloadRulesUseRegionalProtocol(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderCN)
	auth.Metadata["region"] = qoderauth.RegionCN
	cacheQoderTestModel(t, auth.ID)
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "*", Protocol: "qoder-cn"}}, Params: map[string]any{"parameters.max_tokens": 19},
	}}}}
	var captured []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		captured, _ = io.ReadAll(req.Body)
		return qoderTestResponse(req, http.StatusOK, qoderTestStream()), nil
	}))
	executor := NewQoderExecutor(cfg)
	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "auto", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeQoderExecutorBody(string(captured))
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(decoded, "parameters.max_tokens").Int() != 19 {
		t.Fatalf("qoder-cn rule did not apply: %s", decoded)
	}
}

func TestQoderUnknownModelUsesLoadedCatalogWithoutRefetch(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderGlobal)
	cacheQoderTestModel(t, auth.ID)
	storage, err := qoderauth.CredentialFromAuth(auth)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("unexpected catalog request")
	}))
	executor := NewQoderExecutor(&config.Config{})
	if _, err := executor.resolveModelConfig(ctx, auth, storage, "unknown"); err == nil {
		t.Fatal("unknown model unexpectedly resolved")
	}
	if calls != 0 {
		t.Fatalf("catalog requests = %d, want 0", calls)
	}
}

func TestQoderStreamIgnoresNullData(t *testing.T) {
	auth := qoderTestAuth(t.Name(), qoderauth.ProviderGlobal)
	cacheQoderTestModel(t, auth.ID)
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", qoderTestRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		return qoderTestResponse(req, http.StatusOK, "data: null\n\n"+qoderTestStream()), nil
	}))
	executor := NewQoderExecutor(&config.Config{})
	response, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{Model: "auto", Payload: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatal(err)
	}
	if gjson.GetBytes(response.Payload, "choices.0.message.content").String() != "Hello" {
		t.Fatalf("response = %s", response.Payload)
	}
}

func decodeQoderExecutorBody(encoded string) ([]byte, error) {
	const custom = "_doRTgHZBKcGVjlvpC,@aFSx#DPuNJme&i*MzLOEn)sUrthbf%Y^w.(kIQyXqWA!"
	const standard = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	mapped := make([]byte, len(encoded))
	for index := range encoded {
		if encoded[index] == '$' {
			mapped[index] = '='
			continue
		}
		position := strings.IndexByte(custom, encoded[index])
		if position < 0 {
			return nil, fmt.Errorf("invalid encoded character %q", encoded[index])
		}
		mapped[index] = standard[position]
	}
	length := len(mapped)
	third := length / 3
	reordered := make([]byte, length)
	copy(reordered[:third], mapped[length-third:])
	copy(reordered[third:length-third], mapped[third:length-third])
	copy(reordered[length-third:], mapped[:third])
	return base64.StdEncoding.DecodeString(string(reordered))
}
