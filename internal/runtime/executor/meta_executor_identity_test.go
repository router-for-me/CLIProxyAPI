package executor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestMetaStreamPreservesDeclaredToolName(t *testing.T) {
	const toolName = "collaboration-optimize__spawn_agent"
	body := []byte(`{"tools":[{"type":"function","name":"collaboration-optimize__spawn_agent","parameters":{"type":"object","properties":{},"required":[],"additionalProperties":false},"strict":true}],"input":[{"type":"agent_message","author":"review-agent","recipient":"all","content":[{"type":"input_text","text":"hello"}]}]}`)
	completion := `{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","name":"collaboration-optimize__spawn_agent","call_id":"call1","arguments":"{}"}]}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if got := gjson.GetBytes(raw, "tools.0.name").String(); got != toolName {
			t.Errorf("outbound declaration renamed: %s", raw)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", completion)
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.Client.Codex.OptimizeMultiAgentV2 = true
	e := NewMetaExecutor(cfg)
	auth := &cliproxyauth.Auth{ID: "strict-review-meta", Provider: "meta", Attributes: map[string]string{"api_key": "test-token", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "muse-spark-1.3", Payload: body}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, OriginalRequest: body, Headers: http.Header{"User-Agent": []string{"codex-tui/0.154.0"}}, Stream: true}
	prepared, err := e.prepareResponsesRequest(context.Background(), auth, req, opts, true)
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(prepared.body, "tools.0.name").String(); got != toolName {
		t.Fatalf("fixture changed declared tool name: %s", prepared.body)
	}
	if got := gjson.GetBytes(prepared.body, "input.0.type").String(); got != "message" {
		t.Fatalf("fixture did not convert agent message: %s", prepared.body)
	}
	// The non-stream path is a positive control: the declared tool remains callable.
	nonStream, err := e.translateMetaCompleted(context.Background(), req, prepared, []byte("data: "+completion+"\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := gjson.GetBytes(nonStream.payload, "output.0.name").String(); got != toolName {
		t.Fatalf("non-stream positive control changed declared tool: %s", nonStream.payload)
	}
	streamed, err := e.ExecuteStream(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatal(err)
	}
	var result []byte
	for chunk := range streamed.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		result = append(result, chunk.Payload...)
	}
	if !bytes.Contains(result, []byte(`"name":"`+toolName+`"`)) {
		t.Fatalf("stream changed a declared user tool merely because input was rewritten: %s", result)
	}
}
