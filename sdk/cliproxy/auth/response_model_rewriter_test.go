package auth

import (
	"bytes"
	"testing"
)

func TestRewriteModelInResponse(t *testing.T) {
	t.Parallel()

	data := []byte(`{"model":"claude-opus-4-5","response":{"model":"gpt-4"}}`)
	result := rewriteModelInResponse(data, "alias-model")

	if !bytes.Contains(result, []byte(`"model":"alias-model"`)) {
		t.Fatalf("expected model to be rewritten, got: %s", string(result))
	}
}

func TestStreamRewriter_RewritesModel(t *testing.T) {
	t.Parallel()

	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "alias-model"})

	chunk := []byte("data: {\"model\":\"claude-opus-4-5\"}\n\n")
	out := rewriter.RewriteChunk(chunk)

	if !bytes.Contains(out, []byte(`"model":"alias-model"`)) {
		t.Fatalf("expected model to be rewritten, got output: %s", string(out))
	}
}

func TestStreamRewriter_RewritesResponseModel(t *testing.T) {
	t.Parallel()

	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: "alias-model"})

	chunk := []byte("data: {\"response\":{\"model\":\"gpt-5\"}}\n\n")
	out := rewriter.RewriteChunk(chunk)

	if !bytes.Contains(out, []byte(`"model":"alias-model"`)) {
		t.Fatalf("expected response.model to be rewritten, got output: %s", string(out))
	}
}

func TestStreamRewriter_NoRewriteWhenEmpty(t *testing.T) {
	t.Parallel()

	rewriter := NewStreamRewriter(StreamRewriteOptions{RewriteModel: ""})

	chunk := []byte("data: {\"model\":\"original\"}\n\n")
	out := rewriter.RewriteChunk(chunk)

	if !bytes.Equal(out, chunk) {
		t.Fatalf("expected no rewrite when RewriteModel is empty")
	}
}
