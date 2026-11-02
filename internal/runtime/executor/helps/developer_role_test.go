package helps

import (
	"bytes"
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeDeveloperRole(t *testing.T) {
	t.Run("converts only message roles", func(t *testing.T) {
		payload := []byte(`{"model":"deepseek-flash","messages":[{"role":"developer","content":"first developer"},{"role":"user","content":"developer"},{"role":"assistant","content":[{"type":"text","text":"developer"}],"tool_calls":[{"id":"call_1","type":"function","function":{"name":"inspect","arguments":"{}"}}]},{"role":"developer","content":[{"type":"text","text":"last developer"}]}],"metadata":{"developer":"unchanged"}}`)

		got := NormalizeDeveloperRole(payload)
		messages := gjson.GetBytes(got, "messages").Array()
		if len(messages) != 4 {
			t.Fatalf("message count = %d, want 4; output=%s", len(messages), got)
		}
		wantRoles := []string{"system", "user", "assistant", "system"}
		for index, message := range messages {
			if role := message.Get("role").String(); role != wantRoles[index] {
				t.Fatalf("message %d role = %q, want %q; output=%s", index, role, wantRoles[index], got)
			}
		}
		if gotText := messages[1].Get("content").String(); gotText != "developer" {
			t.Fatalf("user content changed to %q", gotText)
		}
		if gotText := messages[2].Get("content.0.text").String(); gotText != "developer" {
			t.Fatalf("assistant content changed to %q", gotText)
		}
		if gotMetadata := gjson.GetBytes(got, "metadata.developer").String(); gotMetadata != "unchanged" {
			t.Fatalf("metadata changed to %q", gotMetadata)
		}
		if gotCallID := messages[2].Get("tool_calls.0.id").String(); gotCallID != "call_1" {
			t.Fatalf("tool call id = %q, want call_1", gotCallID)
		}
	})

	t.Run("returns payload unchanged when no developer role exists", func(t *testing.T) {
		payload := []byte(`{"messages":[{"role":"system","content":"system"},{"role":"user","content":"developer"}]}`)
		if got := NormalizeDeveloperRole(payload); !bytes.Equal(got, payload) {
			t.Fatalf("payload changed unexpectedly: %s", got)
		}
	})

	t.Run("returns payload unchanged without a messages array", func(t *testing.T) {
		payload := []byte(`{"input":[{"role":"developer","content":"instructions"}]}`)
		if got := NormalizeDeveloperRole(payload); !bytes.Equal(got, payload) {
			t.Fatalf("payload changed unexpectedly: %s", got)
		}
	})

	t.Run("handles empty payload", func(t *testing.T) {
		if got := NormalizeDeveloperRole(nil); len(got) != 0 {
			t.Fatalf("expected empty payload, got %s", got)
		}
	})
}
