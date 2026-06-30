package executor

import (
	"bytes"
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	clineauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorPrepareRequestUsesClineProviderSettingsToken(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "providers.json")
	raw := []byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"auth": {"accessToken": "cline-access-token"}
				}
			}
		}
	}`)
	if err := os.WriteFile(settingsPath, raw, 0600); err != nil {
		t.Fatalf("failed to write Cline provider settings: %v", err)
	}

	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                 clineauth.APIBaseURL,
		"credential_source":        clineauth.CredentialSourceProviderSettings,
		"cline_provider":           clineauth.ProviderClinePass,
		cliproxyauth.AttributePath: settingsPath,
	}}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer cline-access-token" {
		t.Fatalf("Authorization = %q, want bearer token from Cline provider settings", got)
	}
}

func TestOpenAICompatExecutorPrepareRequestUsesClineAccountTokenForClinePass(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "providers.json")
	raw := []byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"model": "cline-pass/glm-5.2"
				}
			},
			"cline": {
				"settings": {
					"provider": "cline",
					"auth": {"accessToken": "cline-account-token"}
				}
			}
		}
	}`)
	if err := os.WriteFile(settingsPath, raw, 0600); err != nil {
		t.Fatalf("failed to write Cline provider settings: %v", err)
	}

	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                 clineauth.APIBaseURL,
		"credential_source":        clineauth.CredentialSourceProviderSettings,
		"cline_provider":           clineauth.ProviderClinePass,
		cliproxyauth.AttributePath: settingsPath,
	}}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer cline-account-token" {
		t.Fatalf("Authorization = %q, want bearer token from Cline account settings", got)
	}
}

func TestOpenAICompatExecutorPrepareRequestDoesNotUseClineTokenForOtherBaseURL(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "providers.json")
	raw := []byte(`{
		"providers": {
			"cline-pass": {
				"settings": {
					"provider": "cline-pass",
					"auth": {"accessToken": "cline-access-token"}
				}
			}
		}
	}`)
	if err := os.WriteFile(settingsPath, raw, 0600); err != nil {
		t.Fatalf("failed to write Cline provider settings: %v", err)
	}

	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                 "https://example.com/v1",
		"credential_source":        clineauth.CredentialSourceProviderSettings,
		"cline_provider":           clineauth.ProviderClinePass,
		cliproxyauth.AttributePath: settingsPath,
	}}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	if err := executor.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest error: %v", err)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty header for non-Cline base URL", got)
	}
}

func TestOpenAICompatExecutorPrepareRequestErrorsWhenClineTokenUnavailable(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                 clineauth.APIBaseURL,
		"credential_source":        clineauth.CredentialSourceProviderSettings,
		"cline_provider":           clineauth.ProviderClinePass,
		cliproxyauth.AttributePath: filepath.Join(t.TempDir(), "missing-providers.json"),
	}}
	req, err := http.NewRequest(http.MethodPost, "https://example.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}

	err = executor.PrepareRequest(req, auth)
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error = %T(%v), want statusErr", err, err)
	}
	if status.code != http.StatusFailedDependency {
		t.Fatalf("status code = %d, want %d", status.code, http.StatusFailedDependency)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want no header when token is unavailable", got)
	}
}

func TestOpenAICompatExecutorUnwrapsClineProviderSettingsEnvelope(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          clineauth.APIBaseURL,
		"credential_source": clineauth.CredentialSourceProviderSettings,
		"cline_provider":    clineauth.ProviderClinePass,
	}}
	body := []byte(`{
		"success": true,
		"data": {
			"id": "chatcmpl_1",
			"object": "chat.completion",
			"model": "zai/glm-5.2",
			"choices": [{"message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
		}
	}`)

	got, err := executor.handleClineProviderSettingsEnvelope(auth, body)
	if err != nil {
		t.Fatalf("handleClineProviderSettingsEnvelope error: %v", err)
	}
	if gjson.GetBytes(got, "object").String() != "chat.completion" {
		t.Fatalf("payload was not unwrapped: %s", string(got))
	}
	if gjson.GetBytes(got, "success").Exists() {
		t.Fatalf("unexpected Cline envelope in payload: %s", string(got))
	}
}

func TestOpenAICompatExecutorDoesNotUnwrapNonClineEnvelope(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "https://example.com/v1",
		"api_key":  "test",
	}}
	body := []byte(`{"success":true,"data":{"object":"chat.completion"}}`)

	got, err := executor.handleClineProviderSettingsEnvelope(auth, body)
	if err != nil {
		t.Fatalf("handleClineProviderSettingsEnvelope error: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("non-Cline payload was unwrapped: %s", string(got))
	}
}

func TestOpenAICompatExecutorErrorsOnClineProviderSettingsFailureEnvelope(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          clineauth.APIBaseURL,
		"credential_source": clineauth.CredentialSourceProviderSettings,
		"cline_provider":    clineauth.ProviderClinePass,
	}}
	body := []byte(`{"success":false,"code":"invalid_auth","message":"token expired"}`)

	got, err := executor.handleClineProviderSettingsEnvelope(auth, body)
	if err == nil {
		t.Fatalf("expected failure envelope error, got payload: %s", string(got))
	}
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", err)
	}
	if status.code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", status.code, http.StatusBadGateway)
	}
	if !strings.Contains(status.msg, "invalid_auth") {
		t.Fatalf("status message = %q, want safe upstream code", status.msg)
	}
	if strings.Contains(status.msg, "token expired") {
		t.Fatalf("status message leaked free-form upstream message: %q", status.msg)
	}
}

func TestOpenAICompatExecutorSanitizesClineProviderSettingsStreamJSONEnvelope(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          clineauth.APIBaseURL,
		"credential_source": clineauth.CredentialSourceProviderSettings,
		"cline_provider":    clineauth.ProviderClinePass,
	}}
	body := []byte(`{"success":false,"code":"invalid_auth","message":"token expired"}`)

	err := executor.openAICompatNonSSEStreamError(auth, body)
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", err)
	}
	if status.code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", status.code, http.StatusBadGateway)
	}
	if !strings.Contains(status.msg, "invalid_auth") {
		t.Fatalf("status message = %q, want safe upstream code", status.msg)
	}
	if strings.Contains(status.msg, "token expired") || strings.Contains(status.msg, `"success"`) {
		t.Fatalf("status message used unsanitized upstream JSON: %q", status.msg)
	}
}

func TestOpenAICompatExecutorKeepsRawClineStreamJSONWithoutEnvelope(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          clineauth.APIBaseURL,
		"credential_source": clineauth.CredentialSourceProviderSettings,
		"cline_provider":    clineauth.ProviderClinePass,
	}}
	body := []byte(`{"error":{"message":"plain upstream JSON"}}`)

	err := executor.openAICompatNonSSEStreamError(auth, body)
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", err)
	}
	if status.code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", status.code, http.StatusBadGateway)
	}
	if status.msg != string(body) {
		t.Fatalf("status message = %q, want raw JSON %q", status.msg, string(body))
	}
}

func TestOpenAICompatExecutorErrorsOnMalformedClineProviderSettingsEnvelope(t *testing.T) {
	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          clineauth.APIBaseURL,
		"credential_source": clineauth.CredentialSourceProviderSettings,
		"cline_provider":    clineauth.ProviderClinePass,
	}}
	body := []byte(`{"success":true}`)

	got, err := executor.handleClineProviderSettingsEnvelope(auth, body)
	if err == nil {
		t.Fatalf("expected malformed envelope error, got payload: %s", string(got))
	}
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", err)
	}
	if status.code != http.StatusBadGateway {
		t.Fatalf("status code = %d, want %d", status.code, http.StatusBadGateway)
	}
	if !strings.Contains(status.msg, "missing data") {
		t.Fatalf("status message = %q, want missing data", status.msg)
	}
}

func TestOpenAICompatExecutorForcesClineProviderSettingsNonStreamChatPayload(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          clineauth.APIBaseURL,
		"credential_source": clineauth.CredentialSourceProviderSettings,
		"cline_provider":    clineauth.ProviderClinePass,
	}}
	body := []byte(`{"model":"cline-pass/glm-5.2","messages":[{"role":"user","content":"hi"}]}`)

	got := forceClineProviderSettingsNonStreamChatPayload(auth, body)
	stream := gjson.GetBytes(got, "stream")
	if !stream.Exists() || stream.Bool() {
		t.Fatalf("stream = %s, want explicit false; body=%s", stream.Raw, string(got))
	}

	bodyWithStreamTrue := []byte(`{"model":"cline-pass/glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	got = forceClineProviderSettingsNonStreamChatPayload(auth, bodyWithStreamTrue)
	stream = gjson.GetBytes(got, "stream")
	if !stream.Exists() || stream.Bool() {
		t.Fatalf("stream = %s, want forced false; body=%s", stream.Raw, string(got))
	}
}

func TestOpenAICompatExecutorDoesNotForceNonClineNonStreamChatPayload(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": "https://example.com/v1",
		"api_key":  "test",
	}}
	body := []byte(`{"model":"custom-openai","messages":[{"role":"user","content":"hi"}]}`)

	got := forceClineProviderSettingsNonStreamChatPayload(auth, body)
	if !bytes.Equal(got, body) {
		t.Fatalf("non-Cline body changed: %s", string(got))
	}
}

func TestOpenAICompatExecutorExecuteForcesClineProviderSettingsNonStream(t *testing.T) {
	var gotPath string
	var gotBody []byte
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		gotPath = req.URL.Path
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatalf("failed to read request body: %v", err)
		}
		gotBody = body
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)),
			Request:    req,
		}, nil
	}))

	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":          clineauth.APIBaseURL,
		"api_key":           "test",
		"credential_source": clineauth.CredentialSourceProviderSettings,
		"cline_provider":    clineauth.ProviderClinePass,
	}}
	payload := []byte(`{"model":"cline-pass/glm-5.2","messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "cline-pass/glm-5.2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/api/v1/chat/completions" {
		t.Fatalf("path = %q, want /api/v1/chat/completions", gotPath)
	}
	stream := gjson.GetBytes(gotBody, "stream")
	if !stream.Exists() || stream.Bool() {
		t.Fatalf("stream = %s, want explicit false; body=%s", stream.Raw, string(gotBody))
	}
}

func TestOpenAICompatExecutorExecuteDoesNotCallUpstreamWhenClineTokenUnavailable(t *testing.T) {
	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		t.Fatalf("upstream should not be called when Cline provider settings token is unavailable")
		return nil, nil
	}))

	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                 clineauth.APIBaseURL,
		"credential_source":        clineauth.CredentialSourceProviderSettings,
		"cline_provider":           clineauth.ProviderClinePass,
		cliproxyauth.AttributePath: filepath.Join(t.TempDir(), "missing-providers.json"),
	}}
	payload := []byte(`{"model":"cline-pass/glm-5.2","messages":[{"role":"user","content":"hi"}]}`)

	_, err := executor.Execute(ctx, auth, cliproxyexecutor.Request{
		Model:   "cline-pass/glm-5.2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error = %T(%v), want statusErr", err, err)
	}
	if status.code != http.StatusFailedDependency {
		t.Fatalf("status code = %d, want %d", status.code, http.StatusFailedDependency)
	}
	if called {
		t.Fatal("upstream was called despite missing Cline provider settings token")
	}
}

func TestOpenAICompatExecutorExecuteStreamDoesNotCallUpstreamWhenClineTokenUnavailable(t *testing.T) {
	called := false
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		t.Fatalf("upstream should not be called when Cline provider settings token is unavailable")
		return nil, nil
	}))

	executor := NewOpenAICompatExecutor("openai-compatible-cline-pass", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                 clineauth.APIBaseURL,
		"credential_source":        clineauth.CredentialSourceProviderSettings,
		"cline_provider":           clineauth.ProviderClinePass,
		cliproxyauth.AttributePath: filepath.Join(t.TempDir(), "missing-providers.json"),
	}}
	payload := []byte(`{"model":"cline-pass/glm-5.2","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	_, err := executor.ExecuteStream(ctx, auth, cliproxyexecutor.Request{
		Model:   "cline-pass/glm-5.2",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error = %T(%v), want statusErr", err, err)
	}
	if status.code != http.StatusFailedDependency {
		t.Fatalf("status code = %d, want %d", status.code, http.StatusFailedDependency)
	}
	if called {
		t.Fatal("upstream was called despite missing Cline provider settings token")
	}
}

func TestOpenAICompatExecutorCompactPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"gpt-5.1-codex-max","input":[{"role":"user","content":"hi"}]}`)
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.1-codex-max",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/responses/compact" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/responses/compact")
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected input in body")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected messages in body")
	}
	if string(resp.Payload) != `{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}` {
		t.Fatalf("payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorPayloadOverrideWinsOverThinkingSuffix(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
		Payload: config.PayloadConfig{
			Override: []config.PayloadRule{
				{
					Models: []config.PayloadModelRule{
						{Name: "custom-openai", Protocol: "openai"},
					},
					Params: map[string]any{
						"reasoning_effort": "low",
					},
				},
			},
		},
	})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"custom-openai(high)","messages":[{"role":"user","content":"hi"}]}`)
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "custom-openai(high)",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got := gjson.GetBytes(gotBody, "reasoning_effort").String(); got != "low" {
		t.Fatalf("reasoning_effort = %q, want %q; body=%s", got, "low", string(gotBody))
	}
}

func TestOpenAICompatExecutorImagesGenerationsPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"AA=="}],"usage":{"total_tokens":1}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image",
		Payload: []byte(`{"model":"compat-image","prompt":"draw"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       false,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/images/generations")
	}
	if gotContentType != "application/json" {
		t.Fatalf("content type = %q, want application/json", gotContentType)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image; body=%s", got, string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "data.0.b64_json").String(); got != "AA==" {
		t.Fatalf("response payload = %s", string(resp.Payload))
	}
}

func TestOpenAICompatExecutorImagesGenerationsStreamsUpstream(t *testing.T) {
	var gotPath string
	var gotBody []byte
	var gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: image_generation.partial\ndata: {\"type\":\"image_generation.partial\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	streamResult, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image",
		Payload: []byte(`{"model":"compat-image","prompt":"draw","stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       true,
		Headers: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/generations",
		},
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	var streamed bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
		streamed.Write(chunk.Payload)
	}
	if gotPath != "/v1/images/generations" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/images/generations")
	}
	if gotAccept != "text/event-stream" {
		t.Fatalf("accept = %q, want text/event-stream", gotAccept)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image; body=%s", got, string(gotBody))
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("stream flag missing from upstream body: %s", string(gotBody))
	}
	if !strings.Contains(streamed.String(), "event: image_generation.partial") || !strings.Contains(streamed.String(), "data: [DONE]") {
		t.Fatalf("streamed body = %q", streamed.String())
	}
}

func TestOpenAICompatExecutorImagesEditsMultipartRewritesModel(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if errWrite := writer.WriteField("model", "compat-image"); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("prompt", "edit"); errWrite != nil {
		t.Fatalf("write prompt field: %v", errWrite)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.png"))
	header.Set("Content-Type", "image/png")
	part, errCreate := writer.CreatePart(header)
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := part.Write([]byte("png-data")); errWrite != nil {
		t.Fatalf("write image field: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}
	contentType := writer.FormDataContentType()

	var gotPath string
	var gotModel string
	var gotPrompt string
	var gotFile string
	var gotFileContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if errParse := r.ParseMultipartForm(32 << 20); errParse != nil {
			t.Fatalf("parse multipart form: %v", errParse)
		}
		gotModel = r.FormValue("model")
		gotPrompt = r.FormValue("prompt")
		file, fileHeader, errFile := r.FormFile("image")
		if errFile != nil {
			t.Fatalf("read image file: %v", errFile)
		}
		gotFileContentType = fileHeader.Header.Get("Content-Type")
		data, errRead := io.ReadAll(file)
		if errClose := file.Close(); errClose != nil {
			t.Fatalf("close image file: %v", errClose)
		}
		if errRead != nil {
			t.Fatalf("read image file: %v", errRead)
		}
		gotFile = string(data)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"created":123,"data":[{"b64_json":"AA=="}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "upstream-image",
		Payload: body.Bytes(),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-image"),
		Stream:       false,
		Headers: http.Header{
			"Content-Type": []string{contentType},
		},
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/images/edits",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/images/edits" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/images/edits")
	}
	if gotModel != "upstream-image" {
		t.Fatalf("model = %q, want upstream-image", gotModel)
	}
	if gotPrompt != "edit" {
		t.Fatalf("prompt = %q, want edit", gotPrompt)
	}
	if gotFile != "png-data" {
		t.Fatalf("file = %q, want png-data", gotFile)
	}
	if gotFileContentType != "image/png" {
		t.Fatalf("file content type = %q, want image/png", gotFileContentType)
	}
}

func TestRewriteOpenAICompatImagesMultipartPayloadPreservesStreamAndFileContentType(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if errWrite := writer.WriteField("model", "compat-image"); errWrite != nil {
		t.Fatalf("write model field: %v", errWrite)
	}
	if errWrite := writer.WriteField("stream", "false"); errWrite != nil {
		t.Fatalf("write stream field: %v", errWrite)
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", multipart.FileContentDisposition("image", "image.webp"))
	header.Set("Content-Type", "image/webp")
	part, errCreate := writer.CreatePart(header)
	if errCreate != nil {
		t.Fatalf("create image field: %v", errCreate)
	}
	if _, errWrite := part.Write([]byte("webp-data")); errWrite != nil {
		t.Fatalf("write image field: %v", errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("close multipart writer: %v", errClose)
	}

	out, contentType, err := prepareOpenAICompatImagesPayload(body.Bytes(), "upstream-image", writer.FormDataContentType(), true)
	if err != nil {
		t.Fatalf("prepareOpenAICompatImagesPayload error: %v", err)
	}
	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil {
		t.Fatalf("parse content type: %v", errParse)
	}
	if mediaType != "multipart/form-data" {
		t.Fatalf("media type = %q, want multipart/form-data", mediaType)
	}
	reader := multipart.NewReader(bytes.NewReader(out), params["boundary"])
	form, errRead := reader.ReadForm(32 << 20)
	if errRead != nil {
		t.Fatalf("read rewritten form: %v", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			t.Fatalf("remove form files: %v", errRemove)
		}
	}()
	if got := form.Value["model"]; len(got) != 1 || got[0] != "upstream-image" {
		t.Fatalf("model values = %#v, want upstream-image", got)
	}
	if got := form.Value["stream"]; len(got) != 1 || got[0] != "true" {
		t.Fatalf("stream values = %#v, want true", got)
	}
	if got := form.File["image"]; len(got) != 1 || got[0].Header.Get("Content-Type") != "image/webp" {
		t.Fatalf("image headers = %#v, want image/webp", got)
	}
}

func TestOpenAICompatExecutorStreamRejectsPlainJSONAfterBlankLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("\n\n: openrouter processing\n\nevent: error\n"))
		_, _ = w.Write([]byte(`{"error":{"message":"upstream failed","type":"server_error"}}` + "\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "openrouter-model",
		Payload: []byte(`{"model":"openrouter-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var gotErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			gotErr = chunk.Err
			break
		}
	}
	if gotErr == nil {
		t.Fatalf("expected plain JSON stream error")
	}
	if status, ok := gotErr.(interface{ StatusCode() int }); !ok || status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("stream error status = %v, want %d", gotErr, http.StatusBadGateway)
	}
	if !strings.Contains(gotErr.Error(), "upstream failed") {
		t.Fatalf("stream error = %v", gotErr)
	}
}

func TestOpenAICompatExecutorStreamSkipsKeepAliveUntilDataLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("\n\n: openrouter processing\n\nevent: ping\nid: 1\nretry: 1000\n"))
		_, _ = w.Write([]byte(`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":null}]}` + "\n"))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "openrouter-model",
		Payload: []byte(`{"model":"openrouter-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	var got strings.Builder
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream error: %v", chunk.Err)
		}
		got.Write(chunk.Payload)
	}
	if gjson.Get(got.String(), "choices.0.delta.content").String() != "hello" {
		t.Fatalf("stream payload = %s", got.String())
	}
}
