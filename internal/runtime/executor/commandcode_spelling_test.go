package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestCommandCodeExecutor_UpstreamModelSpellingRewrite verifies that the
// executor rewrites a lowercase requested model id to the official gateway
// spelling inside the /alpha/generate envelope, while the client-facing
// model name stays the lowercase id.
func TestCommandCodeExecutor_UpstreamModelSpellingRewrite(t *testing.T) {
	restore := registry.SetCommandCodeOfficialSpellingsForTest(map[string]string{
		"qwen/qwen3.8-flash": "Qwen/Qwen3.8-Flash",
	})
	defer restore()

	var upstreamModel string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		var envelope struct {
			Params struct {
				Model string `json:"model"`
			} `json:"params"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("parse envelope: %v", err)
		}
		upstreamModel = envelope.Params.Model

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"text-delta","id":"txt-0","text":"OK"}`)
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{
		ID:       "test-auth",
		Provider: "commandcode",
		Attributes: map[string]string{
			"api_key": "test-api-key",
		},
	}

	reqJSON := []byte(`{"model":"qwen/qwen3.8-flash","messages":[{"role":"user","content":"say OK"}],"stream":false}`)

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "qwen/qwen3.8-flash",
		Payload: reqJSON,
	}, cliproxyexecutor.Options{Stream: false})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if upstreamModel != "Qwen/Qwen3.8-Flash" {
		t.Fatalf("upstream envelope model = %q, want %q", upstreamModel, "Qwen/Qwen3.8-Flash")
	}
}

// TestCommandCodeExecutor_UnknownModelPassthrough ensures ids absent from the
// spelling table reach the envelope untouched (existing behavior for the
// all-lowercase official ids).
func TestCommandCodeExecutor_UnknownModelPassthrough(t *testing.T) {
	restore := registry.SetCommandCodeOfficialSpellingsForTest(map[string]string{})
	defer restore()

	var upstreamModel string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		var envelope struct {
			Params struct {
				Model string `json:"model"`
			} `json:"params"`
		}
		_ = json.Unmarshal(body, &envelope)
		upstreamModel = envelope.Params.Model

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"finish-step","finishReason":"stop","usage":{"inputTokens":1,"outputTokens":1,"totalTokens":2}}`)
	}))
	defer ts.Close()

	exec := &CommandCodeExecutor{BaseURL: ts.URL}
	auth := &cliproxyauth.Auth{
		ID:       "test-auth",
		Provider: "commandcode",
		Attributes: map[string]string{
			"api_key": "test-api-key",
		},
	}

	reqJSON := []byte(`{"model":"deepseek/deepseek-v4-flash","messages":[{"role":"user","content":"say OK"}],"stream":false}`)

	_, err := exec.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "deepseek/deepseek-v4-flash",
		Payload: reqJSON,
	}, cliproxyexecutor.Options{Stream: false})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if upstreamModel != "deepseek/deepseek-v4-flash" {
		t.Fatalf("upstream envelope model = %q, want passthrough", upstreamModel)
	}
}
