package claude

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestIssue7967UsesNativeToolResultResponseKey(t *testing.T) {
	tests := []struct {
		name    string
		isError bool
		key     string
	}{
		{name: "success", key: "output"},
		{name: "error", isError: true, key: "error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errorField := ""
			if tt.isError {
				errorField = `,"is_error":true`
			}
			input := []byte(`{"messages":[
                {"role":"assistant","content":[{"type":"tool_use","id":"call-1","name":"Read","input":{}}]},
                {"role":"user","content":[{"type":"tool_result","tool_use_id":"call-1","content":"tool text"` + errorField + `}]}
            ]}`)

			out, err := ConvertClaudeRequestToAntigravity("claude-sonnet-4-5", input, false)
			if err != nil {
				t.Fatal(err)
			}
			response := ojson.GetBytesNoCopy(out, "request.contents.1.parts.0.functionResponse.response")
			if got := response.Get(tt.key).String(); got != "tool text" {
				t.Fatalf("response.%s = %q, want tool text; output=%s", tt.key, got, out)
			}
			if response.Get("result").Exists() {
				t.Fatalf("response.result must not be emitted; output=%s", out)
			}
		})
	}
}
