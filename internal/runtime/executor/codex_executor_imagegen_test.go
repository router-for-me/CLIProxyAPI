package executor

import (
	"context"
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestCodexImageGenerationPolicy_DoesNotAutoInjectForPlainResponses(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","input":"hello"}`)

	result, err := applyCodexImageGenerationToolPolicy(context.Background(), "CodexExecutor", body, "gpt-5.5", "gpt-5.5", "/v1/responses", nil)
	if err != nil {
		t.Fatalf("applyCodexImageGenerationToolPolicy() error = %v", err)
	}
	if string(result) != string(body) {
		t.Fatalf("body changed unexpectedly: got %s want %s", result, body)
	}
	if gjson.GetBytes(result, "tools").Exists() {
		t.Fatalf("expected no injected tools, got %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestCodexImageGenerationPolicy_PreservesExplicitImageGenerationTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation","output_format":"webp"},{"type":"function","name":"lookup"}]}`)

	result, err := applyCodexImageGenerationToolPolicy(context.Background(), "CodexExecutor", body, "gpt-5.5", "gpt-5.5", "/v1/responses", nil)
	if err != nil {
		t.Fatalf("applyCodexImageGenerationToolPolicy() error = %v", err)
	}
	if got := gjson.GetBytes(result, "tools.0.output_format").String(); got != "webp" {
		t.Fatalf("output_format = %q, want webp", got)
	}
	if got := len(gjson.GetBytes(result, "tools").Array()); got != 2 {
		t.Fatalf("tools len = %d, want 2", got)
	}
}

func TestCodexImageGenerationPolicy_RejectsSparkModelExplicitTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.3-codex-spark","tools":[{"type":"image_generation","output_format":"png"}]}`)

	_, err := applyCodexImageGenerationToolPolicy(context.Background(), "CodexExecutor", body, "gpt-5.3-codex-spark", "gpt-5.3-codex-spark", "/v1/responses", nil)
	if err == nil {
		t.Fatal("expected unsupported image_generation error")
	}
	status, ok := err.(interface{ StatusCode() int })
	if !ok {
		t.Fatalf("error does not expose StatusCode(): %T", err)
	}
	if got := status.StatusCode(); got != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", got, http.StatusBadRequest)
	}
	if got := gjson.Get(err.Error(), "error.code").String(); got != "unsupported_builtin_tool" {
		t.Fatalf("error.code = %q, want unsupported_builtin_tool", got)
	}
}

func TestCodexImageGenerationPolicy_RejectsFreePlanExplicitTool(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","tools":[{"type":"image_generation","output_format":"png"}]}`)
	freeAuth := &cliproxyauth.Auth{
		Provider:   "codex",
		Attributes: map[string]string{"plan_type": "free"},
	}

	_, err := applyCodexImageGenerationToolPolicy(context.Background(), "CodexExecutor", body, "gpt-5.5", "gpt-5.5", "/v1/responses", freeAuth)
	if err == nil {
		t.Fatal("expected unsupported image_generation error")
	}
	if got := gjson.Get(err.Error(), "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error", got)
	}
}
