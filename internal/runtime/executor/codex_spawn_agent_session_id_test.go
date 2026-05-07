package executor

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestNormalizeCodexSpawnAgentSessionIDToolSchema(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gpt-5.5",
		"tools": [
			{
				"type": "function",
				"name": "diagnostics",
				"parameters": {"type":"object","properties":{}}
			},
			{
				"type": "function",
				"name": "spawn_agent",
				"parameters": {
					"type": "object",
					"required": ["label", "message", "session_id"],
					"properties": {
						"label": {"type": "string"},
						"message": {"type": "string"},
						"session_id": {
							"type": "string",
							"default": null,
							"nullable": true,
							"description": "Session ID of an existing agent session."
						}
					}
				}
			}
		]
	}`)

	output := normalizeCodexSpawnAgentSessionIDToolSchema(inputJSON)

	if got := gjson.GetBytes(output, "tools.1.parameters.properties.session_id.type.0").String(); got != "string" {
		t.Fatalf("session_id type[0] = %q, want string: %s", got, string(output))
	}
	if got := gjson.GetBytes(output, "tools.1.parameters.properties.session_id.type.1").String(); got != "null" {
		t.Fatalf("session_id type[1] = %q, want null: %s", got, string(output))
	}
	if gjson.GetBytes(output, "tools.1.parameters.properties.session_id.nullable").Exists() {
		t.Fatalf("session_id nullable should be removed: %s", string(output))
	}
	for _, required := range gjson.GetBytes(output, "tools.1.parameters.required").Array() {
		if required.String() == "session_id" {
			t.Fatalf("session_id should not be required: %s", string(output))
		}
	}
}
