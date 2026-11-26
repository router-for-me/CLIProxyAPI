package util

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestRemovePlaceholderFields_RootLevelUnderscore(t *testing.T) {
	t.Run("removes root-level underscore placeholder and empty required", func(t *testing.T) {
		input := `{"type":"object","properties":{"_":{"type":"boolean"}},"required":["_"]}`
		for name, fn := range map[string]func(string) string{
			"removePlaceholderFields":            removePlaceholderFields,
			"CleanJSONSchemaForGemini":           CleanJSONSchemaForGemini,
			"CleanJSONSchemaForGeminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
		} {
			t.Run(name, func(t *testing.T) {
				got := fn(input)
				parsed := ojson.Parse(got)
				if parsed.Get("properties._").Exists() {
					t.Fatalf("expected root-level properties._ to be removed, got: %s", got)
				}
				if parsed.Get("required").Exists() {
					t.Fatalf("expected empty root-level required to be removed, got: %s", got)
				}
			})
		}
	})

	t.Run("removes root-level underscore while preserving real properties and required entries", func(t *testing.T) {
		input := `{"type":"object","properties":{"_":{"type":"boolean"},"query":{"type":"string"}},"required":["_","query"]}`
		for name, fn := range map[string]func(string) string{
			"removePlaceholderFields":            removePlaceholderFields,
			"CleanJSONSchemaForGemini":           CleanJSONSchemaForGemini,
			"CleanJSONSchemaForGeminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
		} {
			t.Run(name, func(t *testing.T) {
				got := fn(input)
				parsed := ojson.Parse(got)
				if parsed.Get("properties._").Exists() {
					t.Fatalf("expected root-level properties._ to be removed, got: %s", got)
				}
				if !parsed.Get("properties.query").Exists() {
					t.Fatalf("expected real property query to be preserved, got: %s", got)
				}
				req := parsed.Get("required").Array()
				if len(req) != 1 || req[0].String() != "query" {
					t.Fatalf(`expected required to be ["query"], got: %s`, got)
				}
			})
		}
	})
}

func TestRemovePlaceholderFields_RootLevelReason(t *testing.T) {
	t.Run("removes root-level placeholder-only reason and empty required", func(t *testing.T) {
		input := `{"type":"object","properties":{"reason":{"type":"string","description":"` + placeholderReasonDescription + `"}},"required":["reason"]}`
		for name, fn := range map[string]func(string) string{
			"removePlaceholderFields":            removePlaceholderFields,
			"CleanJSONSchemaForGemini":           CleanJSONSchemaForGemini,
			"CleanJSONSchemaForGeminiJSONSchema": CleanJSONSchemaForGeminiJSONSchema,
		} {
			t.Run(name, func(t *testing.T) {
				got := fn(input)
				parsed := ojson.Parse(got)
				if parsed.Get("properties.reason").Exists() {
					t.Fatalf("expected root-level placeholder properties.reason to be removed, got: %s", got)
				}
				if parsed.Get("required").Exists() {
					t.Fatalf("expected empty root-level required to be removed, got: %s", got)
				}
			})
		}
	})

	t.Run("preserves root-level reason with custom description", func(t *testing.T) {
		input := `{"type":"object","properties":{"reason":{"type":"string","description":"User-supplied cancellation reason"}},"required":["reason"]}`
		got := CleanJSONSchemaForGeminiJSONSchema(input)
		parsed := ojson.Parse(got)
		if !parsed.Get("properties.reason").Exists() {
			t.Fatalf("expected non-placeholder root-level properties.reason to be preserved, got: %s", got)
		}
		req := parsed.Get("required").Array()
		if len(req) != 1 || req[0].String() != "reason" {
			t.Fatalf(`expected required to keep "reason", got: %s`, got)
		}
	})

	t.Run("preserves root-level reason when object has multiple properties", func(t *testing.T) {
		input := `{"type":"object","properties":{"reason":{"type":"string","description":"` + placeholderReasonDescription + `"},"target":{"type":"string"}},"required":["reason","target"]}`
		got := CleanJSONSchemaForGeminiJSONSchema(input)
		parsed := ojson.Parse(got)
		if !parsed.Get("properties.reason").Exists() {
			t.Fatalf("expected root-level properties.reason in multi-property object to be preserved, got: %s", got)
		}
		req := parsed.Get("required").Array()
		if len(req) != 2 {
			t.Fatalf("expected both required entries to be preserved, got: %s", got)
		}
	})

	t.Run("still removes nested placeholder properties", func(t *testing.T) {
		input := `{"type":"object","properties":{"nested":{"type":"object","properties":{"_":{"type":"boolean"},"id":{"type":"string"}},"required":["_","id"]},"empty":{"type":"object","properties":{"reason":{"type":"string","description":"` + placeholderReasonDescription + `"}},"required":["reason"]}}}`
		got := CleanJSONSchemaForGeminiJSONSchema(input)
		parsed := ojson.Parse(got)
		if parsed.Get("properties.nested.properties._").Exists() {
			t.Fatalf("expected nested properties._ to be removed, got: %s", got)
		}
		nestedReq := parsed.Get("properties.nested.required").Array()
		if len(nestedReq) != 1 || nestedReq[0].String() != "id" {
			t.Fatalf(`expected nested required to be ["id"], got: %s`, got)
		}
		if parsed.Get("properties.empty.properties.reason").Exists() {
			t.Fatalf("expected nested placeholder properties.reason to be removed, got: %s", got)
		}
		if parsed.Get("properties.empty.required").Exists() {
			t.Fatalf("expected nested empty required to be removed, got: %s", got)
		}
	})
}
