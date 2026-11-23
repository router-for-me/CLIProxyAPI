package executor

import (
	"testing"

	"github.com/MoeForge/moego/ojson"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestAppendAntigravityToolContinuationUserTurnDoc(t *testing.T) {
	t.Parallel()

	t.Run("appends continuation turn when trailing content has functionResponse for openai", func(t *testing.T) {
		raw := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"do action"}]},{"role":"model","parts":[{"functionCall":{"name":"cmd"}}]},{"role":"user","parts":[{"functionResponse":{"name":"cmd","response":{"output":"done"}}}]}]}}`)
		doc := ojson.NewDoc(raw)

		appended := appendAntigravityToolContinuationUserTurnDoc("gemini-3.8-flash-high", sdktranslator.FormatOpenAI, doc)
		if !appended {
			t.Fatalf("expected continuation turn to be appended")
		}

		contents := doc.Get("request.contents").Array()
		if len(contents) != 4 {
			t.Fatalf("expected 4 contents, got %d", len(contents))
		}
		last := contents[3]
		if last.Get("role").String() != "user" {
			t.Fatalf("expected last turn role to be user, got %q", last.Get("role").String())
		}
		text := last.Get("parts.0.text").String()
		if text != "Review the tool result and proceed." {
			t.Fatalf("unexpected continuation text: %q", text)
		}
	})

	t.Run("skips when model is claude", func(t *testing.T) {
		raw := []byte(`{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"cmd"}}]}]}}`)
		doc := ojson.NewDoc(raw)

		appended := appendAntigravityToolContinuationUserTurnDoc("claude-sonnet-4-6", sdktranslator.FormatOpenAI, doc)
		if appended {
			t.Fatalf("expected claude to be skipped")
		}
		if len(doc.Get("request.contents").Array()) != 1 {
			t.Fatalf("contents should remain untouched")
		}
	})

	t.Run("skips when format is not openai", func(t *testing.T) {
		raw := []byte(`{"request":{"contents":[{"role":"user","parts":[{"functionResponse":{"name":"cmd"}}]}]}}`)
		doc := ojson.NewDoc(raw)

		appended := appendAntigravityToolContinuationUserTurnDoc("gemini-3.8-flash-high", sdktranslator.FormatClaude, doc)
		if appended {
			t.Fatalf("expected non-openai format to be skipped")
		}
	})

	t.Run("skips when trailing content does not contain functionResponse", func(t *testing.T) {
		raw := []byte(`{"request":{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}}`)
		doc := ojson.NewDoc(raw)

		appended := appendAntigravityToolContinuationUserTurnDoc("gemini-3.8-flash-high", sdktranslator.FormatOpenAI, doc)
		if appended {
			t.Fatalf("expected content without functionResponse to be skipped")
		}
	})
}

