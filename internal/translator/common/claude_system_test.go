package common

import (
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestBuildClaudeStructuredOutputInstruction(t *testing.T) {
	t.Run("empty or invalid", func(t *testing.T) {
		if got := BuildClaudeStructuredOutputInstruction(ojson.Result{}); got != "" {
			t.Fatalf("expected empty instruction, got: %s", got)
		}
		if got := BuildClaudeStructuredOutputInstruction(ojson.Parse(`{"type":"text"}`)); got != "" {
			t.Fatalf("expected empty instruction for text, got: %s", got)
		}
	})

	t.Run("json_object", func(t *testing.T) {
		got := BuildClaudeStructuredOutputInstruction(ojson.Parse(`{"type":"json_object"}`))
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
		got := BuildClaudeStructuredOutputInstruction(ojson.Parse(input))
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
		got := BuildClaudeStructuredOutputInstruction(ojson.Parse(input))
		if !strings.Contains(got, "key") {
			t.Fatalf("expected key in instruction, got: %s", got)
		}
	})

	t.Run("json_schema without schema falls back to json_object instruction", func(t *testing.T) {
		input := `{"type": "json_schema"}`
		got := BuildClaudeStructuredOutputInstruction(ojson.Parse(input))
		if !strings.Contains(got, "JSON object") {
			t.Fatalf("expected JSON object fallback, got: %s", got)
		}
	})
}

func TestSystemReminderText(t *testing.T) {
	text := "Please call a tool now."
	got := SystemReminderText(text)
	want := "<system-reminder>\nPlease call a tool now.\n</system-reminder>"
	if got != want {
		t.Fatalf("SystemReminderText(%q) = %q, want %q", text, got, want)
	}
}

func TestBuildClaudeOutputFormat(t *testing.T) {
	t.Run("chat completions json_schema passes the schema unchanged", func(t *testing.T) {
		schema := `{"type":"object","properties":{"tags":{"type":"array","maxItems":3,"items":{"type":"string","pattern":"^[a-z]+$"}}},"required":["tags"],"additionalProperties":false}`
		got, ok := BuildClaudeOutputFormat(ojson.Parse(`{"type":"json_schema","json_schema":{"name":"tags","strict":true,"schema":` + schema + `}}`))
		if !ok || string(got) != `{"type":"json_schema","schema":`+schema+`}` {
			t.Fatalf("got %s, %v", got, ok)
		}
	})

	t.Run("responses json_schema passes the schema unchanged", func(t *testing.T) {
		schema := `{"type":"object","properties":{"facts":{"type":"array","items":{"type":"string"}}},"required":["facts"]}`
		got, ok := BuildClaudeOutputFormat(ojson.Parse(`{"type":"JSON_SCHEMA","name":"facts","strict":true,"schema":` + schema + `}`))
		if !ok || string(got) != `{"type":"json_schema","schema":`+schema+`}` {
			t.Fatalf("got %s, %v", got, ok)
		}
	})

	for name, format := range map[string]string{
		"missing":                              ``,
		"text":                                 `{"type":"text"}`,
		"json_object":                          `{"type":"json_object"}`,
		"json_schema without schema":           `{"type":"json_schema","json_schema":{"name":"empty"}}`,
		"json_schema with a non-object schema": `{"type":"json_schema","schema":"not a schema"}`,
	} {
		t.Run(name+" keeps the instruction path", func(t *testing.T) {
			if got, ok := BuildClaudeOutputFormat(ojson.Parse(format)); ok || got != nil {
				t.Fatalf("got %s, %v", got, ok)
			}
		})
	}
}
