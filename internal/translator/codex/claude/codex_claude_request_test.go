package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodex_StripsDeferLoadingAndCacheControl(t *testing.T) {
	in := []byte(`{
		"model":"any",
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{
			"name":"t1",
			"description":"d",
			"input_schema":{"type":"object","properties":{}},
			"defer_loading":true,
			"cache_control":{"type":"ephemeral"}
		}]
	}`)

	out := ConvertClaudeRequestToCodex("gpt-5.3-codex", in, false)

	if gjson.GetBytes(out, "tools.0.defer_loading").Exists() {
		t.Fatalf("tools.0.defer_loading should be stripped: %s", string(out))
	}
	if gjson.GetBytes(out, "tools.0.cache_control").Exists() {
		t.Fatalf("tools.0.cache_control should be stripped: %s", string(out))
	}
}
