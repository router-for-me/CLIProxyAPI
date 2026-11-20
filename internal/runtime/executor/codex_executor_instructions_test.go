package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexOutboundSystemInstructions(t *testing.T) {
	const interactions = `{"system_instruction":"Be brief","input":"hi"}`
	for _, tc := range []struct {
		name         string
		from         sdktranslator.Format
		payload      string
		overrideText string
	}{
		{"chat", sdktranslator.FormatOpenAI, `{"messages":[{"role":"system","content":"Be brief"},{"role":"user","content":"hi"}]}`, ""},
		{"claude", sdktranslator.FormatClaude, `{"system":"Be brief","messages":[{"role":"user","content":"hi"}]}`, ""},
		{"gemini", sdktranslator.FormatGemini, `{"systemInstruction":{"parts":[{"text":"Be brief"}]},"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, ""},
		{"interactions", sdktranslator.FormatInteractions, interactions, ""},
		{"interactions_payload_override", sdktranslator.FormatInteractions, interactions, "Configured prefix"},
		{"responses", sdktranslator.FormatOpenAIResponse, `{"instructions":"Be brief","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`, ""},
	} {
		for _, auth := range []struct {
			name      string
			newAuth   func(string) *cliproxyauth.Auth
			wantToken string
		}{
			{"oauth", codexOAuthTestAuth, "oauth-token"},
			{"api_key", codexAPIKeyTestAuth, "sk-test"},
		} {
			for _, transport := range []string{"http", "websocket"} {
				t.Run(tc.name+"/"+auth.name+"/"+transport, func(t *testing.T) {
					server, captured, _ := newCodexHostedImageTestServer(t, transport)
					cfg := &config.Config{}
					wantText := "Be brief"
					if tc.overrideText != "" {
						cfg.Payload.Override = []config.PayloadRule{{
							Models: []config.PayloadModelRule{{Name: "*", Match: []map[string]any{{"input.0.role": "developer"}}}},
							Params: map[string]any{"input.0.content.0.text": tc.overrideText},
						}}
						wantText = tc.overrideText
					}
					executeStream := NewCodexExecutor(cfg).ExecuteStream
					if transport == "websocket" {
						executeStream = NewCodexWebsocketsExecutor(cfg).ExecuteStream
					}
					req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: []byte(tc.payload)}
					opts := cliproxyexecutor.Options{SourceFormat: tc.from, Stream: true}
					result, errExecute := executeStream(t.Context(), auth.newAuth(server.URL), req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
					select {
					case upstream := <-captured:
						if got := upstream.headers.Get("Authorization"); got != "Bearer "+auth.wantToken {
							t.Errorf("Authorization = %q, want Bearer %s", got, auth.wantToken)
						}
						root := ojson.ParseBytes(upstream.body)
						items := root.Get("input").Array()
						if tc.from == sdktranslator.FormatOpenAIResponse {
							if got := root.Get("instructions").String(); got != wantText || len(items) != 1 {
								t.Fatalf("expected Responses instructions unchanged without a developer prefix; body=%s", upstream.body)
							}
						} else {
							if root.Get("instructions").String() != "" || len(items) != 2 {
								t.Fatalf("expected only a developer prefix followed by the user message; body=%s", upstream.body)
							}
							prefix := items[0]
							if prefix.Get("type").String() != "message" || prefix.Get("role").String() != "developer" ||
								prefix.Get("content.#").Int() != 1 || prefix.Get("content.0.type").String() != "input_text" || prefix.Get("content.0.text").String() != wantText {
								t.Errorf("input[0] = %s, want typed developer message with text %q", prefix.Raw, wantText)
							}
						}
						if user := items[len(items)-1]; user.Get("role").String() != "user" || user.Get("content.0.text").String() != "hi" {
							t.Errorf("last input item = %s, want user message with text hi", user.Raw)
						}
					default:
						t.Fatal("upstream received no request")
					}
				})
			}
		}
	}
}

func TestCodexExecutorExecuteNormalizesNullInstructions(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	_, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","instructions":null,"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       false,
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/responses")
	}
	if ojson.GetBytes(gotBody, "instructions").Type != ojson.String {
		t.Fatalf("instructions type = %v, want string", ojson.GetBytes(gotBody, "instructions").Type)
	}
	if ojson.GetBytes(gotBody, "instructions").String() != "" {
		t.Fatalf("instructions = %q, want empty string", ojson.GetBytes(gotBody, "instructions").String())
	}
}

func TestCodexExecutorExecuteStreamNormalizesNullInstructions(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"created_at\":0,\"status\":\"completed\",\"background\":false,\"error\":null}}\n\n"))
	}))
	defer server.Close()

	executor := NewCodexExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL,
		"api_key":  "test",
	}}

	result, err := executor.ExecuteStream(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","instructions":null,"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}
	for range result.Chunks {
	}
	if gotPath != "/responses" {
		t.Fatalf("path = %q, want %q", gotPath, "/responses")
	}
	if ojson.GetBytes(gotBody, "instructions").Type != ojson.String {
		t.Fatalf("instructions type = %v, want string", ojson.GetBytes(gotBody, "instructions").Type)
	}
	if ojson.GetBytes(gotBody, "instructions").String() != "" {
		t.Fatalf("instructions = %q, want empty string", ojson.GetBytes(gotBody, "instructions").String())
	}
}

func TestCodexExecutorCountTokensTreatsNullInstructionsAsEmpty(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{})

	nullResp, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","instructions":null,"input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("CountTokens(null) error: %v", err)
	}

	emptyResp, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","instructions":"","input":"hello"}`),
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-response"),
	})
	if err != nil {
		t.Fatalf("CountTokens(empty) error: %v", err)
	}

	if string(nullResp.Payload) != string(emptyResp.Payload) {
		t.Fatalf("token count payload mismatch:\nnull=%s\nempty=%s", string(nullResp.Payload), string(emptyResp.Payload))
	}
}
