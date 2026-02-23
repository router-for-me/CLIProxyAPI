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
