package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorMiniMaxImageGenerationUsesNativeEndpoint(t *testing.T) {
	var gotPath string
	var gotBody []byte
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"img_1","data":{"image_base64":["abc"]},"base_resp":{"status_code":0}}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("minimax", nil)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":    server.URL + "/v1",
		"api_key":     "test-key",
		"compat_kind": "minimax",
	}}

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "image-01",
		Payload: []byte(`{"model":"client-alias","prompt":"draw","response_format":"base64"}`),
	}, cliproxyexecutor.Options{
		Alt:          openAICompatAltMiniMaxImageGeneration,
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if gotPath != "/v1/image_generation" {
		t.Fatalf("path = %q, want /v1/image_generation", gotPath)
	}
	if gotAuth != "Bearer test-key" {
		t.Fatalf("Authorization = %q, want bearer key", gotAuth)
	}
	if got := gjson.GetBytes(gotBody, "model").String(); got != "image-01" {
		t.Fatalf("upstream model = %q, want image-01", got)
	}
	if got := gjson.GetBytes(resp.Payload, "data.0.b64_json").String(); got != "abc" {
		t.Fatalf("response b64_json = %q, want abc", got)
	}
}

func TestBuildOpenAIImagesResponseFromMiniMaxURL(t *testing.T) {
	body := []byte(`{"id":"img_1","data":{"image_urls":["https://example.com/a.png"]},"base_resp":{"status_code":0}}`)

	out, err := buildOpenAIImagesResponseFromMiniMax(body)
	if err != nil {
		t.Fatalf("buildOpenAIImagesResponseFromMiniMax() error = %v", err)
	}
	if got := gjson.GetBytes(out, "data.0.url").String(); got != "https://example.com/a.png" {
		t.Fatalf("url = %q", got)
	}
}

func TestBuildOpenAIImagesResponseFromMiniMaxLogicalError(t *testing.T) {
	body := []byte(`{"base_resp":{"status_code":1008,"status_msg":"insufficient balance"}}`)

	_, err := buildOpenAIImagesResponseFromMiniMax(body)
	if err == nil {
		t.Fatalf("expected error")
	}
	status, ok := err.(statusErr)
	if !ok {
		t.Fatalf("error type = %T, want statusErr", err)
	}
	if status.StatusCode() != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", status.StatusCode())
	}
	if status.ErrorCode() != "minimax_1008" {
		t.Fatalf("error code = %q, want minimax_1008", status.ErrorCode())
	}
}
