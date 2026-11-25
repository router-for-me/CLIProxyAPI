package thinking

import (
	"bytes"
	"strings"
	"testing"
	"unsafe"
)

func TestUsageEffortDoesNotRetainRequestBuffer(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		read      func([]byte) string
	}{
		{"client Claude effort", `{"output_config":{"effort":"high"},"content":"%s"}`, func(b []byte) string { return ExtractReasoningEffort(b, "claude", "gpt-5.4") }},
		{"final Codex effort", `{"reasoning":{"effort":"high"},"input":"%s"}`, func(b []byte) string { return ExtractTranslatedReasoningEffort(b, "codex") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(strings.Replace(tc.raw, "%s", strings.Repeat("x", 1<<20), 1))
			result := tc.read(body)
			if result != "high" {
				t.Fatalf("got effort %q", result)
			}
			index := bytes.Index(body, []byte("high"))
			if unsafe.StringData(result) == &body[index] {
				t.Fatal("the saved effort points into the full 1 MiB JSON buffer")
			}
		})
	}
}
