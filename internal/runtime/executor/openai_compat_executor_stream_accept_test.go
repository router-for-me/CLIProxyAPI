package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatExecutorStreamAcceptHeader(t *testing.T) {
	for _, tc := range []struct {
		name       string
		attributes map[string]string
		wantAccept string
	}{
		{
			name:       "default ignores unconfigured client Accept",
			wantAccept: "text/event-stream",
		},
		{
			name:       "custom Accept overrides default",
			attributes: map[string]string{"header:Accept": "application/json"},
			wantAccept: "application/json",
		},
		{
			name:       "lowercase custom Accept overrides default",
			attributes: map[string]string{"header:accept": "application/json"},
			wantAccept: "application/json",
		},
		{
			name:       "configured client Accept overrides default",
			attributes: map[string]string{"header:Accept": "$Accept"},
			wantAccept: "application/json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			type capturedRequest struct {
				accept string
				body   []byte
			}
			requests := make(chan capturedRequest, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, errRead := io.ReadAll(r.Body)
				if errRead != nil {
					http.Error(w, "cannot read request", http.StatusBadRequest)
					return
				}
				requests <- capturedRequest{accept: r.Header.Get("Accept"), body: body}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()

			auth := &cliproxyauth.Auth{
				Provider: "openai-compatibility",
				Attributes: map[string]string{
					"base_url": server.URL + "/v1",
					"api_key":  "test-key",
				},
			}
			for key, value := range tc.attributes {
				auth.Attributes[key] = value
			}
			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "test-model",
				Payload: []byte(`{"model":"test-model","messages":[{"role":"user","content":"hi"}],"stream":true}`),
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai"),
				Headers:      http.Header{"Accept": {"application/json"}},
				Stream:       true,
			})
			if err != nil {
				t.Fatalf("ExecuteStream error: %v", err)
			}

			var content strings.Builder
			var sawFinishReason bool
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected stream error: %v", chunk.Err)
				}
				content.WriteString(ojson.GetBytes(chunk.Payload, "choices.0.delta.content").String())
				if ojson.GetBytes(chunk.Payload, "choices.0.finish_reason").String() == "stop" {
					sawFinishReason = true
				}
			}
			got := <-requests
			if got.accept != tc.wantAccept {
				t.Errorf("Accept = %q, want %q", got.accept, tc.wantAccept)
			}
			if !ojson.GetBytes(got.body, "stream").Bool() {
				t.Errorf("upstream request is not streaming: %s", got.body)
			}
			if content.String() != "ok" || !sawFinishReason {
				t.Errorf("incomplete SSE response: content = %q, saw finish reason = %t", content.String(), sawFinishReason)
			}
		})
	}
}
