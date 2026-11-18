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

func TestClaudeOutputFormatSchema(t *testing.T) {
	t.Run("chat completions json_schema returns schema unchanged", func(t *testing.T) {
		schema := `{"type":"object","properties":{"tags":{"type":"array","maxItems":3,"items":{"type":"string","pattern":"^[a-z]+$"}}},"required":["tags"],"additionalProperties":false}`
		got, ok := ClaudeOutputFormatSchema("claude-sonnet-5-5", ojson.Parse(`{"type":"json_schema","json_schema":{"name":"tags","strict":true,"schema":`+schema+`}}`))
		if !ok || got.Raw != schema {
			t.Fatalf("got %s, %v", got.Raw, ok)
		}
	})

	t.Run("responses json_schema returns schema unchanged", func(t *testing.T) {
		schema := `{ "required": ["facts"], "type": "object",  "properties": {"facts": {"type": "array", "items": {"type": "string"}}}}`
		got, ok := ClaudeOutputFormatSchema("claude-sonnet-5-5", ojson.Parse(`{"type":"JSON_SCHEMA","name":"facts","strict":true,"schema":`+schema+`}`))
		if !ok || got.Raw != schema {
			t.Fatalf("got %s, %v", got.Raw, ok)
		}
	})

	for name, format := range map[string]string{
		"missing":                        ``,
		"text":                           `{"type":"text"}`,
		"json_object":                    `{"type":"json_object"}`,
		"json_schema without schema":     `{"type":"json_schema","json_schema":{"name":"empty"}}`,
		"json_schema non-object schema":  `{"type":"json_schema","schema":"not a schema"}`,
		"json_schema nested null schema": `{"type":"json_schema","json_schema":{"schema":null}}`,
	} {
		t.Run(name+" falls back to instruction", func(t *testing.T) {
			if got, ok := ClaudeOutputFormatSchema("claude-sonnet-5-5", ojson.Parse(format)); ok || got.Exists() {
				t.Fatalf("got %s, %v", got.Raw, ok)
			}
		})
	}
}

func TestClaudeOutputFormatSchemaModels(t *testing.T) {
	format := ojson.Parse(`{"type":"json_schema","name":"facts","schema":{"type":"object","properties":{"facts":{"type":"string"}}}}`)
	for model, native := range map[string]bool{
		"claude-2.1":                          false,
		"claude-instant-1.2":                  false,
		"claude-3-5-haiku-20241022":           false,
		"claude-3.5-sonnet":                   false,
		"claude-3-7-sonnet-20250219":          false,
		"claude-3-7-sonnet-20250219-thinking": false,
		"claude-sonnet-4-20250514":            false,
		"claude-sonnet-4-0":                   false,
		"claude-sonnet-4":                     false,
		"claude-sonnet-4-latest":              false,
		"claude-opus-4-20250514":              false,
		"claude-opus-4-0":                     false,
		"claude-opus-4":                       false,
		"claude-opus-4-latest":                false,
		"claude-opus-4-1-20250805":            false,
		"claude-opus-4-1":                     false,
		"claude-opus-4-1-latest":              false,
		"anthropic/claude-sonnet-4":           false,
		"anthropic/claude-opus-4-1-20250805":  false,
		"Claude-Sonnet-4-20250514(high)":      false,
		"claude-opus-4-1[1m]":                 false,
		"claude-opus-4-1-thinking[1m]":        false,
		"claude-sonnet-4-5-20250929":          true,
		"claude-sonnet-4-5":                   true,
		"claude-sonnet-4-6":                   true,
		"claude-opus-4-5-20251101":            true,
		"claude-opus-4-6":                     true,
		"claude-opus-4-7":                     true,
		"claude-opus-4-8":                     true,
		"claude-haiku-4-5-20251001":           true,
		"claude-sonnet-5-5":                   true,
		"claude-opus-5-5(high)":               true,
		"claude-fable-5-2":                    true,
		"an-unknown-model":                    true,
	} {
		t.Run(model, func(t *testing.T) {
			if _, ok := ClaudeOutputFormatSchema(model, format); ok != native {
				t.Fatalf("ClaudeOutputFormatSchema(%q) = %v, want %v", model, ok, native)
			}
		})
	}
}

func TestBuildClaudeOutputFormatDescription(t *testing.T) {
	schema := `{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}`
	for name, format := range map[string]string{
		"chat completions": `{"type":"json_schema","json_schema":{"name":"recipe","description":"A dish for two.","schema":` + schema + `}}`,
		"responses":        `{"type":"json_schema","name":"recipe","description":"A dish for two.","schema":` + schema + `}`,
	} {
		t.Run(name+" keeps name and description without repeating schema", func(t *testing.T) {
			got := BuildClaudeOutputFormatDescription(ojson.Parse(format))
			if !strings.Contains(got, "Schema Name: recipe") || !strings.Contains(got, "Schema Description: A dish for two.") {
				t.Fatalf("got %q", got)
			}
			if strings.Contains(got, `"properties"`) || strings.Contains(got, "conforms strictly") {
				t.Fatalf("schema body repeated in description instruction: %q", got)
			}
		})
	}

	t.Run("no description yields empty instruction", func(t *testing.T) {
		if got := BuildClaudeOutputFormatDescription(ojson.Parse(`{"type":"json_schema","name":"recipe","schema":` + schema + `}`)); got != "" {
			t.Fatalf("got %q", got)
		}
	})
}
