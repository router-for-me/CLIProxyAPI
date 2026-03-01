package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestToolMessageContentToString_EdgeCases(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "null", raw: `null`, want: ""},
		{name: "string null placeholder", raw: `"null"`, want: ""},
		{name: "empty array", raw: `[]`, want: ""},
		{name: "array with only empty items", raw: `[null,"","null",{"type":"text","text":" "},{"text":"null"}]`, want: ""},
		{name: "array filters null placeholder and keeps value", raw: `["ok","null",{"type":"text","text":"done"}]`, want: "ok\n\ndone"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toolMessageContentToString(gjson.Parse(tt.raw))
			if got != tt.want {
				t.Fatalf("toolMessageContentToString(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestOpenAICompatExecutor_ToolResultForceString_NonStream(t *testing.T) {
	tests := []struct {
		name         string
		forceString  bool
		wantIsString bool
	}{
		{name: "default keep multimodal", forceString: false, wantIsString: false},
		{name: "force string", forceString: true, wantIsString: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotBody []byte
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				gotBody = body
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()

			authAttrs := map[string]string{
				"base_url": server.URL + "/v1",
				"api_key":  "test-key",
			}
			if tt.forceString {
				authAttrs["tool_result_force_string"] = "true"
			}

			exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
			payload := []byte(`{
				"model":"gpt-5.1-codex-max",
				"messages":[
					{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"demo","arguments":"{}"}}]},
					{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"ok"},{"type":"image_url","image_url":{"url":"https://example.com/a.png"}}]}
				]
			}`)

			_, err := exec.Execute(context.Background(), &cliproxyauth.Auth{Attributes: authAttrs}, cliproxyexecutor.Request{
				Model:   "gpt-5.1-codex-max",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString("openai"),
				Stream:       false,
			})
			if err != nil {
				t.Fatalf("Execute error: %v", err)
			}

			content := gjson.GetBytes(gotBody, "messages.1.content")
			if content.Type == gjson.String {
				if !tt.wantIsString {
					t.Fatalf("tool content unexpectedly string: %q", content.String())
				}
				if !strings.Contains(content.String(), "https://example.com/a.png") {
					t.Fatalf("forced string content should contain image url, got %q", content.String())
				}
				return
			}

			if tt.wantIsString {
				t.Fatalf("tool content expected string, got %s", content.Raw)
			}
			if !content.IsArray() {
				t.Fatalf("tool content expected array, got %s", content.Raw)
			}
			if got := gjson.GetBytes(gotBody, "messages.1.content.1.image_url.url").String(); got != "https://example.com/a.png" {
				t.Fatalf("tool image url = %q, want %q", got, "https://example.com/a.png")
			}
		})
	}
}

func TestOpenAICompatExecutor_ToolResultForceString_Stream(t *testing.T) {
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl_1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"}}]}\\n\\n")
		_, _ = io.WriteString(w, "data: [DONE]\\n\\n")
	}))
	defer server.Close()

	exec := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	payload := []byte(`{
		"model":"gpt-5.1-codex-max",
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"demo","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"ok"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]}
		],
		"stream":true
	}`)

	streamResp, err := exec.ExecuteStream(context.Background(), &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url":                 server.URL + "/v1",
		"api_key":                  "test-key",
		"tool_result_force_string": "true",
	}}, cliproxyexecutor.Request{
		Model:   "gpt-5.1-codex-max",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       true,
	})
	if err != nil {
		t.Fatalf("ExecuteStream error: %v", err)
	}

	for chunk := range streamResp.Chunks {
		if chunk.Err != nil {
			t.Fatalf("stream chunk error: %v", chunk.Err)
		}
	}

	content := gjson.GetBytes(gotBody, "messages.1.content")
	if content.Type != gjson.String {
		t.Fatalf("tool content expected string, got %s", content.Raw)
	}
	if !strings.Contains(content.String(), "data:image/png;base64,QUJD") {
		t.Fatalf("forced string content should contain data url, got %q", content.String())
	}
}
