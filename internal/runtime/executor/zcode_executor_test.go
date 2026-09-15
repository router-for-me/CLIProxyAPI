package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestZCodeExecutor_RequestToFormat(t *testing.T) {
	e := NewZCodeExecutor(nil)
	if f := e.RequestToFormat(cliproxyexecutor.Request{}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); f != sdktranslator.FormatClaude {
		t.Fatalf("expected claude format, got %v", f)
	}
}

func TestZCodeExecutor_SendsIdentityHeaders(t *testing.T) {
	var gotHeaders http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","type":"message","model":"glm-5.3","content":[{"type":"text","text":"hi"}]}`))
	}))
	defer srv.Close()

	auth := &cliproxyauth.Auth{
		ID: "zcode.json", Provider: "zcode",
		Attributes: map[string]string{
			"api_key":                  "k.s",
			"base_url":                 srv.URL,
			"header:User-Agent":        "ZCode/3.12.0",
			"header:X-ZCode-Agent":     "glm",
			"header:X-Title":           "ZCode",
			"header:X-Client-Lang":     "zh-CN",
			"header:X-Client-Timezone": "Asia/Shanghai",
		},
	}
	reqBody, _ := json.Marshal(map[string]any{
		"model":      "glm-5.3",
		"max_tokens": 1024,
		"messages":   []any{map[string]any{"role": "user", "content": "hi"}},
	})
	req := cliproxyexecutor.Request{Model: "glm-5.3", Payload: reqBody, Format: sdktranslator.FormatClaude}

	e := NewZCodeExecutor(nil)
	resp, err := e.Execute(context.Background(), auth, req, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(resp.Payload) == 0 {
		t.Fatal("expected non-empty response payload")
	}
	if gotHeaders.Get("User-Agent") != "ZCode/3.12.0" {
		t.Fatalf("User-Agent = %q", gotHeaders.Get("User-Agent"))
	}
	if gotHeaders.Get("X-ZCode-Agent") != "glm" {
		t.Fatalf("X-ZCode-Agent = %q", gotHeaders.Get("X-ZCode-Agent"))
	}
	if gotHeaders.Get("X-Client-Lang") != "zh-CN" {
		t.Fatalf("X-Client-Lang = %q", gotHeaders.Get("X-Client-Lang"))
	}
	if gotHeaders.Get("X-Client-Timezone") != "Asia/Shanghai" {
		t.Fatalf("X-Client-Timezone = %q", gotHeaders.Get("X-Client-Timezone"))
	}
}
