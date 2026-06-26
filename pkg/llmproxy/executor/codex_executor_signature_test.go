package executor

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	cliproxyauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/kooshapari/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func validCodexReasoningEncryptedContentForTest() string {
	payload := make([]byte, 1+8+16+16+32)
	payload[0] = 0x80
	for i := 9; i < len(payload); i++ {
		payload[i] = byte(i)
	}
	return base64.RawURLEncoding.EncodeToString(payload)
}

func newCodexSignatureTestAuth(serverURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": serverURL,
		"api_key":  "test",
	}}
}

func TestCodexExecutorDropsInvalidReasoningEncryptedContentFromFinalRequest(t *testing.T) {
	validEncryptedContent := validCodexReasoningEncryptedContentForTest()
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), newCodexSignatureTestAuth(server.URL), cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[` +
			`{"id":"rs_bad","type":"reasoning","encrypted_content":"gAAAAABqFTIa\u2026abc","summary":[]},` +
			`{"id":"rs_non_string","type":"reasoning","encrypted_content":123,"summary":[]},` +
			`{"id":"rs_good","type":"reasoning","encrypted_content":"` + validEncryptedContent + `","summary":[]},` +
			`{"role":"user","content":"hello","encrypted_content":"leave-message-alone"}` +
			`]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}

	if gjson.GetBytes(gotBody, "input.0.encrypted_content").Exists() {
		t.Fatalf("invalid reasoning encrypted_content exists, want removed; body=%s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "input.1.encrypted_content").Exists() {
		t.Fatalf("non-string reasoning encrypted_content exists, want removed; body=%s", string(gotBody))
	}
	if got := gjson.GetBytes(gotBody, "input.2.encrypted_content").String(); got != validEncryptedContent {
		t.Fatalf("valid reasoning encrypted_content = %q, want preserved", got)
	}
	if got := gjson.GetBytes(gotBody, "input.3.encrypted_content").String(); got != "leave-message-alone" {
		t.Fatalf("non-reasoning encrypted_content = %q, want untouched", got)
	}
}

func TestCodexExecutorExecuteStreamDropsInvalidReasoningEncryptedContentFromFinalRequest(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	result, err := executor.ExecuteStream(context.Background(), newCodexSignatureTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","stream":true,"input":[{"id":"rs_bad","type":"reasoning","encrypted_content":"bad","summary":[]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}
	if gjson.GetBytes(gotBody, "input.0.encrypted_content").Exists() {
		t.Fatalf("invalid stream reasoning encrypted_content exists, want removed; body=%s", string(gotBody))
	}
}

func TestCodexExecutorCompactDropsInvalidReasoningEncryptedContentFromFinalRequest(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.Execute(context.Background(), newCodexSignatureTestAuth(server.URL), cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","input":[{"id":"rs_bad","type":"reasoning","encrypted_content":"bad","summary":[]}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute compact error: %v", err)
	}
	if gjson.GetBytes(gotBody, "input.0.encrypted_content").Exists() {
		t.Fatalf("invalid compact reasoning encrypted_content exists, want removed; body=%s", string(gotBody))
	}
}
