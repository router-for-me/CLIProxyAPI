package executor

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGeminiStreamPreservesGroundingBeforeTerminalUsage(t *testing.T) {
	capture := &websocketUsageCapture{authID: t.Name()}
	usage.RegisterNamedPlugin(t.Name(), capture)
	defer usage.RegisterNamedPlugin(t.Name(), &websocketUsageCapture{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"weather\"}]},\"groundingMetadata\":{\"webSearchQueries\":[\"weather\"]}}]}\n\n")
		_, _ = fmt.Fprint(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":100,\"candidatesTokenCount\":10,\"totalTokenCount\":110}}\n\n")
	}))
	defer server.Close()
	auth := &cliproxyauth.Auth{ID: t.Name(), Provider: "gemini", Attributes: map[string]string{"api_key": "test", "base_url": server.URL}}
	req := cliproxyexecutor.Request{Model: "gemini-2.5-flash", Payload: []byte(`{"contents":[{"role":"user","parts":[{"text":"weather"}]}]}`)}
	result, errStream := NewGeminiExecutor(&config.Config{}).ExecuteStream(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatGemini})
	if errStream != nil {
		t.Fatal(errStream)
	}
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.records) != 1 {
		t.Fatalf("got %d usage records, want one terminal event", len(capture.records))
	}
	detail := capture.records[0].Detail
	if detail.TotalTokens != 110 || !gjson.Get(detail.RawUsage, "unpriced_server_tools").Bool() {
		t.Fatalf("native stream discarded early grounding: %+v", detail)
	}
}
