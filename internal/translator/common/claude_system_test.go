package common

import (
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestBuildClaudeStructuredOutputInstruction(t *testing.T) {
	t.Run("empty or invalid", func(t *testing.T) {
		if got := BuildClaudeStructuredOutputInstruction(gjson.Result{}); got != "" {
			t.Fatalf("expected empty instruction, got: %s", got)
		}
		if got := BuildClaudeStructuredOutputInstruction(gjson.Parse(`{"type":"text"}`)); got != "" {
			t.Fatalf("expected empty instruction for text, got: %s", got)
		}
	})

	t.Run("json_object", func(t *testing.T) {
		got := BuildClaudeStructuredOutputInstruction(gjson.Parse(`{"type":"json_object"}`))
		if !strings.Contains(got, "JSON object") {
			t.Fatalf("expected JSON object instruction, got: %s", got)
		}
	})

	t.Run("json_schema with inner schema", func(t *testing.T) {
		input := `{
			"type": "json_schema",
			"json_schema": {
				"name": "extracted_facts",
				"description": "Facts from text",
				"schema": {
					"type": "object",
					"properties": {"facts": {"type": "array"}}
				}
			}
		}`
		got := BuildClaudeStructuredOutputInstruction(gjson.Parse(input))
		if !strings.Contains(got, "extracted_facts") {
			t.Fatalf("expected schema name in instruction, got: %s", got)
		}
		if !strings.Contains(got, "Facts from text") {
			t.Fatalf("expected description in instruction, got: %s", got)
		}
		if !strings.Contains(got, `"properties"`) {
			t.Fatalf("expected schema body in instruction, got: %s", got)
		}
	})

	t.Run("json_schema flat schema", func(t *testing.T) {
		input := `{
			"type": "json_schema",
			"schema": {
				"type": "object",
				"properties": {"key": {"type": "string"}}
			}
		}`
		got := BuildClaudeStructuredOutputInstruction(gjson.Parse(input))
		if !strings.Contains(got, "key") {
			t.Fatalf("expected key in instruction, got: %s", got)
		}
	})

	t.Run("json_schema without schema falls back to json_object instruction", func(t *testing.T) {
		input := `{"type": "json_schema"}`
		got := BuildClaudeStructuredOutputInstruction(gjson.Parse(input))
		if !strings.Contains(got, "JSON object") {
			t.Fatalf("expected JSON object fallback, got: %s", got)
		}
	})
}
