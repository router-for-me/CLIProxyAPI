package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexOpenAIImageRetriesIncompleteNonStreamResponse(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Errorf("path = %q, want /responses", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		call := atomic.AddInt32(&calls, 1)
		if call == 1 {
			_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"output_format\":\"png\"}}\n\n")
			return
		}
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"created_at\":1,\"output\":[{\"type\":\"image_generation_call\",\"result\":\"aGVsbG8=\",\"output_format\":\"png\",\"revised_prompt\":\"drawn\"}],\"tool_usage\":{\"image_gen\":{\"count\":1}}}}\n\n")
	}))
	defer server.Close()

	executor := NewCodexExecutor(nil)
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}
	req := cliproxyexecutor.Request{
		Model:   "gpt-image-2",
		Payload: []byte(`{"model":"gpt-image-2","prompt":"draw","response_format":"b64_json"}`),
	}
	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString(codexOpenAIImageSourceFormat),
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: codexImagesGenerationsPath,
		},
	}

	resp, err := executor.executeOpenAIImage(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("executeOpenAIImage() error = %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("calls = %d, want 2", got)
	}
	if got := gjson.GetBytes(resp.Payload, "data.0.b64_json").String(); got != "aGVsbG8=" {
		t.Fatalf("b64_json = %q, want aGVsbG8=; body=%s", got, string(resp.Payload))
	}
}

func TestCodexOpenAIImageStreamStatusErrWrapsHTTP2Reset(t *testing.T) {
	err := codexOpenAIImageStreamStatusErr(fmt.Errorf("stream ID 13; INTERNAL_ERROR; received from peer"))
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error type = %T, want StatusCode", err)
	}
	if got := statusErr.StatusCode(); got != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d", got, http.StatusGatewayTimeout)
	}
}
