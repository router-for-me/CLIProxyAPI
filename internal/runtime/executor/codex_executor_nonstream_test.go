package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexExecutorExecute_ReturnsOnResponseDoneBeforeEOF(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/responses")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.done\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":123,\"status\":\"completed\",\"background\":false,\"error\":null,\"output\":[]}}\n\n")
		flusher.Flush()
		<-release
	}))
	defer server.Close()
	defer close(release)

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test-key",
	}}
	resultCh := make(chan struct {
		resp cliproxyexecutor.Response
		err  error
	}, 1)

	go func() {
		resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
			Model:   "gpt-5.4",
			Payload: []byte(`{"model":"gpt-5.4","input":[]}`),
		}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FromString("openai-response"),
			OriginalRequest: []byte(`{"model":"gpt-5.4","input":[]}`),
		})
		resultCh <- struct {
			resp cliproxyexecutor.Response
			err  error
		}{resp: resp, err: err}
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			t.Fatalf("Execute error: %v", result.err)
		}
		if gjson.GetBytes(result.resp.Payload, "type").String() != "response.completed" {
			t.Fatalf("payload = %s", string(result.resp.Payload))
		}
		if gjson.GetBytes(result.resp.Payload, "response.id").String() != "resp_1" {
			t.Fatalf("payload = %s", string(result.resp.Payload))
		}
		if gjson.GetBytes(result.resp.Payload, "response.status").String() != "completed" {
			t.Fatalf("status = %q, want %q", gjson.GetBytes(result.resp.Payload, "response.status").String(), "completed")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Execute did not return after terminal response event")
	}
}
