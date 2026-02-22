package claude

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestSanitizeClaudeRequest_RemovesPlaceholderReasonOnlySchema(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{
				"name":"EnterPlanMode",
				"description":"Switch to plan mode",
				"input_schema":{
					"type":"object",
					"properties":{
						"reason":{
							"type":"string",
							"description":"Brief explanation of why you are calling this tool"
						}
					},
					"required":["reason"]
				}
			}
		]
	}`)

	sanitized := sanitizeClaudeRequest(raw)

	if gjson.GetBytes(sanitized, "tools.0.input_schema.properties.reason").Exists() {
		t.Fatalf("expected placeholder reason property to be removed, got: %s", string(sanitized))
	}
	if gjson.GetBytes(sanitized, "tools.0.input_schema.required").Exists() {
		t.Fatalf("expected required to be removed after stripping placeholder-only schema, got: %s", string(sanitized))
	}
}

func TestSanitizeClaudeRequest_PreservesNonPlaceholderReasonSchema(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{
				"name":"RealReasonTool",
				"input_schema":{
					"type":"object",
					"properties":{
						"reason":{
							"type":"string",
							"description":"Business reason"
						}
					},
					"required":["reason"]
				}
			}
		]
	}`)

	sanitized := sanitizeClaudeRequest(raw)

	if !gjson.GetBytes(sanitized, "tools.0.input_schema.properties.reason").Exists() {
		t.Fatalf("expected non-placeholder reason property to be preserved, got: %s", string(sanitized))
	}
	if gjson.GetBytes(sanitized, "tools.0.input_schema.required.0").String() != "reason" {
		t.Fatalf("expected required reason to be preserved, got: %s", string(sanitized))
	}
}
