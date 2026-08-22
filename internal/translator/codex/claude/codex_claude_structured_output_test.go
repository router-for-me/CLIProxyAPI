package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertClaudeRequestToCodexPreservesStructuredOutput(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":"Return JSON"}],"output_config":{"format":{"type":"json_schema","schema":{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"]}}}}`)
	out := ConvertClaudeRequestToCodex("gpt-5.4", payload, false)
	if got := gjson.GetBytes(out, "text.format.type").String(); got != "json_schema" {
		t.Fatalf("text.format.type = %q, want json_schema; payload=%s", got, out)
	}
	if got := gjson.GetBytes(out, "text.format.name").String(); got == "" {
		t.Fatalf("text.format.name is empty; payload=%s", out)
	}
	if !gjson.GetBytes(out, "text.format.strict").Bool() {
		t.Fatalf("text.format.strict is false; payload=%s", out)
	}
	if got := gjson.GetBytes(out, "text.format.schema.required.0").String(); got != "answer" {
		t.Fatalf("structured output schema was not preserved; payload=%s", out)
	}
}
