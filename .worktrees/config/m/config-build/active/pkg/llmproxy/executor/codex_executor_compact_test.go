package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorCompactUsesCompactEndpoint(t *testing.T) {
	var gotPath string
	var gotAccept string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAccept = r.Header.Get("Accept")
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response.compaction","usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"gpt-5.1-codex-max","input":[{"role":"user","content":"compact this"}]}`)
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
	if gotPath != "/responses/compact" {
		t.Fatalf("path = %q, want %q", gotPath, "/responses/compact")
	}
	if gotAccept != "application/json" {
		t.Fatalf("accept = %q, want application/json", gotAccept)
	}
	if !gjson.GetBytes(gotBody, "input").Exists() {
		t.Fatalf("expected input in body")
	}
	if gjson.GetBytes(gotBody, "stream").Exists() {
		t.Fatalf("stream must not be present for compact requests")
	}
	if gjson.GetBytes(resp.Payload, "object").String() != "response.compaction" {
		t.Fatalf("unexpected payload: %s", string(resp.Payload))
	}
}

func TestCodexExecutorCompactStreamingRejected(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.ExecuteStream(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.1-codex-max",
		Payload: []byte(`{"model":"gpt-5.1-codex-max","input":"x"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Alt:          "responses/compact",
		Stream:       true,
	})
	if err == nil {
		t.Fatal("expected error for streaming compact request")
	}
	st, ok := err.(statusErr)
	if !ok {
		t.Fatalf("expected statusErr, got %T", err)
	}
	if st.code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", st.code, http.StatusBadRequest)
	}
}
