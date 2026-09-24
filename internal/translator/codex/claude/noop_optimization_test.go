package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodexNormalizesNonStringToolName(t *testing.T) {
	input := []byte(`{"messages":[],"tools":[{"name":123,"input_schema":{"type":"object"}}]}`)

	output := ConvertClaudeRequestToCodex("gpt-test", input, false)

	name := gjson.GetBytes(output, "tools.0.name")
	// SanitizedFunctionNameMap runs SanitizeFunction which prepends _ for digit-starting names
	if name.Type != gjson.String || name.String() != "_123" {
		t.Fatalf("tools.0.name = %s, want string _123", name.Raw)
	}
}
