package util

import (
	"strings"
	"testing"
)

func TestCleanJSONSchemaForGeminiInjectsMissingArrayItems(t *testing.T) {
	in := `{"type":"object","properties":{"params":{"type":"array"},"nested":{"type":"object","properties":{"times":{"type":["array","null"]}}},"ok":{"type":"array","items":{"type":"number"}}}}`
	for _, out := range []string{CleanJSONSchemaForGemini(in), CleanJSONSchemaForAntigravity(in)} {
		if strings.Contains(out, `"params":{"type":"array"}`) {
			t.Fatalf("top-level array still missing items: %s", out)
		}
		if !strings.Contains(out, `"items":{"type":"number"}`) {
			t.Fatalf("existing items were altered: %s", out)
		}
		if strings.Count(out, `"items":{"type":"string"}`) < 2 {
			t.Fatalf("placeholder items not injected for both bare arrays: %s", out)
		}
	}
}
