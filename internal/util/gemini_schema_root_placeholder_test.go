package util

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestRemovePlaceholderFields_RootLevelUnderscore(t *testing.T) {
	t.Run("only underscore property and required", func(t *testing.T) {
		input := `{"type":"object","properties":{"_":{"type":"boolean","description":"placeholder"}},"required":["_"]}`
		got := removePlaceholderFields(input)
		doc := ojson.NewDocString(got)

		if doc.Get("properties._").Exists() {
			t.Fatalf("expected root-level properties._ to be removed, got: %s", got)
		}
		if doc.Get("required").Exists() {
			t.Fatalf("expected empty required array to be removed, got: %s", got)
		}
	})

	t.Run("underscore alongside real property preserves real required", func(t *testing.T) {
		input := `{"type":"object","properties":{"_":{"type":"boolean"},"query":{"type":"string"}},"required":["_","query"]}`
		got := removePlaceholderFields(input)
		doc := ojson.NewDocString(got)

		if doc.Get("properties._").Exists() {
			t.Fatalf("expected root-level properties._ to be removed, got: %s", got)
		}
		if !doc.Get("properties.query").Exists() {
			t.Fatalf("expected root-level properties.query to be preserved, got: %s", got)
		}
		req := doc.Get("required")
		if !req.IsArray() || len(req.Array()) != 1 || req.Array()[0].String() != "query" {
			t.Fatalf("expected required to be [\"query\"], got: %s", got)
		}
	})
}

func TestRemovePlaceholderFields_RootLevelReason(t *testing.T) {
	t.Run("placeholder-only reason property and required", func(t *testing.T) {
		input := `{"type":"object","properties":{"reason":{"type":"string","description":"` + placeholderReasonDescription + `"}},"required":["reason"]}`
		got := removePlaceholderFields(input)
		doc := ojson.NewDocString(got)

		if doc.Get("properties.reason").Exists() {
			t.Fatalf("expected root-level placeholder properties.reason to be removed, got: %s", got)
		}
		if doc.Get("required").Exists() {
			t.Fatalf("expected empty required array to be removed, got: %s", got)
		}
	})

	t.Run("non-placeholder reason property is preserved", func(t *testing.T) {
		input := `{"type":"object","properties":{"reason":{"type":"string","description":"User-supplied cancellation reason"}},"required":["reason"]}`
		got := removePlaceholderFields(input)
		doc := ojson.NewDocString(got)

		if !doc.Get("properties.reason").Exists() {
			t.Fatalf("expected non-placeholder properties.reason to be preserved, got: %s", got)
		}
		req := doc.Get("required")
		if !req.IsArray() || len(req.Array()) != 1 || req.Array()[0].String() != "reason" {
			t.Fatalf("expected required to preserve \"reason\", got: %s", got)
		}
	})

	t.Run("reason alongside another property is preserved", func(t *testing.T) {
		input := `{"type":"object","properties":{"reason":{"type":"string","description":"` + placeholderReasonDescription + `"},"id":{"type":"string"}},"required":["reason","id"]}`
		got := removePlaceholderFields(input)
		doc := ojson.NewDocString(got)

		if !doc.Get("properties.reason").Exists() {
			t.Fatalf("expected properties.reason alongside other properties to be preserved, got: %s", got)
		}
	})
}
