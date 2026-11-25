package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestGeminiVertexStream_FilterSSEUsageMetadata(t *testing.T) {
	upstreamSSE := strings.Join([]string{
		`data: {"candidates":[{"content":{"parts":[{"text":"hel"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12},"modelVersion":"gemini-2.5-flash","responseId":"vertex-filter-usage-test"}`,
		``,
		`data: {"candidates":[{"content":{"parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-flash","responseId":"vertex-filter-usage-test"}`,
		``,
		``,
	}, "\n")

	formats := []struct {
		name           string
		sourceFormat   sdktranslator.Format
		responseFormat sdktranslator.Format
		payload        []byte
	}{
		{
			name:           "gemini",
			sourceFormat:   sdktranslator.FormatGemini,
			responseFormat: sdktranslator.FormatGemini,
			payload:        []byte(`{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`),
		},
		{
			name:           "openai",
			sourceFormat:   sdktranslator.FormatOpenAI,
			responseFormat: sdktranslator.FormatOpenAI,
			payload:        []byte(`{"model":"gemini-2.5-flash","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		},
	}

	for _, provider := range []string{"api_key", "service_account"} {
		t.Run(provider, func(t *testing.T) {
			for _, tc := range formats {
				t.Run(tc.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/token" {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"access_token":"local-token","token_type":"Bearer","expires_in":3600}`)
							return
						}
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, upstreamSSE)
					}))
					defer server.Close()

					ctx := context.Background()
					auth := &cliproxyauth.Auth{ID: t.Name()}
					if provider == "api_key" {
						auth.Attributes = map[string]string{"api_key": "test-key", "base_url": server.URL}
					} else {
						var serviceAccount map[string]any
						if errUnmarshal := json.Unmarshal(testVertexServiceAccountJSON(t, server.URL+"/token"), &serviceAccount); errUnmarshal != nil {
							t.Fatal(errUnmarshal)
						}
						auth.Metadata = map[string]any{
							"project_id":      "proxy-test",
							"location":        "global",
							"service_account": serviceAccount,
						}
						target, errParse := parseTestURL(t, server.URL)
						if errParse != nil {
							t.Fatal(errParse)
						}
						transport := http.DefaultTransport.(*http.Transport).Clone()
						transport.Proxy = nil
						t.Cleanup(transport.CloseIdleConnections)
						ctx = context.WithValue(ctx, "cliproxy.roundtripper", issue6258RedirectTransport{target: target, base: transport})
					}

					result, errExecute := NewGeminiVertexExecutor(&config.Config{}).ExecuteStream(ctx, auth, cliproxyexecutor.Request{
						Model:   "gemini-2.5-flash",
						Payload: tc.payload,
					}, cliproxyexecutor.Options{
						SourceFormat:   tc.sourceFormat,
						ResponseFormat: tc.responseFormat,
						Stream:         true,
					})
					if errExecute != nil {
						t.Fatalf("ExecuteStream() error = %v", errExecute)
					}

					var payloads []string
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("unexpected stream error: %v", chunk.Err)
						}
						for _, line := range strings.Split(string(chunk.Payload), "\n") {
							trimmed := strings.TrimSpace(line)
							if trimmed == "" {
								continue
							}
							data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
							if data == "" || data == "[DONE]" {
								continue
							}
							payloads = append(payloads, data)
						}
					}

					if len(payloads) < 2 {
						t.Fatalf("expected at least 2 SSE data payloads, got %d: %v", len(payloads), payloads)
					}

					var sawIntermediate, sawTerminal bool
					for _, p := range payloads {
						switch tc.responseFormat {
						case sdktranslator.FormatGemini:
							if strings.Contains(p, `"hel"`) {
								sawIntermediate = true
								if ojson.Get(p, "usageMetadata").Exists() {
									t.Errorf("intermediate Gemini SSE chunk leaked usageMetadata: %s", p)
								}
								if !ojson.Get(p, "cpaUsageMetadata").Exists() {
									t.Errorf("intermediate Gemini SSE chunk missing cpaUsageMetadata: %s", p)
								}
							}
							if ojson.Get(p, "candidates.0.finishReason").String() == "STOP" {
								sawTerminal = true
								if got := ojson.Get(p, "usageMetadata.totalTokenCount").Int(); got != 15 {
									t.Errorf("terminal Gemini SSE chunk usageMetadata.totalTokenCount = %d, want 15; payload: %s", got, p)
								}
							}
						case sdktranslator.FormatOpenAI:
							if strings.Contains(p, `"hel"`) {
								sawIntermediate = true
								if ojson.Get(p, "usage").Exists() {
									t.Errorf("intermediate OpenAI SSE chunk leaked usage: %s", p)
								}
							}
							if ojson.Get(p, "choices.0.finish_reason").String() == "stop" {
								sawTerminal = true
								if got := ojson.Get(p, "usage.total_tokens").Int(); got != 15 {
									t.Errorf("terminal OpenAI SSE chunk usage.total_tokens = %d, want 15; payload: %s", got, p)
								}
							}
						}
					}

					if !sawIntermediate {
						t.Fatalf("did not observe intermediate chunk in payloads: %v", payloads)
					}
					if !sawTerminal {
						t.Fatalf("did not observe terminal chunk in payloads: %v", payloads)
					}
				})
			}
		})
	}
}
