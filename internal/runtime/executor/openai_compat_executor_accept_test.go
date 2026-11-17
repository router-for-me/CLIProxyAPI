package executor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestOpenAICompatExecutorNonStreamSendsAcceptJSON(t *testing.T) {
	for _, tc := range []struct {
		name       string
		attributes map[string]string
		want       string
	}{
		{name: "default", want: "application/json"},
		{name: "custom header wins", attributes: map[string]string{"header:Accept": "application/x-ndjson"}, want: "application/x-ndjson"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotAccept string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAccept = r.Header.Get("Accept")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			}))
			defer server.Close()

			executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{Name: "compat"}},
			})
			attributes := map[string]string{
				"base_url":     server.URL + "/v1",
				"api_key":      "test",
				"compat_name":  "compat",
				"provider_key": "compat",
			}
			for key, value := range tc.attributes {
				attributes[key] = value
			}
			auth := &cliproxyauth.Auth{Provider: "openai-compatibility", Attributes: attributes}
			_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "m",
				Payload: []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`),
			}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FromString("openai"), Stream: false})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}
			if gotAccept != tc.want {
				t.Fatalf("Accept = %q, want %q", gotAccept, tc.want)
			}
		})
	}
}
