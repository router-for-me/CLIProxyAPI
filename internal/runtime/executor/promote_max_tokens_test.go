package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

// max_tokens present, no max_completion_tokens → rename.
func TestPromoteMaxTokens_Rename(t *testing.T) {
	in := []byte(`{"model":"gpt-5","max_tokens":1024,"messages":[]}`)
	out := promoteMaxTokens(in)

	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Error("max_tokens should be removed")
	}
	if gjson.GetBytes(out, "max_completion_tokens").Int() != 1024 {
		t.Errorf("max_completion_tokens = %d, want 1024", gjson.GetBytes(out, "max_completion_tokens").Int())
	}
}

// max_completion_tokens already set → just drop max_tokens, keep existing value.
func TestPromoteMaxTokens_AlreadySet(t *testing.T) {
	in := []byte(`{"max_tokens":512,"max_completion_tokens":2048}`)
	out := promoteMaxTokens(in)

	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Error("max_tokens should be removed")
	}
	if gjson.GetBytes(out, "max_completion_tokens").Int() != 2048 {
		t.Errorf("max_completion_tokens = %d, want 2048 (original)", gjson.GetBytes(out, "max_completion_tokens").Int())
	}
}

// no max_tokens at all → payload unchanged.
func TestPromoteMaxTokens_NoOp(t *testing.T) {
	in := []byte(`{"model":"gpt-5","messages":[]}`)
	out := promoteMaxTokens(in)

	if gjson.GetBytes(out, "max_tokens").Exists() {
		t.Error("unexpected max_tokens")
	}
	if gjson.GetBytes(out, "max_completion_tokens").Exists() {
		t.Error("unexpected max_completion_tokens")
	}
}

// empty/nil payload → no panic.
func TestPromoteMaxTokens_EmptyPayload(t *testing.T) {
	if out := promoteMaxTokens(nil); out != nil {
		t.Error("nil input should return nil")
	}
	if out := promoteMaxTokens([]byte{}); len(out) != 0 {
		t.Error("empty input should return empty")
	}
}
