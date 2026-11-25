package helps

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestSanitizeGeminiInteractionsUnsupportedInputIDs_NestedContentParts(t *testing.T) {
	bareTinkSig := testVertexBareTinkInteractionsSignature()

	t.Run("preserves nested function_call id and strips unsupported sibling ids", func(t *testing.T) {
		body := []byte(`{"input":[{"role":"model","content":[{"type":"function_call","id":"fc_1","name":"get_weather","arguments":{"city":"Paris"}},{"type":"text","id":"txt_1","text":"checking"}]}]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)

		if fcID := ojson.GetBytes(got, "input.0.content.0.id").String(); fcID != "fc_1" {
			t.Fatalf("nested function_call id must be preserved, got %q (body: %s)", fcID, got)
		}
		if ojson.GetBytes(got, "input.0.content.1.id").Exists() {
			t.Fatalf("nested text part id must be stripped: %s", got)
		}
	})

	t.Run("promotes nested function_call call_id when id is missing or empty", func(t *testing.T) {
		body := []byte(`{"input":[{"role":"model","content":[{"type":"function_call","id":"","call_id":"call_nested_1","name":"lookup"},{"type":"function_call","id":"   ","call_id":"call_nested_2","name":"lookup2"}]}]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)

		if fcID := ojson.GetBytes(got, "input.0.content.0.id").String(); fcID != "call_nested_1" {
			t.Fatalf("expected empty id to promote call_id, got %q (body: %s)", fcID, got)
		}
		if ojson.GetBytes(got, "input.0.content.0.call_id").Exists() {
			t.Fatalf("nested function_call call_id must be removed: %s", got)
		}
		if fcID := ojson.GetBytes(got, "input.0.content.1.id").String(); fcID != "call_nested_2" {
			t.Fatalf("expected whitespace id to promote call_id, got %q (body: %s)", fcID, got)
		}
		if ojson.GetBytes(got, "input.0.content.1.call_id").Exists() {
			t.Fatalf("nested function_call call_id must be removed: %s", got)
		}
	})

	t.Run("validates and filters nested thought parts including Vertex bare-Tink", func(t *testing.T) {
		body := []byte(`{"input":[{"role":"model","content":[{"type":"thought","signature":"` + bareTinkSig + `","id":"th_1","text":"internal"},{"type":"thought","signature":"not-valid-base64!!!"},{"type":"text","text":"answer"}]}]}`)

		public := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
		if ojson.GetBytes(public, `input.0.content.#(type=="thought")`).Exists() {
			t.Fatalf("public Interactions endpoint must drop nested Vertex bare-Tink and invalid thoughts: %s", public)
		}
		if text := ojson.GetBytes(public, "input.0.content.0.text").String(); text != "answer" {
			t.Fatalf("expected remaining text part after dropping thoughts, got %q (body: %s)", text, public)
		}

		vertex := SanitizeGeminiInteractionsUnsupportedInputIDs(body, true)
		if count := len(ojson.GetBytes(vertex, `input.0.content.#(type=="thought")#`).Array()); count != 1 {
			t.Fatalf("Vertex Interactions endpoint must keep only the valid bare-Tink thought, got count=%d (body: %s)", count, vertex)
		}
		if gotSig := ojson.GetBytes(vertex, "input.0.content.0.signature").String(); gotSig != bareTinkSig {
			t.Fatalf("expected bare-Tink signature to be preserved on Vertex, got %q", gotSig)
		}
		if ojson.GetBytes(vertex, "input.0.content.0.id").Exists() || ojson.GetBytes(vertex, "input.0.content.0.text").Exists() {
			t.Fatalf("unsupported fields on kept nested thought must be stripped: %s", vertex)
		}
	})

	t.Run("promotes top-level function_call call_id when id is empty", func(t *testing.T) {
		body := []byte(`{"input":[{"type":"function_call","id":"","call_id":"call_top_1","name":"run"}]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)

		if fcID := ojson.GetBytes(got, "input.0.id").String(); fcID != "call_top_1" {
			t.Fatalf("expected empty top-level id to promote call_id, got %q (body: %s)", fcID, got)
		}
		if ojson.GetBytes(got, "input.0.call_id").Exists() {
			t.Fatalf("top-level function_call call_id must be removed: %s", got)
		}
	})
}
