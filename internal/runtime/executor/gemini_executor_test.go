package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeGeminiUpstreamBody_RemovesUnsupportedToolInvocationFields(t *testing.T) {
	in := []byte(`{"toolConfig":{"includeServerSideToolInvocations":true,"functionCallingConfig":{"mode":"AUTO"}},"tool_config":{"include_server_side_tool_invocations":true},"request":{"toolConfig":{"includeServerSideToolInvocations":true},"tool_config":{"include_server_side_tool_invocations":true}}}`)
	out := sanitizeGeminiUpstreamBody(in)

	paths := []string{
		"toolConfig.includeServerSideToolInvocations",
		"tool_config.include_server_side_tool_invocations",
		"request.toolConfig.includeServerSideToolInvocations",
		"request.tool_config.include_server_side_tool_invocations",
	}
	for _, path := range paths {
		if gjson.GetBytes(out, path).Exists() {
			t.Fatalf("path still exists after sanitize: %s => %s", path, string(out))
		}
	}
	if got := gjson.GetBytes(out, "toolConfig.functionCallingConfig.mode").String(); got != "AUTO" {
		t.Fatalf("unexpected functionCallingConfig.mode after sanitize: %q", got)
	}
}
