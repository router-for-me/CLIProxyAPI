package claude

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestConvertClaudeRequestToCodexNormalizesNonStringToolName(t *testing.T) {
	input := []byte(`{"messages":[],"tools":[{"name":123,"input_schema":{"type":"object"}}]}`)

	output, _ := ConvertClaudeRequestToCodex("gpt-test", input, false)

	name := ojson.GetBytesNoCopy(output, "tools.0.name")
	if name.Type != ojson.String || name.String() != "123" {
		t.Fatalf("tools.0.name = %s, want string 123", name.Raw)
	}
}
