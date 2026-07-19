package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecuteStreamShortensOverlongInputItemIDs(t *testing.T) {
	longCallItemID := strings.Repeat("grok-call-item-", 6)
	longOutputItemID := strings.Repeat("grok-output-item-", 6)
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			t.Fatalf("read body: %v", errRead)
		}
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"output\":[],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{SDKConfig: config.SDKConfig{DisableImageGeneration: config.DisableImageGenerationAll}})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"base_url": server.URL, "api_key": "test"}}
	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model: "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","stream":true,"input":[` +
			`{"type":"function_call","id":"` + longCallItemID + `","call_id":"call-1","name":"lookup","arguments":"{}"},` +
			`{"type":"function_call_output","id":"` + longOutputItemID + `","call_id":"call-1","output":"ok"},` +
			`{"type":"message","id":"msg-1","role":"user","content":"continue"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}

	for index, testCase := range []struct {
		path       string
		originalID string
	}{
		{path: "input.0.id", originalID: longCallItemID},
		{path: "input.1.id", originalID: longOutputItemID},
	} {
		actual := gjson.GetBytes(gotBody, testCase.path).String()
		if len([]rune(actual)) > 64 || actual == testCase.originalID {
			t.Fatalf("input.%d.id was not shortened to at most 64 characters: %q", index, actual)
		}
	}
	if got := gjson.GetBytes(gotBody, "input.0.call_id").String(); got != "call-1" {
		t.Fatalf("function call_id = %q, want call-1", got)
	}
	if got := gjson.GetBytes(gotBody, "input.1.call_id").String(); got != "call-1" {
		t.Fatalf("function call output call_id = %q, want call-1", got)
	}
	if got := gjson.GetBytes(gotBody, "input.2.id").String(); got != "msg-1" {
		t.Fatalf("valid input item ID changed: %q", got)
	}
}
