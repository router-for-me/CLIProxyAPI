package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v6/internal/translator"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorDoesNotInjectImageGenerationToolForGenericResponsesClient(t *testing.T) {
	capturedBody := executeCodexStreamAndCaptureBody(t, []byte(`{"model":"gpt-5.4","input":"Say ok","tools":[{"type":"web_search_preview"}]}`))

	for _, tool := range gjson.GetBytes(capturedBody, "tools").Array() {
		if tool.Get("type").String() == "image_generation" {
			t.Fatalf("unexpected image_generation tool injected for generic Responses client; upstream body=%s", string(capturedBody))
		}
	}
}

func TestCodexExecutorEnsuresImageGenerationToolForExplicitToolChoice(t *testing.T) {
	capturedBody := executeCodexStreamAndCaptureBody(t, []byte(`{"model":"gpt-5.4","input":"Draw a cat","tool_choice":{"type":"image_generation"}}`))

	if !hasImageGenerationTool(capturedBody) {
		t.Fatalf("expected image_generation tool for explicit tool_choice; upstream body=%s", string(capturedBody))
	}
	if got := gjson.GetBytes(capturedBody, "tool_choice.type").String(); got != "image_generation" {
		t.Fatalf("tool_choice.type = %q, want image_generation; upstream body=%s", got, string(capturedBody))
	}
}

func executeCodexStreamAndCaptureBody(t *testing.T, payload []byte) []byte {
	t.Helper()

	bodyCh := make(chan []byte, 1)
	errCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, errRead := io.ReadAll(r.Body)
		if errRead != nil {
			errCh <- errRead
			http.Error(w, errRead.Error(), http.StatusInternalServerError)
			return
		}
		bodyCh <- append([]byte(nil), body...)

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":1775555723,\"status\":\"completed\",\"model\":\"gpt-5.4\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, errExec := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if errExec != nil {
		t.Fatalf("ExecuteStream error: %v", errExec)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}
	select {
	case errRead := <-errCh:
		t.Fatalf("read request body: %v", errRead)
	case capturedBody := <-bodyCh:
		if len(capturedBody) == 0 {
			t.Fatal("missing captured upstream request body")
		}
		return capturedBody
	default:
		t.Fatal("missing captured upstream request body")
	}
	return nil
}
