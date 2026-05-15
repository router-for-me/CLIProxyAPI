package executor

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestIsCodexInvalidEncryptedContentError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{
			name: "error code",
			body: []byte(`{"error":{"code":"invalid_encrypted_content","message":"bad encrypted state"}}`),
			want: true,
		},
		{
			name: "plain upstream message",
			body: []byte(`The encrypted content gAAA...5Q== could not be verified. Reason: Encrypted content could not be decrypted or parsed.`),
			want: true,
		},
		{
			name: "unrelated request error",
			body: []byte(`{"error":{"code":"bad_request","message":"invalid schema"}}`),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isCodexInvalidEncryptedContentError(http.StatusBadRequest, tc.body)
			if got != tc.want {
				t.Fatalf("isCodexInvalidEncryptedContentError() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStripCodexEncryptedReasoningState(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"gpt-5.5",
		"include":["reasoning.encrypted_content"],
		"input":[
			{"type":"reasoning","id":"rs_1","encrypted_content":"gAAA","summary":[]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi","encrypted_content":"nested"}]},
			{"type":"compaction","encrypted_content":"QVhO","id":"cmp_1"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"}
		]
	}`)

	out, changed := stripCodexEncryptedReasoningState(body)
	if !changed {
		t.Fatalf("stripCodexEncryptedReasoningState() changed = false")
	}
	if gjson.GetBytes(out, "input.#").Int() != 3 {
		t.Fatalf("input length = %d, want 3: %s", gjson.GetBytes(out, "input.#").Int(), string(out))
	}
	if gjson.GetBytes(out, "input.0.encrypted_content").Exists() {
		t.Fatalf("reasoning encrypted_content should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, "input.1.content.0.encrypted_content").Exists() {
		t.Fatalf("nested encrypted_content should be removed: %s", string(out))
	}
	if gjson.GetBytes(out, `input.#(type=="compaction")`).Exists() {
		t.Fatalf("compaction item should be removed: %s", string(out))
	}
	if got := gjson.GetBytes(out, "include.0").String(); got != "reasoning.encrypted_content" {
		t.Fatalf("include.0 = %q, want reasoning.encrypted_content: %s", got, string(out))
	}
}

func TestStripCodexEncryptedReasoningStateUnchanged(t *testing.T) {
	t.Parallel()

	body := []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`)
	out, changed := stripCodexEncryptedReasoningState(body)
	if changed {
		t.Fatalf("stripCodexEncryptedReasoningState() changed = true: %s", string(out))
	}
}
