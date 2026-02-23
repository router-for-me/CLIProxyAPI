package executor

import (
	"testing"

	"github.com/tiktoken-go/tokenizer"
)

func TestCountCodexInputTokens_FunctionCallOutputObjectIncluded(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		t.Fatalf("tokenizer init failed: %v", err)
	}

	body := []byte(`{"input":[{"type":"function_call_output","output":{"ok":true,"items":[1,2,3]}}]}`)
	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		t.Fatalf("countCodexInputTokens failed: %v", err)
	}
	if count <= 0 {
		t.Fatalf("count = %d, want > 0", count)
	}
}

func TestCountCodexInputTokens_FunctionCallArgumentsObjectIncluded(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		t.Fatalf("tokenizer init failed: %v", err)
	}

	body := []byte(`{"input":[{"type":"function_call","name":"sum","arguments":{"a":1,"b":2}}]}`)
	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		t.Fatalf("countCodexInputTokens failed: %v", err)
	}
	if count <= 0 {
		t.Fatalf("count = %d, want > 0", count)
	}
}

func TestCountCodexInputTokens_FunctionCallArgumentsObjectSerializationParity(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.Cl100kBase)
	if err != nil {
		t.Fatalf("tokenizer init failed: %v", err)
	}

	objectBody := []byte(`{"input":[{"type":"function_call","name":"sum","arguments":{"a":1,"b":{"nested":true},"items":[1,2,3]}}]}`)
	stringBody := []byte(`{"input":[{"type":"function_call","name":"sum","arguments":"{\"a\":1,\"b\":{\"nested\":true},\"items\":[1,2,3]}"}]}`)

	objectCount, err := countCodexInputTokens(enc, objectBody)
	if err != nil {
		t.Fatalf("countCodexInputTokens object failed: %v", err)
	}
	stringCount, err := countCodexInputTokens(enc, stringBody)
	if err != nil {
		t.Fatalf("countCodexInputTokens string failed: %v", err)
	}

	if objectCount <= 0 || stringCount <= 0 {
		t.Fatalf("counts must be positive, object=%d string=%d", objectCount, stringCount)
	}
	if objectCount != stringCount {
		t.Fatalf("object vs string count mismatch: object=%d string=%d", objectCount, stringCount)
	}
}
