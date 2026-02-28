package fallback

import (
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func rewriteConfig(preserve, expose bool) *config.Config {
	cfg := &config.Config{
		ModelFallback: config.ModelFallback{
			Enabled:                true,
			PreserveRequestedModel: preserve,
			ExposeActualModelHeader: expose,
		},
	}
	cfg.SanitizeModelFallback()
	return cfg
}

func TestRewriteResponseModel_Replaces(t *testing.T) {
	cfg := rewriteConfig(true, false)
	payload := []byte(`{"model":"gpt-4o","choices":[]}`)
	result := RewriteResponseModel(payload, "gpt-4", "gpt-4o", cfg)
	if !strings.Contains(string(result), `"gpt-4"`) {
		t.Errorf("expected model to be rewritten to gpt-4, got %s", result)
	}
}

func TestRewriteResponseModel_SameModel(t *testing.T) {
	cfg := rewriteConfig(true, false)
	payload := []byte(`{"model":"gpt-4","choices":[]}`)
	result := RewriteResponseModel(payload, "gpt-4", "gpt-4", cfg)
	if string(result) != string(payload) {
		t.Errorf("same model should return original payload")
	}
}

func TestRewriteResponseModel_DisabledPreserve(t *testing.T) {
	cfg := rewriteConfig(false, false)
	payload := []byte(`{"model":"gpt-4o","choices":[]}`)
	result := RewriteResponseModel(payload, "gpt-4", "gpt-4o", cfg)
	if string(result) != string(payload) {
		t.Errorf("disabled preserve should return original payload")
	}
}

func TestRewriteStreamChunkModel_RewritesDataPrefix(t *testing.T) {
	cfg := rewriteConfig(true, false)
	chunk := []byte("data: {\"model\":\"gpt-4o\",\"choices\":[]}\n\n")
	result := RewriteStreamChunkModel(chunk, "gpt-4", "gpt-4o", cfg)
	if !strings.Contains(string(result), `"gpt-4"`) {
		t.Errorf("expected stream model rewrite, got %s", result)
	}
	if !strings.HasPrefix(string(result), "data: ") {
		t.Errorf("expected data: prefix preserved, got %s", result)
	}
}

func TestRewriteStreamChunkModel_NonJSON(t *testing.T) {
	cfg := rewriteConfig(true, false)
	chunk := []byte("data: [DONE]\n\n")
	result := RewriteStreamChunkModel(chunk, "gpt-4", "gpt-4o", cfg)
	if string(result) != string(chunk) {
		t.Errorf("non-JSON chunk should be unchanged, got %s", result)
	}
}

func TestSetFallbackHeaders(t *testing.T) {
	cfg := rewriteConfig(true, true)
	headers := http.Header{}
	SetFallbackHeaders(headers, "gpt-4", "gpt-4o", 2, cfg)

	if v := headers.Get(HeaderActualModel); v != "gpt-4o" {
		t.Errorf("X-Actual-Model = %q, want gpt-4o", v)
	}
	if v := headers.Get(HeaderRequestedModel); v != "gpt-4" {
		t.Errorf("X-Requested-Model = %q, want gpt-4", v)
	}
	if v := headers.Get(HeaderFallbackAttempts); v != "2" {
		t.Errorf("X-Model-Fallback-Attempts = %q, want 2", v)
	}
}

func TestRewriteResponseModel_WithThinkingSuffix(t *testing.T) {
	cfg := rewriteConfig(true, false)
	// Response model field is the bare name without suffix
	payload := []byte(`{"model":"gpt-4o","choices":[]}`)
	// actualModel has thinking suffix, requestedModel also has one
	result := RewriteResponseModel(payload, "gpt-4(8192)", "gpt-4o(8192)", cfg)
	if !strings.Contains(string(result), `"gpt-4"`) {
		t.Errorf("expected model to be rewritten to bare gpt-4, got %s", result)
	}
	// Should NOT contain the suffix in the response
	if strings.Contains(string(result), "(8192)") {
		t.Errorf("response model should not contain thinking suffix, got %s", result)
	}
}

func TestRewriteStreamChunkModel_WithThinkingSuffix(t *testing.T) {
	cfg := rewriteConfig(true, false)
	// Stream response has bare model name
	chunk := []byte("data: {\"model\":\"gpt-4o\",\"choices\":[]}\n\n")
	result := RewriteStreamChunkModel(chunk, "gpt-4(8192)", "gpt-4o(8192)", cfg)
	if !strings.Contains(string(result), `"gpt-4"`) {
		t.Errorf("expected stream model rewrite to bare gpt-4, got %s", result)
	}
	if strings.Contains(string(result), "(8192)") {
		t.Errorf("stream model should not contain thinking suffix, got %s", result)
	}
}
