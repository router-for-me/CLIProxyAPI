package executor

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	_ "github.com/router-for-me/CLIProxyAPI/v7/internal/translator"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestCodexCountTokensClaudeContract(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{})
	response, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Count these tokens"}],"context_management":{"edits":[]}}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}

	inputTokens := gjson.GetBytes(response.Payload, "input_tokens").Int()
	if inputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want positive estimate; payload=%s", inputTokens, response.Payload)
	}
	if got := gjson.GetBytes(response.Payload, "context_management.original_input_tokens").Int(); got != inputTokens {
		t.Fatalf("original_input_tokens = %d, want %d; payload=%s", got, inputTokens, response.Payload)
	}
	if got := response.Headers.Get("X-CLIProxyAPI-Token-Count-Mode"); got != "estimate" {
		t.Fatalf("token count mode = %q, want estimate", got)
	}
}

func TestCodexCountTokensRejectsLossyClaudeRequest(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{})
	_, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Count these tokens"}],"stop_sequences":["END"]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err == nil {
		t.Fatal("CountTokens error = nil, want rejection")
	}
	var requestScoped interface{ IsRequestScoped() bool }
	if !errors.As(err, &requestScoped) || !requestScoped.IsRequestScoped() {
		t.Fatalf("error %T is not request-scoped: %v", err, err)
	}
	var status interface{ StatusCode() int }
	if !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("error status = %v, want %d", err, http.StatusBadRequest)
	}
}

func TestCodexCountTokensClaudeOmitsContextManagementWhenNotRequested(t *testing.T) {
	executor := NewCodexExecutor(&config.Config{})
	response, err := executor.CountTokens(context.Background(), nil, cliproxyexecutor.Request{
		Model:   "gpt-5.4",
		Payload: []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"Count these tokens"}]}`),
	}, cliproxyexecutor.Options{
		SourceFormat:   sdktranslator.FormatClaude,
		ResponseFormat: sdktranslator.FormatClaude,
	})
	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}
	if gjson.GetBytes(response.Payload, "context_management").Exists() {
		t.Fatalf("unexpected context_management; payload=%s", response.Payload)
	}
}
