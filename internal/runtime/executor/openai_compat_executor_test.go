package executor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

const minimalOpenAIResponse = `{"id":"gen-1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`

func TestOpenAICompatExecutor_KimiUserAgent(t *testing.T) {
	var gotUA string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(minimalOpenAIResponse))
	}))
	defer stub.Close()

	cfg := &config.Config{}
	e := NewOpenAICompatExecutor("kimi", cfg)
	auth := &cliproxyauth.Auth{
		Provider:   "kimi",
		Attributes: map[string]string{"base_url": stub.URL, "api_key": "sk-test"},
	}
	payload := []byte(`{"model":"kimi-for-coding","messages":[{"role":"user","content":"hi"}]}`)
	req := cliproxyexecutor.Request{Model: "kimi-for-coding", Payload: payload}
	opts := cliproxyexecutor.Options{
		Stream:       false,
		SourceFormat: sdktranslator.FormatOpenAI,
	}

	resp, err := e.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("empty response payload")
	}
	if gotUA != KimiUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, KimiUserAgent)
	}
}

func TestOpenAICompatExecutor_NonKimiUserAgent(t *testing.T) {
	var gotUA string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(minimalOpenAIResponse))
	}))
	defer stub.Close()

	cfg := &config.Config{}
	e := NewOpenAICompatExecutor("openrouter", cfg)
	auth := &cliproxyauth.Auth{
		Provider:   "openrouter",
		Attributes: map[string]string{"base_url": stub.URL, "api_key": "sk-or-xxx"},
	}
	payload := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	req := cliproxyexecutor.Request{Model: "gpt-4", Payload: payload}
	opts := cliproxyexecutor.Options{
		Stream:       false,
		SourceFormat: sdktranslator.FormatOpenAI,
	}

	_, err := e.Execute(context.Background(), auth, req, opts)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	want := "cli-proxy-openai-compat"
	if gotUA != want {
		t.Errorf("User-Agent = %q, want %q", gotUA, want)
	}
}

func TestOpenAICompatExecutor_PrepareRequest_KimiUserAgent(t *testing.T) {
	var gotUA string
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer stub.Close()

	cfg := &config.Config{}
	e := NewOpenAICompatExecutor("kimi", cfg)
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{"base_url": stub.URL, "api_key": "sk-kimi"},
	}
	body := bytes.NewReader([]byte(`{}`))
	req, err := http.NewRequest(http.MethodPost, stub.URL+"/chat/completions", body)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.PrepareRequest(req, auth); err != nil {
		t.Fatal(err)
	}
	client := stub.Client()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotUA != KimiUserAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, KimiUserAgent)
	}
}

func TestNormalizeKimiOpenAIResponse(t *testing.T) {
	tests := []struct {
		name   string
		input  []byte
		output string // gjson path to check, or "unchanged" to expect input equality
		want   string
	}{
		{
			name:  "reasoning_content only (message)",
			input: []byte(`{"choices":[{"message":{"role":"assistant","content":"","reasoning_content":"hello"}}]}`),
			output: "choices.0.message.content",
			want:   "hello",
		},
		{
			name:  "reasoning_content only (delta)",
			input: []byte(`{"choices":[{"delta":{"content":null,"reasoning_content":"hi"}}]}`),
			output: "choices.0.delta.content",
			want:   "hi",
		},
		{
			name:   "content already set",
			input:  []byte(`{"choices":[{"message":{"content":"ok","reasoning_content":"skip"}}]}`),
			output: "choices.0.message.content",
			want:   "ok",
		},
		{
			name:   "DONE unchanged",
			input:  []byte("[DONE]"),
			output: "unchanged",
			want:   "[DONE]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := normalizeKimiOpenAIResponse(tt.input)
			if tt.output == "unchanged" {
				if string(got) != tt.want {
					t.Errorf("got %q, want %q", got, tt.want)
				}
				return
			}
			v := string(got)
			if len(v) < 2 {
				t.Fatalf("normalize returned too short: %q", v)
			}
			c := gjson.Get(v, tt.output)
			if c.String() != tt.want {
				t.Errorf("%s = %q, want %q", tt.output, c.String(), tt.want)
			}
		})
	}
}
