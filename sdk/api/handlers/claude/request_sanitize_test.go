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

func TestSanitizeClaudeRequest_RemovesPlaceholderReasonWithOtherProperties(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{
				"name":"EnterPlanMode",
				"input_schema":{
					"type":"object",
					"properties":{
						"reason":{
							"type":"string",
							"description":"Brief explanation of why you are calling this tool"
						},
						"_":{
							"type":"string"
						},
						"mode":{
							"type":"string"
						}
					},
					"required":["reason","_","mode"]
				}
			}
		]
	}`)

	sanitized := sanitizeClaudeRequest(raw)

	if gjson.GetBytes(sanitized, "tools.0.input_schema.properties.reason").Exists() {
		t.Fatalf("expected placeholder reason property to be removed, got: %s", string(sanitized))
	}
	if gjson.GetBytes(sanitized, "tools.0.input_schema.properties._").Exists() {
		t.Fatalf("expected placeholder underscore property to be removed, got: %s", string(sanitized))
	}
	if got := gjson.GetBytes(sanitized, "tools.0.input_schema.required.#").Int(); got != 1 {
		t.Fatalf("expected only one required entry after stripping placeholders, got %d in %s", got, string(sanitized))
	}
	if got := gjson.GetBytes(sanitized, "tools.0.input_schema.required.0").String(); got != "mode" {
		t.Fatalf("expected remaining required field to be mode, got %q in %s", got, string(sanitized))
	}
}

func TestSanitizeClaudeRequest_RemovesPlaceholderReasonFromCustomInputSchema(t *testing.T) {
	raw := []byte(`{
		"model":"claude-test",
		"messages":[{"role":"user","content":"hello"}],
		"tools":[
			{
				"name":"CustomSchemaTool",
				"custom":{
					"input_schema":{
						"type":"object",
						"properties":{
							"reason":{
								"type":"string",
								"description":"Brief explanation of why you are calling this tool"
							},
							"mode":{"type":"string"}
						},
						"required":["reason","mode"]
					}
				}
			}
		]
	}`)

	sanitized := sanitizeClaudeRequest(raw)

	if gjson.GetBytes(sanitized, "tools.0.custom.input_schema.properties.reason").Exists() {
		t.Fatalf("expected placeholder reason in custom.input_schema to be removed, got: %s", string(sanitized))
	}
	if got := gjson.GetBytes(sanitized, "tools.0.custom.input_schema.required.#").Int(); got != 1 {
		t.Fatalf("expected one required field to remain, got %d in %s", got, string(sanitized))
	}
	if got := gjson.GetBytes(sanitized, "tools.0.custom.input_schema.required.0").String(); got != "mode" {
		t.Fatalf("expected remaining required field to be mode, got %q in %s", got, string(sanitized))
	}
}
