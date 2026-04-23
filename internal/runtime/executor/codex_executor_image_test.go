package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorImageGenerationBuildsRequestAndParsesSSE(t *testing.T) {
	var gotBody []byte
	var gotAuth string
	var gotAccount string
	var gotInstall string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("path = %q, want /responses", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("Chatgpt-Account-Id")
		gotInstall = r.Header.Get("x-codex-installation-id")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"BASE64PNG\",\"revised_prompt\":\"better prompt\"}],\"usage\":{\"input_tokens\":2,\"output_tokens\":0,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": server.URL},
		Metadata:   map[string]any{"access_token": "oauth-token", "account_id": "acct-1"},
	}
	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5",
		Payload: []byte(`{"model":"codex/gpt-image","prompt":"draw a cat","size":"1024x1024","quality":"high","output_format":"png","n":1,"created":123}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Alt:          "images/generations",
		Metadata:     map[string]any{cliproxyexecutor.RequestedModelMetadataKey: "codex/gpt-image"},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotAuth != "Bearer oauth-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotAccount != "acct-1" {
		t.Fatalf("Chatgpt-Account-Id = %q", gotAccount)
	}
	if gotInstall != "" {
		t.Fatalf("installation id header = %q, want empty", gotInstall)
	}
	if gjson.GetBytes(gotBody, "model").String() != "gpt-5" {
		t.Fatalf("request model = %s; body=%s", gjson.GetBytes(gotBody, "model").String(), string(gotBody))
	}
	if gjson.GetBytes(gotBody, "tools.0.type").String() != "image_generation" {
		t.Fatalf("missing image_generation tool: %s", string(gotBody))
	}
	if !gjson.GetBytes(gotBody, "stream").Bool() {
		t.Fatalf("stream not enabled: %s", string(gotBody))
	}
	if strings.Contains(string(gotBody), "client_metadata") || strings.Contains(string(gotBody), "installation") {
		t.Fatalf("request body leaked installation metadata: %s", string(gotBody))
	}
	if got := gjson.GetBytes(resp.Payload, "data.0.b64_json").String(); got != "BASE64PNG" {
		t.Fatalf("b64_json = %q; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "codex/gpt-image" {
		t.Fatalf("response model = %q; payload=%s", got, string(resp.Payload))
	}
	if got := gjson.GetBytes(resp.Payload, "usage.generated_images").Int(); got != 1 {
		t.Fatalf("generated_images = %d; payload=%s", got, string(resp.Payload))
	}
}

func TestParseCodexImageGenerationResponsePartialThenFinal(t *testing.T) {
	data := []byte("data: {\"type\":\"response.image_generation_call.partial_image\",\"partial_image_b64\":\"PARTIAL\"}\n" +
		"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"image_generation_call\",\"result\":\"FINAL\"}}\n")
	got, err := parseCodexImageGenerationResponse(data)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if got.B64JSON != "FINAL" || got.PartialImages != 1 {
		t.Fatalf("got %+v, want final image and one partial", got)
	}
}
