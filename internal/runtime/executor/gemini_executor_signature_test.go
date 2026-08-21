package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	internalsignature "github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
	"google.golang.org/protobuf/encoding/protowire"
)

const testClaudeCAISSample = "CAISqwIKiAEIEBgCKkBHRlRBsNiptQUWfPoOhuQKwi5LnncZVO9bB5jqOs76D7uBtgktML0zqJtNmLHXHHcgD6lk4MQu4QBXzFd1lbC3Mg5jbGF1ZGUtZmFibGUtNTgBQgh0aGlua2luZ1okZDk3NDM5NzUtNGJiMC00OTM2LTllMjgtZDViMGQyMWJkYzQ4EgxCGh+XVFFFeySAjtAaDL/A1LltGu6MMJ+eXSIwsN0oBpDrqLv22UBfkMnTotnIbkvkOyb9xZHgigG6OZVHaI3gThm+maLKmgO5PrFLKlDFYp+YZksy/wKwszJlnLTPzAK+NUlfzagOE1ymtZTXhAYK260XyFYmg/te/C231+Fr/hoX+EJoUBnrn0gD7hqMISOT+TaFEuOXYsN517GfaxgB"

func testNativeGemini3ThoughtSignature() string {
	inner := protowire.AppendTag(nil, 1, protowire.BytesType)
	inner = protowire.AppendBytes(inner, []byte{0x01, 0x0c, 0x39, 0xd6, 0xc7, 0x34})
	encoded := protowire.AppendTag(nil, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, inner)
	return base64.StdEncoding.EncodeToString(encoded)
}

func claudeRequestWithThinkingSignature(sig string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	req := cliproxyexecutor.Request{
		Model: "gemini-2.5-flash",
		Payload: []byte(`{
		"model": "claude-3-7-sonnet-20250219",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "thinking", "thinking": "Let me think...", "signature": "` + sig + `"},
					{"type": "text", "text": "Here is the response."}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "Follow up question."}
				]
			}
		]
	}`),
		Metadata: map[string]any{
			"cliproxy.resolved_api_key_model_info": &registry.ModelInfo{IsCompat: true},
		},
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatClaude,
	}
	return req, opts
}

func TestGeminiExecutorExecute_SanitizesClaudeCAISSignature(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-api-key",
			"base_url": server.URL,
		},
	}

	req, opts := claudeRequestWithThinkingSignature(testClaudeCAISSample)

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if bytes.Contains(upstreamBody, []byte(testClaudeCAISSample)) {
		t.Fatalf("upstream request leaked raw Claude CAIS signature: %s", upstreamBody)
	}

	contents := gjson.GetBytes(upstreamBody, "contents").Array()
	for _, content := range contents {
		if content.Get("role").String() == "model" {
			for _, part := range content.Get("parts").Array() {
				if sig := part.Get("thoughtSignature").String(); sig == testClaudeCAISSample {
					t.Fatalf("model part thoughtSignature contains raw Claude CAIS signature: %s", upstreamBody)
				}
			}
		}
	}
}

func TestGeminiExecutorExecuteStream_SanitizesClaudeCAISSignature(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"chunk\"}]}}]}\n\n"))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-api-key",
			"base_url": server.URL,
		},
	}

	req, opts := claudeRequestWithThinkingSignature(testClaudeCAISSample)

	res, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range res.Chunks {
	}

	if bytes.Contains(upstreamBody, []byte(testClaudeCAISSample)) {
		t.Fatalf("upstream stream request leaked raw Claude CAIS signature: %s", upstreamBody)
	}
}

func TestGeminiExecutorCountTokens_SanitizesClaudeCAISSignature(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens": 42}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-api-key",
			"base_url": server.URL,
		},
	}

	req, opts := claudeRequestWithThinkingSignature(testClaudeCAISSample)

	_, err := executor.CountTokens(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}

	if bytes.Contains(upstreamBody, []byte(testClaudeCAISSample)) {
		t.Fatalf("upstream countTokens request leaked raw Claude CAIS signature: %s", upstreamBody)
	}
}

func TestGeminiExecutorExecute_FunctionCall_ReplacesClaudeSignatureWithBypass(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-api-key",
			"base_url": server.URL,
		},
	}

	reqPayload := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{
						"functionCall": {"name": "search", "args": {"q": "go"}},
						"thoughtSignature": "` + testClaudeCAISSample + `"
					}
				]
			},
			{
				"role": "user",
				"parts": [
					{
						"functionResponse": {"name": "search", "response": {"result": "found"}}
					}
				]
			}
		]
	}`)

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: reqPayload,
	}

	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	gotSig := gjson.GetBytes(upstreamBody, "contents.1.parts.0.thoughtSignature").String()
	if gotSig != internalsignature.GeminiSkipThoughtSignatureValidator {
		t.Fatalf("first functionCall thoughtSignature = %q, want bypass sentinel %q; upstreamBody=%s",
			gotSig, internalsignature.GeminiSkipThoughtSignatureValidator, upstreamBody)
	}
}

func TestGeminiExecutorExecute_PreservesNativeGeminiSignature(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	nativeSig := testNativeGemini3ThoughtSignature()
	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-api-key",
			"base_url": server.URL,
		},
	}

	reqPayload := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{
						"functionCall": {"name": "search", "args": {"q": "go"}},
						"thoughtSignature": "` + nativeSig + `"
					}
				]
			},
			{
				"role": "user",
				"parts": [
					{
						"functionResponse": {"name": "search", "response": {"result": "found"}}
					}
				]
			}
		]
	}`)

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: reqPayload,
	}

	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	gotSig := gjson.GetBytes(upstreamBody, "contents.1.parts.0.thoughtSignature").String()
	if gotSig != nativeSig {
		t.Fatalf("thoughtSignature = %q, want preserved native signature %q; upstreamBody=%s",
			gotSig, nativeSig, upstreamBody)
	}
}

func TestGeminiExecutorExecute_UnsignedRequestNotCorrupted(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	executor := NewGeminiExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "test-api-key",
			"base_url": server.URL,
		},
	}

	reqPayload := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "Hello world"}]
			}
		]
	}`)

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: reqPayload,
	}

	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	text := gjson.GetBytes(upstreamBody, "contents.0.parts.0.text").String()
	if text != "Hello world" {
		t.Fatalf("text = %q, want 'Hello world'; upstreamBody=%s", text, upstreamBody)
	}
}

func geminiRequestWithThinkingSignature(sig string) (cliproxyexecutor.Request, cliproxyexecutor.Options) {
	req := cliproxyexecutor.Request{
		Model: "gemini-2.5-flash",
		Payload: []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{"text": "Let me think...", "thought": true, "thoughtSignature": "` + sig + `"},
					{"text": "Here is the response."}
				]
			},
			{
				"role": "user",
				"parts": [
					{"text": "Follow up question."}
				]
			}
		]
	}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
	}
	return req, opts
}

func TestGeminiVertexExecutorExecute_GeminiPayload_SanitizesClaudeCAISSignature(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	executor := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-vertex-key",
			"base_url": server.URL,
		},
	}

	req, opts := geminiRequestWithThinkingSignature(testClaudeCAISSample)

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if bytes.Contains(upstreamBody, []byte(testClaudeCAISSample)) {
		t.Fatalf("vertex upstream request leaked raw Claude CAIS signature: %s", upstreamBody)
	}
}

func TestGeminiVertexExecutorExecuteStream_GeminiPayload_SanitizesClaudeCAISSignature(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"chunk\"}]}}]}\n\n"))
	}))
	defer server.Close()

	executor := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-vertex-key",
			"base_url": server.URL,
		},
	}

	req, opts := geminiRequestWithThinkingSignature(testClaudeCAISSample)

	res, err := executor.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("ExecuteStream() error = %v", err)
	}
	for range res.Chunks {
	}

	if bytes.Contains(upstreamBody, []byte(testClaudeCAISSample)) {
		t.Fatalf("vertex stream upstream request leaked raw Claude CAIS signature: %s", upstreamBody)
	}
}

func TestGeminiVertexExecutorCountTokens_GeminiPayload_SanitizesClaudeCAISSignature(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"totalTokens": 42}`))
	}))
	defer server.Close()

	executor := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-vertex-key",
			"base_url": server.URL,
		},
	}

	req, opts := geminiRequestWithThinkingSignature(testClaudeCAISSample)

	_, err := executor.CountTokens(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("CountTokens() error = %v", err)
	}

	if bytes.Contains(upstreamBody, []byte(testClaudeCAISSample)) {
		t.Fatalf("vertex countTokens upstream request leaked raw Claude CAIS signature: %s", upstreamBody)
	}
}

func TestGeminiVertexExecutorExecute_PreservesNativeGeminiSignature(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	nativeSig := testNativeGemini3ThoughtSignature()
	executor := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-vertex-key",
			"base_url": server.URL,
		},
	}

	reqPayload := []byte(`{
		"contents": [
			{
				"role": "model",
				"parts": [
					{
						"functionCall": {"name": "search", "args": {"q": "go"}},
						"thoughtSignature": "` + nativeSig + `"
					}
				]
			},
			{
				"role": "user",
				"parts": [
					{
						"functionResponse": {"name": "search", "response": {"result": "found"}}
					}
				]
			}
		]
	}`)

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: reqPayload,
	}

	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	gotSig := gjson.GetBytes(upstreamBody, "contents.1.parts.0.thoughtSignature").String()
	if gotSig != nativeSig {
		t.Fatalf("thoughtSignature = %q, want preserved native signature %q; upstreamBody=%s",
			gotSig, nativeSig, upstreamBody)
	}
}

func TestGeminiVertexExecutorExecute_UnsignedRequestNotCorrupted(t *testing.T) {
	var upstreamBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read request body: %v", errRead)
		}
		upstreamBody = append([]byte(nil), body...)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	executor := NewGeminiVertexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "vertex",
		Attributes: map[string]string{
			"api_key":  "test-vertex-key",
			"base_url": server.URL,
		},
	}

	reqPayload := []byte(`{
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "Hello world"}]
			}
		]
	}`)

	req := cliproxyexecutor.Request{
		Model:   "gemini-2.5-flash",
		Payload: reqPayload,
	}

	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatGemini,
	}

	_, err := executor.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	text := gjson.GetBytes(upstreamBody, "contents.0.parts.0.text").String()
	if text != "Hello world" {
		t.Fatalf("text = %q, want 'Hello world'; upstreamBody=%s", text, upstreamBody)
	}
}
