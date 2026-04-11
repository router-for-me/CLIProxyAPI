package openai

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestRewriteModelAliasInChunk_RewritesTopLevelModel(t *testing.T) {
	chunk := []byte(`{"id":"chatcmpl-1","model":"qwen3-coder-flash","choices":[]}`)
	rewritten := rewriteModelAliasInChunk(chunk, "111")

	if got := gjson.GetBytes(rewritten, "model").String(); got != "111" {
		t.Fatalf("model = %q, want %q", got, "111")
	}
}

func TestRewriteModelAliasInChunk_RewritesResponsesCompletedModel(t *testing.T) {
	chunk := []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"model\":\"qwen3-coder-flash\",\"status\":\"completed\"}}\n")
	rewritten := rewriteModelAliasInChunk(chunk, "111")

	if strings.Contains(string(rewritten), "qwen3-coder-flash") {
		t.Fatalf("expected upstream model removed, got %q", string(rewritten))
	}
	if !strings.Contains(string(rewritten), `"model":"111"`) {
		t.Fatalf("expected requested model in chunk, got %q", string(rewritten))
	}
}

func TestRewriteModelAliasInChunk_LeavesDoneMarkerUntouched(t *testing.T) {
	chunk := []byte("event: response.completed\ndata: [DONE]\n")
	rewritten := rewriteModelAliasInChunk(chunk, "111")

	if string(rewritten) != string(chunk) {
		t.Fatalf("expected chunk unchanged, got %q", string(rewritten))
	}
}
